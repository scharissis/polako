package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// lastShiftFixture has two shifts in example/repo — the newer one spanning
// 02:10 to 08:22 with one merge, one park and one issue still in flight — and
// a later shift in another repository that the -repo scope has to drop.
const lastShiftFixture = `
{"v":1,"kind":"run","ts":"2026-02-10T09:00:00Z","ended":"2026-02-10T09:30:00Z","repo":"example/repo","shift":"aaaa0001","issue":1,"reason":"implement","status":"ok","outcome":"opened_pr","cost_usd":5.00,"wall_ms":1800000,"tokens":{"in":1,"out":1}}
{"v":1,"kind":"issue","ts":"2026-02-10T10:00:00Z","repo":"example/repo","shift":"aaaa0001","issue":1,"pr":10,"outcome":"merged"}
{"v":1,"kind":"run","ts":"2026-02-21T02:10:00Z","ended":"2026-02-21T03:00:00Z","repo":"example/repo","shift":"bbbb0002","issue":3,"reason":"implement","status":"ok","outcome":"opened_pr","cost_usd":10.00,"wall_ms":3000000,"tokens":{"in":1,"out":1}}
{"v":1,"kind":"issue","ts":"2026-02-21T04:00:00Z","repo":"example/repo","shift":"bbbb0002","issue":3,"pr":40,"outcome":"merged"}
{"v":1,"kind":"run","ts":"2026-02-21T04:10:00Z","ended":"2026-02-21T05:00:00Z","repo":"example/repo","shift":"bbbb0002","issue":5,"reason":"implement","status":"ok","outcome":"nothing","cost_usd":20.00,"wall_ms":3000000,"tokens":{"in":1,"out":1}}
{"v":1,"kind":"issue","ts":"2026-02-21T05:10:00Z","repo":"example/repo","shift":"bbbb0002","issue":5,"pr":0,"outcome":"needs_human","park_reason":"produced_nothing"}
{"v":1,"kind":"run","ts":"2026-02-21T05:20:00Z","ended":"2026-02-21T08:22:00Z","repo":"example/repo","shift":"bbbb0002","issue":7,"reason":"implement","status":"ok","outcome":"opened_pr","cost_usd":1.20,"wall_ms":10920000,"tokens":{"in":1,"out":1}}
`

const lastShiftOtherRepo = `
{"v":1,"kind":"run","ts":"2026-02-25T09:00:00Z","ended":"2026-02-25T10:00:00Z","repo":"example/other","shift":"cccc0003","issue":9,"reason":"implement","status":"ok","outcome":"opened_pr","cost_usd":99.00,"wall_ms":3600000,"tokens":{"in":1,"out":1}}
`

// wantLastShiftLine is the fixture's line read from dir — a test directory,
// never the default, so the hint always carries -metrics.
func wantLastShiftLine(dir string) string {
	return "last shift here: Feb 21 02:10, 6h12m — 1 merged, 1 parked, $31.20 — " +
		"polako stats -shift bbbb0002 -metrics " + dir
}

func TestLastShiftLineFromRunData(t *testing.T) {
	t.Parallel()
	dir := writePricingFixture(t, map[string]string{
		"example--repo.jsonl":  lastShiftFixture,
		"example--other.jsonl": lastShiftOtherRepo,
	})
	if got, want := lastShiftLine(readLastShift(dir, "example/repo", statusNow)), wantLastShiftLine(dir); got != want {
		t.Errorf("line:\n got %q\nwant %q", got, want)
	}
}

// Records in the default directory need no -metrics to find again; anywhere
// else, the hint says where, or stats would look in the wrong place.
func TestLastShiftHintNamesOnlyANonDefaultDirectory(t *testing.T) {
	t.Parallel()
	def, err := defaultMetricsDir()
	if err != nil {
		t.Skipf("no home directory: %v", err)
	}
	for dir, want := range map[string]string{
		def:         "polako stats -shift bbbb0002",
		"/elsewhere": "polako stats -shift bbbb0002 -metrics /elsewhere",
	} {
		if got := shiftHint("bbbb0002", dir); got != want {
			t.Errorf("hint for %s = %q, want %q", dir, got, want)
		}
	}
}

// The start is rendered on the reader's clock, not the records' UTC.
func TestLastShiftLineUsesTheLocalZone(t *testing.T) {
	t.Parallel()
	dir := writePricingFixture(t, map[string]string{"example--repo.jsonl": lastShiftFixture})
	now := statusNow.In(time.FixedZone("AEDT", 11*3600))
	got := lastShiftLine(readLastShift(dir, "example/repo", now))
	if !strings.HasPrefix(got, "last shift here: Feb 21 13:10, 6h12m") {
		t.Errorf("line = %q, want the start at 13:10 in +11:00", got)
	}
}

// A shift from another year says so, rather than reading as a recent one.
func TestLastShiftLineNamesAnOlderYear(t *testing.T) {
	t.Parallel()
	dir := writePricingFixture(t, map[string]string{"example--repo.jsonl": lastShiftFixture})
	got := lastShiftLine(readLastShift(dir, "example/repo", statusNow.AddDate(1, 0, 0)))
	if !strings.HasPrefix(got, "last shift here: Feb 21 2026 02:10, 6h12m") {
		t.Errorf("line = %q, want the year named", got)
	}
}

func TestLastShiftAbsentWithoutHistory(t *testing.T) {
	t.Parallel()
	const idless = `
{"v":1,"kind":"run","ts":"2026-02-21T02:10:00Z","ended":"2026-02-21T03:00:00Z","repo":"example/repo","issue":3,"reason":"implement","status":"ok","outcome":"opened_pr","cost_usd":10.00,"wall_ms":3000000,"tokens":{"in":1,"out":1}}
`
	for name, dir := range map[string]string{
		"-metrics off":             "",
		"empty directory":          t.TempDir(),
		"no such dir":              t.TempDir() + "/missing",
		"other repo only":          writePricingFixture(t, map[string]string{"example--other.jsonl": lastShiftOtherRepo}),
		"records with no shift id": writePricingFixture(t, map[string]string{"example--repo.jsonl": idless}),
	} {
		if ls := readLastShift(dir, "example/repo", statusNow); ls != nil {
			t.Errorf("%s: got %+v, want nil", name, ls)
		}
	}
}

// -metrics goes through runStatus's own flag parse: off reads nothing, and a
// path naming a record file is refused before any GitHub read, with the fix.
func TestStatusMetricsFlag(t *testing.T) {
	t.Parallel()
	if dir, err := statusMetricsDir("off"); dir != "" || err != nil {
		t.Errorf("off: dir %q, err %v; want nothing to read", dir, err)
	}
	dir := writePricingFixture(t, map[string]string{"example--repo.jsonl": lastShiftFixture})
	if got, err := statusMetricsDir(dir); got != dir || err != nil {
		t.Errorf("directory: got %q, %v; want %q", got, err, dir)
	}
	file := filepath.Join(dir, "example--repo.jsonl")
	err := runStatus(context.Background(), []string{"-metrics", file}, &strings.Builder{}, statusNow, report{})
	if err == nil || !strings.Contains(err.Error(), "is a file, not a directory") {
		t.Errorf("-metrics <file>: err = %v, want the is-a-file refusal", err)
	}
}

// The run data changes the report by that one line and nothing else: the same
// GitHub snapshot rendered with and without a metrics directory differs only
// in the last-shift line, text and JSON both.
func TestStatusRunDataChangesOnlyTheLastShiftLine(t *testing.T) {
	t.Parallel()
	cfg, _ := statusConfigFor(t, &ghState{
		Issues: map[string]*fakeIssue{
			"3": {Open: true},
			"7": {Open: true, Labels: []string{awaitingAnswerLabel}},
			"9": {Open: true, Labels: []string{needsHumanLabel}},
		},
		PRs: map[string]*fakePR{
			"issue-3": {Number: 40, State: "OPEN", Mergeable: "MERGEABLE", Checks: []string{"SUCCESS"}},
		},
	})
	metrics := writePricingFixture(t, map[string]string{"example--repo.jsonl": lastShiftFixture})

	render := func(dir string) (string, map[string]json.RawMessage) {
		snap, err := readStatus(context.Background(), cfg, statusNow)
		if err != nil {
			t.Fatalf("readStatus: %v", err)
		}
		snap.lastShift = readLastShift(dir, cfg.repo, statusNow)
		var text, js strings.Builder
		renderStatus(&text, report{}, cfg, snap)
		if err := renderStatusJSON(&js, cfg, snap); err != nil {
			t.Fatalf("renderStatusJSON: %v", err)
		}
		var doc map[string]json.RawMessage
		if err := json.Unmarshal([]byte(js.String()), &doc); err != nil {
			t.Fatalf("parsing -json: %v", err)
		}
		return text.String(), doc
	}
	with, withDoc := render(metrics)
	without, withoutDoc := render("")

	withLines, withoutLines := strings.Split(with, "\n"), strings.Split(without, "\n")
	i := slices.Index(withLines, wantLastShiftLine(metrics))
	if i < 0 {
		t.Fatalf("report with run data is missing %q\ngot:\n%s", wantLastShiftLine(metrics), with)
	}
	if !slices.Equal(slices.Delete(slices.Clone(withLines), i, i+1), withoutLines) {
		t.Errorf("reports differ by more than the last-shift line\nwith:\n%s\nwithout:\n%s", with, without)
	}

	if string(withoutDoc["last_shift"]) != "null" {
		t.Errorf("-json last_shift without run data = %s, want null", withoutDoc["last_shift"])
	}
	var ls statusDocLastShift
	if err := json.Unmarshal(withDoc["last_shift"], &ls); err != nil {
		t.Fatalf("last_shift: %v", err)
	}
	want := statusDocLastShift{Shift: "bbbb0002", Started: "2026-02-21T02:10:00Z", SpanSeconds: 22320,
		Merged: 1, Parked: 1, CostUSD: 31.2}
	if ls != want {
		t.Errorf("last_shift = %+v, want %+v", ls, want)
	}
	for k, v := range withDoc {
		if k != "last_shift" && string(v) != string(withoutDoc[k]) {
			t.Errorf("-json field %q changed with run data:\n with %s\n without %s", k, v, withoutDoc[k])
		}
	}
}
