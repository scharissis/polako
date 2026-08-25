package main

// `backlog-drain stats` reads the run data back. It is the only reader there
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

// The -by groupings. Kept to three: JSONL is already jq, DuckDB and
// spreadsheet food, so this command answers the questions worth a flag and
// leaves the long tail to those.
const (
	byIssue = "issue"
	byModel = "model"
	byTag   = "tag"
)

// errFlagsReported marks a flag error the flag package has already printed,
// together with the usage text that explains it. Saying it a second time on
// the way out helps nobody.
var errFlagsReported = errors.New("invalid flags")

type statsOptions struct {
	repo  string
	since time.Duration
	by    string
}

// runStats is the `stats` subcommand: parse its own flags, read the records,
// print one report. now is passed in so -since is testable.
func runStats(args []string, out io.Writer, now time.Time) error {
	fs := flag.NewFlagSet("stats", flag.ContinueOnError)
	fs.SetOutput(out)
	var opt statsOptions
	var metrics string
	fs.StringVar(&metrics, "metrics", "",
		"directory holding the run-data records (default ~/.backlog-drain/metrics)")
	fs.StringVar(&opt.repo, "repo", "", "only count records for this repository (owner/name)")
	fs.DurationVar(&opt.since, "since", 0, "only count records newer than this ago (e.g. 168h)")
	fs.StringVar(&opt.by, "by", "", "also break the numbers down by issue, model or tag")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "Usage: backlog-drain stats [flags]\n\n"+
			"Reports on the run data recorded by previous drains. Reads only;\n"+
			"nothing here changes what a drain does.\n\nFlags:\n")
		fs.PrintDefaults()
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
	if opt.by != "" && !slices.Contains([]string{byIssue, byModel, byTag}, opt.by) {
		return fmt.Errorf("-by %q: choose one of %s, %s or %s", opt.by, byIssue, byModel, byTag)
	}

	dir, err := statsDir(metrics)
	if err != nil {
		return err
	}
	ds, err := loadRecords(dir, opt, now)
	if err != nil {
		return err
	}
	render(out, ds, opt)
	return nil
}

// statsDir resolves where to read from. "off" is the one value that means
// something to the writer and nothing here, so say that rather than looking
// for a directory called "off".
func statsDir(spec string) (string, error) {
	dir := strings.TrimSpace(spec)
	if strings.EqualFold(dir, metricsOff) {
		return "", fmt.Errorf("-metrics off has nothing to read — point it at a directory, "+
			"or omit it for the default (%s)", "~/.backlog-drain/metrics")
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
	terminal := map[issueKey]issueRecord{}
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
				if !inScope(rec.Repo, rec.TS, opt, cutoff) {
					continue
				}
				// Latest wins: the supervisor can be killed between a merge
				// and its record, so the same issue may reach a terminal
				// state twice. The newest line is the one that happened.
				key := issueKey{rec.Repo, rec.Issue}
				if prev, ok := terminal[key]; !ok || !recTime(rec.TS).Before(recTime(prev.TS)) {
					terminal[key] = rec
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
	ds.issues = make([]issueRecord, 0, len(terminal))
	for _, rec := range terminal {
		ds.issues = append(ds.issues, rec)
	}
	sort.Slice(ds.issues, func(i, j int) bool { return lessIssue(ds.issues[i], ds.issues[j]) })
	sort.SliceStable(ds.runs, func(i, j int) bool {
		return recTime(ds.runs[i].TS).Before(recTime(ds.runs[j].TS))
	})
	return ds, nil
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
		return "in flight"
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

func render(w io.Writer, ds dataset, opt statsOptions) {
	if len(ds.runs) == 0 && len(ds.issues) == 0 {
		fmt.Fprintf(w, "no run data in %s%s\n", ds.dir, scopeSuffix(opt))
		if len(ds.unread) > 0 {
			fmt.Fprintf(w, "%s there could not be opened: %s\n",
				plural(len(ds.unread), "file"), strings.Join(ds.unread, ", "))
			return
		}
		if ds.files == 0 {
			fmt.Fprintln(w, "a drain records there automatically, unless it was run with -metrics off.")
		}
		return
	}
	issues := rollUpIssues(ds)

	fmt.Fprintf(w, "run data from %s\n", ds.dir)
	printPairs(w, "", sourcePairs(ds, opt))
	printPairs(w, "issues", issuePairs(issues))
	printPairs(w, "runs", runPairs(ds))
	printPairs(w, "cost", costPairs(ds, issues))
	printPairs(w, "human latency", latencyPairs(issues))

	switch opt.by {
	case byIssue:
		printIssueTable(w, issues)
	case byModel, byTag:
		printGroupTable(w, ds, issues, opt.by)
	}
	if note := resumeNote(ds); note != "" {
		fmt.Fprintf(w, "\n%s\n", note)
	}
}

func scopeSuffix(opt statsOptions) string {
	var parts []string
	if opt.repo != "" {
		parts = append(parts, "for "+opt.repo)
	}
	if opt.since > 0 {
		parts = append(parts, "in the last "+dur(opt.since))
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
	if scope := strings.TrimSpace(scopeSuffix(opt)); scope != "" {
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

	pairs := [][2]string{{"terminal", terminal}, {"in flight", strconv.Itoa(inFlight)}}

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
			breakdown(statuses, []string{"ok", "error", "no-turns", "crash", "stalled", "interrupted", "no-skill", "auth"}))},
		{"reasons", breakdown(reasons, []string{reasonImplement, reasonResume, reasonAnswers, reasonRemediate})},
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

// resumeNote flags the one number in this report that may be double-counted.
// Whether a --resume'd run's result event reports that invocation's cost or
// the whole session's is unverified against the real CLI; costs are summed as
// reported, which is right in the first case and high in the second.
func resumeNote(ds dataset) string {
	resumed := 0
	for _, r := range ds.runs {
		if r.Reason == reasonResume {
			resumed++
		}
	}
	if resumed == 0 {
		return ""
	}
	return fmt.Sprintf("note: %s resumed an earlier session. Costs are summed exactly as each\n"+
		"      run reported them — see \"resumed sessions\" in the README.", plural(resumed, "run"))
}

// --- tables ---

func printIssueTable(w io.Writer, issues []*issueStats) {
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
	printTable(w, "by issue",
		[]string{"issue", "outcome", "runs", "questions", "cost", "tokens", "wall"}, rows, 2)
}

// printGroupTable breaks the numbers down by the configuration under test. An
// issue whose runs span two models or two tags counts under each — the point
// of the breakdown is comparing batches, and a batch is normally one of both,
// so the footnote appears only when that assumption does not hold.
func printGroupTable(w io.Writer, ds dataset, issues []*issueStats, by string) {
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
		name := r.Tag
		if by == byModel {
			name = r.Model
		}
		if name == "" {
			name = "(none)"
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
	rows := make([][]string, 0, len(order))
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
		perMerge := "—"
		if wins > 0 {
			perMerge = usd(g.cost / float64(wins))
		}
		rows = append(rows, []string{
			g.name, strconv.Itoa(len(g.issues)), strconv.Itoa(wins),
			strconv.Itoa(g.runs), usd(g.cost), perMerge, count(g.tokens),
		})
	}
	printTable(w, "by "+by,
		[]string{by, "issues", "merged", "runs", "cost", "$/merged", "tokens"}, rows, 1)
	// How many issues span groups — not how many surplus memberships they add
	// up to, which is a different and larger number whenever one spans three.
	spanning := 0
	for _, n := range memberships {
		if n > 1 {
			spanning++
		}
	}
	if spanning > 0 {
		fmt.Fprintf(w, "  (%s more than one %s, and is counted under each)\n",
			plural(spanning, "issue")+" spans", by)
	}
}

// --- plain aligned text ---

func printPairs(w io.Writer, title string, pairs [][2]string) {
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
		fmt.Fprintf(w, "  %-*s  %s\n", width, p[0], p[1])
	}
}

// printTable aligns a header and its rows: the leading left columns name
// things, everything after them is a number, and a column of dollars only
// reads as a column when it is right-aligned.
func printTable(w io.Writer, title string, header []string, rows [][]string, left int) {
	fmt.Fprintf(w, "\n%s\n", title)
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
	line := func(cells []string) {
		var b strings.Builder
		for i, cell := range cells {
			b.WriteString("  ")
			if i < left {
				fmt.Fprintf(&b, "%-*s", widths[i], cell)
			} else {
				fmt.Fprintf(&b, "%*s", widths[i], cell)
			}
		}
		fmt.Fprintln(w, strings.TrimRight(b.String(), " "))
	}
	line(header)
	for _, row := range rows {
		line(row)
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
