package main

// The two waits on a human's reply: waitForReply, for one issue under
// -strict-order or design -wait, and awaitAnswer, for whichever of the issues
// the drain put down answers first. Both peek between full reads (peek.go).

import (
	"context"
	"fmt"
	"time"
)

func waitForReply(ctx context.Context, cfg config, issue int, baseline int64) error {
	watch := []*etagWatch{issueWatch(issue)}
	deadline := time.Now().Add(cfg.poll)
	for {
		changed, err := waitForChange(ctx, cfg, deadline, watch)
		if err != nil {
			return err
		}
		full := len(changed) == 0
		if full {
			deadline = time.Now().Add(cfg.poll)
		}
		comments, err := issueComments(ctx, cfg, issue)
		if err != nil {
			if full {
				cfg.narrate(sevWarning, "transient: checking #%d comments failed (%v) — will retry", issue, err)
			}
			continue
		}
		if replyArrived(comments, baseline) {
			return nil
		}
		// An early wake that wasn't a reply — a bot, a label, an edit — says
		// so only when it was a bot, and otherwise waits out the full poll
		// without a line per peek.
		if note := botsOnly(comments, baseline); full || note != "" {
			cfg.logf("issue #%d still awaiting a reply%s — %s", issue, note, nextCheck(cfg))
		}
	}
}

// botsOnly says out loud that the thread moved and it still was not an answer.
// Without it a filtered-out comment is invisible: the log repeats "still
// awaiting a reply" while GitHub plainly shows new comments, and the honest
// reading of that is that the drain is broken.
func botsOnly(comments []issueComment, baseline int64) string {
	n := 0
	for _, c := range comments {
		if c.ID > baseline && c.fromBot() {
			n++
		}
	}
	if n == 0 {
		return ""
	}
	return fmt.Sprintf(" (%d new comment(s), none of them from a person)", n)
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
			cfg.logf("issue #%d was already labelled %q when this shift reached it — re-running it "+
				"to see whether the answer is on the thread", issue, awaitingAnswerLabel)
			return issue, nil
		}
	}
	cfg.logf("nothing else to work — waiting for a reply on %s, %s",
		issueRefs(blocked), nextCheck(cfg))
	watches := make([]*etagWatch, len(blocked))
	for i, issue := range blocked {
		if states[issue].thread == nil {
			states[issue].thread = issueWatch(issue)
		}
		watches[i] = states[issue].thread
	}
	deadline := time.Now().Add(cfg.poll)
	for {
		changed, err := waitForChange(ctx, cfg, deadline, watches)
		if err != nil {
			return 0, err
		}
		// An early wake reads only the threads that moved; the full check at
		// the deadline reads them all, then hands back to the drain so the
		// queue is re-derived at least every -poll, as before.
		toRead := blocked
		if len(changed) > 0 {
			toRead = nil
			for _, i := range changed {
				toRead = append(toRead, blocked[i])
			}
		}
		if issue, err := replyOn(ctx, cfg, toRead, states, len(changed) == 0); issue != 0 || err != nil {
			return issue, err
		}
		if len(changed) == 0 {
			return 0, nil
		}
	}
}

// replyOn reads each issue's thread and returns the first one a person has
// answered. full is the -poll check, the only one that reports a read that
// failed — a failed read after a free check is left to the next full one.
func replyOn(ctx context.Context, cfg config, issues []int, states map[int]*issueState, full bool) (int, error) {
	for _, issue := range issues {
		comments, err := issueComments(ctx, cfg, issue)
		if err != nil {
			if ctx.Err() != nil {
				return 0, ctx.Err()
			}
			if full {
				cfg.narrate(sevWarning, "transient: checking #%d comments failed (%v) — will retry", issue, err)
			}
			continue
		}
		baseline := states[issue].baseline
		if replyArrived(comments, baseline) {
			cfg.logf("somebody replied on #%d — re-running to fold the answers in", issue)
			states[issue].answered = true
			return issue, nil
		}
		if note := botsOnly(comments, baseline); note != "" {
			cfg.logf("issue #%d still awaiting a reply%s", issue, note)
		}
	}
	return 0, nil
}
