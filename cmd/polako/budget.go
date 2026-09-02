package main

// The spend caps, checked between runs and never inside one: -max-cost and
// -max-issue-time bound a single issue (overBudget / runLimit), and the usage
// gate pauses the shift while the plan's own session or week pool is over the
// configured percent. A cap is a ceiling on observed spend — a run that never
// reported a cost is counted as zero.

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"
)

// overBudget reports which per-issue cap an issue has reached, in the words the
// park comment and the exit summary go on to carry — or "" while both caps are
// off (maxCost's default; maxIssueTime is off only under -max-issue-time 0),
// or while neither has been reached.
//
// It gates work about to be dispatched and never work already done. A run that
// overspent and opened a PR leaves an issue this process has finished with, and
// waiting for a human to merge costs nothing more; parking there would hand
// back an issue whose work is sitting on GitHub ready to go.
//
// Both figures undercount, in the one direction that matters: a run that
// crashed, stalled or was interrupted never emitted a result event, so it
// reports no cost at all and its duration is timed from the clock instead. A
// cap is therefore a ceiling on what was *observed*, and a drain that keeps
// dying spends more than either number admits.
func overBudget(cfg config, t issueTally) string {
	if cfg.maxCost > 0 && t.costUSD >= cfg.maxCost {
		return fmt.Sprintf("this shift has spent %s on it, the whole of its -max-cost of %s",
			usd(t.costUSD), usd(cfg.maxCost))
	}
	if cfg.maxIssueTime > 0 && runTime(t) >= cfg.maxIssueTime {
		return fmt.Sprintf("its runs have taken %s, the whole of its -max-issue-time of %s",
			dur(runTime(t)), dur(cfg.maxIssueTime))
	}
	return ""
}

// runLimit is how long a run dispatched now may take before -max-issue-time is
// spent. Zero means unbounded — the default, and what every run got before the
// cap existed.
//
// The floor is defensive only: every caller asks overBudget first, and an issue
// with nothing left to spend parks there rather than reaching this.
func runLimit(cfg config, t issueTally) time.Duration {
	if cfg.maxIssueTime <= 0 {
		return 0
	}
	return max(cfg.maxIssueTime-runTime(t), time.Millisecond)
}

// runTime is how long this shift's runs on one issue have taken.
func runTime(t issueTally) time.Duration { return time.Duration(t.wallMS) * time.Millisecond }

// capNotes names the caps in force, for the startup line. Worth saying out
// loud because the environment can set any flag: a park whose reason names a
// -max-cost nobody typed is a mystery, and this is where it stops being one.
func capNotes(cfg config) string {
	var parts []string
	if cfg.maxCost > 0 {
		parts = append(parts, "-max-cost "+usd(cfg.maxCost)+" per issue")
	}
	if cfg.maxIssueTime > 0 {
		parts = append(parts, "-max-issue-time "+dur(cfg.maxIssueTime)+" of run time per issue")
	}
	if cfg.maxSessionCost > 0 {
		parts = append(parts, "-max-session-cost "+usd(cfg.maxSessionCost)+" for this shift")
	}
	if cfg.maxSessionUsage > 0 {
		parts = append(parts, fmt.Sprintf("-max-session-usage %d%% for this shift", cfg.maxSessionUsage))
	}
	if cfg.maxWeekUsage > 0 {
		parts = append(parts, fmt.Sprintf("-max-week-usage %d%% for this shift", cfg.maxWeekUsage))
	}
	return strings.Join(parts, ", ")
}

// usageGateOn is whether either usage cap is set. Every probe call this file
// makes on the usage gate's behalf is guarded by it, so a shift that sets
// neither flag pays no cost for a feature it never asked for — no extra
// `claude -p /usage` exec, no new log line, behaviour byte-for-byte what it
// was before this gate existed.
func usageGateOn(cfg config) bool {
	return cfg.maxSessionUsage > 0 || cfg.maxWeekUsage > 0
}

// usageGateReason reports whether the plan's own usage has crossed a
// configured threshold and, if so, how long to wait before looking again and
// the line that explains the wait. A crossed threshold is waited out, never a
// stop and never a park — the same doctrine as the mid-run refusal in
// processIssue (behaviour.md, "A session limit is waited out, not retried
// against"): the limit is a fact about the account, not about any one issue,
// so the queue keeps and the shift resumes once the pool resets. Starting
// already over the ceiling therefore waits rather than producing nothing
// (issue #245). A probe that cannot answer never trips the gate — same
// direction every uncertainty here resolves in, as with authFailure and
// lacksCommand — and neither does a gated pool the probe's answer simply did
// not include.
//
// This probes independently of sampleWeekUsage's own call inside processIssue
// a few lines below, rather than sharing one snapshot for both: an issue
// dispatched off the "awaiting answer" path can follow this check by minutes
// (awaitAnswer sleeps up to -poll before it returns one), so a cached reading
// handed to that issue's pickup sample would silently go stale for exactly
// the case sampling most needs to get right. The probe itself is cheap and
// meant to be called routinely (see probeUsage's own doc comment), so the
// extra exec buys correctness rather than costing anything worth trading it
// away for.
func usageGateReason(ctx context.Context, cfg config) (time.Duration, string, bool) {
	if !usageGateOn(cfg) {
		return 0, "", false
	}
	snap, ok := probeUsage(ctx, cfg)
	if !ok {
		log.Print("usage gate: could not read the plan's usage this pass " +
			"(an older claude with no /usage, or an unparseable reply) — " +
			"proceeding as if -max-session-usage and -max-week-usage were unset until the next check")
		return 0, "", false
	}
	if cfg.maxSessionUsage > 0 {
		if pool, found := poolByLabel(snap.pools, "session"); found && pool.percent >= cfg.maxSessionUsage {
			wait, reason := usageGateWait("session", "-max-session-usage", cfg.maxSessionUsage, pool, cfg.poll)
			return wait, reason, true
		}
	}
	if cfg.maxWeekUsage > 0 {
		if pool, found := poolByLabel(snap.pools, "week"); found && pool.percent >= cfg.maxWeekUsage {
			wait, reason := usageGateWait("week", "-max-week-usage", cfg.maxWeekUsage, pool, cfg.poll)
			return wait, reason, true
		}
	}
	return 0, "", false
}

// usageGateWait turns a tripped pool into the pause before the next probe and
// the line that explains it. A readable reset clause is waited out in one
// sleep, with the same 90s slack behind the CLI's own clock the mid-run
// refusal allows (limitReset's callers). A pool whose reset this cannot read,
// or one whose named reset has already passed while the meter still reads
// over, falls back to one -poll and another look — the mirror of limitReset's
// own unreadable-clock fallback, and bounded in real time either way because
// the pool resets on its own.
func usageGateWait(label, flagName string, threshold int, pool usagePool, poll time.Duration) (time.Duration, string) {
	if pool.hasReset {
		if until := time.Until(pool.reset); until > 0 {
			wait := until + 90*time.Second
			return wait, fmt.Sprintf(
				"this shift's %s usage is at %d%%, at or over its %s of %d%% — waiting %s for it "+
					"to reset (%s), then carrying on where this left off "+
					"(Ctrl+C is safe: everything is on GitHub)",
				label, pool.percent, flagName, threshold, dur(wait), formatUsageReset(pool.reset))
		}
	}
	return poll, fmt.Sprintf(
		"this shift's %s usage is at %d%%, at or over its %s of %d%%, and no upcoming reset this "+
			"can wait out — re-checking every %s until it drops "+
			"(Ctrl+C is safe: everything is on GitHub)",
		label, pool.percent, flagName, threshold, dur(poll))
}

// sampleWeekUsage is the one-line "what does the plan's week usage read right
// now" primitive processIssue's pickup and terminal sampling both need.
// Always returns a fresh reading — off (usageGateOn false), a failed probe,
// and a snapshot with no "week" pool all collapse to (0, false) alike, so a
// caller that assigns both return values unconditionally can never be left
// holding a previous call's stale answer.
func sampleWeekUsage(ctx context.Context, cfg config) (int, bool) {
	if !usageGateOn(cfg) {
		return 0, false
	}
	snap, ok := probeUsage(ctx, cfg)
	if !ok {
		return 0, false
	}
	pool, found := poolByLabel(snap.pools, "week")
	if !found {
		return 0, false
	}
	return pool.percent, true
}
