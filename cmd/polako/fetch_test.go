package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// refLockErr is git's own wording for a fetch that lost the race to update a
// remote-tracking ref, as capture wraps it.
var refLockErr = fmt.Errorf("git fetch origin --quiet: exit status 1: error: cannot lock ref " +
	"'refs/remotes/origin/main': is at 9c49155e0a1b2c3d4e5f60718293a4b5c6d7e8f9 but expected " +
	"d32d345e0a1b2c3d4e5f60718293a4b5c6d7e8f9\n ! d32d345..0869031  main -> origin/main  (unable to update local ref)")

func TestGitRefLockRace(t *testing.T) {
	t.Parallel()
	if !gitRefLockRace(refLockErr) {
		t.Errorf("gitRefLockRace missed git's own ref-lock message:\n%v", refLockErr)
	}
	for _, msg := range []string{
		"git@github.com: Permission denied (publickey).\nfatal: Could not read from remote repository.",
		"fatal: '/tmp/gone' does not appear to be a git repository",
		// A stale lock file: retrying at once won't clear it, so it isn't the race.
		"error: cannot lock ref 'refs/remotes/origin/main': Unable to create '.git/refs/remotes/origin/main.lock': File exists.",
	} {
		if gitRefLockRace(fmt.Errorf("%s", msg)) {
			t.Errorf("gitRefLockRace matched an unrelated failure:\n%s", msg)
		}
	}
}

// fakeFetch fails with errs in order, then succeeds, counting calls.
func fakeFetch(calls *int, errs ...error) func() ([]byte, error) {
	return func() ([]byte, error) {
		*calls++
		if *calls <= len(errs) {
			return nil, errs[*calls-1]
		}
		return nil, nil
	}
}

// The race heals on the next try, so it's retried at once and logged as
// detail — no "transient:" warning, no wait.
func TestRetryFetchRetriesARefLockRaceAtOnce(t *testing.T) {
	t.Parallel()
	buf := captureLog(t)
	calls := 0
	err := retryFetch(context.Background(), config{ui: testUI(t), ghRetryWait: 1}, fakeFetch(&calls, refLockErr))
	if err != nil {
		t.Fatalf("err = %v, want the retry to succeed", err)
	}
	if calls != 2 {
		t.Errorf("fetch ran %d times, want 2", calls)
	}
	if strings.Contains(buf.String(), "transient:") {
		t.Errorf("a ref-lock race logged as a warning:\n%s", buf)
	}
	if !strings.Contains(buf.String(), "raced another git process") {
		t.Errorf("the race left no detail line:\n%s", buf)
	}
}

// A race that keeps failing isn't swallowed: it goes through retryRead's
// usual warning path and, past the budget, is returned.
func TestRetryFetchReportsAPersistentRefLockRace(t *testing.T) {
	t.Parallel()
	buf := captureLog(t)
	calls := 0
	errs := make([]error, 2*ghReads)
	for i := range errs {
		errs[i] = refLockErr
	}
	err := retryFetch(context.Background(), config{ui: testUI(t), ghRetryWait: 1}, fakeFetch(&calls, errs...))
	if err == nil || !gitRefLockRace(err) {
		t.Fatalf("err = %v, want the race returned once the budget is spent", err)
	}
	if calls != 2*ghReads {
		t.Errorf("fetch ran %d times, want %d", calls, 2*ghReads)
	}
	if got := strings.Count(buf.String(), "transient: git fetch origin failed"); got != ghReads-1 {
		t.Errorf("%d transient warnings, want %d\n%s", got, ghReads-1, buf)
	}
}

// Anything else keeps today's path: one try per retryRead attempt, warned.
func TestRetryFetchLeavesOtherFailuresAlone(t *testing.T) {
	t.Parallel()
	buf := captureLog(t)
	calls := 0
	dead := fmt.Errorf("fatal: '/tmp/gone' does not appear to be a git repository")
	if err := retryFetch(context.Background(), config{ui: testUI(t), ghRetryWait: 1}, fakeFetch(&calls, dead)); err != nil {
		t.Fatalf("err = %v, want the second attempt to succeed", err)
	}
	if calls != 2 {
		t.Errorf("fetch ran %d times, want 2", calls)
	}
	if !strings.Contains(buf.String(), "transient: git fetch origin failed") {
		t.Errorf("an ordinary failure skipped the warning:\n%s", buf)
	}
	if strings.Contains(buf.String(), "raced another git process") {
		t.Errorf("an ordinary failure was taken for the race:\n%s", buf)
	}
}
