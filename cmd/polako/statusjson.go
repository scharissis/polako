package main

import (
	"encoding/json"
	"fmt"
	"io"
)

// `polako status -json` prints the same snapshot as the text report, as one
// JSON document on stdout — split from status.go once the file crossed the
// 1,000-line budget (sizebudget_test.go), the same way statsjson.go already
// sits beside stats.go.
//
// A second renderer over statusSnapshot, exactly as buildHTMLReport
// (statshtml.go) is a second renderer over a stats dataset: every fact below
// is read out of the same snapshot the text report walks, or computed by the
// same helper (nextLine, needsYouParts, the PR cell functions) the text
// report calls — never re-derived. The two can disagree about layout; they
// cannot disagree about a fact, because there is only one place each fact is
// computed.

// statusDoc is the whole answer to `polako status -json`, in explicit typed
// fields rather than map[string]any: a schema that is reviewable, per the
// acceptance criteria on #118. Reviewable does not mean frozen — #168 widened
// `queue.containers` from bare issue numbers to objects once the sub-issue
// rollup's completed count was worth reporting, and said so in docs/reference.md.
type statusDoc struct {
	Repo          string         `json:"repo"`
	Scope         statusDocScope `json:"scope"`
	Queue         statusDocQueue `json:"queue"`
	Next          statusDocNext  `json:"next"`
	PRs           []statusDocPR  `json:"prs"`
	UndetailedPRs []int          `json:"undetailed_prs"`
	NeedsYou      []string       `json:"needs_you"`
	// Notes is everything the text report prints under its header that isn't
	// part of the queue itself — today just a missing -label's note. Always
	// `[]`, never null, the same rule every array field here holds to.
	Notes []string       `json:"notes"`
	Plans statusDocPlans `json:"plans"`
	// Plan is the same line the text report prints, or nil when the usage
	// probe could not answer — never an empty string standing in for "no
	// usage", which would be indistinguishable from a genuine 0%.
	Plan *string `json:"plan,omitempty"`
	// Published is the release polako has actually published — the same read
	// the text report's own notice makes, carried here whether or not that
	// notice fires, since a caller may want to know the current release
	// either way. Nil when the read failed, timed out, or could not run.
	Published *string `json:"published,omitempty"`
	// LastShift is the "last shift here" line's facts, from this machine's
	// run data — null, never absent, when there is none or -metrics is off.
	LastShift *statusDocLastShift `json:"last_shift"`
}

type statusDocScope struct {
	Label string `json:"label"`
	// Source is where Label came from: "flag", "env", or "github" (found
	// via markedGateLabel with no -label or POLAKO_LABEL given) — omitted
	// when Label is "" and the report is unscoped.
	Source      string `json:"source,omitempty"`
	StrictOrder bool   `json:"strict_order"`
}

type statusDocQueue struct {
	Ready      []int                `json:"ready"`
	HeldBack   []statusDocHeldBack  `json:"held_back"`
	Blocked    []statusDocBlocked   `json:"blocked"`
	Parked     []statusDocParked    `json:"parked"`
	Proposed   []int                `json:"proposed"`
	Containers []statusDocContainer `json:"containers"`
	// OutsideGate is gateSplit.outside in full — the text row stops at ten.
	// Always `[]` on an unscoped report.
	OutsideGate []int `json:"outside_gate"`
	// UngatedProposed is the part of Proposed lacking the gate label, which
	// takes adding it as well as dropping proposed to queue.
	UngatedProposed []int `json:"ungated_proposed"`
}

// statusDocHeldBack is one otherwise-ready issue put down this pass because
// at least one blockedBy dependency is still open — heldBackInfo, in JSON
// shape. Blockers is always `[]`, never null, the same rule every array
// field here holds to.
type statusDocHeldBack struct {
	Issue    int   `json:"issue"`
	Blockers []int `json:"blockers"`
}

// statusDocParked is one parked issue with the entries and category its own
// park comment named — #168's widening applied here too: bare issue numbers
// to objects, so a caller can tell a park with a named grant from one still
// waiting on a person's own judgment without a second call. Entries is
// always `[]`, never null — the same rule every array field in this
// document holds to — and only ever the valid, thread-safe entries
// validParkEntry accepts; an ignored one (unpark's own term for a footer
// entry shaped wrong, or naming a never-grant command) is left out, same as
// unpark's own rendering. Category is one of the fixed identifiers in
// metrics.go, or "" when the comment carried none parseParkCategory
// recognizes — no comment of polako's own, or a hand label.
type statusDocParked struct {
	Issue    int      `json:"issue"`
	Entries  []string `json:"entries"`
	Category string   `json:"category"`
}

// statusDocContainer is one container issue with its sub-issue rollup, so a
// caller can tell #113 (6 of 6 closed) from #147 (1 of 5) without a second
// call — the same widening `blocked` already carries for quiet_seconds.
// Finished is containerInfo.finished() verbatim: the one place that decides
// "done" is a Go method, and repeating its comparison in every jq script that
// reads this document would be exactly the "children invent their own"
// outcome that method exists to prevent.
type statusDocContainer struct {
	Issue     int  `json:"issue"`
	Total     int  `json:"total"`
	Completed int  `json:"completed"`
	Finished  bool `json:"finished"`
	// Held is containerInfo.held: a human has put needs-human or proposed on the
	// container, so the drain leaves it alone rather than closing it once
	// finished. Without this a caller cannot tell a finished container that is
	// about to be closed from one it must close itself.
	Held bool `json:"held"`
	// Closed is containerInfo.closed: the container issue itself is closed.
	// Only plans.docs[].containers ever carries true — queue.containers is
	// open issues alone. Without this a closed container and an open,
	// finished one both read as finished:true, held:false, and a caller
	// cannot tell "already closed" from "the next shift is about to close
	// it".
	Closed bool `json:"closed"`
}

// toStatusDocContainer is the one place containerInfo becomes a
// statusDocContainer, so queue.containers and plans.docs[].containers cannot
// silently diverge in shape the way two copies of this literal would let
// them.
func toStatusDocContainer(c containerInfo) statusDocContainer {
	return statusDocContainer{
		Issue: c.number, Total: c.total, Completed: c.completed, Finished: c.finished(), Held: c.held,
		Closed: c.closed,
	}
}

// statusDocBlocked is one issue awaiting an answer. QuietSeconds is a pointer
// because 0 (a reply just landed) and "the thread's age could not be read"
// are both real, distinct states — the same distinction unknownCell exists to
// preserve for a PR's fields, applied here to a duration instead of a string.
type statusDocBlocked struct {
	Issue        int    `json:"issue"`
	QuietSeconds *int64 `json:"quiet_seconds,omitempty"`
}

// statusDocNext is the issue a drain starting now would pick up, and why.
// Issue is 0 for none; Reason is nextLine(snap) verbatim, which already
// covers the 0 case in words.
type statusDocNext struct {
	Issue  int    `json:"issue"`
	Reason string `json:"reason"`
}

// statusDocPR mirrors the text report's table columns. Mergeable, Checks and
// Review are the same cell strings the table prints, unknownCell ("not read")
// included: reusing them rather than inventing a JSON-specific sentinel means
// there is exactly one meaning of "not read", not two.
type statusDocPR struct {
	Number    int    `json:"number"`
	Branch    string `json:"branch"`
	Issue     int    `json:"issue"`
	URL       string `json:"url"`
	Mergeable string `json:"mergeable"`
	Checks    string `json:"checks"`
	Review    string `json:"review"`
}

// renderStatusJSON writes statusDoc as the whole of stdout: one document, no
// header, no trailing prose, so `polako status -json | jq` works.
func renderStatusJSON(w io.Writer, cfg config, snap statusSnapshot) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(statusDocFrom(cfg, snap)); err != nil {
		return fmt.Errorf("could not encode the status report as JSON (%w) — this is a bug in polako; "+
			"dropping -json still gets you the text report", err)
	}
	return nil
}

func statusDocFrom(cfg config, snap statusSnapshot) statusDoc {
	heldBack := make([]statusDocHeldBack, 0, len(snap.queues.heldBack))
	for _, h := range snap.queues.heldBack {
		heldBack = append(heldBack, statusDocHeldBack{Issue: h.number, Blockers: nonNilSlice(h.blockers)})
	}

	blocked := make([]statusDocBlocked, 0, len(snap.queues.blocked))
	for _, issue := range snap.queues.blocked {
		b := statusDocBlocked{Issue: issue}
		if d, ok := snap.quiet[issue]; ok {
			secs := int64(d.Seconds())
			b.QuietSeconds = &secs
		}
		blocked = append(blocked, b)
	}

	containers := make([]statusDocContainer, 0, len(snap.queues.containers))
	for _, c := range snap.queues.containers {
		containers = append(containers, toStatusDocContainer(c))
	}

	parked := make([]statusDocParked, 0, len(snap.queues.parked))
	for _, issue := range snap.queues.parked {
		parked = append(parked, statusDocParked{
			Issue:    issue,
			Entries:  nonNilSlice(snap.parks[issue].entries),
			Category: snap.parks[issue].category,
		})
	}

	prs := make([]statusDocPR, 0, len(snap.prs))
	for _, p := range snap.prs {
		prs = append(prs, statusDocPR{
			Number:    p.number,
			Branch:    p.branch,
			Issue:     p.issue,
			URL:       p.url,
			Mergeable: mergeableCell(p),
			Checks:    checksCell(p),
			Review:    reviewCell(p),
		})
	}

	planDocs := make([]statusDocPlan, 0, len(snap.plans.docs))
	for _, d := range snap.plans.docs {
		dcontainers := make([]statusDocContainer, 0, len(d.containers))
		for _, c := range d.containers {
			dcontainers = append(dcontainers, toStatusDocContainer(c))
		}
		planDocs = append(planDocs, statusDocPlan{
			Path: d.path, State: string(d.state), Containers: nonNilSlice(dcontainers), OpenChildren: d.openChildren,
		})
	}
	gone := make([]statusDocGone, 0, len(snap.plans.gone))
	for _, g := range snap.plans.gone {
		gone = append(gone, statusDocGone{Path: g.path, Issues: nonNilSlice(g.issues), Open: len(g.openIssues)})
	}

	doc := statusDoc{
		Repo:  cfg.repo,
		Scope: statusDocScope{Label: cfg.label, Source: snap.labelSource, StrictOrder: cfg.strictOrder},
		Queue: statusDocQueue{
			Ready:           nonNilSlice(snap.queues.ready),
			HeldBack:        nonNilSlice(heldBack),
			Blocked:         blocked,
			Parked:          parked,
			Proposed:        nonNilSlice(snap.queues.proposed),
			Containers:      nonNilSlice(containers),
			OutsideGate:     nonNilSlice(snap.gate.outside),
			UngatedProposed: nonNilSlice(snap.gate.ungatedProposed),
		},
		Next:          statusDocNext{Issue: snap.next, Reason: nextLine(snap)},
		PRs:           prs,
		UndetailedPRs: nonNilSlice(snap.undetailed),
		NeedsYou:      nonNilSlice(needsYouParts(snap)),
		Notes:         nonNilSlice(snap.notes),
		Plans: statusDocPlans{
			Docs: nonNilSlice(planDocs), Gone: nonNilSlice(gone), Truncated: snap.plans.truncated,
		},
		LastShift: toStatusDocLastShift(snap.lastShift),
	}
	if line := statusPlanLine(snap); line != "" {
		doc.Plan = &line
	}
	if snap.published != "" {
		doc.Published = &snap.published
	}
	return doc
}

// nonNilSlice keeps every array field a `[]`, never a JSON `null`, so a
// script can `.[]` into any of them without special-casing the empty case.
func nonNilSlice[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
