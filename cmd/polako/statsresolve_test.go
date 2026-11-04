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
		// Sizes for the two it settled, and no review figure: GitHub's
		// size query doesn't give one.
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

// An issue merged off-shift has no record of what it changed, so a change
// line built from the records alone samples only the merges a drain saw.
// GitHub's size query supplies the size; a review count would need the
// review bodies, so the review median stays over the drain's own records and
// says so.
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

	// Per report: one listing per repo, and beside each one size query
	// naming the PRs its records name with no size — the open #12 too,
	// asked before anyone knows it's open, but not #13, which its drain
	// already sized. Both settled PRs answered, so no second round. Nothing
	// asks for PR text, and nothing is written back.
	calls, err := os.ReadFile(ghLog)
	if err != nil {
		t.Fatal(err)
	}
	lists, queries := 0, 0
	for _, call := range strings.Split(strings.TrimSpace(string(calls)), "\n") {
		switch {
		case strings.HasPrefix(call, "pr list"):
			lists++
			if !strings.Contains(call, "--json "+prListFields) {
				t.Errorf("pr list asked for other fields: %q", call)
			}
		case strings.HasPrefix(call, "api graphql"):
			queries++
			var named []string
			for _, m := range sizeAlias.FindAllStringSubmatch(call, -1) {
				named = append(named, m[2])
			}
			want := []string{"10", "11", "12"}
			if strings.Contains(call, "name=two") {
				want = []string{"20"}
			}
			if !slices.Equal(named, want) {
				t.Errorf("the size query named PRs %v, want %v: %q", named, want, call)
			}
			asks := sizeAlias.ReplaceAllString(call, "")
			for _, field := range []string{"title", "body", "reviews", "comments"} {
				if strings.Contains(asks, field) {
					t.Errorf("the size query asks for %q: %q", field, call)
				}
			}
		}
	}
	if lists != 4 || queries != 4 {
		t.Errorf("%d pr list and %d api graphql calls, want 4 of each:\n%s", lists, queries, calls)
	}
	for _, field := range []string{"title", "body", "reviews", "comments"} {
		if slices.Contains(strings.Split(prListFields, ","), field) {
			t.Errorf("prListFields names %q: the listing must not bring PR text in", field)
		}
	}
	if after, err := os.ReadFile(path); err != nil || string(after) != string(before)+onShift {
		t.Errorf("stats changed the record file (err %v)", err)
	}
}

// oneRepoDir is resolveFixture's example/one records alone: one repo, so
// one size query a round, and FailReads fails the round a test means.
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

// A size round that fails gets one more try, after the listing, in whatever
// time is left. One that fails twice costs the sizes, never the outcomes:
// both issues still settle, the change line counts neither rather than as
// zeros, and the note says so rather than pass a short line off as whole.
func TestStatsSizeFailureKeepsTheOutcome(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		fails      int
		change     string
		note, json string
	}{
		{fails: 1, change: "change per issue +70 -7 across 2 files (medians over 2 issues with PR data)"},
		{fails: 2,
			note: "note GitHub gave no PR size for 2 issues it settled, so change per issue leaves them out; rerun to retry",
			json: "GitHub gave no PR size for 2 issues it settled, so change per issue leaves them out; rerun to retry"},
	} {
		t.Run(fmt.Sprintf("%d failed", tc.fails), func(t *testing.T) {
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
			if !hasLine(got, "terminal 2 — merged 1 (50%), closed unmerged 1 (2 from GitHub)") {
				t.Errorf("a failed size query lost an outcome:\n%s", got)
			}
			if tc.change != "" && !hasLine(got, tc.change) {
				t.Errorf("missing %q — the second try should have sized both:\n%s", tc.change, got)
			}
			if tc.change == "" && strings.Contains(got, "\n  change per issue") {
				t.Errorf("sizes GitHub never gave produced a change line:\n%s", got)
			}
			if tc.note != "" && !hasLine(got, tc.note) {
				t.Errorf("missing %q in:\n%s", tc.note, got)
			}
			if tc.note == "" && strings.Contains(got, "note ") {
				t.Errorf("a size round that recovered left a note:\n%s", got)
			}
			if tc.json == "" {
				return
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
			if err := json.Unmarshal(out.Bytes(), &doc); err != nil || doc.Issues.GitHubNote != tc.json {
				t.Errorf("github_note = %q (err %v), want %q", doc.Issues.GitHubNote, err, tc.json)
			}
		})
	}
}

// The note names an unreachable repo, a size shortfall, or both, in one
// line; with neither there is no line at all.
func TestResolutionNoteSaysWhatGitHubDidntGive(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		res  prResolution
		want string
	}{
		{prResolution{}, ""},
		{prResolution{unreached: []string{"o/a"}},
			"GitHub didn't answer for o/a — their in-flight and parked issues stay as they are; rerun to retry"},
		{prResolution{unsized: 1},
			"GitHub gave no PR size for 1 issue it settled, so change per issue leaves it out; rerun to retry"},
		{prResolution{unreached: []string{"o/a"}, unsized: 3},
			"GitHub didn't answer for o/a — their in-flight and parked issues stay as they are; " +
				"GitHub gave no PR size for 3 issues it settled, so change per issue leaves them out; rerun to retry"},
	} {
		if got := tc.res.note(); got != tc.want {
			t.Errorf("note() = %q, want %q", got, tc.want)
		}
	}
}

// GitHub answers a PR number it can't resolve with null beside the rest,
// and gh exits 1 over it; every size it did give still counts.
func TestSizeQueryKeepsWhatGitHubAnswered(t *testing.T) {
	t.Parallel()
	got := lookupPRSizes(context.Background(), resolveCfg(t), sizeChunk{"example/one", []int{10, 98}})
	if want := map[int]prSize{10: {Additions: 100, Deletions: 10, ChangedFiles: 3}}; !maps.Equal(got, want) {
		t.Errorf("sizes = %v, want %v", got, want)
	}
	if got := lookupPRSizes(context.Background(), resolveCfg(t), sizeChunk{"example/two", []int{20}}); len(got) != 0 {
		t.Errorf("a repo GitHub can't see gave sizes %v", got)
	}
}

// A park a merge overturned keeps the size its drain recorded, reviews and
// all, and isn't asked about again. One whose drain had no PR to size gets
// GitHub's, as does an in-flight issue settled through its branch.
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
	ghLog := filepath.Join(t.TempDir(), "gh.log")
	setFakeEnv(&cfg, fakeGhLogEnv, ghLog)
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
	calls, err := os.ReadFile(ghLog)
	if err != nil {
		t.Fatal(err)
	}
	for _, call := range strings.Split(string(calls), "\n") {
		if strings.HasPrefix(call, "api graphql") && (strings.Contains(call, "p30:") ||
			!strings.Contains(call, "p40:") || !strings.Contains(call, "p41:")) {
			t.Errorf("the size query should name PRs 40 and 41, not 30: %q", call)
		}
	}
}

// A size query names each PR once, however many issues settled on it, and no
// more than statsSizeChunk of them.
func TestSizeChunksDedupeAndSplit(t *testing.T) {
	t.Parallel()
	var many []int
	for n := range 2*statsSizeChunk + 1 {
		many = append(many, n+1)
	}
	var got []string
	for _, c := range sizeChunks(map[string][]int{"o/a": many, "o/b": {7, 7}}) {
		got = append(got, fmt.Sprintf("%s %d-%d (%d)", c.repo, c.numbers[0], c.numbers[len(c.numbers)-1], len(c.numbers)))
	}
	want := []string{"o/a 1-50 (50)", "o/a 51-100 (50)", "o/a 101-101 (1)", "o/b 7-7 (1)"}
	if !slices.Equal(got, want) {
		t.Errorf("chunks = %q, want %q", got, want)
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

// verdict's edge cases, on listings the fake gh can't hold: two PRs on one
// branch, and a recorded PR on another branch than the issue's own.
func TestVerdictWeighsBothCandidates(t *testing.T) {
	t.Parallel()
	run := []runRecord{{TS: "2026-08-20T09:00:00Z", PR: 10}}
	park := &issueRecord{TS: "2026-08-20T10:00:00Z", Outcome: issueNeedsHuman, PR: 10}
	pr := func(n int, state, ended string) prState {
		st := prState{Number: n, State: state, ClosedAt: ended, HeadRefName: "issue-1"}
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
			l := prListing{byNumber: map[int]prState{}, byBranch: map[string]prState{}}
			if tc.recorded.Number != 0 {
				l.byNumber[tc.recorded.Number] = tc.recorded
			}
			if tc.branch.Number != 0 {
				l.byBranch["issue-1"] = tc.branch
			}
			rec, ok := l.verdict(is, "issue-1")
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
