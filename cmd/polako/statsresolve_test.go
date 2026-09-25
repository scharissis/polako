package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// offlineStats is the config every stats test that isn't about resolution
// runs under: no ghBin, so resolveInFlight asks nothing and no test reaches
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
	st := &ghState{
		Repo: "example/one",
		PRs: map[string]*fakePR{
			"issue-1": {Number: 10, State: "MERGED", MergedAt: "2026-08-20T15:00:00Z", ClosedAt: "2026-08-20T15:00:00Z"},
			"issue-2": {Number: 11, State: "CLOSED", ClosedAt: "2026-08-20T16:00:00Z"},
			"issue-3": {Number: 12, State: "OPEN"},
			// Would answer for #9 if its repo were visible, so the test
			// proves the failure path rather than a missing number.
			"issue-9": {Number: 20, State: "MERGED", MergedAt: "2026-08-20T17:00:00Z"},
		},
		HiddenRepos: []string{"example/two"},
	}
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
		"note GitHub didn't answer for example/two — their in-flight issues stay in flight; rerun to retry",
		// $7.50 over the one merge GitHub supplied.
		"per merged PR $7.50 across 1 merge",
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
	for _, line := range strings.Split(out.String(), "\n") {
		if strings.Contains(line, "example/one#1 ") && !strings.Contains(line, "merged") {
			t.Errorf("-by issue row for #1 should read merged: %q", line)
		}
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
