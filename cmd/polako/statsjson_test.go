package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The whole document, pinned, over the simplest fixture there is: one run,
// no terminal record yet. Every field here was worked out by hand from the
// record above it — if a change moves one, the change has to be able to say
// why, the same discipline TestStatsReport holds the text report to.
func TestStatsJSONGoldenDocument(t *testing.T) {
	dir := t.TempDir()
	body := `{"v":1,"kind":"run","ts":"2026-08-20T09:00:00Z","ended":"2026-08-20T09:10:00Z","repo":"r/r","issue":1,"reason":"implement","status":"ok","outcome":"opened_pr","pr":2,"cost_usd":1.5,"turns":9}
`
	if err := os.WriteFile(filepath.Join(dir, "r--r.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	out, errOut := statsOutErr(t, "-metrics", dir, "-json")
	if errOut != "" {
		t.Errorf("nothing should reach errOut without -html: %q", errOut)
	}

	want := fmt.Sprintf(`{
  "dir": %q,
  "scope": {},
  "source": {
    "files": 1,
    "records": 1,
    "skipped": 0,
    "unread": [],
    "window_from": "2026-08-20T09:00:00Z",
    "window_to": "2026-08-20T09:10:00Z",
    "repos": [
      "r/r"
    ]
  },
  "issues": {
    "terminal": {},
    "done": 0,
    "in_flight": 1,
    "priced": 0
  },
  "runs": {
    "total": 1,
    "statuses": {
      "ok": 1
    },
    "reasons": {
      "implement": 1
    },
    "outcomes": {
      "opened_pr": 1
    },
    "turns": 9,
    "tool_uses": 0,
    "approximated": 0
  },
  "cost": {
    "total_usd": 1.5,
    "merged": 0,
    "tokens": {
      "in": 0,
      "out": 0,
      "cache_read": 0,
      "cache_write": 0
    },
    "total_tokens": 0
  },
  "latency": {
    "blocked_on_answers": {
      "count": 0
    },
    "pr_to_merge": {
      "count": 0
    }
  }
}
`, dir)
	if out != want {
		t.Errorf("stats -json differs\n--- got ---\n%s\n--- want ---\n%s", out, want)
	}

	var doc statsDoc
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("output did not parse as JSON: %v\n%s", err, out)
	}
}

// One document, no header, no trailing prose — stdout only, so a pipe into
// jq sees exactly the facts and nothing else.
func TestStatsJSONIsTheWholeOfStdout(t *testing.T) {
	out := stats(t, "-metrics", fixtureDir(t), "-json")
	if !json.Valid([]byte(out)) {
		t.Fatalf("stats -json is not valid JSON on its own:\n%s", out)
	}
	if strings.Contains(out, "run data from") {
		t.Errorf("the text report's header leaked into -json output:\n%s", out)
	}
}

// Cross-checks the same fixture TestStatsReport pins, field by field, so the
// JSON renderer cannot silently drift from the text one: the two read the
// same statsSummary, but this is the test that would catch it if they ever
// stopped agreeing about a figure.
func TestStatsJSONMatchesTheTextReport(t *testing.T) {
	dir := fixtureDir(t)
	out := stats(t, "-metrics", dir, "-json")
	var doc statsDoc
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("output did not parse as JSON: %v\n%s", err, out)
	}

	if doc.Issues.Terminal["merged"] != 3 || doc.Issues.Done != 4 || doc.Issues.InFlight != 1 {
		t.Errorf("issues = %+v, want the same terminal 4 — merged 3, in flight 1 the text report shows", doc.Issues)
	}
	if doc.Issues.ParkReasons["produced_nothing"] != 1 {
		t.Errorf("park_reasons = %+v, want produced_nothing 1", doc.Issues.ParkReasons)
	}
	if doc.Issues.CostPerIssueUSD == nil || fmt.Sprintf("%.2f", doc.Issues.CostPerIssueUSD.Mean) != "1.92" {
		t.Errorf("cost_per_issue_usd = %+v, want mean 1.92 — the text report's $1.92 mean", doc.Issues.CostPerIssueUSD)
	}
	if doc.Runs.Total != 7 || doc.Runs.Statuses["crash"] != 1 || doc.Runs.Turns != 131 || doc.Runs.ToolUses != 115 {
		t.Errorf("runs = %+v, want total 7, 1 crash, 131 turns, 115 tool uses", doc.Runs)
	}
	if doc.Runs.Approximated != 1 {
		t.Errorf("approximated = %d, want 1 — the text report's \"1 of 7 runs\"", doc.Runs.Approximated)
	}
	if fmt.Sprintf("%.2f", doc.Cost.TotalUSD) != "8.10" {
		t.Errorf("cost.total_usd = %v, want 8.10", doc.Cost.TotalUSD)
	}
	if doc.Cost.PerMergedUSD == nil || fmt.Sprintf("%.2f", *doc.Cost.PerMergedUSD) != "2.70" {
		t.Errorf("per_merged_usd = %v, want 2.70 across 3 merges", doc.Cost.PerMergedUSD)
	}
	if doc.Cost.Merged != 3 {
		t.Errorf("cost.merged = %d, want 3", doc.Cost.Merged)
	}
	if doc.Latency.BlockedOnAnswers.Count != 1 || doc.Latency.PRToMerge.Count != 3 {
		t.Errorf("latency = %+v, want 1 blocked span and 3 pr-to-merge spans", doc.Latency)
	}
}

// Every array and breakdown map stays [] / {}, never null, whatever the
// fixture — a script doing `.source.unread[]` must not special-case empty.
func TestStatsJSONKeepsCollectionsEmptyNotNull(t *testing.T) {
	out := stats(t, "-metrics", t.TempDir(), "-json")
	for _, unwanted := range []string{
		`"unread":null`, `"repos":null`, `"terminal":null`,
		`"statuses":null`, `"reasons":null`, `"outcomes":null`,
	} {
		if strings.Contains(out, unwanted) {
			t.Errorf("output has %q, want an empty collection instead\n%s", unwanted, out)
		}
	}
}

// -by and -runs are typed sections present only when asked for, over the
// full fixture so every kind of row has real data behind it.
func TestStatsJSONByAndRunLog(t *testing.T) {
	dir := fixtureDir(t)

	t.Run("by issue", func(t *testing.T) {
		out := stats(t, "-metrics", dir, "-json", "-by", byIssue)
		var doc statsDoc
		mustUnmarshal(t, out, &doc)
		if doc.By == nil || doc.By.Kind != byIssue {
			t.Fatalf("by = %+v, want kind %q", doc.By, byIssue)
		}
		row := findIssueRow(t, doc.By.Issues, "scharissis/polako", 12)
		if row.Outcome != "merged" || row.Runs != 2 {
			t.Errorf("issue #12 row = %+v, want outcome merged, 2 runs", row)
		}
	})

	t.Run("by model", func(t *testing.T) {
		out := stats(t, "-metrics", dir, "-json", "-by", byModel)
		var doc statsDoc
		mustUnmarshal(t, out, &doc)
		if doc.By == nil || doc.By.Kind != byModel || len(doc.By.Groups) == 0 {
			t.Fatalf("by model = %+v, want at least one group", doc.By)
		}
		found := false
		for _, g := range doc.By.Groups {
			if g.Name == "claude-opus-5" {
				found = true
				if g.Merged == 0 || g.PerMergedUSD == nil {
					t.Errorf("claude-opus-5 group = %+v, want a merge and a per-merged price", g)
				}
			}
		}
		if !found {
			t.Errorf("no claude-opus-5 group in %+v", doc.By.Groups)
		}
	})

	t.Run("run log", func(t *testing.T) {
		out := stats(t, "-metrics", dir, "-json", "-runs")
		var doc statsDoc
		mustUnmarshal(t, out, &doc)
		if len(doc.RunLog) != 7 {
			t.Fatalf("run_log has %d rows, want 7 — one per run in the fixture", len(doc.RunLog))
		}
		var sawSession bool
		for _, r := range doc.RunLog {
			if r.Session == "s13a" {
				sawSession = true
			}
		}
		if !sawSession {
			t.Errorf("run_log never named session s13a:\n%+v", doc.RunLog)
		}
	})

	t.Run("absent unless asked for", func(t *testing.T) {
		out := stats(t, "-metrics", dir, "-json")
		var doc statsDoc
		mustUnmarshal(t, out, &doc)
		if doc.By != nil {
			t.Errorf("by = %+v, want nil without -by", doc.By)
		}
		if doc.RunLog != nil {
			t.Errorf("run_log = %+v, want nil without -runs", doc.RunLog)
		}
	})
}

func mustUnmarshal(t *testing.T, out string, doc *statsDoc) {
	t.Helper()
	if err := json.Unmarshal([]byte(out), doc); err != nil {
		t.Fatalf("output did not parse as JSON: %v\n%s", err, out)
	}
}

func findIssueRow(t *testing.T, rows []statsDocIssueRow, repo string, issue int) statsDocIssueRow {
	t.Helper()
	for _, r := range rows {
		if r.Repo == repo && r.Issue == issue {
			return r
		}
	}
	t.Fatalf("no row for %s#%d in %+v", repo, issue, rows)
	return statsDocIssueRow{}
}

// A true median of 0 reviews (some issues reviewed, the middle one not) and
// "no review data at all" are different states, and change_per_issue must
// keep them apart: reviews_median present-and-0 for the former, the whole
// field absent for the latter.
func TestStatsJSONReviewsMedianSurvivesAGenuineZero(t *testing.T) {
	dir := t.TempDir()
	body := `{"v":1,"kind":"issue","ts":"2026-08-20T09:00:00Z","repo":"r/r","issue":1,"pr":1,"outcome":"merged","additions":10,"deletions":2,"changed_files":1,"reviews":0}
{"v":1,"kind":"issue","ts":"2026-08-20T10:00:00Z","repo":"r/r","issue":2,"pr":2,"outcome":"merged","additions":20,"deletions":4,"changed_files":2,"reviews":0}
{"v":1,"kind":"issue","ts":"2026-08-20T11:00:00Z","repo":"r/r","issue":3,"pr":3,"outcome":"merged","additions":30,"deletions":6,"changed_files":3,"reviews":5}
`
	if err := os.WriteFile(filepath.Join(dir, "r--r.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	out := stats(t, "-metrics", dir, "-json")
	var doc statsDoc
	mustUnmarshal(t, out, &doc)

	c := doc.Issues.ChangePerIssue
	if c == nil {
		t.Fatalf("change_per_issue is nil, want additions/deletions/reviews over 3 issues")
	}
	if c.ReviewsMedian == nil {
		t.Fatalf("reviews_median is absent, want a present 0 — median of [0, 0, 5] is a real 0, not \"no data\"")
	}
	if *c.ReviewsMedian != 0 {
		t.Errorf("reviews_median = %d, want 0", *c.ReviewsMedian)
	}
	if !strings.Contains(out, `"reviews_median": 0`) {
		t.Errorf("the raw JSON dropped the zero median instead of printing it:\n%s", out)
	}
}

// -json with -html still writes the file; the confirmation must not land on
// stdout, where it would break "-json is the whole of stdout" and could not
// be told apart from part of the document by a naive line-splitter.
func TestStatsJSONHTMLConfirmationGoesToStderr(t *testing.T) {
	dir := fixtureDir(t)
	path := filepath.Join(t.TempDir(), "report.html")
	out, errOut := statsOutErr(t, "-metrics", dir, "-json", "-html", path)

	if !json.Valid([]byte(out)) {
		t.Fatalf("stdout is not valid JSON once -html is added:\n%s", out)
	}
	if strings.Contains(out, "wrote the HTML report") {
		t.Errorf("the -html confirmation leaked into the JSON document:\n%s", out)
	}
	if !strings.Contains(errOut, "wrote the HTML report to "+path) {
		t.Errorf("errOut = %q, want the confirmation there instead", errOut)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("-html %s: file was not written (%v)", path, err)
	}
}

// window is present, typed, and matches the header line's own numbers —
// present only when -window was given, the same "absent, not zeroed" rule
// every other conditional field on this document already follows.
//
// Goes through statsReport with a fake claudeBin rather than stats()'s
// public runStats: -window today needs no probe, but this keeps the same
// hermetic pattern every probe-adjacent stats test in this package uses,
// rather than depending on runStats's default claudeBin ("claude") failing
// to resolve on whatever machine runs the suite.
func TestStatsJSONWindow(t *testing.T) {
	t.Setenv(fakeClaudeEnv, "stream")
	cfg := config{claudeBin: fakeCLI(t), usageTimeout: 5 * time.Second}
	now := time.Date(2026, 8, 25, 15, 0, 0, 0, time.UTC)
	opt := statsOptions{window: windowToday}

	ds, issues, summary, err := statsReport(context.Background(), cfg, opt, t.TempDir(), now)
	if err != nil {
		t.Fatalf("statsReport: %v", err)
	}
	var buf bytes.Buffer
	if err := renderStatsJSON(&buf, ds, issues, summary, opt); err != nil {
		t.Fatalf("renderStatsJSON: %v", err)
	}
	var doc statsDoc
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatalf("output did not parse as JSON: %v\n%s", err, buf.String())
	}
	if doc.Window == nil {
		t.Fatal("window is nil, want it present under -window")
	}
	want := statsDocWindow{
		Kind: "today", From: "2026-08-25T00:00:00Z", PeriodEnd: "2026-08-26T00:00:00Z",
		ElapsedSeconds: (15 * time.Hour).Seconds(), RemainingSeconds: (9 * time.Hour).Seconds(),
	}
	if *doc.Window != want {
		t.Errorf("window = %+v, want %+v", *doc.Window, want)
	}
	if doc.Plan != nil {
		t.Errorf("plan = %+v, want nil — no run data at all in this fixture", doc.Plan)
	}
}

// plan carries the same figures planCostPairs' text line does, cross-check
// included, and is nil exactly when that line is absent.
func TestStatsJSONPlan(t *testing.T) {
	t.Setenv(fakeClaudeEnv, "stream")
	t.Setenv(fakeUsageEnv, "sub")
	cfg := config{claudeBin: fakeCLI(t), usageTimeout: 5 * time.Second}

	ds, issues, summary, err := statsReport(context.Background(), cfg, statsOptions{}, planCostDir(t), fixtureNow)
	if err != nil {
		t.Fatalf("statsReport: %v", err)
	}
	var buf bytes.Buffer
	if err := renderStatsJSON(&buf, ds, issues, summary, statsOptions{}); err != nil {
		t.Fatalf("renderStatsJSON: %v", err)
	}
	var doc statsDoc
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatalf("output did not parse as JSON: %v\n%s", err, buf.String())
	}
	if doc.Plan == nil {
		t.Fatal("plan is nil, want it present — three issues carry a usable sample")
	}
	if doc.Plan.SampledIssues != 3 || doc.Plan.UnsampledIssues != 2 {
		t.Errorf("plan sampled/unsampled = %d/%d, want 3/2", doc.Plan.SampledIssues, doc.Plan.UnsampledIssues)
	}
	if doc.Plan.MeanPercent != 5 || doc.Plan.MedianPercent != 4 {
		t.Errorf("plan mean/median = %v/%v, want 5/4", doc.Plan.MeanPercent, doc.Plan.MedianPercent)
	}
	if doc.Plan.CrossCheckPercent == nil || *doc.Plan.CrossCheckPercent != 29 || doc.Plan.CrossCheckWindow != "24h" {
		t.Errorf("cross check = %+v, want 29%% over 24h — the fake CLI's usageSample", doc.Plan)
	}
	if doc.Window != nil {
		t.Errorf("window = %+v, want nil — -window was not given", doc.Window)
	}
}
