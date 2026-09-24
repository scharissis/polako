package main

// `status`'s view of design requests: the row and the needs-you clauses.
// Apart from status.go because that file sits at its size budget
// (sizebudget_test.go).

import (
	"fmt"
	"strings"
)

// designLine is the `design` row: how many, which, and the label that keeps
// them out of the queue.
func designLine(design []designInfo) string {
	nums := make([]int, len(design))
	for i, d := range design {
		nums[i] = d.number
	}
	return fmt.Sprintf("%s — %s, labelled %s", plural(len(nums), "issue"), issueRefs(nums), designLabel)
}

// designClauses is one needs-you clause per design request. Nothing a drain
// does moves one, so every one is the operator's: answer the question first
// if a design run stopped to ask one, else start the run.
func designClauses(design []designInfo) []string {
	parts := make([]string, 0, len(design))
	for _, d := range design {
		if d.awaiting {
			parts = append(parts, fmt.Sprintf("reply on #%d, then polako design -issue %d", d.number, d.number))
		} else {
			parts = append(parts, fmt.Sprintf("run polako design -issue %d", d.number))
		}
	}
	return parts
}

// parkedDesignClause is the needs-you clause for a parked design request.
// Clearing needs-human alone moves nothing — `work` leaves design issues
// out — so every form ends with designRunCommand, the same verb unpark's
// next-shift line names (parkNextShift). It never joins the batched "drop
// needs-human to requeue" clause, since there is no queue to go back to.
func parkedDesignClause(it parkListItem) string {
	then := fmt.Sprintf("polako unpark %d, then %s", it.issue, designRunCommand(it.issue))
	if len(it.entries) > 0 {
		return fmt.Sprintf("grant %s or fix the skill, then %s", strings.Join(it.entries, ", "), then)
	}
	clause, ok := parkNeedsYouClause[it.category]
	if !ok {
		clause = "is a parked design request"
	}
	return fmt.Sprintf("#%d %s — %s", it.issue, clause, then)
}
