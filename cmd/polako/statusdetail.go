package main

// The per-issue details `status` hangs on its queue rows, all from the one
// listing it already paid for (issueDetail): how long a parked or proposed
// issue has sat, and what a ready or held-back issue's own model:/effort:
// labels will make its run cost. Labels and a timestamp only — no issue text.

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// idleSpans is how long each parked and proposed issue has gone untouched,
// by GitHub's updatedAt. Those two rows only: a parked or proposed issue waits
// on a person, so its age is the thing worth seeing, while a ready issue's age
// says nothing about what a drain does next. An issue whose updatedAt did not
// read is left out rather than shown as fresh.
func idleSpans(q issueQueues, now time.Time) map[int]time.Duration {
	idle := map[int]time.Duration{}
	for _, n := range slices.Concat(q.parked, q.proposed) {
		at := q.detail[n].updated
		if at.IsZero() {
			continue
		}
		// A clock disagreement is not an update from the future — fresh, as
		// quietFor reads the same case.
		idle[n] = max(now.Sub(at), 0)
	}
	return idle
}

// idleNote is one issue's "quiet 12d", or "" with no span to show.
func (snap statusSnapshot) idleNote(n int) string {
	if d, ok := snap.idle[n]; ok {
		return "quiet " + dur(d)
	}
	return ""
}

// policyNote is one issue's own model:/effort: labels as `status` shows them
// — "opus, high" — or "" when neither family resolved. A malformed or doubled
// label resolves to nothing (parseLabelPolicy), so it shows nothing here
// either, the same fallthrough a pickup takes.
func (q issueQueues) policyNote(n int) string {
	lc := q.detail[n].policy
	var parts []string
	switch {
	case lc.modelSet && lc.model == "":
		parts = append(parts, "default model")
	case lc.modelSet:
		parts = append(parts, lc.model)
	}
	if lc.effortSet {
		parts = append(parts, lc.effort)
	}
	return strings.Join(parts, ", ")
}

// annotatedRefs is issueRefs with each issue's non-empty notes in brackets
// after it: `#9 (budget, quiet 12d)`.
func annotatedRefs(numbers []int, notes ...func(int) string) string {
	refs := make([]string, len(numbers))
	for i, n := range numbers {
		refs[i] = annotatedRef(n, notes...)
	}
	return strings.Join(refs, ", ")
}

func annotatedRef(n int, notes ...func(int) string) string {
	var parts []string
	for _, note := range notes {
		if s := note(n); s != "" {
			parts = append(parts, s)
		}
	}
	ref := "#" + strconv.Itoa(n)
	if len(parts) > 0 {
		ref += " (" + strings.Join(parts, ", ") + ")"
	}
	return ref
}

func queueLine(q issueQueues) string {
	if len(q.ready) == 0 {
		return "no issue is workable right now"
	}
	return fmt.Sprintf("%s — %s", plural(len(q.ready), "issue"), annotatedRefs(q.ready, q.policyNote))
}

// heldBackLine renders the held-back row: every otherwise-ready issue this
// pass put down for an open blockedBy dependency, and what's holding each
// one — the same wording logHeldBack (drain.go) narrates per-issue, folded
// into one row here.
func heldBackLine(q issueQueues) string {
	refs := make([]string, len(q.heldBack))
	for i, h := range q.heldBack {
		behind := "behind " + issueRefs(h.blockers)
		refs[i] = annotatedRef(h.number, func(int) string { return behind }, q.policyNote)
	}
	return fmt.Sprintf("%s — %s", plural(len(q.heldBack), "issue"), strings.Join(refs, ", "))
}

// statusDocDetail is one issue's entry in `status -json`'s queue.details: the
// same facts the text rows annotate, for the same issues. QuietSeconds is a
// pointer for the reason statusDocBlocked's is; Model is "default" for
// model:default, the account's own default.
type statusDocDetail struct {
	Issue        int    `json:"issue"`
	QuietSeconds *int64 `json:"quiet_seconds,omitempty"`
	Model        string `json:"model,omitempty"`
	Effort       string `json:"effort,omitempty"`
}

// statusDocDetails lists, ascending, every issue a text row annotates —
// parked and proposed with their idle span, ready and held back with their
// labels — and leaves out any with nothing to say.
func statusDocDetails(snap statusSnapshot) []statusDocDetail {
	q := snap.queues
	var out []statusDocDetail
	for n, d := range snap.idle {
		secs := int64(d.Seconds())
		out = append(out, statusDocDetail{Issue: n, QuietSeconds: &secs})
	}
	workable := slices.Clone(q.ready)
	for _, h := range q.heldBack {
		workable = append(workable, h.number)
	}
	for _, n := range workable {
		lc := q.detail[n].policy
		d := statusDocDetail{Issue: n, Model: lc.model, Effort: lc.effort}
		if lc.modelSet && lc.model == "" {
			d.Model = "default"
		}
		if d.Model != "" || d.Effort != "" {
			out = append(out, d)
		}
	}
	slices.SortFunc(out, func(a, b statusDocDetail) int { return a.Issue - b.Issue })
	return nonNilSlice(out)
}
