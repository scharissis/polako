package main

// `status`'s view of design requests: the row and the needs-you clauses.
// Apart from status.go because that file sits at its size budget
// (sizebudget_test.go).

import "fmt"

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
