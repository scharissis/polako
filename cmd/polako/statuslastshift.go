package main

// What `polako status` takes from run data rather than GitHub: the newest
// shift this machine recorded for this repository, a price on the ready row,
// and why each parked issue parked.
//
// This makes status the third reader of ~/.polako/metrics, beside `stats` and
// proposalPricingLine, under the same terms CLAUDE.md gives the other two:
// human-facing rendering, read after the GitHub snapshot is complete, feeding
// nothing that snapshot decides — which issues sit in which row, `next`,
// `needs you`. The ready and parked rows gain a suffix; their membership is
// GitHub's alone. Delete the directory and the line and the suffixes go,
// nothing else. It says "here" because a drain on another machine leaves no
// record on this one, and status never claims to know about that drain. The
// shift log stays unread.

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// statusMetricsDir resolves status's -metrics before anything is read: ""
// for off (or no home directory), else the directory. A path naming a file
// is refused with the same fix stats gives for it — the glob inside a file
// finds nothing, and a silently missing line would read as "no history".
func statusMetricsDir(spec string) (string, error) {
	dir := resolveDataDir(spec, "metrics", "metrics", "to read run data from")
	if info, err := os.Stat(dir); dir != "" && err == nil && !info.IsDir() {
		return "", fmt.Errorf("-metrics %s is a file, not a directory — pass the directory that holds the .jsonl files (%s)",
			dir, filepath.Dir(dir))
	}
	return dir, nil
}

// lastShift is the newest shift in this repository's records, summed the way
// `stats -shift <id>` would sum it.
type lastShift struct {
	id     string
	start  time.Time
	span   time.Duration
	merged int
	parked int
	cost   float64
	// otherYear is set when the shift started in a year other than now's, so
	// the line names the year rather than passing last September for this one.
	otherYear bool
	// ran is false for a shift that recorded no run — one that only waited
	// on a PR an earlier shift opened, say. Its start is just its first
	// terminal record and it has no span worth printing: it began whenever
	// it began, and wrote nothing until the merge.
	ran bool
	// hint is the stats command that opens this shift, -metrics included
	// when the records came from somewhere stats wouldn't look by default.
	hint string
}

// readLastShift loads this repository's records and keeps the newest shift.
// nil means there is nothing to say: -metrics off (metricsDir ""), no
// records, a directory that would not read, or a newest record written before
// shift ids existed — records with no id are every id-less drain lumped
// together, not one shift, so there is no honest span or count to give. It
// reuses loadRecords + rollUpIssues, so its torn-line, dedupe and -shift last
// rules are exactly the ones `stats` keeps.
func readLastShift(metricsDir, repo string, now time.Time) *lastShift {
	if metricsDir == "" {
		return nil
	}
	ds, err := loadRecords(metricsDir, statsOptions{repo: repo, shift: shiftLast}, now)
	if err != nil || ds.shift == noneGroup || (len(ds.runs) == 0 && len(ds.issues) == 0) {
		return nil
	}
	ls := &lastShift{id: ds.shift, hint: shiftHint(ds.shift, metricsDir), ran: len(ds.runs) > 0}
	var end time.Time
	widen := func(from, to time.Time) {
		if !from.IsZero() && (ls.start.IsZero() || from.Before(ls.start)) {
			ls.start = from
		}
		if to.After(end) {
			end = to
		}
	}
	for _, r := range ds.runs {
		widen(recTime(r.TS), endOf(r))
	}
	for _, r := range ds.issues {
		t := recTime(r.TS)
		widen(t, t)
	}
	if ls.start.IsZero() {
		return nil // no record whose time would parse: nothing to date the line by
	}
	if ls.ran {
		ls.span = end.Sub(ls.start)
	}
	// now's zone — the reader's own clock, not the UTC the records carry.
	ls.start = ls.start.In(now.Location())
	ls.otherYear = ls.start.Year() != now.Year()
	for _, is := range rollUpIssues(ds) {
		ls.cost += is.cost
		switch is.outcome() {
		case issueMerged:
			ls.merged++
		case issueNeedsHuman, issueClosed: // a closed-unmerged PR parks the issue too (parkPRClosed)
			ls.parked++
		}
	}
	return ls
}

// readParkReasons is each issue's park_reason, where the newest local issue
// record for it is a park — nil with no history. An older park the issue has
// since been merged or closed past says nothing about why it's parked now, so
// only the newest record counts; loadRecords' latest-wins dedupe is what makes
// rollUpIssues' terminal that record.
func readParkReasons(metricsDir, repo string, now time.Time) map[int]string {
	if metricsDir == "" {
		return nil
	}
	ds, err := loadRecords(metricsDir, statsOptions{repo: repo}, now)
	if err != nil {
		return nil
	}
	var reasons map[int]string
	for _, is := range rollUpIssues(ds) {
		if is.outcome() != issueNeedsHuman || is.terminal.ParkReason == "" {
			continue
		}
		if reasons == nil {
			reasons = map[int]string{}
		}
		reasons[is.key.issue] = is.terminal.ParkReason
	}
	return reasons
}

// statusRunData is what status reads from run data. Every field is nil with
// no local history or -metrics off, and the report renders as it would
// without it.
type statusRunData struct {
	lastShift   *lastShift
	readyMedian *issueMedian
	parkReasons map[int]string
}

func readStatusRunData(metricsDir, repo string, now time.Time) statusRunData {
	rd := statusRunData{
		lastShift:   readLastShift(metricsDir, repo, now),
		parkReasons: readParkReasons(metricsDir, repo, now),
	}
	if m, ok := mergedMedian(metricsDir, repo, now); ok {
		rd.readyMedian = &m
	}
	return rd
}

// readySuffix prices the ready row: the same median proposalPricingLine
// prices with, times the ready issues a drain would actually run the skill on
// — "" with no history or none to run. A ready issue whose branch already has
// an open PR is left out: restart safety means a drain waits on that PR rather
// than paying for a fresh run.
func (rd statusRunData) readySuffix(ready []int, prs []statusPR) string {
	n := len(ready)
	for _, pr := range prs {
		if slices.Contains(ready, pr.issue) {
			n--
		}
	}
	if rd.readyMedian == nil || n <= 0 {
		return ""
	}
	return " — about " + approxUSD(float64(n)*rd.readyMedian.cost) + " at your median"
}

// parkedRefs is issueRefs with each parked issue's recorded reason beside it,
// where there is one: `#77 (budget)`.
func (rd statusRunData) parkedRefs(parked []int) string {
	refs := make([]string, len(parked))
	for i, n := range parked {
		refs[i] = "#" + strconv.Itoa(n)
		if why := rd.parkReasons[n]; why != "" {
			refs[i] += " (" + why + ")"
		}
	}
	return strings.Join(refs, ", ")
}

// shiftHint is the stats command that opens shift id's records in dir.
func shiftHint(id, dir string) string {
	hint := "polako stats -shift " + id
	if def, err := defaultMetricsDir(); err != nil || def != dir {
		hint += " -metrics " + dir
	}
	return hint
}

// lastShiftLine renders the line. The span takes medianDur's minute
// resolution, since seconds on a multi-hour shift are noise. The hint names
// the id rather than `-shift last`: without -repo, stats' "last" is the newest
// shift across every repository, which need not be this one.
func lastShiftLine(ls *lastShift) string {
	if ls == nil {
		return ""
	}
	layout := "Jan 2 15:04"
	if ls.otherYear {
		layout = "Jan 2 2006 15:04"
	}
	when := ls.start.Format(layout)
	if ls.ran {
		when += ", " + medianDur(ls.span)
	}
	return fmt.Sprintf("last shift here: %s — %d merged, %d parked, %s — %s",
		when, ls.merged, ls.parked, usd(ls.cost), ls.hint)
}

// statusDocLastShift is last_shift in `status -json`. Started is RFC 3339 in
// UTC, as the records themselves are; the text line's local rendering is
// layout, not a fact.
type statusDocLastShift struct {
	Shift       string  `json:"shift"`
	Started     string  `json:"started"`
	SpanSeconds int64   `json:"span_seconds"`
	Merged      int     `json:"merged"`
	Parked      int     `json:"parked"`
	CostUSD     float64 `json:"cost_usd"`
}

func toStatusDocLastShift(ls *lastShift) *statusDocLastShift {
	if ls == nil {
		return nil
	}
	return &statusDocLastShift{
		Shift: ls.id, Started: ls.start.UTC().Format(time.RFC3339), SpanSeconds: int64(ls.span.Seconds()),
		// Cents, as the text line prints it: a float sum of per-run costs
		// otherwise leaks 0.30000000000000004 into the schema.
		Merged: ls.merged, Parked: ls.parked, CostUSD: round2(ls.cost),
	}
}
