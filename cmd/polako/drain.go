package main

// The drain loop: work the queue lowest-first, wait on each PR to merge, park
// an issue that cannot be finished, put down one that asked a question, and
// close a finished container on the way past. Everything durable is on GitHub;
// issueState only saves re-deriving it within one process.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"
)

// issueResult is one issue's fate, kept only long enough to print the summary
// this process ends with. Nothing reads it back afterwards — the durable record
// of a park is the label on GitHub, and of an unanswered question the other one.
type issueResult struct {
	issue    int
	parked   bool
	awaiting bool // put down waiting on a human answer, not finished
	// closedNoChange is the fourth ending (#210): closed directly on verified
	// evidence rather than merged, finished all the same. Mutually exclusive
	// with parked and awaiting — see issueState.closedNoChange.
	closedNoChange bool
	reason         string // why it parked; empty otherwise
	// What this shift's runs on the issue cost, and how many of them reported
	// no cost at all. Both come off the tally the issue was carrying, so an
	// issue this process only waited on contributes an honest zero.
	cost         float64
	approximated int
}

// issueState is what one drain remembers between the runs it dispatches for a
// single issue. Like the skip map it sits beside, it is not state: nothing
// reads it once the process ends, and a restart re-derives everything that
// matters from GitHub. What it buys is not having to re-derive it *within* a
// process — most of all the tally, which is what -post-summary reports and
// which would otherwise be lost every time an issue is put down for an answer.
type issueState struct {
	tally    issueTally
	answered bool  // a reply landed, so the next run folds it in
	awaiting bool  // this drain left it flagged for a human
	baseline int64 // newest comment on the thread when the question was flagged
	// session is the resume target: the last session any run on this issue
	// reported. It outlives a single processIssue call so that a run dispatched
	// once an answer lands, and then dying before it reports a session of its
	// own, still has one to resume rather than starting over from nothing.
	session string
	// closedNoChange is set when this issue's run took the fourth ending
	// (#210): verified evidence the issue needed no code change, and closed it
	// directly rather than opening a PR. Read once processIssue returns nil, to
	// tell that ending apart from a merge — both report success the same way,
	// since neither needs a human, but the summary and the run-data record
	// should not call a closed-no-PR issue "merged".
	closedNoChange bool
	// weekUsageAtPickup is the plan's week-usage percent sampled at the start
	// of the processIssue call currently working this issue. Overwritten
	// unconditionally on every call rather than once per issue, so an issue
	// put down for an answer and picked back up later gets the pickup that
	// belongs to the leg which actually reaches a terminal state — and a
	// later leg's failed probe resets this to (0, false) rather than leaving
	// an earlier leg's reading in place. Unset when the usage gate is off, no
	// recorder is configured, or the probe could not answer.
	weekUsageAtPickup    int
	hasWeekUsageAtPickup bool
}

// resumeHint points the operator at the two local handles for an issue a shift
// left mid-flight: the last skill run's transcript, and this shift's slice of
// the run data. Both are machine-local — the transcript lives under ~/.claude —
// so neither is GitHub-shaped state, and both are logged here and never posted
// to the issue thread: the same reasoning that keeps the session id out of a
// park's reason, which reaches the thread verbatim.
//
// "skill run", because a park raised while supervising a PR follows remediation
// runs, which report sessions of their own that this state does not track;
// those are on their own "session started" lines in the log above.
//
// Printed on a park, and — since #218 — on the interrupt and fatal exits too: a
// Ctrl+C during a usage-limit wait used to drop a resumable 32-minute session,
// review gate included, and say nothing.
func resumeHint(cfg config, issue int, st *issueState) {
	if st == nil {
		return
	}
	if st.session != "" {
		log.Printf("issue #%d: `claude --resume %s` reopens what the last skill run on it did",
			issue, st.session)
	}
	if cfg.shiftID != "" {
		log.Printf("issue #%d: `polako stats -shift %s` reports on this shift alone",
			issue, cfg.shiftID)
	}
	// A permission park's reason names "this shift's log" but cannot carry its
	// path — the path is exactly the kind of local detail that belongs beside
	// the resume id, not on the issue thread — so it is repeated here, at the
	// one park-adjacent line an operator reading only the terminal (or its
	// scrollback later) is guaranteed to see.
	if cfg.logPath != "" {
		log.Printf("issue #%d: %s has the full transcript, including any refused tool call",
			issue, cfg.logPath)
	}
}

// drain works the queue until it empties, an issue proves fatal, or -once says
// stop. An issue that cannot be finished is parked rather than fatal, and an
// issue waiting on a human answer is put down rather than waited on, so neither
// one stops a backlog draining overnight.
func drain(ctx context.Context, cfg config) error {
	started := time.Now()

	// Parked issues leave the queue by their label, but labelling is a network
	// call that can fail, and a park the label did not take would put this loop
	// straight back on the same issue. Remembering them here is what guarantees
	// it terminates. It is not state: nothing reads it after the process ends,
	// and a restart re-derives the queue from GitHub alone.
	skip := maps.Clone(cfg.skip)
	if skip == nil {
		skip = map[int]bool{}
	}
	cfg.skip = skip
	states := map[int]*issueState{}
	// Same shape as skip, for the same reason: not state a restart needs, only
	// a memo so this shift does not re-read a finished container's thread on
	// every pass once it already knows the marker is there.
	commentedContainers := map[int]bool{}
	// Same shape again: the containers this shift has already tried to close, so
	// a stale post-close listing does not re-fire epic-done and a failing close
	// warns once a shift rather than once a pass.
	closedContainers := map[int]bool{}
	// The containers this shift closed, accumulated across passes for the exit
	// summary: a container closed mid-shift is gone from the next listing, so
	// lastContainers can no longer speak for it. Not state either — a restart
	// re-derives an epic's closed state from GitHub like everything else.
	var closedEpics []containerInfo
	// Same shape and reasoning as closedEpics: the retire issues this shift
	// filed off a container close, accumulated across passes for the exit
	// summary. Not state — a restart re-derives whether a retire issue is
	// needed from GitHub alone, same as everything else here.
	var retiredDocs []retiredDoc

	var results []issueResult
	// The containers the most recent successful listing found — carried across
	// passes so the exit summary can name one that finished mid-shift without
	// an extra `gh` call or a merge-moment hook: the next pass to run one
	// already reflects an epic's last child merging. A shift that stops
	// without a next pass — -once after one issue, -max-session-cost, a fatal
	// error — reports what this listing knew and no more, same as any other
	// "ends before it re-lists" exit; the next shift's own first pass says
	// the rest.
	var lastContainers []containerInfo
	// Every exit goes through finish, fatal ones included: a session that died
	// on issue nine should still account for the eight before it. The issue it
	// died on is not among them — unfinished is not an outcome — so a run that
	// dies on its first issue has nothing to summarize and says nothing.
	finish := func(err error) error {
		if lines := drainSummary(append(results, stillWaiting(states)...), lastContainers, closedEpics, retiredDocs, time.Since(started)); len(lines) > 0 {
			narrate(sevSection, "%s", lines[0]) // the shift's own closing heading
			for _, line := range lines[1:] {
				log.Print(line)
			}
		}
		// A drain that ended before the backlog did needs somebody. Ctrl+C is
		// the exception rather than an oversight: whoever pressed it is at the
		// keyboard already.
		if err != nil && !errors.Is(err, context.Canceled) {
			notify(ctx, cfg, notification{event: notifyStopped, reason: err.Error()})
		}
		return err
	}

	// Once, before the first issue is picked up: between two shifts a human
	// merges PRs by hand, and every one of those leaves a worktree and branch no
	// merge-moment cleanup will ever revisit. The per-merge sweep below keeps it
	// from accumulating again.
	tidySweep(ctx, cfg, 0)

	for {
		// Read between issues and never inside one: ending a drain cleanly means
		// declining to take on more work, not killing a run part-way through an
		// issue that would then have to be parked. One issue can therefore carry
		// the total past the budget by whatever that issue costs — and -max-cost
		// does not bound that to itself, since it gates the next run rather than
		// the one in flight.
		if spent := sessionSpend(results, states); cfg.maxSessionCost > 0 && spent >= cfg.maxSessionCost {
			reason := fmt.Sprintf("this shift has spent %s of its -max-session-cost of %s — stopping here; "+
				"everything is on GitHub, so raise the budget and start it again to carry on",
				usd(spent), usd(cfg.maxSessionCost))
			log.Print(reason)
			// A clean exit, but the backlog is not drained and only a person can
			// decide to raise the budget — which is what notifyStopped is for.
			notify(ctx, cfg, notification{event: notifyStopped, reason: reason})
			return finish(nil)
		}
		// Same placement and the same reasoning as the cost budget above: read
		// between issues and never inside one, and never a park — the plan's own
		// limit is a fact about the account, not about this issue. Unlike the
		// cost budget it does not stop the shift: the pool resets on its own, so
		// the gate waits it out and loops back to probe again, the fence
		// matching the wall a mid-run refusal hits (behaviour.md). A Ctrl+C
		// during the wait returns context.Canceled from sleep, which finish
		// prints a summary for and reports without a notify.
		if wait, reason, tripped := usageGateReason(ctx, cfg); tripped {
			log.Print(reason)
			if err := sleep(ctx, wait); err != nil {
				return finish(err)
			}
			continue
		}
		ready, blocked, heldBack, containers, err := openIssues(ctx, cfg)
		if err != nil {
			return finish(err)
		}
		lastContainers = containers
		logHeldBack(heldBack, skip)
		closedNow, retiredNow, err := closeFinishedContainers(ctx, cfg, containers, commentedContainers, closedContainers)
		closedEpics = append(closedEpics, closedNow...)
		retiredDocs = append(retiredDocs, retiredNow...)
		if err != nil {
			return finish(err)
		}
		issue := pickLowest(ready, skip)
		if issue == 0 {
			blocked = slices.DeleteFunc(blocked, func(n int) bool { return skip[n] })
			if len(blocked) == 0 {
				// Nothing open and unparked means nothing is waiting on a reply
				// either: a flag this drain raised and can no longer see was
				// closed, parked or cleared by hand while it worked elsewhere.
				// Naming those in the summary would send an operator to a thread
				// with nothing left to do on it.
				clear(states)
				narrate(sevSuccess, "no open issues — backlog cleared")
				notify(ctx, cfg, notification{event: notifyCleared,
					reason: "no open issues left to work"})
				return finish(nil)
			}
			// Nothing else is workable, so the only way forward is an issue
			// somebody owes an answer on.
			if issue, err = awaitAnswer(ctx, cfg, blocked, states); err != nil {
				return finish(err)
			}
			if issue == 0 {
				continue // the queue moved while waiting — ask GitHub again
			}
		}
		narrate(sevSection, "=== issue #%d ===", issue)

		st := states[issue]
		if st == nil {
			st = &issueState{}
			states[issue] = st
		}
		// Working it is the end of waiting on it. Left standing, the flag would
		// have every exit from here that is neither a merge nor a park — Ctrl+C
		// while its PR is open, a fatal error — end by telling the operator to
		// reply on a thread nobody is waiting on any more.
		st.awaiting = false
		err = processIssue(ctx, cfg, issue, st)
		reason, parked := parkReason(err)
		switch deferred, isDeferred := deferReason(err); {
		case isDeferred:
			st.awaiting, st.baseline = true, deferred.baseline
			log.Printf("issue #%d is labelled %q — leaving it for a human and working the queue behind it",
				issue, awaitingAnswerLabel)
		case parked:
			skip[issue] = true
			results = append(results, spend(st, issueResult{issue: issue, parked: true, reason: reason}))
			delete(states, issue)
			parkAndMoveOn(ctx, cfg, issue, st, reason, err)
		case err != nil:
			// Ctrl+C mid-issue, or a fatal error: neither prints the park's
			// resume hint, yet both leave a run's transcript on disk that
			// somebody will want to reopen — and the id is dropped on exit
			// otherwise (#218).
			resumeHint(cfg, issue, st)
			return finish(fmt.Errorf("issue #%d: %w", issue, err))
		default:
			results = append(results, spend(st, issueResult{issue: issue, closedNoChange: st.closedNoChange}))
			delete(states, issue)
		}
		if cfg.once {
			log.Println("-once set — exiting after one issue")
			return finish(nil)
		}
	}
}

// spend carries an issue's tally into the result the summary is built from —
// the last moment it is readable, since the state is dropped immediately after.
func spend(st *issueState, r issueResult) issueResult {
	r.cost, r.approximated = st.tally.costUSD, st.tally.approximated
	return r
}

// sessionSpend is what this shift's runs have cost so far: the issues it has
// finished with, plus the ones it put down still holding a tally. The two sets
// are disjoint — an issue leaves states exactly as it enters results — so
// nothing here is counted twice.
func sessionSpend(results []issueResult, states map[int]*issueState) float64 {
	total := 0.0
	for _, r := range results {
		total += r.cost
	}
	for _, st := range states {
		total += st.tally.costUSD
	}
	return total
}

// awaitAnswer decides which of the issues waiting on a human is worth running
// now, and blocks until one of them is. It returns 0 when the queue itself
// moved instead — a label removed by hand, a new issue opened — because
// re-deriving the queue outranks going on waiting.
//
// An issue this drain did not flag itself is run straight away. Its answer may
// already be sitting on the thread — left before this process started, or while
// an earlier one was down — and nothing on GitHub says whether it is. Which
// comment is this drain's own question is exactly what it cannot tell, running
// as it does under the credentials of the person it is asking. One run settles
// it for a price the skill keeps low: it re-reads the thread and stops again
// without re-asking when the answer is not there. From then on this drain holds
// a baseline to compare against, so the question is only paid for once.
func awaitAnswer(ctx context.Context, cfg config, blocked []int, states map[int]*issueState) (int, error) {
	for _, issue := range blocked {
		if st := states[issue]; st == nil || !st.awaiting {
			log.Printf("issue #%d was already labelled %q when this shift reached it — re-running it "+
				"to see whether the answer is on the thread", issue, awaitingAnswerLabel)
			return issue, nil
		}
	}
	log.Printf("nothing else to work — waiting for a reply on %s, next check in %s",
		issueRefs(blocked), cfg.poll)
	if err := sleep(ctx, cfg.poll); err != nil {
		return 0, err
	}
	for _, issue := range blocked {
		comments, err := issueComments(ctx, cfg, issue)
		if err != nil {
			if ctx.Err() != nil {
				return 0, ctx.Err()
			}
			narrate(sevWarning, "transient: checking #%d comments failed (%v) — will retry", issue, err)
			continue
		}
		baseline := states[issue].baseline
		if replyArrived(comments, baseline) {
			log.Printf("somebody replied on #%d — re-running to fold the answers in", issue)
			states[issue].answered = true
			return issue, nil
		}
		if note := botsOnly(comments, baseline); note != "" {
			log.Printf("issue #%d still awaiting a reply%s", issue, note)
		}
	}
	return 0, nil
}

// stillWaiting is what the summary owes an operator about the issues this drain
// put down: each one is flagged on GitHub and waiting on them, and an exit that
// does not name them reads as though they were never reached.
func stillWaiting(states map[int]*issueState) []issueResult {
	var out []issueResult
	for issue, st := range states {
		if st.awaiting {
			out = append(out, spend(st, issueResult{issue: issue, awaiting: true}))
		}
	}
	slices.SortFunc(out, func(a, b issueResult) int { return a.issue - b.issue })
	return out
}

// parkIssue hands one issue back to a person: the label that takes it out of
// the queue, and a comment saying what happened. Both are best-effort — a
// GitHub call that fails must not end a drain that is otherwise healthy — but
// a label that did not take means the next drain retries this issue, so that
// one says so out loud rather than failing quietly.
func parkIssue(ctx context.Context, cfg config, issue int, reason string) {
	n := strconv.Itoa(issue)
	_, err := gh(ctx, cfg, "issue", "edit", n, "--add-label", needsHumanLabel)
	if err != nil {
		// Far the likeliest cause is a repository that has never used the
		// label: GitHub refuses to add one that does not exist yet. A create
		// that fails means the label was already there, so the first failure
		// was something else and retrying the edit would only repeat it.
		if cerr := ensureLabel(ctx, cfg, needsHumanLabel, "D93F0B",
			"polako parked this issue for a human"); cerr == nil {
			_, err = gh(ctx, cfg, "issue", "edit", n, "--add-label", needsHumanLabel)
		}
	}
	if err != nil {
		narrate(sevWarning, "could not label issue #%d %q (%v) — the next shift will pick it up again "+
			"unless you label it yourself or close it", issue, needsHumanLabel, err)
	}
	// Parking supersedes any question still flagged on the issue: what it now
	// waits on is a decision, not a reply. Left up, the flag would also outlive
	// the park — the drain that follows an operator removing needs-human would
	// read it as a question of its own and sit waiting for a comment nobody
	// owes it. Best-effort and silent: the issue is already parked either way.
	_, _ = gh(ctx, cfg, "issue", "edit", n, "--remove-label", awaitingAnswerLabel)
	body := fmt.Sprintf("**polako parked this issue.** %s\n\n"+
		"Nothing will run on it again until the `%s` label is removed — "+
		"`gh issue edit %s --remove-label %s`.", reason, needsHumanLabel, n, needsHumanLabel)
	if _, cerr := gh(ctx, cfg, "issue", "comment", n, "--body", body); cerr != nil {
		narrate(sevWarning, "could not comment on issue #%d (%v) — the reason is in this log and in the exit summary",
			issue, cerr)
	}
}

// parkAndMoveOn is the GitHub-facing half of a park: the narration, the label
// and comment, and the notify. The drain loop has already dropped the issue
// from this shift's queue by the time this runs — split out so the loop body
// reads as one step per line.
func parkAndMoveOn(ctx context.Context, cfg config, issue int, st *issueState, reason string, err error) {
	narrate(sevWarning, "issue #%d needs a human: %s — parking it and moving on", issue, reason)
	// Whatever the park had to say that the issue thread must not carry. Today
	// that is where on this disk the work it left is sitting.
	if aside := parkAsideOf(err); aside != "" {
		log.Printf("issue #%d: %s", issue, aside)
	}
	// A park is exactly when somebody wants to read what the run actually did,
	// and the session is the whole transcript of it.
	resumeHint(cfg, issue, st)
	parkIssue(ctx, cfg, issue, reason)
	// After the park, not before: by now the label and the comment saying why
	// are on the issue, so somebody following the notification finds the whole
	// story there.
	notify(ctx, cfg, notification{event: notifyParked, issue: issue, reason: reason})
}

// finishedContainerMarker tags the comment polako leaves on a container before
// it closes it, so a shift whose close failed can tell, next time round, that
// the explanation is already on the thread and only the close needs retrying.
// Matched as a substring of a comment's body, so rewording the prose around it
// never breaks the check.
const finishedContainerMarker = "<!-- polako: epic finished -->"

// finishedContainerComment is the record of why the close that follows it
// happened, posted first. The wording reports rather than asks: the machine is
// not judging whether the work is done — the children decided that by closing —
// it acts on the near-certain reading that "every child closed" means "the epic
// is finished", which a reopen undoes for one click the rare time it is wrong.
// It says only that the children are closed, which is all the sub-issue rollup
// proves; whether each closed behind a merged PR is not a claim to make on a
// permanent thread. The child issues are not named: containerInfo carries only
// their count, and listing them by number would cost an extra `gh` call per
// container.
func finishedContainerComment(c containerInfo) string {
	return fmt.Sprintf("All %s closed, so I closed this epic. If the children did not add up to "+
		"the design recorded above, reopen it — that costs one click.\n\n%s",
		plural(c.total, "sub-issue"), finishedContainerMarker)
}

// closeFinishedContainers closes every container in scope whose children have
// all closed, commenting on the thread first to say why — a close with no
// explanation on the thread is the thing that would make this feel arbitrary.
// A container a human has held (needs-human, or still proposed) is left alone:
// not commented, not closed, named in the exit summary as theirs to close. That
// opt-out needs no new flag — needs-human on the container already means hands
// off everywhere else, and exclusion beats inclusion the same way
// selectableIssues orders it.
//
// The comment and the close are two different questions and one flag cannot
// answer both. The comment is gated on the marker: the thread is read once per
// shift (the commented memo, mirroring the drain loop's own skip map, keeps it
// to one read) and the comment posted only if it is not already there. The
// close is gated only on the container still being open — which every container
// in this listing is, because the listing is --state open — so a shift that
// commented and then failed to close retries only the close next time rather
// than being turned away by its own marker. That retry is next *shift*, not
// next pass: closedThisShift records every container this shift has already
// tried to close, success or failure, so a single stale listing (GitHub's
// issue list lags a close by seconds) does not fire epic-done twice, and a
// close that keeps failing warns once a shift rather than once a pass.
//
// notifyEpicDone fires once, on the successful close: naturally once-ever, since
// the container is gone from every later listing. A held container fires
// nothing — with no comment there is no durable anchor to fire once against,
// and firing it every shift is exactly the "paged every night about an epic you
// have not closed" failure the marker gate was built to prevent.
//
// Best-effort like parkIssue's own comment: nothing else in the drain reads any
// of this back, so a failed comment or a failed close is a warning and the
// shift carries on — except a context cancellation, the operator stopping the
// process on purpose, which propagates like every other gh call in this loop.
// Returns the containers it closed this call, for the exit summary, and any
// retire issue that close triggered (retire.go) — best-effort in the same
// way, so a failure there never turns a successful container close into a
// drain failure. Never reached on a dry run — dryRun and drain are separate
// paths off run().
func closeFinishedContainers(ctx context.Context, cfg config, containers []containerInfo, commented, closedThisShift map[int]bool) ([]containerInfo, []retiredDoc, error) {
	var closed []containerInfo
	var retired []retiredDoc
	// The documents this call has already retired, moments ago, for an
	// earlier container in this same loop — see retireOrphanedDoc's own
	// comment for why the search alone cannot be trusted to catch this.
	retiredThisCall := map[string]bool{}
	for _, c := range containers {
		if !c.finished() || c.held || closedThisShift[c.number] {
			continue
		}
		if !commented[c.number] {
			comments, err := issueComments(ctx, cfg, c.number)
			if err != nil {
				if ctx.Err() != nil {
					return closed, retired, ctx.Err()
				}
				narrate(sevWarning, "could not read #%d's thread to check for the epic-finished note (%v) — "+
					"will try again next pass", c.number, err)
				continue
			}
			hasMarker := slices.ContainsFunc(comments, func(cm issueComment) bool {
				return strings.Contains(cm.Body, finishedContainerMarker)
			})
			if !hasMarker {
				if _, err := gh(ctx, cfg, "issue", "comment", strconv.Itoa(c.number), "--body", finishedContainerComment(c)); err != nil {
					if ctx.Err() != nil {
						return closed, retired, ctx.Err()
					}
					narrate(sevWarning, "could not comment on finished epic #%d (%v) — will try again next pass", c.number, err)
					continue
				}
			}
			commented[c.number] = true
		}
		if _, err := gh(ctx, cfg, "issue", "close", strconv.Itoa(c.number), "--reason", "completed"); err != nil {
			if ctx.Err() != nil {
				return closed, retired, ctx.Err()
			}
			closedThisShift[c.number] = true
			narrate(sevWarning, "could not close finished epic #%d (%v) — the comment saying why is on the thread; "+
				"the close is retried next shift", c.number, err)
			continue
		}
		closedThisShift[c.number] = true
		closed = append(closed, c)
		log.Printf("epic #%d: all %s closed — commented and closed it", c.number, plural(c.total, "sub-issue"))
		notify(ctx, cfg, notification{event: notifyEpicDone, issue: c.number,
			reason: fmt.Sprintf("all %s closed — closed it", plural(c.total, "sub-issue"))})
		if r, ok, err := retireOrphanedDoc(ctx, cfg, c, retiredThisCall); err != nil {
			return closed, retired, err
		} else if ok {
			retired = append(retired, r)
			retiredThisCall[r.doc] = true
		}
	}
	return closed, retired, nil
}

// ensureLabel declares a label on the repository, and errors if it was already
// there — which is what makes it useful as a test as well as a create. Both
// callers want it best-effort in their own way: preflight ignores the answer,
// and a park reads it to tell "the label did not exist" apart from "the edit
// failed for some other reason".
func ensureLabel(ctx context.Context, cfg config, name, color, description string) error {
	_, err := gh(ctx, cfg, "label", "create", name, "--color", color, "--description", description)
	return err
}

// drainSummary is what the process says on its way out: what merged, what is
// still waiting on an answer, what was parked and why, and how long it all
// took. Returned as lines rather than one blob so each carries the log's own
// timestamp.
//
// The waiting clause appears only when there is something in it. Most drains
// have nothing waiting, and a bucket that is empty on every ordinary run is
// noise in the one line an operator actually reads. Dollars come and go the
// same way: a drain that spent nothing — one that only waited on a PR an
// earlier process opened — would otherwise report "$0.00 spent", which reads
// as a free backlog rather than as an absent number.
//
// closed is the containers this shift closed itself; containers is the most
// recent listing the drain made. An epic earns a line either way, but a
// different one: "closed it" for one polako closed, and the older "close it
// when the design is satisfied" for one still open at the last look — which,
// now that a finished unheld container is closed on sight, means one a human
// held with needs-human or proposed (or, rarely, one whose close failed and
// was already warned about). Scope is decided upstream: a container outside
// `-label` was in neither set to begin with. A container closed just before a
// stop that skips the re-list is in both — named once, from closed.
//
// retired is the retire issues (retire.go) any of those closes triggered —
// keyed by the container that triggered it, so its line prints right after
// that container's own "closed it".
func drainSummary(results []issueResult, containers, closed []containerInfo, retired []retiredDoc, elapsed time.Duration) []string {
	var epics []string
	retiredByContainer := make(map[int]retiredDoc, len(retired))
	for _, r := range retired {
		retiredByContainer[r.container] = r
	}
	closedNums := make(map[int]bool, len(closed))
	for _, c := range closed {
		closedNums[c.number] = true
		epics = append(epics, fmt.Sprintf("  epic    #%d: all %s closed — closed it",
			c.number, plural(c.total, "sub-issue")))
		if r, ok := retiredByContainer[c.number]; ok {
			epics = append(epics, fmt.Sprintf("  retire  #%d: %s — every issue it proposed is closed", r.issue, r.doc))
		}
	}
	for _, c := range containers {
		if c.finished() && !closedNums[c.number] {
			epics = append(epics, fmt.Sprintf("  epic    #%d: all %s closed — close it when the design is satisfied",
				c.number, plural(c.total, "sub-issue")))
		}
	}
	// A shift that touched no issue has nothing to summarize in the usual
	// sense, but a container the queue already shows finished is still worth
	// naming on its own — printing "0 issues merged, 0 issues parked" over it
	// would be exactly the noise this early return exists to avoid.
	if len(results) == 0 {
		return epics
	}
	total, approximated := 0.0, 0
	for _, r := range results {
		total += r.cost
		approximated += r.approximated
	}
	// Per issue only once there is a total to break down, so an uncosted drain
	// prints exactly what it always printed.
	price := func(c float64) string {
		if total <= 0 {
			return ""
		}
		return " (" + usd(c) + ")"
	}
	var merged []string
	var closedNoChange []string
	var waiting []string
	var parked []string
	for _, r := range results {
		switch {
		case r.awaiting:
			waiting = append(waiting, "#"+strconv.Itoa(r.issue)+price(r.cost))
		case r.parked:
			parked = append(parked, fmt.Sprintf("  parked  #%d%s — %s", r.issue, price(r.cost), r.reason))
		case r.closedNoChange:
			closedNoChange = append(closedNoChange, "#"+strconv.Itoa(r.issue)+price(r.cost))
		default:
			merged = append(merged, "#"+strconv.Itoa(r.issue)+price(r.cost))
		}
	}
	head := fmt.Sprintf("summary: %s merged, %s parked",
		plural(len(merged), "issue"), plural(len(parked), "issue"))
	if len(closedNoChange) > 0 {
		head += ", " + plural(len(closedNoChange), "issue") + " closed with no change needed"
	}
	if len(waiting) > 0 {
		head += ", " + plural(len(waiting), "issue") + " awaiting an answer"
	}
	if total > 0 {
		head += ", " + usd(total) + " spent"
		// A run that crashed, stalled or was interrupted never emitted a result
		// event, and pricing belongs to the CLI rather than to this binary — so
		// it contributed nothing to the figure above. Unqualified, the total
		// would read as the whole bill, and the caps that read the same number
		// would look broken rather than conservative.
		if approximated > 0 {
			head += fmt.Sprintf(" (%s reported none, so that is an undercount)",
				plural(approximated, "run"))
		}
	}
	lines := []string{head + ", " + dur(elapsed) + " of wall clock"}
	if len(merged) > 0 {
		lines = append(lines, "  merged  "+strings.Join(merged, ", "))
	}
	if len(closedNoChange) > 0 {
		lines = append(lines, "  closed  "+strings.Join(closedNoChange, ", "))
	}
	if len(waiting) > 0 {
		lines = append(lines, "  waiting "+strings.Join(waiting, ", ")+
			" — reply on the thread and the next shift picks them up")
	}
	lines = append(lines, parked...)
	return append(lines, epics...)
}

// issueRefs renders a list of issue numbers the way the rest of the output
// spells them.
func issueRefs(numbers []int) string {
	refs := make([]string, len(numbers))
	for i, n := range numbers {
		refs[i] = "#" + strconv.Itoa(n)
	}
	return strings.Join(refs, ", ")
}

// pickLowest returns the smallest number not in skip, or 0 if none remain.
func pickLowest(numbers []int, skip map[int]bool) int {
	lowest := 0
	for _, n := range numbers {
		if skip[n] {
			continue
		}
		if lowest == 0 || n < lowest {
			lowest = n
		}
	}
	return lowest
}

// logHeldBack narrates every issue this pass is putting down for an open
// blocker — once per issue no matter how many blockers it names, since
// openIssues is called once per pass and this is called once per that call.
// -skip already told the operator once about a number they typed themselves,
// so it is quiet about those.
func logHeldBack(heldBack []heldBackInfo, skip map[int]bool) {
	for _, h := range heldBack {
		if skip[h.number] {
			continue
		}
		log.Printf("issue #%d blocked by %s — skipping this pass", h.number, issueRefs(h.blockers))
	}
}

// issueOpenState reads one issue's state ("OPEN" or "CLOSED") off GitHub,
// retried the way every other read-only lookup on this loop is — see
// retryRead. Shared by ensureIssueClosed (a PR merged, so "Closes #N" should
// have already closed it — belt and braces) and dispatchRun's check for the
// fourth ending (#210): a run that closed its own issue directly, verifying
// the code needed no change, rather than opening a PR.
func issueOpenState(ctx context.Context, cfg config, issue int) (string, error) {
	out, err := retryRead(ctx, cfg, fmt.Sprintf("reading #%d's state", issue), func() ([]byte, error) {
		return gh(ctx, cfg, "issue", "view", strconv.Itoa(issue), "--json", "state")
	})
	if err != nil {
		return "", err
	}
	var v struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return "", fmt.Errorf("parsing issue state: %w", err)
	}
	return v.State, nil
}

func ensureIssueClosed(ctx context.Context, cfg config, issue, prNumber int) error {
	state, err := issueOpenState(ctx, cfg, issue)
	if err != nil {
		return err
	}
	if state == "OPEN" { // "Closes #N" normally handles this; belt and braces
		_, err = gh(ctx, cfg, "issue", "close", strconv.Itoa(issue),
			"--comment", fmt.Sprintf("Shipped in #%d.", prNumber))
		return err
	}
	return nil
}
