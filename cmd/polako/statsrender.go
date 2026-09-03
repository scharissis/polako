package main

// The text report: `polako stats` with neither -json nor -html. Everything
// here turns a statsSummary (stats.go), and the dataset/[]*issueStats behind
// it, into aligned prose and tables on an io.Writer. It is one of three
// renderers over that same summary — statsjson.go and statshtml.go are the
// others — and like them it only formats: every figure is computed once in
// stats.go, so two renderers can differ on layout but never on a number.
//
// Split out of stats.go (issue #149's accretion debt) as verbatim movement;
// nothing here changed, since it is the same package.

import (
	"fmt"
	"io"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
)

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
	case byModel, byTag, byShift, byReason:
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
