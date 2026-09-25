package main

// processIssue advances one issue as far as it will go: to merged, to a park,
// or to a question a human owes an answer to. It owns the resume policy — the
// two ceilings below, one for crash loops and one for the pricier clean-exit
// resumes — and dispatches each run through runClaude.
//
// Its loop has two arms. dispatchRun is the one taken while no PR exists yet:
// it runs Claude and classifies what came back, through the per-attempt
// helpers on runAttempt (attempt.go). superviseToClose is the other: a PR
// exists, so wait it out to merged or parked. resumeLedger is the
// interlocking retry counters both the crash arm and the clean-exit arm read,
// plus the one non-counter detail a worked-around refusal needs carried
// across a resume.

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// defaultResumeCeiling bounds how many times one issue may be resumed in total,
// however much each of those runs got done. -retries bounds the consecutive
// fruitless ones, which is the loop worth giving up on quickly; this is the
// backstop for the other shape, a run that gets a little further every time,
// dies again, and so keeps resetting the counter that was supposed to stop it.
//
// Crude and generous on purpose. What one issue may really consume is -max-cost
// and -max-issue-time, and #7 is where a proper ceiling belongs; this only has
// to guarantee the loop ends.
const defaultResumeCeiling = 20

// cleanExitResumeCeiling bounds the other flavour of resume: a run that ended
// cleanly, opened no PR, and left work on disk anyway. Small on purpose, and
// not an operator knob.
//
// Every attempt of this kind burns a *complete* run rather than failing fast in
// seconds the way a crash loop does — the run this exists for cost $3.61 and
// eight minutes. -max-cost is off by default, and -max-issue-time's own
// default (45m) sums across every resume of the issue, so it would eventually
// catch this loop too — but not fast: two 8-minute attempts is nowhere near
// 45m, so this ceiling is still the one that actually stops it quickly. Two
// buys the case worth buying, a run that was one commit from done, and
// refuses to fund the other one.
const cleanExitResumeCeiling = 2

// resumeLedger is the state machine processIssue's loop runs on: across the
// resumes of one issue it decides both whether the next trip round the loop
// resumes the dead session and what that resumed run is told.
//
// Two counters because they guard two different failures: a session that dies
// straight back on every resume, and a run that inches a little further each
// time and never arrives. cleanResumes is a third bound again, over the
// resumes of the second kind, because those are the expensive ones — an issue
// alternating crashes and clean exits must not farm two ceilings.
type resumeLedger struct {
	// fruitless counts consecutive crashes that got nothing done, and is what
	// -retries bounds. It is zeroed by a crash that got work done.
	fruitless int
	// resumes counts every retry this issue has had, fruitful ones included,
	// and is what resumeCeiling bounds.
	resumes int
	// cleanResumes counts only the resumes of the second kind — a run that
	// ended cleanly with no PR and work on disk — and is bounded separately,
	// because those are the expensive ones. They spend the shared budget too.
	cleanResumes int
	// everProgressed tracks, across every resume counted by resumes, whether
	// any of them ever produced real work — unlike fruitless, it is never
	// reset. Its one read site picks the give-up message's clause.
	everProgressed bool
	// kind is which sort of resume the next trip round the loop is, "" for
	// none. It decides both that the session is worth resuming and what the
	// resumed run is told, and neither counter can answer it — fruitless is
	// zeroed by a crash that got work done, and reading the resume off it
	// would silently turn every such retry into a fresh run that threw the
	// crashed session away. One field rather than a bool per flavour: the
	// kinds are exclusive, and a second bool is one more thing every place
	// that clears the first has to remember.
	kind string
	// deferred carries a worked-around refusal's record (issue #461) across
	// the resume afterCleanExit let it try instead of parking straight away.
	// A later run of this same issue may open a PR — then this is never read
	// again — or end with no PR of its own, with or without a fresh refusal;
	// either way the eventual park still blames the original refusal rather
	// than reporting "produced nothing" or the bound that actually stopped
	// the resuming. Zero means no such refusal is pending. Cleared by
	// clearRetries: a fresh start (a reply folded in) means whatever comes
	// next is answering a different question, not still working around this
	// refusal, so a later unrelated park must not keep blaming it (issue
	// #461's own review — a -strict-order question answered mid-issue reuses
	// this same ledger for the run after it).
	deferred deferredRefusal
}

// deferredRefusal is what afterCleanExit records once a worked-around
// refusal (issue #461) lets a resume try to finish the job — the raw
// refusals themselves, not a rendering of them, so a later run on this same
// issue (recovered or not) always has the full history to report from,
// never only its own. Issue #432:
// keeping only rendered text here is what let an early draft of this ticket
// drop a deferred refusal's history the moment a later resume hit a fresh
// refusal it did *not* recover from — exactly the "#390 drew five, only one
// reported" bug across a resume boundary instead of within one run.
type deferredRefusal struct {
	refusals []refusal // every refusal from every worked-around run on this issue so far
	message  string    // the most recent worked-around run's own clipped final words
}

// permissionParkRefusals is the full refusal history a permission park
// should report: any refusal deferred from an earlier worked-around run on
// this issue, plus this run's own. A later run's own refusal — recovered or
// not — must never silently drop an earlier one's.
func permissionParkRefusals(deferred deferredRefusal, rep runReport) []refusal {
	if len(deferred.refusals) == 0 {
		return rep.refusals
	}
	return append(append([]refusal{}, deferred.refusals...), rep.refusals...)
}

// deferredRefusalAside renders a deferred refusal's terminal-only detail for
// a park's aside: every command drawn across every run that deferred, and,
// when there is one, the most recent worked-around run's own clipped final
// words — #390's shape, where that sentence (an unreachable SSH agent) was
// the actual tell.
func deferredRefusalAside(dp deferredRefusal) string {
	aside := "refused: " + clip(joinRefusalDetails(dp.refusals), 400)
	if dp.message != "" {
		aside += " — final message: " + clip(dp.message, 200)
	}
	return aside
}

// clearRetries is what a fresh start does: a reply arrived, or a PR opened, so
// nothing is owed a resume, the crash budget starts over, and a refusal this
// issue was resuming past is no longer this run's to blame.
func (l *resumeLedger) clearRetries() {
	l.fruitless, l.kind, l.deferred = 0, "", deferredRefusal{}
}

// noteCrashResume books a crash-driven resume: another retry, resuming the
// dead session by id. progressed is rep.progressed() — a run that did real
// work before it died was not the crash loop -retries exists to stop, so it
// starts that budget over rather than spending it, and is remembered as
// having gotten somewhere.
func (l *resumeLedger) noteCrashResume(progressed bool) {
	l.resumes++
	l.kind = reasonResume
	if progressed {
		l.fruitless = 0
		l.everProgressed = true
	} else {
		l.fruitless++
	}
}

// noteCleanResume books the pricier kind: a run that ended its turn cleanly
// with no PR but left salvageable work on disk. That work is itself the
// evidence progressed() is a proxy for — stronger, since it is what a human
// would check by hand — so everProgressed is set here too.
func (l *resumeLedger) noteCleanResume() {
	l.resumes++
	l.cleanResumes++
	l.everProgressed = true
	l.kind = reasonUnfinished
}

// mayCrashRetry reports whether a crashed run is still inside both budgets
// that bound crash resumes.
func (l *resumeLedger) mayCrashRetry(cfg config) bool {
	return l.fruitless < cfg.retries && l.resumes < cfg.resumeCeiling
}

// issueLoop is one processIssue call's loop-carried state: the context every
// step shares, the tally it accumulates into, and the resume ledger the loop
// turns on. The two loop arms and the terminal/parked recorders hang off it as
// methods rather than each taking the same handful of arguments.
type issueLoop struct {
	ctx    context.Context
	cfg    config
	issue  int
	branch string
	st     *issueState
	// tally is what -post-summary reports. It lives on issueState rather than
	// here so an issue put down for an answer and picked up later still
	// reports every run behind it. Nothing reads it back once the process
	// ends, so it stays telemetry rather than state: a supervisor restarted
	// mid-issue starts a fresh one, and the comment it feeds says it covers
	// this drain.
	tally  *issueTally
	ledger resumeLedger
	// policy resolves each run's model and effort from the operator's cells —
	// see policy.go. Set once in processIssue; every dispatch reads it.
	policy runPolicy
}

// terminal marks how the issue ended, failures included — they are the most
// informative rows in the dataset, and every one of them ends this issue:
// merged, or parked for a human and left behind. Transient GitHub errors and
// Ctrl+C are deliberately not outcomes: the issue is still open and unparked,
// and the next drain resumes it.
func (r *issueLoop) terminal(prNumber int, outcome, why string) {
	usage := issueUsageSamples{atPickup: r.st.weekUsageAtPickup, hasPickup: r.st.hasWeekUsageAtPickup}
	if r.cfg.rec.enabled() {
		usage.atTerminal, usage.hasTerminal = sampleWeekUsage(r.ctx, r.cfg)
	}
	r.cfg.rec.recordIssue(r.cfg, r.issue, prNumber, outcome, why, lookupPRFacts(r.ctx, r.cfg, prNumber), usage, r.policy.size)
}

// parked is terminal for the hand-backs, and the reason it files them under is
// the one the park itself named. Classifying the error here rather than at
// each callsite is what keeps the record and the sentence on the issue thread
// describing the same thing: a park raised inside supervisePR is several calls
// away from the record that reports it.
func (r *issueLoop) parked(prNumber int, err error) error {
	r.terminal(prNumber, issueNeedsHuman, parkCategoryOf(err))
	return err
}

// waitsOnPR reports whether an issue's branch already has a PR — restart
// safety (CLAUDE.md): once one exists, the skill is never re-run for this
// issue, whatever the PR's own state; the loop goes straight to waiting on
// it instead. unpark's own next-shift line (unpark_work.go) makes this exact
// call too, off a PR it read the same way, so a test holds the two to one
// function rather than two copies that can drift.
func waitsOnPR(pr *pullRequest) bool {
	return pr != nil
}

// processIssue advances one issue as far as it will go: to merged, to a park,
// or — the one way back out that is neither — to a question a human owes an
// answer to, returned as a *deferredError for the caller to put down.
//
// st carries what the drain already knows about this issue, and collects what
// this call learns. Everything durable is on GitHub; st only saves re-deriving
// it within one process.
func processIssue(ctx context.Context, cfg config, issue int, st *issueState) error {
	r := &issueLoop{
		ctx:    ctx,
		cfg:    cfg,
		issue:  issue,
		branch: fmt.Sprintf("%s%d", cfg.branchPrefix, issue),
		st:     st,
		tally:  &st.tally,
		policy: newRunPolicy(cfg),
	}

	// The issue's model:/effort: labels and its Estimate: size, resolved once
	// here at pickup: every run this leg dispatches — implement and the three
	// remediations — reads the result off r.policy. Resolving per pickup rather
	// than per shift is what makes a label edited while the issue waited on an
	// answer take effect on the next leg. Its own read, not folded into
	// dispatchRun's awaiting-answer check: that one is re-read every loop
	// iteration on purpose, this one is fixed for the leg.
	r.policy.labels, r.policy.size = issuePickupPolicy(ctx, cfg, issue)
	warnEffortLabelEnv(cfg, issue, r.policy.labels)

	// Before the run, not only after the last merge: the gap this closes is also
	// opened by a teammate's push and by a drain restarted days later, and the
	// moment that matters is the one just before a branch is cut and a review
	// resolves its base. It is also the gate: an origin that cannot be fetched
	// ends the shift here, before a run is paid for that could not push.
	if err := syncDefaultBranch(ctx, cfg, st); err != nil {
		return err
	}

	// The pickup half of the two samples the ledger reads a terminal record
	// for — see issueState.weekUsageAtPickup. Sampled here rather than at the
	// call site that dispatched into processIssue, so it covers every way in
	// (a fresh pickup, a resume after a crash, a resume once an answer
	// landed) with the one line. Unconditional so a second or third leg's
	// failed probe overwrites an earlier leg's reading with "not sampled"
	// rather than leaving it in place — st outlives a single call for an
	// issue put down for an answer, and a stale pickup from before the wait
	// is worse than none. Skipped when nothing would read it: -metrics off
	// (or no recorder at all) makes recordIssue's own write a no-op, so
	// sampling for it would be a probe call spent on a value nobody keeps.
	if cfg.rec.enabled() {
		st.weekUsageAtPickup, st.hasWeekUsageAtPickup = sampleWeekUsage(ctx, cfg)
	}

	for {
		pr, err := prForBranch(ctx, cfg, r.branch)
		if err != nil {
			return err
		}
		if !waitsOnPR(pr) {
			// After the PR check, not before: a PR already open needs no fetch
			// to be waited on, only a new run does (issue #595).
			if st.fetchAuthFailed {
				return errFetchAuthHeld
			}
			// dispatchRun's return shapes are the loop's verdict: (nil, err)
			// is a terminal exit with that error, (nil, nil) means go round
			// again, and (pr, nil) means a PR is open now and superviseToClose
			// takes it from here.
			pr, err = r.dispatchRun()
			if errors.Is(err, errIssueClosedNoChange) {
				return nil
			}
			if err != nil {
				return err
			}
			if pr == nil {
				continue
			}
		}
		return r.superviseToClose(pr)
	}
}

// nextRunKind decides what the run about to be dispatched is: a resume of the
// stored session, a fresh run folding in an answer that landed, or a plain
// implement run. A retry with no session to resume — the crashed run never got
// one, or the one it got turned out to be unresumable — is a fresh skill run
// in everything but name, so it is recorded as one; the skill re-derives where
// it got to from the worktree. st.answered is consumed here: the flag is for
// one run only.
func (r *issueLoop) nextRunKind() (resumeTarget, reason string) {
	reason = reasonImplement
	switch {
	case r.ledger.kind != "" && r.st.session != "":
		resumeTarget = r.st.session
		reason = r.ledger.kind
	case r.st.answered:
		reason = reasonAnswers
	}
	r.st.answered = false
	return resumeTarget, reason
}

// dispatchRun is the loop's no-PR-yet arm: the budget and awaiting-answer
// checks, a Claude run through runClaude, and then — via classifyNoPR — the
// classification of what came back. See processIssue's loop for how its
// return shapes are read; (nil, nil) means loop again.
func (r *issueLoop) dispatchRun() (*pullRequest, error) {
	ctx, cfg, issue := r.ctx, r.cfg, r.issue

	// Asked before another run is dispatched rather than after one returns,
	// which is the only place a cost cap can be enforced at all: cost arrives
	// on the result event, so it can bound the next run and never the one
	// that spent it.
	if reason := overBudget(cfg, *r.tally); reason != "" {
		return nil, r.parked(0, r.budgetPark(reason))
	}

	// The label is durable, so "it is up after the run" does not by itself
	// mean this run raised it. Read it before the run too, and the two
	// readings tell a question apart from an earlier one's flag that this run
	// died before clearing.
	wasBlocked, err := issueHasLabel(ctx, cfg, issue, awaitingAnswerLabel)
	if err != nil {
		return nil, err
	}

	resumeTarget, reason := r.nextRunKind()

	// Resolve this run's model and effort from the operator's cells. runLimit
	// stays on the base cfg — it is a per-issue budget, not a per-run knob.
	choice := r.policy.choose(reason)
	if line := choice.dispatchLine(issue); line != "" {
		cfg.logf("%s", line)
	}
	runCfg := choice.apply(cfg)

	started := time.Now()
	rep, runErr := runClaude(ctx, runCfg, issue, resumeTarget, reason, runLimit(cfg, *r.tally))
	a := &runAttempt{
		issueLoop: r,
		rep:       rep,
		rc: runContext{
			issue: issue, reason: reason, attempt: r.ledger.resumes,
			resumedFrom: resumeTarget,
			runChoice:   choice,
			started:     started, ended: time.Now(),
		},
	}
	if rep.sessionID != "" {
		r.st.session = rep.sessionID
	}
	// A resume that never started is a dead session, not a crashed run: its
	// JSONL was truncated by a hard kill mid-append, or it has aged out of the
	// CLI's retention. execClaude seeds the report's session from the one it
	// was asked to resume, so the id survives a run that emitted nothing at
	// all — and every later attempt then fails the same way in seconds,
	// parking a workable issue as "claude crashed and 3 resume attempts
	// failed". Forget the session instead and let the next attempt go fresh,
	// which is the run that would have worked.
	//
	// Only when the run also failed on its own: a resume that answered cleanly
	// without an init event is not a shape any CLI produces, and as with
	// lacksCommand every uncertainty here resolves toward carrying on. A
	// shutdown signal is the other exclusion — it kills the child through the
	// context, so a resume interrupted before its first event looks exactly
	// like a dead session and is not one.
	if resumeTarget != "" && runErr != nil && ctx.Err() == nil && !rep.started {
		cfg.narrate(sevWarning, "session %s could not be resumed — the next attempt starts a fresh run, "+
			"which re-derives where the last one got to from the worktree", resumeTarget)
		r.st.session = ""
	}

	if runErr != nil {
		if ctx.Err() != nil {
			a.record(0, outcomeUnknown)
			return nil, ctx.Err()
		}
		// A prompt that never resolved will never resolve on a retry, and the
		// generic "no PR and no questions" report buries the cause. Say what
		// is actually wrong and stop.
		if errors.Is(runErr, errNoWork) {
			a.record(0, outcomeNothing)
			return nil, r.parked(0, fmt.Errorf("%w — check that -skill %q names a skill this "+
				"installation has; plugin skills are namespaced <plugin>:<skill>, "+
				"a skill copied into ~/.claude/skills is not",
				runErr, cfg.skill))
		}
		cfg.logf("claude run ended with error (%v) — checking what it left behind", runErr)
	}

	pr, err := prForBranch(ctx, cfg, r.branch)
	if err != nil {
		a.record(0, outcomeUnknown)
		return nil, err
	}
	if pr == nil {
		// Checked ahead of classifyNoPR's crash/budget/question logic, which is
		// all about *why* a run left no PR behind — moot once the issue itself
		// is closed, the fourth ending (#210): a run that verified the code
		// needed no change (already fixed elsewhere, a duplicate) and closed it
		// directly instead of opening one. Gated on runErr == nil: this ending
		// reports a close *this run verified*, and only a clean exit is that —
		// a token refused mid-session or a budget kill can coincide with the
		// issue being closed by an unrelated actor, and classifyNoPR is what
		// turns those into the fatal park CLAUDE.md requires, not this success.
		if runErr == nil {
			state, serr := issueOpenState(ctx, cfg, issue)
			if serr != nil {
				a.record(0, outcomeUnknown)
				return nil, serr
			}
			if state == "CLOSED" {
				a.record(0, outcomeClosedIssue)
				a.terminal(0, issueClosedNoChange, "")
				r.st.closedNoChange = true
				return nil, errIssueClosedNoChange
			}
		}
		return a.classifyNoPR(runErr, wasBlocked)
	}
	a.record(pr.Number, outcomeOpenedPR)
	r.ledger.clearRetries()
	return pr, nil
}

// errIssueClosedNoChange marks dispatchRun's fourth-ending exit: the run
// closed its own issue directly rather than opening a PR, and GitHub confirms
// it. Not a park — processIssue's loop reads it as success, the same as a
// merge, because there is nothing left for a human to do.
var errIssueClosedNoChange = errors.New("issue closed with no code change needed")

// fetchAuthParkReason leads a clean-exit park's reason when this leg's own
// pickup fetch couldn't authenticate (issue #425): polako's own words, no raw
// git stderr, since this reaches the issue thread verbatim. It doesn't say
// the run's own classification is wrong — a permission refusal drawn along
// the way is still worth knowing — only that this is the likelier root cause,
// named first rather than left for #390's misattribution to repeat.
const fetchAuthParkReason = "polako's own fetch couldn't authenticate just before this run; " +
	"fix git access in -dir (`ssh-add -l`, or an https remote), then remove needs-human"

// budgetPark builds a budget park's error: cause is overBudget's own sentence
// (or errBudget's, on the leg that classifies a run the cap killed mid-run),
// and inspectLeftWork's account of what is on disk is appended the same way
// parkCleanExit appends it — so the person clearing needs-human knows
// whether to pick up a real change or start from scratch. #318 is the case
// that named the gap (issue #466): a run killed at the cap left two commits
// and 33 dirty files nowhere but its own disk, and the park comment named the
// cap and nothing else.
//
// Unlike every other park, this one writes: a branch inspectLeftWork finds
// with commits not on its remote-tracking ref gets pushed to origin,
// best-effort, before the message is built. A budget park is exactly #318's
// own case — killed by the clock, with nothing durable saying where the work
// went — and pushing a branch nothing points a PR at yet is not a merge and
// touches no default branch, so "nothing merges itself" holds; see "What
// leaves the machine" in CLAUDE.md, which this is the reasoning for. A failed
// push is folded into the reason rather than swallowed, since that is the one
// case where the work really is nowhere but this disk.
func (r *issueLoop) budgetPark(cause string) error {
	left := inspectLeftWork(r.ctx, r.cfg, r.issue)
	reason := cause
	if left.commits > 0 && !left.pushed {
		if _, err := git(r.ctx, r.cfg, "push", "origin", left.branch); err != nil {
			reason += fmt.Sprintf("; tried to push branch %s to origin so the work "+
				"is not only on this machine, but the push failed — push it by hand "+
				"(`git push origin %s`), then remove needs-human", left.branch, left.branch)
		} else {
			left.pushed = true
		}
	}
	if d := left.describe(); d != "" {
		reason += "; " + d
	}
	if w := left.where(); w != "" {
		return parkAside(parkBudget, w, "%s", reason)
	}
	return park(parkBudget, "%s", reason)
}

// superviseToClose is the loop's PR-exists arm: wait on an open PR through
// supervisePR, then act on how it left the OPEN state — a merge cleans up and
// closes the issue, a close without merge parks for a human, anything else is
// an unexpected-state park. Every path is terminal, so processIssue returns
// straight through it.
func (r *issueLoop) superviseToClose(pr *pullRequest) error {
	ctx, cfg, issue := r.ctx, r.cfg, r.issue
	switch pr.State {
	case "OPEN":
		cfg.logf("PR #%d open — waiting for merge (%s)", pr.Number, pr.URL)
		// One choice for all three remediations: reasonRemediate, reasonChecks
		// and reasonReview resolve identically (remediationReasons), so the
		// class representative is enough.
		remChoice := r.policy.choose(reasonRemediate)
		state, err := supervisePR(ctx, cfg, issue, pr.Number, r.st, r.tally, remChoice)
		if err != nil {
			if ctx.Err() == nil { // not Ctrl+C: remediation ran out of attempts
				return r.parked(pr.Number, err)
			}
			return err
		}
		pr.State = state
		fallthrough
	case "MERGED", "CLOSED":
		if pr.State == "MERGED" {
			cfg.narrate(sevSuccess, "PR #%d merged — cleaning up and advancing", pr.Number)
			// Reclaims this issue's worktree and branch, plus anything else a
			// hand-merge between shifts left finished. The sweep fast-forwards
			// the mirror before it judges anything, which is also the sync this
			// arm used to make by hand — the merge just made the local default
			// branch stale, and doing it here leaves the operator a current
			// checkout when this was the last issue in the backlog.
			tidySweep(ctx, cfg, issue)
			r.terminal(pr.Number, issueMerged, "")
			postSummary(ctx, cfg, pr.Number, *r.tally)
			return ensureIssueClosed(ctx, cfg, issue, pr.Number)
		}
		r.terminal(pr.Number, issueClosed, "")
		// The park comment's own "remove the label" isn't enough here: GitHub
		// can't delete the closed PR, and it blocks every rerun of issue-N
		// (waitsOnPR), so the reason names the two ways out that work.
		return park(parkPRClosed,
			"PR #%d was closed without merging, which is a decision only a human can make. "+
				"Reopen it to carry on, or file a fresh issue and close this one to start over.",
			pr.Number)
	default:
		return r.parked(pr.Number,
			park(parkPRState, "PR #%d is in the unexpected state %q", pr.Number, pr.State))
	}
}
