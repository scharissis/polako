package main

import (
	"context"
	"fmt"
	"slices"
)

// Scoped to the gate on GitHub's side, a `proposed` issue nobody has added the
// gate label to yet drops out of the report — and so does the curate clause
// that would have told an operator it's waiting — and an outside issue waiting
// for triage is invisible. So a gated `status` makes one more listing,
// unscoped, for what lies outside the gate.
//
// The queues themselves still come from `work`'s own `--label` listing, not
// from partitioning the unscoped one. Both are capped at --limit 200, and on a
// repository with more open issues than that, the unscoped listing's newest
// 200 can crowd out an old in-gate issue — the one the queue must not lose.
// The extras are context; losing a few of those past the cap costs little.

// outsideGateShown caps the `outside the gate` row: on a public repository
// that row is everyone's untriaged issues, and it is context, not the queue.
// -json carries the full list.
const outsideGateShown = 10

// gateSplit is what the unscoped listing shows beyond the in-gate queues.
// Both are empty on an unscoped report.
type gateSplit struct {
	// label is the gate label itself, so the curate clause can name it.
	label string
	// outside is every open issue without the gate label that the gate label
	// alone would queue — what sortIssueQueues calls ready or held back —
	// ascending. A hold (needs-human, proposed, awaiting-answer) or a
	// container is not triage, so it isn't here.
	outside []int
	// ungatedProposed is the proposed issues lacking the gate label — already
	// merged into issueQueues.proposed, kept here so the curate clause can say
	// approving them takes adding the gate label as well.
	ungatedProposed []int
	// held counts the rest outside the gate — holds and containers. Not
	// listed, but still open, so a report with only these is not a cleared one.
	held int
	// detail is the unscoped listing's per-issue detail, for the
	// ungatedProposed statusQueues folds into the queues.
	detail map[int]issueDetail
}

// open reports whether anything at all is open outside the gate.
func (g gateSplit) open() bool {
	return len(g.outside)+len(g.ungatedProposed)+g.held > 0
}

// statusQueues is openQueues for `status`: the same queues, plus what lies
// outside the gate when there is one.
func statusQueues(ctx context.Context, cfg config) (issueQueues, gateSplit, error) {
	q, err := openQueues(ctx, cfg)
	if err != nil || cfg.label == "" {
		return q, gateSplit{}, err
	}
	all := cfg
	all.label = ""
	raw, err := retryRead(ctx, cfg, "listing issues outside the gate", func() ([]byte, error) {
		return listOpenIssues(ctx, all)
	})
	if err != nil {
		return issueQueues{}, gateSplit{}, err
	}
	issues, err := parseIssueList(raw)
	if err != nil {
		return issueQueues{}, gateSplit{}, err
	}
	split := outsideTheGate(issues, cfg.label)
	q.proposed = append(q.proposed, split.ungatedProposed...)
	slices.Sort(q.proposed)
	// The ungated proposals just joined q.proposed, so their age comes from
	// this listing: the gated one never saw them.
	for _, n := range split.ungatedProposed {
		q.detail[n] = split.detail[n]
	}
	return q, split, nil
}

// outsideTheGate sorts the issues without the gate label through the same
// sortIssueQueues the queue uses, so an exclusion added there reaches this
// row too, precedence and all.
func outsideTheGate(issues []ghIssue, gate string) gateSplit {
	var outside []ghIssue
	for _, is := range issues {
		if !is.hasLabel(gate) {
			outside = append(outside, is)
		}
	}
	o := sortIssueQueues(outside)
	split := gateSplit{label: gate, outside: o.ready, ungatedProposed: o.proposed,
		held: len(o.blocked) + len(o.parked) + len(o.containers), detail: o.detail}
	for _, h := range o.heldBack {
		split.outside = append(split.outside, h.number)
	}
	slices.Sort(split.outside)
	return split
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
