package main

// -dry-run answers "what would this do here?", so what it says is held against
// what a real drain does — with the same fake gh and fake claude the rest of
// the suite runs on.

import (
	"context"
	"os"
	"strings"
	"testing"
)

// The whole promise: it says what the drain would do to the next issue, and
// does none of it. Nothing on GitHub moves, no run data is written, and no
// claude process starts — which is what makes it safe to point at a repository
// nobody here has drained before.
func TestDryRunSaysWhatItWouldDoAndChangesNothing(t *testing.T) {
	buf := captureLog(t)
	cfg, path := drainConfig(t, "stream", &ghState{
		Issues: map[string]*fakeIssue{
			"1": {Open: true},
			"2": {Open: true, Labels: []string{awaitingAnswerLabel}},
			"3": {Open: true, Labels: []string{needsHumanLabel}},
			"4": {Open: true},
			// A finished container: a real drain would comment on this one, and a
			// dry run must not — it never calls commentFinishedContainers at all.
			"5": {Open: true, SubIssues: 6, SubIssuesCompleted: 6},
		},
	})
	records := t.TempDir()
	cfg.rec = newRecorder(records)
	cfg.skip = map[int]bool{4: true}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading fake gh state: %v", err)
	}

	var out strings.Builder
	if err := dryRun(context.Background(), cfg, &out); err != nil {
		t.Fatalf("dryRun: %v", err)
	}

	printed := strings.TrimSpace(out.String())
	for _, want := range []string{
		cfg.claudeBin,                         // the -claude binary, first
		`'/polako:implement-issue 1'`,         // the prompt, quoted to survive a paste
		`Bash(gh issue edit 1 --add-label:*)`, // the grant pinned to this issue
		"--output-format stream-json",
	} {
		if !strings.Contains(printed, want) {
			t.Errorf("printed invocation is missing %q\ngot: %s", want, printed)
		}
	}
	if strings.Contains(printed, "\n") {
		t.Errorf("want one invocation and nothing else on stdout, got:\n%s", printed)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading fake gh state: %v", err)
	}
	if string(after) != string(before) {
		t.Errorf("a dry run changed the repository:\nbefore %s\nafter  %s", before, after)
	}
	entries, err := os.ReadDir(records)
	if err != nil {
		t.Fatalf("reading the metrics directory: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("a dry run wrote %d run-data files, want none", len(entries))
	}

	said := buf.String()
	for _, want := range []string{
		"ready: #1", // #3 is parked and #4 is skipped, so neither is offered
		"waiting on an answer: #2",
		"issue #1 would be worked next",
	} {
		if !strings.Contains(said, want) {
			t.Errorf("log is missing %q\ngot:\n%s", want, said)
		}
	}
	if strings.Contains(said, "session started") {
		t.Errorf("a dry run started a claude session:\n%s", said)
	}
	for _, unwanted := range []string{"#3", "#4"} {
		if strings.Contains(said, unwanted) {
			t.Errorf("log offers %s, which this drain would not work\ngot:\n%s", unwanted, said)
		}
	}
}

// The queue a dry run prints is the queue a shift would work, exclusions and
// all: it derives it through the same call, so a proposal and a container are
// no more offered here than they would be worked there.
func TestDryRunInheritsTheCurationGate(t *testing.T) {
	buf := captureLog(t)
	cfg, _ := drainConfig(t, "stream", &ghState{
		Issues: map[string]*fakeIssue{
			"1": {Open: true, Labels: []string{proposedLabel}},
			"2": {Open: true, SubIssues: 3},
			"3": {Open: true},
		},
	})

	var out strings.Builder
	if err := dryRun(context.Background(), cfg, &out); err != nil {
		t.Fatalf("dryRun: %v", err)
	}

	said := buf.String()
	for _, want := range []string{
		"ready: #3\n", // and nothing else on that line
		"issue #3 would be worked next",
		"ignoring 1 proposed issue(s) awaiting curation",
	} {
		if !strings.Contains(said, want) {
			t.Errorf("log is missing %q\ngot:\n%s", want, said)
		}
	}
}

// "The exact invocation" is the whole value of the flag, so it is checked
// against the one a real run makes rather than against a second rendering of
// itself — the pair that would otherwise drift the first time either changed.
func TestDryRunPrintsTheInvocationARunWouldMake(t *testing.T) {
	buf := captureLog(t)
	cfg, _ := drainConfig(t, "stream", &ghState{Issues: map[string]*fakeIssue{"1": {Open: true}}})

	var out strings.Builder
	if err := dryRun(context.Background(), cfg, &out); err != nil {
		t.Fatalf("dryRun: %v", err)
	}
	// The run produces no PR and so parks, which is fine: all this needs from
	// it is the command line it logged on the way.
	if err := drain(context.Background(), cfg); err != nil {
		t.Fatalf("drain: %v", err)
	}

	// Re-splitting the printed line undoes the quoting a paste needs, leaving
	// the argv itself — which is what a run logs, one space between each.
	want := "running: " + strings.Join(splitCommand(strings.TrimSpace(out.String())), " ")
	if !strings.Contains(buf.String(), want) {
		t.Errorf("the dry run printed an invocation no run makes\nwant: %s\ngot:\n%s", want, buf.String())
	}
}

// Restart safety is the first thing an issue is put through, so it is the first
// thing a dry run has to report: an issue whose branch already carries a PR
// gets no claude run at all, and printing one would be a lie.
//
// Which is only half the answer, because what the drain does with that PR is
// not one thing — it waits on an open one, closes the issue behind a merged
// one, and parks an issue whose PR was closed unmerged. A dry run that says
// "waiting" for all three names the wrong next step, and the merged case is the
// one where it would touch GitHub at all.
func TestDryRunReportsWhatItWouldDoWithAnExistingPR(t *testing.T) {
	for _, tc := range []struct {
		state string
		want  string
	}{
		{"OPEN", "issue #1 already has PR #42 (OPEN) on branch issue-1 — it would wait on that PR"},
		{"MERGED", "PR #42 (MERGED) on branch issue-1 — it would close the issue and move on"},
		{"CLOSED", "PR #42 (CLOSED) on branch issue-1 — it would park the issue for a human"},
	} {
		t.Run(tc.state, func(t *testing.T) {
			buf := captureLog(t)
			cfg, _ := drainConfig(t, "stream", &ghState{
				Issues: map[string]*fakeIssue{"1": {Open: true}},
				PRs:    map[string]*fakePR{"issue-1": {Number: 42, State: tc.state}},
			})

			var out strings.Builder
			if err := dryRun(context.Background(), cfg, &out); err != nil {
				t.Fatalf("dryRun: %v", err)
			}

			if got := strings.TrimSpace(out.String()); got != "" {
				t.Errorf("printed %q, want no invocation for an issue that would not get one", got)
			}
			if !strings.Contains(buf.String(), tc.want) {
				t.Errorf("log is missing %q\ngot:\n%s", tc.want, buf.String())
			}
		})
	}
}
