package main

// The per-attempt classifiers: what one dispatched Claude run means once it
// returns. runAttempt pairs an issueLoop with that run's report and context,
// and its methods sort a returned run into a PR opened, a resume, a wait-out,
// a handed-off question, or a park — see issue.go's dispatchRun, the caller
// that dispatches the run and reads runAttempt's verdict.

import (
	"cmp"
	"errors"
	"fmt"
	"strings"
	"time"
)

// runAttempt is one trip through dispatchRun: the issueLoop it belongs to,
// plus the report and run context of the single Claude run this attempt
// dispatched. The classification helpers hang off it so record — which needs
// both — is one method rather than a closure threaded through every branch.
type runAttempt struct {
	*issueLoop
	rep runReport
	rc  runContext
}

// record files this attempt's run under an outcome the classification below
// settled on, so every exit from dispatchRun passes through one.
func (a *runAttempt) record(prNumber int, outcome string) {
	a.rc.pr, a.rc.outcome = prNumber, outcome
	rec := a.cfg.rec.recordRun(a.cfg, a.rc, a.rep)
	a.tally.add(rec)
	noteRanOn(a.st, rec, a.rep)
}

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
		return nil, a.parked(0, a.budgetPark(cmp.Or(overBudget(cfg, *a.tally), runErr.Error())))
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
		a.cfg.logf("claude is over its usage limit until %s — waiting %s, then resuming "+
			"(Ctrl+C is safe: state is on GitHub, and rerunning after the reset "+
			"picks this issue back up)", reset.Format("15:04 MST"), dur(wait))
	} else {
		a.cfg.logf("claude is over its usage limit, and the refusal names no reset time "+
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
	cfg.logf("issue #%d is labelled %q — waiting for a reply on the thread",
		issue, awaitingAnswerLabel)
	if err := waitForReply(ctx, cfg, issue, baseline); err != nil {
		return nil, err
	}
	cfg.logf("somebody replied on #%d — re-running to fold the answers in", issue)
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
		return nil, a.parked(0, a.budgetPark(reason))
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
		cfg.logf("%s (retry %d/%d; the last run got work done before it "+
			"ended, so the -retries budget starts over) in %s",
			mode, a.ledger.resumes, cfg.resumeCeiling, cfg.retryWait)
	} else {
		cfg.logf("%s (attempt %d/%d) in %s",
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
	var reason string
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
		reason = fmt.Sprintf("claude has been retried %d times on this issue and still has "+
			"not finished it — %s, which needs a human", a.ledger.resumes, clause)
	} else {
		reason = fmt.Sprintf("claude crashed and %d resume attempts failed", cfg.retries)
	}
	if dp := a.ledger.deferred; len(dp.refusals) > 0 {
		// issue #461: a worked-around refusal resumed into this crash loop
		// instead of ending cleanly again. The refusal is still the likelier
		// root cause than the crash count, so the park keeps blaming it —
		// same rule afterCleanExit's own deferred check follows, just reached
		// from the other arm.
		allowlist := resolveTools(a.cfg.tools, a.cfg.addTools)
		workedAroundReason, entries := permissionParkReasonWorkedAroundAndEntries(dp.refusals, allowlist)
		return a.parked(0, &parkedError{
			category: parkPermission,
			reason:   fmt.Sprintf("%s; %s", workedAroundReason, reason),
			aside:    deferredRefusalAside(dp),
			entries:  entries,
		})
	}
	return a.parked(0, park(parkRetries, "%s", reason))
}

// afterCleanExit handles a clean exit that opened no PR and flagged no
// question through the proper channel. Four different runs end this way and
// only one of them is the "Claude decided nothing" this used to assume: two
// believed they had paused for something that will never come back, or ran out
// of road mid-task, and both have the change sitting on disk, finished or
// nearly — exactly what resume exists for. The fourth asked the operator to
// approve a tool this allowlist never granted and ended its turn unheard —
// see rep.permissionRefused below, whose fix (-add-tools) is not something
// resuming the same session can reach — unless the run worked around it and
// kept going (rep.refusalWorkedAround, issue #461), in which case it is worth
// letting the resume machinery try to finish the job before assuming so.
//
// The first three (decided nothing, paused forever, ran out of road) are told
// apart by that work on disk, not rep.progressed(): every clean exit
// progressed — the run this was written for scored 59 turns and 58 tool
// uses — so progress cannot separate them. Whether the branch has commits, or
// the worktree is dirty, can. The fourth is told apart by the run's own final
// words instead, classified by permissionRefusal, and checked first — unless
// worked around, no amount of salvageable work changes what fixes it.
func (a *runAttempt) afterCleanExit() (*pullRequest, error) {
	a.record(0, outcomeNothing)
	// One probe, feeding both the decision and, if it turns out to be a park
	// after all, the message.
	left := inspectLeftWork(a.ctx, a.cfg, a.issue)

	allowlist := resolveTools(a.cfg.tools, a.cfg.addTools)
	workedAround := a.rep.permissionRefused && a.rep.refusalWorkedAround()
	// Every refusal this park could blame: whatever an earlier worked-around
	// resume of this same issue deferred, plus this run's own — so a run that
	// does *not* recover this time never silently drops history an earlier
	// one deferred (issue #432's own review caught this: this branch used to
	// read only a.rep.refusals).
	allRefusals := permissionParkRefusals(a.ledger.deferred, a.rep)
	if a.rep.permissionRefused && !workedAround {
		// Resuming replays the identical session against the identical
		// allowlist, so it hits the same wall again — only the operator can
		// grant the tool, so park straight away rather than spending the
		// clean-exit resume budget finding that out the slow way. #126's own
		// shape (refused, then nothing more attempted) and #138's (the final
		// message is itself the ask) both land here.
		reason, entries := permissionParkReasonAndEntries(allRefusals, allowlist)
		aside := ""
		if d := joinRefusalDetails(allRefusals); d != "" {
			aside = "refused: " + clip(d, 400)
		}
		return nil, a.parkCleanExit(parkPermission, reason, aside, entries, left)
	}
	if workedAround {
		// Remembered on the ledger, not just this attempt's own report: if the
		// resume below still ends with no PR — this run's or a later one's,
		// with or without a fresh refusal of its own — the eventual park has
		// to keep blaming this refusal rather than reporting "produced
		// nothing" or whatever bound actually stopped the resuming.
		a.ledger.deferred = deferredRefusal{
			refusals: allRefusals,
			message:  clip(strings.TrimSpace(a.rep.lastResultText), 200),
		}
	}

	bound, boundWhy, resume := a.cleanExitDisposition(left)
	if resume {
		// No -retry-wait. A crash sleeps because a crash is often transient —
		// an API drop, a rate limit, a host that woke mid-run — and this is
		// not: the process ended because the model ended its turn, and waiting
		// changes nothing about what the next attempt finds.
		if workedAround {
			a.cfg.logf("the run was refused a tool mid-run but kept going and left work "+
				"behind — resuming it to finish before treating the refusal as the blocker (%d/%d)",
				a.ledger.cleanResumes, cleanExitResumeCeiling)
		} else {
			a.cfg.logf("the run ended its turn without opening a PR but left work "+
				"behind — resuming it to finish (%d/%d)", a.ledger.cleanResumes, cleanExitResumeCeiling)
		}
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
		reason, _ = permissionParkReasonAndEntries(a.rep.refusals, allowlist)
		if bound == "" {
			category = parkPermission
		}
	}
	aside, entries := "", []string(nil)
	if dp := a.ledger.deferred; len(dp.refusals) > 0 {
		// Unlike permissionAsked above, the category does not follow the
		// bound here: this is a structural refusal (#209's own signal), not a
		// weaker prose-based one, and the resume was already the concession —
		// #461's whole point is that this park keeps blaming the refusal
		// rather than the ceiling or budget that happened to be what actually
		// stopped the resuming.
		reason, entries = permissionParkReasonWorkedAroundAndEntries(dp.refusals, allowlist)
		category = parkPermission
		aside = deferredRefusalAside(dp)
	}
	if bound != "" {
		reason += "; " + bound
	}
	return nil, a.parkCleanExit(category, reason, aside, entries, left)
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
// what is on disk appended to reason for the person picking it up. extraAside
// is one more caller-built clause for the terminal only — a refused
// command can carry a local absolute path (a worktree path inside a Bash
// command) exactly the way a worktree's own path can, see leftWork.where(),
// so it travels here rather than in reason, which is posted to the issue
// thread verbatim. entries are the thread-safe -add-tools entries a
// permission park's reason could derive, if any — carried on the
// parkedError so parkIssue can add its own Refused: footer to the same
// comment (issue #432).
func (a *runAttempt) parkCleanExit(category, reason, extraAside string, entries []string, left leftWork) error {
	if a.st.fetchAuthFailed {
		reason = fetchAuthParkReason + "; " + reason
	}
	if d := left.describe(); d != "" {
		reason += "; " + d
	}
	var asideParts []string
	if w := left.where(); w != "" {
		asideParts = append(asideParts, w)
	}
	if extraAside != "" {
		asideParts = append(asideParts, extraAside)
	}
	return a.parked(0, &parkedError{
		category: category,
		reason:   reason,
		aside:    strings.Join(asideParts, " — "),
		entries:  entries,
	})
}
