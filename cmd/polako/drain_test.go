package main

// The drain loop is mostly GitHub bookkeeping, and the behaviour worth proving
// — one dead issue must not end the session — only shows up across several
// issues. So the test binary impersonates `gh` the same way it already
// impersonates `claude`: no network, no gh installed, no shell scripts.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeGhEnv points the fake gh at the JSON file holding its pretend
// repository. Every gh call is a separate process, so the state has to live
// somewhere both the drain's writes and its reads can see.
const fakeGhEnv = "POLAKO_FAKE_GH"

// ghSubcommands are the first arguments that mean "this invocation is gh".
// The test process exports both fake-CLI variables at once and children
// inherit them, so argv is what tells the two impersonations apart.
var ghSubcommands = []string{"repo", "issue", "pr", "label", "api"}

// ghState is the whole of a pretend repository.
type ghState struct {
	Repo   string                `json:"repo"`
	Issues map[string]*fakeIssue `json:"issues"`
	PRs    map[string]*fakePR    `json:"prs"`    // keyed by head branch
	Labels []string              `json:"labels"` // labels the repo has defined

	// FailReads is a network that has not come back yet after the host woke:
	// the next N calls of a kind ("issue list", "pr list") fail the way gh does
	// when it cannot reach GitHub, and then it answers normally again. Keyed by
	// the two-word call so a test can be flaky about exactly one lookup.
	FailReads map[string]int `json:"fail_reads"`

	// ClaudeRuns counts the invocations a fake CLI has made, for the modes whose
	// answer depends on how far the supervisor has got. See countClaudeRun.
	ClaudeRuns int `json:"claude_runs"`
}

type fakeIssue struct {
	Open   bool     `json:"open"`
	Labels []string `json:"labels"`
	// Comments is how many comments the thread has; they carry ids 1..Comments
	// in the order they were written, the way GitHub hands them out. Bots holds
	// the ids of the ones a GitHub App wrote rather than a person.
	Comments int   `json:"comments"`
	Bots     []int `json:"bots"`
	// CommentedAt stamps every comment on the thread. Only the newest one's
	// date is ever read — `status` measures how long a thread has been quiet by
	// it — so one date says everything a per-comment one would. Empty stands
	// for a thread whose dates GitHub did not report.
	CommentedAt string `json:"commented_at"`

	// ReplyOnRead is a human answering a question while the supervisor polls:
	// their comment appears on the Nth read of the thread from now. Counting
	// reads rather than wall clock keeps the test deterministic — the drain
	// unblocks because the thread moved, never because a timer went off.
	ReplyOnRead int `json:"reply_on_read"`

	// BotOnRead is the same for the comments this issue exists to ignore: CI, a
	// linked-PR notice, a stale-bot nudge. A bot comment lands on the Nth read
	// from now, and the wait must not end because of it.
	BotOnRead int `json:"bot_on_read"`

	// CloseOnList is a human dealing with the issue themselves while the
	// supervisor is off working another one: it disappears from the Nth
	// `issue list` from now. Counted the same way and for the same reason.
	CloseOnList int `json:"close_on_list"`
}

type fakePR struct {
	Number    int    `json:"number"`
	State     string `json:"state"`
	Mergeable string `json:"mergeable"`

	// Head is the commit `pr view` reports, and Checks the conclusions of its
	// rollup — "PENDING" for one that has not finished yet. A remediation run
	// that pushes moves Head and turns Checks green; see the "fixci" fake CLI.
	Head   string   `json:"head"`
	Checks []string `json:"checks"`

	// The review half of `pr view`: the latest verdict per reviewer, and when
	// the newest commit on the branch was made. Timestamps are literals rather
	// than anything derived from the clock, so a test states outright whether
	// the branch moved after the review — which is the whole of what the
	// supervisor decides on. See the "fixreview" fake CLI for a run that pushes.
	Reviews     []fakeReview `json:"reviews"`
	CommittedAt string       `json:"committed_at"`

	// MergeOnRead is a human merging the PR while the supervisor polls: it
	// reports MERGED on the Nth `pr view` from now. Counted in reads rather
	// than wall clock for the same reason as ReplyOnRead.
	MergeOnRead int `json:"merge_on_read"`
}

// fakeReview is one entry of `pr view --json reviews`. Author is optional:
// the tests here have a single reviewer, and the reduction to one verdict per
// reviewer is exercised by name in main_test.go.
type fakeReview struct {
	Author      string `json:"author"`
	State       string `json:"state"`
	SubmittedAt string `json:"submitted_at"`
}

// apiIssue picks the issue number out of the one REST path the drain asks for,
// repos/{owner}/{repo}/issues/N/comments?per_page=100.
func apiIssue(path string) string {
	_, rest, ok := strings.Cut(path, "/issues/")
	if !ok {
		return ""
	}
	n, _, _ := strings.Cut(rest, "/")
	return n
}

func readGhState(path string) (*ghState, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var st ghState
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

func writeGhState(path string, st *ghState) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

// fakeGhLogEnv names a file every gh invocation is appended to, one argv per
// line. It is how a test proves which calls a path made, which the state
// afterwards cannot: a mutation gh refused leaves the state untouched too, and
// so does one that wrote back exactly what was already there.
const fakeGhLogEnv = "POLAKO_FAKE_GH_LOG"

// fakeGh answers one gh invocation and persists anything it changed.
func fakeGh(path string, args []string) int {
	if dest := os.Getenv(fakeGhLogEnv); dest != "" {
		if f, err := os.OpenFile(dest, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			fmt.Fprintln(f, strings.Join(args, " "))
			f.Close()
		}
	}
	st, err := readGhState(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fake gh: %v\n", err)
		return 1
	}
	out, changed, code := answerGh(st, args)
	if changed {
		if err := writeGhState(path, st); err != nil {
			fmt.Fprintf(os.Stderr, "fake gh: %v\n", err)
			return 1
		}
	}
	fmt.Fprint(os.Stdout, out)
	return code
}

// answerGh is the fake's whole repertoire: exactly the calls the supervisor
// makes. Anything else fails loudly, so a new gh call added to the drain shows
// up as a named failure rather than an empty answer.
func answerGh(st *ghState, args []string) (out string, changed bool, code int) {
	at := func(i int) string {
		if i < len(args) {
			return args[i]
		}
		return ""
	}
	flagVal := func(name string) string {
		if i := slices.Index(args, name); i >= 0 && i+1 < len(args) {
			return args[i+1]
		}
		return ""
	}
	issue := func() *fakeIssue { return st.Issues[at(2)] }

	call := at(0) + " " + at(1)
	// `gh api` names its target in a URL path, so the second word is no use as a
	// call name. The drain makes exactly one api call; give it a readable name so
	// a FailReads entry can be flaky about it like any other.
	if at(0) == "api" {
		call = "api comments"
	}
	// Ahead of everything, so a test can be flaky about a call whatever it would
	// otherwise have answered. The countdown has to be persisted even though
	// nothing else changed, or every invocation would start it over.
	if n := st.FailReads[call]; n > 0 {
		st.FailReads[call] = n - 1
		fmt.Fprintf(os.Stderr, "could not connect to api.github.com\n")
		return "", true, 1
	}

	switch call {
	case "repo view":
		return st.Repo + "\n", false, 0

	case "issue list":
		out, changed := listIssues(st, flagVal("--label"))
		return out, changed, 0

	case "issue view":
		is := issue()
		if is == nil {
			fmt.Fprintf(os.Stderr, "no issue #%s\n", at(2))
			return "", false, 1
		}
		if strings.Contains(flagVal("--json"), "labels") {
			var labels []string
			for _, l := range is.Labels {
				labels = append(labels, fmt.Sprintf(`{"name":%q}`, l))
			}
			return `{"labels":[` + strings.Join(labels, ",") + `]}`, false, 0
		}
		state := "CLOSED"
		if is.Open {
			state = "OPEN"
		}
		return `{"state":"` + state + `"}`, false, 0

	case "api comments":
		is := st.Issues[apiIssue(at(1))]
		if is == nil {
			fmt.Fprintf(os.Stderr, "no issue for %s\n", at(1))
			return "", false, 1
		}
		// Both countdowns run on every read, and both have to be persisted even
		// on the reads before they fire, or each read would start them over.
		counting := is.ReplyOnRead > 0 || is.BotOnRead > 0
		if is.ReplyOnRead > 0 {
			is.ReplyOnRead--
			if is.ReplyOnRead == 0 {
				is.Comments++ // the human's reply lands on this read
			}
		}
		if is.BotOnRead > 0 {
			is.BotOnRead--
			if is.BotOnRead == 0 {
				is.Comments++
				is.Bots = append(is.Bots, is.Comments)
			}
		}
		var comments []string
		for id := 1; id <= is.Comments; id++ {
			author := "User"
			if slices.Contains(is.Bots, id) {
				author = "Bot"
			}
			comments = append(comments, fmt.Sprintf(`{"id":%d,"user":{"type":%q},"created_at":%q}`,
				id, author, is.CommentedAt))
		}
		return "[" + strings.Join(comments, ",") + "]", counting, 0

	case "issue edit":
		is := issue()
		if is == nil {
			return "", false, 1
		}
		if label := flagVal("--remove-label"); label != "" {
			is.Labels = slices.DeleteFunc(is.Labels, func(l string) bool { return l == label })
			return "", true, 0
		}
		label := flagVal("--add-label")
		if !slices.Contains(st.Labels, label) {
			// What GitHub does with a label the repository never defined.
			fmt.Fprintf(os.Stderr, "could not add label: '%s' not found\n", label)
			return "", false, 1
		}
		is.Labels = append(is.Labels, label)
		return "", true, 0

	case "issue comment":
		is := issue()
		if is == nil {
			return "", false, 1
		}
		is.Comments++
		return "", true, 0

	case "issue close":
		is := issue()
		if is == nil {
			return "", false, 1
		}
		is.Open = false
		is.Comments++
		return "", true, 0

	case "label create":
		if slices.Contains(st.Labels, at(2)) {
			fmt.Fprintf(os.Stderr, "label already exists: %s\n", at(2))
			return "", false, 1
		}
		st.Labels = append(st.Labels, at(2))
		return "", true, 0

	case "pr list":
		// With --head, the branch lookup the drain makes. Without one, every PR
		// in the requested state — which is how `status` finds the open PRs on
		// issue branches in a single call.
		if head := flagVal("--head"); head != "" {
			pr, ok := st.PRs[head]
			if !ok {
				return "[]", false, 0
			}
			return "[" + prListJSON(head, pr) + "]", false, 0
		}
		var rows []string
		for _, branch := range slices.Sorted(maps.Keys(st.PRs)) {
			if pr := st.PRs[branch]; flagVal("--state") == "" ||
				strings.EqualFold(flagVal("--state"), "all") ||
				strings.EqualFold(flagVal("--state"), pr.State) {
				rows = append(rows, prListJSON(branch, pr))
			}
		}
		return "[" + strings.Join(rows, ",") + "]", false, 0

	case "pr view":
		for _, pr := range st.PRs {
			if strconv.Itoa(pr.Number) != at(2) {
				continue
			}
			merging := pr.MergeOnRead > 0
			if merging {
				pr.MergeOnRead--
				if pr.MergeOnRead == 0 {
					pr.State = "MERGED" // the human merges it on this read
				}
			}
			// The countdown has to be persisted even on the reads before the
			// merge, or every read would start it over.
			return fmt.Sprintf(
					`{"state":%q,"mergeable":%q,"headRefOid":%q,"statusCheckRollup":%s,`+
						`"reviewDecision":"","reviews":%s,"commits":%s}`,
					pr.State, pr.Mergeable, pr.Head, rollupJSON(pr.Checks),
					reviewsJSON(pr.Reviews), commitsJSON(pr.CommittedAt)),
				merging, 0
		}
		fmt.Fprintf(os.Stderr, "no PR #%s\n", at(2))
		return "", false, 1
	}
	fmt.Fprintf(os.Stderr, "fake gh: unhandled call %q\n", strings.Join(args, " "))
	return "", false, 1
}

// prListJSON renders one row of `gh pr list --json number,state,url` — plus the
// headRefName `status` filters on, which the drain's own lookup does not ask
// for and ignores.
func prListJSON(branch string, pr *fakePR) string {
	return fmt.Sprintf(`{"number":%d,"state":%q,"headRefName":%q,"url":"https://example.invalid/pr/%d"}`,
		pr.Number, pr.State, branch, pr.Number)
}

// rollupJSON renders the statusCheckRollup half of `pr view --json`. Every
// entry is a CheckRun, which is what a GitHub Actions workflow produces; the
// StatusContext shape the array can also carry is covered by the classifier's
// own tests, where both can be written out literally.
func rollupJSON(checks []string) string {
	nodes := make([]string, 0, len(checks))
	for i, c := range checks {
		status, conclusion := "COMPLETED", c
		if c == "PENDING" {
			status, conclusion = "IN_PROGRESS", ""
		}
		nodes = append(nodes, fmt.Sprintf(
			`{"__typename":"CheckRun","name":"check-%d","status":%q,"conclusion":%q}`,
			i+1, status, conclusion))
	}
	return "[" + strings.Join(nodes, ",") + "]"
}

// reviewsJSON renders the reviews half of `pr view --json`, oldest first. The
// reviewDecision beside it is left empty, which is what GitHub reports on a
// repository whose branch protection requires no review — the common case, and
// the one where the reviews themselves have to carry the verdict.
func reviewsJSON(reviews []fakeReview) string {
	nodes := make([]string, 0, len(reviews))
	for _, r := range reviews {
		nodes = append(nodes, fmt.Sprintf(`{"author":{"login":%q},"state":%q,"submittedAt":%q}`,
			r.Author, r.State, r.SubmittedAt))
	}
	return "[" + strings.Join(nodes, ",") + "]"
}

// commitsJSON renders the commits half. Only the newest date matters, so one
// commit says everything two would; an empty date stands for a PR whose commits
// GitHub did not report.
func commitsJSON(committedAt string) string {
	if committedAt == "" {
		return "[]"
	}
	return fmt.Sprintf(`[{"committedDate":%q}]`, committedAt)
}

// listIssues renders `gh issue list --json number,labels` in ascending order,
// which is the order the real thing is only relied on not to guarantee. It also
// ticks the countdowns that stand in for a human acting on the repository
// between one listing and the next, so it reports whether it changed anything.
func listIssues(st *ghState, label string) (out string, changed bool) {
	numbers := make([]int, 0, len(st.Issues))
	for k := range st.Issues {
		n, err := strconv.Atoi(k)
		if err != nil {
			continue
		}
		numbers = append(numbers, n)
	}
	slices.Sort(numbers)

	var rows []string
	for _, n := range numbers {
		is := st.Issues[strconv.Itoa(n)]
		if is.CloseOnList > 0 {
			is.CloseOnList--
			if is.CloseOnList == 0 {
				is.Open = false // the human closes it on this listing
			}
			changed = true
		}
		if !is.Open || (label != "" && !slices.Contains(is.Labels, label)) {
			continue
		}
		var labels []string
		for _, l := range is.Labels {
			labels = append(labels, fmt.Sprintf(`{"name":%q}`, l))
		}
		rows = append(rows, fmt.Sprintf(`{"number":%d,"labels":[%s]}`, n, strings.Join(labels, ",")))
	}
	return "[" + strings.Join(rows, ",") + "]", changed
}

// --- the drain loop, end to end ---

func drainConfig(t *testing.T, mode string, st *ghState) (config, string) {
	t.Helper()
	if st.Repo == "" {
		st.Repo = "example/repo"
	}
	path := filepath.Join(t.TempDir(), "gh-state.json")
	if err := writeGhState(path, st); err != nil {
		t.Fatalf("writing fake gh state: %v", err)
	}
	t.Setenv(fakeGhEnv, path)
	t.Setenv(fakeClaudeEnv, mode)
	return config{
		// Not a checkout at all, which is deliberate: worktree cleanup is
		// best-effort, and so is the probe that says what a parked run left
		// behind — every git call here fails, so every park in these tests
		// pins the wording a failed probe degrades to.
		dir:            t.TempDir(),
		claudeBin:      fakeCLI(t),
		ghBin:          fakeCLI(t),
		repo:           st.Repo, // preflight fills this in; drain tests call drain directly
		skill:          defaultSkill,
		branchPrefix:   "issue-",
		permissionMode: "acceptEdits",
		tools:          "Read",
		poll:           10 * time.Millisecond,
		stall:          10 * time.Second,
		ghRetryWait:    time.Millisecond,
		resumeCeiling:  defaultResumeCeiling,
	}, path
}

func finalGhState(t *testing.T, path string) *ghState {
	t.Helper()
	st, err := readGhState(path)
	if err != nil {
		t.Fatalf("reading fake gh state: %v", err)
	}
	return st
}

// The point of the whole issue: issue 1 cannot be finished, and the drain has
// to park it and go on to issue 2 rather than ending the session on it.
func TestDrainParksADeadIssueAndKeepsGoing(t *testing.T) {
	buf := captureLog(t)
	cfg, path := drainConfig(t, "stream", &ghState{
		Issues: map[string]*fakeIssue{
			"1": {Open: true},
			"2": {Open: true},
		},
		// Issue 2's PR is already merged, so it advances without a claude run;
		// issue 1 has none, and the fake CLI opens none.
		PRs: map[string]*fakePR{"issue-2": {Number: 9, State: "MERGED"}},
	})

	if err := drain(context.Background(), cfg); err != nil {
		t.Fatalf("one dead issue must not end the drain: %v", err)
	}

	st := finalGhState(t, path)
	if got := st.Issues["1"]; !slices.Contains(got.Labels, needsHumanLabel) {
		t.Errorf("issue 1 labels = %v, want %s", got.Labels, needsHumanLabel)
	}
	if got := st.Issues["1"].Comments; got != 1 {
		t.Errorf("issue 1 comments = %d, want 1 saying why it parked", got)
	}
	if st.Issues["2"].Open {
		t.Error("issue 2 should have been closed after its PR merged")
	}
	// The label did not exist on the repository, so the park had to create it.
	if !slices.Contains(st.Labels, needsHumanLabel) {
		t.Errorf("repo labels = %v, want the park to have created %s", st.Labels, needsHumanLabel)
	}

	out := buf.String()
	for _, want := range []string{
		"issue #1 needs a human: the run completed but produced no PR and no questions",
		// A park is when somebody wants to read what the run actually did, so
		// the id that reopens it goes beside the reason — in the log, never in
		// the reason itself, which is posted to the issue thread.
		"issue #1: `claude --resume sess-xyz` reopens what the last skill run on it did",
		"=== issue #2 ===",
		// The run cost something, so the summary prices the drain and each
		// issue in it — including the issue this drain only waited on, whose
		// honest share of the bill is nothing.
		"summary: 1 issue merged, 1 issue parked, $0.50 spent",
		"merged  #2 ($0.00)",
		"parked  #1 ($0.50) — the run completed but produced no PR and no questions",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log is missing %q\ngot:\n%s", want, out)
		}
	}
}

// Issue #56: a run that implemented the whole change and never committed it
// exits exactly as cleanly as one that decided nothing, and both used to park
// with the same sentence. The difference is half an hour of rebase versus an
// issue that needs re-specifying, and it is sitting on disk — so the park says
// it, in the log, in the summary and on the thread, which all carry the one
// reason string.
// leftBehind points cfg at a real checkout in the state issue #42 parked in: a
// branch with a commit on it and a worktree holding more work that was never
// committed. Real, because git is what answers this and no fake can say it
// truthfully — and it is what both the park message and the resume decision
// turn on.
func leftBehind(t *testing.T, cfg *config) {
	t.Helper()
	_, checkout := upstream(t)
	cfg.dir = checkout
	wt := filepath.Join(t.TempDir(), "checkout-issue-1")
	gitAt(t, checkout, "worktree", "add", wt, "-b", "issue-1")
	commit(t, wt, "half-the-change")
	if err := os.WriteFile(filepath.Join(wt, "the-other-half"), []byte("never committed"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A clean exit with work on disk is resumed now rather than parked, so reaching
// the message this test is about takes running the resumes out first — which is
// what the fake does here, ending every turn without a PR.
func TestDrainParkSaysWhatTheRunLeftBehind(t *testing.T) {
	buf := captureLog(t)
	cfg, _ := drainConfig(t, "stream", &ghState{
		Issues: map[string]*fakeIssue{"1": {Open: true}},
	})
	calls := filepath.Join(t.TempDir(), "gh-calls.log")
	t.Setenv(fakeGhLogEnv, calls)
	leftBehind(t, &cfg)

	if err := drain(context.Background(), cfg); err != nil {
		t.Fatalf("one dead issue must not end the drain: %v", err)
	}

	out := buf.String()
	for _, want := range []string{
		"the run completed but produced no PR and no questions; it has been resumed 2 times " +
			"after ending a turn without opening a PR and has still not opened one, which needs " +
			"a human; branch issue-1 has 1 commit " +
			"and its worktree has uncommitted changes in 1 file — the run left work behind, " +
			"so start there rather than from scratch",
		// Where it is goes to the terminal only, beside the resume id and for
		// the same reason. Matched by its tail: git reports the path with
		// symlinks resolved, so on macOS what it prints is not what is spelled
		// here.
		"checkout-issue-1\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log is missing %q\ngot:\n%s", want, out)
		}
	}
	// The park comment is a second audience for the same fact, and the operator
	// reading the summary a third. Both are fed the reason verbatim, so seeing
	// it in the argv of the comment gh was asked to post proves the round trip
	// — and proves what the thread does *not* get.
	posted, err := os.ReadFile(calls)
	if err != nil {
		t.Fatalf("reading the fake gh call log: %v", err)
	}
	if want := "the run left work behind"; !strings.Contains(string(posted), want) {
		t.Errorf("no gh call carried %q to the thread\ngot:\n%s", want, posted)
	}
	if strings.Contains(string(posted), "checkout-issue-1") {
		t.Errorf("a gh call carried the operator's local worktree path to the thread\ngot:\n%s", posted)
	}
}

// The other side of it. A branch that is identical to its base, in a worktree
// with nothing uncommitted in it, is a run that really did decide nothing — and
// the sentence that has always meant that has to go on meaning only that.
func TestDrainParkSaysNothingExtraWhenTheRunLeftNothing(t *testing.T) {
	buf := captureLog(t)
	cfg, _ := drainConfig(t, "stream", &ghState{
		Issues: map[string]*fakeIssue{"1": {Open: true}},
	})
	_, checkout := upstream(t)
	cfg.dir = checkout
	wt := filepath.Join(t.TempDir(), "checkout-issue-1")
	gitAt(t, checkout, "worktree", "add", wt, "-b", "issue-1")
	// With the plan the skill writes before it implements anything still sitting
	// there, because that is what this case looks like in production: nothing
	// commits it, deletes it or ignores it, so counting it would make every park
	// claim work was left behind.
	if err := os.WriteFile(filepath.Join(wt, planFile), []byte("## Approach\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := drain(context.Background(), cfg); err != nil {
		t.Fatalf("one dead issue must not end the drain: %v", err)
	}

	out := buf.String()
	if want := "parked  #1 ($0.50) — the run completed but produced no PR and no questions\n"; !strings.Contains(out, want) {
		t.Errorf("log is missing %q\ngot:\n%s", want, out)
	}
	for _, unwanted := range []string{"left work behind", "branch issue-1 has", "the work it left is in"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("log says %q about a run that left nothing\ngot:\n%s", unwanted, out)
		}
	}
	// And nothing was resumed. A run with nothing on disk is the one clean exit
	// that really did decide nothing, and paying for two more of it is exactly
	// what the branch-state probe is there to prevent.
	if got := strings.Count(out, "session started"); got != 1 {
		t.Errorf("%d runs dispatched, want 1 — a run that left nothing must not be resumed\ngot:\n%s",
			got, out)
	}
}

// The wording, over the combinations a drain test would need a separate
// checkout apiece to reach. Worth pinning by hand: this text is often the only
// thing a person is told about a run, and "no commits" beside a dirty worktree
// is the case that matters most — it is the one that reads as nothing happened
// and is not.
func TestLeftWorkDescribe(t *testing.T) {
	for _, tc := range []struct {
		name string
		w    leftWork
		want string
	}{
		{"nothing at all", leftWork{branch: "issue-9", counted: true}, ""},
		{"a worktree with nothing in it", leftWork{branch: "issue-9", counted: true, path: "/w"}, ""},
		{"issue #42: implemented and never committed",
			leftWork{branch: "issue-42", counted: true, path: "/w", dirty: 6},
			"branch issue-42 has no commits and its worktree has uncommitted changes in 6 files " +
				"— the run left work behind, so start there rather than from scratch"},
		{"committed but never pushed, worktree tidy",
			leftWork{branch: "issue-42", counted: true, path: "/w", commits: 2},
			"branch issue-42 has 2 commits and its worktree has no uncommitted changes " +
				"— the run left work behind, so start there rather than from scratch"},
		{"a worktree somebody already removed",
			leftWork{branch: "issue-42", counted: true, commits: 1},
			"branch issue-42 has 1 commit — the run left work behind, so start there rather than from scratch"},
		// A checkout with no origin/HEAD to compare against. "no commits" is the
		// half of this a person acts on, so an uncounted branch says so rather
		// than reporting the zero it never established.
		{"the branch could not be compared with anything",
			leftWork{branch: "issue-42", path: "/w", dirty: 6},
			"branch issue-42 could not be compared with the default branch and its worktree has " +
				"uncommitted changes in 6 files — the run left work behind, so start there " +
				"rather than from scratch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.w.describe(); got != tc.want {
				t.Errorf("describe() = %q, want %q", got, tc.want)
			}
		})
	}
}

// The path is the one thing a person cannot get off the issue thread, and the
// one thing that must not go on it: the thread may be public, and an absolute
// path names the operator's account and how their disk is laid out. So it
// travels beside the park rather than inside its reason.
func TestLeftWorkWhereIsForTheLogAlone(t *testing.T) {
	w := leftWork{branch: "issue-42", counted: true, path: "/src/repo-issue-42", dirty: 6}
	if got, want := w.where(), "the work it left is in /src/repo-issue-42"; got != want {
		t.Errorf("where() = %q, want %q", got, want)
	}
	if strings.Contains(w.describe(), w.path) {
		t.Errorf("describe() carries the path to the issue thread: %q", w.describe())
	}
	// Nothing to point at is nothing to say: a worktree that is merely clean
	// leaves no work, and a park that named it would be sending a person to a
	// directory with nothing in it.
	for _, empty := range []leftWork{
		{branch: "issue-42", counted: true, path: "/src/repo-issue-42"},
		{branch: "issue-42", counted: true, commits: 3},
	} {
		if got := empty.where(); got != "" {
			t.Errorf("where() = %q for %+v, want \"\"", got, empty)
		}
	}
}

// git names the worktree holding a branch, and it is asked rather than guessed
// because a run driven from the desktop app puts one somewhere the sibling
// convention would never look. A detached worktree in the way must not be
// mistaken for it.
func TestWorktreeForFindsTheWorktreeHoldingABranch(t *testing.T) {
	list := "worktree /repo\nHEAD aaa\nbranch refs/heads/main\n\n" +
		"worktree /tmp/detached\nHEAD bbb\ndetached\n\n" +
		"worktree /elsewhere/wt\nHEAD ccc\nbranch refs/heads/issue-7\n\n"
	if got := worktreeFor(list, "issue-7"); got != "/elsewhere/wt" {
		t.Errorf("worktreeFor = %q, want /elsewhere/wt", got)
	}
	if got := worktreeFor(list, "issue-8"); got != "" {
		t.Errorf("worktreeFor for a branch with no worktree = %q, want \"\"", got)
	}
}

// Two things only a real repository can answer, which is why they are not in
// the table above: what git counts as changed, and what it says about a
// worktree whose directory is gone.
func TestInspectLeftWorkAgainstARealCheckout(t *testing.T) {
	t.Run("a whole new directory is counted file by file", func(t *testing.T) {
		_, checkout := upstream(t)
		wt := filepath.Join(t.TempDir(), "checkout-issue-3")
		gitAt(t, checkout, "worktree", "add", wt, "-b", "issue-3")
		if err := os.MkdirAll(filepath.Join(wt, "newpkg"), 0o755); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"a", "b", "c"} {
			if err := os.WriteFile(filepath.Join(wt, "newpkg", name), []byte(name), 0o644); err != nil {
				t.Fatal(err)
			}
		}

		// `git status --porcelain` alone answers "1 file" here: it folds an
		// untracked directory into one `newpkg/` line. That is the run this
		// message exists for — a change written and never committed — so the
		// number it reports has to be the number of files.
		w := inspectLeftWork(context.Background(), config{dir: checkout, branchPrefix: "issue-"}, 3)
		if w.dirty != 3 {
			t.Errorf("dirty = %d, want 3 — an untracked directory counted as one file", w.dirty)
		}
	})

	t.Run("the plan the skill wrote is not work left behind", func(t *testing.T) {
		_, checkout := upstream(t)
		wt := filepath.Join(t.TempDir(), "checkout-issue-5")
		gitAt(t, checkout, "worktree", "add", wt, "-b", "issue-5")
		if err := os.WriteFile(filepath.Join(wt, planFile), []byte("## Approach\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		cfg := config{dir: checkout, branchPrefix: "issue-"}
		if w := inspectLeftWork(context.Background(), cfg, 5); w.dirty != 0 || w.salvageable() {
			t.Errorf("dirty = %d, salvageable = %v for a worktree holding only the plan; "+
				"counted, every park would claim work was left behind", w.dirty, w.salvageable())
		}
		// And it is only the plan that is discounted: real work beside it counts.
		if err := os.WriteFile(filepath.Join(wt, "half-the-change"), []byte("code"), 0o644); err != nil {
			t.Fatal(err)
		}
		if w := inspectLeftWork(context.Background(), cfg, 5); w.dirty != 1 {
			t.Errorf("dirty = %d, want 1 — the plan is discounted, the change beside it is not", w.dirty)
		}
	})

	t.Run("a worktree deleted by hand is not named", func(t *testing.T) {
		_, checkout := upstream(t)
		wt := filepath.Join(t.TempDir(), "checkout-issue-4")
		gitAt(t, checkout, "worktree", "add", wt, "-b", "issue-4")
		commit(t, wt, "half-the-change")
		// rm -rf, not `git worktree remove`: git goes on listing the worktree
		// until something prunes it, and a park that reads that list at face
		// value sends a person to a directory that is not there any more.
		if err := os.RemoveAll(wt); err != nil {
			t.Fatal(err)
		}

		w := inspectLeftWork(context.Background(), config{dir: checkout, branchPrefix: "issue-"}, 4)
		if w.path != "" {
			t.Errorf("path = %q, want \"\" — the directory is gone", w.path)
		}
		want := "branch issue-4 has 1 commit — the run left work behind, so start there rather than from scratch"
		if got := w.describe(); got != want {
			t.Errorf("describe() = %q, want %q", got, want)
		}
	})
}

// The whole question round, end to end, under -strict-order: a run that asks
// something flags the issue, the drain waits on that flag in place, and the
// re-run that finds the answer clears it and ships. The claude invocation is
// checked too — the run can only raise the flag if the allowlist it was handed
// permits it, pinned to this issue and no other.
func TestDrainWaitsForAnAnswerThenFoldsItIn(t *testing.T) {
	buf := captureLog(t)
	cfg, path := drainConfig(t, "asks", &ghState{
		Issues: map[string]*fakeIssue{"1": {Open: true}},
		Labels: []string{awaitingAnswerLabel},
	})
	cfg.strictOrder = true

	if err := drain(context.Background(), cfg); err != nil {
		t.Fatalf("drain: %v", err)
	}

	st := finalGhState(t, path)
	if got := st.Issues["1"].Labels; slices.Contains(got, awaitingAnswerLabel) {
		t.Errorf("issue 1 labels = %v, want %s cleared once the answer landed", got, awaitingAnswerLabel)
	}
	if got := st.Issues["1"].Labels; slices.Contains(got, needsHumanLabel) {
		t.Errorf("issue 1 labels = %v, want a question round not to park the issue", got)
	}
	if st.Issues["1"].Open {
		t.Error("issue 1 should have been closed after its PR merged")
	}

	out := buf.String()
	for _, want := range []string{
		`issue #1 is labelled "awaiting-answer" — waiting for a reply on the thread`,
		"somebody replied on #1 — re-running to fold the answers in",
		"Bash(gh issue edit 1 --add-label:*)",
		"summary: 1 issue merged, 0 issues parked",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log is missing %q\ngot:\n%s", want, out)
		}
	}
}

// The flag is durable, so "it is up after the run" is not the same as "this run
// raised it". A re-run dispatched to fold an answer in, dying before it can
// clear the flag, leaves the asking run's flag standing — and waiting on that
// one waits for the reply that already arrived, which is to say forever. The
// crash has to be treated as a crash: resume it, and park when the resumes run
// out, the same as any other run that dies with nothing to show.
func TestDrainDoesNotWaitTwiceOnOneQuestion(t *testing.T) {
	buf := captureLog(t)
	cfg, path := drainConfig(t, "askscrash", &ghState{
		Issues: map[string]*fakeIssue{"1": {Open: true}},
		Labels: []string{awaitingAnswerLabel},
	})
	cfg.retries = 1

	// Bounded, because the regression this guards is an unbounded wait: without
	// the fix the drain never returns, and a hung suite says less than a failure.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := drain(ctx, cfg); err != nil {
		t.Fatalf("drain: %v", err)
	}

	st := finalGhState(t, path)
	if got := st.Issues["1"].Labels; !slices.Contains(got, needsHumanLabel) {
		t.Errorf("issue 1 labels = %v, want it parked once the resumes ran out", got)
	}
	if got := st.Issues["1"].Labels; slices.Contains(got, awaitingAnswerLabel) {
		t.Errorf("issue 1 labels = %v, want the park to clear %s — what it waits on "+
			"now is a decision, not a reply", got, awaitingAnswerLabel)
	}

	out := buf.String()
	for _, want := range []string{
		"somebody replied on #1 — re-running to fold the answers in",
		"resuming session sess-xyz",
		"claude crashed and 1 resume attempts failed",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log is missing %q\ngot:\n%s", want, out)
		}
	}
	// One question, one hand-off. A second would be the bug.
	if got := strings.Count(out, "leaving it for a human"); got != 1 {
		t.Errorf("put the issue down %d times, want exactly 1\ngot:\n%s", got, out)
	}
}

// The point of parking a blocked issue: issue 1 stops to ask something, and the
// drain has to work issue 2 rather than sitting on the thread. Issue 1 is
// picked back up once nothing else is left and the reply has landed, so both
// still ship — and only ever one at a time.
func TestDrainWorksALaterIssueWhileOneAwaitsAnAnswer(t *testing.T) {
	buf := captureLog(t)
	cfg, path := drainConfig(t, "asks", &ghState{
		Issues: map[string]*fakeIssue{"1": {Open: true}, "2": {Open: true}},
		// Issue 2's PR is already merged, so it advances without a claude run
		// — the fake CLI only knows how to work whichever issue it is handed.
		PRs:    map[string]*fakePR{"issue-2": {Number: 9, State: "MERGED"}},
		Labels: []string{awaitingAnswerLabel},
	})

	if err := drain(context.Background(), cfg); err != nil {
		t.Fatalf("drain: %v", err)
	}

	st := finalGhState(t, path)
	for _, n := range []string{"1", "2"} {
		if st.Issues[n].Open {
			t.Errorf("issue %s should have been closed after its PR merged", n)
		}
		if got := st.Issues[n].Labels; slices.Contains(got, needsHumanLabel) {
			t.Errorf("issue %s labels = %v, want a question round not to park anything", n, got)
		}
	}
	if got := st.Issues["1"].Labels; slices.Contains(got, awaitingAnswerLabel) {
		t.Errorf("issue 1 labels = %v, want %s cleared once the answer landed", got, awaitingAnswerLabel)
	}

	out := buf.String()
	// Issue 2 has to be reached *after* issue 1 is put down: reaching it first
	// would prove nothing, and never reaching it is the bug this fixes.
	putDown := strings.Index(out, "issue #1 is labelled \"awaiting-answer\" — leaving it for a human")
	second := strings.Index(out, "=== issue #2 ===")
	if putDown < 0 || second < 0 || second < putDown {
		t.Errorf("want issue 2 worked after issue 1 was put down (put down at %d, issue 2 at %d)\ngot:\n%s",
			putDown, second, out)
	}
	for _, want := range []string{
		"nothing else to work — waiting for a reply on #1",
		"somebody replied on #1 — re-running to fold the answers in",
		"summary: 2 issues merged, 0 issues parked",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log is missing %q\ngot:\n%s", want, out)
		}
	}
}

// Restart safety: the comment baseline a wait compares against dies with the
// process, and nothing on GitHub says whether the reply already landed. So an
// issue found already flagged is re-run rather than waited on — the skill
// re-reads the thread, and stops again without re-asking if there is nothing
// new. Waiting instead would sit forever on an answer given while this drain
// was not running.
func TestDrainRetriesAnIssueFlaggedBeforeItStarted(t *testing.T) {
	buf := captureLog(t)
	cfg, path := drainConfig(t, "asks", &ghState{
		Issues: map[string]*fakeIssue{"1": {Open: true, Labels: []string{awaitingAnswerLabel}, Comments: 2}},
		Labels: []string{awaitingAnswerLabel},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := drain(ctx, cfg); err != nil {
		t.Fatalf("drain: %v", err)
	}

	if st := finalGhState(t, path); st.Issues["1"].Open {
		t.Error("issue 1 should have been closed after the answer was folded in and its PR merged")
	}
	out := buf.String()
	if want := "was already labelled \"awaiting-answer\" when this shift reached it"; !strings.Contains(out, want) {
		t.Errorf("log is missing %q\ngot:\n%s", want, out)
	}
	if strings.Contains(out, "nothing else to work") {
		t.Errorf("a flag this drain did not raise must be re-run, not waited on\ngot:\n%s", out)
	}
}

// Working an issue is the end of waiting on it. An exit that is neither a merge
// nor a park — refused credentials here — must not sign off by sending the
// operator to a thread they have already replied on.
func TestDrainStopsCallingAnIssueWaitingOnceItIsPickedBackUp(t *testing.T) {
	buf := captureLog(t)
	cfg, _ := drainConfig(t, "asksthenauth", &ghState{
		Issues: map[string]*fakeIssue{"1": {Open: true}},
		Labels: []string{awaitingAnswerLabel},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := drain(ctx, cfg); !errors.Is(err, errAuth) {
		t.Fatalf("drain error = %v, want it to carry %v", err, errAuth)
	}

	out := buf.String()
	if !strings.Contains(out, "somebody replied on #1") {
		t.Fatalf("the answer never landed, so this proves nothing\ngot:\n%s", out)
	}
	if strings.Contains(out, "awaiting an answer") || strings.Contains(out, "waiting #1") {
		t.Errorf("issue 1 was picked back up, so it is not waiting on a reply any more\ngot:\n%s", out)
	}
}

// A drained backlog means nothing is waiting either. An issue this drain put
// down and a human then closed themselves is gone from the queue, and naming it
// in the summary would send somebody to a thread with nothing left to do on it.
func TestDrainForgetsAQuestionAHumanClosedInstead(t *testing.T) {
	buf := captureLog(t)
	cfg, _ := drainConfig(t, "asks", &ghState{
		// Issue 1 asks something, and is closed by hand on the third listing —
		// by which time the drain has moved on to issue 2 and finished it.
		Issues: map[string]*fakeIssue{"1": {Open: true, CloseOnList: 3}, "2": {Open: true}},
		PRs:    map[string]*fakePR{"issue-2": {Number: 9, State: "MERGED"}},
		Labels: []string{awaitingAnswerLabel},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := drain(ctx, cfg); err != nil {
		t.Fatalf("drain: %v", err)
	}

	out := buf.String()
	for _, want := range []string{
		"leaving it for a human", // it really was put down
		"=== issue #2 ===",       // and the queue behind it really was worked
		"no open issues — backlog cleared",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("log is missing %q, so this proves nothing\ngot:\n%s", want, out)
		}
	}
	if strings.Contains(out, "awaiting an answer") || strings.Contains(out, "waiting #1") {
		t.Errorf("issue 1 is closed, so nothing is waiting on a reply\ngot:\n%s", out)
	}
}

// -once is "one issue, then give me my terminal back", and an issue waiting on
// a person is as done with as this process can make it. It must not spend the
// night polling a thread, and it must say what it left behind.
func TestDrainOnceExitsOnAQuestion(t *testing.T) {
	buf := captureLog(t)
	cfg, path := drainConfig(t, "asks", &ghState{
		Issues: map[string]*fakeIssue{"1": {Open: true}, "2": {Open: true}},
		Labels: []string{awaitingAnswerLabel},
	})
	cfg.once = true

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := drain(ctx, cfg); err != nil {
		t.Fatalf("drain: %v", err)
	}

	st := finalGhState(t, path)
	if got := st.Issues["1"].Labels; !slices.Contains(got, awaitingAnswerLabel) {
		t.Errorf("issue 1 labels = %v, want the question left flagged for a human", got)
	}
	if got := st.Issues["1"].Labels; slices.Contains(got, needsHumanLabel) {
		t.Errorf("issue 1 labels = %v, want a question not to park the issue", got)
	}
	if got := st.Issues["2"].Comments; got != 0 {
		t.Errorf("issue 2 comments = %d, want -once to have stopped before reaching it", got)
	}

	out := buf.String()
	for _, want := range []string{
		"summary: 0 issues merged, 0 issues parked, 1 issue awaiting an answer, $0.50 spent",
		// An issue put down for an answer keeps its tally, so the summary can
		// still say what the round of questions cost.
		"waiting #1 ($0.50) — reply on the thread and the next shift picks them up",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log is missing %q\ngot:\n%s", want, out)
		}
	}
}

// The bug this label replaced: "the skill asked a question" was inferred from
// the comment count rising across a run, so CI, a bot or a passer-by commenting
// mid-run left the drain waiting forever on a reply nobody knew was expected.
// Only the label says a question was asked, so a noisy thread now ends the way
// any other run that produced nothing does.
func TestDrainDoesNotReadStrayCommentsAsQuestions(t *testing.T) {
	buf := captureLog(t)
	cfg, path := drainConfig(t, "noisy", &ghState{
		Issues: map[string]*fakeIssue{"1": {Open: true}},
		Labels: []string{awaitingAnswerLabel},
	})

	if err := drain(context.Background(), cfg); err != nil {
		t.Fatalf("drain: %v", err)
	}

	st := finalGhState(t, path)
	if got := st.Issues["1"].Comments; got < 1 {
		t.Fatalf("issue 1 comments = %d, want the bot's comment to have landed", got)
	}
	if got := st.Issues["1"].Labels; !slices.Contains(got, needsHumanLabel) {
		t.Errorf("issue 1 labels = %v, want it parked rather than waited on", got)
	}
	if want := "the run completed but produced no PR and no questions"; !strings.Contains(buf.String(), want) {
		t.Errorf("log is missing %q\ngot:\n%s", want, buf.String())
	}
}

// Issue #30: a wait ends when somebody answers, not when the thread twitches.
// CI, a stale-bot nudge or a release announcement lands on an issue that is
// still exactly as blocked as it was, and each one used to cost a full Claude
// run to discover that. Under -strict-order, where the drain sits on the thread
// itself.
func TestDrainKeepsWaitingThroughABotComment(t *testing.T) {
	buf := captureLog(t)
	cfg, path := drainConfig(t, "asksbot", &ghState{
		Issues: map[string]*fakeIssue{"1": {Open: true}},
		Labels: []string{awaitingAnswerLabel},
	})
	cfg.strictOrder = true

	// Bounded: the failure this guards against is a wait that never ends.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := drain(ctx, cfg); err != nil {
		t.Fatalf("drain: %v", err)
	}

	st := finalGhState(t, path)
	if len(st.Issues["1"].Bots) != 1 {
		t.Fatalf("issue 1 bot comments = %v, want the bot to have commented — this proves nothing otherwise",
			st.Issues["1"].Bots)
	}
	if st.Issues["1"].Open {
		t.Error("issue 1 should have been closed after the answer was folded in and its PR merged")
	}

	out := buf.String()
	for _, want := range []string{
		"still awaiting a reply (1 new comment(s), none of them from a person)",
		"somebody replied on #1 — re-running to fold the answers in",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log is missing %q\ngot:\n%s", want, out)
		}
	}
	// Two runs: the one that asked, and the one that folded the answer in. A
	// third would be the bot's comment paid for at full price.
	if got := strings.Count(out, "session started"); got != 2 {
		t.Errorf("dispatched %d claude runs, want 2 — a bot comment must not be one of them\ngot:\n%s",
			got, out)
	}
}

// The same, on the path a default drain actually takes: issue 1 is put down and
// issue 2 worked, and the poll that decides whether to pick issue 1 back up has
// to ignore the bot exactly as the in-place wait does.
func TestDrainDoesNotPickAnIssueBackUpForABotComment(t *testing.T) {
	buf := captureLog(t)
	cfg, path := drainConfig(t, "asksbot", &ghState{
		Issues: map[string]*fakeIssue{"1": {Open: true}, "2": {Open: true}},
		// Issue 2's PR is already merged, so it advances without a claude run and
		// the drain is left with nothing to work but the blocked issue.
		PRs:    map[string]*fakePR{"issue-2": {Number: 9, State: "MERGED"}},
		Labels: []string{awaitingAnswerLabel},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := drain(ctx, cfg); err != nil {
		t.Fatalf("drain: %v", err)
	}

	st := finalGhState(t, path)
	if len(st.Issues["1"].Bots) != 1 {
		t.Fatalf("issue 1 bot comments = %v, want the bot to have commented — this proves nothing otherwise",
			st.Issues["1"].Bots)
	}
	if got := st.Issues["1"].Labels; slices.Contains(got, awaitingAnswerLabel) {
		t.Errorf("issue 1 labels = %v, want %s cleared once the answer landed", got, awaitingAnswerLabel)
	}

	out := buf.String()
	for _, want := range []string{
		"issue #1 still awaiting a reply (1 new comment(s), none of them from a person)",
		"somebody replied on #1 — re-running to fold the answers in",
		"summary: 2 issues merged, 0 issues parked",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log is missing %q\ngot:\n%s", want, out)
		}
	}
	// The bot's comment came between the two runs on issue 1; picking it back up
	// for that would show as a third "=== issue #1 ===".
	if got := strings.Count(out, "=== issue #1 ==="); got != 2 {
		t.Errorf("picked issue 1 up %d times, want 2 — a bot comment must not be one of them\ngot:\n%s",
			got, out)
	}
}

// --- red CI on an open PR ---

// The point of issue #5: a failing check used to be invisible to the
// supervisor, logged as "still open" every poll until a human noticed. It has
// to dispatch one remediation run — one, not one per poll — and the PR goes on
// to merge once that run has pushed.
func TestDrainRemediatesAFailingCheck(t *testing.T) {
	buf := captureLog(t)
	cfg, path := drainConfig(t, "fixci", &ghState{
		Issues: map[string]*fakeIssue{"1": {Open: true}},
		// The PR already exists, so restart safety sends the drain straight to
		// supervising it: the only claude run here is the remediation.
		PRs: map[string]*fakePR{"issue-1": {
			Number: 9, State: "OPEN", Mergeable: "MERGEABLE",
			Head: "abc123", Checks: []string{"SUCCESS", "FAILURE"},
			MergeOnRead: 3,
		}},
	})

	if err := drain(context.Background(), cfg); err != nil {
		t.Fatalf("drain: %v", err)
	}

	st := finalGhState(t, path)
	if st.Issues["1"].Open {
		t.Error("issue 1 should have been closed once the fixed PR merged")
	}
	if got := st.Issues["1"].Labels; slices.Contains(got, needsHumanLabel) {
		t.Errorf("issue 1 labels = %v, want a repaired build not to park the issue", got)
	}

	out := buf.String()
	// The name has to come from the node that failed, not from the rollup: a
	// remediation told to fix "check-1" would go looking at the green one.
	if want := "PR #9 has 1 check failing (check-2)"; !strings.Contains(out, want) {
		t.Errorf("log is missing %q\ngot:\n%s", want, out)
	}
	if got := strings.Count(out, "dispatching remediation"); got != 1 {
		t.Errorf("dispatched %d remediation runs, want exactly 1 for one observed failure\ngot:\n%s", got, out)
	}
	if want := "checks: passing"; !strings.Contains(out, want) {
		t.Errorf("the poll after the fix should report %q\ngot:\n%s", want, out)
	}
}

// A remediation that finishes without pushing has diagnosed all it is going to.
// Running it again reads the same logs against the same commit and lands in the
// same place, so the issue parks rather than looping until someone notices.
func TestDrainParksWhenCIRemediationChangesNothing(t *testing.T) {
	buf := captureLog(t)
	cfg, path := drainConfig(t, "stream", &ghState{
		Issues: map[string]*fakeIssue{"1": {Open: true}},
		// "stream" leaves the pretend repository alone: the run ends cleanly
		// having pushed nothing, which is the case worth pinning.
		PRs: map[string]*fakePR{"issue-1": {
			Number: 9, State: "OPEN", Mergeable: "MERGEABLE",
			Head: "abc123", Checks: []string{"FAILURE"},
		}},
		Labels: []string{needsHumanLabel},
	})

	// Bounded, because the regression this guards is an unbounded wait: without
	// the fix the drain never returns, and a hung suite says less than a failure.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := drain(ctx, cfg); err != nil {
		t.Fatalf("one unfixable build must not end the drain: %v", err)
	}

	st := finalGhState(t, path)
	if got := st.Issues["1"].Labels; !slices.Contains(got, needsHumanLabel) {
		t.Errorf("issue 1 labels = %v, want it parked once remediation stopped helping", got)
	}
	if !st.Issues["1"].Open {
		t.Error("parking must leave the issue open for a human")
	}

	out := buf.String()
	if got := strings.Count(out, "dispatching remediation"); got != 1 {
		t.Errorf("dispatched %d remediation runs, want 1 before giving up\ngot:\n%s", got, out)
	}
	if want := "CI on PR #9 is still red and remediation left the branch unchanged"; !strings.Contains(out, want) {
		t.Errorf("log is missing %q\ngot:\n%s", want, out)
	}
}

// Half a suite is not a diagnosis: a job still running can only add to the list
// of failures, so a rollup with anything pending in it waits.
func TestDrainWaitsOutChecksStillRunning(t *testing.T) {
	buf := captureLog(t)
	cfg, path := drainConfig(t, "stream", &ghState{
		Issues: map[string]*fakeIssue{"1": {Open: true}},
		PRs: map[string]*fakePR{"issue-1": {
			Number: 9, State: "OPEN", Mergeable: "MERGEABLE",
			Head: "abc123", Checks: []string{"FAILURE", "PENDING"},
			MergeOnRead: 2,
		}},
	})

	if err := drain(context.Background(), cfg); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if st := finalGhState(t, path); st.Issues["1"].Open {
		t.Error("issue 1 should have been closed once its PR merged")
	}

	out := buf.String()
	if strings.Contains(out, "dispatching remediation") {
		t.Errorf("remediated a suite that had not finished\ngot:\n%s", out)
	}
	if want := "checks: pending"; !strings.Contains(out, want) {
		t.Errorf("log is missing %q\ngot:\n%s", want, out)
	}
}

// --- changes requested on an open PR ---

// The point of issue #6: a human asking for changes used to be invisible to the
// supervisor, which went on waiting for a merge nobody was going to perform. It
// has to dispatch one remediation run — one, not one per poll — and the PR goes
// on to merge once that run has pushed.
func TestDrainRemediatesARequestedChange(t *testing.T) {
	buf := captureLog(t)
	cfg, path := drainConfig(t, "fixreview", &ghState{
		Issues: map[string]*fakeIssue{"1": {Open: true}},
		// The PR already exists, so restart safety sends the drain straight to
		// supervising it: the only claude run here is the remediation. Green and
		// mergeable, so the review is the only thing left to act on.
		PRs: map[string]*fakePR{"issue-1": {
			Number: 9, State: "OPEN", Mergeable: "MERGEABLE",
			Head: "abc123", Checks: []string{"SUCCESS"},
			Reviews:     []fakeReview{{State: reviewChangesRequested, SubmittedAt: "2026-08-20T10:00:00Z"}},
			CommittedAt: "2026-08-19T10:00:00Z", // the review is newer: nobody has answered it
			MergeOnRead: 3,
		}},
	})

	if err := drain(context.Background(), cfg); err != nil {
		t.Fatalf("drain: %v", err)
	}

	st := finalGhState(t, path)
	if st.Issues["1"].Open {
		t.Error("issue 1 should have been closed once the reworked PR merged")
	}
	if got := st.Issues["1"].Labels; slices.Contains(got, needsHumanLabel) {
		t.Errorf("issue 1 labels = %v, want an answered review not to park the issue", got)
	}

	out := buf.String()
	if want := "PR #9 has changes requested — dispatching remediation"; !strings.Contains(out, want) {
		t.Errorf("log is missing %q\ngot:\n%s", want, out)
	}
	if got := strings.Count(out, "dispatching remediation"); got != 1 {
		t.Errorf("dispatched %d remediation runs, want exactly 1 for one review\ngot:\n%s", got, out)
	}
}

// A review a run cannot answer is not worth re-reading. Once a remediation has
// finished and left the branch where it was, the same words against the same
// commit land in the same place, so the issue parks for a human instead of
// consuming a run per poll.
func TestDrainParksWhenReviewRemediationChangesNothing(t *testing.T) {
	buf := captureLog(t)
	cfg, path := drainConfig(t, "stream", &ghState{
		Issues: map[string]*fakeIssue{"1": {Open: true}},
		// "stream" leaves the pretend repository alone: the run ends cleanly
		// having pushed nothing, which is the case worth pinning.
		PRs: map[string]*fakePR{"issue-1": {
			Number: 9, State: "OPEN", Mergeable: "MERGEABLE",
			Head: "abc123", Checks: []string{"SUCCESS"},
			Reviews:     []fakeReview{{State: reviewChangesRequested, SubmittedAt: "2026-08-20T10:00:00Z"}},
			CommittedAt: "2026-08-19T10:00:00Z",
		}},
		Labels: []string{needsHumanLabel},
	})

	// Bounded, because the regression this guards is an unbounded wait: without
	// the fix the drain never returns, and a hung suite says less than a failure.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := drain(ctx, cfg); err != nil {
		t.Fatalf("one unanswerable review must not end the drain: %v", err)
	}

	st := finalGhState(t, path)
	if got := st.Issues["1"].Labels; !slices.Contains(got, needsHumanLabel) {
		t.Errorf("issue 1 labels = %v, want it parked once remediation stopped helping", got)
	}
	if !st.Issues["1"].Open {
		t.Error("parking must leave the issue open for a human")
	}

	out := buf.String()
	if got := strings.Count(out, "dispatching remediation"); got != 1 {
		t.Errorf("dispatched %d remediation runs, want 1 before giving up\ngot:\n%s", got, out)
	}
	if want := "changes are still requested on PR #9 and remediation left the branch unchanged"; !strings.Contains(out, want) {
		t.Errorf("log is missing %q\ngot:\n%s", want, out)
	}
}

// A review somebody has already pushed an answer to is waiting on the reviewer,
// not on this process. Dispatching a run at it would rewrite code in response to
// comments that have already been addressed, so the poll only says what is
// holding the PR up.
func TestDrainWaitsOutAnAnsweredReview(t *testing.T) {
	buf := captureLog(t)
	cfg, path := drainConfig(t, "stream", &ghState{
		Issues: map[string]*fakeIssue{"1": {Open: true}},
		PRs: map[string]*fakePR{"issue-1": {
			Number: 9, State: "OPEN", Mergeable: "MERGEABLE",
			Head: "abc123", Checks: []string{"SUCCESS"},
			Reviews:     []fakeReview{{State: reviewChangesRequested, SubmittedAt: "2026-08-19T10:00:00Z"}},
			CommittedAt: "2026-08-20T10:00:00Z", // the branch moved after the review
			MergeOnRead: 2,
		}},
	})

	if err := drain(context.Background(), cfg); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if st := finalGhState(t, path); st.Issues["1"].Open {
		t.Error("issue 1 should have been closed once its PR merged")
	}

	out := buf.String()
	if strings.Contains(out, "dispatching remediation") {
		t.Errorf("remediated a review that had already been answered\ngot:\n%s", out)
	}
	if want := "changes requested and answered — waiting on a re-review"; !strings.Contains(out, want) {
		t.Errorf("log is missing %q, so the poll never says what holds the PR up\ngot:\n%s", want, out)
	}
}

// A parked issue must not come back round: the label is what takes it out of
// the queue, and the loop has to terminate rather than retry it forever.
func TestDrainStopsSelectingAParkedIssue(t *testing.T) {
	captureLog(t)
	cfg, path := drainConfig(t, "stream", &ghState{
		Issues: map[string]*fakeIssue{"1": {Open: true}},
		Labels: []string{needsHumanLabel},
	})

	if err := drain(context.Background(), cfg); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if st := finalGhState(t, path); !st.Issues["1"].Open {
		t.Error("parking must leave the issue open for a human, not close it")
	}
}

// Refused credentials are not one issue's problem — every later issue would
// hit the same wall — so they still end the drain, and the issue is left alone
// rather than blamed for it. Issue 1 merges first, so this also pins the other
// half: what got done before the fatal error is still accounted for.
func TestDrainStillStopsOnAFatalError(t *testing.T) {
	buf := captureLog(t)
	cfg, path := drainConfig(t, "authfail", &ghState{
		Issues: map[string]*fakeIssue{"1": {Open: true}, "2": {Open: true}},
		PRs:    map[string]*fakePR{"issue-1": {Number: 8, State: "MERGED"}},
	})

	err := drain(context.Background(), cfg)
	if !errors.Is(err, errAuth) {
		t.Fatalf("drain error = %v, want it to carry %v", err, errAuth)
	}
	st := finalGhState(t, path)
	if got := st.Issues["2"]; slices.Contains(got.Labels, needsHumanLabel) {
		t.Errorf("issue 2 labels = %v, want no park for a credentials failure", got.Labels)
	}
	if got := st.Issues["2"].Comments; got != 0 {
		t.Errorf("issue 2 comments = %d, want none", got)
	}
	if want := "summary: 1 issue merged, 0 issues parked"; !strings.Contains(buf.String(), want) {
		t.Errorf("a fatal exit must still account for what finished: missing %q\ngot:\n%s",
			want, buf.String())
	}
}

func TestDrainHonoursOnceAfterAPark(t *testing.T) {
	captureLog(t)
	cfg, path := drainConfig(t, "stream", &ghState{
		Issues: map[string]*fakeIssue{"1": {Open: true}, "2": {Open: true}},
		PRs:    map[string]*fakePR{"issue-2": {Number: 9, State: "MERGED"}},
	})
	cfg.once = true

	if err := drain(context.Background(), cfg); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if st := finalGhState(t, path); !st.Issues["2"].Open {
		t.Error("-once must stop after the parked issue, leaving issue 2 untouched")
	}
}

// --- the pieces, on their own ---

func TestSelectableIssuesDropsParkedOnes(t *testing.T) {
	raw := []byte(`[{"number":4,"labels":[{"name":"bug"}]},
		{"number":5,"labels":[{"name":"Needs-Human"}]},
		{"number":6,"labels":[]},
		{"number":7,"labels":[{"name":"bug"},{"name":"needs-human"}]}]`)

	q, err := selectableIssues(raw)
	if err != nil {
		t.Fatalf("selectableIssues: %v", err)
	}
	// 5 proves the match ignores case, the way GitHub treats label names.
	if want := []int{4, 6}; !slices.Equal(q.ready, want) {
		t.Errorf("ready = %v, want %v", q.ready, want)
	}
	if len(q.blocked) != 0 {
		t.Errorf("blocked = %v, want nothing waiting on an answer", q.blocked)
	}
	// Out of the drain's way, but still findable: `status` reports what is
	// parked, and a queue that merely forgot them could not.
	if want := []int{5, 7}; !slices.Equal(q.parked, want) {
		t.Errorf("parked = %v, want %v", q.parked, want)
	}
}

// The two queues are separate: a flagged issue is not ready, but it is not
// parked either, so it has to come back as something the drain can revisit.
func TestSelectableIssuesSeparatesBlockedOnes(t *testing.T) {
	raw := []byte(`[{"number":9,"labels":[{"name":"Awaiting-Answer"}]},
		{"number":4,"labels":[{"name":"awaiting-answer"}]},
		{"number":6,"labels":[]},
		{"number":7,"labels":[{"name":"awaiting-answer"},{"name":"needs-human"}]}]`)

	q, err := selectableIssues(raw)
	if err != nil {
		t.Fatalf("selectableIssues: %v", err)
	}
	if want := []int{6}; !slices.Equal(q.ready, want) {
		t.Errorf("ready = %v, want %v", q.ready, want)
	}
	// Ascending, because the drain revisits the lowest first and gh guarantees
	// no order. 7 is parked as well as flagged, and parked wins.
	if want := []int{4, 9}; !slices.Equal(q.blocked, want) {
		t.Errorf("blocked = %v, want %v", q.blocked, want)
	}
	if want := []int{7}; !slices.Equal(q.parked, want) {
		t.Errorf("parked = %v, want %v", q.parked, want)
	}
}

func TestSelectableIssuesRejectsJunk(t *testing.T) {
	if _, err := selectableIssues([]byte("not json")); err == nil {
		t.Fatal("a payload that is not an issue list must be an error, not an empty queue")
	}
}

func TestParkedErrorsSurviveWrapping(t *testing.T) {
	err := fmt.Errorf("issue #3: %w", park("PR #%d was closed", 12))
	reason, parked := parkReason(err)
	if !parked {
		t.Fatalf("%v should still park after wrapping", err)
	}
	if want := "PR #12 was closed"; reason != want {
		t.Errorf("reason = %q, want %q", reason, want)
	}
	if _, parked := parkReason(errAuth); parked {
		t.Error("a credentials failure must stay fatal")
	}
	if _, parked := parkReason(nil); parked {
		t.Error("no error is not a park")
	}
	// The two have to stay apart: a question is not a decision, so reading one
	// as the other would label a perfectly workable issue needs-human.
	if _, deferred := deferReason(park("PR #12 was closed")); deferred {
		t.Error("a park is not a deferral")
	}
	if _, parked := parkReason(&deferredError{}); parked {
		t.Error("a deferral is not a park")
	}
	de, deferred := deferReason(fmt.Errorf("issue #3: %w", &deferredError{baseline: 4}))
	if !deferred {
		t.Fatal("a deferral should survive wrapping")
	}
	if de.baseline != 4 {
		t.Errorf("baseline = %d, want 4", de.baseline)
	}
}

func TestDrainSummaryReportsEveryOutcome(t *testing.T) {
	got := strings.Join(drainSummary([]issueResult{
		{issue: 1, parked: true, reason: "no PR and no questions"},
		{issue: 2},
		{issue: 5},
		{issue: 3, awaiting: true},
	}, 90*time.Minute), "\n")

	for _, want := range []string{
		"summary: 2 issues merged, 1 issue parked, 1 issue awaiting an answer, 1h30m of wall clock",
		"merged  #2, #5",
		"waiting #3 — reply on the thread",
		"parked  #1 — no PR and no questions",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("summary is missing %q\ngot:\n%s", want, got)
		}
	}
}

// Most drains have nothing waiting, and a bucket that reads "0 issues awaiting
// an answer" on every ordinary run is noise in the one line anybody reads.
func TestDrainSummaryOmitsAnEmptyWaitingBucket(t *testing.T) {
	got := strings.Join(drainSummary([]issueResult{{issue: 2}}, time.Minute), "\n")
	if want := "summary: 1 issue merged, 0 issues parked, 1m of wall clock"; !strings.Contains(got, want) {
		t.Errorf("summary is missing %q\ngot:\n%s", want, got)
	}
	if strings.Contains(got, "waiting") {
		t.Errorf("nothing is waiting, so the summary should not mention it\ngot:\n%s", got)
	}
}

// A drain that never reached an issue has nothing to summarize, and "0 issues
// merged" on every empty backlog is noise.
func TestDrainSummaryIsSilentWithoutResults(t *testing.T) {
	if got := drainSummary(nil, time.Minute); got != nil {
		t.Errorf("summary = %v, want nothing", got)
	}
}

// What a drained backlog cost, in total and issue by issue.
func TestDrainSummaryPricesTheDrainAndEachIssue(t *testing.T) {
	got := strings.Join(drainSummary([]issueResult{
		{issue: 1, parked: true, reason: "no PR and no questions", cost: 2.5},
		{issue: 2, cost: 4},
		{issue: 3, awaiting: true, cost: 0.25},
	}, 90*time.Minute), "\n")

	for _, want := range []string{
		"$6.75 spent, 1h30m of wall clock",
		"merged  #2 ($4.00)",
		"waiting #3 ($0.25)",
		"parked  #1 ($2.50) — no PR and no questions",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("summary is missing %q\ngot:\n%s", want, got)
		}
	}
}

// A run that crashed, stalled or was interrupted never reported a cost, so the
// total is a floor rather than the bill. Unqualified it would read as the
// whole of it — and the caps that read the same number would look broken
// rather than conservative.
func TestDrainSummarySaysWhenTheTotalUndercounts(t *testing.T) {
	got := strings.Join(drainSummary([]issueResult{
		{issue: 1, cost: 4, approximated: 2},
	}, time.Minute), "\n")

	if want := "$4.00 spent (2 runs reported none, so that is an undercount)"; !strings.Contains(got, want) {
		t.Errorf("summary is missing %q\ngot:\n%s", want, got)
	}
}

// A drain that only waited on a PR an earlier process opened spent nothing,
// and "$0.00" reads as a free backlog rather than as an absent number.
func TestDrainSummaryOmitsDollarsItNeverSpent(t *testing.T) {
	got := strings.Join(drainSummary([]issueResult{{issue: 2}}, time.Minute), "\n")
	if strings.Contains(got, "$") {
		t.Errorf("an uncosted drain should print no dollars at all\ngot:\n%s", got)
	}
}

// The point of the cost cap: a run reports what it spent, the cap says that is
// the lot, and the issue is parked with the arithmetic in the reason rather
// than resumed into another bill.
func TestDrainParksAnIssueOverItsCostCap(t *testing.T) {
	buf := captureLog(t)
	cfg, path := drainConfig(t, "costlycrash", &ghState{
		Issues: map[string]*fakeIssue{"1": {Open: true}},
	})
	cfg.maxCost = 1
	cfg.retries = 2 // without the cap, this run would be resumed twice

	if err := drain(context.Background(), cfg); err != nil {
		t.Fatalf("a cap is a park, not a fatal error: %v", err)
	}

	st := finalGhState(t, path)
	if got := st.Issues["1"].Labels; !slices.Contains(got, needsHumanLabel) {
		t.Errorf("issue 1 labels = %v, want %s", got, needsHumanLabel)
	}

	out := buf.String()
	for _, want := range []string{
		"issue #1 needs a human: this shift has spent $9.00 on it, the whole of its -max-cost of $1.00",
		"parked  #1 ($9.00) — this shift has spent $9.00 on it",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log is missing %q\ngot:\n%s", want, out)
		}
	}
	// One run, not three: the cap has to be read before the resume budget, or
	// it only ever fires after -retries has spent the money anyway.
	if got := strings.Count(out, "session started"); got != 1 {
		t.Errorf("%d runs dispatched, want 1 — the cap must be read before the resume", got)
	}
	// And the park has to be reported instead of the resume, not after it: an
	// unattended log that announces a resume, sleeps -retry-wait for it and
	// then parks anyway is a worse diagnosis than the park on its own.
	if strings.Contains(out, "resuming session") || strings.Contains(out, "restarting fresh") {
		t.Errorf("the log promises a resume the cap will refuse\ngot:\n%s", out)
	}
}

// The gap -stall leaves: this run is not silent, it just never stops. The
// budget watchdog kills it, and the kill is a park rather than the crash it
// looks like — a resume would spend the same allowance reaching the same kill.
func TestDrainParksAnIssueOverItsTimeCap(t *testing.T) {
	buf := captureLog(t)
	cfg, path := drainConfig(t, "hang", &ghState{
		Issues: map[string]*fakeIssue{"1": {Open: true}},
	})
	cfg.stall = 0 // only the budget may end this run
	cfg.maxIssueTime = 100 * time.Millisecond
	cfg.retries = 2

	if err := drain(context.Background(), cfg); err != nil {
		t.Fatalf("a cap is a park, not a fatal error: %v", err)
	}

	st := finalGhState(t, path)
	if got := st.Issues["1"].Labels; !slices.Contains(got, needsHumanLabel) {
		t.Errorf("issue 1 labels = %v, want %s", got, needsHumanLabel)
	}

	out := buf.String()
	if !strings.Contains(out, "issue #1 needs a human:") || !strings.Contains(out, "-max-issue-time") {
		t.Errorf("log should park issue 1 and name the cap that did it\ngot:\n%s", out)
	}
	if got := strings.Count(out, "session started"); got != 1 {
		t.Errorf("%d runs dispatched, want 1 — a run the cap killed must not be resumed", got)
	}
}

// The session budget ends the drain rather than parking anything: the operator
// asked to stop spending, not to hand issues back. Everything is on GitHub, so
// raising it and starting again carries on from here.
func TestDrainStopsCleanlyOnTheSessionBudget(t *testing.T) {
	buf := captureLog(t)
	cfg, path := drainConfig(t, "stream", &ghState{
		Issues: map[string]*fakeIssue{
			"1": {Open: true},
			"2": {Open: true},
		},
	})
	cfg.maxSessionCost = 0.4 // one run of the fake CLI costs $0.50

	if err := drain(context.Background(), cfg); err != nil {
		t.Fatalf("an exhausted session budget is a clean exit: %v", err)
	}

	st := finalGhState(t, path)
	if got := st.Issues["2"]; slices.Contains(got.Labels, needsHumanLabel) || got.Comments != 0 {
		t.Errorf("issue 2 = %+v, want it left untouched rather than parked", got)
	}
	if !st.Issues["2"].Open {
		t.Error("issue 2 should still be open — the budget must not close anything")
	}

	out := buf.String()
	if want := "spent $0.50 of its -max-session-cost of $0.40 — stopping here"; !strings.Contains(out, want) {
		t.Errorf("log is missing %q\ngot:\n%s", want, out)
	}
	if strings.Contains(out, "=== issue #2 ===") {
		t.Errorf("issue 2 was picked up after the budget was spent\ngot:\n%s", out)
	}
}

// Waking from sleep is exactly when a gh call fails for a few seconds, because
// the network has not reassociated yet. The paths that wait on something always
// shrugged that off and tried again; the lookups that decide what to work next
// did not, and one of them failing ended the whole backlog.
func TestDrainSurvivesAFlakyGh(t *testing.T) {
	buf := captureLog(t)
	cfg, path := drainConfig(t, "stream", &ghState{
		Issues: map[string]*fakeIssue{"1": {Open: true}},
		// Already merged, so the issue advances with no claude run at all and
		// every gh call the drain makes is one of the lookups under test.
		PRs: map[string]*fakePR{"issue-1": {Number: 9, State: "MERGED"}},
		FailReads: map[string]int{
			"issue list": 1, // deciding what to work
			"pr list":    1, // restart safety, the first thing an issue is put through
			"issue view": 1, // reading whether the merge closed the issue
		},
	})

	if err := drain(context.Background(), cfg); err != nil {
		t.Fatalf("one failed gh call must not end the drain: %v", err)
	}

	if st := finalGhState(t, path); st.Issues["1"].Open {
		t.Error("issue 1 should have been closed after its PR merged")
	}
	out := buf.String()
	for _, want := range []string{
		"transient: listing open issues failed",
		"transient: looking up the PR on branch issue-1 failed",
		"transient: reading #1's state failed",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log is missing %q\ngot:\n%s", want, out)
		}
	}
}

// Bounded, though. "A gh that cannot answer" is one of the few conditions that
// is meant to be fatal: every issue behind this one would hit the same wall, so
// parking them one at a time would only bury the cause.
func TestDrainStopsWhenGhKeepsFailing(t *testing.T) {
	captureLog(t)
	cfg, _ := drainConfig(t, "stream", &ghState{
		Issues:    map[string]*fakeIssue{"1": {Open: true}},
		FailReads: map[string]int{"issue list": ghReads},
	})

	if err := drain(context.Background(), cfg); err == nil {
		t.Fatal("a gh that never answers must still stop the drain")
	}
}

// --- one issue, decision by decision ---

// processIssue holds the switch that decides resume against wait against give
// up, and it is the most intricate thing in the package. The drain tests above
// reach most of its branches, but each one reaches them incidentally, through
// whichever route the issue it was really about happened to take. These drive
// the function itself, one case per branch, so a branch that stops behaving
// fails by name rather than by whichever end-to-end test noticed first.
//
// Runs are counted off the init event the supervisor logs for every
// invocation, and the resume is read out of the same log — no state the fakes
// would have to keep just to be asked about.
func TestProcessIssueDecidesWhatOneRunLeftBehind(t *testing.T) {
	// Every resume the ceiling allows costs a fake claude process and the gh
	// calls around it, so the shipped 20 spends over a minute under -race and
	// ran the bound below out on CI. Three proves the same thing: it is past
	// -retries 1, so only the reset can reach it and only the ceiling stops it.
	const ceiling = 3
	open := func() map[string]*fakeIssue { return map[string]*fakeIssue{"1": {Open: true}} }
	parkedFor := func(t *testing.T, err error) string {
		t.Helper()
		reason, parked := parkReason(err)
		if !parked {
			t.Fatalf("err = %v, want the issue handed back to a human", err)
		}
		return reason
	}

	for _, tc := range []struct {
		name  string
		mode  string
		state *ghState
		// tune takes the subtest's *testing.T because several of these branches
		// turn on what git says about the workspace, which means building a real
		// checkout for the case rather than only setting a field.
		tune func(*testing.T, *config)
		// runs is how many claude invocations this decision should dispatch.
		// Zero is the interesting value: it is the restart-safety guarantee.
		runs  int
		check func(t *testing.T, err error, st *ghState, out string)
	}{
		{
			// The restart-safety invariant: a PR on the branch means the work is
			// done, whatever this process remembers, so the skill is never run
			// again for it. Everything else here is about runs; this is about the
			// one that must not happen.
			name: "a PR already open is supervised rather than re-run",
			mode: "stream",
			state: &ghState{
				Issues: open(),
				PRs: map[string]*fakePR{"issue-1": {
					Number: 9, State: "OPEN", Mergeable: "MERGEABLE",
					Head: "abc123", Checks: []string{"SUCCESS"}, MergeOnRead: 1,
				}},
			},
			runs: 0,
			check: func(t *testing.T, err error, st *ghState, out string) {
				if err != nil {
					t.Errorf("err = %v, want a merge to finish the issue", err)
				}
				if st.Issues["1"].Open {
					t.Error("issue 1 should have been closed once its PR merged")
				}
			},
		},
		{
			name:  "a run that asked something hands the issue back",
			mode:  "asks",
			state: &ghState{Issues: open(), Labels: []string{awaitingAnswerLabel}},
			runs:  1,
			check: func(t *testing.T, err error, st *ghState, out string) {
				deferred, ok := deferReason(err)
				if !ok {
					t.Fatalf("err = %v, want the issue put down for a human", err)
				}
				// Read after the question was posted, so a reply is anything past
				// it — a baseline of zero would make the question itself the reply.
				if deferred.baseline != 1 {
					t.Errorf("baseline = %d, want the question already counted", deferred.baseline)
				}
				if !slices.Contains(st.Issues["1"].Labels, awaitingAnswerLabel) {
					t.Error("the flag is the only durable trace of the question; it must be left up")
				}
			},
		},
		{
			name:  "a crash resumes the session it died in and ships",
			mode:  "crashthenships",
			state: &ghState{Issues: open()},
			tune:  func(_ *testing.T, cfg *config) { cfg.retries = 2 },
			runs:  2,
			check: func(t *testing.T, err error, st *ghState, out string) {
				if err != nil {
					t.Errorf("err = %v, want the resume to finish the issue", err)
				}
				// By ID: the research the dead run did is the whole reason to
				// resume rather than start the issue over.
				if !strings.Contains(out, "resuming session sess-crash") {
					t.Errorf("want the crashed session resumed by id\ngot:\n%s", out)
				}
				if st.Issues["1"].Open {
					t.Error("issue 1 should have been closed once its PR merged")
				}
			},
		},
		{
			// The resume target is not a fact about the world: the session can
			// be gone — its JSONL truncated by a hard kill, or aged out of the
			// CLI's retention — and then every attempt fails on it identically
			// in seconds and the issue parks with the wrong diagnosis. The fresh
			// run is the one that would have worked, so it has to be reached.
			name:  "a session that cannot be resumed falls back to a fresh run",
			mode:  "deadsession",
			state: &ghState{Issues: open()},
			tune:  func(_ *testing.T, cfg *config) { cfg.retries = 3 },
			// Two init events: the fresh run that crashes, and the fresh run
			// that ships. The dead resume between them emits none — which is
			// exactly what the supervisor reads it by.
			runs: 2,
			check: func(t *testing.T, err error, st *ghState, out string) {
				if err != nil {
					t.Errorf("err = %v, want the fresh fallback to finish the issue", err)
				}
				if !strings.Contains(out, "session sess-crash could not be resumed") {
					t.Errorf("want the dead session named and given up on\ngot:\n%s", out)
				}
				if !strings.Contains(out, "restarting fresh") {
					t.Errorf("want the attempt after it announced as a fresh run\ngot:\n%s", out)
				}
				if st.Issues["1"].Open {
					t.Error("issue 1 should have been closed once its PR merged")
				}
			},
		},
		{
			// -retries is there to stop a session that resumes and dies straight
			// back. A run cut off after real work — a laptop that slept, a
			// network that dropped — is not that, and charging it for one parks
			// perfectly healthy issues. So progress resets the budget, and a
			// separate ceiling is what guarantees the loop still ends.
			name:  "a crash that got work done first does not spend the retry budget",
			mode:  "partial",
			state: &ghState{Issues: open()},
			tune:  func(_ *testing.T, cfg *config) { cfg.retries, cfg.resumeCeiling = 1, ceiling },
			// One fresh run and every resume the ceiling allows. The old counter
			// would have stopped at two.
			runs: 1 + ceiling,
			check: func(t *testing.T, err error, st *ghState, out string) {
				want := fmt.Sprintf("claude has been retried %d times on this issue and still has "+
					"not finished it — each run gets somewhere and then dies, which needs a human",
					ceiling)
				if got := parkedFor(t, err); got != want {
					t.Errorf("park reason = %q, want %q", got, want)
				}
				if !strings.Contains(out, "the -retries budget starts over") {
					t.Errorf("want the reset said out loud, since -retries 1 would not explain "+
						"%d runs on its own\ngot:\n%s", 1+ceiling, out)
				}
				// The invocation, not only the announcement. Reading "resume this
				// session or start fresh" off the same counter the reset zeroes
				// makes every one of these retries a silent fresh run: the log
				// still promises the resume, and the crashed session's context is
				// thrown away on the one path that exists to keep it.
				if !strings.Contains(out, "--resume sess-partial") {
					t.Errorf("want the announced resume actually made\ngot:\n%s", out)
				}
			},
		},
		{
			name:  "a crash with the resumes spent parks",
			mode:  "crash",
			state: &ghState{Issues: open()},
			tune:  func(_ *testing.T, cfg *config) { cfg.retries = 1 },
			runs:  2, // the fresh attempt and the one resume it was allowed
			check: func(t *testing.T, err error, st *ghState, out string) {
				if got, want := parkedFor(t, err), "claude crashed and 1 resume attempts failed"; got != want {
					t.Errorf("park reason = %q, want %q", got, want)
				}
			},
		},
		{
			// Nothing crashed, nothing was asked, nothing was produced, and
			// there is nothing on the branch or in the worktree either. That is
			// the clean exit that really did decide nothing, and a machine
			// cannot tell what it means, so it says so rather than retrying its
			// way around it.
			name:  "a clean exit that produced nothing parks",
			mode:  "stream",
			state: &ghState{Issues: open()},
			tune:  func(_ *testing.T, cfg *config) { cfg.retries = 2 },
			runs:  1, // nothing to salvage, so nothing to resume
			check: func(t *testing.T, err error, st *ghState, out string) {
				if got, want := parkedFor(t, err), "the run completed but produced no PR and no questions"; got != want {
					t.Errorf("park reason = %q, want %q", got, want)
				}
			},
		},
		{
			// Issue #42, which is what this whole branch exists for: the run
			// implemented the change, started something in the background, ended
			// its turn expecting to be brought back — and under headless
			// `claude -p` that ends the process. It had not decided nothing; it
			// had decided to wait. Resuming it costs one run, and not resuming it
			// cost a person half an hour of finishing the job by hand.
			name:  "a clean exit that left work behind is resumed rather than parked",
			mode:  "waitsthenships",
			state: &ghState{Issues: open()},
			tune: func(t *testing.T, cfg *config) {
				leftBehind(t, cfg)
				cfg.retries = 2
			},
			runs: 2, // the turn that ended, and the resume that opened the PR
			check: func(t *testing.T, err error, st *ghState, out string) {
				if err != nil {
					t.Errorf("err = %v, want the resumed run to finish the issue", err)
				}
				if st.Issues["1"].Open {
					t.Error("issue 1 should have been closed once its PR merged")
				}
				// The invocation, not only the decision: a resume that quietly
				// restarted the skill from scratch would throw away the context
				// that already holds the whole implementation.
				if !strings.Contains(out, "--resume sess-xyz") {
					t.Errorf("want the session resumed rather than restarted\ngot:\n%s", out)
				}
				// And told the thing that stops it waiting a second time.
				if !strings.Contains(out, "no later turn") {
					t.Errorf("want the resumed run told its turn ends the process\ngot:\n%s", out)
				}
			},
		},
		{
			// The bound on the expensive flavour. Every attempt here burns a
			// complete run rather than failing fast in seconds, and -max-cost is
			// off by default, so a run that keeps deciding to wait has to stop
			// being funded by something.
			name:  "clean-exit resumes stop at their own ceiling",
			mode:  "stream",
			state: &ghState{Issues: open()},
			tune: func(t *testing.T, cfg *config) {
				leftBehind(t, cfg)
				// Deliberately generous, so only the clean-exit ceiling can be
				// what stops this.
				cfg.retries, cfg.resumeCeiling = 5, ceiling
			},
			runs: 1 + cleanExitResumeCeiling,
			check: func(t *testing.T, err error, st *ghState, out string) {
				want := "it has been resumed 2 times after ending a turn without opening a PR " +
					"and has still not opened one, which needs a human"
				if got := parkedFor(t, err); !strings.Contains(got, want) {
					t.Errorf("park reason = %q, want it to name the bound it reached: %q", got, want)
				}
			},
		},
		{
			// The two budgets are one budget. Without this, an issue that
			// alternates crashing and ending its turn draws on two separate
			// ceilings and outlives both of them.
			name:  "a clean-exit resume spends the shared resume budget too",
			mode:  "crashthenwaits",
			state: &ghState{Issues: open()},
			tune: func(t *testing.T, cfg *config) {
				leftBehind(t, cfg)
				// One resume in total, and the crash takes it. The clean exit
				// that follows has work on disk and its own ceiling untouched,
				// and must still be refused.
				cfg.retries, cfg.resumeCeiling = 5, 1
			},
			runs: 2, // the crash, and the one resume the shared ceiling allowed
			check: func(t *testing.T, err error, st *ghState, out string) {
				want := "claude has been retried 1 time on this issue and still has not finished it"
				if got := parkedFor(t, err); !strings.Contains(got, want) {
					t.Errorf("park reason = %q, want it to name the shared budget: %q", got, want)
				}
				// The work is still described, because it is still there and
				// somebody now has to pick it up.
				if got := parkedFor(t, err); !strings.Contains(got, "the run left work behind") {
					t.Errorf("park reason = %q, want it to say what is on disk", got)
				}
			},
		},
		{
			// Fatal rather than parked: a -skill this installation does not have
			// fails identically on every issue behind this one, so parking them
			// one at a time only buries the cause.
			name:  "a skill the session does not have stops the drain",
			mode:  "unknownskill",
			state: &ghState{Issues: open()},
			tune:  func(_ *testing.T, cfg *config) { cfg.retries = 2 },
			runs:  1,
			check: func(t *testing.T, err error, st *ghState, out string) {
				if !errors.Is(err, errNoWork) {
					t.Fatalf("err = %v, want %v", err, errNoWork)
				}
				if _, parked := parkReason(err); parked {
					t.Error("a misconfigured -skill dooms every later issue too, so it cannot be a park")
				}
				if !strings.Contains(err.Error(), "-skill") {
					t.Errorf("err = %v, want it to name the flag to fix", err)
				}
			},
		},
		{
			// Fatal for the same reason, and the reason nothing here retries it:
			// no resume changes the token, so each one buys another 401.
			name:  "refused credentials stop the drain",
			mode:  "authfail",
			state: &ghState{Issues: open()},
			tune:  func(_ *testing.T, cfg *config) { cfg.retries = 2 },
			runs:  1,
			check: func(t *testing.T, err error, st *ghState, out string) {
				if !errors.Is(err, errAuth) {
					t.Fatalf("err = %v, want %v", err, errAuth)
				}
				if _, parked := parkReason(err); parked {
					t.Error("a token the API refuses fails every later issue too, so it cannot be a park")
				}
				if !strings.Contains(err.Error(), "claude auth status") {
					t.Errorf("err = %v, want it to say how to fix the token", err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			buf := captureLog(t)
			cfg, path := drainConfig(t, tc.mode, tc.state)
			cfg.retryWait = time.Millisecond
			if tc.tune != nil {
				tc.tune(t, &cfg)
			}
			// Bounded: several of these branches are the ones that used to wait
			// on something forever, and a hung suite says less than a failure.
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			err := processIssue(ctx, cfg, 1, &issueState{})

			out := buf.String()
			if got := strings.Count(out, "session started"); got != tc.runs {
				t.Errorf("%d runs dispatched, want %d\ngot:\n%s", got, tc.runs, out)
			}
			tc.check(t, err, finalGhState(t, path), out)
		})
	}
}
