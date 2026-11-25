package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// processIssue holds the switch that decides resume against wait against give
// up, and it is the most intricate thing in the package. The drain tests in
// drain_test.go reach most of its branches, but each one reaches them incidentally, through
// whichever route the issue it was really about happened to take. These drive
// the function itself, one case per branch, so a branch that stops behaving
// fails by name rather than by whichever end-to-end test noticed first.
//
// Runs are counted off the init event the supervisor logs for every
// invocation, and the resume is read out of the same log — no state the fakes
// would have to keep just to be asked about.
func TestProcessIssueDecidesWhatOneRunLeftBehind(t *testing.T) {
	t.Parallel()
	// Every resume the ceiling allows costs a fake claude process and the gh
	// calls around it, so the shipped 20 spends over a minute under -race and
	// ran the bound below out on CI. Three proves the same thing: it is past
	// -retries 1, so only the reset can reach it and only the ceiling stops it.
	const ceiling = 3
	open := func() map[string]*fakeIssue { return map[string]*fakeIssue{"1": {Open: true}} }
	parkedFor := func(t *testing.T, err error) string {
		t.Helper()
		reason, parked := parkReason(err)
		if !parked {
			t.Fatalf("err = %v, want the issue handed back to a human", err)
		}
		return reason
	}

	for _, tc := range []struct {
		name  string
		mode  string
		state *ghState
		// tune takes the subtest's *testing.T because several of these branches
		// turn on what git says about the workspace, which means building a real
		// checkout for the case rather than only setting a field.
		tune func(*testing.T, *config)
		// runs is how many claude invocations this decision should dispatch.
		// Zero is the interesting value: it is the restart-safety guarantee.
		runs  int
		check func(t *testing.T, err error, st *ghState, out string)
	}{
		{
			// The restart-safety invariant: a PR on the branch means the work is
			// done, whatever this process remembers, so the skill is never run
			// again for it. Everything else here is about runs; this is about the
			// one that must not happen.
			name: "a PR already open is supervised rather than re-run",
			mode: "stream",
			state: &ghState{
				Issues: open(),
				PRs: map[string]*fakePR{"issue-1": {
					Number: 9, State: "OPEN", Mergeable: "MERGEABLE",
					Head: "abc123", Checks: []string{"SUCCESS"}, MergeOnRead: 1,
				}},
			},
			runs: 0,
			check: func(t *testing.T, err error, st *ghState, out string) {
				if err != nil {
					t.Errorf("err = %v, want a merge to finish the issue", err)
				}
				if st.Issues["1"].Open {
					t.Error("issue 1 should have been closed once its PR merged")
				}
			},
		},
		{
			name:  "a run that asked something hands the issue back",
			mode:  "asks",
			state: &ghState{Issues: open(), Labels: []string{awaitingAnswerLabel}},
			runs:  1,
			check: func(t *testing.T, err error, st *ghState, out string) {
				deferred, ok := deferReason(err)
				if !ok {
					t.Fatalf("err = %v, want the issue put down for a human", err)
				}
				// Read after the question was posted, so a reply is anything past
				// it — a baseline of zero would make the question itself the reply.
				if deferred.baseline != 1 {
					t.Errorf("baseline = %d, want the question already counted", deferred.baseline)
				}
				if !slices.Contains(st.Issues["1"].Labels, awaitingAnswerLabel) {
					t.Error("the flag is the only durable trace of the question; it must be left up")
				}
			},
		},
		{
			name:  "a crash resumes the session it died in and ships",
			mode:  "crashthenships",
			state: &ghState{Issues: open()},
			tune:  func(_ *testing.T, cfg *config) { cfg.retries = 2 },
			runs:  2,
			check: func(t *testing.T, err error, st *ghState, out string) {
				if err != nil {
					t.Errorf("err = %v, want the resume to finish the issue", err)
				}
				// By ID: the research the dead run did is the whole reason to
				// resume rather than start the issue over.
				if !strings.Contains(out, "resuming session sess-crash") {
					t.Errorf("want the crashed session resumed by id\ngot:\n%s", out)
				}
				if st.Issues["1"].Open {
					t.Error("issue 1 should have been closed once its PR merged")
				}
			},
		},
		{
			// The resume target is not a fact about the world: the session can
			// be gone — its JSONL truncated by a hard kill, or aged out of the
			// CLI's retention — and then every attempt fails on it identically
			// in seconds and the issue parks with the wrong diagnosis. The fresh
			// run is the one that would have worked, so it has to be reached.
			name:  "a session that cannot be resumed falls back to a fresh run",
			mode:  "deadsession",
			state: &ghState{Issues: open()},
			tune:  func(_ *testing.T, cfg *config) { cfg.retries = 3 },
			// Two init events: the fresh run that crashes, and the fresh run
			// that ships. The dead resume between them emits none — which is
			// exactly what the supervisor reads it by.
			runs: 2,
			check: func(t *testing.T, err error, st *ghState, out string) {
				if err != nil {
					t.Errorf("err = %v, want the fresh fallback to finish the issue", err)
				}
				if !strings.Contains(out, "session sess-crash could not be resumed") {
					t.Errorf("want the dead session named and given up on\ngot:\n%s", out)
				}
				if !strings.Contains(out, "restarting fresh") {
					t.Errorf("want the attempt after it announced as a fresh run\ngot:\n%s", out)
				}
				if st.Issues["1"].Open {
					t.Error("issue 1 should have been closed once its PR merged")
				}
			},
		},
		{
			// -retries is there to stop a session that resumes and dies straight
			// back. A run cut off after real work — a laptop that slept, a
			// network that dropped — is not that, and charging it for one parks
			// perfectly healthy issues. So progress resets the budget, and a
			// separate ceiling is what guarantees the loop still ends.
			name:  "a crash that got work done first does not spend the retry budget",
			mode:  "partial",
			state: &ghState{Issues: open()},
			tune:  func(_ *testing.T, cfg *config) { cfg.retries, cfg.resumeCeiling = 1, ceiling },
			// One fresh run and every resume the ceiling allows. The old counter
			// would have stopped at two.
			runs: 1 + ceiling,
			check: func(t *testing.T, err error, st *ghState, out string) {
				want := fmt.Sprintf("claude has been retried %d times on this issue and still has "+
					"not finished it — each run gets somewhere and then dies, which needs a human",
					ceiling)
				if got := parkedFor(t, err); got != want {
					t.Errorf("park reason = %q, want %q", got, want)
				}
				if !strings.Contains(out, "the -retries budget starts over") {
					t.Errorf("want the reset said out loud, since -retries 1 would not explain "+
						"%d runs on its own\ngot:\n%s", 1+ceiling, out)
				}
				// The invocation, not only the announcement. Reading "resume this
				// session or start fresh" off the same counter the reset zeroes
				// makes every one of these retries a silent fresh run: the log
				// still promises the resume, and the crashed session's context is
				// thrown away on the one path that exists to keep it.
				if !strings.Contains(out, "--resume sess-partial") {
					t.Errorf("want the announced resume actually made\ngot:\n%s", out)
				}
			},
		},
		{
			name:  "a crash with the resumes spent parks",
			mode:  "crash",
			state: &ghState{Issues: open()},
			tune:  func(_ *testing.T, cfg *config) { cfg.retries = 1 },
			runs:  2, // the fresh attempt and the one resume it was allowed
			check: func(t *testing.T, err error, st *ghState, out string) {
				if got, want := parkedFor(t, err), "claude crashed and 1 resume attempts failed"; got != want {
					t.Errorf("park reason = %q, want %q", got, want)
				}
			},
		},
		{
			// Issue #166: a --resume the CLI kills on arrival still emits one
			// assistant event before it dies, and that alone used to count as
			// progress — progressed() read any assistant event as work done,
			// so fruitless was zeroed every attempt and -retries never bit.
			// Fixed, this parks at -retries rather than riding the resume
			// ceiling all the way to 20.
			name:  "a resume crash-loop that never does real work spends -retries, not the ceiling",
			mode:  "deathrattle",
			state: &ghState{Issues: open()},
			tune:  func(_ *testing.T, cfg *config) { cfg.retries = 3 },
			runs:  1 + 3, // the fresh attempt and every resume -retries allowed
			check: func(t *testing.T, err error, st *ghState, out string) {
				if got, want := parkedFor(t, err), "claude crashed and 3 resume attempts failed"; got != want {
					t.Errorf("park reason = %q, want %q", got, want)
				}
			},
		},
		{
			// -retries has no enforced ceiling of its own, so a value set
			// above resumeCeiling can still let a pure death-rattle crash
			// loop reach the ceiling with zero progress ever observed. The
			// ceiling's park sentence must not claim one of them got
			// somewhere in that case.
			name:  "a crash loop with no progress ever reaches the ceiling honestly",
			mode:  "deathrattle",
			state: &ghState{Issues: open()},
			tune:  func(_ *testing.T, cfg *config) { cfg.retries, cfg.resumeCeiling = 100, ceiling },
			runs:  1 + ceiling,
			check: func(t *testing.T, err error, st *ghState, out string) {
				got := parkedFor(t, err)
				if strings.Contains(got, "gets somewhere and then dies") {
					t.Errorf("park reason = %q, want no claim of progress that was never observed", got)
				}
				want := fmt.Sprintf("claude has been retried %d times on this issue and still has "+
					"not finished it — every attempt has died before doing any observable work, "+
					"which needs a human", ceiling)
				if got != want {
					t.Errorf("park reason = %q, want %q", got, want)
				}
			},
		},
		{
			// The other resume flavour must count too: a clean exit with
			// real commits or a dirty worktree is stronger evidence of
			// progress than progressed() itself asks for, and it spends the
			// same resumes counter the crash arm's ceiling message reads
			// (see resumeLedger's comment in issue.go on what each
			// counter bounds). One in the middle of an otherwise pure death-rattle
			// crash loop must still make the ceiling's sentence say a run
			// got somewhere.
			name:  "a clean-exit resume with real work counts toward the ceiling's honesty too",
			mode:  "deathrattlemixed",
			state: &ghState{Issues: open()},
			tune: func(t *testing.T, cfg *config) {
				leftBehind(t, cfg)
				cfg.retries, cfg.resumeCeiling = 100, ceiling
			},
			runs: 1 + ceiling, // fresh crash, the clean-exit resume, two more crashes
			check: func(t *testing.T, err error, st *ghState, out string) {
				want := fmt.Sprintf("claude has been retried %d times on this issue and still has "+
					"not finished it — each run gets somewhere and then dies, which needs a human", ceiling)
				if got := parkedFor(t, err); got != want {
					t.Errorf("park reason = %q, want %q", got, want)
				}
			},
		},
		{
			// Nothing crashed, nothing was asked, nothing was produced, and
			// there is nothing on the branch or in the worktree either. That is
			// the clean exit that really did decide nothing, and a machine
			// cannot tell what it means, so it says so rather than retrying its
			// way around it.
			name:  "a clean exit that produced nothing parks",
			mode:  "stream",
			state: &ghState{Issues: open()},
			tune:  func(_ *testing.T, cfg *config) { cfg.retries = 2 },
			runs:  1, // nothing to salvage, so nothing to resume
			check: func(t *testing.T, err error, st *ghState, out string) {
				if got, want := parkedFor(t, err), "the run completed without opening a PR"; got != want {
					t.Errorf("park reason = %q, want %q", got, want)
				}
			},
		},
		{
			// Issue #42, which is what this whole branch exists for: the run
			// implemented the change, started something in the background, ended
			// its turn expecting to be brought back — and under headless
			// `claude -p` that ends the process. It had not decided nothing; it
			// had decided to wait. Resuming it costs one run, and not resuming it
			// cost a person half an hour of finishing the job by hand.
			name:  "a clean exit that left work behind is resumed rather than parked",
			mode:  "waitsthenships",
			state: &ghState{Issues: open()},
			tune: func(t *testing.T, cfg *config) {
				leftBehind(t, cfg)
				cfg.retries = 2
			},
			runs: 2, // the turn that ended, and the resume that opened the PR
			check: func(t *testing.T, err error, st *ghState, out string) {
				if err != nil {
					t.Errorf("err = %v, want the resumed run to finish the issue", err)
				}
				if st.Issues["1"].Open {
					t.Error("issue 1 should have been closed once its PR merged")
				}
				// The invocation, not only the decision: a resume that quietly
				// restarted the skill from scratch would throw away the context
				// that already holds the whole implementation.
				if !strings.Contains(out, "--resume sess-xyz") {
					t.Errorf("want the session resumed rather than restarted\ngot:\n%s", out)
				}
				// And told the thing that stops it waiting a second time.
				if !strings.Contains(out, "no later turn") {
					t.Errorf("want the resumed run told its turn ends the process\ngot:\n%s", out)
				}
			},
		},
		{
			// The bound on the expensive flavour. Every attempt here burns a
			// complete run rather than failing fast in seconds, and -max-cost is
			// off by default, so a run that keeps deciding to wait has to stop
			// being funded by something.
			name:  "clean-exit resumes stop at their own ceiling",
			mode:  "stream",
			state: &ghState{Issues: open()},
			tune: func(t *testing.T, cfg *config) {
				leftBehind(t, cfg)
				// Deliberately generous, so only the clean-exit ceiling can be
				// what stops this.
				cfg.retries, cfg.resumeCeiling = 5, ceiling
			},
			runs: 1 + cleanExitResumeCeiling,
			check: func(t *testing.T, err error, st *ghState, out string) {
				want := "it has been resumed 2 times after ending a turn without opening a PR " +
					"and has still not opened one, which needs a human"
				if got := parkedFor(t, err); !strings.Contains(got, want) {
					t.Errorf("park reason = %q, want it to name the bound it reached: %q", got, want)
				}
			},
		},
		{
			// The two budgets are one budget. Without this, an issue that
			// alternates crashing and ending its turn draws on two separate
			// ceilings and outlives both of them.
			name:  "a clean-exit resume spends the shared resume budget too",
			mode:  "crashthenwaits",
			state: &ghState{Issues: open()},
			tune: func(t *testing.T, cfg *config) {
				leftBehind(t, cfg)
				// One resume in total, and the crash takes it. The clean exit
				// that follows has work on disk and its own ceiling untouched,
				// and must still be refused.
				cfg.retries, cfg.resumeCeiling = 5, 1
			},
			runs: 2, // the crash, and the one resume the shared ceiling allowed
			check: func(t *testing.T, err error, st *ghState, out string) {
				want := "claude has been retried 1 time on this issue and still has not finished it"
				if got := parkedFor(t, err); !strings.Contains(got, want) {
					t.Errorf("park reason = %q, want it to name the shared budget: %q", got, want)
				}
				// The work is still described, because it is still there and
				// somebody now has to pick it up.
				if got := parkedFor(t, err); !strings.Contains(got, "the run left work behind") {
					t.Errorf("park reason = %q, want it to say what is on disk", got)
				}
			},
		},
		{
			// Fatal rather than parked: a -skill this installation does not have
			// fails identically on every issue behind this one, so parking them
			// one at a time only buries the cause.
			name:  "a skill the session does not have stops the drain",
			mode:  "unknownskill",
			state: &ghState{Issues: open()},
			tune:  func(_ *testing.T, cfg *config) { cfg.retries = 2 },
			runs:  1,
			check: func(t *testing.T, err error, st *ghState, out string) {
				if !errors.Is(err, errNoWork) {
					t.Fatalf("err = %v, want %v", err, errNoWork)
				}
				if _, parked := parkReason(err); parked {
					t.Error("a misconfigured -skill dooms every later issue too, so it cannot be a park")
				}
				if !strings.Contains(err.Error(), "-skill") {
					t.Errorf("err = %v, want it to name the flag to fix", err)
				}
			},
		},
		{
			// Fatal for the same reason, and the reason nothing here retries it:
			// no resume changes the token, so each one buys another 401.
			name:  "refused credentials stop the drain",
			mode:  "authfail",
			state: &ghState{Issues: open()},
			tune:  func(_ *testing.T, cfg *config) { cfg.retries = 2 },
			runs:  1,
			check: func(t *testing.T, err error, st *ghState, out string) {
				if !errors.Is(err, errAuth) {
					t.Fatalf("err = %v, want %v", err, errAuth)
				}
				if _, parked := parkReason(err); parked {
					t.Error("a token the API refuses fails every later issue too, so it cannot be a park")
				}
				if !strings.Contains(err.Error(), "claude auth status") {
					t.Errorf("err = %v, want it to say how to fix the token", err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			buf := captureLog(t)
			cfg, path := drainConfig(t, tc.mode, tc.state)
			cfg.retryWait = time.Millisecond
			if tc.tune != nil {
				tc.tune(t, &cfg)
			}
			// Bounded: several of these branches are the ones that used to wait
			// on something forever, and a hung suite says less than a failure.
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			err := processIssue(ctx, cfg, 1, &issueState{})

			out := buf.String()
			if got := strings.Count(out, "session started"); got != tc.runs {
				t.Errorf("%d runs dispatched, want %d\ngot:\n%s", got, tc.runs, out)
			}
			tc.check(t, err, finalGhState(t, path), out)
		})
	}
}
