package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// htmlReportOf runs `stats -html` over a fixture and hands back the page it
// wrote, so every test below asserts against the real bytes on disk rather
// than against the builder's opinion of them.
func htmlReportOf(t *testing.T, dir string, extra ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "report.html")
	stats(t, append([]string{"-metrics", dir, "-html", path}, extra...)...)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the report -html wrote: %v", err)
	}
	return string(b)
}

// The requirement, asserted against the rendered page: this file holds the
// operator's private numbers, so opening it must not tell anybody it was
// opened. It has to render with the network cable pulled.
func TestStatsHTMLLoadsNothingExternal(t *testing.T) {
	page := htmlReportOf(t, fixtureDir(t), "-runs", "-by", byTag)

	// Every way a browser is made to fetch something while rendering.
	for _, forbidden := range []string{
		"<script", "<link", "<img", "<iframe", "<object", "<embed", "<use",
		"src=", "url(", "@import", "@font-face", "xlink:href", "srcset",
		"<base", "http-equiv",
	} {
		if strings.Contains(strings.ToLower(page), forbidden) {
			t.Errorf("the page contains %q — it must load nothing when opened offline", forbidden)
		}
	}

	// The one thing left that could name a remote host is an anchor, which is
	// fetched on a click and not before. Nothing else may mention a URL at all.
	lower := strings.ToLower(page)
	const anchor = `href="`
	for i := 0; ; {
		at := strings.Index(lower[i:], "http")
		if at < 0 {
			break
		}
		at += i
		if at < len(anchor) || lower[at-len(anchor):at] != anchor {
			t.Errorf("the page mentions a URL outside an anchor, at %q",
				page[max(0, at-40):min(len(page), at+40)])
		}
		i = at + 4
	}
	if !strings.Contains(page, `href="https://github.com/scharissis/polako/issues/12"`) {
		t.Errorf("an issue row should still link out to GitHub:\n%s", page)
	}
}

// The page is a second view of the report, not a second derivation of it. Every
// figure here was read off the text output of the same fixture.
func TestStatsHTMLShowsTheSameNumbersAsTheText(t *testing.T) {
	dir := fixtureDir(t)
	page := htmlReportOf(t, dir, "-runs")
	text := stats(t, "-metrics", dir, "-runs")

	for _, want := range []string{
		"$8.10",                    // total spend
		"75%",                      // merge rate
		"$2.70",                    // per merged PR
		"19.1M",                    // tokens
		"$1.92 mean, $2.00 median", // cost per issue, straight from issuePairs
		"131 turns, 115 tool uses", // work, straight from runPairs
		"scharissis/polako#12",     // the by-issue row
		"s13a",                     // the run log's session handle
		"1h20m median, 2h max",     // pr open to merge
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the page is missing %q, which the text report prints:\n%s", want, page)
		}
		if !hasLine(text, want) {
			t.Errorf("the fixture no longer produces %q in text — this test has gone stale", want)
		}
	}
}

// -html adds an output; it never swaps one out. Anybody who asked for a file
// still wants to see what went into it.
func TestStatsHTMLLeavesTheTextReportAlone(t *testing.T) {
	dir := fixtureDir(t)
	plain := stats(t, "-metrics", dir)

	path := filepath.Join(t.TempDir(), "report.html")
	withHTML := stats(t, "-metrics", dir, "-html", path)

	rest, ok := strings.CutSuffix(withHTML, "\nwrote the HTML report to "+path+"\n")
	if !ok {
		t.Fatalf("-html should end by naming the file it wrote, got:\n%s", withHTML)
	}
	if rest != plain {
		t.Errorf("-html changed the text report:\n--- without ---\n%s\n--- with ---\n%s", plain, rest)
	}
}

func TestStatsHTMLHonoursTheFilters(t *testing.T) {
	page := htmlReportOf(t, fixtureDir(t), "-repo", "scharissis/other")
	if !strings.Contains(page, "scharissis/other#5") {
		t.Errorf("the filtered repo's issue is missing:\n%s", page)
	}
	if strings.Contains(page, "scharissis/polako#12") {
		t.Errorf("-repo did not reach the page — the other repository is still on it:\n%s", page)
	}
	if !strings.Contains(page, "for scharissis/other") {
		t.Errorf("the page should say which filters produced it:\n%s", page)
	}
}

// The shift tables are the point of the per-shift half of the page, so they are
// there whether or not -by asked for one.
func TestStatsHTMLAlwaysBreaksDownByShiftAndIssue(t *testing.T) {
	page := htmlReportOf(t, drainFixtureDir(t))
	for _, want := range []string{"by shift", "by issue"} {
		if !strings.Contains(page, ">"+want+"<") {
			t.Errorf("the page has no %q section:\n%s", want, page)
		}
	}
	// -by names a third table rather than replacing either of those two, so a
	// flag typed alongside -html never turns out to have meant nothing.
	tagged := htmlReportOf(t, fixtureDir(t), "-by", byTag)
	for _, want := range []string{"by tag", "by shift", "by issue"} {
		if !strings.Contains(tagged, ">"+want+"<") {
			t.Errorf("-by tag should add a table, not swap one out; %q is missing:\n%s", want, tagged)
		}
	}
}

// The run log costs a wide table, so it stays behind the same flag it does in
// text rather than appearing because the output happens to be a page.
func TestStatsHTMLIncludesTheRunLogOnlyWithRuns(t *testing.T) {
	dir := fixtureDir(t)
	if page := htmlReportOf(t, dir); strings.Contains(page, ">run log<") {
		t.Errorf("the run log should need -runs, as it does in text:\n%s", page)
	}
	if page := htmlReportOf(t, dir, "-runs"); !strings.Contains(page, ">run log<") {
		t.Errorf("-runs should add the run log to the page:\n%s", page)
	}
}

// An empty directory still gets a file. A nightly `stats -html` that skipped
// the write would leave yesterday's numbers on disk looking like today's.
func TestStatsHTMLOnAnEmptyDirectory(t *testing.T) {
	page := htmlReportOf(t, filepath.Join(t.TempDir(), "never-drained"))
	if !strings.Contains(page, "No run data here") {
		t.Errorf("want a page that says there is nothing to report:\n%s", page)
	}
	if strings.Contains(page, "<table") {
		t.Errorf("nothing to tabulate, so no empty tables:\n%s", page)
	}
}

// A window that clears out a directory full of records is not a sign that
// recording was off, and the text report says nothing of the sort. Blaming
// -metrics here would send the operator after the wrong thing.
func TestStatsHTMLOnAnEmptyWindowOverFullFiles(t *testing.T) {
	page := htmlReportOf(t, fixtureDir(t), "-since", "1h")
	if !strings.Contains(page, "No run data here in the last 1h.") {
		t.Errorf("want a page that says the window is empty:\n%s", page)
	}
	if strings.Contains(page, "-metrics off") {
		t.Errorf("the records are there, only outside the window — do not blame -metrics:\n%s", page)
	}
}

// Errors say what to do about it: this one runs unattended often enough that
// its output is the only diagnostic.
func TestStatsHTMLSaysWhatToDoAboutABadPath(t *testing.T) {
	dir := fixtureDir(t)
	cases := map[string]string{
		"a directory":      t.TempDir(),
		"a missing folder": filepath.Join(t.TempDir(), "no-such-folder", "report.html"),
	}
	for name, path := range cases {
		t.Run(name, func(t *testing.T) {
			clearEnvDefaults(t)
			var buf strings.Builder
			err := runStats([]string{"-metrics", dir, "-html", path}, &buf, io.Discard, fixtureNow, report{})
			if err == nil {
				t.Fatalf("-html %s succeeded, want an error explaining what to type instead", path)
			}
			if !strings.Contains(err.Error(), path) {
				t.Errorf("the error should name the path it could not write: %v", err)
			}
			// The text report is still worth having when only the file failed.
			if !strings.Contains(buf.String(), "run data from") {
				t.Errorf("the text report should have printed regardless:\n%s", buf.String())
			}
		})
	}
}

// The page is the private records laid out, so it inherits their mode. A
// default umask would show them to everyone with an account on the machine.
func TestStatsHTMLIsNotWorldReadable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits are not how Windows decides this")
	}
	// Both the file -html creates and one it overwrites: O_CREATE's mode applies
	// only to the former, and a nightly report lands on a path that already
	// exists every night but the first.
	for _, existing := range []os.FileMode{0, 0o644} {
		path := filepath.Join(t.TempDir(), "report.html")
		if existing != 0 {
			if err := os.WriteFile(path, []byte("yesterday"), existing); err != nil {
				t.Fatalf("writing the file to overwrite: %v", err)
			}
		}
		stats(t, "-metrics", fixtureDir(t), "-html", path)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if mode := info.Mode().Perm(); mode&0o077 != 0 {
			t.Errorf("%s is mode %04o after overwriting mode %04o — group and other must have no access",
				path, mode, existing)
		}
	}
}

// Record fields are copied out of a file that another shift, or another person,
// may have written. html/template is chosen for exactly this.
func TestStatsHTMLEscapesWhatItReadFromTheRecords(t *testing.T) {
	dir := t.TempDir()
	body := `{"v":1,"kind":"run","ts":"2026-08-20T09:00:00Z","ended":"2026-08-20T09:10:00Z","repo":"r/r","issue":1,"reason":"implement","status":"ok","outcome":"opened_pr","pr":2,"cost_usd":1.5,"turns":9,"tag":"<script>alert(1)</script>"}
`
	if err := os.WriteFile(filepath.Join(dir, "r--r.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	page := htmlReportOf(t, dir, "-by", byTag)
	if strings.Contains(page, "<script>") {
		t.Errorf("a tag out of a record file reached the page unescaped:\n%s", page)
	}
	if !strings.Contains(page, "&lt;script&gt;") {
		t.Errorf("the tag should still be shown, escaped:\n%s", page)
	}
}

// --- the pieces, unit by unit ---

// A repo name is copied out of a record file and is not guaranteed to be a
// GitHub path at all. A fabricated link that 404s is worse for a reader than a
// cell that is plainly not a link.
func TestIssueURLLinksOnlyPlainRepoNames(t *testing.T) {
	cases := map[string]struct {
		repo  string
		issue int
		want  string
	}{
		"owner/name":      {"scharissis/polako", 12, "https://github.com/scharissis/polako/issues/12"},
		"dots and dashes": {"some-org/repo.js", 3, "https://github.com/some-org/repo.js/issues/3"},
		"no issue number": {"scharissis/polako", 0, ""},
		"no repo":         {"", 1, ""},
		"no slash":        {"polako", 1, ""},
		"two slashes":     {"a/b/c", 1, ""},
		"a traversal":     {"../../etc", 1, ""},
		"a space":         {"own er/name", 1, ""},
		"a scheme":        {"javascript:alert/1", 1, ""},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := issueURL(c.repo, c.issue); got != c.want {
				t.Errorf("issueURL(%q, %d) = %q, want %q", c.repo, c.issue, got, c.want)
			}
		})
	}
}

// A year of daily bars is 365 slivers with no room to label them, so the bucket
// widens with the window.
func TestChartUnitWidensWithTheWindow(t *testing.T) {
	day := 24 * time.Hour
	cases := map[time.Duration]string{
		0:         "day",
		3 * day:   "day",
		62 * day:  "day",
		63 * day:  "week",
		434 * day: "week",
		435 * day: "month",
		900 * day: "month",
	}
	for span, want := range cases {
		if got := chartUnit(span); got != want {
			t.Errorf("chartUnit(%v) = %q, want %q", span, got, want)
		}
	}
}

// Buckets snap in UTC, the clock every other timestamp in the report is printed
// in — so a bar and the run log row behind it always agree about which day it
// was. Weeks start Monday, which means the same thing wherever the file is read.
func TestBucketStartSnapsInUTC(t *testing.T) {
	// A Saturday, late enough that a westward local clock would call it Friday.
	ts := time.Date(2026, 8, 22, 23, 30, 0, 0, time.UTC)
	cases := map[string]string{
		"day":   "2026-08-22",
		"week":  "2026-08-17", // the Monday before
		"month": "2026-08-01",
	}
	for unit, want := range cases {
		if got := bucketStart(ts, unit).Format("2006-01-02"); got != want {
			t.Errorf("bucketStart(%v, %q) = %s, want %s", ts, unit, got, want)
		}
	}
}

// A gap in the work is part of the shape. Skipping the quiet days would draw a
// batch that ran solidly for a fortnight.
func TestCostChartKeepsTheQuietDays(t *testing.T) {
	dir := t.TempDir()
	body := `{"v":1,"kind":"run","ts":"2026-08-20T09:00:00Z","ended":"2026-08-20T09:10:00Z","repo":"r/r","issue":1,"reason":"implement","status":"ok","outcome":"nothing","cost_usd":1}
{"v":1,"kind":"run","ts":"2026-08-24T09:00:00Z","ended":"2026-08-24T09:10:00Z","repo":"r/r","issue":2,"reason":"implement","status":"ok","outcome":"nothing","cost_usd":3}
`
	if err := os.WriteFile(filepath.Join(dir, "r--r.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	ds, err := loadRecords(dir, statsOptions{}, fixtureNow)
	if err != nil {
		t.Fatalf("loading: %v", err)
	}
	chart := costChart(ds)
	if len(chart.Bars) != 5 { // the 20th through the 24th, three of them empty
		t.Fatalf("want a bar for every day from the 20th to the 24th, got %d", len(chart.Bars))
	}
	if chart.Max != "$3.00" {
		t.Errorf("the axis should top out at the busiest day, got %s", chart.Max)
	}
	// The tallest bar is the last one, and the empty days are hairlines rather
	// than gaps where no bar was drawn.
	if chart.Bars[4].H <= chart.Bars[0].H || chart.Bars[1].H <= 0 {
		t.Errorf("bar heights do not follow the spend: %+v", chart.Bars)
	}
	for _, tick := range chart.Ticks {
		if tick.Y != chart.Bottom {
			t.Errorf("a date label sits off the baseline: %+v", tick)
		}
	}
}

// A record whose timestamp will not parse cannot be put on an axis, and dating
// it to year 1 would stretch the chart across two millennia.
func TestCostChartSkipsUndatableRuns(t *testing.T) {
	ds := dataset{runs: []runRecord{
		{TS: "not a timestamp", CostUSD: 5},
		{TS: "2026-08-20T09:00:00Z", CostUSD: 2},
	}}
	chart := costChart(ds)
	if len(chart.Bars) != 1 || chart.Max != "$2.00" {
		t.Errorf("want the one datable run charted alone, got %d bars topping out at %s",
			len(chart.Bars), chart.Max)
	}
}

// A timestamp centuries out parses perfectly well, and months are the widest
// bucket there is. Charting it anyway would be a hundred thousand rectangles
// and a page weighing megabytes.
func TestCostChartRefusesAnAbsurdSpan(t *testing.T) {
	chart := costChart(dataset{runs: []runRecord{
		{TS: "2026-08-20T09:00:00Z", CostUSD: 2},
		{TS: "9999-01-01T00:00:00Z", CostUSD: 1},
	}})
	if chart.Empty == "" {
		t.Errorf("want the chart to bow out and say why, got %d bars", len(chart.Bars))
	}
	if len(chart.Bars) != 0 {
		t.Errorf("want no bars at all, got %d", len(chart.Bars))
	}
}

func TestCostChartWithNothingDatable(t *testing.T) {
	chart := costChart(dataset{runs: []runRecord{{TS: "", CostUSD: 5}}})
	if chart.Empty == "" {
		t.Errorf("a chart with no usable timestamps should say so rather than draw an empty axis")
	}
	if len(chart.Bars) != 0 {
		t.Errorf("want no bars, got %d", len(chart.Bars))
	}
}

// The breakdown shows in-flight issues as one of the slices. Leaving them out
// would make the shares add up to less than everything and say so nowhere.
func TestIssueBarsCountTheOnesStillInFlight(t *testing.T) {
	merged := issueRecord{Outcome: issueMerged}
	breakdown := issueBreakdown([]*issueStats{
		{terminal: &merged},
		{terminal: &merged},
		{}, // no terminal record yet
		{}, //
	})
	// The caption carries the denominator: these shares are over every issue,
	// while the "terminal" line on the same page is over the ones that finished.
	if breakdown.Caption != "of 4 issues" {
		t.Errorf("caption = %q, want the total the shares are of", breakdown.Caption)
	}
	got := map[string]htmlBar{}
	for _, b := range breakdown.Bars {
		got[b.Label] = b
	}
	if b := got[issueMerged]; b.Value != "2 (50%)" || b.Width != 50 {
		t.Errorf("merged bar = %+v, want half the width and 2 (50%%)", b)
	}
	if b := got[inFlight]; b.Value != "2 (50%)" {
		t.Errorf("in-flight bar = %+v, want 2 (50%%)", b)
	}
}

// A status this version has never seen belongs to a newer writer. It is shown
// rather than dropped, and it is not coloured as bad news on a guess.
func TestProportionBarsKeepUnknownValuesNeutral(t *testing.T) {
	bars := proportionBars(map[string]int{"ok": 3, "brand_new": 1}, []string{"ok"})
	if len(bars) != 2 {
		t.Fatalf("want both values shown, got %+v", bars)
	}
	if bars[0].Label != "ok" || bars[0].Tone != "good" {
		t.Errorf("the listed order should lead: %+v", bars[0])
	}
	if bars[1].Label != "brand new" || bars[1].Tone != "flat" {
		t.Errorf("an unknown value should be shown, neutral: %+v", bars[1])
	}
}

// The plan-cost card joins the headline row only when there is a usable
// sample to show — same "absent, not a card of zeroes" rule the text line
// follows. Built through statsReport with a fake claudeBin, like every other
// probe-adjacent stats test in this package, rather than htmlReportOf's
// public runStats: this fixture has samples, so a real "claude" resolved off
// PATH would otherwise be probed for the cross-check.
func TestStatsHTMLPlanCostCard(t *testing.T) {
	t.Setenv(fakeClaudeEnv, "stream")
	t.Setenv(fakeUsageEnv, "sub")
	cfg := config{claudeBin: fakeCLI(t), usageTimeout: 5 * time.Second}
	ds, issues, summary, err := statsReport(context.Background(), cfg, statsOptions{}, planCostDir(t), fixtureNow)
	if err != nil {
		t.Fatalf("statsReport: %v", err)
	}
	path := filepath.Join(t.TempDir(), "report.html")
	if err := writeHTMLReport(path, ds, issues, summary, statsOptions{}, fixtureNow); err != nil {
		t.Fatalf("writeHTMLReport: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the report -html wrote: %v", err)
	}
	page := string(b)
	if !strings.Contains(page, "plan cost per issue") {
		t.Errorf("no plan-cost card in the page:\n%s", page)
	}
	if !strings.Contains(page, "polako 29% of last 24h") {
		t.Errorf("no cross-check figure on the card:\n%s", page)
	}
}

// No terminal issue anywhere in this fixture carries a sample, so the card
// is absent — never a card reporting a percentage nobody measured.
func TestStatsHTMLOmitsPlanCostCardWithoutSamples(t *testing.T) {
	page := htmlReportOf(t, fixtureDir(t))
	if strings.Contains(page, "plan cost per issue") {
		t.Errorf("no samples in this fixture, want no plan-cost card:\n%s", page)
	}
}
