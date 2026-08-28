package main

// `polako stats -json` prints the same facts as the text report, as one
// JSON document on stdout — the stats sibling of `status -json`.
//
// It is a second *view*, never a second *derivation*: every figure comes
// straight out of the statsSummary (stats.go) that sourcePairs, issuePairs,
// runPairs, costPairs and latencyPairs already format prose from. The two
// can disagree about layout; they cannot disagree about a number, because
// there is only one place each one is computed.
//
// The `-by` and `-runs` sections are the one part with no shared summary —
// they are typed straight out of dataset/[]*issueStats, the same rows
// issueRows/groupRows/runRows format into table cells, just kept as numbers
// instead of being stringified first.

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"time"
)

// statsDoc is the whole answer to `polako stats -json`, in explicit typed
// fields rather than map[string]any — reviewable and stable, the same
// promise statusDoc makes.
type statsDoc struct {
	Dir     string          `json:"dir"`
	Scope   statsDocScope   `json:"scope"`
	Source  statsDocSource  `json:"source"`
	Issues  statsDocIssues  `json:"issues"`
	Runs    statsDocRuns    `json:"runs"`
	Cost    statsDocCost    `json:"cost"`
	Latency statsDocLatency `json:"latency"`
	By      *statsDocBy     `json:"by,omitempty"`
	RunLog  []statsDocRun   `json:"run_log,omitempty"`
}

// statsDocScope names what was asked for. SinceSeconds is a pointer because
// "no -since given" and "-since 0s" are different questions nobody would
// actually ask, but the distinction is free and a pointer says so plainly.
// Shift is the *resolved* id — ds.shift — never the literal "last".
type statsDocScope struct {
	Repo         string `json:"repo,omitempty"`
	SinceSeconds *int64 `json:"since_seconds,omitempty"`
	Shift        string `json:"shift,omitempty"`
}

type statsDocSource struct {
	Files      int      `json:"files"`
	Records    int      `json:"records"`
	Skipped    int      `json:"skipped"`
	Unread     []string `json:"unread"`
	WindowFrom string   `json:"window_from,omitempty"`
	WindowTo   string   `json:"window_to,omitempty"`
	Repos      []string `json:"repos"`
}

// statsDocMeanMedian is mean and median together, because one pathological
// issue drags a mean somewhere no real issue has ever been — the same reason
// the text report always shows both.
type statsDocMeanMedian struct {
	Mean   float64 `json:"mean"`
	Median float64 `json:"median"`
}

// tokenSplitDoc is a token count's four components. Used both for a total
// (cost.tokens) and a per-issue mean (issues.tokens_per_issue_split).
type tokenSplitDoc struct {
	In         int64 `json:"in"`
	Out        int64 `json:"out"`
	CacheRead  int64 `json:"cache_read"`
	CacheWrite int64 `json:"cache_write"`
}

func tokenSplitDocFrom(t tokenCounts) tokenSplitDoc {
	return tokenSplitDoc{In: t.In, Out: t.Out, CacheRead: t.CacheRead, CacheWrite: t.CacheWrite}
}

// statsDocIssues mirrors issuePairs. RunsPerIssue, CostPerIssueUSD,
// TokensPerIssue and TokensPerIssueSplit are nil together whenever no
// terminal issue has runs in scope to price — the same "nothing to price"
// state the text report's "per issue" line names in words. ParkReasons and
// ChangePerIssue are nil when the text report omits their line entirely,
// never a map or struct of zeroes standing in for "not applicable".
type statsDocIssues struct {
	Terminal            map[string]int      `json:"terminal"`
	Done                int                 `json:"done"`
	InFlight            int                 `json:"in_flight"`
	ParkReasons         map[string]int      `json:"park_reasons,omitempty"`
	Priced              int                 `json:"priced"`
	RunsPerIssue        *statsDocMeanMedian `json:"runs_per_issue,omitempty"`
	CostPerIssueUSD     *statsDocMeanMedian `json:"cost_per_issue_usd,omitempty"`
	TokensPerIssue      *statsDocMeanMedian `json:"tokens_per_issue,omitempty"`
	TokensPerIssueSplit *tokenSplitDoc      `json:"tokens_per_issue_split,omitempty"`
	ChangePerIssue      *statsDocChange     `json:"change_per_issue,omitempty"`
}

type statsDocChange struct {
	AdditionsMedian int `json:"additions_median"`
	DeletionsMedian int `json:"deletions_median"`
	FilesMedian     int `json:"files_median"`
	ReviewsMedian   int `json:"reviews_median,omitempty"`
	Issues          int `json:"issues"`
}

type statsDocRuns struct {
	Total        int            `json:"total"`
	Statuses     map[string]int `json:"statuses"`
	Reasons      map[string]int `json:"reasons"`
	Outcomes     map[string]int `json:"outcomes"`
	Turns        int            `json:"turns"`
	ToolUses     int            `json:"tool_uses"`
	Approximated int            `json:"approximated"`
}

type statsDocCost struct {
	TotalUSD     float64       `json:"total_usd"`
	PerDayUSD    *float64      `json:"per_day_usd,omitempty"`
	Merged       int           `json:"merged"`
	PerMergedUSD *float64      `json:"per_merged_usd,omitempty"`
	Tokens       tokenSplitDoc `json:"tokens"`
	TotalTokens  int64         `json:"total_tokens"`
}

type statsDocLatency struct {
	BlockedOnAnswers statsDocSpans `json:"blocked_on_answers"`
	PRToMerge        statsDocSpans `json:"pr_to_merge"`
}

// statsDocSpans is spanSummary's numbers. MedianSeconds and MaxSeconds are
// nil together when there are no spans in the window — Count 0 already says
// that; a duration nobody measured is not zero seconds.
type statsDocSpans struct {
	Count         int      `json:"count"`
	MedianSeconds *float64 `json:"median_seconds,omitempty"`
	MaxSeconds    *float64 `json:"max_seconds,omitempty"`
}

// statsDocBy is the `-by` breakdown, present only when the flag was given.
// Kind names which one; exactly one of Issues or Groups is populated.
type statsDocBy struct {
	Kind     string             `json:"kind"`
	Issues   []statsDocIssueRow `json:"issues,omitempty"`
	Groups   []statsDocGroupRow `json:"groups,omitempty"`
	Spanning int                `json:"spanning,omitempty"`
}

// statsDocIssueRow is `-by issue`'s row, the typed source issueRows formats
// into table cells. Outcome is the raw record value ("opened_pr", not
// "opened pr") — the documented on-disk vocabulary, and what a script wants
// to compare against.
type statsDocIssueRow struct {
	Repo        string  `json:"repo"`
	Issue       int     `json:"issue"`
	Outcome     string  `json:"outcome"`
	Runs        int     `json:"runs"`
	Questions   int     `json:"questions"`
	CostUSD     float64 `json:"cost_usd"`
	Tokens      int64   `json:"tokens"`
	WallSeconds int64   `json:"wall_seconds"`
}

// statsDocGroupRow is one row of `-by model`, `-by tag` or `-by shift`.
type statsDocGroupRow struct {
	Name         string   `json:"name"`
	Issues       int      `json:"issues"`
	Merged       int      `json:"merged"`
	Runs         int      `json:"runs"`
	CostUSD      float64  `json:"cost_usd"`
	PerMergedUSD *float64 `json:"per_merged_usd,omitempty"`
	Tokens       int64    `json:"tokens"`
}

// statsDocRun is one row of the `-runs` run log — the same ledger runRows
// formats into table cells, kept as numbers.
type statsDocRun struct {
	Started     string  `json:"started,omitempty"`
	Repo        string  `json:"repo"`
	Issue       int     `json:"issue"`
	Reason      string  `json:"reason"`
	Status      string  `json:"status"`
	Outcome     string  `json:"outcome"`
	Session     string  `json:"session,omitempty"`
	Attempt     int     `json:"attempt"`
	CostUSD     float64 `json:"cost_usd"`
	Tokens      int64   `json:"tokens"`
	WallSeconds int64   `json:"wall_seconds"`
}

// renderStatsJSON writes statsDoc as the whole of stdout: one document, no
// header, no trailing prose, so `polako stats -json | jq` works.
func renderStatsJSON(w io.Writer, ds dataset, issues []*issueStats, summary statsSummary, opt statsOptions) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(statsDocFrom(ds, issues, summary, opt)); err != nil {
		return fmt.Errorf("could not encode the stats report as JSON (%w) — this is a bug in polako; "+
			"dropping -json still gets you the text report", err)
	}
	return nil
}

func statsDocFrom(ds dataset, issues []*issueStats, summary statsSummary, opt statsOptions) statsDoc {
	doc := statsDoc{
		Dir:     ds.dir,
		Scope:   statsDocScopeFrom(summary.source),
		Source:  statsDocSourceFrom(summary.source),
		Issues:  statsDocIssuesFrom(summary.issues),
		Runs:    statsDocRunsFrom(summary.runs),
		Cost:    statsDocCostFrom(summary.cost),
		Latency: statsDocLatencyFrom(summary.latency),
	}
	if opt.by != "" {
		by := statsDocByFrom(ds, issues, opt.by)
		doc.By = &by
	}
	if opt.runs {
		doc.RunLog = statsDocRunsLogFrom(ds)
	}
	return doc
}

func statsDocScopeFrom(s sourceSummary) statsDocScope {
	scope := statsDocScope{Repo: s.repoFilter, Shift: s.shift}
	if s.sinceFilter > 0 {
		secs := int64(s.sinceFilter.Seconds())
		scope.SinceSeconds = &secs
	}
	return scope
}

func statsDocSourceFrom(s sourceSummary) statsDocSource {
	doc := statsDocSource{
		Files:   s.files,
		Records: s.records,
		Skipped: s.skipped,
		Unread:  nonNilSlice(s.unread),
		Repos:   nonNilSlice(s.repos),
	}
	if !s.windowFrom.IsZero() {
		doc.WindowFrom = stamp(s.windowFrom)
		doc.WindowTo = stamp(s.windowTo)
	}
	return doc
}

func statsDocIssuesFrom(s issuesSummary) statsDocIssues {
	doc := statsDocIssues{
		Terminal: nonNilMap(s.terminal),
		Done:     s.done,
		InFlight: s.inFlight,
		Priced:   s.priced,
	}
	if len(s.parkReasons) > 0 {
		doc.ParkReasons = unrecordedKeyed(s.parkReasons)
	}
	if s.change != nil {
		doc.ChangePerIssue = &statsDocChange{
			AdditionsMedian: s.change.addsMedian,
			DeletionsMedian: s.change.delsMedian,
			FilesMedian:     s.change.filesMedian,
			Issues:          s.change.n,
		}
		if s.change.hasReviews {
			doc.ChangePerIssue.ReviewsMedian = s.change.reviewsMedian
		}
	}
	if s.priced == 0 {
		return doc
	}
	doc.RunsPerIssue = &statsDocMeanMedian{Mean: s.runsMean, Median: s.runsMedian}
	doc.CostPerIssueUSD = &statsDocMeanMedian{Mean: s.costMean, Median: s.costMedian}
	doc.TokensPerIssue = &statsDocMeanMedian{Mean: s.tokensMean, Median: float64(s.tokensMedian)}
	split := tokenSplitDocFrom(divideTokens(s.tokensSplitSum, s.tokensSplitN))
	doc.TokensPerIssueSplit = &split
	return doc
}

// divideTokens is the per-issue composition split() renders as prose,
// division included: the two must read the same n runs of integer division
// so a JSON reader and the text report never round differently.
func divideTokens(t tokenCounts, n int64) tokenCounts {
	if n < 1 {
		n = 1
	}
	return tokenCounts{In: t.In / n, Out: t.Out / n, CacheRead: t.CacheRead / n, CacheWrite: t.CacheWrite / n}
}

func statsDocRunsFrom(s runsSummary) statsDocRuns {
	return statsDocRuns{
		Total:        s.total,
		Statuses:     nonNilMap(s.statuses),
		Reasons:      nonNilMap(s.reasons),
		Outcomes:     nonNilMap(s.outcomes),
		Turns:        s.turns,
		ToolUses:     s.tools,
		Approximated: s.approximated,
	}
}

func statsDocCostFrom(s costSummary) statsDocCost {
	doc := statsDocCost{
		TotalUSD:    s.totalUSD,
		Merged:      s.merged,
		Tokens:      tokenSplitDocFrom(s.tokens),
		TotalTokens: s.tokens.total(),
	}
	if days := s.windowTo.Sub(s.windowFrom).Hours() / 24; days >= 1.0/24 {
		perDay := s.totalUSD / days
		doc.PerDayUSD = &perDay
	}
	if s.merged > 0 {
		perMerged := s.totalUSD / float64(s.merged)
		doc.PerMergedUSD = &perMerged
	}
	return doc
}

func statsDocLatencyFrom(s latencySummary) statsDocLatency {
	return statsDocLatency{
		BlockedOnAnswers: statsDocSpansFrom(s.blocked),
		PRToMerge:        statsDocSpansFrom(s.toMerge),
	}
}

func statsDocSpansFrom(spans []time.Duration) statsDocSpans {
	doc := statsDocSpans{Count: len(spans)}
	if len(spans) == 0 {
		return doc
	}
	med, max := median(spans).Seconds(), slices.Max(spans).Seconds()
	doc.MedianSeconds, doc.MaxSeconds = &med, &max
	return doc
}

func statsDocByFrom(ds dataset, issues []*issueStats, by string) statsDocBy {
	if by == byIssue {
		rows := make([]statsDocIssueRow, 0, len(issues))
		for _, is := range issues {
			rows = append(rows, statsDocIssueRow{
				Repo: is.key.repo, Issue: is.key.issue, Outcome: is.outcome(),
				Runs: len(is.runs), Questions: is.questions(),
				CostUSD: is.cost, Tokens: is.tokens.total(), WallSeconds: is.wallMS / 1000,
			})
		}
		return statsDocBy{Kind: by, Issues: rows}
	}

	groups, order := groupTotals(ds, by)
	merged := mergedIssues(issues)
	rows := make([]statsDocGroupRow, 0, len(order))
	for _, name := range order {
		g := groups[name]
		wins := 0
		for key := range g.issues {
			if merged[key] {
				wins++
			}
		}
		row := statsDocGroupRow{Name: g.name, Issues: len(g.issues), Merged: wins, Runs: g.runs, CostUSD: g.cost, Tokens: g.tokens}
		if wins > 0 {
			perMerged := g.cost / float64(wins)
			row.PerMergedUSD = &perMerged
		}
		rows = append(rows, row)
	}
	return statsDocBy{Kind: by, Groups: rows, Spanning: spanningCount(groups, order)}
}

func statsDocRunsLogFrom(ds dataset) []statsDocRun {
	rows := make([]statsDocRun, 0, len(ds.runs))
	for _, r := range ds.runs {
		rows = append(rows, statsDocRun{
			Started: r.TS, Repo: r.Repo, Issue: r.Issue, Reason: r.Reason, Status: r.Status,
			Outcome: r.Outcome, Session: r.Session, Attempt: r.Attempt,
			CostUSD: r.CostUSD, Tokens: r.Tokens.total(), WallSeconds: r.WallMS / 1000,
		})
	}
	return rows
}

// nonNilSlice is status.go's — reused here so both -json documents keep the
// same "empty array, never null" promise without two copies of the helper.

// nonNilMap is nonNilSlice's twin for the breakdown maps: `{}`, never `null`.
func nonNilMap[K comparable, V any](m map[K]V) map[K]V {
	if m == nil {
		return map[K]V{}
	}
	return m
}

// unrecordedKeyed renames the "" key breakdown() treats as "unrecorded" — a
// record written before the field it counts existed — to that word, since ""
// is not a usable JSON object key for a script to look up.
func unrecordedKeyed(counts map[string]int) map[string]int {
	if n, ok := counts[""]; ok {
		out := make(map[string]int, len(counts))
		for k, v := range counts {
			if k != "" {
				out[k] = v
			}
		}
		out["unrecorded"] = n
		return out
	}
	return counts
}
