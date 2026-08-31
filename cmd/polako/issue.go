package main

// processIssue advances one issue as far as it will go: to merged, to a park,
// or to a question a human owes an answer to. It owns the resume policy — the
// two ceilings below, one for crash loops and one for the pricier clean-exit
// resumes — and dispatches each run through runClaude.
//
// Its loop has two arms. dispatchRun is the one taken while no PR exists yet:
// it runs Claude and classifies what came back, through the per-attempt
// helpers on runAttempt. superviseToClose is the other: a PR exists, so wait
// it out to merged or parked. resumeLedger is the four interlocking retry
// counters both the crash arm and the clean-exit arm read.

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
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
// eight minutes — and -max-cost and -max-issue-time are both off by default, so
// nothing else is standing between a run that keeps deciding to wait and a
// three-figure bill. Two buys the case worth buying, a run that was one commit
// from done, and refuses to fund the other one.
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
}

// clearRetries is what a fresh start does: a reply arrived, or a PR opened, so
// nothing is owed a resume and the crash budget starts over.
func (l *resumeLedger) clearRetries() { l.fruitless, l.kind = 0, "" }

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
}

// runAttempt is one trip through dispatchRun: the issueLoop it belongs to,
// plus the report and run context of the single Claude run this attempt
// dispatched. The classification helpers hang off it so record — which needs
// both — is one method rather than a closure threaded through every branch.
type runAttempt struct {
	*issueLoop
	rep runReport
	rc  runContext
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
	r.cfg.rec.recordIssue(r.cfg, r.issue, prNumber, outcome, why, lookupPRFacts(r.ctx, r.cfg, prNumber), usage)
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

// record files this attempt's run under an outcome the classification below
// settled on, so every exit from dispatchRun passes through one.
func (a *runAttempt) record(prNumber int, outcome string) {
	a.rc.pr, a.rc.outcome = prNumber, outcome
	a.tally.add(a.cfg.rec.recordRun(a.cfg, a.rc, a.rep))
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
	}

	// Before the run, not only after the last merge: the gap this closes is also
	// opened by a teammate's push and by a drain restarted days later, and the
	// moment that matters is the one just before a branch is cut and a review
	// resolves its base.
	syncDefaultBranch(ctx, cfg)

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
		// Restart safety: if a PR already exists for this branch, never
		// re-run Claude — go straight to waiting on it.
		pr, err := prForBranch(ctx, cfg, r.branch)
		if err != nil {
			return err
		}
		if pr == nil {
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
		return nil, r.parked(0, park(parkBudget, "%s", reason))
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

	started := time.Now()
	rep, runErr := runClaude(ctx, cfg, issue, resumeTarget, reason, runLimit(cfg, *r.tally))
	a := &runAttempt{
		issueLoop: r,
		rep:       rep,
		rc: runContext{
			issue: issue, reason: reason, attempt: r.ledger.resumes,
			resumedFrom: resumeTarget, started: started, ended: time.Now(),
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
		narrate(sevWarning, "session %s could not be resumed — the next attempt starts a fresh run, "+
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
		log.Printf("claude run ended with error (%v) — checking what it left behind", runErr)
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
		// directly instead of opening one. Whatever runErr says stops mattering
		// the moment GitHub itself says the issue is done.
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

// classifyNoPR decides what a run that opened no PR means: a dead end to park
// for a human, a usage limit to wait out, a question handed back, a crash to
// resume, or a clean exit that left salvageable work. It never returns a
// non-nil PR — (nil, nil) tells dispatchRun's caller to loop again, (nil, err)
// is terminal.
func (a *runAttempt) classifyNoPR(runErr error, wasBlocked bool) (*pullRequest, error) {
	ctx, cfg, issue := a.ctx, a.cfg, a.issue

	// The token does not come back on its own, so every resume spends minutes
	// reaching the same 401 and buries the cause under a report about crashes.
	// Stop the drain: every later issue would hit the same wall.
	//
	// Checked here rather than beside errNoWork, which stops before the PR
	// lookup: a token can die after the run opened its PR, and that case
	// belongs on the waiting path, which needs no token at all. Checked before
	// the label, too: a run refused at the door cannot have raised one, so a
	// label found here is an older run's, and crediting it to this one would
	// bias every questions rate stats computes.
	if errors.Is(runErr, errAuth) {
		a.record(0, outcomeNothing)
		return nil, a.parked(0, authAdvice(runErr))
	}
	// A cap killed this run, so it is a dead end for the same reason a refused
	// token is: a resume would spend the same budget over again on the same
	// issue and be killed at the same point. The record comes first, because
	// this run's own numbers are what carried the issue over the line and the
	// reason quotes them.
	if errors.Is(runErr, errBudget) {
		a.record(0, outcomeNothing)
		return nil, a.parked(0, park(parkBudget, "%s",
			cmp.Or(overBudget(cfg, *a.tally), runErr.Error())))
	}
	if errors.Is(runErr, errLimit) {
		return a.waitOutLimit()
	}

	blocked, err := issueHasLabel(ctx, cfg, issue, awaitingAnswerLabel)
	if err != nil {
		a.record(0, outcomeUnknown)
		return nil, err
	}
	// A flag this run raised means it asked something, crash or not. A flag
	// that was already up and is still up after a crash proves nothing: the
	// run is far likelier to have died before clearing it, and the reply that
	// flag waits for is the one that dispatched this very run — so waiting on
	// it waits forever, while a resume is exactly what unsticks it.
	asked := blocked && (!wasBlocked || runErr == nil)
	switch {
	case asked:
		return a.handOffQuestion()
	case runErr != nil && a.ledger.mayCrashRetry(cfg):
		return a.resumeCrash()
	case runErr != nil:
		return nil, a.giveUpAfterCrash()
	default:
		return a.afterCleanExit()
	}
}

// waitOutLimit handles a run refused for a usage limit: neither this issue's
// fault nor a crash a resume can route around, since every attempt before the
// reset is refused the same way in seconds, and each one used to spend the
// retry budgets that exist for real crashes — twenty refusals thirty seconds
// apart, and a healthy issue was parked (#67). Wait for the reset the refusal
// names, then resume. The wait is charged to neither -retries nor the resume
// ceiling, because those bound evidence that the issue cannot be finished and
// this run is evidence about the account; what bounds the wait is the clock —
// a readable reset is at most a day away, and a refusal with no clock this can
// read falls back to one attempt per -poll rather than a tight loop.
func (a *runAttempt) waitOutLimit() (*pullRequest, error) {
	// outcomeUnknown, not outcomeNothing: the account cut this run off —
	// mid-session, after a commit and a finished review gate on the shift #218
	// was filed from — so it never decided to produce nothing, and reading it
	// as a run that did would bias every rate stats computes. The sibling
	// Ctrl+C branch records the same for the same reason.
	a.record(0, outcomeUnknown)
	wait := a.cfg.poll
	if reset, ok := limitReset(a.rep.limitMsg, time.Now()); ok {
		// Slack behind the CLI's own clock: a resume dispatched on the named
		// minute can still be refused by it.
		wait = time.Until(reset) + 90*time.Second
		log.Printf("claude is over its usage limit until %s — waiting %s, then resuming "+
			"(Ctrl+C is safe: state is on GitHub, and rerunning after the reset "+
			"picks this issue back up)", reset.Format("15:04 MST"), dur(wait))
	} else {
		log.Printf("claude is over its usage limit, and the refusal names no reset time "+
			"this supervisor can read (%q) — retrying every %s until it lifts "+
			"(Ctrl+C is safe: state is on GitHub, and rerunning later picks this "+
			"issue back up)", clip(a.rep.limitMsg, 120), dur(a.cfg.poll))
	}
	if err := sleep(a.ctx, wait); err != nil {
		return nil, err
	}
	a.ledger.kind = reasonResume
	return nil, nil
}

// handOffQuestion handles a run that posted and flagged a question (even if it
// then crashed). The baseline for "a reply arrived" is read now, so the
// question itself is the newest thing on the thread and can never be mistaken
// for its own answer.
func (a *runAttempt) handOffQuestion() (*pullRequest, error) {
	ctx, cfg, issue := a.ctx, a.cfg, a.issue
	a.record(0, outcomeQuestions)
	comments, err := issueComments(ctx, cfg, issue)
	if err != nil {
		return nil, err
	}
	baseline := commentBaseline(comments)
	// Fired here rather than in either fork below, because both of them leave
	// the issue waiting on the same person: this is the state the flag exists
	// for, and -strict-order only changes what the supervisor does in the
	// meantime.
	notify(ctx, cfg, notification{event: notifyAwaiting, issue: issue,
		reason: "a run stopped to ask something on the issue thread — " +
			"reply there and the next shift folds the answer in"})
	if !cfg.strictOrder {
		// The question is flagged on GitHub, which is durable and is all a
		// later drain needs. Hand the issue back so the queue behind it can be
		// worked: an issue nobody is working is not one in flight, so the
		// no-conflict guarantee is untouched.
		return nil, &deferredError{baseline: baseline}
	}
	log.Printf("issue #%d is labelled %q — waiting for a reply on the thread",
		issue, awaitingAnswerLabel)
	if err := waitForReply(ctx, cfg, issue, baseline); err != nil {
		return nil, err
	}
	log.Printf("somebody replied on #%d — re-running to fold the answers in", issue)
	a.ledger.clearRetries()
	a.st.answered = true
	return nil, nil
}

// resumeCrash handles a crash (API drop, stall, tool failure) that is still
// inside both retry budgets: resume the exact session by ID, keeping its
// research context. If no session was ever created, retry as a fresh run
// instead.
func (a *runAttempt) resumeCrash() (*pullRequest, error) {
	cfg := a.cfg
	a.record(0, outcomeNothing)
	// The run that just died is what carried the issue over, so the resume
	// this is about to announce would be refused by the gate at the top of the
	// loop anyway. Say so here: an unattended log that promises a resume it
	// never makes, after sleeping -retry-wait for it, is a worse diagnosis
	// than the park it is really doing.
	if reason := overBudget(cfg, *a.tally); reason != "" {
		return nil, a.parked(0, park(parkBudget, "%s", reason))
	}
	progressed := a.rep.progressed()
	a.ledger.noteCrashResume(progressed)
	mode := "restarting fresh"
	if a.st.session != "" {
		mode = "resuming session " + a.st.session
	}
	if progressed {
		// This run did real work before it died — an hour of it, for all
		// anyone here knows — so it was not the crash loop -retries exists to
		// stop. A host that sleeps four times across one long issue must not
		// park it.
		log.Printf("%s (retry %d/%d; the last run got work done before it "+
			"ended, so the -retries budget starts over) in %s",
			mode, a.ledger.resumes, cfg.resumeCeiling, cfg.retryWait)
	} else {
		log.Printf("%s (attempt %d/%d) in %s",
			mode, a.ledger.fruitless, cfg.retries, cfg.retryWait)
	}
	if err := sleep(a.ctx, cfg.retryWait); err != nil {
		return nil, err
	}
	return nil, nil
}

// giveUpAfterCrash parks an issue whose run crashed with the retry budgets
// spent. It always returns a non-nil error.
func (a *runAttempt) giveUpAfterCrash() error {
	cfg := a.cfg
	a.record(0, outcomeNothing)
	if a.ledger.resumes >= cfg.resumeCeiling {
		// "retried" rather than "resumed": most of these are resumes, but a
		// dead session turns one into a fresh restart, and the count covers
		// both. everProgressed picks the clause: -retries has no enforced
		// ceiling of its own, so a value set above resumeCeiling can still
		// reach here on a run of pure death-rattle crashes, and claiming one
		// of them got somewhere would be exactly the false diagnosis this
		// issue is about.
		clause := "every attempt has died before doing any observable work"
		if a.ledger.everProgressed {
			clause = "each run gets somewhere and then dies"
		}
		return a.parked(0, park(parkRetries,
			"claude has been retried %d times on this issue and still has "+
				"not finished it — %s, which needs a human", a.ledger.resumes, clause))
	}
	return a.parked(0, park(parkRetries,
		"claude crashed and %d resume attempts failed", cfg.retries))
}

// afterCleanExit handles a clean exit that opened no PR and flagged no
// question through the proper channel. Four different runs end this way and
// only one of them is the "Claude decided nothing" this used to assume: two
// believed they had paused for something that will never come back, or ran out
// of road mid-task, and both have the change sitting on disk, finished or
// nearly — exactly what resume exists for. The fourth asked the operator to
// approve a tool this allowlist never granted and ended its turn unheard —
// see rep.permissionRefused below, whose fix (-add-tools) is not something
// resuming the same session can reach.
//
// The first three (decided nothing, paused forever, ran out of road) are told
// apart by that work on disk, not rep.progressed(): every clean exit
// progressed — the run this was written for scored 59 turns and 58 tool
// uses — so progress cannot separate them. Whether the branch has commits, or
// the worktree is dirty, can. The fourth is told apart by the run's own final
// words instead, classified by permissionRefusal, and checked first: no amount
// of salvageable work changes what fixes it.
func (a *runAttempt) afterCleanExit() (*pullRequest, error) {
	a.record(0, outcomeNothing)
	// One probe, feeding both the decision and, if it turns out to be a park
	// after all, the message.
	left := inspectLeftWork(a.ctx, a.cfg, a.issue)

	if a.rep.permissionRefused {
		// Resuming replays the identical session against the identical
		// allowlist, so it hits the same wall again — only the operator can
		// grant the tool, so park straight away rather than spending the
		// clean-exit resume budget finding that out the slow way.
		return nil, a.parkCleanExit(parkPermission, permissionParkReason, a.rep.permissionRefusedDetail, left)
	}

	bound, boundWhy, resume := a.cleanExitDisposition(left)
	if resume {
		// No -retry-wait. A crash sleeps because a crash is often transient —
		// an API drop, a rate limit, a host that woke mid-run — and this is
		// not: the process ended because the model ended its turn, and waiting
		// changes nothing about what the next attempt finds.
		log.Printf("the run ended its turn without opening a PR but left work "+
			"behind — resuming it to finish (%d/%d)", a.ledger.cleanResumes, cleanExitResumeCeiling)
		return nil, nil
	}

	// What happened and why we stopped trying; parkCleanExit appends what is
	// there for the person picking it up. With nothing on disk that extra
	// clause is empty. No longer claims the run asked nothing — only a
	// question flagged through the proper channel is ruled out by the time
	// this runs, and the permission case above is proof that prose the model
	// never flagged can still be one.
	reason, category := "the run completed without opening a PR", boundWhy
	if a.rep.permissionAsked {
		// An earlier turn asked for a tool this run was not granted (the
		// result text did not, or permissionRefused above would have parked
		// without resuming). Whether that ask is why nothing shipped or a
		// detour it recovered from, it is the first thing for an operator to
		// rule out — and the generic sentence sends them to the shift log to
		// learn it was even asked. Naming it here changes neither that we park
		// nor that a resume was tried first: control reaches this line only
		// once resuming is done.
		//
		// The category still follows a bound when one stopped the resume: a
		// cap or an exhausted resume budget is a parkBudget/parkRetries in the
		// report whether or not the run also asked for a tool, for the same
		// reason the crash arm files those causes that way — otherwise
		// clearing needs-human after -add-tools just burns back into the same
		// ceiling.
		reason = permissionParkReason
		if bound == "" {
			category = parkPermission
		}
	}
	if bound != "" {
		reason += "; " + bound
	}
	return nil, a.parkCleanExit(category, reason, "", left)
}

// cleanExitDisposition decides what to do with a clean exit that left work
// behind: resume it, or name the bound that stops the resume that would
// otherwise be warranted. The bound feeds both the park message and its
// category — filing a cap or an exhausted resume budget under "produced
// nothing" would point the report's ranking at the skill when the lever is
// the operator's own flag, and the crash arm files those same causes
// correctly. With nothing salvageable on disk there is nothing to resume and
// no bound to name: parkNothing, the generic case.
func (a *runAttempt) cleanExitDisposition(left leftWork) (bound, boundWhy string, resume bool) {
	if !left.salvageable() {
		return "", parkNothing, false
	}
	switch over := overBudget(a.cfg, *a.tally); {
	case over != "":
		// As in the crash arm: the gate at the top of the loop would refuse
		// this dispatch anyway, and a log promising a resume it never makes is
		// a worse diagnosis than the park it is really doing.
		return over, parkBudget, false
	case a.ledger.cleanResumes >= cleanExitResumeCeiling:
		return fmt.Sprintf("it has been resumed %s after ending a turn without opening a PR "+
			"and has still not opened one, which needs a human",
			plural(a.ledger.cleanResumes, "time")), parkRetries, false
	case a.ledger.resumes >= a.cfg.resumeCeiling:
		// "retried" rather than "resumed", as in the crash arm and for the
		// same reason: a dead session turns one of these into a fresh restart,
		// and the count covers both.
		return fmt.Sprintf("claude has been retried %s on this issue and still has not "+
			"finished it, which needs a human", plural(a.ledger.resumes, "time")), parkRetries, false
	default:
		a.ledger.noteCleanResume()
		return "", "", true
	}
}

// parkCleanExit parks a clean exit under category, with left's summary of
// what is on disk appended to reason for the person picking it up.
func (a *runAttempt) parkCleanExit(category, reason, refusedCmd string, left leftWork) error {
	if d := left.describe(); d != "" {
		reason += "; " + d
	}
	// The refused command can carry a local absolute path (a worktree path
	// inside a Bash command) exactly the way a worktree's own path can — see
	// leftWork.where() — so it travels beside it in aside, never in reason,
	// which is posted to the issue thread verbatim. Joined the same way
	// leftWork.describe() joins its own optional clauses.
	var asideParts []string
	if w := left.where(); w != "" {
		asideParts = append(asideParts, w)
	}
	if refusedCmd != "" {
		asideParts = append(asideParts, "the refused command was: "+clip(refusedCmd, 200))
	}
	return a.parked(0, parkAside(category, strings.Join(asideParts, " — "), "%s", reason))
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
		log.Printf("PR #%d open — waiting for merge (%s)", pr.Number, pr.URL)
		state, err := supervisePR(ctx, cfg, issue, pr.Number, r.tally)
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
			narrate(sevSuccess, "PR #%d merged — cleaning up and advancing", pr.Number)
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
		return park(parkPRClosed,
			"PR #%d was closed without merging, which is a decision only a human can make",
			pr.Number)
	default:
		return r.parked(pr.Number,
			park(parkPRState, "PR #%d is in the unexpected state %q", pr.Number, pr.State))
	}
}
