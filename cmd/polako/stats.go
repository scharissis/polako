package main

// `polako stats` reads the run data back. This file is its entry point
// (runStats), the -window glue (statsReport), and the read-time rollups every
// renderer formats from; the record reader is statsrecords.go, the text/JSON/
// HTML renderers their own files, and the shared formatters format.go.
//
// `stats` is the only reader the run data has: the drain loop never opens
// these files, so telemetry stays telemetry rather than becoming state the
// supervisor depends on.
//
// Everything summable — cost per issue, runs per issue, question rounds, the
// human-latency spans — is derived here, at read time, from run records. That
// is what keeps the writing side free of rollup state it would have to carry
// across restarts and would get wrong when it didn't.
//
// One rule that had to be measured rather than reasoned out: a resumed run's
// row is summed like any other. A --resume'd result event reports that
// invocation and not the session it continued — issue #78 against real
// records, #258 by a deliberate resume — so the two halves of a resumed
// session do not overlap and nothing takes a per-session maximum. Reports used to carry a
// footnote hedging on that; they no longer need one.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"time"
)

// The -by groupings. Kept short: JSONL is already jq, DuckDB and spreadsheet
// food, so this command answers the questions worth a flag and leaves the long
// tail to those.
const (
	byIssue  = "issue"
	byModel  = "model"
	byTag    = "tag"
	byShift  = "shift"
	byReason = "reason"
)

// byGroups is the whitelist and the order the error message lists them in.
var byGroups = []string{byIssue, byModel, byTag, byShift, byReason}

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

// --- records ---

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
