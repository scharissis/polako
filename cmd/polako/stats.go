package main

// `polako stats` reads the run data back. It is the only reader there
// is: the drain loop never opens these files, so telemetry stays telemetry
// rather than becoming state the supervisor depends on.
//
// Everything summable — cost per issue, runs per issue, question rounds, the
// human-latency spans — is derived here, at read time, from run records. That
// is what keeps the writing side free of rollup state it would have to carry
// across restarts and would get wrong when it didn't.
//
// Reader rules, all of them deliberate: skip a line that will not parse (a
// hard kill can leave the last one torn), ignore fields and record kinds this
// version does not know (the schema grows by adding, never by migrating),
// dedupe issue records latest-wins, and order runs by timestamp — never by
// attempt, which resets whenever the supervisor restarts.
//
// One rule that had to be measured rather than reasoned out: a resumed run's
// row is summed like any other. A --resume'd result event reports that
// invocation and not the session it continued, settled on issue #78 against
// real records, so the two halves of a resumed session do not overlap and
// nothing here has to take a per-session maximum. Reports used to carry a
// footnote hedging on that; they no longer need one.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The -by groupings. Kept short: JSONL is already jq, DuckDB and spreadsheet
// food, so this command answers the questions worth a flag and leaves the long
// tail to those.
const (
	byIssue = "issue"
	byModel = "model"
	byTag   = "tag"
	byShift = "shift"
)

// byGroups is the whitelist and the order the error message lists them in.
var byGroups = []string{byIssue, byModel, byTag, byShift}

// The -window values. Each resolves to a from/periodEnd pair rather than a
// bare cutoff — the calendar boundary is what the header's progress line
// measures against, and what a script (statsDocWindow) wants over "-since
// happened to be 6h19m".
const (
	windowToday   = "today"
	windowWeek    = "week"
	windowMonth   = "month"
	windowSession = "session"
)

// statsWindows is the whitelist and the order the error message lists them
// in, the -window twin of byGroups.
var statsWindows = []string{windowToday, windowWeek, windowMonth, windowSession}

// sessionWindow is the plan's own session length, approximated: see
// resolveWindowBounds's windowSession case.
const sessionWindow = 5 * time.Hour

// orList renders a choice the way the flag's help and its error both want it:
// "issue, model, tag or shift", so a message that lists four reads as English
// rather than as a dump of the slice behind it.
func orList(items []string) string {
	if len(items) < 2 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " or " + items[len(items)-1]
}

// shiftLast is the -drain value meaning "whichever drain wrote the newest
// record in scope". It is what makes the flag usable on a drain that is still
// running, without going back to the startup line for its id.
const shiftLast = "last"

// noneGroup labels records carrying no value for the field in hand — an
// untagged run, and every record written before drain ids existed. It is also
// what -drain accepts for that set, so a row of the -by table can be typed
// straight back in.
const noneGroup = "(none)"

// errFlagsReported marks a flag error the flag package has already printed,
// together with the usage text that explains it. Saying it a second time on
// the way out helps nobody.
var errFlagsReported = errors.New("invalid flags")

type statsOptions struct {
	repo   string
	since  time.Duration
	window string
	by     string
	shift  string
	runs   bool
	html   string
	json   bool
}

// runStats is the `stats` subcommand: parse its own flags, read the records,
// print one report. now is passed in so -since is testable; rpt is the
// styler stats/status share, TTY-detected on stdout at the dispatch in main.
// errOut is where the "-html" write confirmation goes under -json, so that
// stdout can carry exactly one JSON document and nothing else; in text mode
// the confirmation still goes to out, unchanged.
func runStats(args []string, out, errOut io.Writer, now time.Time, rpt report) error {
	fs := flag.NewFlagSet("stats", flag.ContinueOnError)
	fs.SetOutput(out)
	var opt statsOptions
	var metrics string
	fs.StringVar(&metrics, "metrics", "",
		"directory holding the run-data records (default ~/.polako/metrics)")
	fs.StringVar(&opt.repo, "repo", "", "only count records for this repository (owner/name)")
	fs.DurationVar(&opt.since, "since", 0, "only count records newer than this ago (e.g. 168h)")
	fs.StringVar(&opt.window, "window", "",
		"report a calendar-aligned window instead of -since: "+orList(statsWindows))
	fs.StringVar(&opt.shift, "shift", "",
		`only count records from this shift's id, or "`+shiftLast+`" for the newest shift in scope`)
	fs.StringVar(&opt.by, "by", "", "also break the numbers down by "+orList(byGroups))
	// No backquotes in this text: the flag package reads the first backquoted
	// word as the argument's name, and a bool flag has no argument to name.
	fs.BoolVar(&opt.runs, "runs", false,
		"also list the individual runs, with the session id that reopens each one")
	fs.StringVar(&opt.html, "html", "",
		"also write the report to this `path`, as one self-contained HTML file")
	fs.BoolVar(&opt.json, "json", false,
		"print one JSON document to stdout instead of the text report — see docs/run-data.md for the schema")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "Usage: polako stats [flags]\n\n"+
			"Reports on the run data recorded by previous shifts. Reads only;\n"+
			"nothing here changes what a shift does.\n\n"+envUsage+"\nFlags:\n")
		fs.PrintDefaults()
	}
	// The same environment defaults the drain honours, which is what lets one
	// POLAKO_METRICS point both halves at the same directory.
	if err := applyEnvDefaults(fs); err != nil {
		return err
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil // -h is how a person finds the flags, not a failure
		}
		return errFlagsReported
	}
	if rest := fs.Args(); len(rest) > 0 {
		return fmt.Errorf("unexpected argument %q — stats takes flags only", rest[0])
	}
	if opt.since < 0 {
		// Silently ignoring it would report all of time to someone who asked
		// for a window, with no line in the output to correct them.
		return fmt.Errorf("-since %s is negative — it is how far back to look, e.g. -since 168h", opt.since)
	}
	// fs.Visit rather than a zero check: -since 0s is a real (if odd) explicit
	// value, and only an explicit -since should collide with an explicit
	// -window. Neither silently wins over the other.
	var sinceSet, windowSet bool
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "since":
			sinceSet = true
		case "window":
			windowSet = true
		}
	})
	if sinceSet && windowSet {
		return fmt.Errorf("-since and -window both name a window to report — pass one, not both")
	}
	// An explicit argument beats a POLAKO_SINCE/POLAKO_WINDOW default the
	// same way applyEnvDefaults already promises for every flag on its own
	// — but that promise is per-flag, so it says nothing about the other
	// flag in this pair. Without this, an explicit -since with no -window
	// argument would still be silently overridden below by a -window
	// picked up from the environment, and "arguments win" would be false
	// for exactly the one flag pair that checks it.
	if sinceSet {
		opt.window = ""
	} else if windowSet {
		opt.since = 0
	}
	opt.window = strings.TrimSpace(opt.window)
	if opt.window != "" && !slices.Contains(statsWindows, opt.window) {
		return fmt.Errorf("-window %q: choose one of %s", opt.window, orList(statsWindows))
	}
	if opt.by != "" && !slices.Contains(byGroups, opt.by) {
		return fmt.Errorf("-by %q: choose one of %s", opt.by, orList(byGroups))
	}
	opt.shift = strings.TrimSpace(opt.shift)
	opt.html = strings.TrimSpace(opt.html)

	dir, err := statsDir(metrics)
	if err != nil {
		return err
	}
	// context.Background() rather than a param this function would have to
	// grow: the one thing here that reaches outside the process is
	// probeUsage, best-effort and bounded by its own usageTimeout, so nothing
	// downstream needs external cancellation the way a long-running drain
	// does.
	cfg := config{claudeBin: "claude", usageTimeout: defaultUsageProbeTimeout}
	ds, issues, summary, err := statsReport(context.Background(), cfg, opt, dir, now)
	if err != nil {
		return err
	}

	// The confirmation below moves to errOut under -json, so stdout carries
	// exactly the one document a script piping it into jq expects — never in
	// text mode, where it is part of the same report as everything above it.
	confirm := out
	if opt.json {
		if err := renderStatsJSON(out, ds, issues, summary, opt); err != nil {
			return err
		}
		confirm = errOut
	} else {
		render(out, rpt, ds, issues, summary, opt)
	}
	if opt.html == "" {
		return nil
	}
	// A second view of the report just printed, never a replacement for it: an
	// operator who asked for a file still wants to see what went into it, and a
	// command that printed nothing would look like one that did nothing.
	if err := writeHTMLReport(opt.html, ds, issues, summary, opt, now); err != nil {
		return err
	}
	fmt.Fprintf(confirm, "\nwrote the HTML report to %s\n", opt.html)
	return nil
}

// statsReport is runStats's testable core: resolve -window (probing the
// plan's own usage when it might answer either the week anchor or the
// plan-cost cross-check below), load the records it resolved to, and build
// the one summary every renderer — text, -json, -html — formats from. Split
// out so a test can hand it a cfg whose claudeBin points at a fake CLI, the
// same seam readStatus already gives status's own tests.
func statsReport(ctx context.Context, cfg config, opt statsOptions, dir string, now time.Time) (dataset, []*issueStats, statsSummary, error) {
	var bounds windowBounds
	var probe *usageSnapshot
	if opt.window != "" {
		var err error
		bounds, probe, err = resolveWindowBounds(ctx, cfg, opt, dir, now)
		if err != nil {
			return dataset{}, nil, statsSummary{}, err
		}
		// Reuses the existing -since cutoff machinery in loadRecords rather
		// than teaching it a second kind of bound: since = now - from is the
		// exact inverse of the cutoff computation there (cutoff = now -
		// since), so this round-trips to the same instant regardless of DST
		// — time.Time subtraction and addition both work in absolute time,
		// never wall-clock arithmetic.
		opt.since = now.Sub(bounds.from)
	}

	ds, err := loadRecords(dir, opt, now)
	if err != nil {
		return dataset{}, nil, statsSummary{}, err
	}
	issues := rollUpIssues(ds)

	// The plan-cost cross-check needs the same probe attribution -window
	// week may already have fetched above, reused here rather than asked
	// twice; failing that, one attempt iff there is a sample this report
	// would otherwise show with no cross-check at all. -window week always
	// attempts probeUsage once in resolveWindowBounds above regardless of
	// whether it succeeds, so probe == nil there means "already tried and
	// failed," not "never asked" — a second attempt against the same
	// unreachable CLI would only double the cost, never the odds.
	if probe == nil && opt.window != windowWeek && issuesHaveUsageSamples(issues) {
		if snap, ok := probeUsage(ctx, cfg); ok {
			probe = &snap
		}
	}

	summary := buildStatsSummary(ds, issues, opt, now, bounds, probe)
	return ds, issues, summary, nil
}

// windowBounds is what -window resolved to: the span used to filter records
// (from) and the calendar period a progress line is measured against
// (periodEnd) — always in the future relative to from, even when now has
// already passed it (a session block that ended is still worth showing as a
// completed period).
type windowBounds struct {
	from, periodEnd time.Time
	// anchor names how the window picked its start, for the header line and
	// -json's window.anchor. Empty for today and month, whose start is
	// simply the top of the calendar unit and needs no explanation.
	anchor string
}

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

// --- reading ---

type issueKey struct {
	repo  string
	issue int
}

// dataset is everything the reader kept, after filtering.
type dataset struct {
	dir     string
	runs    []runRecord
	issues  []issueRecord // deduped latest-wins by (repo, issue)
	files   int
	skipped int      // lines that were not usable JSON — a torn tail, or junk
	unread  []string // files that could not be opened at all
	// drain is what -drain resolved to: an id, noneGroup for the records
	// written before ids existed, or empty when the flag was not given. The
	// report names this rather than what was typed, so a "last" run says which
	// drain it turned out to cover.
	shift string
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

// enriched reports whether GitHub's answer about the PR reached this record.
// Records written before the enrichment existed carry none, and neither does
// one whose lookup failed, so every number derived from it counts its own
// issues rather than assuming zeros.
func (r issueRecord) enriched() bool {
	return r.ChangedFiles > 0 || r.Additions > 0 || r.Deletions > 0 || r.PROpened != ""
}

// hasUsageSamples reports whether the usage gate's two week-usage samples
// reached this record — the same "either field nonzero" heuristic enriched
// uses above, and for the same reason: 0 and "never sampled" are
// indistinguishable on the wire (WeekUsageAtPickup/WeekUsageAtTerminal's own
// doc comment in metrics.go names this trade), so a record genuinely
// sampled at exactly 0% on both readings reads as unsampled too. That is the
// safe direction — it undercounts the plan-cost line's sample size rather
// than ever averaging in a delta that was never actually measured.
func (r issueRecord) hasUsageSamples() bool {
	return r.WeekUsageAtPickup > 0 || r.WeekUsageAtTerminal > 0
}

// endOf is when a run stopped, falling back to when it started: a record
// written by a path that never learned an end time is still worth a span.
func endOf(r runRecord) time.Time {
	if t := recTime(r.Ended); !t.IsZero() {
		return t
	}
	return recTime(r.TS)
}

// --- per-issue rollups, derived at read time ---

type issueStats struct {
	key      issueKey
	runs     []runRecord // timestamp order
	terminal *issueRecord
	cost     float64
	tokens   tokenCounts
	wallMS   int64
}

func (is *issueStats) questions() int {
	n := 0
	for _, r := range is.runs {
		if r.Outcome == outcomeQuestions {
			n++
		}
	}
	return n
}

func (is *issueStats) outcome() string {
	if is.terminal == nil {
		return inFlight
	}
	return is.terminal.Outcome
}

// rollUpIssues folds runs and terminal records into one entry per issue. An
// issue can appear with no runs (its runs fell outside the window) or with no
// terminal record (still in flight); both are real states worth showing.
func rollUpIssues(ds dataset) []*issueStats {
	index := map[issueKey]*issueStats{}
	get := func(key issueKey) *issueStats {
		if is, ok := index[key]; ok {
			return is
		}
		is := &issueStats{key: key}
		index[key] = is
		return is
	}
	for _, r := range ds.runs {
		is := get(issueKey{r.Repo, r.Issue})
		is.runs = append(is.runs, r) // ds.runs is already in timestamp order
		is.cost += r.CostUSD
		is.tokens.addCounts(r.Tokens)
		is.wallMS += r.WallMS
	}
	for i := range ds.issues {
		get(issueKey{ds.issues[i].Repo, ds.issues[i].Issue}).terminal = &ds.issues[i]
	}
	out := make([]*issueStats, 0, len(index))
	for _, is := range index {
		out = append(out, is)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].key.repo != out[j].key.repo {
			return out[i].key.repo < out[j].key.repo
		}
		return out[i].key.issue < out[j].key.issue
	})
	return out
}

// answerSpans is how long each round of questions waited for a human: from the
// run that posted them to the run that folded the reply back in. A run can do
// both — answer one round and ask another — so the close and the open are
// checked separately, in that order.
func answerSpans(is *issueStats) []time.Duration {
	var spans []time.Duration
	var asked time.Time
	for _, r := range is.runs {
		if r.Reason == reasonAnswers && !asked.IsZero() {
			if d := recTime(r.TS).Sub(asked); d > 0 {
				spans = append(spans, d)
			}
			asked = time.Time{}
		}
		if r.Outcome == outcomeQuestions {
			asked = endOf(r)
		}
	}
	return spans
}

// mergeSpan is PR-open-to-merge. Reported, but confounded by human
// availability in a way time-to-PR is not: nobody merges at 3am.
func mergeSpan(is *issueStats) (time.Duration, bool) {
	if is.terminal == nil || is.terminal.Outcome != issueMerged {
		return 0, false
	}
	// GitHub's own timestamps when the enrichment got them: they measure the
	// PR rather than the runs around it, and they are there even when the run
	// that opened it fell outside the window — or belonged to another drain,
	// on another machine.
	if opened, merged := recTime(is.terminal.PROpened), recTime(is.terminal.PRMerged); !opened.IsZero() && !merged.IsZero() {
		if d := merged.Sub(opened); d > 0 {
			return d, true
		}
	}
	var opened time.Time
	for _, r := range is.runs {
		if r.Outcome != outcomeOpenedPR {
			continue
		}
		if is.terminal.PR != 0 && r.PR != 0 && r.PR != is.terminal.PR {
			continue // a different PR for the same issue: an earlier, abandoned one
		}
		if t := endOf(r); !t.IsZero() && (opened.IsZero() || t.Before(opened)) {
			opened = t
		}
	}
	if opened.IsZero() {
		return 0, false
	}
	if d := recTime(is.terminal.TS).Sub(opened); d > 0 {
		return d, true
	}
	return 0, false
}

// --- typed summary: the numbers, computed once ---
//
// Unlike status (a typed statusSnapshot the text report already formats
// from), stats's summary numbers used to be computed straight into formatted
// [][2]string pairs — there was nowhere for -json to read a figure from
// without recomputing it. statsSummary is that missing typed layer: built
// once from dataset + []*issueStats, it is what sourcePairs, issuePairs,
// runPairs, costPairs and latencyPairs now format prose from, and what
// statsDocFrom (statsjson.go) reads directly. The two can disagree about
// layout; they cannot disagree about a figure, because there is only one
// place each one is computed.
type statsSummary struct {
	source  sourceSummary
	issues  issuesSummary
	runs    runsSummary
	cost    costSummary
	latency latencySummary
	plan    planCostSummary
}

func buildStatsSummary(ds dataset, issues []*issueStats, opt statsOptions, now time.Time, bounds windowBounds, probe *usageSnapshot) statsSummary {
	from, to := window(ds)
	return statsSummary{
		source:  buildSourceSummary(ds, opt, from, to, now, bounds),
		issues:  buildIssuesSummary(issues),
		runs:    buildRunsSummary(ds),
		cost:    buildCostSummary(ds, issues, from, to),
		latency: buildLatencySummary(issues),
		plan:    buildPlanCostSummary(issues, probe),
	}
}

type sourceSummary struct {
	files       int
	records     int
	skipped     int
	unread      []string
	windowFrom  time.Time // the data's own extent — never the requested window, see window(ds)
	windowTo    time.Time
	scope       string // scopeSuffix(opt, ds), trimmed — the text report's own prose
	repos       []string
	repoFilter  string
	sinceFilter time.Duration
	shift       string // ds.shift: the resolved id, "" when -shift was not given

	// The following are set only when -window was given: the resolved
	// calendar bounds it named, which the printed "window" line and
	// statsDocWindow (-json) show in place of the data-extent pair above —
	// the requested window's own edges are what a person who asked for
	// "today" wants named, not wherever the data inside it happened to
	// start and stop.
	reqWindow string // "" unless -window was given: "today"/"week"/"month"/"session"
	reqFrom   time.Time
	reqTo     time.Time // the period's calendar end, not now — see windowBounds
	reqAnchor string    // "" except week ("the plan's reset"/"monday") and session ("approximate")
	reqNow    time.Time
}

func buildSourceSummary(ds dataset, opt statsOptions, from, to time.Time, now time.Time, bounds windowBounds) sourceSummary {
	s := sourceSummary{
		files:       ds.files,
		records:     len(ds.runs) + len(ds.issues),
		skipped:     ds.skipped,
		unread:      ds.unread,
		windowFrom:  from,
		windowTo:    to,
		scope:       strings.TrimSpace(scopeSuffix(opt, ds)),
		repos:       repoNames(ds),
		repoFilter:  opt.repo,
		sinceFilter: opt.since,
		shift:       ds.shift,
	}
	if opt.window != "" {
		s.reqWindow, s.reqFrom, s.reqTo, s.reqAnchor, s.reqNow = opt.window, bounds.from, bounds.periodEnd, bounds.anchor, now
	}
	return s
}

// issuesSummary holds the numbers issuePairs formats. done == 0 means
// "nothing terminal yet", the one state with no averages to report — priced,
// runsMean etc. are meaningless zero values in that case, never read.
type issuesSummary struct {
	done           int
	inFlight       int
	terminal       map[string]int // by outcome, merged included
	parkReasons    map[string]int // by park_reason; nil when nothing was parked
	priced         int
	runsMean       float64
	runsMedian     float64
	costMean       float64
	costMedian     float64
	tokensMean     float64
	tokensMedian   int64
	tokensSplitSum tokenCounts
	tokensSplitN   int64
	change         *changeSummary // nil when no terminal issue carries PR data
}

type changeSummary struct {
	addsMedian    int
	delsMedian    int
	filesMedian   int
	reviewsMedian int
	hasReviews    bool
	n             int
}

func buildIssuesSummary(issues []*issueStats) issuesSummary {
	s := issuesSummary{terminal: map[string]int{}}
	var done []*issueStats
	for _, is := range issues {
		if is.terminal == nil {
			s.inFlight++
			continue
		}
		done = append(done, is)
		s.terminal[is.terminal.Outcome]++
	}
	s.done = len(done)
	if s.done == 0 {
		return s
	}
	if pr := buildParkReasons(done); len(pr) > 0 {
		s.parkReasons = pr
	}
	s.change = buildChangeSummary(done)

	var priced []*issueStats
	for _, is := range done {
		if len(is.runs) > 0 {
			priced = append(priced, is)
		}
	}
	s.priced = len(priced)
	if s.priced == 0 {
		return s
	}
	var runs, costs []float64
	var tokens []int64
	sum := tokenCounts{}
	for _, is := range priced {
		runs = append(runs, float64(len(is.runs)))
		costs = append(costs, is.cost)
		tokens = append(tokens, is.tokens.total())
		sum.addCounts(is.tokens)
	}
	s.runsMean, s.runsMedian = mean(runs), median(runs)
	s.costMean, s.costMedian = mean(costs), median(costs)
	s.tokensMean, s.tokensMedian = mean(tokens), median(tokens)
	s.tokensSplitSum, s.tokensSplitN = sum, int64(len(priced))
	return s
}

// buildParkReasons breaks the hand-backs down by why they happened. A record
// written before the field existed counts as unrecorded (key ""), never as
// unknown, which is the field's own value for a park path that could not say.
func buildParkReasons(done []*issueStats) map[string]int {
	counts := map[string]int{}
	for _, is := range done {
		if is.terminal.Outcome == issueNeedsHuman {
			counts[is.terminal.ParkReason]++
		}
	}
	return counts
}

// buildChangeSummary is what the work actually changed, from the GitHub
// enrichment folded into terminal records. A record written before that
// enrichment existed carries none, and neither does one whose lookup failed,
// so this covers its own set of issues and says how many (n).
func buildChangeSummary(done []*issueStats) *changeSummary {
	var adds, dels, files, reviews []int
	for _, is := range done {
		if !is.terminal.enriched() {
			continue
		}
		adds = append(adds, is.terminal.Additions)
		dels = append(dels, is.terminal.Deletions)
		files = append(files, is.terminal.ChangedFiles)
		reviews = append(reviews, is.terminal.Reviews)
	}
	if len(adds) == 0 {
		return nil
	}
	return &changeSummary{
		addsMedian:    median(adds),
		delsMedian:    median(dels),
		filesMedian:   median(files),
		reviewsMedian: median(reviews),
		// A repository whose PRs are merged without a formal review reports
		// zero every time, and a column of zeros is noise rather than a finding.
		hasReviews: slices.Max(reviews) > 0,
		n:          len(adds),
	}
}

type runsSummary struct {
	total        int
	statuses     map[string]int
	reasons      map[string]int
	outcomes     map[string]int
	approximated int
	turns        int
	tools        int
}

func buildRunsSummary(ds dataset) runsSummary {
	s := runsSummary{total: len(ds.runs), statuses: map[string]int{}, reasons: map[string]int{}, outcomes: map[string]int{}}
	for _, r := range ds.runs {
		s.statuses[r.Status]++
		s.reasons[r.Reason]++
		s.outcomes[r.Outcome]++
		if r.UsageSource == usageObserved {
			s.approximated++
		}
		s.turns += max(r.Turns, 0)
		s.tools += r.ToolUses
	}
	return s
}

type costSummary struct {
	totalUSD   float64
	windowFrom time.Time
	windowTo   time.Time
	merged     int
	tokens     tokenCounts
}

func buildCostSummary(ds dataset, issues []*issueStats, from, to time.Time) costSummary {
	s := costSummary{windowFrom: from, windowTo: to}
	for _, r := range ds.runs {
		s.totalUSD += r.CostUSD
		s.tokens.addCounts(r.Tokens)
	}
	// Only merges with runs in scope belong here: one whose runs a -since
	// window clipped away contributes nothing to the total above, so counting
	// it here would price the tool below what it costs.
	for _, is := range issues {
		if is.terminal != nil && is.terminal.Outcome == issueMerged && len(is.runs) > 0 {
			s.merged++
		}
	}
	return s
}

type latencySummary struct {
	blocked []time.Duration
	toMerge []time.Duration
}

func buildLatencySummary(issues []*issueStats) latencySummary {
	var s latencySummary
	for _, is := range issues {
		s.blocked = append(s.blocked, answerSpans(is)...)
		if d, ok := mergeSpan(is); ok {
			s.toMerge = append(s.toMerge, d)
		}
	}
	return s
}

// --- rendering ---

func render(w io.Writer, rpt report, ds dataset, issues []*issueStats, summary statsSummary, opt statsOptions) {
	if len(ds.runs) == 0 && len(ds.issues) == 0 {
		fmt.Fprintf(w, "no run data in %s%s\n", ds.dir, scopeSuffix(opt, ds))
		if len(ds.unread) > 0 {
			fmt.Fprintf(w, "%s there could not be opened: %s\n",
				plural(len(ds.unread), "file"), strings.Join(ds.unread, ", "))
			return
		}
		if ds.files == 0 {
			fmt.Fprintln(w, "a work run records there automatically, unless it was run with -metrics off.")
		}
		return
	}

	fmt.Fprintf(w, "%s\n", rpt.bold(fmt.Sprintf("run data from %s", ds.dir)))
	printPairs(w, rpt, "", sourcePairs(summary.source))
	printPairs(w, rpt, "issues", issuePairs(summary.issues, summary.plan))
	printPairs(w, rpt, "runs", runPairs(summary.runs))
	printPairs(w, rpt, "cost", costPairs(summary.cost))
	printPairs(w, rpt, "human latency", latencyPairs(summary.latency))

	switch opt.by {
	case byIssue:
		printIssueTable(w, rpt, issues)
	case byModel, byTag, byShift:
		printGroupTable(w, rpt, ds, issues, opt.by)
	}
	// Last, because it is the rawest view of the same rows every summary above
	// it was derived from.
	if opt.runs {
		printRunTable(w, rpt, ds)
	}
}

func scopeSuffix(opt statsOptions, ds dataset) string {
	var parts []string
	if opt.repo != "" {
		parts = append(parts, "for "+opt.repo)
	}
	switch {
	case opt.window != "":
		// The header's own "window" line already gives the resolved bounds
		// and the progress through them; naming the window here too, without
		// repeating either, is what lets a report get to "filtered" without
		// backtracking to work out that -window is why.
		parts = append(parts, "for "+opt.window)
	case opt.since > 0:
		parts = append(parts, "in the last "+dur(opt.since))
	}
	// The resolved id, never the literal "last": a report that names the drain
	// it covered is one that still means the same thing tomorrow.
	if opt.shift != "" {
		parts = append(parts, "from shift "+ds.shift)
	}
	if len(parts) == 0 {
		return ""
	}
	return " " + strings.Join(parts, " ")
}

func sourcePairs(s sourceSummary) [][2]string {
	files := fmt.Sprintf("%s, %s", plural(s.files, "file"), plural(s.records, "record"))
	if s.skipped > 0 {
		files += fmt.Sprintf(" (%s skipped)", plural(s.skipped, "unreadable line"))
	}
	pairs := [][2]string{{"read", files}}
	if len(s.unread) > 0 {
		pairs = append(pairs, [2]string{"could not open",
			fmt.Sprintf("%s — %s", plural(len(s.unread), "file"), strings.Join(s.unread, ", "))})
	}
	switch {
	case s.reqWindow != "":
		pairs = append(pairs, [2]string{"window", reqWindowLine(s)})
	case !s.windowFrom.IsZero():
		span := ""
		if d := s.windowTo.Sub(s.windowFrom); d > 0 {
			span = fmt.Sprintf(" (%s)", dur(d))
		}
		pairs = append(pairs, [2]string{"window", fmt.Sprintf("%s → %s%s", stamp(s.windowFrom), stamp(s.windowTo), span)})
	}
	if s.scope != "" {
		pairs = append(pairs, [2]string{"filtered", s.scope})
	}
	if len(s.repos) > 1 {
		pairs = append(pairs, [2]string{"repos", strings.Join(s.repos, ", ")})
	}
	return pairs
}

// windowElapsed is the clamp both reqWindowLine (text) and
// statsDocWindowFrom (-json) apply to how far now falls into a resolved
// window — never negative (a window that hasn't started), never past the
// end (one already over) — computed once so the two renderers cannot drift
// apart on the rule.
func windowElapsed(from, to, now time.Time) (elapsed, total time.Duration) {
	total = to.Sub(from)
	elapsed = min(max(now.Sub(from), 0), total)
	return elapsed, total
}

// reqWindowLine renders the resolved -window bounds and how far through the
// window now falls: the progress a fixed -since has no equivalent of, since
// a calendar window has a known end and -since does not.
func reqWindowLine(s sourceSummary) string {
	elapsed, total := windowElapsed(s.reqFrom, s.reqTo, s.reqNow)
	remaining := total - elapsed
	label := s.reqWindow
	if s.reqAnchor != "" {
		label += " — anchor: " + s.reqAnchor
	}
	return fmt.Sprintf("%s → %s (%s; %s elapsed, %s left, %s through)",
		stamp(s.reqFrom), stamp(s.reqTo), label, dur(elapsed), dur(remaining),
		percent(int(elapsed.Seconds()), int(total.Seconds())))
}

// window is the span the records actually cover, which is what a rate has to
// be divided by — never the wall clock, so the same file reports the same
// numbers tomorrow.
func window(ds dataset) (from, to time.Time) {
	note := func(t time.Time) {
		if t.IsZero() {
			return
		}
		if from.IsZero() || t.Before(from) {
			from = t
		}
		if t.After(to) {
			to = t
		}
	}
	for _, r := range ds.runs {
		note(recTime(r.TS))
		note(endOf(r))
	}
	for _, r := range ds.issues {
		note(recTime(r.TS))
	}
	return from, to
}

func repoNames(ds dataset) []string {
	var names []string
	for _, r := range ds.runs {
		if r.Repo != "" && !slices.Contains(names, r.Repo) {
			names = append(names, r.Repo)
		}
	}
	for _, r := range ds.issues {
		if r.Repo != "" && !slices.Contains(names, r.Repo) {
			names = append(names, r.Repo)
		}
	}
	slices.Sort(names)
	return names
}

func issuePairs(s issuesSummary, plan planCostSummary) [][2]string {
	if s.done == 0 {
		return [][2]string{{"terminal", "none yet — every issue in this window is still in flight"},
			{"in flight", strconv.Itoa(s.inFlight)}}
	}
	// The merge rate leads, with its percentage attached: it is the headline
	// number, and "merged 3, needs human 1" buries it in a list.
	merged := s.terminal[issueMerged]
	terminal := fmt.Sprintf("%d — merged %d (%s)", s.done, merged, percent(merged, s.done))
	rest := maps.Clone(s.terminal)
	delete(rest, issueMerged)
	if other := breakdown(rest, []string{issueClosedNoChange, issueClosed, issueNeedsHuman}); other != "none" {
		terminal += ", " + other
	}

	pairs := [][2]string{{"terminal", terminal}}
	// What "needs human" above is made of — the most actionable ranking in the
	// report, because it says which half of the tool the next change belongs
	// in. Absent when nothing was parked, rather than a line of zeroes.
	if len(s.parkReasons) > 0 {
		pairs = append(pairs, [2]string{"park reasons", breakdown(s.parkReasons, parkReasonOrder)})
	}
	pairs = append(pairs, [2]string{"in flight", strconv.Itoa(s.inFlight)})

	change := changePairsFrom(s.change)
	planPairs := planCostPairs(plan)
	if s.priced == 0 {
		pairs = append(pairs, [2]string{"per issue",
			"nothing to price — no terminal issue has runs in this window"})
		return append(append(pairs, change...), planPairs...)
	}

	// Say what the averages are over whenever that is not every terminal
	// issue, so a shrunken denominator can never pass for a cheap batch.
	over := ""
	if s.priced != s.done {
		over = fmt.Sprintf(" (over the %d with runs in this window)", s.priced)
	}
	pairs = append(pairs,
		// Mean and median both, because one pathological issue drags a mean
		// somewhere no real issue has ever been.
		[2]string{"runs per issue", fmt.Sprintf("%s mean, %s median%s",
			trimZero(s.runsMean), trimZero(s.runsMedian), over)},
		[2]string{"cost per issue", fmt.Sprintf("%s mean, %s median%s",
			usd(s.costMean), usd(s.costMedian), over)},
		[2]string{"tokens per issue", fmt.Sprintf("%s mean, %s median (%s)",
			count(int64(s.tokensMean)), count(s.tokensMedian), split(s.tokensSplitSum, s.tokensSplitN))},
	)
	return append(append(pairs, change...), planPairs...)
}

// changePairsFrom formats what the work actually changed, from a
// *changeSummary built over the terminal records carrying the GitHub
// enrichment — every issue that ended with a PR, abandoned ones included,
// which is the same set the lines above it count. nil means no terminal
// issue carries that enrichment, and there is nothing to print.
func changePairsFrom(c *changeSummary) [][2]string {
	if c == nil {
		return nil
	}
	line := fmt.Sprintf("+%d -%d across %s", c.addsMedian, c.delsMedian, plural(c.filesMedian, "file"))
	if c.hasReviews {
		line += ", " + plural(c.reviewsMedian, "review")
	}
	return [][2]string{{"change per issue",
		fmt.Sprintf("%s (medians over %s with PR data)", line, plural(c.n, "issue"))}}
}

// planCostSummary is what a terminal issue cost the plan: the delta between
// its two week-usage samples (#139's usage gate), as a percentage of a
// week. It is stated as the upper bound it is — the delta counts everything
// the account did during that span, including the operator's own
// interactive session on another machine — which is why crossCheckPercent,
// the probe's own attribution figure over a different, self-reported
// window, is carried beside it and never folded into it.
type planCostSummary struct {
	n            int // issues with a usable (non-negative) sample
	unsampled    int // terminal issues with no sample, or one a mid-issue reset invalidated
	mean, median float64

	hasCrossCheck     bool
	crossCheckPercent int
	crossCheckWindow  string
}

// buildPlanCostSummary reads the delta straight off each terminal record —
// nothing here is summed across runs, unlike every other per-issue figure,
// because the two samples are already cumulative account state rather than
// something this issue alone produced.
func buildPlanCostSummary(issues []*issueStats, probe *usageSnapshot) planCostSummary {
	var s planCostSummary
	var deltas []float64
	for _, is := range issues {
		if is.terminal == nil || !is.terminal.hasUsageSamples() {
			if is.terminal != nil {
				s.unsampled++
			}
			continue
		}
		delta := float64(is.terminal.WeekUsageAtTerminal - is.terminal.WeekUsageAtPickup)
		if delta < 0 {
			// A reset landed mid-issue: the terminal reading belongs to a
			// fresh cycle, not a continuation of the pickup one, so
			// subtracting them means nothing — counted with the unsampled
			// issues rather than as a false near-zero.
			s.unsampled++
			continue
		}
		deltas = append(deltas, delta)
	}
	s.n = len(deltas)
	if s.n > 0 {
		s.mean, s.median = mean(deltas), median(deltas)
	}
	if probe != nil && probe.hasAttribution && probe.attribution.hasPluginPercent {
		s.hasCrossCheck = true
		s.crossCheckPercent = probe.attribution.pluginPercent
		s.crossCheckWindow = probe.attribution.windowLabel
	}
	return s
}

// issuesHaveUsageSamples reports whether probing for the plan-cost
// cross-check is worth the call at all — no point asking the CLI for its own
// attribution figure to sit beside a line the report is not going to print.
func issuesHaveUsageSamples(issues []*issueStats) bool {
	for _, is := range issues {
		if is.terminal != nil && is.terminal.hasUsageSamples() {
			return true
		}
	}
	return false
}

// pct1 renders a percentage to one decimal place, trailing zero trimmed —
// trimZero's own rule, reused here rather than duplicated.
func pct1(f float64) string { return trimZero(f) + "%" }

// planCostPairs is the "plan cost per issue" line — absent entirely when no
// terminal issue in scope has a usable sample, exactly the treatment
// changePairsFrom already gives an empty "change per issue" (nil, not a line
// of zeroes standing in for "never measured"). When some but not all do, it
// names how many were left out, the same "(medians over N issues with PR
// data)" convention changePairsFrom uses for the same situation.
func planCostPairs(p planCostSummary) [][2]string {
	if p.n == 0 {
		return nil
	}
	line := fmt.Sprintf("%s mean, %s median of a week", pct1(p.mean), pct1(p.median))
	if p.median > 0 {
		line += fmt.Sprintf(" — about %d issues to a full week", int(math.Round(100/p.median)))
	}
	note := "upper bound: counts everything the account did meanwhile, not just this issue"
	if p.hasCrossCheck {
		note += fmt.Sprintf("; polako's own share was %d%% of the last %s", p.crossCheckPercent, crossCheckWindowLabel(p))
	}
	line += " (" + note + ")"
	if p.unsampled > 0 {
		line += fmt.Sprintf(" (%s of %s had no usable reading)", plural(p.unsampled, "issue"), plural(p.n+p.unsampled, "issue"))
	}
	return [][2]string{{"plan cost per issue", line}}
}

// crossCheckWindowLabel names the probe attribution's own window ("24h"),
// falling back to a description when the CLI reported none — one fallback
// string, shared by the text line above and the HTML card (statshtml.go),
// rather than two independently-worded copies.
func crossCheckWindowLabel(p planCostSummary) string {
	if p.crossCheckWindow != "" {
		return p.crossCheckWindow
	}
	return "reported window"
}

func runPairs(s runsSummary) [][2]string {
	pairs := [][2]string{
		{"total", fmt.Sprintf("%d — %s", s.total,
			breakdown(s.statuses, []string{"ok", "error", "no-turns", "crash", "stalled",
				"interrupted", "no-skill", "auth", "limit", "budget"}))},
		{"reasons", breakdown(s.reasons, []string{reasonImplement, reasonResume, reasonUnfinished,
			reasonAnswers, reasonRemediate, reasonChecks, reasonReview})},
		{"outcomes", breakdown(s.outcomes, []string{outcomeOpenedPR, outcomeClosedIssue, outcomeQuestions, outcomeNothing, outcomeUnknown})},
		{"work", fmt.Sprintf("%s, %s", plural(s.turns, "turn"), plural(s.tools, "tool use"))},
	}
	if s.approximated > 0 {
		// Crash, stall and interrupt never emit a result event. Their numbers
		// are the tally seen streaming past, and undercount by construction.
		pairs = append(pairs, [2]string{"approximated",
			fmt.Sprintf("%d of %d runs priced from the streamed tally, not a result event",
				s.approximated, s.total)})
	}
	return pairs
}

func costPairs(s costSummary) [][2]string {
	spend := usd(s.totalUSD)
	if days := s.windowTo.Sub(s.windowFrom).Hours() / 24; days >= 1.0/24 {
		spend += fmt.Sprintf(" over %s (%s/day)", dur(s.windowTo.Sub(s.windowFrom)), usd(s.totalUSD/days))
	}
	pairs := [][2]string{{"total", spend}}
	if s.merged > 0 {
		// Everything spent in the window over what shipped from it — failed
		// runs included, because they are part of what a merged PR costs.
		pairs = append(pairs, [2]string{"per merged PR",
			fmt.Sprintf("%s across %s", usd(s.totalUSD/float64(s.merged)), plural(s.merged, "merge"))})
	}
	pairs = append(pairs, [2]string{"tokens", fmt.Sprintf("%s (%s)", count(s.tokens.total()), split(s.tokens, 1))})
	return pairs
}

func latencyPairs(s latencySummary) [][2]string {
	return [][2]string{
		{"blocked on answers", spanSummary(s.blocked)},
		{"pr open to merge", spanSummary(s.toMerge) + confounded(s.toMerge)},
	}
}

func spanSummary(spans []time.Duration) string {
	s := summarizeSpans(spans)
	if s.count == 0 {
		return "no spans in this window"
	}
	return fmt.Sprintf("%s — %s median, %s max", plural(s.count, "span"), dur(s.median), dur(s.max))
}

// spanStats is a span list's count, median and max, computed once so
// spanSummary (text) and statsDocSpansFrom (statsjson.go) format the same
// numbers rather than each reducing the slice itself.
type spanStats struct {
	count  int
	median time.Duration
	max    time.Duration
}

func summarizeSpans(spans []time.Duration) spanStats {
	if len(spans) == 0 {
		return spanStats{}
	}
	return spanStats{count: len(spans), median: median(spans), max: slices.Max(spans)}
}

func confounded(spans []time.Duration) string {
	if len(spans) == 0 {
		return ""
	}
	return " (human availability, not the tool)"
}

// --- tables ---

// issueHeader and issueRows are split out from the printing so the HTML report
// can lay out the very same cells. Two renderers deriving the same table twice
// is two chances to disagree about what a column means.
var issueHeader = []string{"issue", "outcome", "runs", "questions", "cost", "tokens", "wall"}

// issueLeft is how many of issueHeader's columns are names rather than numbers.
const issueLeft = 2

func issueRows(issues []*issueStats) [][]string {
	rows := make([][]string, 0, len(issues))
	for _, is := range issues {
		rows = append(rows, []string{
			fmt.Sprintf("%s#%d", is.key.repo, is.key.issue),
			label(is.outcome()),
			strconv.Itoa(len(is.runs)),
			strconv.Itoa(is.questions()),
			usd(is.cost),
			count(is.tokens.total()),
			dur(time.Duration(is.wallMS) * time.Millisecond),
		})
	}
	return rows
}

func printIssueTable(w io.Writer, rpt report, issues []*issueStats) {
	printTable(w, rpt, "by issue", issueHeader, issueRows(issues), issueLeft)
}

// noValue fills a table cell a record has nothing for — most often the session
// id, which older records carry none of and neither does a run that died before
// the CLI announced itself. A blank cell in a column of ids reads as a
// rendering bug rather than as a run there is nothing to reopen.
const noValue = "—"

// started renders a record's timestamp in the same self-describing UTC form
// the window line uses. A value that will not parse is shown as it was written
// rather than as the zero time, which would date a real run to year 1.
func started(ts string) string {
	if t := recTime(ts); !t.IsZero() {
		return stamp(t)
	}
	if ts == "" {
		return noValue
	}
	return ts
}

// printRunTable lists the runs themselves, newest last. Everything else in the
// report is a rollup; this is the ledger they were computed from, and its last
// text column is the handle that turns a row back into the whole transcript:
// `claude --resume <session>`.
//
// ds.runs arrives filtered by -repo, -since and -shift and already in
// timestamp order, so the table composes with every filter without asking.
//
// The title is "run log" rather than "runs" because the summary section above
// already owns that word, and a report with two sections of one name is one
// nobody can talk about.
// The six text columns lead so that the numbers all right-align together — and
// so that a session of "—" sits at the left of its column rather than a UUID's
// width away from the row it belongs to.
var runHeader = []string{"started", "issue", "reason", "status", "outcome", "session",
	"attempt", "cost", "tokens", "wall"}

const runLeft = 6

func runRows(ds dataset) [][]string {
	rows := make([][]string, 0, len(ds.runs))
	for _, r := range ds.runs {
		session := r.Session
		if session == "" {
			session = noValue
		}
		rows = append(rows, []string{
			started(r.TS),
			fmt.Sprintf("%s#%d", r.Repo, r.Issue),
			label(r.Reason),
			r.Status,
			label(r.Outcome),
			session,
			strconv.Itoa(r.Attempt),
			usd(r.CostUSD),
			count(r.Tokens.total()),
			dur(time.Duration(r.WallMS) * time.Millisecond),
		})
	}
	return rows
}

func printRunTable(w io.Writer, rpt report, ds dataset) {
	if len(ds.runs) == 0 {
		// Reached when a window kept issue records but clipped every run away.
		fmt.Fprintf(w, "\n%s\n  (no runs in this window)\n", rpt.bold("run log"))
		return
	}
	printTable(w, rpt, "run log", runHeader, runRows(ds), runLeft)
}

func printGroupTable(w io.Writer, rpt report, ds dataset, issues []*issueStats, by string) {
	rows, spanning := groupRows(ds, issues, by)
	printTable(w, rpt, "by "+by, groupHeader(by), rows, groupLeft)
	if note := spanningNote(spanning, by); note != "" {
		fmt.Fprintf(w, "  %s\n", note)
	}
}

// groupHeader names the first column after whatever is being grouped by, so
// the same builder serves -by model, -by tag and -by shift.
func groupHeader(by string) []string {
	return []string{by, "issues", "merged", "runs", "cost", "$/merged", "tokens"}
}

const groupLeft = 1

// spanningNote reports how many issues fell into more than one group — not how
// many surplus memberships they add up to, which is a different and larger
// number whenever one spans three.
func spanningNote(spanning int, by string) string {
	if spanning == 0 {
		return ""
	}
	return fmt.Sprintf("(%s more than one %s, and is counted under each)",
		plural(spanning, "issue")+" spans", by)
}

// groupRows breaks the numbers down by the configuration under test, or by the
// drain that did the work. An issue whose runs span two models or two tags
// counts under each — the point of the breakdown is comparing batches, and a
// batch is normally one of both, so the footnote appears only when that
// assumption does not hold. By drain it holds far less often: an issue picked
// up by one drain and finished by the next is the ordinary shape of a restart.
//
// merged is the issue's own final outcome, so every group that worked it
// counts the merge — the same rule for a drain as for a tag, and what makes
// $/merged "spent by this group per issue of theirs that shipped". It is why a
// -by drain row can read merged 1 for a drain whose own terminal record parked
// the issue, while -drain <id> on the same drain reads needs human 1: the
// filter narrows the records to that drain's, so the report is that drain's
// verdict rather than the issue's fate. Two questions, two right answers.
func groupRows(ds dataset, issues []*issueStats, by string) (rows [][]string, spanning int) {
	groups, order := groupTotals(ds, by)
	merged := mergedIssues(issues)
	rows = make([][]string, 0, len(order))
	for _, name := range order {
		g := groups[name]
		wins := 0
		for key := range g.issues {
			if merged[key] {
				wins++
			}
		}
		perMerge := noValue
		if wins > 0 {
			perMerge = usd(g.cost / float64(wins))
		}
		rows = append(rows, []string{
			g.name, strconv.Itoa(len(g.issues)), strconv.Itoa(wins),
			strconv.Itoa(g.runs), usd(g.cost), perMerge, count(g.tokens),
		})
	}
	return rows, spanningCount(groups, order)
}

// statGroup is one -by bucket's tally — runs, cost, tokens, and which issues
// touched it. groupTotals is the one place it is computed, so groupRows
// (text) and statsDocByFrom (statsjson.go) format the same numbers rather
// than each summing the run records itself.
type statGroup struct {
	name   string
	runs   int
	cost   float64
	tokens int64
	issues map[issueKey]bool
}

// groupTotals breaks the numbers down by the configuration under test, or by
// the drain that did the work. An issue whose runs span two models or two
// tags counts under each — the point of the breakdown is comparing batches,
// and a batch is normally one of both, so spanningCount's footnote appears
// only when that assumption does not hold. By drain it holds far less often:
// an issue picked up by one drain and finished by the next is the ordinary
// shape of a restart.
func groupTotals(ds dataset, by string) (groups map[string]*statGroup, order []string) {
	groups = map[string]*statGroup{}
	for _, r := range ds.runs {
		var name string
		switch by {
		case byModel:
			name = r.Model
		case byShift:
			name = r.Shift
		default:
			name = r.Tag
		}
		if name == "" {
			name = noneGroup
		}
		g, ok := groups[name]
		if !ok {
			g = &statGroup{name: name, issues: map[issueKey]bool{}}
			groups[name], order = g, append(order, name)
		}
		g.runs++
		g.cost += r.CostUSD
		g.tokens += r.Tokens.total()
		g.issues[issueKey{r.Repo, r.Issue}] = true
	}
	sort.SliceStable(order, func(i, j int) bool {
		a, b := groups[order[i]], groups[order[j]]
		if a.cost != b.cost {
			return a.cost > b.cost // most expensive first: that is the one worth explaining
		}
		return a.name < b.name
	})
	return groups, order
}

// mergedIssues is the issue's own final outcome, so every group that worked
// it counts the merge — the same rule for a drain as for a tag, and what
// makes $/merged "spent by this group per issue of theirs that shipped". It
// is why a -by drain row can read merged 1 for a drain whose own terminal
// record parked the issue, while -drain <id> on the same drain reads needs
// human 1: the filter narrows the records to that drain's, so the report is
// that drain's verdict rather than the issue's fate. Two questions, two
// right answers.
func mergedIssues(issues []*issueStats) map[issueKey]bool {
	merged := map[issueKey]bool{}
	for _, is := range issues {
		if is.terminal != nil && is.terminal.Outcome == issueMerged {
			merged[is.key] = true
		}
	}
	return merged
}

// spanningCount is how many issues fell into more than one group — not how
// many surplus memberships they add up to, which is a different and larger
// number whenever one spans three.
func spanningCount(groups map[string]*statGroup, order []string) int {
	memberships := map[issueKey]int{}
	for _, name := range order {
		for key := range groups[name].issues {
			memberships[key]++
		}
	}
	spanning := 0
	for _, n := range memberships {
		if n > 1 {
			spanning++
		}
	}
	return spanning
}

// --- plain aligned text ---

func printPairs(w io.Writer, rpt report, title string, pairs [][2]string) {
	if len(pairs) == 0 {
		return
	}
	if title != "" {
		fmt.Fprintf(w, "\n%s\n", title)
	}
	width := pairWidth(pairs)
	for _, p := range pairs {
		// Padded first, styled after: wrapping ANSI codes around the label
		// before %-*s pads it would make the escape bytes count toward the
		// width and misalign every row behind it.
		label := rpt.cell(fmt.Sprintf("%-*s", width, p[0]))
		fmt.Fprintf(w, "  %s  %s\n", label, rpt.cell(p[1]))
	}
}

// pairWidth is the label width printPairs aligns its rows to. Shared with
// preflight's startup settings block (main.go), so both column up the same
// way even though the block reaches the terminal through narrate rather than
// through this file's io.Writer-based rendering.
func pairWidth(pairs [][2]string) int {
	width := 0
	for _, p := range pairs {
		width = max(width, len(p[0]))
	}
	return width
}

// printTable aligns a header and its rows: the leading left columns name
// things, everything after them is a number, and a column of dollars only
// reads as a column when it is right-aligned.
func printTable(w io.Writer, rpt report, title string, header []string, rows [][]string, left int) {
	fmt.Fprintf(w, "\n%s\n", rpt.bold(title))
	if len(rows) == 0 {
		fmt.Fprintln(w, "  (no runs in this window to break down)")
		return
	}
	widths := make([]int, len(header))
	for i, h := range header {
		widths[i] = len(h)
	}
	for _, row := range rows {
		for i, cell := range row {
			widths[i] = max(widths[i], len(cell))
		}
	}
	line := func(cells []string, style func(string) string) {
		var b strings.Builder
		for i, cell := range cells {
			b.WriteString("  ")
			var padded string
			if i < left {
				padded = fmt.Sprintf("%-*s", widths[i], cell)
			} else {
				padded = fmt.Sprintf("%*s", widths[i], cell)
			}
			if i == len(cells)-1 {
				// Trimmed before styling, not after: a styled last cell ends
				// in a reset code, not a space, so trimming the assembled
				// line after the fact would miss padding a style wrapped.
				padded = strings.TrimRight(padded, " ")
			}
			b.WriteString(style(padded))
		}
		fmt.Fprintln(w, b.String())
	}
	line(header, rpt.dim)
	for _, row := range rows {
		line(row, rpt.cell)
	}
}

// breakdown renders "merged 6, needs human 2" in a fixed order, so the same
// data prints the same way twice. Keys the caller did not list follow, sorted.
func breakdown(counts map[string]int, order []string) string {
	var parts []string
	for _, k := range order {
		if n := counts[k]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", label(k), n))
		}
	}
	var rest []string
	for k := range counts {
		if k != "" && !slices.Contains(order, k) {
			rest = append(rest, k)
		}
	}
	slices.Sort(rest)
	for _, k := range rest {
		parts = append(parts, fmt.Sprintf("%s %d", label(k), counts[k]))
	}
	if n := counts[""]; n > 0 {
		parts = append(parts, fmt.Sprintf("unrecorded %d", n))
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}

// label renders a record's snake_case value as prose.
func label(s string) string { return strings.ReplaceAll(s, "_", " ") }

func percent(n, total int) string {
	if total == 0 {
		return "0%"
	}
	return fmt.Sprintf("%.0f%%", 100*float64(n)/float64(total))
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

// split renders a token block's four ways, divided by n — the same helper for
// a total (n = 1) and a per-issue mean. divideTokens is the division itself,
// its own function so statsDocIssuesFrom (statsjson.go) can read the same
// per-issue split as numbers rather than reducing tokensSplitSum a second
// time.
func split(t tokenCounts, n int64) string {
	d := divideTokens(t, n)
	return fmt.Sprintf("in %s, out %s, cache read %s, cache write %s",
		count(d.In), count(d.Out), count(d.CacheRead), count(d.CacheWrite))
}

func divideTokens(t tokenCounts, n int64) tokenCounts {
	if n < 1 {
		n = 1
	}
	return tokenCounts{In: t.In / n, Out: t.Out / n, CacheRead: t.CacheRead / n, CacheWrite: t.CacheWrite / n}
}

// count renders a magnitude at a glance: 8.1M reads, 8123400 does not.
func count(n int64) string {
	abs := n
	if abs < 0 {
		abs = -abs
	}
	switch {
	case abs >= 1_000_000_000:
		return trimZero(float64(n)/1e9) + "G"
	case abs >= 1_000_000:
		return trimZero(float64(n)/1e6) + "M"
	case abs >= 1_000:
		return trimZero(float64(n)/1e3) + "k"
	}
	return strconv.FormatInt(n, 10)
}

func trimZero(f float64) string {
	return strings.TrimSuffix(fmt.Sprintf("%.1f", f), ".0")
}

func usd(f float64) string { return fmt.Sprintf("$%.2f", f) }

// dur renders a duration the way a person reads one: no "0s" tail, and days
// once hours stop being a useful unit.
func dur(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	if d >= 48*time.Hour {
		return trimZero(d.Hours()/24) + "d"
	}
	d = d.Round(time.Second)
	h := int(d / time.Hour)
	m := int(d/time.Minute) % 60
	s := int(d/time.Second) % 60
	switch {
	case h > 0 && m > 0:
		return fmt.Sprintf("%dh%dm", h, m)
	case h > 0:
		return fmt.Sprintf("%dh", h)
	case m > 0 && s > 0:
		return fmt.Sprintf("%dm%ds", m, s)
	case m > 0:
		return fmt.Sprintf("%dm", m)
	}
	return fmt.Sprintf("%ds", s)
}

// number is what the mean/median helpers work on: counts, dollars and
// durations (whose underlying type is int64) all qualify.
type number interface {
	~int | ~int64 | ~float64
}

func mean[T number](v []T) float64 {
	if len(v) == 0 {
		return 0
	}
	var sum float64
	for _, x := range v {
		sum += float64(x)
	}
	return sum / float64(len(v))
}

func median[T number](v []T) T {
	if len(v) == 0 {
		var zero T
		return zero
	}
	s := slices.Clone(v)
	slices.Sort(s)
	if n := len(s); n%2 == 1 {
		return s[n/2]
	}
	return (s[len(s)/2-1] + s[len(s)/2]) / 2
}
