package main

// `polako stats -html <path>` writes the same report as one HTML file.
//
// Two rules shape everything below.
//
// It is a second *view*, never a second *derivation*. Every number on the page
// comes out of the same helpers the text report prints — sourcePairs,
// issuePairs, runPairs, costPairs, latencyPairs, issueRows, groupRows, runRows
// — so the two can disagree about a figure only by first disagreeing in the
// one place it is computed. The page adds layout: headline cards, proportion
// bars, and cost over time as a chart. It adds no arithmetic of its own beyond
// turning those numbers into pixels.
//
// And self-contained means self-contained. The file holds the operator's
// private numbers, so opening it must not tell anybody it was opened: no
// script, no external stylesheet, no webfont, no image, no url() and no
// @import anywhere in it. The chart is inline SVG built from rectangles whose
// coordinates are computed here in Go. Anchors out to github.com are the one
// exception, and they are not one — a link is fetched when it is clicked, not
// when the page loads. A test asserts all of this against the rendered bytes.
//
// html/template rather than text/template because a tag, a repo name or a
// model string arrives from a record file that may have been written by
// somebody else's shift. The contextual escaping is the reason to pay for the
// heavier package.

import (
	"bytes"
	"fmt"
	"html/template"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// writeHTMLReport renders the page and puts it at path.
func writeHTMLReport(path string, ds dataset, issues []*issueStats, summary statsSummary, opt statsOptions, now time.Time) error {
	// Named early: a directory here is the easy mistake, and OpenFile's own
	// "is a directory" says nothing about what to type instead.
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		return fmt.Errorf("-html %s is a directory — pass the file to write, e.g. %s",
			path, filepath.Join(path, "polako.html"))
	}
	// Rendered whole before anything is opened, so a template that fails leaves
	// yesterday's report intact rather than a truncated page that still opens.
	var buf bytes.Buffer
	if err := htmlPage().Execute(&buf, buildHTMLReport(ds, issues, summary, opt, now)); err != nil {
		return fmt.Errorf("could not render the HTML report (%w) — this is a bug in polako; "+
			"the text report above is unaffected", err)
	}
	// 0600, the same as the records themselves: this page is those private
	// numbers laid out, and a default umask would show them to everyone with an
	// account on the machine.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("could not write the HTML report to %s (%w) — "+
			"check that the directory exists and is writable", path, err)
	}
	// O_CREATE's mode applies only to a file it had to create, so overwriting
	// last night's report — or a path somebody touched first under a default
	// umask — would keep whatever mode it already had. Re-applied before a byte
	// of it is written, so the numbers are never briefly world-readable.
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return fmt.Errorf("could not restrict %s to your account (%w) — "+
			"nothing was written to it; fix that file's permissions or pass another path", path, err)
	}
	if _, err := f.Write(buf.Bytes()); err != nil {
		f.Close()
		return fmt.Errorf("could not write the HTML report to %s (%w)", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("could not finish writing the HTML report to %s (%w)", path, err)
	}
	return nil
}

// --- the shapes the template walks ---

// htmlCell is one table cell: its text, the GitHub page it names if it names
// one, and whether it is a number and so right-aligns under its heading.
type htmlCell struct {
	Text  string
	Href  string
	Right bool
}

type htmlTable struct {
	Title string
	Head  []htmlCell
	Rows  [][]htmlCell
	Note  string
	Empty string // shown in place of the rows when there are none
}

type htmlCard struct {
	Label string
	Value string
	Note  string
}

// htmlBar is one slice of a breakdown — "merged 3 (60%)" — drawn as a bar as
// wide as its share.
type htmlBar struct {
	Label string
	Value string
	Width float64
	Tone  string
}

// htmlBreakdown is a set of those bars under the total they are shares of. The
// caption is not decoration: these percentages are over every issue, while the
// "terminal" line further down the page is over the ones that finished, so two
// true numbers about merges sit a screen apart and differ. Naming the
// denominator on each is what stops that reading as a bug.
type htmlBreakdown struct {
	Caption string
	Bars    []htmlBar
}

type htmlSection struct {
	Title string
	Pairs [][2]string
}

type htmlReport struct {
	Title      string
	Dir        string
	Generated  string
	Version    string
	Facts      [][2]string
	Empty      string // set when there is nothing to report, and then nothing else is
	Cards      []htmlCard
	Chart      htmlChart
	Breakdowns []htmlBreakdown
	Sections   []htmlSection
	Tables     []htmlTable
}

// inFlight is the pseudo-outcome for an issue with no terminal record. The
// text report keeps it on its own line; a breakdown of what became of the
// issues has to show it as one of the slices, or the shares add up to less than
// everything and say so nowhere.
const inFlight = "in flight"

func buildHTMLReport(ds dataset, issues []*issueStats, summary statsSummary, opt statsOptions, now time.Time) htmlReport {
	rep := htmlReport{
		Title:     "polako run data",
		Dir:       ds.dir,
		Generated: stamp(now),
		Version:   polakoVersion(),
		Facts:     sourcePairs(summary.source),
	}
	if len(ds.runs) == 0 && len(ds.issues) == 0 {
		rep.Empty = "No run data here" + scopeSuffix(opt, ds) + "."
		// Gated exactly as the text report gates it: a -since window that
		// cleared out a directory full of records says nothing about whether
		// recording was on, and blaming -metrics sends the operator after the
		// wrong thing. Files that could not be opened are in the facts above.
		if ds.files == 0 {
			rep.Empty += " A work run records automatically, unless it was run with -metrics off."
		}
		return rep
	}

	rep.Cards = headlineCards(summary)
	if card, ok := planCostCard(summary.plan); ok {
		rep.Cards = append(rep.Cards, card)
	}
	rep.Chart = costChart(ds)
	for _, b := range []htmlBreakdown{issueBreakdown(issues), runBreakdown(ds)} {
		if len(b.Bars) > 0 {
			rep.Breakdowns = append(rep.Breakdowns, b)
		}
	}
	rep.Sections = []htmlSection{
		{"issues", issuePairs(summary.issues, summary.plan)},
		{"runs", runPairs(summary.runs)},
		{"cost", costPairs(summary.cost)},
		{"human latency", latencyPairs(summary.latency)},
	}

	// By shift and by issue always: they are the two breakdowns this page
	// exists for. -by model and -by tag add a third, so no flag an operator
	// typed alongside -html turns out to have meant nothing.
	rep.Tables = append(rep.Tables, groupTable(ds, issues, byShift))
	if opt.by == byModel || opt.by == byTag {
		rep.Tables = append(rep.Tables, groupTable(ds, issues, opt.by))
	}
	rep.Tables = append(rep.Tables, perIssueTable(issues))
	if opt.runs {
		rep.Tables = append(rep.Tables, runLogTable(ds))
	}
	return rep
}

// headlineCards are the half-dozen numbers somebody opens this file to see —
// read straight out of the same statsSummary the sections below format, so
// the cards cannot drift from the numbers a screen down.
func headlineCards(summary statsSummary) []htmlCard {
	cost, runs, iss := summary.cost, summary.runs, summary.issues

	perDay := ""
	if days := cost.windowTo.Sub(cost.windowFrom).Hours() / 24; days >= 1.0/24 {
		perDay = usd(cost.totalUSD/days) + "/day over " + dur(cost.windowTo.Sub(cost.windowFrom))
	}
	rate, rateNote := noValue, "nothing terminal yet"
	if iss.done > 0 {
		merged := iss.terminal[issueMerged]
		rate = percent(merged, iss.done)
		rateNote = fmt.Sprintf("%d of %s", merged, plural(iss.done, "terminal issue"))
	}
	// cost.merged is the same denominator costPairs uses: an issue whose runs
	// a window clipped away contributed nothing to the spend above, so
	// counting it here would price the tool below what it cost.
	perMerge, perMergeNote := noValue, "nothing merged in this window"
	if cost.merged > 0 {
		perMerge = usd(cost.totalUSD / float64(cost.merged))
		perMergeNote = "across " + plural(cost.merged, "merge")
	}
	return []htmlCard{
		{"total spend", usd(cost.totalUSD), perDay},
		{"merge rate", rate, rateNote},
		{"per merged PR", perMerge, perMergeNote},
		{"runs", strconv.Itoa(runs.total), fmt.Sprintf("%s, %s", plural(runs.turns, "turn"), plural(runs.tools, "tool use"))},
		{"tokens", count(cost.tokens.total()), "in, out and cache"},
		{"in flight", strconv.Itoa(iss.inFlight), "no terminal record yet"},
	}
}

// planCostCard is the plan-cost-per-issue card, conditional on there being a
// usable sample — the same "absent, not a card of zeroes" rule
// planCostPairs applies to the text report's own line.
func planCostCard(p planCostSummary) (htmlCard, bool) {
	if p.n == 0 {
		return htmlCard{}, false
	}
	note := "of a week, median — upper bound"
	if p.median > 0 {
		note = fmt.Sprintf("median, about %d issues to a full week", int(math.Round(100/p.median)))
	}
	if p.hasCrossCheck {
		note += fmt.Sprintf(" — polako %d%% of last %s", p.crossCheckPercent, crossCheckWindowLabel(p))
	}
	return htmlCard{"plan cost per issue", pct1(p.median), note}, true
}

// --- proportion bars ---

// tone colours a slice by what it means, so the shape of a batch reads before
// any of the labels do. Everything unrecognised stays neutral rather than being
// guessed at: a record kind from a newer writer is not automatically bad news.
func tone(key string) string {
	switch key {
	case issueMerged, "ok":
		return "good"
	case issueNeedsHuman, issueClosed, "crash", "error", "auth", "no-skill":
		return "bad"
	case inFlight, "no-turns", "stalled", "interrupted", "limit", "budget":
		return "warn"
	}
	return "flat"
}

// proportionBars lays a count breakdown out in the fixed order the text report
// lists it in, so the page and the line under it never name them differently.
// Keys the caller did not list follow it, which is where a value written by a
// newer version ends up.
func proportionBars(counts map[string]int, order []string) []htmlBar {
	total := 0
	for _, n := range counts {
		total += n
	}
	if total == 0 {
		return nil
	}
	seen := map[string]bool{}
	keys := make([]string, 0, len(counts))
	for _, k := range order {
		if counts[k] > 0 {
			keys, seen[k] = append(keys, k), true
		}
	}
	var rest []string
	for k, n := range counts {
		if n > 0 && !seen[k] {
			rest = append(rest, k)
		}
	}
	slices.Sort(rest)
	keys = append(keys, rest...)

	bars := make([]htmlBar, 0, len(keys))
	for _, k := range keys {
		name := label(k)
		if k == "" {
			name = "unrecorded"
		}
		bars = append(bars, htmlBar{
			Label: name,
			Value: fmt.Sprintf("%d (%s)", counts[k], percent(counts[k], total)),
			Width: round2(100 * float64(counts[k]) / float64(total)),
			Tone:  tone(k),
		})
	}
	return bars
}

// issueBreakdown is what became of every issue in scope, in-flight ones
// included: leaving those out would make the shares add up to less than
// everything and say so nowhere.
func issueBreakdown(issues []*issueStats) htmlBreakdown {
	counts := map[string]int{}
	for _, is := range issues {
		counts[is.outcome()]++ // "in flight" when there is no terminal record
	}
	return htmlBreakdown{
		Caption: "of " + plural(len(issues), "issue"),
		// The same order issuePairs lists them in, so the bars and the "terminal"
		// line further down the page never name them differently.
		Bars: proportionBars(counts, []string{issueMerged, issueClosed, issueNeedsHuman, inFlight}),
	}
}

func runBreakdown(ds dataset) htmlBreakdown {
	counts := map[string]int{}
	for _, r := range ds.runs {
		counts[r.Status]++
	}
	return htmlBreakdown{
		Caption: "of " + plural(len(ds.runs), "run"),
		Bars: proportionBars(counts, []string{"ok", "error", "no-turns", "crash", "stalled",
			"interrupted", "no-skill", "auth", "limit", "budget"}),
	}
}

// --- cost over time ---

type htmlChartBar struct {
	X, Y, W, H float64
	Title      string // the browser's own tooltip, which needs no script
}

// htmlChartTick carries its own Y because the template draws it inside a
// range, where the chart's baseline is no longer the dot.
type htmlChartTick struct {
	X, Y   float64
	Anchor string
	Label  string
}

type htmlChart struct {
	Width, Height float64
	Top, Bottom   float64 // the gridlines the bars are measured between
	Left, Right   float64
	Bars          []htmlChartBar
	Max           string
	Zero          string
	Unit          string
	Ticks         []htmlChartTick
	Empty         string
}

// Chart geometry, in the SVG's own user units. The viewBox scales it to
// whatever width the page gives it, so these are proportions rather than
// pixels — and the left gutter is sized for a dollar label, which is the only
// text that has to fit outside the plot.
const (
	chartW    = 720.0
	chartH    = 210.0
	chartPadL = 58.0
	chartPadR = 10.0
	chartPadT = 12.0
	chartPadB = 26.0
)

// costChart is spend bucketed over the window. Each run counts on the day it
// started, not the day it ended: that is the day an operator remembers setting
// it going, and it is the column the run log's "started" already puts it in.
func costChart(ds dataset) htmlChart {
	c := htmlChart{
		Width: chartW, Height: chartH,
		Top: chartPadT, Bottom: chartH - chartPadB,
		Left: chartPadL, Right: chartW - chartPadR,
	}
	spend := map[time.Time]float64{}
	var first, last time.Time
	unit := chartUnit(spanOf(ds))
	for _, r := range ds.runs {
		t := recTime(r.TS)
		if t.IsZero() {
			continue // a timestamp that will not parse cannot be put on an axis
		}
		b := bucketStart(t, unit)
		spend[b] += r.CostUSD
		if first.IsZero() || b.Before(first) {
			first = b
		}
		if b.After(last) {
			last = b
		}
	}
	if first.IsZero() {
		c.Empty = "no runs with a usable timestamp in this window"
		return c
	}

	// Every bucket from first to last, empty ones included: a gap in the work
	// is part of the shape, and skipping the quiet days would draw a batch that
	// ran solidly for a fortnight.
	//
	// maxBuckets is the backstop, because months are the widest bucket there is
	// and a record file can carry any timestamp that parses — one stray year
	// 9999 would otherwise be a hundred thousand rectangles and a page weighing
	// megabytes. Thirty-three years of monthly bars is past the point where the
	// timestamps, not the chart, are what needs looking at.
	const maxBuckets = 400
	var buckets []time.Time
	for b := first; !b.After(last); b = nextBucket(b, unit) {
		if len(buckets) == maxBuckets {
			// The two ends rather than the span between them: a Duration tops
			// out at 292 years, so subtracting these would report a shorter
			// stretch than the timestamps actually name.
			c.Empty = fmt.Sprintf("these runs span %s to %s, which is too long to chart — "+
				"one record with a stray timestamp does this, and the run log names it",
				bucketLabel(first, unit), bucketLabel(last, unit))
			return c
		}
		buckets = append(buckets, b)
	}
	most := 0.0
	for _, b := range buckets {
		most = math.Max(most, spend[b])
	}

	plotW := c.Right - c.Left
	plotH := c.Bottom - c.Top
	slot := plotW / float64(len(buckets))
	// gap keeps neighbouring bars apart; maxBar stops three days of work being
	// drawn as three slabs a fifth of the page wide, which reads as a diagram
	// of something else entirely. minH is a hairline for a bucket that spent
	// nothing, so a quiet day is visibly a day rather than a hole where no bar
	// was drawn.
	const gap, minH, maxBar = 2.0, 1.0, 44.0
	w := math.Min(maxBar, math.Max(1, slot-gap))
	for i, b := range buckets {
		h := minH
		if most > 0 {
			h = math.Max(minH, plotH*spend[b]/most)
		}
		c.Bars = append(c.Bars, htmlChartBar{
			X:     round2(c.Left + float64(i)*slot + (slot-w)/2), // centred in its slot
			Y:     round2(c.Bottom - h),
			W:     round2(w),
			H:     round2(h),
			Title: fmt.Sprintf("%s — %s", bucketLabel(b, unit), usd(spend[b])),
		})
	}
	c.Max, c.Zero, c.Unit = usd(most), usd(0), unit
	c.Ticks = chartTicks(buckets, unit, c.Left, c.Right, c.Bottom)
	return c
}

// spanOf is the window the chart has to fit, measured the same way the report's
// "window" line measures it.
func spanOf(ds dataset) time.Duration {
	from, to := window(ds)
	if from.IsZero() {
		return 0
	}
	return to.Sub(from)
}

// chartUnit picks the bucket that keeps the chart readable. A year of daily
// bars is 365 slivers with no room for a label between them, so past a couple
// of months it becomes weeks, and past a year of weeks, months.
func chartUnit(span time.Duration) string {
	switch days := span.Hours() / 24; {
	case days <= 62:
		return "day"
	case days <= 434: // 62 weeks, the same bar count as 62 days
		return "week"
	}
	return "month"
}

// bucketStart snaps a timestamp to the bucket holding it, in UTC — the same
// clock every other timestamp in the report is printed in, so a bar and the run
// log row behind it always agree about which day it was.
func bucketStart(t time.Time, unit string) time.Time {
	t = t.UTC()
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	switch unit {
	case "week":
		// Monday: an ISO week means the same thing to whoever the file is sent
		// to, which the locale's idea of a first day does not.
		return day.AddDate(0, 0, -((int(day.Weekday()) + 6) % 7))
	case "month":
		return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
	}
	return day
}

func nextBucket(t time.Time, unit string) time.Time {
	switch unit {
	case "week":
		return t.AddDate(0, 0, 7)
	case "month":
		return t.AddDate(0, 1, 0)
	}
	return t.AddDate(0, 0, 1)
}

func bucketLabel(t time.Time, unit string) string {
	switch unit {
	case "week":
		return "week of " + t.Format("2006-01-02")
	case "month":
		return t.Format("2006-01")
	}
	return t.Format("2006-01-02")
}

// chartTicks labels the ends of the axis and nothing in between: two dates say
// what the span is, and a row of them would not fit whatever the bucket.
func chartTicks(buckets []time.Time, unit string, left, right, y float64) []htmlChartTick {
	if len(buckets) == 1 {
		return []htmlChartTick{{X: round2((left + right) / 2), Y: y, Anchor: "middle",
			Label: bucketLabel(buckets[0], unit)}}
	}
	return []htmlChartTick{
		{X: left, Y: y, Anchor: "start", Label: bucketLabel(buckets[0], unit)},
		{X: right, Y: y, Anchor: "end", Label: bucketLabel(buckets[len(buckets)-1], unit)},
	}
}

func round2(f float64) float64 { return math.Round(f*100) / 100 }

// --- tables ---

func perIssueTable(issues []*issueStats) htmlTable {
	t := htmlTableOf("by issue", issueHeader, issueRows(issues), issueLeft,
		"no issues in this window")
	hrefs := make([]string, 0, len(issues))
	for _, is := range issues {
		hrefs = append(hrefs, issueURL(is.key.repo, is.key.issue))
	}
	linkColumn(&t, 0, hrefs)
	return t
}

func groupTable(ds dataset, issues []*issueStats, by string) htmlTable {
	rows, spanning := groupRows(ds, issues, by)
	t := htmlTableOf("by "+by, groupHeader(by), rows, groupLeft,
		"no runs in this window to break down")
	t.Note = spanningNote(spanning, by)
	return t
}

func runLogTable(ds dataset) htmlTable {
	t := htmlTableOf("run log", runHeader, runRows(ds), runLeft, "no runs in this window")
	hrefs := make([]string, 0, len(ds.runs))
	for _, r := range ds.runs {
		hrefs = append(hrefs, issueURL(r.Repo, r.Issue))
	}
	linkColumn(&t, 1, hrefs) // the issue column; "started" leads the row
	return t
}

func htmlTableOf(title string, header []string, rows [][]string, left int, empty string) htmlTable {
	t := htmlTable{Title: title, Empty: empty}
	for i, h := range header {
		t.Head = append(t.Head, htmlCell{Text: h, Right: i >= left})
	}
	for _, row := range rows {
		cells := make([]htmlCell, 0, len(row))
		for i, text := range row {
			cells = append(cells, htmlCell{Text: text, Right: i >= left})
		}
		t.Rows = append(t.Rows, cells)
	}
	return t
}

// linkColumn hangs a URL off one column, hrefs aligned to rows. An empty one
// leaves that cell as plain text, which is what a row with nothing to link to
// gets.
func linkColumn(t *htmlTable, col int, hrefs []string) {
	for i := range t.Rows {
		if i < len(hrefs) && col < len(t.Rows[i]) {
			t.Rows[i][col].Href = hrefs[i]
		}
	}
}

// issueURL is the one kind of outward link on the page. Only a plain
// owner/name is linked: repo names are copied out of record files, and a
// fabricated URL that 404s is worse for a reader than a cell that is simply not
// a link.
func issueURL(repo string, issue int) string {
	if issue <= 0 || !plainRepo(repo) {
		return ""
	}
	return fmt.Sprintf("https://github.com/%s/issues/%d", repo, issue)
}

func plainRepo(repo string) bool {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" {
		return false
	}
	for _, part := range [2]string{owner, name} {
		for _, r := range part {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			case r == '-', r == '_', r == '.':
			default:
				return false
			}
		}
	}
	return true
}

// --- the page ---

// htmlPage parses the template once. A parse failure here is a mistake in the
// constant below, not in anything an operator did, so it is worth crashing on
// the first render rather than reporting on every one.
var htmlPage = sync.OnceValue(func() *template.Template {
	return template.Must(template.New("report").Parse(pageTemplate))
})

// pageTemplate is the whole file: markup, styles and chart in one string, since
// "self-contained" is the requirement and a second asset would break it. The
// styles name only fonts already installed on the machine — no @font-face, no
// url(), nothing to fetch — and there is no <script> anywhere by design.
const pageTemplate = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<style>
:root {
  color-scheme: light dark;
  --bg: #fbfaf8; --panel: #ffffff; --ink: #1d1c1a; --muted: #6d6b66;
  --line: #e4e1db; --grid: #efece7;
  --good: #2f7d55; --warn: #a97b17; --bad: #b23f2d; --flat: #7a7873;
  --link: #3f6ba8;
}
@media (prefers-color-scheme: dark) {
  :root {
    --bg: #15151a; --panel: #1e1e25; --ink: #e8e6e1; --muted: #9b9992;
    --line: #313139; --grid: #292930;
    --good: #62b98a; --warn: #d5a54c; --bad: #e07460; --flat: #8b8983;
    --link: #90b1e2;
  }
}
* { box-sizing: border-box; }
body {
  margin: 0; padding: 2.5rem 1.25rem 5rem; background: var(--bg); color: var(--ink);
  font: 15px/1.55 system-ui, -apple-system, "Segoe UI", Roboto, Ubuntu, Cantarell, sans-serif;
}
main { max-width: 60rem; margin: 0 auto; }
a { color: var(--link); }
h1 { font-size: 1.35rem; margin: 0 0 .2rem; letter-spacing: -.01em; }
h2 {
  font-size: .8rem; font-weight: 600; letter-spacing: .08em; text-transform: uppercase;
  color: var(--muted); margin: 2.4rem 0 .7rem;
}
.sub { color: var(--muted); margin: 0 0 1.5rem; font-size: .85rem; word-break: break-all; }
.panel {
  background: var(--panel); border: 1px solid var(--line); border-radius: 10px; padding: 1rem 1.1rem;
}
.facts { margin: 0; display: grid; grid-template-columns: max-content 1fr; gap: .3rem 1rem; font-size: .85rem; }
.facts dt { color: var(--muted); }
.facts dd { margin: 0; word-break: break-word; }
.empty { color: var(--muted); }
.cards { display: grid; grid-template-columns: repeat(auto-fit, minmax(11rem, 1fr)); gap: .75rem; margin-top: 1.25rem; }
.card { background: var(--panel); border: 1px solid var(--line); border-radius: 10px; padding: .85rem 1rem; }
.card .label { color: var(--muted); font-size: .7rem; letter-spacing: .08em; text-transform: uppercase; }
.card .value { font-size: 1.6rem; font-variant-numeric: tabular-nums; margin: .15rem 0 .1rem; }
.card .note { color: var(--muted); font-size: .78rem; }
.chart { width: 100%; height: auto; display: block; }
.chart .grid { stroke: var(--grid); stroke-width: 1; }
.chart .bar { fill: var(--link); }
.chart text { fill: var(--muted); font-size: 11px; font-family: inherit; }
.split { display: grid; grid-template-columns: repeat(auto-fit, minmax(17rem, 1fr)); gap: 1rem; }
.cap { color: var(--muted); font-size: .8rem; margin: 0 0 .7rem; }
.bars { list-style: none; margin: 0; padding: 0; }
.bars li { margin-bottom: .55rem; }
.bars .row { display: flex; justify-content: space-between; font-size: .82rem; gap: 1rem; }
.bars .row .n { color: var(--muted); font-variant-numeric: tabular-nums; white-space: nowrap; }
.bars .track { background: var(--grid); border-radius: 3px; height: 7px; margin-top: .2rem; overflow: hidden; }
.bars .fill { height: 100%; border-radius: 3px; }
.tone-good { background: var(--good); }
.tone-warn { background: var(--warn); }
.tone-bad { background: var(--bad); }
.tone-flat { background: var(--flat); }
.pairs { margin: 0; display: grid; grid-template-columns: max-content 1fr; gap: .3rem 1rem; font-size: .87rem; }
.pairs dt { color: var(--muted); }
.pairs dd { margin: 0; font-variant-numeric: tabular-nums; }
.scroll { overflow-x: auto; }
table { border-collapse: collapse; width: 100%; font-size: .85rem; font-variant-numeric: tabular-nums; }
th, td { padding: .4rem .6rem; border-bottom: 1px solid var(--line); text-align: left; white-space: nowrap; }
th { color: var(--muted); font-weight: 600; font-size: .75rem; letter-spacing: .04em; }
th.n, td.n { text-align: right; }
tbody tr:last-child th, tbody tr:last-child td { border-bottom: 0; }
.note { color: var(--muted); font-size: .8rem; margin: .5rem 0 0; }
footer { color: var(--muted); font-size: .78rem; margin-top: 3rem; border-top: 1px solid var(--line); padding-top: 1rem; }
footer p { margin: .3rem 0; }
</style>
</head>
<body>
<main>
<h1>{{.Title}}</h1>
<p class="sub">{{.Dir}}</p>
<div class="panel"><dl class="facts">
{{- range .Facts}}<dt>{{index . 0}}</dt><dd>{{index . 1}}</dd>{{end}}
</dl></div>

{{if .Empty}}<p class="empty">{{.Empty}}</p>{{else}}

<div class="cards">
{{- range .Cards}}
<div class="card"><div class="label">{{.Label}}</div><div class="value">{{.Value}}</div><div class="note">{{.Note}}</div></div>
{{- end}}
</div>

<h2>cost over time</h2>
<div class="panel">
{{- with .Chart}}
{{- if .Empty}}<p class="empty">{{.Empty}}</p>{{else}}
<svg class="chart" viewBox="0 0 {{.Width}} {{.Height}}" role="img" aria-label="cost per {{.Unit}}">
  <line class="grid" x1="{{.Left}}" y1="{{.Top}}" x2="{{.Right}}" y2="{{.Top}}"></line>
  <line class="grid" x1="{{.Left}}" y1="{{.Bottom}}" x2="{{.Right}}" y2="{{.Bottom}}"></line>
  <text x="{{.Left}}" y="{{.Top}}" dx="-8" dy="4" text-anchor="end">{{.Max}}</text>
  <text x="{{.Left}}" y="{{.Bottom}}" dx="-8" dy="4" text-anchor="end">{{.Zero}}</text>
  {{- range .Bars}}
  <rect class="bar" x="{{.X}}" y="{{.Y}}" width="{{.W}}" height="{{.H}}"><title>{{.Title}}</title></rect>
  {{- end}}
  {{- range .Ticks}}
  <text x="{{.X}}" y="{{.Y}}" dy="18" text-anchor="{{.Anchor}}">{{.Label}}</text>
  {{- end}}
</svg>
<p class="note">one bar per {{.Unit}}, by the {{.Unit}} each run started</p>
{{- end}}
{{- end}}
</div>

{{if .Breakdowns}}
<h2>how they ended</h2>
<div class="split">
{{- range .Breakdowns}}
<div class="panel">
<p class="cap">{{.Caption}}</p>
<ul class="bars">
{{- range .Bars}}
<li><div class="row"><span>{{.Label}}</span><span class="n">{{.Value}}</span></div>
<div class="track"><div class="fill tone-{{.Tone}}" style="width:{{.Width}}%"></div></div></li>
{{- end}}
</ul>
</div>
{{- end}}
</div>
{{end}}

{{range .Sections}}
<h2>{{.Title}}</h2>
<div class="panel"><dl class="pairs">
{{- range .Pairs}}<dt>{{index . 0}}</dt><dd>{{index . 1}}</dd>{{end}}
</dl></div>
{{end}}

{{range .Tables}}
<h2>{{.Title}}</h2>
<div class="panel">
{{- if .Rows}}
<div class="scroll"><table>
<thead><tr>{{range .Head}}<th{{if .Right}} class="n"{{end}}>{{.Text}}</th>{{end}}</tr></thead>
<tbody>
{{- range .Rows}}
<tr>{{range .}}<td{{if .Right}} class="n"{{end}}>{{if .Href}}<a href="{{.Href}}">{{.Text}}</a>{{else}}{{.Text}}{{end}}</td>{{end}}</tr>
{{- end}}
</tbody>
</table></div>
{{- if .Note}}<p class="note">{{.Note}}</p>{{end}}
{{- else}}<p class="empty">{{.Empty}}</p>{{end}}
</div>
{{end}}

{{end}}

<footer>
<p>Written by polako{{if .Version}} {{.Version}}{{end}} at {{.Generated}}. Dollars are the Claude CLI&#39;s API-equivalent pricing.</p>
<p>This page is self-contained: it loads nothing from anywhere, and opening it tells nobody that you did.</p>
</footer>
</main>
</body>
</html>
`
