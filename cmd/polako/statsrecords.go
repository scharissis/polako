package main

// The record reader and the -window resolver, split out of stats.go (issue
// #149's accretion debt) as a verbatim, self-contained unit. Everything here
// runs before any report is built and answers one of two questions: which
// JSONL records are in scope (loadRecords and the filters and dedupe it
// leans on), and what calendar span did -window name (resolveWindowBounds
// and the week anchoring under it). The rollups and the renderers stay in
// stats.go; nothing there had to change, since this is the same package.
//
// Reader rules, all of them deliberate: skip a line that will not parse (a
// hard kill can leave the last one torn), ignore fields and record kinds this
// version does not know (the schema grows by adding, never by migrating),
// dedupe issue records latest-wins, and order runs by timestamp — never by
// attempt, which resets whenever the supervisor restarts.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
)

// resolveWindowBounds turns -window into calendar bounds, in the machine's
// local zone and via AddDate rather than a fixed Duration — the difference
// that lets "today" and "month" survive a DST change or a month boundary:
// AddDate adds calendar units and lets the clock fall wherever that lands,
// where a fixed 24h/730h add would carry a DST hour's error into every
// bound computed from it.
func resolveWindowBounds(ctx context.Context, cfg config, opt statsOptions, dir string, now time.Time) (windowBounds, *usageSnapshot, error) {
	loc := now.Location()
	switch opt.window {
	case windowToday:
		from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
		return windowBounds{from: from, periodEnd: from.AddDate(0, 0, 1)}, nil, nil
	case windowMonth:
		from := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc)
		return windowBounds{from: from, periodEnd: from.AddDate(0, 1, 0)}, nil, nil
	case windowWeek:
		return resolveWeekWindow(ctx, cfg, now, loc)
	case windowSession:
		from, ok := sessionAnchor(dir, opt, now)
		if !ok {
			// No run in the lookback to anchor to: a plain 5h ending now,
			// still labelled approximate below rather than presented as a
			// real block boundary nobody has seen.
			from = now.Add(-sessionWindow)
		}
		return windowBounds{from: from, periodEnd: from.Add(sessionWindow), anchor: "approximate"}, nil, nil
	}
	return windowBounds{}, nil, fmt.Errorf("unknown -window %q", opt.window) // unreachable: runStats validates first
}

// resolveWeekWindow anchors to the plan's own weekly reset when probeUsage
// can answer, and falls back to the most recent Monday 00:00 local
// otherwise, saying which one won.
func resolveWeekWindow(ctx context.Context, cfg config, now time.Time, loc *time.Location) (windowBounds, *usageSnapshot, error) {
	fallback := windowBounds{from: mondayStart(now, loc), anchor: "monday"}
	fallback.periodEnd = fallback.from.AddDate(0, 0, 7)
	snap, ok := probeUsage(ctx, cfg)
	if !ok {
		return fallback, nil, nil
	}
	if bounds, ok := weekAnchorFromProbe(snap, now); ok {
		return bounds, &snap, nil
	}
	return fallback, &snap, nil
}

// weekAnchorFromProbe is resolveWeekWindow's pure half, split out so it can
// be tested against a hand-built snapshot rather than a live probe whose own
// reset parsing is keyed to the real wall clock (probeUsage calls
// time.Now() itself — see usage.go — so nothing above this function can be
// pinned to a test's own clock). Rolls the reset clause back in 7-day steps
// to the most recent occurrence at or before now: the clause names the
// *next* reset, which for a weekly pool is always exactly 7 days ahead of
// the one before it.
func weekAnchorFromProbe(snap usageSnapshot, now time.Time) (windowBounds, bool) {
	week, ok := poolByLabel(snap.pools, "week")
	if !ok || !week.hasReset {
		return windowBounds{}, false
	}
	anchor := week.reset
	for anchor.After(now) {
		anchor = anchor.AddDate(0, 0, -7)
	}
	return windowBounds{from: anchor, periodEnd: anchor.AddDate(0, 0, 7), anchor: "the plan's reset"}, true
}

// mondayStart is the most recent Monday 00:00 local at or before now — the
// week -window's fallback anchor, and the same "Monday means the same thing
// to everyone" reasoning the HTML chart's own week bucketing already uses
// (statshtml.go's bucketStart), applied in the local zone rather than UTC
// since this is what a person reads the report in.
func mondayStart(now time.Time, loc *time.Location) time.Time {
	local := now.In(loc)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	return day.AddDate(0, 0, -((int(day.Weekday()) + 6) % 7))
}

// sessionAnchor finds the earliest run in the last 5h, the approximation
// -window session anchors to. It reuses loadRecords rather than duplicating
// its record-reading and -repo filtering, bounded to the same 5h lookback
// the resulting window can never exceed — so this never reads more of the
// directory than the final report will.
func sessionAnchor(dir string, opt statsOptions, now time.Time) (time.Time, bool) {
	probe := opt
	probe.since, probe.window = sessionWindow, ""
	ds, err := loadRecords(dir, probe, now)
	if err != nil {
		return time.Time{}, false
	}
	var earliest time.Time
	for _, r := range ds.runs {
		t := recTime(r.TS)
		if t.IsZero() {
			continue
		}
		if earliest.IsZero() || t.Before(earliest) {
			earliest = t
		}
	}
	return earliest, !earliest.IsZero()
}

// statsDir resolves where to read from. "off" is the one value that means
// something to the writer and nothing here, so say that rather than looking
// for a directory called "off".
func statsDir(spec string) (string, error) {
	dir := strings.TrimSpace(spec)
	if strings.EqualFold(dir, metricsOff) {
		return "", fmt.Errorf("-metrics off has nothing to read — point it at a directory, "+
			"or omit it for the default (%s)", "~/.polako/metrics")
	}
	if dir == "" {
		home, err := defaultMetricsDir()
		if err != nil {
			return "", fmt.Errorf("no home directory to read run data from (%w) — pass -metrics <dir>", err)
		}
		return home, nil
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	// Naming the record file itself is the easy mistake, and globbing inside
	// it silently finds nothing — which reads as "you have no run data" when
	// the data is in the very path just given.
	if info, err := os.Stat(dir); err == nil && !info.IsDir() {
		return "", fmt.Errorf("-metrics %s is a file, not a directory — pass the directory that holds the .jsonl files (%s)",
			dir, filepath.Dir(dir))
	}
	return dir, nil
}

func loadRecords(dir string, opt statsOptions, now time.Time) (dataset, error) {
	ds := dataset{dir: dir}
	paths, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil {
		return ds, err
	}
	sort.Strings(paths) // deterministic output whatever order the filesystem lists
	var cutoff time.Time
	if opt.since > 0 {
		cutoff = now.Add(-opt.since)
	}
	// Kept as read, not deduped yet: -drain has to be applied first, and the
	// dedupe below is what would otherwise decide between two drains' records
	// before either was filtered out.
	var terminal []issueRecord
	for _, path := range paths {
		f, err := os.Open(path)
		if err != nil {
			// One file nobody can read is not a reason to report nothing.
			// A -metrics directory shared with a team is full of files
			// written 0600 by other people; the readable ones still count.
			ds.unread = append(ds.unread, filepath.Base(path))
			continue
		}
		ds.files++
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
		for sc.Scan() {
			line := bytes.TrimSpace(sc.Bytes())
			if len(line) == 0 {
				continue
			}
			var head struct {
				Kind string `json:"kind"`
			}
			if json.Unmarshal(line, &head) != nil {
				ds.skipped++
				continue
			}
			switch head.Kind {
			case "run":
				var rec runRecord
				if json.Unmarshal(line, &rec) != nil {
					ds.skipped++
					continue
				}
				if inScope(rec.Repo, rec.TS, opt, cutoff) {
					ds.runs = append(ds.runs, rec)
				}
			case "issue":
				var rec issueRecord
				if json.Unmarshal(line, &rec) != nil {
					ds.skipped++
					continue
				}
				if inScope(rec.Repo, rec.TS, opt, cutoff) {
					terminal = append(terminal, rec)
				}
			}
			// Any other kind belongs to a newer writer than this reader.
			// Ignoring it is what lets the schema grow without migrations.
		}
		f.Close()
		if err := sc.Err(); err != nil {
			// A line too long for the buffer, or a read that failed partway.
			ds.skipped++
		}
	}
	// Before the dedupe, never after: two drains can each record the same
	// issue reaching a terminal state, and a latest-wins dedupe run first
	// would hand the drain being asked about the other drain's record — or
	// drop the issue from its report entirely.
	if opt.shift != "" {
		ds.shift = resolveShift(opt.shift, ds.runs, terminal)
		ds.runs = slices.DeleteFunc(ds.runs, func(r runRecord) bool { return !sameShift(r.Shift, ds.shift) })
		terminal = slices.DeleteFunc(terminal, func(r issueRecord) bool { return !sameShift(r.Shift, ds.shift) })
	}
	ds.issues = dedupeIssues(terminal)
	sort.SliceStable(ds.runs, func(i, j int) bool {
		return recTime(ds.runs[i].TS).Before(recTime(ds.runs[j].TS))
	})
	return ds, nil
}

// dedupeIssues keeps one terminal record per issue, latest wins: the
// supervisor can be killed between a merge and its record, so the same issue
// may reach a terminal state twice. The newest line is the one that happened,
// and among lines sharing a timestamp it is the last one written.
func dedupeIssues(records []issueRecord) []issueRecord {
	latest := map[issueKey]issueRecord{}
	for _, rec := range records {
		key := issueKey{rec.Repo, rec.Issue}
		if prev, ok := latest[key]; !ok || !recTime(rec.TS).Before(recTime(prev.TS)) {
			latest[key] = rec
		}
	}
	out := make([]issueRecord, 0, len(latest))
	for _, rec := range latest {
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool { return lessIssue(out[i], out[j]) })
	return out
}

// resolveShift turns the -drain value into the id to filter on. "last" is the
// drain that wrote the newest record already in scope, so it composes with
// -repo and -since rather than overriding them: the last drain to touch one
// repository is a different question from the last drain overall.
//
// Records are timed by when they were written — a run's end, not its start —
// because a long run begun before a second drain started is still the older
// record of the two.
func resolveShift(spec string, runs []runRecord, issues []issueRecord) string {
	if !strings.EqualFold(spec, shiftLast) {
		return spec
	}
	var newest time.Time
	id := ""
	for _, r := range runs {
		if t := endOf(r); t.After(newest) {
			newest, id = t, r.Shift
		}
	}
	for _, r := range issues {
		if t := recTime(r.TS); t.After(newest) {
			newest, id = t, r.Shift
		}
	}
	// Nothing in scope, or nothing in scope carrying an id: either way the
	// answer is the set of records with none, which is what the -by table
	// already calls (none).
	if id == "" {
		return noneGroup
	}
	return id
}

// sameShift matches a record's id against the one -drain resolved to.
func sameShift(got, want string) bool {
	if want == noneGroup {
		return got == ""
	}
	return strings.EqualFold(got, want)
}

func lessIssue(a, b issueRecord) bool {
	if a.Repo != b.Repo {
		return a.Repo < b.Repo
	}
	return a.Issue < b.Issue
}

// inScope applies -repo and -since. A record whose timestamp will not parse
// cannot be shown to be inside a window, so a window excludes it.
func inScope(repo, ts string, opt statsOptions, cutoff time.Time) bool {
	if opt.repo != "" && !strings.EqualFold(repo, opt.repo) {
		return false
	}
	if cutoff.IsZero() {
		return true
	}
	t := recTime(ts)
	return !t.IsZero() && !t.Before(cutoff)
}

func recTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}
