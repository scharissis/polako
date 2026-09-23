package main

import (
	"context"
	"fmt"
	"slices"
)

// A gated `status` lists the whole repository once and partitions it here,
// rather than passing `--label` the way `work` does. Scoped to the gate on
// GitHub's side, a `proposed` issue nobody has added the gate label to yet
// drops out of the report — and so does the curate clause that would have
// told an operator it's waiting — and an outside issue waiting for triage is
// invisible. Only the in-gate subset goes through sortIssueQueues, so the
// queue `status` reports is still exactly the one `work -label` would drain.

// outsideGateShown caps the `outside the gate` row: on a public repository
// that row is everyone's untriaged issues, and it is context, not the queue.
// -json carries the full list.
const outsideGateShown = 10

// gateSplit is what the unscoped listing shows beyond the in-gate queues.
// Both are empty on an unscoped report.
type gateSplit struct {
	// label is the gate label itself, so the curate clause can name it.
	label string
	// outside is every open issue with neither the gate label nor a hold
	// (needs-human, proposed, awaiting-answer), ascending. Containers are left
	// out too: never worked, so never gated.
	outside []int
	// ungatedProposed is the proposed issues lacking the gate label — already
	// merged into issueQueues.proposed, kept here so the curate clause can say
	// approving them takes adding the gate label as well.
	ungatedProposed []int
}

// statusQueues is openQueues for `status`: the same queues, plus what lies
// outside the gate when there is one.
func statusQueues(ctx context.Context, cfg config) (issueQueues, gateSplit, error) {
	if cfg.label == "" {
		q, err := openQueues(ctx, cfg)
		return q, gateSplit{}, err
	}
	all := cfg
	all.label = ""
	raw, err := retryRead(ctx, cfg, "listing open issues", func() ([]byte, error) {
		return listOpenIssues(ctx, all)
	})
	if err != nil {
		return issueQueues{}, gateSplit{}, err
	}
	issues, err := parseIssueList(raw)
	if err != nil {
		return issueQueues{}, gateSplit{}, err
	}
	q, split := partitionByGate(issues, cfg.label)
	return q, split, nil
}

// partitionByGate splits one unscoped listing at the gate label. The label
// checks for the out-of-gate rows follow sortIssueQueues' own precedence: a
// container first, then needs-human over proposed.
func partitionByGate(issues []ghIssue, gate string) (issueQueues, gateSplit) {
	var inGate []ghIssue
	split := gateSplit{label: gate}
	for _, is := range issues {
		switch {
		case is.hasLabel(gate):
			inGate = append(inGate, is)
		case is.SubIssues.Total > 0, is.hasLabel(needsHumanLabel), is.hasLabel(awaitingAnswerLabel):
		case is.hasLabel(proposedLabel):
			split.ungatedProposed = append(split.ungatedProposed, is.Number)
		default:
			split.outside = append(split.outside, is.Number)
		}
	}
	q := sortIssueQueues(inGate)
	slices.Sort(split.outside)
	slices.Sort(split.ungatedProposed)
	q.proposed = append(q.proposed, split.ungatedProposed...)
	slices.Sort(q.proposed)
	return q, split
}

// curateClauses is the needs-you line's curation half. A proposal already
// carrying the gate label queues once `proposed` comes off; one without it
// takes a second move, and the clause names both so neither is guessed at.
func curateClauses(snap statusSnapshot) []string {
	var gated []int
	for _, n := range snap.queues.proposed {
		if !slices.Contains(snap.gate.ungatedProposed, n) {
			gated = append(gated, n)
		}
	}
	var parts []string
	if len(gated) > 0 {
		parts = append(parts, fmt.Sprintf("curate %s (drop %s to queue them)", issueRefs(gated), proposedLabel))
	}
	if len(snap.gate.ungatedProposed) > 0 {
		parts = append(parts, fmt.Sprintf("curate %s (drop %s, add %s)",
			issueRefs(snap.gate.ungatedProposed), proposedLabel, snap.gate.label))
	}
	return parts
}

// outsideGateLine renders the `outside the gate` row: numbers only, the first
// outsideGateShown, then how many more.
func outsideGateLine(outside []int) string {
	shown := outside
	more := ""
	if len(shown) > outsideGateShown {
		shown = shown[:outsideGateShown]
		more = fmt.Sprintf(" and %d more", len(outside)-outsideGateShown)
	}
	return plural(len(outside), "issue") + " — " + issueRefs(shown) + more
}
