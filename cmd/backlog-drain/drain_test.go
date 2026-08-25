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
const fakeGhEnv = "BACKLOG_DRAIN_FAKE_GH"

// ghSubcommands are the first arguments that mean "this invocation is gh".
// The test process exports both fake-CLI variables at once and children
// inherit them, so argv is what tells the two impersonations apart.
var ghSubcommands = []string{"repo", "issue", "pr", "label"}

// ghState is the whole of a pretend repository.
type ghState struct {
	Repo   string                `json:"repo"`
	Issues map[string]*fakeIssue `json:"issues"`
	PRs    map[string]*fakePR    `json:"prs"`    // keyed by head branch
	Labels []string              `json:"labels"` // labels the repo has defined
}

type fakeIssue struct {
	Open     bool     `json:"open"`
	Labels   []string `json:"labels"`
	Comments int      `json:"comments"`

	// ReplyOnRead is a human answering a question while the supervisor polls:
	// their comment appears on the Nth read of the thread from now. Counting
	// reads rather than wall clock keeps the test deterministic — the drain
	// unblocks because the thread moved, never because a timer went off.
	ReplyOnRead int `json:"reply_on_read"`
}

type fakePR struct {
	Number    int    `json:"number"`
	State     string `json:"state"`
	Mergeable string `json:"mergeable"`
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

// fakeGh answers one gh invocation and persists anything it changed.
func fakeGh(path string, args []string) int {
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

	switch at(0) + " " + at(1) {
	case "repo view":
		return st.Repo + "\n", false, 0

	case "issue list":
		return listIssues(st, flagVal("--label")), false, 0

	case "issue view":
		is := issue()
		if is == nil {
			fmt.Fprintf(os.Stderr, "no issue #%s\n", at(2))
			return "", false, 1
		}
		if strings.Contains(flagVal("--json"), "comments") {
			counting := is.ReplyOnRead > 0
			if counting {
				is.ReplyOnRead--
				if is.ReplyOnRead == 0 {
					is.Comments++ // the human's reply lands on this read
				}
			}
			// The countdown has to be persisted even on the reads before the
			// reply, or every read would start it over.
			return `{"comments":[` + strings.TrimSuffix(strings.Repeat("{},", is.Comments), ",") + `]}`,
				counting, 0
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
		pr, ok := st.PRs[flagVal("--head")]
		if !ok {
			return "[]", false, 0
		}
		return fmt.Sprintf(`[{"number":%d,"state":%q,"url":"https://example.invalid/pr/%d"}]`,
			pr.Number, pr.State, pr.Number), false, 0

	case "pr view":
		for _, pr := range st.PRs {
			if strconv.Itoa(pr.Number) == at(2) {
				return fmt.Sprintf(`{"state":%q,"mergeable":%q}`, pr.State, pr.Mergeable), false, 0
			}
		}
		fmt.Fprintf(os.Stderr, "no PR #%s\n", at(2))
		return "", false, 1
	}
	fmt.Fprintf(os.Stderr, "fake gh: unhandled call %q\n", strings.Join(args, " "))
	return "", false, 1
}

// listIssues renders `gh issue list --json number,labels` in ascending order,
// which is the order the real thing is only relied on not to guarantee.
func listIssues(st *ghState, label string) string {
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
		if !is.Open || (label != "" && !slices.Contains(is.Labels, label)) {
			continue
		}
		var labels []string
		for _, l := range is.Labels {
			labels = append(labels, fmt.Sprintf(`{"name":%q}`, l))
		}
		rows = append(rows, fmt.Sprintf(`{"number":%d,"labels":[%s]}`, n, strings.Join(labels, ",")))
	}
	return "[" + strings.Join(rows, ",") + "]"
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
		dir:            t.TempDir(), // not a checkout: worktree cleanup is best-effort
		claudeBin:      os.Args[0],
		ghBin:          os.Args[0],
		skill:          defaultSkill,
		branchPrefix:   "issue-",
		permissionMode: "acceptEdits",
		tools:          "Read",
		poll:           10 * time.Millisecond,
		stall:          10 * time.Second,
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
		"=== issue #2 ===",
		"summary: 1 issue merged, 1 issue parked",
		"merged  #2",
		"parked  #1 — the run completed but produced no PR and no questions",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log is missing %q\ngot:\n%s", want, out)
		}
	}
}

// The whole question round, end to end: a run that asks something flags the
// issue, the drain waits on that flag, and the re-run that finds the answer
// clears it and ships. The claude invocation is checked too — the run can only
// raise the flag if the allowlist it was handed permits it, pinned to this
// issue and no other.
func TestDrainWaitsForAnAnswerThenFoldsItIn(t *testing.T) {
	buf := captureLog(t)
	cfg, path := drainConfig(t, "asks", &ghState{
		Issues: map[string]*fakeIssue{"1": {Open: true}},
		Labels: []string{awaitingAnswerLabel},
	})

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
		"new activity on #1 — re-running to fold the answers in",
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
		"new activity on #1 — re-running to fold the answers in",
		"resuming session sess-xyz",
		"claude crashed and 1 resume attempts failed",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log is missing %q\ngot:\n%s", want, out)
		}
	}
	// One question, one wait. A second would be the bug.
	if got := strings.Count(out, "waiting for a reply on the thread"); got != 1 {
		t.Errorf("waited on the flag %d times, want exactly 1\ngot:\n%s", got, out)
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

	got, err := selectableIssues(raw)
	if err != nil {
		t.Fatalf("selectableIssues: %v", err)
	}
	// 5 proves the match ignores case, the way GitHub treats label names.
	if want := []int{4, 6}; !slices.Equal(got, want) {
		t.Errorf("selectable = %v, want %v", got, want)
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
}

func TestDrainSummaryReportsBothOutcomes(t *testing.T) {
	got := strings.Join(drainSummary([]issueResult{
		{issue: 1, parked: true, reason: "no PR and no questions"},
		{issue: 2},
		{issue: 5},
	}, 90*time.Minute), "\n")

	for _, want := range []string{
		"summary: 2 issues merged, 1 issue parked, 1h30m of wall clock",
		"merged  #2, #5",
		"parked  #1 — no PR and no questions",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("summary is missing %q\ngot:\n%s", want, got)
		}
	}
}

// A drain that never reached an issue has nothing to summarize, and "0 issues
// merged" on every empty backlog is noise.
func TestDrainSummaryIsSilentWithoutResults(t *testing.T) {
	if got := drainSummary(nil, time.Minute); got != nil {
		t.Errorf("summary = %v, want nothing", got)
	}
}
