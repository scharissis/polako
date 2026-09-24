package main

// Holding a pickup whose own fetch git refused on auth (issue #595). Since
// #493 the skill stops at its own failed fetch, so a run started after one only
// works if the agent recovers in the minute before that — and on 2026-09-23
// three in a row didn't, each parking a healthy issue. So no run starts:
// processIssue returns errFetchAuthHeld, and the drain waits a -poll and tries
// the pickup again.

import (
	"context"
	"errors"
	"fmt"
)

// errFetchAuthHeld is processIssue's exit when this pickup's own fetch
// couldn't authenticate and there is no PR to wait on: no run started, nothing
// parked. The drain waits a -poll and picks up again (shift.authHold); design,
// a one-issue verb, stops on it.
var errFetchAuthHeld = errors.New("polako's own git fetch could not authenticate, so no run was started — " +
	"fix git access in -dir (`ssh-add -l`, or an https remote), then start it again")

// authHoldLimit is how many auth-failed pickups in a row the drain waits out
// before it stops the shift. On 2026-09-23 each pickup's fetch spent about
// three minutes in retries, so three pickups and the two -polls between them
// come to about twenty minutes at the defaults — longer than that day's
// fifteen-minute outage, short enough that a dead agent doesn't hold the shift
// all night.
const authHoldLimit = 3

// authHold is the drain's answer to errFetchAuthHeld: the issue stays in the
// queue with nothing parked, and the pickup is tried again after a -poll —
// until authHoldLimit in a row, when a still-failing fetch is no longer a
// blip and the shift stops the way a dead remote stops it.
func (s *shift) authHold(ctx context.Context, issue int) error {
	s.authFailures++
	s.authStreak++
	if s.authStreak >= authHoldLimit {
		return fetchOriginError(s.cfg.dir, fmt.Errorf("polako's own git fetch failed to authenticate at %s in a row",
			plural(s.authStreak, "pickup")))
	}
	s.cfg.narrate(sevWarning, "issue #%d: no run started and nothing parked, since polako's own git fetch "+
		"could not authenticate — trying the pickup again in %s (%d of %d before the shift stops)",
		issue, s.cfg.poll, s.authStreak, authHoldLimit)
	return sleep(ctx, s.cfg.poll)
}

// authSummaryLine is the exit summary's one auth line — once for the shift
// rather than repeated per issue, so an operator sees one number.
func authSummaryLine(n int) string {
	return fmt.Sprintf("  auth    polako's own git fetch failed to authenticate at %s this shift "+
		"— no run started after one; check git access in -dir if it keeps happening", plural(n, "pickup"))
}
