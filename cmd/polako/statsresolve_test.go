package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// offlineStats is the config every stats test that isn't about resolution
// runs under: no ghBin, so resolveFromGitHub asks nothing and no test reaches
// a real gh.
var offlineStats = config{claudeBin: "claude", usageTimeout: defaultUsageProbeTimeout}

// Five issues with no terminal record. On example/one: #1's PR merged, #2's
// closed unmerged, #3's is still open, #4 never opened one. On example/two,
// which the token can't see, #9's PR merged — but nobody can say so.
const resolveFixture = `
{"v":1,"kind":"run","ts":"2026-08-20T09:00:00Z","ended":"2026-08-20T09:30:00Z","repo":"example/one","issue":1,"pr":10,"reason":"implement","status":"ok","outcome":"opened_pr","cost_usd":2.00}
{"v":1,"kind":"run","ts":"2026-08-20T10:00:00Z","ended":"2026-08-20T10:30:00Z","repo":"example/one","issue":2,"pr":11,"reason":"implement","status":"ok","outcome":"opened_pr","cost_usd":1.00}
{"v":1,"kind":"run","ts":"2026-08-20T11:00:00Z","ended":"2026-08-20T11:30:00Z","repo":"example/one","issue":3,"pr":12,"reason":"implement","status":"ok","outcome":"opened_pr","cost_usd":1.00}
{"v":1,"kind":"run","ts":"2026-08-20T12:00:00Z","ended":"2026-08-20T12:30:00Z","repo":"example/one","issue":4,"pr":0,"reason":"implement","status":"ok","outcome":"posted_questions","cost_usd":0.50}
{"v":1,"kind":"run","ts":"2026-08-20T13:00:00Z","ended":"2026-08-20T13:30:00Z","repo":"example/two","issue":9,"pr":20,"reason":"implement","status":"ok","outcome":"opened_pr","cost_usd":3.00}
`

func resolveCfg(t *testing.T) config {
	t.Helper()
	return resolveCfgWith(t, nil)
}

// resolveCfgWith is resolveCfg with the fake's state changed first.
func resolveCfgWith(t *testing.T, change func(*ghState)) config {
	t.Helper()
	st := &ghState{
		Repo: "example/one",
		PRs: map[string]*fakePR{
			"issue-1": {Number: 10, State: "MERGED", MergedAt: "2026-08-20T15:00:00Z", ClosedAt: "2026-08-20T15:00:00Z",
				Additions: 100, Deletions: 10, ChangedFiles: 3},
			"issue-2": {Number: 11, State: "CLOSED", ClosedAt: "2026-08-20T16:00:00Z",
				Additions: 40, Deletions: 4, ChangedFiles: 1},
			// Sized far off the others, so a median that counted either of
			// the next two would show it.
			"issue-3": {Number: 12, State: "OPEN", Additions: 999, Deletions: 99, ChangedFiles: 99},
			// Would answer for #9 if its repo were visible, so the test
			// proves the failure path rather than a missing number.
			"issue-9": {Number: 20, State: "MERGED", MergedAt: "2026-08-20T17:00:00Z",
				Additions: 999, Deletions: 99, ChangedFiles: 99},
		},
		HiddenRepos: []string{"example/two"},
	}
	if change != nil {
		change(st)
	}
	return ghStateCfg(t, st)
}

// ghStateCfg is offlineStats pointed at a fake gh holding st.
func ghStateCfg(t *testing.T, st *ghState) config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gh-state.json")
	if err := writeGhState(path, st); err != nil {
		t.Fatalf("writing fake gh state: %v", err)
	}
	cfg := offlineStats
	cfg.ghBin, cfg.env = fakeCLI(t), fakeEnv(fakeGhEnv, path)
	return cfg
}

func resolveDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	body := strings.TrimPrefix(resolveFixture, "\n")
	if err := os.WriteFile(filepath.Join(dir, "example.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestStatsResolvesInFlightIssuesFromGitHub(t *testing.T) {
	t.Parallel()
	cfg, dir := resolveCfg(t), resolveDir(t)
	var out bytes.Buffer
	if err := runStatsWith(cfg, []string{"-metrics", dir}, &out, io.Discard, fixtureNow, report{}); err != nil {
		t.Fatalf("stats: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"terminal 2 — merged 1 (50%), closed unmerged 1 (2 from GitHub)",
		"in flight 3",
		"note GitHub didn't answer for example/two — their in-flight and parked issues stay as they are; rerun to retry",
		// $7.50 over the one merge GitHub supplied.
		"per merged PR $7.50 across 1 merge",
		// Sizes for the two it settled, and no review figure: stats'
		// query doesn't ask GitHub for one.
		"change per issue +70 -7 across 2 files (medians over 2 issues with PR data)",
	} {
		if !hasLine(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	// The page's merge-rate card says so too, not just its issues section.
	page := filepath.Join(t.TempDir(), "report.html")
	if err := runStatsWith(cfg, []string{"-metrics", dir, "-html", page}, io.Discard, io.Discard, fixtureNow, report{}); err != nil {
		t.Fatalf("stats -html: %v", err)
	}
	if b, err := os.ReadFile(page); err != nil || !strings.Contains(string(b), "1 of 2 terminal issues, 2 from GitHub") {
		t.Errorf("the merge-rate card should say 2 came from GitHub (err %v)", err)
	}
	// Nothing written back: the records are exactly what the fixture wrote.
	b, err := os.ReadFile(filepath.Join(dir, "example.jsonl"))
	if err != nil || string(b) != strings.TrimPrefix(resolveFixture, "\n") {
		t.Errorf("stats changed the record file (err %v)", err)
	}
}

func TestStatsResolvedCountsReachJSONAndTheByTable(t *testing.T) {
	t.Parallel()
	cfg, dir := resolveCfg(t), resolveDir(t)
	var out bytes.Buffer
	if err := runStatsWith(cfg, []string{"-metrics", dir, "-json"}, &out, io.Discard, fixtureNow, report{}); err != nil {
		t.Fatalf("stats -json: %v", err)
	}
	var doc struct {
		Issues struct {
			Terminal   map[string]int `json:"terminal"`
			InFlight   int            `json:"in_flight"`
			FromGitHub int            `json:"from_github"`
			GitHubNote string         `json:"github_note"`
		} `json:"issues"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("parsing -json: %v\n%s", err, out.String())
	}
	if doc.Issues.FromGitHub != 2 || doc.Issues.InFlight != 3 ||
		doc.Issues.Terminal[issueMerged] != 1 || doc.Issues.Terminal[issueClosed] != 1 {
		t.Errorf("issues = %+v, want 2 from GitHub, 3 in flight, merged 1, closed_unmerged 1", doc.Issues)
	}
	if !strings.Contains(doc.Issues.GitHubNote, "example/two") {
		t.Errorf("github_note = %q, want it to name example/two", doc.Issues.GitHubNote)
	}

	out.Reset()
	if err := runStatsWith(cfg, []string{"-metrics", dir, "-by", byIssue}, &out, io.Discard, fixtureNow, report{}); err != nil {
		t.Fatalf("stats -by issue: %v", err)
	}
	found := false
	for _, line := range strings.Split(out.String(), "\n") {
		if strings.Contains(line, "example/one#1 ") {
			found = true
			if !strings.Contains(line, "merged") {
				t.Errorf("-by issue row for #1 should read merged: %q", line)
			}
		}
	}
	if !found {
		t.Errorf("no -by issue row for example/one#1:\n%s", out.String())
	}
}

// An issue whose PR is older than a repo's newest 1000 still settles: it's
// asked about by number, not found in a listing. The fake's own `pr list`
// keeps only the newest --limit, as gh does, so a listing would miss it.
func TestStatsResolvesAPROlderThanTheNewest1000(t *testing.T) {
	t.Parallel()
	st := &ghState{Repo: "example/one", PRs: map[string]*fakePR{
		"issue-1": {Number: 10, State: "MERGED", MergedAt: "2026-08-20T15:00:00Z", ClosedAt: "2026-08-20T15:00:00Z",
			Additions: 100, Deletions: 10, ChangedFiles: 3},
	}}
	for n := range 1000 {
		st.PRs[fmt.Sprintf("other-%d", n)] = &fakePR{Number: 11 + n, State: "OPEN"}
	}
	cfg := ghStateCfg(t, st)
	listing := cfg
	listing.ghRepo = "example/one"
	listed, err := gh(context.Background(), listing, "pr", "list", "--state", "all", "--limit", "1000", "--json", "number")
	if err != nil || strings.Contains(string(listed), `"number":10}`) || strings.Count(string(listed), `"number"`) != 1000 {
		t.Fatalf("the fake's listing should hold the newest 1000 PRs and not #10 (err %v)", err)
	}
	dir := t.TempDir()
	body := `{"v":1,"kind":"run","ts":"2026-08-20T09:00:00Z","ended":"2026-08-20T09:30:00Z","repo":"example/one","issue":1,"pr":10,"reason":"implement","status":"ok","outcome":"opened_pr","cost_usd":2.00}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "example.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runStatsWith(cfg, []string{"-metrics", dir}, &out, io.Discard, fixtureNow, report{}); err != nil {
		t.Fatalf("stats: %v", err)
	}
	for _, want := range []string{
		"terminal 1 — merged 1 (100%) (1 from GitHub)",
		"change per issue +100 -10 across 3 files (medians over 1 issue with PR data)",
	} {
		if !hasLine(out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, out.String())
		}
	}
}

// An issue merged off-shift has no record of what it changed, so a change
// line built from the records alone samples only the merges a drain saw.
// GitHub's answer carries the size; a review count would need the review
// bodies, so the review median stays over the drain's own records and says
// so.
func TestStatsResolvedPRsCountTowardTheChangeLine(t *testing.T) {
	t.Parallel()
	cfg, dir := resolveCfg(t), resolveDir(t)
	// One merge a drain saw and enriched, beside the four it never closed.
	onShift := `{"v":1,"kind":"run","ts":"2026-08-20T14:00:00Z","ended":"2026-08-20T14:30:00Z","repo":"example/one","issue":5,"pr":13,"reason":"implement","status":"ok","outcome":"opened_pr","cost_usd":1.00}
{"v":1,"kind":"issue","ts":"2026-08-20T18:00:00Z","repo":"example/one","issue":5,"pr":13,"outcome":"merged","additions":412,"deletions":38,"changed_files":7,"reviews":2}
`
	path := filepath.Join(dir, "example.jsonl")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(before, onShift...), 0o600); err != nil {
		t.Fatal(err)
	}
	ghLog := filepath.Join(t.TempDir(), "gh.log")
	setFakeEnv(&cfg, fakeGhLogEnv, ghLog)

	// The records alone: one issue with PR data.
	var out bytes.Buffer
	if err := runStatsWith(offlineStats, []string{"-metrics", dir}, &out, io.Discard, fixtureNow, report{}); err != nil {
		t.Fatalf("stats offline: %v", err)
	}
	if want := "change per issue +412 -38 across 7 files, 2 reviews (medians over 1 issue with PR data)"; !hasLine(out.String(), want) {
		t.Errorf("missing %q offline in:\n%s", want, out.String())
	}

	// With GitHub: the merged #1 and the closed #2 join it. The open #3 and
	// the unreachable #9 don't — either would have moved every median.
	out.Reset()
	if err := runStatsWith(cfg, []string{"-metrics", dir}, &out, io.Discard, fixtureNow, report{}); err != nil {
		t.Fatalf("stats: %v", err)
	}
	if want := "change per issue +100 -10 across 3 files, 2 reviews (medians over 3 issues with PR data, reviews over 1)"; !hasLine(out.String(), want) {
		t.Errorf("missing %q in:\n%s", want, out.String())
	}

	out.Reset()
	if err := runStatsWith(cfg, []string{"-metrics", dir, "-json"}, &out, io.Discard, fixtureNow, report{}); err != nil {
		t.Fatalf("stats -json: %v", err)
	}
	var doc struct {
		Issues struct {
			Change map[string]int `json:"change_per_issue"`
		} `json:"issues"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("parsing -json: %v\n%s", err, out.String())
	}
	want := map[string]int{"additions_median": 100, "deletions_median": 10, "files_median": 3,
		"reviews_median": 2, "reviews_issues": 1, "issues": 3}
	if !maps.Equal(doc.Issues.Change, want) {
		t.Errorf("change_per_issue = %v, want %v", doc.Issues.Change, want)
	}

	// Per report: one query per repo and no listing, naming every PR the
	// unsettled issues' records name — the open #12 too, asked before
	// anyone knows it's open, but not #13, whose issue a drain settled — and
	// every one of those issues' branches. The hidden repo's null isn't asked
	// again. Nothing asks for PR text, and nothing is written back.
	calls, err := os.ReadFile(ghLog)
	if err != nil {
		t.Fatal(err)
	}
	queries := 0
	for _, call := range strings.Split(strings.TrimSpace(string(calls)), "\n") {
		if strings.HasPrefix(call, "pr list") {
			t.Errorf("stats listed PRs: %q", call)
		}
		if !strings.HasPrefix(call, "api graphql") {
			continue
		}
		queries++
		prs, branches := queryNames(call)
		wantPRs, wantBranches := []string{"10", "11", "12"}, []string{"issue-1", "issue-2", "issue-3", "issue-4"}
		if strings.Contains(call, "name=two") {
			wantPRs, wantBranches = []string{"20"}, []string{"issue-9"}
		}
		if !slices.Equal(prs, wantPRs) || !slices.Equal(branches, wantBranches) {
			t.Errorf("the query named PRs %v and branches %v, want %v and %v: %q", prs, branches, wantPRs, wantBranches, call)
		}
		if !strings.Contains(call, "fragment pr on PullRequest { "+prFields+" }") {
			t.Errorf("the query asks for something other than prFields: %q", call)
		}
		asks := branchAlias.ReplaceAllString(prAlias.ReplaceAllString(call, ""), "")
		for _, field := range []string{"title", "body", "reviews", "comments"} {
			if strings.Contains(asks, field) {
				t.Errorf("the query asks for %q: %q", field, call)
			}
		}
	}
	if queries != 4 {
		t.Errorf("%d api graphql calls, want 4 — one per repo per report:\n%s", queries, calls)
	}
	for _, field := range []string{"title", "body", "reviews", "comments"} {
		if slices.Contains(strings.Fields(prFields), field) {
			t.Errorf("prFields names %q: the query must not bring PR text in", field)
		}
	}
	if after, err := os.ReadFile(path); err != nil || string(after) != string(before)+onShift {
		t.Errorf("stats changed the record file (err %v)", err)
	}
}

// queryNames is the PR numbers and branch names one logged PR query asks
// about, in the order it asks.
func queryNames(call string) (prs, branches []string) {
	for _, m := range prAlias.FindAllStringSubmatch(call, -1) {
		prs = append(prs, m[2])
	}
	for _, m := range branchAlias.FindAllStringSubmatch(call, -1) {
		if _, rest, ok := strings.Cut(call, " -f "+m[2]+"="); ok {
			branch, _, _ := strings.Cut(rest, " ")
			branches = append(branches, branch)
		}
	}
	return prs, branches
}

// oneRepoDir is resolveFixture's example/one records alone: one repo, so
// one query, and FailReads fails the tries a test means.
func oneRepoDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	var lines []string
	for _, line := range strings.Split(strings.TrimPrefix(resolveFixture, "\n"), "\n") {
		if !strings.Contains(line, `"repo":"example/two"`) {
			lines = append(lines, line)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "example.jsonl"), []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// A query the network lost gets one more try, in whatever time is left. One
// lost twice loses its issues' outcomes, and the note says so rather than
// pass a short report off as whole: here it is example/one's only query, so
// the repo is named as one GitHub didn't answer for.
func TestStatsLostQueryIsAskedAgain(t *testing.T) {
	t.Parallel()
	const unreached = "GitHub didn't answer for example/one — their in-flight and parked issues stay as they are; rerun to retry"
	for _, tc := range []struct {
		fails          int
		terminal, note string
	}{
		{fails: 1, terminal: "terminal 2 — merged 1 (50%), closed unmerged 1 (2 from GitHub)"},
		{fails: 2, terminal: "in flight 4", note: unreached},
	} {
		t.Run(fmt.Sprintf("%d lost", tc.fails), func(t *testing.T) {
			t.Parallel()
			// Fresh per report: the fake's failure countdown is spent by it.
			failing := func() config {
				return resolveCfgWith(t, func(st *ghState) { st.FailReads = map[string]int{"api graphql": tc.fails} })
			}
			dir := oneRepoDir(t)
			var out bytes.Buffer
			if err := runStatsWith(failing(), []string{"-metrics", dir}, &out, io.Discard, fixtureNow, report{}); err != nil {
				t.Fatalf("stats: %v", err)
			}
			got := out.String()
			if !hasLine(got, tc.terminal) {
				t.Errorf("missing %q in:\n%s", tc.terminal, got)
			}
			if change := "change per issue +70 -7 across 2 files (medians over 2 issues with PR data)"; tc.note == "" && !hasLine(got, change) {
				t.Errorf("missing %q — the second try should have sized both:\n%s", change, got)
			}
			if tc.note == "" && strings.Contains(got, "note ") {
				t.Errorf("a query that recovered left a note:\n%s", got)
			}
			if tc.note == "" {
				return
			}
			if !hasLine(got, "note "+tc.note) {
				t.Errorf("missing note %q in:\n%s", tc.note, got)
			}
			out.Reset()
			if err := runStatsWith(failing(), []string{"-metrics", dir, "-json"}, &out, io.Discard, fixtureNow, report{}); err != nil {
				t.Fatalf("stats -json: %v", err)
			}
			var doc struct {
				Issues struct {
					GitHubNote string `json:"github_note"`
				} `json:"issues"`
			}
			if err := json.Unmarshal(out.Bytes(), &doc); err != nil || doc.Issues.GitHubNote != tc.note {
				t.Errorf("github_note = %q (err %v), want %q", doc.Issues.GitHubNote, err, tc.note)
			}
		})
	}
}

// A repo that answered some queries isn't named as unreachable, but the
// issues its lost query asked about are counted: 30 issues make two queries,
// 25 and 5, and the second names a PR the network always drops.
func TestStatsPartlyAnsweredRepoCountsWhatsLeftOut(t *testing.T) {
	t.Parallel()
	st := &ghState{Repo: "example/one", PRs: map[string]*fakePR{}, DropQueriesNaming: []int{127}}
	var body strings.Builder
	for n := 1; n <= 30; n++ {
		fmt.Fprintf(&body, `{"v":1,"kind":"run","ts":"2026-08-20T09:00:00Z","repo":"example/one","issue":%d,"pr":%d,"reason":"implement","status":"ok","outcome":"opened_pr","cost_usd":1.00}`+"\n", n, 100+n)
		st.PRs[fmt.Sprintf("issue-%d", n)] = &fakePR{Number: 100 + n, State: "MERGED",
			MergedAt: "2026-08-20T15:00:00Z", ClosedAt: "2026-08-20T15:00:00Z", Additions: 1, ChangedFiles: 1}
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "example.jsonl"), []byte(body.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := ghStateCfg(t, st)
	ghLog := filepath.Join(t.TempDir(), "gh.log")
	setFakeEnv(&cfg, fakeGhLogEnv, ghLog)
	var out bytes.Buffer
	if err := runStatsWith(cfg, []string{"-metrics", dir, "-json"}, &out, io.Discard, fixtureNow, report{}); err != nil {
		t.Fatalf("stats -json: %v", err)
	}
	var doc struct {
		Issues struct {
			InFlight   int    `json:"in_flight"`
			FromGitHub int    `json:"from_github"`
			GitHubNote string `json:"github_note"`
		} `json:"issues"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("parsing -json: %v\n%s", err, out.String())
	}
	want := "GitHub didn't answer about 5 issues — they stay as they are; rerun to retry"
	if doc.Issues.FromGitHub != 25 || doc.Issues.InFlight != 5 || doc.Issues.GitHubNote != want {
		t.Errorf("issues = %+v, want 25 from GitHub, 5 in flight and note %q", doc.Issues, want)
	}
	calls, err := os.ReadFile(ghLog)
	if err != nil {
		t.Fatal(err)
	}
	var asked []string
	for _, call := range strings.Split(strings.TrimSpace(string(calls)), "\n") {
		_, branches := queryNames(call)
		asked = append(asked, fmt.Sprintf("%s-%s", branches[0], branches[len(branches)-1]))
	}
	slices.Sort(asked)
	if w := []string{"issue-1-issue-25", "issue-26-issue-30", "issue-26-issue-30"}; !slices.Equal(asked, w) {
		t.Errorf("queries asked about %v, want %v — the lost one asked twice", asked, w)
	}
}

// The note names an unreachable repo, issues a query lost, or both, in one
// line; with neither there is no line at all.
func TestResolutionNoteSaysWhatGitHubDidntGive(t *testing.T) {
	t.Parallel()
	const stay = " — their in-flight and parked issues stay as they are"
	for _, tc := range []struct {
		res  prResolution
		want string
	}{
		{prResolution{}, ""},
		{prResolution{unreached: []string{"o/a"}}, "GitHub didn't answer for o/a" + stay + "; rerun to retry"},
		{prResolution{unanswered: 1}, "GitHub didn't answer about 1 issue — it stays as it is; rerun to retry"},
		{prResolution{unanswered: 3}, "GitHub didn't answer about 3 issues — they stay as they are; rerun to retry"},
		{prResolution{unreached: []string{"o/a"}, unanswered: 1},
			"GitHub didn't answer for o/a" + stay + ", as does 1 more issue elsewhere; rerun to retry"},
		{prResolution{unreached: []string{"o/a", "o/b"}, unanswered: 3},
			"GitHub didn't answer for o/a and o/b" + stay + ", as do 3 more issues elsewhere; rerun to retry"},
	} {
		if got := tc.res.note(); got != tc.want {
			t.Errorf("note() = %q, want %q", got, tc.want)
		}
	}
}

// GitHub answers a PR number it can't resolve with null beside the rest, and
// gh exits 1 over it; every PR it did answer with still counts, carrying
// every field verdict and the change line read. A repo it can't see is its
// own answer, not asked again; a gh that never answered is.
func TestPRQueryKeepsWhatGitHubAnswered(t *testing.T) {
	t.Parallel()
	issue := func(repo string, n, pr int) *issueStats {
		return &issueStats{key: issueKey{repo, n}, runs: []runRecord{{PR: pr}}}
	}
	q := prQueries(map[string][]*issueStats{"example/one": {issue("example/one", 1, 10), issue("example/one", 7, 98)}}, "issue-")[0]
	ans, retry := q.ask(context.Background(), resolveCfg(t))
	merged := prState{Number: 10, State: "MERGED", MergedAt: "2026-08-20T15:00:00Z", ClosedAt: "2026-08-20T15:00:00Z",
		Additions: 100, Deletions: 10, ChangedFiles: 3}
	if ans == nil || retry {
		t.Fatalf("ask = %v (retry %v), want an answer", ans, retry)
	}
	if want := map[int]prState{10: merged}; !maps.Equal(ans.byNumber, want) {
		t.Errorf("byNumber = %v, want %v", ans.byNumber, want)
	}
	if want := map[string]prState{"issue-1": merged}; !maps.Equal(ans.byBranch, want) {
		t.Errorf("byBranch = %v, want %v — issue-7 has no PR", ans.byBranch, want)
	}

	q = prQueries(map[string][]*issueStats{"example/two": {issue("example/two", 9, 20)}}, "issue-")[0]
	if ans, retry := q.ask(context.Background(), resolveCfg(t)); ans != nil || retry {
		t.Errorf("a repo GitHub can't see gave %v (retry %v), want no answer and no retry", ans, retry)
	}
	cfg := offlineStats
	cfg.ghBin = filepath.Join(t.TempDir(), "no-such-gh")
	if ans, retry := q.ask(context.Background(), cfg); ans != nil || !retry {
		t.Errorf("a gh that isn't there gave %v (retry %v), want no answer and a retry", ans, retry)
	}
}

// A park a merge overturned keeps the size its drain recorded, reviews and
// all: GitHub's answer settles its outcome, and the size beside it goes
// unread. One whose drain had no PR to size gets GitHub's, as does an
// in-flight issue settled through its branch.
func TestStatsSizesOnlyWhatNoDrainSized(t *testing.T) {
	t.Parallel()
	cfg := parkCfgWith(t, func(st *ghState) {
		size := func(branch string, adds, dels, files int) {
			st.PRs[branch].Additions, st.PRs[branch].Deletions, st.PRs[branch].ChangedFiles = adds, dels, files
		}
		size("issue-5", 1, 1, 1) // read, the median would show it
		size("issue-7", 200, 20, 5)
		size("issue-9", 60, 6, 2)
	})
	dir := t.TempDir()
	body := `{"v":1,"kind":"run","ts":"2026-08-20T09:00:00Z","ended":"2026-08-20T09:30:00Z","repo":"example/three","issue":5,"pr":30,"reason":"implement","status":"ok","outcome":"opened_pr","cost_usd":2.00}
{"v":1,"kind":"issue","ts":"2026-08-20T10:00:00Z","repo":"example/three","issue":5,"pr":30,"outcome":"needs_human","park_reason":"checks_remediation","additions":412,"deletions":38,"changed_files":7,"reviews":2}
{"v":1,"kind":"run","ts":"2026-08-20T09:00:00Z","ended":"2026-08-20T09:30:00Z","repo":"example/three","issue":7,"pr":0,"reason":"implement","status":"ok","outcome":"nothing","cost_usd":1.00}
{"v":1,"kind":"issue","ts":"2026-08-20T10:00:00Z","repo":"example/three","issue":7,"pr":0,"outcome":"needs_human","park_reason":"produced_nothing"}
{"v":1,"kind":"run","ts":"2026-08-20T09:00:00Z","ended":"2026-08-20T09:30:00Z","repo":"example/three","issue":9,"pr":0,"reason":"implement","status":"ok","outcome":"posted_questions","cost_usd":0.50}
`
	if err := os.WriteFile(filepath.Join(dir, "example.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runStatsWith(cfg, []string{"-metrics", dir}, &out, io.Discard, fixtureNow, report{}); err != nil {
		t.Fatalf("stats: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"terminal 3 — merged 3 (100%, 2 after a park) (3 from GitHub)",
		"change per issue +200 -20 across 5 files, 2 reviews (medians over 3 issues with PR data, reviews over 1)",
	} {
		if !hasLine(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

// A query asks about at most statsQueryIssues issues, in issue order
// whatever order they came in, and names a PR once however many issues
// recorded it. Each issue's branch is the prefix and its number.
func TestPRQueriesSplitAndDedupe(t *testing.T) {
	t.Parallel()
	var many []*issueStats
	for n := 2*statsQueryIssues + 1; n > 0; n-- {
		many = append(many, &issueStats{key: issueKey{"o/a", n}})
	}
	shared := []*issueStats{
		{key: issueKey{"o/b", 3}, runs: []runRecord{{PR: 7}}},
		{key: issueKey{"o/b", 2}, runs: []runRecord{{PR: 7}, {PR: 0}}},
	}
	queries := prQueries(map[string][]*issueStats{"o/b": shared, "o/a": many}, "feat-")
	var got []string
	for _, q := range queries {
		got = append(got, fmt.Sprintf("%s %s..%s (%d)", q.repo, q.branches[0], q.branches[len(q.branches)-1], len(q.issues)))
	}
	want := []string{"o/a feat-1..feat-25 (25)", "o/a feat-26..feat-50 (25)", "o/a feat-51..feat-51 (1)", "o/b feat-2..feat-3 (2)"}
	if !slices.Equal(got, want) {
		t.Errorf("queries = %q, want %q", got, want)
	}
	if text := queries[3].text(); strings.Count(text, "pullRequest(number: 7)") != 1 || strings.Count(text, "pullRequests(headRefName:") != 2 {
		t.Errorf("o/b's query should name PR 7 once and both branches: %q", text)
	}
	// The fake holds one PR per branch, so it can't tell the newest from
	// the oldest: the query's own words have to.
	if text := queries[3].text(); strings.Count(text, "first: 1, orderBy: {field: CREATED_AT, direction: DESC}") != 2 {
		t.Errorf("each branch lookup should ask for its newest PR alone: %q", text)
	}
}

// With every issue GitHub-resolved there is no review count at all, so the
// line carries no review figure rather than a median of zeros.
func TestChangeSummaryOverResolvedIssuesAloneHasNoReviews(t *testing.T) {
	t.Parallel()
	done := []*issueStats{
		{terminal: &issueRecord{Outcome: issueMerged, Additions: 10, Deletions: 1, ChangedFiles: 2}, resolved: true},
		{terminal: &issueRecord{Outcome: issueMerged, Additions: 30, Deletions: 3, ChangedFiles: 4}, resolved: true},
	}
	c := buildChangeSummary(done)
	if c == nil || c.hasReviews || c.reviewsN != 0 || c.n != 2 {
		t.Fatalf("change = %+v, want n 2 and no reviews", c)
	}
	got := changePairsFrom(c)
	if want := "+20 -2 across 3 files (medians over 2 issues with PR data)"; len(got) != 1 || got[0][1] != want {
		t.Errorf("change line = %v, want %q", got, want)
	}
}

// Where a drain's record and GitHub's answer both carry a size, the drain's
// wins: it comes with a review count, and it's the record a park keeps.
func TestChangeRecordPrefersTheDrainsOwn(t *testing.T) {
	t.Parallel()
	park := &issueRecord{Outcome: issueNeedsHuman, Additions: 412, ChangedFiles: 7, Reviews: 2}
	gh := &issueRecord{Outcome: issueMerged, Additions: 1, ChangedFiles: 1}
	if rec, reviewed := (&issueStats{terminal: gh, resolved: true, park: park}).changeRecord(); rec != park || !reviewed {
		t.Errorf("changeRecord = %+v (reviewed %v), want the park's own record", rec, reviewed)
	}
	if rec, reviewed := (&issueStats{terminal: gh, resolved: true, park: &issueRecord{Outcome: issueNeedsHuman}}).changeRecord(); rec != gh || reviewed {
		t.Errorf("changeRecord = %+v (reviewed %v), want GitHub's, unreviewed, for a park with no size", rec, reviewed)
	}
}

// A terminal GitHub supplied never had usage samples to miss, so it doesn't
// swell the plan-cost line's "no usable reading" count.
func TestResolvedIssuesAreNotCountedUnsampled(t *testing.T) {
	t.Parallel()
	issues := []*issueStats{
		{terminal: &issueRecord{Outcome: issueMerged}},                 // a record the gate missed
		{terminal: &issueRecord{Outcome: issueMerged}, resolved: true}, // GitHub's answer
	}
	if got := buildPlanCostSummary(issues, nil).unsampled; got != 1 {
		t.Errorf("unsampled = %d, want 1 — the resolved issue isn't the gate's miss", got)
	}
}

// -shift is that shift's verdict: another shift's merge record was filtered
// out on purpose, so GitHub isn't asked to fill it back in.
func TestStatsShiftFilterLeavesResolutionOff(t *testing.T) {
	t.Parallel()
	cfg, dir := resolveCfg(t), resolveDir(t)
	var out bytes.Buffer
	if err := runStatsWith(cfg, []string{"-metrics", dir, "-shift", noneGroup}, &out, io.Discard, fixtureNow, report{}); err != nil {
		t.Fatalf("stats -shift: %v", err)
	}
	if got := out.String(); strings.Contains(got, "from GitHub") || !hasLine(got, "in flight 5") {
		t.Errorf("-shift should leave every issue in flight, unresolved:\n%s", got)
	}
}

// An unreachable gh — here, one that isn't there at all — leaves every issue
// in flight and still prints the report.
func TestStatsUnreachableGitHubNeverFailsTheReport(t *testing.T) {
	t.Parallel()
	cfg := offlineStats
	cfg.ghBin = filepath.Join(t.TempDir(), "no-such-gh")
	var out bytes.Buffer
	if err := runStatsWith(cfg, []string{"-metrics", resolveDir(t)}, &out, io.Discard, fixtureNow, report{}); err != nil {
		t.Fatalf("stats failed over an unreachable GitHub: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"none yet — every issue in this window is still in flight",
		"in flight 5",
		"GitHub didn't answer for example/one and example/two",
	} {
		if !hasLine(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

// Parks the queue never revisits, and in-flight issues no run recorded a PR
// for, all on example/three. #5's PR merged after its park: a human fixed
// the checks and merged it. #6's PR is still open. #7 parked having opened
// nothing, and an issue-7 PR no run recorded merged later. #8 merged once,
// was reopened and parked on its second try, so that first merge is older
// than the park and the park stands. #9 is in flight with no PR recorded,
// and its issue-9 PR merged. #10's issue-10 PR merged before #10's first
// run — earlier work under the same branch name — so #10 stays in flight.
const parkFixture = `
{"v":1,"kind":"run","ts":"2026-08-20T09:00:00Z","ended":"2026-08-20T09:30:00Z","repo":"example/three","issue":5,"pr":30,"reason":"implement","status":"ok","outcome":"opened_pr","cost_usd":2.00}
{"v":1,"kind":"issue","ts":"2026-08-20T10:00:00Z","repo":"example/three","issue":5,"pr":30,"outcome":"needs_human","park_reason":"checks_remediation"}
{"v":1,"kind":"run","ts":"2026-08-20T09:00:00Z","ended":"2026-08-20T09:30:00Z","repo":"example/three","issue":6,"pr":31,"reason":"implement","status":"ok","outcome":"opened_pr","cost_usd":2.00}
{"v":1,"kind":"issue","ts":"2026-08-20T10:00:00Z","repo":"example/three","issue":6,"pr":31,"outcome":"needs_human","park_reason":"budget"}
{"v":1,"kind":"run","ts":"2026-08-20T09:00:00Z","ended":"2026-08-20T09:30:00Z","repo":"example/three","issue":7,"pr":0,"reason":"implement","status":"ok","outcome":"nothing","cost_usd":1.00}
{"v":1,"kind":"issue","ts":"2026-08-20T10:00:00Z","repo":"example/three","issue":7,"pr":0,"outcome":"needs_human","park_reason":"produced_nothing"}
{"v":1,"kind":"run","ts":"2026-08-20T07:00:00Z","ended":"2026-08-20T07:30:00Z","repo":"example/three","issue":8,"pr":33,"reason":"implement","status":"ok","outcome":"opened_pr","cost_usd":1.00}
{"v":1,"kind":"issue","ts":"2026-08-20T08:00:00Z","repo":"example/three","issue":8,"pr":33,"outcome":"merged"}
{"v":1,"kind":"run","ts":"2026-08-20T09:00:00Z","ended":"2026-08-20T09:30:00Z","repo":"example/three","issue":8,"pr":0,"reason":"implement","status":"ok","outcome":"nothing","cost_usd":1.00}
{"v":1,"kind":"issue","ts":"2026-08-20T10:00:00Z","repo":"example/three","issue":8,"pr":0,"outcome":"needs_human","park_reason":"produced_nothing"}
{"v":1,"kind":"run","ts":"2026-08-20T09:00:00Z","ended":"2026-08-20T09:30:00Z","repo":"example/three","issue":9,"pr":0,"reason":"implement","status":"ok","outcome":"posted_questions","cost_usd":0.50}
{"v":1,"kind":"run","ts":"2026-08-20T09:00:00Z","ended":"2026-08-20T09:30:00Z","repo":"example/three","issue":10,"pr":0,"reason":"implement","status":"ok","outcome":"posted_questions","cost_usd":0.50}
`

func parkCfg(t *testing.T) config {
	t.Helper()
	return parkCfgWith(t, nil)
}

// parkCfgWith is parkCfg with the fake's state changed first.
func parkCfgWith(t *testing.T, change func(*ghState)) config {
	t.Helper()
	st := &ghState{
		Repo: "example/three",
		PRs: map[string]*fakePR{
			"issue-5":  {Number: 30, State: "MERGED", MergedAt: "2026-08-20T12:00:00Z", ClosedAt: "2026-08-20T12:00:00Z"},
			"issue-6":  {Number: 31, State: "OPEN"},
			"issue-7":  {Number: 40, State: "MERGED", MergedAt: "2026-08-20T14:00:00Z", ClosedAt: "2026-08-20T14:00:00Z"},
			"issue-8":  {Number: 33, State: "MERGED", MergedAt: "2026-08-20T08:00:00Z", ClosedAt: "2026-08-20T08:00:00Z"},
			"issue-9":  {Number: 41, State: "MERGED", MergedAt: "2026-08-20T15:00:00Z", ClosedAt: "2026-08-20T15:00:00Z"},
			"issue-10": {Number: 34, State: "MERGED", MergedAt: "2026-08-19T12:00:00Z", ClosedAt: "2026-08-19T12:00:00Z"},
		},
	}
	if change != nil {
		change(st)
	}
	return ghStateCfg(t, st)
}

func parkDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	body := strings.TrimPrefix(parkFixture, "\n")
	if err := os.WriteFile(filepath.Join(dir, "example.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestStatsResolvesParkedIssuesMergedSince(t *testing.T) {
	t.Parallel()
	cfg, dir := parkCfg(t), parkDir(t)
	var out bytes.Buffer
	if err := runStatsWith(cfg, []string{"-metrics", dir}, &out, io.Discard, fixtureNow, report{}); err != nil {
		t.Fatalf("stats: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		// #5 and #7 merged after their parks, #9 from in flight.
		"terminal 5 — merged 3 (60%, 2 after a park), needs human 2 (3 from GitHub)",
		// Every park still counts, merged since or not.
		"park reasons budget 1, produced nothing 2, checks remediation 1",
		"in flight 1",
		"per merged PR $2.67 across 3 merges",
	} {
		if !hasLine(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}

	out.Reset()
	if err := runStatsWith(cfg, []string{"-metrics", dir, "-by", byIssue}, &out, io.Discard, fixtureNow, report{}); err != nil {
		t.Fatalf("stats -by issue: %v", err)
	}
	want := map[string]string{"#5 ": "merged", "#6 ": "needs human", "#7 ": "merged",
		"#8 ": "needs human", "#9 ": "merged", "#10 ": "in flight"}
	for issue, outcome := range want {
		found := false
		for _, line := range strings.Split(out.String(), "\n") {
			if strings.Contains(line, "example/three"+issue) {
				found = true
				if !strings.Contains(line, outcome) {
					t.Errorf("-by issue row for %s should read %s: %q", issue, outcome, line)
				}
			}
		}
		if !found {
			t.Errorf("no -by issue row for example/three%s:\n%s", issue, out.String())
		}
	}

	page := filepath.Join(t.TempDir(), "report.html")
	if err := runStatsWith(cfg, []string{"-metrics", dir, "-html", page}, io.Discard, io.Discard, fixtureNow, report{}); err != nil {
		t.Fatalf("stats -html: %v", err)
	}
	if b, err := os.ReadFile(page); err != nil || !strings.Contains(string(b), "3 of 5 terminal issues, 2 after a park, 3 from GitHub") {
		t.Errorf("the merge-rate card should name the merges after a park (err %v)", err)
	}

	b, err := os.ReadFile(filepath.Join(dir, "example.jsonl"))
	if err != nil || string(b) != strings.TrimPrefix(parkFixture, "\n") {
		t.Errorf("stats changed the record file (err %v)", err)
	}
}

func TestStatsMergedAfterParkReachesJSON(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	if err := runStatsWith(parkCfg(t), []string{"-metrics", parkDir(t), "-json"}, &out, io.Discard, fixtureNow, report{}); err != nil {
		t.Fatalf("stats -json: %v", err)
	}
	var doc struct {
		Issues struct {
			Terminal        map[string]int `json:"terminal"`
			InFlight        int            `json:"in_flight"`
			FromGitHub      int            `json:"from_github"`
			MergedAfterPark int            `json:"merged_after_park"`
			ParkReasons     map[string]int `json:"park_reasons"`
		} `json:"issues"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("parsing -json: %v\n%s", err, out.String())
	}
	iss := doc.Issues
	if iss.Terminal[issueMerged] != 3 || iss.Terminal[issueNeedsHuman] != 2 || iss.InFlight != 1 ||
		iss.FromGitHub != 3 || iss.MergedAfterPark != 2 {
		t.Errorf("issues = %+v, want merged 3, needs_human 2, in flight 1, 3 from GitHub, 2 after a park", iss)
	}
	if iss.ParkReasons[parkChecks] != 1 || iss.ParkReasons[parkBudget] != 1 || iss.ParkReasons[parkNothing] != 2 {
		t.Errorf("park_reasons = %v, want every park counted, merged since or not", iss.ParkReasons)
	}
}

// -shift is that shift's verdict, and a park is a verdict: nothing a later
// merge did reaches it.
func TestStatsShiftFilterLeavesParksParked(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	if err := runStatsWith(parkCfg(t), []string{"-metrics", parkDir(t), "-shift", noneGroup}, &out, io.Discard, fixtureNow, report{}); err != nil {
		t.Fatalf("stats -shift: %v", err)
	}
	if got := out.String(); strings.Contains(got, "after a park") || !hasLine(got, "merged 0 (0%), needs human 4") {
		t.Errorf("-shift should leave every park parked:\n%s", got)
	}
}

// -branch-prefix is the issue-N contract's other half, as it is for status
// and tidy: the branch fallback looks where the skill was told to push.
func TestStatsBranchFallbackHonoursBranchPrefix(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	args := []string{"-metrics", parkDir(t), "-branch-prefix", "feat-"}
	if err := runStatsWith(parkCfg(t), args, &out, io.Discard, fixtureNow, report{}); err != nil {
		t.Fatalf("stats -branch-prefix: %v", err)
	}
	// Only #5's recorded PR answers now: no feat-N branch exists.
	if got := out.String(); !hasLine(got, "merged 1 (25%, 1 after a park), needs human 3 (1 from GitHub)") {
		t.Errorf("a prefix no branch carries should leave only the recorded PR's answer:\n%s", got)
	}
}

// verdict's edge cases, on answers the fake gh can't give: two PRs on one
// branch, and a recorded PR on another branch than the issue's own.
func TestVerdictWeighsBothCandidates(t *testing.T) {
	t.Parallel()
	run := []runRecord{{TS: "2026-08-20T09:00:00Z", PR: 10}}
	park := &issueRecord{TS: "2026-08-20T10:00:00Z", Outcome: issueNeedsHuman, PR: 10}
	pr := func(n int, state, ended string) prState {
		st := prState{Number: n, State: state, ClosedAt: ended}
		if state == "MERGED" {
			st.MergedAt = ended
		}
		return st
	}
	for _, tc := range []struct {
		name             string
		terminal         *issueRecord
		recorded, branch prState
		want             string // "" for no verdict
		wantPR           int
	}{
		{"in flight, recorded closed, branch open", nil, pr(10, "CLOSED", "2026-08-20T11:00:00Z"), pr(15, "OPEN", ""), "", 0},
		{"in flight, both closed", nil, pr(10, "CLOSED", "2026-08-20T11:00:00Z"), pr(15, "CLOSED", "2026-08-20T12:00:00Z"), issueClosed, 15},
		{"in flight, recorded closed, branch merged", nil, pr(10, "CLOSED", "2026-08-20T11:00:00Z"), pr(15, "MERGED", "2026-08-20T12:00:00Z"), issueMerged, 15},
		{"parked, recorded closed after the park", park, pr(10, "CLOSED", "2026-08-20T11:00:00Z"), prState{}, "", 0},
		{"parked, recorded merged at the park itself", park, pr(10, "MERGED", "2026-08-20T10:00:00Z"), prState{}, "", 0},
		{"parked, branch merged with no time", park, prState{}, pr(15, "MERGED", ""), "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			is := &issueStats{key: issueKey{"o/n", 1}, runs: run, terminal: tc.terminal}
			a := prAnswers{byNumber: map[int]prState{}, byBranch: map[string]prState{}}
			if tc.recorded.Number != 0 {
				a.byNumber[tc.recorded.Number] = tc.recorded
			}
			if tc.branch.Number != 0 {
				a.byBranch["issue-1"] = tc.branch
			}
			rec, ok := a.verdict(is, "issue-1")
			if tc.want == "" {
				if ok {
					t.Errorf("verdict = %+v, want none", rec)
				}
				return
			}
			if !ok || rec.Outcome != tc.want || rec.PR != tc.wantPR {
				t.Errorf("verdict = %+v (ok %v), want %s on PR %d", rec, ok, tc.want, tc.wantPR)
			}
		})
	}
}

// An overturned park's own record still answers for the fields only a
// record carries: its park reason and its usage samples.
func TestOverturnedParkKeepsItsRecordsFields(t *testing.T) {
	t.Parallel()
	park := &issueRecord{Outcome: issueNeedsHuman, ParkReason: parkChecks, WeekUsageAtPickup: 10, WeekUsageAtTerminal: 12}
	is := &issueStats{terminal: &issueRecord{Outcome: issueMerged}, resolved: true, park: park}
	if got := buildParkReasons([]*issueStats{is}); got[parkChecks] != 1 {
		t.Errorf("park reasons = %v, want the overturned park counted", got)
	}
	if got := buildPlanCostSummary([]*issueStats{is}, nil); got.n != 1 || got.unsampled != 0 {
		t.Errorf("plan cost n = %d, unsampled = %d — want the park's own samples read", got.n, got.unsampled)
	}
}
