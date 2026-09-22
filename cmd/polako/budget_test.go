package main

import (
	"strings"
	"testing"
	"time"
)

// The caps are off unless somebody sets one, which is the whole of what keeps
// every existing drain behaving as it did — and each reports in the words the
// park comment and the summary go on to carry.
func TestOverBudgetOnlySpeaksWhenACapIsSet(t *testing.T) {
	t.Parallel()
	spent := issueTally{costUSD: 9, wallMS: (2 * time.Hour).Milliseconds()}

	if got := overBudget(config{}, spent); got != "" {
		t.Errorf("overBudget with no caps = %q, want silence — the default must not park anything", got)
	}
	if got := overBudget(config{maxCost: 20, maxIssueTime: 3 * time.Hour}, spent); got != "" {
		t.Errorf("overBudget under both caps = %q, want silence", got)
	}
	cost := overBudget(config{maxCost: 5}, spent)
	if !strings.Contains(cost, "$9.00") || !strings.Contains(cost, "-max-cost of $5.00") {
		t.Errorf("cost breach = %q, want it to quote what was spent and the cap it broke", cost)
	}
	clock := overBudget(config{maxIssueTime: time.Hour}, spent)
	if !strings.Contains(clock, "2h") || !strings.Contains(clock, "-max-issue-time of 1h") {
		t.Errorf("time breach = %q, want it to quote the run time and the cap it broke", clock)
	}
	// Exactly at the cap counts as spent: a cap of $5 that permits a run once
	// $5 is gone is a cap on nothing.
	if got := overBudget(config{maxCost: 9}, spent); got == "" {
		t.Error("spending the cap exactly must count as reaching it")
	}
}

// The limit handed to a run is what the cap has left, not the cap: an issue on
// its fourth run does not get the whole allowance over again.
func TestRunLimitLeavesOnlyWhatTheIssueHasLeft(t *testing.T) {
	t.Parallel()
	spent := issueTally{wallMS: (20 * time.Minute).Milliseconds()}

	if got := runLimit(config{}, spent); got != 0 {
		t.Errorf("runLimit with the cap off = %v, want 0 for unbounded", got)
	}
	if got := runLimit(config{maxIssueTime: time.Hour}, spent); got != 40*time.Minute {
		t.Errorf("runLimit = %v, want 40m of a 1h cap after 20m of runs", got)
	}
	// Only reachable if a caller skipped overBudget; a limit of zero would read
	// as unbounded, which is the one answer an exhausted issue must not get.
	if got := runLimit(config{maxIssueTime: time.Minute}, spent); got <= 0 {
		t.Errorf("runLimit past the cap = %v, want a positive floor rather than unbounded", got)
	}
}

// A cap set in a shell profile is still a cap, so startup names the ones in
// force rather than leaving a park to quote a flag nobody typed.
func TestCapNotesNameEveryCapInForce(t *testing.T) {
	t.Parallel()
	if got := capNotes(config{}); got != "" {
		t.Errorf("capNotes with no caps = %q, want nothing said", got)
	}
	got := capNotes(config{maxCost: 15, maxIssueTime: 90 * time.Minute, maxSessionCost: 200,
		maxSessionUsage: 80, maxWeekUsage: 90})
	for _, want := range []string{"-max-cost $15.00", "-max-issue-time 1h30m", "-max-session-cost $200.00",
		"-max-session-usage 80%", "-max-week-usage 90%"} {
		if !strings.Contains(got, want) {
			t.Errorf("capNotes = %q, missing %q", got, want)
		}
	}
}
