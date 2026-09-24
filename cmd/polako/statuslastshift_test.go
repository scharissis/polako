package main

import (
	"context"
	"encoding/json"
	"maps"
	"path/filepath"
	"reflect"
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

// The line and last_shift name the models the shift's fresh, inherited runs
// reported, first-seen order. A run that asked for a model, or a resume, says
// nothing about what inherit resolved to; an older shift's model isn't this
// one's.
func TestLastShiftNamesTheModels(t *testing.T) {
	t.Parallel()
	const recs = `
{"v":1,"kind":"run","ts":"2026-02-10T09:00:00Z","ended":"2026-02-10T09:30:00Z","repo":"example/repo","shift":"aaaa0001","issue":1,"reason":"implement","status":"ok","outcome":"opened_pr","cost_usd":5.00,"model":"claude-haiku-4-5"}
{"v":1,"kind":"run","ts":"2026-02-21T02:10:00Z","ended":"2026-02-21T03:00:00Z","repo":"example/repo","shift":"bbbb0002","issue":3,"reason":"implement","status":"ok","outcome":"opened_pr","cost_usd":10.00,"model":"claude-sonnet-5"}
{"v":1,"kind":"run","ts":"2026-02-21T03:10:00Z","ended":"2026-02-21T03:20:00Z","repo":"example/repo","shift":"bbbb0002","issue":3,"reason":"rebase","status":"ok","outcome":"opened_pr","cost_usd":1.00,"model":"claude-fable-5-1","requested_model":"fable"}
{"v":1,"kind":"run","ts":"2026-02-21T03:30:00Z","ended":"2026-02-21T03:40:00Z","repo":"example/repo","shift":"bbbb0002","issue":5,"reason":"resume","status":"ok","outcome":"opened_pr","cost_usd":1.00,"model":"claude-sonnet-5-bare"}
{"v":1,"kind":"run","ts":"2026-02-21T04:10:00Z","ended":"2026-02-21T05:00:00Z","repo":"example/repo","shift":"bbbb0002","issue":7,"reason":"implement","status":"ok","outcome":"opened_pr","cost_usd":2.00,"model":"claude-opus-5-5[1m]"}
{"v":1,"kind":"run","ts":"2026-02-21T05:10:00Z","ended":"2026-02-21T05:20:00Z","repo":"example/repo","shift":"bbbb0002","issue":9,"reason":"implement","status":"ok","outcome":"opened_pr","cost_usd":2.00,"model":"claude-sonnet-5"}
`
	dir := writePricingFixture(t, map[string]string{"example--repo.jsonl": recs})
	ls := readLastShift(dir, "example/repo", statusNow)
	want := "last shift here: Feb 21 02:10, 3h10m — 0 merged, 0 parked, $16.00, on claude-sonnet-5 and claude-opus-5-5[1m] — " +
		"polako stats -shift bbbb0002 -metrics " + dir
	if got := lastShiftLine(ls); got != want {
		t.Errorf("line:\n got %q\nwant %q", got, want)
	}
	if got, want := toStatusDocLastShift(ls).Models, []string{"claude-sonnet-5", "claude-opus-5-5[1m]"}; !slices.Equal(got, want) {
		t.Errorf("last_shift.models = %q, want %q", got, want)
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
		def:          "polako stats -shift bbbb0002",
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

// A shift that only saw a PR merge wrote no run: no span to claim.
func TestLastShiftWithNoRunsDropsTheSpan(t *testing.T) {
	t.Parallel()
	const mergeOnly = `
{"v":1,"kind":"issue","ts":"2026-02-22T04:00:00Z","repo":"example/repo","shift":"dddd0004","issue":3,"pr":40,"outcome":"merged"}
`
	dir := writePricingFixture(t, map[string]string{"example--repo.jsonl": mergeOnly})
	got := lastShiftLine(readLastShift(dir, "example/repo", statusNow))
	if want := "last shift here: Feb 22 04:00 — 1 merged, 0 parked, $0.00 — polako stats -shift dddd0004 -metrics " + dir; got != want {
		t.Errorf("line:\n got %q\nwant %q", got, want)
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

// Only the newest record counts: #1 parked, then merged, says nothing; #5's
// park is its newest; #11 parked with no reason recorded gets no suffix.
func TestReadParkReasonsKeepsTheNewestPark(t *testing.T) {
	t.Parallel()
	const recs = `
{"v":1,"kind":"issue","ts":"2026-02-01T00:00:00Z","repo":"example/repo","issue":1,"outcome":"needs_human","park_reason":"budget"}
{"v":1,"kind":"issue","ts":"2026-02-02T00:00:00Z","repo":"example/repo","issue":1,"pr":10,"outcome":"merged"}
{"v":1,"kind":"issue","ts":"2026-02-01T00:00:00Z","repo":"example/repo","issue":5,"pr":50,"outcome":"merged"}
{"v":1,"kind":"issue","ts":"2026-02-03T00:00:00Z","repo":"example/repo","issue":5,"outcome":"needs_human","park_reason":"checks_remediation"}
{"v":1,"kind":"issue","ts":"2026-02-03T00:00:00Z","repo":"example/repo","issue":11,"outcome":"needs_human"}
`
	dir := writePricingFixture(t, map[string]string{"example--repo.jsonl": recs})
	got := readParkReasons(dir, "example/repo", statusNow)
	if want := map[int]string{5: "checks_remediation"}; !maps.Equal(got, want) {
		t.Errorf("reasons = %v, want %v", got, want)
	}
	if got := readParkReasons("", "example/repo", statusNow); got != nil {
		t.Errorf("-metrics off: reasons = %v, want nil", got)
	}
}

func TestReadySuffix(t *testing.T) {
	t.Parallel()
	m := &issueMedian{cost: 7.5, n: 2}
	five := []int{1, 2, 3, 4, 5}
	// A PR on a ready issue means a drain waits on it, not a fresh run; a PR
	// on an issue outside the ready row changes nothing.
	prs := []statusPR{{number: 40, issue: 3}, {number: 90, issue: 9}}
	for _, c := range []struct {
		m     *issueMedian
		ready []int
		prs   []statusPR
		want  string
	}{
		{m, five, nil, " — about $38 at your median"},
		{m, five, prs, " — about $30 at your median"},
		{m, []int{1}, nil, " — about $7.50 at your median"},
		{m, []int{3}, prs, ""},
		{m, nil, nil, ""},
		{nil, five, nil, ""},
	} {
		if got := (statusRunData{readyMedian: c.m}).readySuffix(c.ready, c.prs); got != c.want {
			t.Errorf("readySuffix(%v, %v, %v) = %q, want %q", c.m, c.ready, c.prs, got, c.want)
		}
	}
}

// The run data changes the report by the last-shift line and the ready and
// parked rows' suffixes, and nothing else: the same GitHub snapshot rendered
// with and without a metrics directory differs only there — and -json only in
// last_shift.
func TestStatusRunDataChangesOnlyItsOwnDetails(t *testing.T) {
	t.Parallel()
	cfg, _ := statusConfigFor(t, &ghState{
		Issues: map[string]*fakeIssue{
			"3": {Open: true},
			"4": {Open: true},
			"7": {Open: true, Labels: []string{awaitingAnswerLabel}},
			"9": {Open: true, Labels: []string{needsHumanLabel}},
		},
		PRs: map[string]*fakePR{
			"issue-3": {Number: 40, State: "OPEN", Mergeable: "MERGEABLE", Checks: []string{"SUCCESS"}},
		},
	})
	// #9's park sits in the older shift, so the last-shift line is unchanged.
	const parkedNine = `{"v":1,"kind":"issue","ts":"2026-02-10T11:00:00Z","repo":"example/repo","shift":"aaaa0001","issue":9,"pr":0,"outcome":"needs_human","park_reason":"budget"}
`
	metrics := writePricingFixture(t, map[string]string{"example--repo.jsonl": lastShiftFixture + parkedNine})

	render := func(dir string) (string, map[string]json.RawMessage) {
		snap, err := readStatus(context.Background(), cfg, statusNow)
		if err != nil {
			t.Fatalf("readStatus: %v", err)
		}
		snap.runData = readStatusRunData(dir, cfg.repo, statusNow)
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

	withLines := strings.Split(with, "\n")
	i := slices.Index(withLines, wantLastShiftLine(metrics))
	if i < 0 {
		t.Fatalf("report with run data is missing %q\ngot:\n%s", wantLastShiftLine(metrics), with)
	}
	// Merged #1 ($5) and #3 ($10) make a $7.50 median; of the two ready
	// issues only #4 is priced, since #3's branch already has PR #40.
	wantText := strings.NewReplacer(
		"#3, #4\n", "#3, #4 — about $7.50 at your median\n",
		"#9, labelled", "#9 (budget), labelled",
	).Replace(without)
	for _, s := range []string{"#3, #4 — about $7.50 at your median", "#9 (budget), labelled"} {
		if !strings.Contains(wantText, s) {
			t.Fatalf("report with run data should carry %q\ngot:\n%s", s, with)
		}
	}
	if got := strings.Join(slices.Delete(slices.Clone(withLines), i, i+1), "\n"); got != wantText {
		t.Errorf("reports differ by more than the run-data details\nwith:\n%s\nwant (plus the last-shift line):\n%s", with, wantText)
	}

	if string(withoutDoc["last_shift"]) != "null" {
		t.Errorf("-json last_shift without run data = %s, want null", withoutDoc["last_shift"])
	}
	var ls statusDocLastShift
	if err := json.Unmarshal(withDoc["last_shift"], &ls); err != nil {
		t.Fatalf("last_shift: %v", err)
	}
	want := statusDocLastShift{Shift: "bbbb0002", Started: "2026-02-21T02:10:00Z", SpanSeconds: 22320,
		Merged: 1, Parked: 1, CostUSD: 31.2, Models: []string{}}
	if !reflect.DeepEqual(ls, want) {
		t.Errorf("last_shift = %+v, want %+v", ls, want)
	}
	for k, v := range withDoc {
		if k != "last_shift" && string(v) != string(withoutDoc[k]) {
			t.Errorf("-json field %q changed with run data:\n with %s\n without %s", k, v, withoutDoc[k])
		}
	}
}
