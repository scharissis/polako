package main

// The `-by` breakdown: grouping run records by whatever configuration or
// provenance value is under test (model, tag, shift, reason) and rendering
// one row per group. Split out of stats.go (issue #149's accretion debt) as
// a verbatim, self-contained unit — nothing here reaches back into the rest
// of that file beyond the shared types and helpers every renderer uses.

import (
	"fmt"
	"io"
	"sort"
	"strconv"
)

func printGroupTable(w io.Writer, rpt report, ds dataset, issues []*issueStats, by string) {
	rows, spanning := groupRows(ds, issues, by)
	printTable(w, rpt, "by "+by, groupHeader(by), rows, groupLeft)
	if note := spanningNote(spanning, by); note != "" {
		fmt.Fprintf(w, "  %s\n", note)
	}
}

// groupHeader names the first column after whatever is being grouped by, so
// the same builder serves -by model, -by tag, -by shift and -by reason.
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

// groupRows breaks the numbers down by the configuration under test, by the
// drain that did the work, or by why each run happened. An issue whose runs
// span two models or two tags counts under each — the point of the breakdown
// is comparing batches, and a batch is normally one of both, so the footnote
// appears only when that assumption does not hold. By drain it holds far less
// often: an issue picked up by one drain and finished by the next is the
// ordinary shape of a restart. By reason it barely holds at all — an issue
// with more than one run routinely cycles through several reasons, so the
// footnote firing on most -by reason reports is expected, not a signal
// something is off; $/merged there reads as "spent on issues that ever needed
// this kind of run, per one that shipped", not a like-for-like cost compare.
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

// groupTotals breaks the numbers down by the configuration under test, by
// the drain that did the work, or by why each run happened. An issue whose
// runs span two models or two tags counts under each — the point of the
// breakdown is comparing batches, and a batch is normally one of both, so
// spanningCount's footnote appears only when that assumption does not hold.
// By drain it holds far less often: an issue picked up by one drain and
// finished by the next is the ordinary shape of a restart. By reason,
// spanning is the common case rather than the exception — see groupRows.
func groupTotals(ds dataset, by string) (groups map[string]*statGroup, order []string) {
	groups = map[string]*statGroup{}
	for _, r := range ds.runs {
		var name string
		switch by {
		case byModel:
			name = r.Model
		case byShift:
			name = r.Shift
		case byReason:
			// label(), not the raw value: the run log (stats.go) and the
			// "reasons" summary line render reason this same way, and a group
			// name is exactly the kind of place that convention exists for.
			name = label(r.Reason)
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
