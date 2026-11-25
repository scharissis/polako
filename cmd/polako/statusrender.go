package main

// `status`'s text report: the snapshot readStatus (status.go) derived, turned
// into the rows, the PR table and the closing needs-you line. The JSON
// rendering (statusDoc and its second-renderer derivation) lives in
// statusjson.go.

import (
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
)

func renderStatus(w io.Writer, rpt report, cfg config, snap statusSnapshot) {
	fmt.Fprintf(w, "%s\n", rpt.bold(fmt.Sprintf("%s%s", cfg.repo, statusScope(cfg, snap.labelSource))))
	if line := updateAvailableLine(snap.selfVersion, cfg.pluginVersion, snap.published); line != "" {
		fmt.Fprintf(w, "%s\n", line)
	}
	for _, note := range snap.notes {
		fmt.Fprintf(w, "%s\n", note)
	}
	printPairs(w, rpt, "", queuePairs(snap))
	printStatusPRs(w, rpt, snap)
	printPlanDocs(w, rpt, snap.plans)
	if line := lastShiftLine(snap.runData.lastShift); line != "" {
		fmt.Fprintf(w, "%s\n", line)
	}
	if line := statusPlanLine(snap); line != "" {
		fmt.Fprintf(w, "%s\n", line)
	}
	if line := needsYou(snap); line != "" {
		fmt.Fprintf(w, "\n%s\n", rpt.bold(line))
	}
}

// statusPlanLine is the same row usageLine builds for work's startup
// banner, reused rather than re-derived: the "second renderer, not a second
// pipeline" rule this file already applies to statusDocFrom, held to a
// single fact source here too.
func statusPlanLine(snap statusSnapshot) string {
	if snap.usage == nil {
		return ""
	}
	return usageLine(*snap.usage)
}

// statusScope names what narrowed or reordered the report, so a snapshot that
// covers less than the whole backlog cannot be read as one that covers all of
// it — the flags are settable from the environment, and one forgotten in a
// profile is otherwise invisible here. source is snap.labelSource: a label
// this run discovered itself (source == "github") is named as such, since
// that's a fact about the repository nobody typed on this invocation.
func statusScope(cfg config, source string) string {
	var parts []string
	if cfg.label != "" {
		label := "issues labelled " + cfg.label
		if source == "github" {
			label += " (gate label, read from GitHub)"
		}
		parts = append(parts, label)
	}
	if cfg.strictOrder {
		parts = append(parts, "-strict-order")
	}
	if len(parts) == 0 {
		return ""
	}
	return " — " + strings.Join(parts, ", ")
}

func queuePairs(snap statusSnapshot) [][2]string {
	q := snap.queues
	if len(q.open()) == 0 && !snap.gate.open() {
		return [][2]string{
			{"queue", "nothing open — a shift starting now would find the backlog cleared"},
			{"next", nextLine(snap)},
		}
	}
	pairs := [][2]string{{"ready", queueLine(q) + snap.runData.readySuffix(q.ready, snap.prs)}}
	if len(q.heldBack) > 0 {
		pairs = append(pairs, [2]string{"held back", heldBackLine(q)})
	}
	if len(q.blocked) > 0 {
		pairs = append(pairs, [2]string{"awaiting you",
			fmt.Sprintf("%s — %s", plural(len(q.blocked), "issue"), annotatedRefs(q.blocked, snap.quietNote))})
	}
	if len(q.parked) > 0 {
		pairs = append(pairs, [2]string{"parked",
			fmt.Sprintf("%s — %s, labelled %s", plural(len(q.parked), "issue"),
				annotatedRefs(q.parked, snap.runData.parkReason, snap.idleNote), needsHumanLabel)})
	}
	// The curation gate and the containers, said here rather than left to be
	// inferred from an issue's absence: a batch of proposals nobody has looked at
	// is exactly the thing a snapshot exists to surface.
	if len(q.proposed) > 0 {
		pairs = append(pairs, [2]string{"proposed",
			fmt.Sprintf("%s — %s, labelled %s", plural(len(q.proposed), "issue"),
				annotatedRefs(q.proposed, snap.idleNote), proposedLabel)})
	}
	if len(q.design) > 0 {
		pairs = append(pairs, [2]string{"design", designLine(q.design)})
	}
	if len(q.containers) > 0 {
		pairs = append(pairs, [2]string{"containers",
			fmt.Sprintf("%s — %s", plural(len(q.containers), "issue"), containerRefs(q.containers))})
	}
	if len(snap.gate.outside) > 0 {
		pairs = append(pairs, [2]string{"outside the gate", outsideGateLine(snap.gate.outside)})
	}
	// Last, because it is the answer the rest of the table is context for.
	pairs = append(pairs, [2]string{"next", nextLine(snap)})
	return pairs
}

// containerRefs renders each container with its sub-issue rollup, so the
// containers row tells a finished epic — every child closed — from one still in
// progress, and a finished one polako will close on its next pass from one a
// human has held open with needs-human or proposed.
func containerRefs(containers []containerInfo) string {
	refs := make([]string, len(containers))
	for i, c := range containers {
		if c.closed {
			// Already closed — not a thing left for a shift or a human to
			// close, so none of the finished/held wording below applies.
			refs[i] = fmt.Sprintf("#%d (closed)", c.number)
			continue
		}
		ref := fmt.Sprintf("#%d (%d/%d closed", c.number, c.completed, c.total)
		switch {
		case c.finished() && c.held:
			ref += " — yours to close"
		case c.finished():
			ref += " — the next shift closes it"
		}
		refs[i] = ref + ")"
	}
	return strings.Join(refs, ", ")
}

// nextLine says what a drain starting now would do first. The cases are the
// drain's own, in the drain's own order: nothing to do at all, else restart
// safety — an existing PR means the skill is never re-run, whichever queue the
// issue came out of — else work the lowest ready issue, or, with none, run the
// lowest issue waiting on an answer to find out whether the reply is already on
// the thread.
//
// Restart safety comes before the awaiting-answer wording because processIssue
// puts it first: a flagged issue whose branch already carries a PR is
// supervised, not re-run.
func nextLine(snap statusSnapshot) string {
	if snap.next == 0 {
		// Which exclusion is holding the backlog matters, because each one is
		// released differently — and naming the wrong one sends an operator to
		// take off a label the issues do not carry.
		var held []string
		if len(snap.queues.heldBack) > 0 {
			held = append(held, "held back")
		}
		if len(snap.queues.parked) > 0 {
			held = append(held, "parked")
		}
		if len(snap.queues.proposed) > 0 {
			held = append(held, "awaiting curation")
		}
		if len(snap.queues.design) > 0 {
			held = append(held, "a design request")
		}
		if len(snap.queues.containers) > 0 {
			held = append(held, "a tracking container")
		}
		if len(snap.gate.outside) > 0 || snap.gate.held > 0 {
			held = append(held, "outside the gate")
		}
		if len(held) == 0 {
			if snap.fillHint != "" {
				return "nothing — no open issue at all; " + snap.fillHint
			}
			return "nothing — no open issue at all"
		}
		return "nothing — every open issue is " + strings.Join(held, " or ")
	}
	if pr := findPR(snap, snap.next); pr != nil {
		return fmt.Sprintf("#%d — its branch already has PR #%d, so it would wait on that rather "+
			"than run the skill again", snap.next, pr.number)
	}
	if slices.Contains(snap.queues.blocked, snap.next) {
		why := "nothing else is workable"
		if snap.strictOrder {
			// It is not that nothing else is workable — issues behind it are.
			// -strict-order is why they wait.
			why = "-strict-order holds the queue behind it"
		}
		return fmt.Sprintf("#%d — %s, so it would re-run that issue "+
			"to see whether your reply is on the thread", snap.next, why)
	}
	return fmt.Sprintf("#%d", snap.next)
}

func findPR(snap statusSnapshot, issue int) *statusPR {
	if i := slices.IndexFunc(snap.prs, func(p statusPR) bool { return p.issue == issue }); i >= 0 {
		return &snap.prs[i]
	}
	return nil
}

func printStatusPRs(w io.Writer, rpt report, snap statusSnapshot) {
	if len(snap.prs) == 0 {
		return
	}
	rows := make([][]string, 0, len(snap.prs))
	for _, p := range snap.prs {
		rows = append(rows, []string{
			"#" + strconv.Itoa(p.number), p.branch, "#" + strconv.Itoa(p.issue),
			mergeableCell(p), checksCell(p), reviewCell(p), p.url,
		})
	}
	// Every column left-aligned: these are names and states, not figures, and a
	// right-aligned URL is a column nobody can scan.
	header := []string{"pr", "branch", "issue", "mergeable", "checks", "review", "url"}
	printTable(w, rpt, "open prs on issue branches", header, rows, len(header))
	if len(snap.undetailed) > 0 {
		fmt.Fprintf(w, "  (%s past the first %d, listed without state: %s)\n",
			plural(len(snap.undetailed), "PR"), statusPRs, issueRefs(snap.undetailed))
	}
}

// unknownCell is what every column of a PR whose state was not read says. It is
// deliberately not "none" or "passing": nobody looked, and a snapshot must not
// invent the answer it went there for.
const unknownCell = "not read"

func mergeableCell(p statusPR) string {
	if !p.detailed {
		return unknownCell
	}
	if p.view.mergeable == "" {
		return "unknown"
	}
	return strings.ToLower(p.view.mergeable)
}

func checksCell(p statusPR) string {
	if !p.detailed {
		return unknownCell
	}
	if p.view.checks == checksFailing {
		return fmt.Sprintf("%s (%s)", p.view.checks, strings.Join(p.view.failing, ", "))
	}
	return p.view.checks
}

// reviewCell says where a PR's reviews stand, in every case — including the
// one prView.reviewNote leaves blank, because supervisePR is about to log a
// remediation for it and has somewhere else to say so. A snapshot has nowhere
// else.
func reviewCell(p statusPR) string {
	switch {
	case !p.detailed:
		return unknownCell
	case !p.view.changesRequested && p.view.outsiderRequest:
		return outsiderNote
	case !p.view.changesRequested:
		return "clear"
	case p.view.reviewOutstanding():
		return "changes requested"
	case p.view.reviewedAt.IsZero():
		// reviewDecision said so and no individual review did, so there is no
		// date to hold the branch against — a block, without a claim about
		// whether anyone has answered it.
		return "changes requested"
	default:
		return "answered, awaiting re-review"
	}
}

// needsYou is the closing line: the things on GitHub that only a person can
// move, in the order they are usually dealt with. Absent when there are none,
// because a line that reads "needs you: nothing" on every healthy backlog is
// one an operator learns to skip past.
func needsYou(snap statusSnapshot) string {
	parts := needsYouParts(snap)
	if len(parts) == 0 {
		return ""
	}
	return "needs you: " + strings.Join(parts, "; ")
}

// parkNeedsYouClause is the short "why it parked" phrase the needs-you line
// uses per park category (metrics.go) — distinct from parkNextStepTable
// (unpark_work.go), which is a "what to do" sentence for unpark's own
// single-issue view. This line already ends "— polako unpark N", so it only
// has to say what happened, terse enough to sit beside other issues' own
// clauses on one line. parkPermission's entry is used only when the park
// named no grantable entry — a granted one keeps needsYouParts's own
// existing "grant ..." wording instead. A category missing here is a bug:
// TestParkNeedsYouClauseCoversEveryCategory walks parkReasonOrder and fails
// if one turns up without a phrase.
var parkNeedsYouClause = map[string]string{
	parkBudget:     "hit its time or cost cap",
	parkRetries:    "kept crashing and ran out of retries",
	parkNothing:    "ran clean but left nothing behind",
	parkNoSkill:    "asked for a skill this install doesn't have",
	parkAuth:       "hit an API auth failure",
	parkPermission: "was refused a permission",
	parkConflicts:  "gave up resolving a merge conflict",
	parkChecks:     "gave up on a failing check",
	parkReview:     "gave up resolving review feedback",
	parkPRState:    "left its PR in a state polako doesn't know",
	parkPRClosed:   "had its PR closed without merging",
	parkUnknown:    "parked for a reason polako couldn't name",
}

// needsYouParts is needsYou's derivation on its own, one clause per item, so
// the JSON renderer can carry the same list structured rather than joined
// into prose — the "second renderer, not a second pipeline" rule applied to
// this line specifically.
func needsYouParts(snap statusSnapshot) []string {
	var parts []string
	if len(snap.queues.blocked) > 0 {
		parts = append(parts, "reply on "+issueRefs(snap.queues.blocked))
	}
	var mergeable, stuck []string
	for _, p := range snap.prs {
		switch {
		case !p.detailed:
		case p.view.remediable():
			// A drain would dispatch a run at this, so it is not yours yet.
		case p.view.checks == checksHuman:
			stuck = append(stuck, "#"+strconv.Itoa(p.number))
		case p.view.checks != checksPending:
			mergeable = append(mergeable, "#"+strconv.Itoa(p.number))
		}
	}
	if len(mergeable) > 0 {
		parts = append(parts, "review and merge PR "+strings.Join(mergeable, ", "))
	}
	if len(stuck) > 0 {
		parts = append(parts, "approve the checks waiting on you on PR "+strings.Join(stuck, ", "))
	}
	// A parked issue whose own park comment named entries a rerun could grant
	// gets its own clause naming them, whatever its category — including a
	// pre-#530 comment with a Refused: footer but no Park: one, category
	// "". Otherwise, a named category gets its parkNeedsYouClause phrase. A
	// park with no entries and no category — no comment of polako's own to
	// read, or a hand label — keeps today's batched clause. snap.parks is
	// nil (not just empty) when the read itself failed, and a nil map's
	// lookups all miss the same way an empty one's would, so every parked
	// issue falls back to the batched clause. One pass decides both lists,
	// so the two clauses can never classify the same issue two different
	// ways. A parked design request always gets its own clause
	// (parkedDesignClause), since dropping the label requeues nothing.
	var undecided []int
	var perIssue []string
	for _, issue := range snap.queues.parked {
		it, ok := snap.parks[issue]
		switch {
		case !ok:
			undecided = append(undecided, issue)
		case it.design:
			perIssue = append(perIssue, parkedDesignClause(it))
		case len(it.entries) > 0:
			perIssue = append(perIssue, fmt.Sprintf("grant %s or fix the skill, then polako unpark #%d",
				strings.Join(it.entries, ", "), issue))
		case it.category != "":
			if clause, ok := parkNeedsYouClause[it.category]; ok {
				perIssue = append(perIssue, fmt.Sprintf("#%d %s — polako unpark %d", issue, clause, issue))
			} else {
				// A category readStatus recognized (parseParkCategory only
				// returns one from parkReasonOrder) but this table hasn't
				// caught up with — the completeness test should catch this
				// first, but the batched clause is still a truthful fallback.
				undecided = append(undecided, issue)
			}
		default:
			undecided = append(undecided, issue)
		}
	}
	if len(undecided) > 0 {
		parts = append(parts, fmt.Sprintf("decide what to do about %s (drop %s to requeue)",
			issueRefs(undecided), needsHumanLabel))
	}
	parts = append(parts, perIssue...)
	// Curation is a person's job by construction — nothing else takes the label
	// off — so a backlog of proposals is one of the things only a person moves.
	parts = append(parts, curateClauses(snap)...)
	parts = append(parts, designClauses(snap.queues.design)...)
	// A finished container polako would close itself, so it is not yours — but
	// one a human has held with needs-human or proposed it will not touch, and
	// that one is now the operator's to close.
	var heldEpics []int
	for _, c := range snap.queues.containers {
		if c.finished() && c.held {
			heldEpics = append(heldEpics, c.number)
		}
	}
	if len(heldEpics) > 0 {
		parts = append(parts, fmt.Sprintf("close %s (every sub-issue closed; held open by %s or %s)",
			issueRefs(heldEpics), needsHumanLabel, proposedLabel))
	}
	return append(parts, planDocClauses(snap.plans)...)
}
