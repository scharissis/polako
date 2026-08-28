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
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
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
	repo  string
	since time.Duration
	by    string
	shift string
	runs  bool
	html  string
}

// runStats is the `stats` subcommand: parse its own flags, read the records,
// print one report. now is passed in so -since is testable; rpt is the
// styler stats/status share, TTY-detected on stdout at the dispatch in main.
func runStats(args []string, out io.Writer, now time.Time, rpt report) error {
	fs := flag.NewFlagSet("stats", flag.ContinueOnError)
	fs.SetOutput(out)
	var opt statsOptions
	var metrics string
	fs.StringVar(&metrics, "metrics", "",
		"directory holding the run-data records (default ~/.polako/metrics)")
	fs.StringVar(&opt.repo, "repo", "", "only count records for this repository (owner/name)")
	fs.DurationVar(&opt.since, "since", 0, "only count records newer than this ago (e.g. 168h)")
	fs.StringVar(&opt.shift, "shift", "",
		`only count records from this shift's id, or "`+shiftLast+`" for the newest shift in scope`)
	fs.StringVar(&opt.by, "by", "", "also break the numbers down by "+orList(byGroups))
	// No backquotes in this text: the flag package reads the first backquoted
	// word as the argument's name, and a bool flag has no argument to name.
	fs.BoolVar(&opt.runs, "runs", false,
		"also list the individual runs, with the session id that reopens each one")
	fs.StringVar(&opt.html, "html", "",
		"also write the report to this `path`, as one self-contained HTML file")
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
	if opt.by != "" && !slices.Contains(byGroups, opt.by) {
		return fmt.Errorf("-by %q: choose one of %s", opt.by, orList(byGroups))
	}
	opt.shift = strings.TrimSpace(opt.shift)
	opt.html = strings.TrimSpace(opt.html)

	dir, err := statsDir(metrics)
	if err != nil {
		return err
	}
	ds, err := loadRecords(dir, opt, now)
	if err != nil {
		return err
	}
	issues := rollUpIssues(ds)
	render(out, rpt, ds, issues, opt)
	if opt.html == "" {
		return nil
	}
	// A second view of the report just printed, never a replacement for it: an
	// operator who asked for a file still wants to see what went into it, and a
	// command that printed nothing would look like one that did nothing.
	if err := writeHTMLReport(opt.html, ds, issues, opt, now); err != nil {
		return err
	}
	fmt.Fprintf(out, "\nwrote the HTML report to %s\n", opt.html)
	return nil
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

// --- rendering ---

func render(w io.Writer, rpt report, ds dataset, issues []*issueStats, opt statsOptions) {
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
	printPairs(w, rpt, "", sourcePairs(ds, opt))
	printPairs(w, rpt, "issues", issuePairs(issues))
	printPairs(w, rpt, "runs", runPairs(ds))
	printPairs(w, rpt, "cost", costPairs(ds, issues))
	printPairs(w, rpt, "human latency", latencyPairs(issues))

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
	if opt.since > 0 {
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

func sourcePairs(ds dataset, opt statsOptions) [][2]string {
	files := fmt.Sprintf("%s, %s", plural(ds.files, "file"), plural(len(ds.runs)+len(ds.issues), "record"))
	if ds.skipped > 0 {
		files += fmt.Sprintf(" (%s skipped)", plural(ds.skipped, "unreadable line"))
	}
	pairs := [][2]string{{"read", files}}
	if len(ds.unread) > 0 {
		pairs = append(pairs, [2]string{"could not open",
			fmt.Sprintf("%s — %s", plural(len(ds.unread), "file"), strings.Join(ds.unread, ", "))})
	}
	from, to := window(ds)
	if !from.IsZero() {
		span := ""
		if d := to.Sub(from); d > 0 {
			span = fmt.Sprintf(" (%s)", dur(d))
		}
		pairs = append(pairs, [2]string{"window", fmt.Sprintf("%s → %s%s", stamp(from), stamp(to), span)})
	}
	if scope := strings.TrimSpace(scopeSuffix(opt, ds)); scope != "" {
		pairs = append(pairs, [2]string{"filtered", scope})
	}
	if repos := repoNames(ds); len(repos) > 1 {
		pairs = append(pairs, [2]string{"repos", strings.Join(repos, ", ")})
	}
	return pairs
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

func issuePairs(issues []*issueStats) [][2]string {
	var done []*issueStats
	inFlight := 0
	counts := map[string]int{}
	for _, is := range issues {
		if is.terminal == nil {
			inFlight++
			continue
		}
		done = append(done, is)
		counts[is.terminal.Outcome]++
	}
	if len(done) == 0 {
		return [][2]string{{"terminal", "none yet — every issue in this window is still in flight"},
			{"in flight", strconv.Itoa(inFlight)}}
	}
	// The merge rate leads, with its percentage attached: it is the headline
	// number, and "merged 3, needs human 1" buries it in a list.
	merged := counts[issueMerged]
	terminal := fmt.Sprintf("%d — merged %d (%s)", len(done), merged, percent(merged, len(done)))
	rest := maps.Clone(counts)
	delete(rest, issueMerged)
	if other := breakdown(rest, []string{issueClosed, issueNeedsHuman}); other != "none" {
		terminal += ", " + other
	}

	pairs := [][2]string{{"terminal", terminal}}
	// What "needs human" above is made of — the most actionable ranking in the
	// report, because it says which half of the tool the next change belongs
	// in. Absent when nothing was parked, rather than a line of zeroes.
	if why := parkReasons(done); why != "" {
		pairs = append(pairs, [2]string{"park reasons", why})
	}
	pairs = append(pairs, [2]string{"in flight", strconv.Itoa(inFlight)})

	// Only issues whose runs are in scope can be priced. An issue that merged
	// inside a -since window after running for two days outside it has a
	// terminal record here and no runs, and averaging it in as $0 would drag
	// every per-issue number toward zero — the one place a filter could
	// quietly turn expensive work into cheap-looking work.
	var priced []*issueStats
	for _, is := range done {
		if len(is.runs) > 0 {
			priced = append(priced, is)
		}
	}
	change := changePairs(done)
	if len(priced) == 0 {
		pairs = append(pairs, [2]string{"per issue",
			"nothing to price — no terminal issue has runs in this window"})
		return append(pairs, change...)
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
	n := int64(len(priced))
	// Say what the averages are over whenever that is not every terminal
	// issue, so a shrunken denominator can never pass for a cheap batch.
	over := ""
	if len(priced) != len(done) {
		over = fmt.Sprintf(" (over the %d with runs in this window)", len(priced))
	}
	pairs = append(pairs,
		// Mean and median both, because one pathological issue drags a mean
		// somewhere no real issue has ever been.
		[2]string{"runs per issue", fmt.Sprintf("%s mean, %s median%s",
			trimZero(mean(runs)), trimZero(median(runs)), over)},
		[2]string{"cost per issue", fmt.Sprintf("%s mean, %s median%s",
			usd(mean(costs)), usd(median(costs)), over)},
		[2]string{"tokens per issue", fmt.Sprintf("%s mean, %s median (%s)",
			count(int64(mean(tokens))), count(median(tokens)), split(sum, n))},
	)
	return append(pairs, change...)
}

// parkReasons breaks the hand-backs down by why they happened, or "" when
// there were none. A record written before the field existed counts as
// unrecorded — never as unknown, which is the field's own value for a park
// path that could not say, and folding the two together would make an old file
// look like a supervisor that had stopped classifying its parks.
func parkReasons(done []*issueStats) string {
	counts := map[string]int{}
	for _, is := range done {
		if is.terminal.Outcome == issueNeedsHuman {
			counts[is.terminal.ParkReason]++
		}
	}
	if len(counts) == 0 {
		return ""
	}
	return breakdown(counts, parkReasonOrder)
}

// changePairs summarizes what the work actually changed, from the GitHub
// enrichment folded into terminal records — every issue that ended with a PR,
// abandoned ones included, which is the same set the lines above it count.
// Medians, over their own
// denominator: a record written before the enrichment existed carries none,
// and neither does one whose lookup failed, so this line covers a different
// set of issues from the ones above it and says which.
func changePairs(done []*issueStats) [][2]string {
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
	line := fmt.Sprintf("+%d -%d across %s", median(adds), median(dels), plural(median(files), "file"))
	// A repository whose PRs are merged without a formal review reports zero
	// every time, and a column of zeros is noise rather than a finding.
	if slices.Max(reviews) > 0 {
		line += ", " + plural(median(reviews), "review")
	}
	return [][2]string{{"change per issue",
		fmt.Sprintf("%s (medians over %s with PR data)", line, plural(len(adds), "issue"))}}
}

func runPairs(ds dataset) [][2]string {
	statuses, reasons, outcomes := map[string]int{}, map[string]int{}, map[string]int{}
	approximate, turns, tools := 0, 0, 0
	for _, r := range ds.runs {
		statuses[r.Status]++
		reasons[r.Reason]++
		outcomes[r.Outcome]++
		if r.UsageSource == usageObserved {
			approximate++
		}
		turns += max(r.Turns, 0)
		tools += r.ToolUses
	}
	pairs := [][2]string{
		{"total", fmt.Sprintf("%d — %s", len(ds.runs),
			breakdown(statuses, []string{"ok", "error", "no-turns", "crash", "stalled",
				"interrupted", "no-skill", "auth", "limit", "budget"}))},
		{"reasons", breakdown(reasons, []string{reasonImplement, reasonResume, reasonUnfinished,
			reasonAnswers, reasonRemediate, reasonChecks, reasonReview})},
		{"outcomes", breakdown(outcomes, []string{outcomeOpenedPR, outcomeQuestions, outcomeNothing, outcomeUnknown})},
		{"work", fmt.Sprintf("%s, %s", plural(turns, "turn"), plural(tools, "tool use"))},
	}
	if approximate > 0 {
		// Crash, stall and interrupt never emit a result event. Their numbers
		// are the tally seen streaming past, and undercount by construction.
		pairs = append(pairs, [2]string{"approximated",
			fmt.Sprintf("%d of %d runs priced from the streamed tally, not a result event",
				approximate, len(ds.runs))})
	}
	return pairs
}

func costPairs(ds dataset, issues []*issueStats) [][2]string {
	total, tokens := 0.0, tokenCounts{}
	for _, r := range ds.runs {
		total += r.CostUSD
		tokens.addCounts(r.Tokens)
	}
	spend := usd(total)
	from, to := window(ds)
	if days := to.Sub(from).Hours() / 24; days >= 1.0/24 {
		spend += fmt.Sprintf(" over %s (%s/day)", dur(to.Sub(from)), usd(total/days))
	}
	// Only merges with runs in scope belong in this denominator: one whose
	// runs a -since window clipped away contributes nothing to the total
	// above, so counting it here would price the tool below what it costs.
	merged := 0
	for _, is := range issues {
		if is.terminal != nil && is.terminal.Outcome == issueMerged && len(is.runs) > 0 {
			merged++
		}
	}
	pairs := [][2]string{{"total", spend}}
	if merged > 0 {
		// Everything spent in the window over what shipped from it — failed
		// runs included, because they are part of what a merged PR costs.
		pairs = append(pairs, [2]string{"per merged PR",
			fmt.Sprintf("%s across %s", usd(total/float64(merged)), plural(merged, "merge"))})
	}
	pairs = append(pairs, [2]string{"tokens", fmt.Sprintf("%s (%s)", count(tokens.total()), split(tokens, 1))})
	return pairs
}

func latencyPairs(issues []*issueStats) [][2]string {
	var blocked, toMerge []time.Duration
	for _, is := range issues {
		blocked = append(blocked, answerSpans(is)...)
		if d, ok := mergeSpan(is); ok {
			toMerge = append(toMerge, d)
		}
	}
	return [][2]string{
		{"blocked on answers", spanSummary(blocked)},
		{"pr open to merge", spanSummary(toMerge) + confounded(toMerge)},
	}
}

func spanSummary(spans []time.Duration) string {
	if len(spans) == 0 {
		return "no spans in this window"
	}
	return fmt.Sprintf("%s — %s median, %s max",
		plural(len(spans), "span"), dur(median(spans)), dur(slices.Max(spans)))
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
	type group struct {
		name   string
		runs   int
		cost   float64
		tokens int64
		issues map[issueKey]bool
	}
	order := []string{}
	groups := map[string]*group{}
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
			g = &group{name: name, issues: map[issueKey]bool{}}
			groups[name], order = g, append(order, name)
		}
		g.runs++
		g.cost += r.CostUSD
		g.tokens += r.Tokens.total()
		g.issues[issueKey{r.Repo, r.Issue}] = true
	}
	merged := map[issueKey]bool{}
	for _, is := range issues {
		if is.terminal != nil && is.terminal.Outcome == issueMerged {
			merged[is.key] = true
		}
	}
	sort.SliceStable(order, func(i, j int) bool {
		a, b := groups[order[i]], groups[order[j]]
		if a.cost != b.cost {
			return a.cost > b.cost // most expensive first: that is the one worth explaining
		}
		return a.name < b.name
	})

	memberships := map[issueKey]int{}
	rows = make([][]string, 0, len(order))
	for _, name := range order {
		g := groups[name]
		wins := 0
		for key := range g.issues {
			if merged[key] {
				wins++
			}
		}
		for key := range g.issues {
			memberships[key]++
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
	for _, n := range memberships {
		if n > 1 {
			spanning++
		}
	}
	return rows, spanning
}

// --- plain aligned text ---

func printPairs(w io.Writer, rpt report, title string, pairs [][2]string) {
	if len(pairs) == 0 {
		return
	}
	if title != "" {
		fmt.Fprintf(w, "\n%s\n", title)
	}
	width := 0
	for _, p := range pairs {
		width = max(width, len(p[0]))
	}
	for _, p := range pairs {
		// Padded first, styled after: wrapping ANSI codes around the label
		// before %-*s pads it would make the escape bytes count toward the
		// width and misalign every row behind it.
		label := rpt.cell(fmt.Sprintf("%-*s", width, p[0]))
		fmt.Fprintf(w, "  %s  %s\n", label, rpt.cell(p[1]))
	}
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
// a total (n = 1) and a per-issue mean.
func split(t tokenCounts, n int64) string {
	if n < 1 {
		n = 1
	}
	return fmt.Sprintf("in %s, out %s, cache read %s, cache write %s",
		count(t.In/n), count(t.Out/n), count(t.CacheRead/n), count(t.CacheWrite/n))
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
