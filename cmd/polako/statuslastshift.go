package main

// The one line of `polako status` that comes from run data rather than
// GitHub: the newest shift this machine recorded for this repository.
//
// This makes status the third reader of ~/.polako/metrics, beside `stats` and
// proposalPricingLine, under the same terms CLAUDE.md gives the other two:
// human-facing rendering, read after the GitHub snapshot is complete, feeding
// nothing that snapshot decides — not a queue row, not `next`, not `needs
// you`. Delete the directory and the line goes, nothing else. It says "here"
// because a drain on another machine leaves no record on this one, and status
// never claims to know about that drain. The shift log stays unread.

import (
	"fmt"
	"os"
	"path/filepath"
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
	ls := &lastShift{id: ds.shift, hint: shiftHint(ds.shift, metricsDir)}
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
	ls.span = end.Sub(ls.start)
	// now's zone — the reader's own clock, not the UTC the records carry.
	ls.start = ls.start.In(now.Location())
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

// shiftHint is the stats command that opens shift id's records in dir.
func shiftHint(id, dir string) string {
	hint := "polako stats -shift " + id
	if def, err := defaultMetricsDir(); err != nil || def != dir {
		hint += " -metrics " + dir
	}
	return hint
}

// lastShiftLine renders the line. The span is rounded to the minute, since
// seconds on a multi-hour shift are noise. The hint names the id rather than
// `-shift last`: without -repo, stats' "last" is the newest shift across every
// repository, which need not be this one.
func lastShiftLine(ls *lastShift) string {
	if ls == nil {
		return ""
	}
	span := ls.span
	if span >= time.Minute {
		span = span.Round(time.Minute)
	}
	return fmt.Sprintf("last shift here: %s, %s — %d merged, %d parked, %s — %s",
		ls.start.Format("Jan 2 15:04"), dur(span),
		ls.merged, ls.parked, usd(ls.cost), ls.hint)
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
		Merged: ls.merged, Parked: ls.parked, CostUSD: ls.cost,
	}
}
