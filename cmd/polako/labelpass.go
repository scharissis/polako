package main

// The enforcing label pass — the mechanism that makes the `proposed` curation
// gate structural rather than something a model has to remember to apply.
// `polako plan` and `polako health` both spawn a skill whose whole write
// surface is `gh issue create`, cap it at -max-issues through dispatchClaude's
// issue-create counter (see errIssueCap), and then run normaliseProposals
// over whatever it filed — always, even on a crash, the cap kill or a Ctrl+C.
// One mechanism, shared: the two verbs differ in what they plan from and
// whether a milestone gets attached, not in how the gate is enforced.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"
)

// The `proposed` label is declared at plan/health preflight and nowhere
// else — the drain only ever excludes it. GitHub refuses to apply a label the
// repository never defined, and a headless run holds no grant that could
// create one, so the supervisor mints it up front exactly as it mints
// awaiting-answer.
const (
	proposedLabelColor = "1D76DB"
	proposedLabelDesc  = "proposed by polako — a human removes this label to queue it"
)

// openIssuesBefore is the set of open issue numbers before a run — the
// baseline the label pass diffs against, so an issue a person files by hand
// while the run is going is told apart from one the skill created. Open
// issues only: a proposal is created open, and a closed one is nothing an
// unattended drain would ever pick up.
func openIssuesBefore(ctx context.Context, cfg config) (map[int]bool, error) {
	raw, err := gh(ctx, cfg, "issue", "list", "--state", "open", "--limit", "1000", "--json", "number")
	if err != nil {
		return nil, err
	}
	var rows []struct {
		Number int `json:"number"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("unreadable issue list: %w", err)
	}
	seen := make(map[int]bool, len(rows))
	for _, r := range rows {
		seen[r.Number] = true
	}
	return seen, nil
}

// labelPassOutcome is what the enforcing label pass did — reported once, and
// turned into the caller's exit status. failures is the load-bearing field: an
// action that did not take means a proposal may be sitting unguarded, so it is
// collected here rather than swallowed and it makes the run exit nonzero.
type labelPassOutcome struct {
	created   int      // new issues this account was found to have filed
	epics     int      // of those, the ones that are containers (sub-issues > 0)
	labelled  []int    // issues confirmed to carry exactly proposedLabel afterwards
	added     int      // missing proposedLabel labels the pass applied
	stripped  int      // stray labels removed, across all of them
	milestone []int    // issues the batch milestone was newly attached to
	failures  []string // one line per action that did not take — loud
	listErr   error    // the after-listing itself failed: nothing could be checked
}

// labelsEnforced is how many label edits the pass had to make — adds plus
// strips — which is the measure of how far the run fell short of self-applying
// the curation gate, and the number the run's record carries for it.
func (o labelPassOutcome) labelsEnforced() int { return o.added + o.stripped }

// normaliseProposals is the enforcing label pass. It lists the issues this gh
// account has open, keeps the ones absent from `before` and numbered above
// everything that was there — the run's own output — and forces each to carry
// *exactly* proposedLabel, attaching the batch milestone to any that has none.
// milestone is "" for a run with no milestone concept at all (`polako
// health`), in which case that half of the pass is simply never reached. This
// is what keeps the `-label` queue-gate humans-only: `Bash(gh issue
// create:*)` is a prefix and no prefix can say "create, but not with that
// flag", so the create stays wide and the cleanup happens here.
//
// The `> maxBefore` guard is what makes the truncation of either listing safe:
// GitHub issue numbers only ever climb, so anything the run filed outnumbers
// everything open before it, and an old issue this account filed that fell off
// the end of the `before` page is never mistaken for the run's own. logTag is
// the narration prefix ("plan" or "health") so a mixed shift's terminal still
// says which run each label edit belongs to.
func normaliseProposals(ctx context.Context, cfg config, before map[int]bool, milestone, logTag string) labelPassOutcome {
	var out labelPassOutcome
	maxBefore := 0
	for n := range before {
		if n > maxBefore {
			maxBefore = n
		}
	}
	// subIssuesSummary rides along so the record can say how many of the run's
	// own issues are epics. A gh too old for the field rejects the whole call
	// before it asks GitHub anything, so fall back to the listing without it —
	// epics_created then reads 0, the same degradation the drain's container
	// skip takes, and a flat run has no epics to miss anyway.
	fields := "number,labels,milestone,subIssuesSummary"
	raw, err := gh(ctx, cfg, "issue", "list", "--author", "@me", "--state", "open",
		"--limit", "1000", "--json", fields)
	if unknownJSONField(err) {
		fields = "number,labels,milestone"
		raw, err = gh(ctx, cfg, "issue", "list", "--author", "@me", "--state", "open",
			"--limit", "1000", "--json", fields)
	}
	if err != nil {
		out.listErr = err
		return out
	}
	var rows []struct {
		Number int `json:"number"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
		Milestone *struct {
			Title string `json:"title"`
		} `json:"milestone"`
		SubIssues struct {
			Total int `json:"total"`
		} `json:"subIssuesSummary"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		out.listErr = fmt.Errorf("unreadable issue list: %w", err)
		return out
	}

	for _, r := range rows {
		if before[r.Number] || r.Number <= maxBefore {
			continue // there before the run — not ours to touch
		}
		out.created++
		if r.SubIssues.Total > 0 {
			out.epics++
		}
		n := strconv.Itoa(r.Number)
		clean := true

		hasProposed := false
		for _, l := range r.Labels {
			if l.Name == proposedLabel {
				hasProposed = true
				continue
			}
			if _, err := gh(ctx, cfg, "issue", "edit", n, "--remove-label", l.Name); err != nil {
				out.failures = append(out.failures,
					fmt.Sprintf("could not strip %q from #%d: %v", l.Name, r.Number, err))
				clean = false
				continue
			}
			log.Printf("%s: stripped %q from #%d — a proposal carries only %s", logTag, l.Name, r.Number, proposedLabel)
			out.stripped++
		}
		if !hasProposed {
			if _, err := gh(ctx, cfg, "issue", "edit", n, "--add-label", proposedLabel); err != nil {
				out.failures = append(out.failures,
					fmt.Sprintf("could not add %s to #%d: %v", proposedLabel, r.Number, err))
				clean = false
			} else {
				log.Printf("%s: labelled #%d %s", logTag, r.Number, proposedLabel)
				out.added++
			}
		}
		if clean {
			out.labelled = append(out.labelled, r.Number)
		}

		if milestone != "" && (r.Milestone == nil || strings.TrimSpace(r.Milestone.Title) == "") {
			if _, err := gh(ctx, cfg, "issue", "edit", n, "--milestone", milestone); err != nil {
				out.failures = append(out.failures,
					fmt.Sprintf("could not attach the %q milestone to #%d: %v", milestone, r.Number, err))
			} else {
				log.Printf("%s: attached the %q milestone to #%d", logTag, milestone, r.Number)
				out.milestone = append(out.milestone, r.Number)
			}
		}
	}
	return out
}

// report says what the pass did, at the severity the outcome earns: an error
// when it could not even list what to check, a warning when the run was capped
// or overspent, success otherwise. The -max-cost check lives here because a
// one-shot run has no next run for it to bound — unlike `work`, where the same
// flag stops the drain dispatching another — so all it can do is say so.
// prefix is the narration tag the caller's own log lines already use ("plan"
// or "health"), so a mixed shift's terminal still says which run this line
// belongs to.
func (o labelPassOutcome) report(prefix string, maxCost float64, rep runReport) {
	if o.listErr != nil {
		narrate(sevError, "%s: could not list what the run created to normalise it (%v) — "+
			"check the backlog for issues missing the %s label", prefix, o.listErr, proposedLabel)
		return
	}
	if maxCost > 0 && rep.costUSD >= maxCost {
		narrate(sevWarning, "%s: the run cost $%.2f, at or past the -max-cost of $%.2f", prefix, rep.costUSD, maxCost)
	}
	sev := sevSuccess
	if rep.capped || len(o.failures) > 0 {
		sev = sevWarning
	}
	narrate(sev, "%s: %s", prefix, o.summary(rep))
}

func (o labelPassOutcome) summary(rep runReport) string {
	if o.created == 0 {
		if rep.capped {
			return "the run was capped before it created anything"
		}
		return "the run created no issues"
	}
	s := fmt.Sprintf("%s created, %d normalised to %s",
		plural(o.created, "issue"), len(o.labelled), proposedLabel)
	if o.stripped > 0 {
		s += fmt.Sprintf(" (%s stripped)", plural(o.stripped, "stray label"))
	}
	if len(o.milestone) > 0 {
		s += fmt.Sprintf(", milestone attached to %d", len(o.milestone))
	}
	if rep.capped {
		s += " — stopped at the -max-issues cap"
	}
	if len(o.failures) > 0 {
		s += fmt.Sprintf(" — %s FAILED, see above", plural(len(o.failures), "action"))
	}
	return s
}

// err is the pass's verdict as the run's exit status: nil unless something did
// not take, loud otherwise. A failed pass outranks a failed run in the caller,
// because an unguarded proposal is the worse outcome to leave unsaid.
func (o labelPassOutcome) err() error {
	if o.listErr != nil {
		return fmt.Errorf("the label pass could not run (%w) — issues the run created may be unlabelled; "+
			"list the backlog and add the %s label to any proposal missing it", o.listErr, proposedLabel)
	}
	if len(o.failures) == 0 {
		return nil
	}
	return fmt.Errorf("the label pass left %s unapplied, so a proposal may be unguarded — "+
		"fix by hand:\n  %s", plural(len(o.failures), "issue action"), strings.Join(o.failures, "\n  "))
}

// noPricingHistory is what the pricing line prints when history cannot price
// the batch — no records, or none that was ever priced. Fixed wording, never
// a guessed number: the estimate is history's to state or nobody's.
const noPricingHistory = "no run history to price against — work a few issues and future plans will estimate themselves"

// proposalPricingLine is the one line a plan or health report prints after the
// label pass: what the operator's own run records say a batch of proposals
// will cost to implement — the median cost and median run time of a merged
// issue in this repository, times the number of proposals. It never invents a
// figure; with no usable history it says exactly that and stops.
//
// This is the second of telemetry's two readers, the one named beside `stats`
// in CLAUDE.md's write-only-telemetry invariant: human-facing rendering,
// computed after the run has ended, deciding nothing the supervisor does.
// Delete the metrics directory mid-run and the only change is this line
// falling back to noPricingHistory. It reuses loadRecords + rollUpIssues — the
// torn-line, unknown-kind and latest-wins-dedupe rules `stats` already keeps
// — rather than parsing the records a second way. It reads ordinary issue-run
// records, keyed on a merged outcome, so it is unaffected by which verb
// proposed the batch it is pricing.
func proposalPricingLine(metricsDir, repo string, proposals int, now time.Time) string {
	if metricsDir == "" {
		return noPricingHistory // -metrics off, or no home directory: nothing to read, no file opened to find out
	}
	ds, err := loadRecords(metricsDir, statsOptions{repo: repo}, now)
	if err != nil {
		return noPricingHistory
	}
	var costs []float64
	var times []time.Duration
	for _, is := range rollUpIssues(ds) {
		if is.terminal == nil || is.terminal.Outcome != issueMerged || len(is.runs) == 0 {
			continue
		}
		// A merged issue whose runs never reported a cost — a crash a human
		// finished by hand, say. A real record and a useless price: its $0 and
		// its handful of minutes would drag both medians down, so it is left
		// out exactly as a wholly unpriced history is. Skipped per issue, not
		// just in aggregate, so one such issue among several does not skew the
		// estimate low.
		if is.cost == 0 {
			continue
		}
		costs = append(costs, is.cost)
		times = append(times, time.Duration(is.wallMS)*time.Millisecond)
	}
	// No merged issue that was ever priced: no history to project from, said as
	// such rather than as a batch that costs $0.
	if len(costs) == 0 {
		return noPricingHistory
	}
	n := len(costs)
	costMedian := median(costs)
	timeMedian := median(times)
	return fmt.Sprintf("your last %s ran %s and %s median — %s ≈ %s and %s of run time, before curation cuts",
		plural(n, "merged issue"), usd(costMedian), dur(timeMedian),
		plural(proposals, "proposal"),
		approxUSD(float64(proposals)*costMedian),
		approxDur(time.Duration(proposals)*timeMedian))
}

// approxUSD renders a projected batch cost — a median times a count — at the
// resolution an estimate earns: whole dollars once it is worth more than a
// few, so "≈ $19" is not dressed up as "$18.90".
func approxUSD(f float64) string {
	if f < 10 {
		return usd(f)
	}
	return fmt.Sprintf("$%.0f", f)
}

// approxDur renders a projected batch run time in half-hour steps once it is
// worth hours, for the same reason approxUSD rounds: "4½h" is honest about a
// median-times-count projection where "4h26m" would feign precision. Under an
// hour it falls back to dur rounded to the minute.
func approxDur(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	if d < time.Hour {
		return dur(d.Round(time.Minute))
	}
	// Round to the nearest half hour. d >= 1h here, so halves >= 2 and the
	// whole-hour count is always >= 1.
	halves := (d + 15*time.Minute) / (30 * time.Minute)
	if halves%2 == 1 {
		return fmt.Sprintf("%d½h", halves/2)
	}
	return fmt.Sprintf("%dh", halves/2)
}
