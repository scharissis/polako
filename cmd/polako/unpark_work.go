package main

// Reading and rendering what unpark shows about a parked issue's branch, PR
// and CI — split out of unpark.go to keep that file from growing past the
// repo's own median file length one feature at a time (issue #531).

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// --- reading work: branch, PR and CI ---

// parkWork is what unpark shows about a parked issue's branch, PR and CI —
// the first thing a human checks about a park: is there already a PR, is it
// green, or is there nothing pushed at all. read is false when the chain of
// gh calls below could not finish — the row still renders, as "not read",
// the same best-effort rule readParkListItem follows for reason.
//
// GitHub only, on purpose: local, unpushed work is a separate concern (a
// later child of #529) that would need the worktree on this machine, which
// a read-only listing has no business assuming exists.
type parkWork struct {
	read     bool
	branch   string
	prNumber int
	prURL    string
	prState  string   // the PR's own state (OPEN, CLOSED, MERGED); "" when prNumber == 0
	checks   string   // one of the checks* verdicts (pr.go), "" when unread or the PR isn't OPEN
	failing  []string // the checks that earned checksFailing
	onOrigin bool     // only meaningful when prNumber == 0
	ahead    int      // commits ahead of the default branch; only meaningful when onOrigin
}

// readParkWork reads one parked issue's branch: prForBranch first, and with
// an OPEN PR, its checks too — retried the same way every other read in this
// chain is, so a transient failure there reads as "not read" rather than as
// a silently green PR. With no PR, def (the repository's default branch,
// read once per listing by readParkedIssues) decides whether origin has the
// branch at all and how far ahead of def it sits.
//
// A PR that isn't OPEN (closed without merging — parkPRClosed's own case —
// or merged) is read no further: its checks are not what a human is
// deciding on, and the reason column already says why the issue parked.
func readParkWork(ctx context.Context, cfg config, issue int, def string) parkWork {
	branch := fmt.Sprintf("%s%d", cfg.branchPrefix, issue)
	w := parkWork{branch: branch}
	pr, err := prForBranch(ctx, cfg, branch)
	if err != nil {
		return w
	}
	if pr != nil {
		w.prNumber, w.prURL, w.prState = pr.Number, pr.URL, pr.State
		if pr.State != "OPEN" {
			w.read = true
			return w
		}
		view, verr := retryRead(ctx, cfg, fmt.Sprintf("reading PR #%d", pr.Number),
			func() (prView, error) { return prStatus(ctx, cfg, pr.Number) })
		if verr != nil {
			return w
		}
		w.read, w.checks, w.failing = true, view.checks, view.failing
		return w
	}
	if def == "" {
		// The default-branch read already failed for the whole listing —
		// nothing left to compare against.
		return w
	}
	ahead, onOrigin, err := branchAheadOfDefault(ctx, cfg, def, branch)
	if err != nil {
		return w
	}
	w.read, w.onOrigin, w.ahead = true, onOrigin, ahead
	return w
}

// branchAheadOfDefault reads how far branch is ahead of def on origin. The
// gh CLI has no subcommand for a compare, only `gh api`; a 404 answers "no
// branch on origin" definitively, the same shape labelExists (labels.go)
// turns its own 404 into (false, nil) before retryRead ever sees it as
// something to retry.
func branchAheadOfDefault(ctx context.Context, cfg config, def, branch string) (ahead int, onOrigin bool, err error) {
	type compareResult struct {
		ahead    int
		onOrigin bool
	}
	r, err := retryRead(ctx, cfg, "comparing "+branch+" to "+def, func() (compareResult, error) {
		out, err := gh(ctx, cfg, "api",
			fmt.Sprintf("repos/{owner}/{repo}/compare/%s...%s", def, branch), "--jq", ".ahead_by")
		if err == nil {
			n, perr := strconv.Atoi(strings.TrimSpace(string(out)))
			if perr != nil {
				return compareResult{}, perr
			}
			return compareResult{ahead: n, onOrigin: true}, nil
		}
		if isNotFoundError(err) {
			return compareResult{}, nil
		}
		return compareResult{}, err
	})
	return r.ahead, r.onOrigin, err
}

// unparkDefaultBranch reads the repository's default branch name — the base
// half of the compare readParkWork uses for a pushed branch with no PR yet.
// Best-effort like ghViewerLogin: a failure here is reported by returning it,
// and readParkedIssues treats it the same way it treats any other read that
// could not finish — the listing still runs, minus the one thing it enabled.
func unparkDefaultBranch(ctx context.Context, cfg config) (string, error) {
	return retryRead(ctx, cfg, "reading the repository's default branch", func() (string, error) {
		out, err := gh(ctx, cfg, "api", "repos/{owner}/{repo}", "--jq", ".default_branch")
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(out)), nil
	})
}

// --- next shift ---

// pr reconstructs the *pullRequest waitsOnPR expects from parkWork's own
// fields — nil when there is none, the same shape prForBranch itself
// returns.
func (w parkWork) pr() *pullRequest {
	if w.prNumber == 0 {
		return nil
	}
	return &pullRequest{Number: w.prNumber, State: w.prState, URL: w.prURL}
}

// nextShiftLine describes what clearing needs-human actually does, using
// processIssue's own restart-safety call (waitsOnPR, issue.go) on the same
// PR this row already read — never a second copy of that rule. With no PR,
// it falls back to what the branch itself shows: commits already on origin
// to resume, local work on this machine that never reached origin, or
// nothing at all, meaning a fresh run. A row that couldn't be read at all
// (w.read false) says so, the same as parkWorkSummary and
// renderParkWorkDetail — a failed GitHub read is unknown state, not evidence
// nothing was pushed. local is the zero value unless readParkedIssues was
// asked to read it (issue #534) — a zero value never
// satisfies salvageable(), so this falls through to today's wording exactly
// as before wherever local work wasn't read.
func nextShiftLine(w parkWork, local leftWork) string {
	if !w.read {
		return "not read"
	}
	if pr := w.pr(); waitsOnPR(pr) {
		line := fmt.Sprintf("waits on PR #%d", pr.Number)
		if w.checks == checksFailing {
			// readParkWork only ever sets w.checks once it has confirmed the
			// PR is OPEN (it returns early otherwise), so this already implies
			// pr.State == "OPEN" without saying so again.
			line += " and remediates its red CI"
		}
		return line
	}
	if w.onOrigin && w.ahead > 0 {
		return fmt.Sprintf("resumes %s from its %s", w.branch, plural(w.ahead, "commit"))
	}
	if local.salvageable() {
		return "resumes from the local worktree"
	}
	return "starts over — nothing was pushed"
}

// designRunCommand is the verb that works a design request. The `work`
// queue leaves design issues out, so for one of those this, not a shift, is
// what clearing needs-human hands the issue back to.
func designRunCommand(issue int) string {
	return fmt.Sprintf("polako design -issue %d", issue)
}

// parkNextShift is nextShiftLine for one listed item: a design issue's line
// names designRunCommand as the subject, since no `work` shift will act on
// it. "not read" stays as it is — there is no action to attribute.
func parkNextShift(it parkListItem) string {
	line := nextShiftLine(it.work, it.local)
	if !it.design || !it.work.read {
		return line
	}
	return designRunCommand(it.issue) + " " + line
}

// --- next step ---

// parkNextStepTable maps each park category (metrics.go) to the one-sentence
// next step issue #533 calls for — what a human should do
// about this park, replacing the generic "fix what it names" that only ever
// made sense for a permission park with named entries. A category missing
// here is a bug: TestParkNextStepCoversEveryCategory walks parkReasonOrder
// and fails if any of them turns up without a sentence, so a new category
// can't ship without one.
var parkNextStepTable = map[string]string{
	parkBudget:    "the work is on the branch — finish it by hand, or rerun with a higher -max-issue-time or -max-cost",
	parkChecks:    "fix the failing check on the PR, then unpark",
	parkConflicts: "resolve the conflict on the PR, then unpark",
	parkReview:    "resolve the review on the PR, then unpark",
	parkNothing:   "check the issue says what to change, then unpark to retry",
	parkRetries:   "check claude runs at all on this machine, then unpark to retry",
	parkPRClosed:  "reopen or delete the PR, then unpark",
	parkPRState:   "reopen or delete the PR, then unpark",
	parkNoSkill:   "read the thread — these end a shift, they rarely park",
	parkAuth:      "read the thread — these end a shift, they rarely park",
	parkUnknown:   "read the thread — these end a shift, they rarely park",
}

// parkNextStepNoEntries is parkPermission's sentence when its park comment
// named no -add-tools entries — the fallback reason ran, or the ask was
// prose (the #461/#472 shapes), so there is nothing to grant.
const parkNextStepNoEntries = "check that shift's log or the thread for what it refused"

// parkNextStepUngrantable is parkPermission's sentence when the comment
// named an entry, but validParkEntry rejected it (it.ignored, not
// it.entries) — a malformed shape, or a never-grant command entry's own
// producer already refused. Distinct from parkNextStepNoEntries: something
// was named, it just isn't something -add-tools can grant.
const parkNextStepUngrantable = "the named entry can't be granted automatically — read the thread"

// parkNextStepUnlabeled is what an issue with no category at all gets: a
// hand-applied label, or a comment readParkListItem could not parse as
// polako's own. Shorter than the table's "these end a shift" framing, since
// there is no run behind this one to point at.
const parkNextStepUnlabeled = "read the thread"

// parkNextStep is the sentence unpark prints for a parked issue: what a
// human should do about it, in one short sentence. permission_refused is the
// one category that reads more than its own category — a granted, valid
// entry (it.entries) means rerunning with -add-tools; an entry the comment
// named but validParkEntry rejected (it.ignored) can never be granted, so it
// says so instead of claiming there's nothing named at all; no entry either
// way falls back to parkNextStepNoEntries. No category — a hand label, or an
// unparsed comment — falls back to parkNextStepUnlabeled.
func parkNextStep(it parkListItem) string {
	if it.category == parkPermission {
		switch {
		case len(it.entries) > 0:
			return "grant them, then rerun with -add-tools"
		case len(it.ignored) > 0:
			return parkNextStepUngrantable
		default:
			return parkNextStepNoEntries
		}
	}
	if s, ok := parkNextStepTable[it.category]; ok {
		return s
	}
	return parkNextStepUnlabeled
}

// staleRedCIWarning flags a checks-remediation park (parkChecks) whose CI is
// still failing right now: the branch hasn't moved since it parked, so
// clearing needs-human without touching it sends the next shift straight
// back into the same remediation loop that just gave up.
func staleRedCIWarning(category string, w parkWork) string {
	if category == parkChecks && w.checks == checksFailing {
		return "the next shift will remediate the same red and likely park again — fix the branch first"
	}
	return ""
}

// --- rendering work ---

// parkWorkSummary is the table's clipped work cell: whether there's a PR and
// whether its CI is red, or, with no PR, whether the branch reached origin
// at all and how far ahead it is.
func parkWorkSummary(w parkWork) string {
	switch {
	case !w.read:
		return "not read"
	case w.prNumber != 0 && w.prState != "OPEN":
		return fmt.Sprintf("PR #%d (%s)", w.prNumber, strings.ToLower(w.prState))
	case w.prNumber != 0:
		if w.checks == checksFailing {
			return fmt.Sprintf("PR #%d, CI red", w.prNumber)
		}
		return fmt.Sprintf("PR #%d", w.prNumber)
	case w.onOrigin:
		return fmt.Sprintf("%s, %s, no PR", w.branch, plural(w.ahead, "commit"))
	default:
		return "nothing pushed"
	}
}

// renderParkWorkDetail is the one-issue view's expansion of parkWorkSummary:
// the PR link and any failing check names, or the branch and its commit
// count when there's no PR yet.
func renderParkWorkDetail(w io.Writer, rpt report, work parkWork) {
	switch {
	case !work.read:
		fmt.Fprintf(w, "  %s  not read\n", rpt.dim("work     "))
	case work.prNumber != 0 && work.prState != "OPEN":
		fmt.Fprintf(w, "  %s  %s (%s)\n", rpt.dim("pr       "), work.prURL, strings.ToLower(work.prState))
	case work.prNumber != 0:
		fmt.Fprintf(w, "  %s  %s\n", rpt.dim("pr       "), work.prURL)
		if len(work.failing) > 0 {
			fmt.Fprintf(w, "  %s  %s\n", rpt.dim("checks   "), strings.Join(work.failing, ", "))
		}
	case work.onOrigin:
		fmt.Fprintf(w, "  %s  %s, %s, no PR\n", rpt.dim("branch   "), work.branch, plural(work.ahead, "commit"))
	default:
		fmt.Fprintf(w, "  %s  nothing pushed\n", rpt.dim("branch   "))
	}
}

// renderLocalWork is the one-issue view's account of what inspectLeftWork
// found on this machine's disk — commits or edits that never reached origin,
// which nothing above (a GitHub read) can see. Terminal only, like the rest
// of this view: local.path is an absolute path on the operator's own disk
// and goes nowhere else. Nothing prints when local is the zero value —
// -repo was given, so readParkedIssues never called inspectLeftWork — or
// when there is nothing new to say: a fully pushed branch's commits are
// already covered by renderParkWorkDetail above, so they're only repeated
// here unpushed.
func renderLocalWork(w io.Writer, rpt report, local leftWork) {
	if !local.salvageable() {
		return
	}
	var parts []string
	if local.commits > 0 && !local.pushed {
		parts = append(parts, plural(local.commits, "commit")+" not pushed")
	}
	if local.dirty > 0 {
		parts = append(parts, plural(local.dirty, "file")+" uncommitted")
	}
	if len(parts) == 0 {
		return
	}
	line := strings.Join(parts, ", ")
	if local.path != "" {
		line += ", in " + local.path
	}
	fmt.Fprintf(w, "  %s  %s\n", rpt.dim("local    "), line)
}
