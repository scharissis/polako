package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestRecordKindPrefixesOnlyADesignVerb(t *testing.T) {
	t.Parallel()
	for verb, want := range map[string]string{
		"":         "run",
		"work":     "run",
		designVerb: "design-run",
	} {
		if got := recordKind(config{verb: verb}, "run"); got != want {
			t.Errorf("recordKind(verb %q) = %q, want %q", verb, got, want)
		}
	}
}

// writeDesignAndWorkRecords appends one merged issue from a work config and
// one from a design config, through the real recorder, so the readers are
// tested against what actually lands on disk.
func writeDesignAndWorkRecords(t *testing.T, dir string, designCost float64) {
	t.Helper()
	work := metricsConfig(t, dir)
	work.verb = "work"
	design := metricsConfig(t, dir)
	design.verb = designVerb
	start := fixtureNow.Add(-2 * time.Hour)
	rc := func(issue int) runContext {
		return runContext{issue: issue, reason: reasonImplement, outcome: outcomeOpenedPR,
			started: start, ended: start.Add(time.Hour)}
	}
	rep := sampleReport()
	work.rec.recordRun(work, rc(1), rep)
	work.rec.recordIssue(work, 1, 10, issueMerged, "", prFacts{}, issueUsageSamples{}, "")
	rep.costUSD = designCost
	design.rec.recordRun(design, rc(2), rep)
	design.rec.recordIssue(design, 2, 20, issueMerged, "", prFacts{}, issueUsageSamples{}, "")
}

func TestADesignConfigWritesItsOwnKinds(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeDesignAndWorkRecords(t, dir, 1)

	var kinds []string
	for _, line := range readRecords(t, dir, "scharissis/polako") {
		var v struct {
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			t.Fatalf("line is not JSON: %v\n%s", err, line)
		}
		kinds = append(kinds, v.Kind)
	}
	if got, want := strings.Join(kinds, ","), "run,issue,design-run,design-issue"; got != want {
		t.Errorf("kinds = %s, want %s", got, want)
	}
}

func TestLoadRecordsDropsDesignKinds(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeDesignAndWorkRecords(t, dir, 1)

	ds, err := loadRecords(dir, statsOptions{}, fixtureNow)
	if err != nil {
		t.Fatalf("loadRecords: %v", err)
	}
	if len(ds.runs) != 1 || ds.runs[0].Issue != 1 {
		t.Errorf("runs = %+v, want only issue #1's work run", ds.runs)
	}
	if len(ds.issues) != 1 || ds.issues[0].Issue != 1 {
		t.Errorf("issues = %+v, want only issue #1's work record", ds.issues)
	}
	if ds.skipped != 0 {
		t.Errorf("skipped = %d, want 0 — a design kind is not a torn line", ds.skipped)
	}
}

// A merged design issue, however dear, must not move what a merged issue
// is said to cost.
func TestPricingLineIgnoresADesignMerge(t *testing.T) {
	t.Parallel()
	plain := t.TempDir()
	withDesign := t.TempDir()
	writeDesignAndWorkRecords(t, withDesign, 500)
	work := metricsConfig(t, plain)
	work.verb = "work"
	start := fixtureNow.Add(-2 * time.Hour)
	work.rec.recordRun(work, runContext{issue: 1, reason: reasonImplement, outcome: outcomeOpenedPR,
		started: start, ended: start.Add(time.Hour)}, sampleReport())
	work.rec.recordIssue(work, 1, 10, issueMerged, "", prFacts{}, issueUsageSamples{}, "")

	want := proposalPricingLine(plain, "scharissis/polako", 5, 0, fixtureNow)
	if want == noPricingHistory {
		t.Fatalf("fixture priced nothing: %q", want)
	}
	if got := proposalPricingLine(withDesign, "scharissis/polako", 5, 0, fixtureNow); got != want {
		t.Errorf("a design merge moved the price:\n got %q\nwant %q", got, want)
	}
}

func TestResumeHintSkipsTheStatsPointerForADesignRun(t *testing.T) {
	t.Parallel()
	for verb, wantStats := range map[string]bool{"work": true, designVerb: false} {
		t.Run(verb, func(t *testing.T) {
			t.Parallel()
			buf := captureLog(t)
			cfg := config{ui: testUI(t), verb: verb, shiftID: "shift7"}
			resumeHint(cfg, 3, &issueState{session: "sess-1"})
			out := buf.String()
			if got := strings.Contains(out, "polako stats -shift shift7"); got != wantStats {
				t.Errorf("stats pointer present = %v, want %v\ngot:\n%s", got, wantStats, out)
			}
			if !strings.Contains(out, "claude --resume sess-1") {
				t.Errorf("the resume pointer went missing:\n%s", out)
			}
		})
	}
}
