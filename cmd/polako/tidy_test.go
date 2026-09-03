package main

// `tidy` is the one verb whose actions cannot be undone, so it is held to
// proving safety before acting on it. These tests run real git — a worktree,
// a merge and a squash-like divergence are not something a fake can tell the
// truth about, the same reasoning sync_test.go already follows — against the
// fake gh drain_test.go's fixture already drives.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// tidyGh points the fake gh at a pretend repository, the same way
// statusConfigFor does for status's own tests.
func tidyGh(t *testing.T, st *ghState) {
	t.Helper()
	if st.Repo == "" {
		st.Repo = "example/repo"
	}
	path := filepath.Join(t.TempDir(), "gh-state.json")
	if err := writeGhState(path, st); err != nil {
		t.Fatalf("writing fake gh state: %v", err)
	}
	t.Setenv(fakeGhEnv, path)
}

// The same flags-only contract every other verb's entry point holds to; see
// TestRunStatusRejectsAnArgument in main_test.go for the sibling this mirrors.
func TestRunTidyRejectsAnArgument(t *testing.T) {
	err := runTidy(context.Background(), []string{"12"}, &strings.Builder{}, report{})
	if err == nil || !strings.Contains(err.Error(), "tidy takes flags only") {
		t.Errorf("err = %v, want a complaint about the argument", err)
	}
}

func tidyCfg(t *testing.T, dir string) config {
	t.Helper()
	return config{
		dir:          dir,
		ghBin:        fakeCLI(t),
		repo:         "example/repo",
		ghRepo:       "example/repo",
		branchPrefix: "issue-",
		ghRetryWait:  time.Millisecond,
	}
}

func findTidyResult(t *testing.T, results []tidyResult, issue int) tidyResult {
	t.Helper()
	for _, r := range results {
		if r.issue == issue {
			return r
		}
	}
	t.Fatalf("no result for issue #%d among %+v", issue, results)
	return tidyResult{}
}

// mergeIssueBranch leaves checkout exactly the way a merged PR does: branch
// cut from whatever checkout was on, committed to, merged back with a real
// merge commit, and pushed — so branch ends up an ancestor of origin's
// default branch, which is the one thing a squash merge would not leave
// behind.
func mergeIssueBranch(t *testing.T, checkout, branch, file string) {
	t.Helper()
	base := gitAt(t, checkout, "rev-parse", "--abbrev-ref", "HEAD")
	gitAt(t, checkout, "checkout", "-b", branch)
	commit(t, checkout, file)
	gitAt(t, checkout, "checkout", base)
	gitAt(t, checkout, "merge", "--no-ff", "-m", "merge "+branch, branch)
	gitAt(t, checkout, "push", "origin", base)
}

// The point of the whole issue: an issue that is closed, merged into the
// default branch and whose worktree is clean is reclaimed in full.
func TestReclaimRemovesAMergedAndCleanIssue(t *testing.T) {
	_, checkout := upstream(t)
	mergeIssueBranch(t, checkout, "issue-1", "feature-1")
	wt := filepath.Join(t.TempDir(), "issue-1-worktree")
	gitAt(t, checkout, "worktree", "add", wt, "issue-1")

	tidyGh(t, &ghState{Issues: map[string]*fakeIssue{"1": {Open: false}}})
	cfg := tidyCfg(t, checkout)

	results, err := reclaim(context.Background(), cfg, true, 0)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	r := findTidyResult(t, results, 1)
	if !r.reclaimed {
		t.Fatalf("issue #1 was not reclaimed: %+v", r)
	}
	if r.why != "closed" {
		t.Errorf("why = %q, want %q", r.why, "closed")
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Errorf("worktree %s still exists", wt)
	}
	if out := gitAt(t, checkout, "branch", "--list", "issue-1"); out != "" {
		t.Errorf("branch issue-1 still exists: %q", out)
	}
}

// reclaim matches a branch's worktree by asking git which branch it holds,
// never by directory name or location (see worktreeFor in sync.go) — so a
// worktree from the old sibling-folder convention and one at the current
// `.worktrees/issue-N` layout the skill creates today are reclaimed
// identically, in the same sweep.
func TestReclaimWorksAtOldAndNewWorktreeLocations(t *testing.T) {
	_, checkout := upstream(t)
	mergeIssueBranch(t, checkout, "issue-2", "feature-2")
	mergeIssueBranch(t, checkout, "issue-3", "feature-3")

	oldStyle := filepath.Join(t.TempDir(), "repo-issue-2")
	gitAt(t, checkout, "worktree", "add", oldStyle, "issue-2")
	newStyle := filepath.Join(checkout, ".worktrees", "issue-3")
	gitAt(t, checkout, "worktree", "add", newStyle, "issue-3")

	tidyGh(t, &ghState{Issues: map[string]*fakeIssue{"2": {Open: false}, "3": {Open: false}}})
	cfg := tidyCfg(t, checkout)

	results, err := reclaim(context.Background(), cfg, true, 0)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	for issue, path := range map[int]string{2: oldStyle, 3: newStyle} {
		r := findTidyResult(t, results, issue)
		if !r.reclaimed {
			t.Errorf("issue #%d (worktree at %s) was not reclaimed: %+v", issue, path, r)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("worktree %s still exists", path)
		}
	}
}

// The skill leaves an untracked PLAN.md in every worktree it creates and never
// cleans it up, so "clean" for reclaim's purposes means "clean but for that".
// A plain `git worktree remove` would refuse the untracked file; the sweep
// forces past it, exactly the way the merge-moment cleanup it replaced did.
func TestReclaimRemovesAWorktreeHoldingOnlyThePlan(t *testing.T) {
	_, checkout := upstream(t)
	mergeIssueBranch(t, checkout, "issue-9", "feature-9")
	wt := filepath.Join(t.TempDir(), "issue-9-worktree")
	gitAt(t, checkout, "worktree", "add", wt, "issue-9")
	if err := os.WriteFile(filepath.Join(wt, planFile), []byte("## Approach\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tidyGh(t, &ghState{Issues: map[string]*fakeIssue{"9": {Open: false}}})
	cfg := tidyCfg(t, checkout)

	results, err := reclaim(context.Background(), cfg, true, 0)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	r := findTidyResult(t, results, 9)
	if !r.reclaimed {
		t.Fatalf("issue #9 was not reclaimed — a lone PLAN.md is not work left behind: %+v", r)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Errorf("worktree %s still exists", wt)
	}
}

// An open issue is left entirely alone, whatever its branch looks like.
func TestReclaimSkipsAnOpenIssue(t *testing.T) {
	_, checkout := upstream(t)
	mergeIssueBranch(t, checkout, "issue-2", "feature-2")

	tidyGh(t, &ghState{Issues: map[string]*fakeIssue{"2": {Open: true}}})
	cfg := tidyCfg(t, checkout)

	results, err := reclaim(context.Background(), cfg, true, 0)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	r := findTidyResult(t, results, 2)
	if r.reclaimed {
		t.Fatalf("an open issue must not be reclaimed: %+v", r)
	}
	if r.reason != "still open" {
		t.Errorf("reason = %q, want %q", r.reason, "still open")
	}
	if out := gitAt(t, checkout, "branch", "--list", "issue-2"); out == "" {
		t.Error("branch issue-2 was deleted, but the issue is still open")
	}
}

// A closed issue whose worktree still has something uncommitted in it is
// named and left alone — removing it would be the one mistake nothing can
// undo.
func TestReclaimSkipsAClosedIssueWithADirtyWorktree(t *testing.T) {
	_, checkout := upstream(t)
	mergeIssueBranch(t, checkout, "issue-3", "feature-3")
	wt := filepath.Join(t.TempDir(), "issue-3-worktree")
	gitAt(t, checkout, "worktree", "add", wt, "issue-3")
	if err := os.WriteFile(filepath.Join(wt, "scratch.txt"), []byte("uncommitted"), 0o644); err != nil {
		t.Fatal(err)
	}

	tidyGh(t, &ghState{Issues: map[string]*fakeIssue{"3": {Open: false}}})
	cfg := tidyCfg(t, checkout)

	results, err := reclaim(context.Background(), cfg, true, 0)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	r := findTidyResult(t, results, 3)
	if r.reclaimed {
		t.Fatalf("a dirty worktree must not be reclaimed: %+v", r)
	}
	if r.reason != "1 uncommitted file" {
		t.Errorf("reason = %q, want %q", r.reason, "1 uncommitted file")
	}
	// A skip carries the path and the PR reference too — tidySweep's warning and
	// renderTidy's table need both to tell an operator what to look at.
	if !strings.HasSuffix(r.worktreePath, "issue-3-worktree") {
		t.Errorf("worktreePath = %q, want it to name the left worktree", r.worktreePath)
	}
	if r.why != "closed" {
		t.Errorf("why = %q, want %q", r.why, "closed")
	}
	if _, err := os.Stat(wt); err != nil {
		t.Errorf("worktree %s should still be there: %v", wt, err)
	}
}

// A worktree that will not answer `git status` — a stale lock, a half-broken
// checkout — is not force-removed on a guess: reclaim cannot vet what a forced
// removal would discard, so it leaves the worktree and its branch for a human
// with the command to finish the job. This is the case the merge-moment
// cleanup named by hand before the sweep replaced it.
func TestReclaimLeavesAWorktreeItCannotReadAlone(t *testing.T) {
	_, checkout := upstream(t)
	mergeIssueBranch(t, checkout, "issue-1", "feature-1")
	wt := filepath.Join(t.TempDir(), "issue-1-worktree")
	gitAt(t, checkout, "worktree", "add", wt, "issue-1")
	// The directory stays on disk, but its .git pointer leads nowhere, so any
	// git command run inside it fails — a half-broken checkout, not a stale
	// admin entry a prune would clear.
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: /does/not/exist\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tidyGh(t, &ghState{Issues: map[string]*fakeIssue{"1": {Open: false}}})
	cfg := tidyCfg(t, checkout)

	results, err := reclaim(context.Background(), cfg, true, 0)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	r := findTidyResult(t, results, 1)
	if r.reclaimed {
		t.Fatalf("a worktree that will not answer git status must not be reclaimed: %+v", r)
	}
	if !strings.Contains(r.reason, "could not be read") {
		t.Errorf("reason = %q, want it to name the unreadable worktree", r.reason)
	}
	if _, err := os.Stat(wt); err != nil {
		t.Errorf("worktree %s should still be there: %v", wt, err)
	}
	if out := gitAt(t, checkout, "branch", "--list", "issue-1"); out == "" {
		t.Error("branch issue-1 was deleted, but its worktree could not be vetted")
	}
}

// A branch that was never actually merged — closed some other way, or still
// waiting — is not reasoned about further: this is also the squash-merge
// case, which is not literally an ancestor of anything either, and gets the
// same honest refusal rather than a guess.
func TestReclaimSkipsABranchNotMergedIntoTheDefaultBranch(t *testing.T) {
	_, checkout := upstream(t)
	gitAt(t, checkout, "checkout", "-b", "issue-4")
	commit(t, checkout, "feature-4")
	gitAt(t, checkout, "checkout", "main")

	tidyGh(t, &ghState{Issues: map[string]*fakeIssue{"4": {Open: false}}})
	cfg := tidyCfg(t, checkout)

	results, err := reclaim(context.Background(), cfg, true, 0)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	r := findTidyResult(t, results, 4)
	if r.reclaimed {
		t.Fatalf("an unmerged branch must not be reclaimed: %+v", r)
	}
	if !strings.Contains(r.reason, "not merged into the default branch") {
		t.Errorf("reason = %q, want it to name the branch as unmerged", r.reason)
	}
	if out := gitAt(t, checkout, "branch", "--list", "issue-4"); out == "" {
		t.Error("branch issue-4 was deleted, but it was never merged")
	}
}

// needs-human and proposed are a human's hold on an issue, and only a human
// takes either off — so the sweep leaves the branch and worktree alone even
// when GitHub says the PR merged. Someone who finishes a parked issue by hand
// still gets to clear the label at their desk.
func TestReclaimLeavesAHeldIssueAlone(t *testing.T) {
	for _, label := range []string{needsHumanLabel, proposedLabel} {
		t.Run(label, func(t *testing.T) {
			_, checkout := upstream(t)
			mergeIssueBranch(t, checkout, "issue-1", "feature-1")
			wt := filepath.Join(t.TempDir(), "issue-1-worktree")
			gitAt(t, checkout, "worktree", "add", wt, "issue-1")

			tidyGh(t, &ghState{
				Issues: map[string]*fakeIssue{"1": {Open: true, Labels: []string{label}}},
				PRs:    map[string]*fakePR{"issue-1": {Number: 9, State: "MERGED"}},
			})
			cfg := tidyCfg(t, checkout)

			results, err := reclaim(context.Background(), cfg, true, 0)
			if err != nil {
				t.Fatalf("reclaim: %v", err)
			}
			r := findTidyResult(t, results, 1)
			if r.reclaimed {
				t.Fatalf("a held issue must not be reclaimed even with a merged PR: %+v", r)
			}
			if !strings.Contains(r.reason, "held by a human") {
				t.Errorf("reason = %q, want it to name the human hold", r.reason)
			}
			if r.heldBy != label {
				t.Errorf("heldBy = %q, want %q so tidySweep can tell a hold from a stuck worktree", r.heldBy, label)
			}
			if _, err := os.Stat(wt); err != nil {
				t.Errorf("a held issue's worktree must be left alone: %v", err)
			}
		})
	}
}

// A human can park an issue while its PR is in flight; the PR then merges and
// the drain witnesses it. The hold outranks the merge, so the sweep leaves the
// worktree — but that is a deliberate, benign state, said quietly, not the
// "PR merged but the worktree could not be reclaimed" alarm reserved for
// uncommitted work the merge did not take.
func TestTidySweepIsQuietAboutAHeldWatchedIssue(t *testing.T) {
	buf := captureLog(t)
	_, checkout := upstream(t)
	mergeIssueBranch(t, checkout, "issue-1", "feature-1")
	wt := filepath.Join(t.TempDir(), "issue-1-worktree")
	gitAt(t, checkout, "worktree", "add", wt, "issue-1")

	tidyGh(t, &ghState{
		Issues: map[string]*fakeIssue{"1": {Open: true, Labels: []string{needsHumanLabel}}},
		PRs:    map[string]*fakePR{"issue-1": {Number: 9, State: "MERGED"}},
	})
	cfg := tidyCfg(t, checkout)

	tidySweep(context.Background(), cfg, 1)

	if _, err := os.Stat(wt); err != nil {
		t.Errorf("a held issue's worktree must be left alone: %v", err)
	}
	if out := buf.String(); strings.Contains(out, "could not be reclaimed") {
		t.Errorf("a human hold must not raise the reclaim alarm:\n%s", out)
	}
}

// The branch whose merge the drain witnessed is reclaimed even when it is not a
// literal ancestor of the default branch — a squash merge never is. The
// evidence is GitHub's own merge event, not the branch shape, and it applies
// only because the local tip still equals what was pushed to origin.
func TestReclaimReclaimsAWitnessedSquashMerge(t *testing.T) {
	_, checkout := upstream(t)
	// issue-7 diverges and is never merged back: 1 commit ahead of main, the
	// shape a squash merge leaves.
	gitAt(t, checkout, "checkout", "-b", "issue-7")
	commit(t, checkout, "feature-7")
	gitAt(t, checkout, "checkout", "main")
	gitAt(t, checkout, "push", "origin", "issue-7")
	wt := filepath.Join(t.TempDir(), "issue-7-worktree")
	gitAt(t, checkout, "worktree", "add", wt, "issue-7")

	tidyGh(t, &ghState{
		Issues: map[string]*fakeIssue{"7": {Open: false}},
		PRs:    map[string]*fakePR{"issue-7": {Number: 9, State: "MERGED"}},
	})
	cfg := tidyCfg(t, checkout)

	// Not witnessed: the conservative refusal still stands.
	unwatched, err := reclaim(context.Background(), cfg, false, 0)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if r := findTidyResult(t, unwatched, 7); r.reclaimed || !strings.Contains(r.reason, "not merged into the default branch") {
		t.Fatalf("unwitnessed, a non-ancestor branch must be refused: %+v", r)
	}

	// Witnessed: reclaimed.
	watched, err := reclaim(context.Background(), cfg, true, 7)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	r := findTidyResult(t, watched, 7)
	if !r.reclaimed {
		t.Fatalf("a witnessed merge must be reclaimed whatever the branch shape: %+v", r)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Errorf("worktree %s still exists", wt)
	}
	if out := gitAt(t, checkout, "branch", "--list", "issue-7"); out != "" {
		t.Errorf("branch issue-7 still exists: %q", out)
	}
}

// A witnessed merge vouches for what was pushed, not for a commit added to the
// local branch on top of it. reclaim confirms the local tip still equals
// origin's before it force-deletes; when it does not, the branch takes the
// ordinary conservative path and is left alone rather than -D'd.
func TestReclaimDoesNotForceDeleteAWitnessedBranchWithUnpushedWork(t *testing.T) {
	_, checkout := upstream(t)
	gitAt(t, checkout, "checkout", "-b", "issue-7")
	commit(t, checkout, "feature-7")
	gitAt(t, checkout, "push", "origin", "issue-7")
	// A commit that never reached origin — the merge event cannot speak for it.
	commit(t, checkout, "local-only-follow-up")
	gitAt(t, checkout, "checkout", "main")

	tidyGh(t, &ghState{
		Issues: map[string]*fakeIssue{"7": {Open: false}},
		PRs:    map[string]*fakePR{"issue-7": {Number: 9, State: "MERGED"}},
	})
	cfg := tidyCfg(t, checkout)

	results, err := reclaim(context.Background(), cfg, true, 7)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	r := findTidyResult(t, results, 7)
	if r.reclaimed {
		t.Fatalf("a witnessed branch with unpushed work must not be force-deleted: %+v", r)
	}
	if out := gitAt(t, checkout, "branch", "--list", "issue-7"); out == "" {
		t.Error("branch issue-7 was deleted despite carrying an unpushed commit")
	}
}

// A branch that never got its own worktree still has its branch deleted —
// the common case for a run that finished cleanly and whose worktree a
// drain already cleaned up on merge.
func TestReclaimDeletesABranchWithNoWorktree(t *testing.T) {
	_, checkout := upstream(t)
	mergeIssueBranch(t, checkout, "issue-5", "feature-5")

	tidyGh(t, &ghState{Issues: map[string]*fakeIssue{"5": {Open: false}}})
	cfg := tidyCfg(t, checkout)

	results, err := reclaim(context.Background(), cfg, true, 0)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	r := findTidyResult(t, results, 5)
	if !r.reclaimed {
		t.Fatalf("issue #5 was not reclaimed: %+v", r)
	}
	if r.worktreePath != "" {
		t.Errorf("worktreePath = %q, want empty — there was never a worktree", r.worktreePath)
	}
	if out := gitAt(t, checkout, "branch", "--list", "issue-5"); out != "" {
		t.Errorf("branch issue-5 still exists: %q", out)
	}
}

// A worktree whose directory was removed by hand outlives that removal as a
// stale admin entry — git calls it prunable. tidy clears it with no
// `worktree remove` call, since there is nothing left to remove.
func TestReclaimClearsAPrunableWorktreeEntry(t *testing.T) {
	_, checkout := upstream(t)
	mergeIssueBranch(t, checkout, "issue-6", "feature-6")
	wt := filepath.Join(t.TempDir(), "issue-6-worktree")
	gitAt(t, checkout, "worktree", "add", wt, "issue-6")
	if err := os.RemoveAll(wt); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gitAt(t, checkout, "worktree", "list", "--porcelain"), "prunable") {
		t.Fatal("removing the directory by hand did not leave a prunable admin entry — fixture is wrong")
	}

	tidyGh(t, &ghState{Issues: map[string]*fakeIssue{"6": {Open: false}}})
	cfg := tidyCfg(t, checkout)

	results, err := reclaim(context.Background(), cfg, true, 0)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	r := findTidyResult(t, results, 6)
	if !r.reclaimed {
		t.Fatalf("issue #6 was not reclaimed: %+v", r)
	}
	if r.worktreePath != "" {
		t.Errorf("worktreePath = %q, want empty — a prunable entry has no live directory to remove", r.worktreePath)
	}
	if strings.Contains(gitAt(t, checkout, "worktree", "list", "--porcelain"), "prunable") {
		t.Error("the prunable admin entry was not cleared")
	}
	if out := gitAt(t, checkout, "branch", "--list", "issue-6"); out != "" {
		t.Errorf("branch issue-6 still exists: %q", out)
	}
}

// A detached worktree carries no branch at all, so it can never be the
// worktree for an issue-N branch — the same "match by branch, not directory"
// rule that keeps a Claude Code worktree safe. It is never a candidate, and
// reclaim must not so much as look at it.
func TestReclaimIgnoresADetachedWorktree(t *testing.T) {
	_, checkout := upstream(t)
	sha := gitAt(t, checkout, "rev-parse", "HEAD")
	wt := filepath.Join(t.TempDir(), "detached-worktree")
	gitAt(t, checkout, "worktree", "add", "--detach", wt, sha)

	tidyGh(t, &ghState{})
	cfg := tidyCfg(t, checkout)

	results, err := reclaim(context.Background(), cfg, true, 0)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("no issue branch exists, so nothing should be a candidate: %+v", results)
	}
	if _, err := os.Stat(wt); err != nil {
		t.Errorf("the detached worktree must be left alone: %v", err)
	}
}

// git protects its own main working tree from `worktree remove`, but a linked
// one gets no such protection — it is removed even while a process is running
// with that directory as its cwd, which is exactly what every git/gh call
// tidy makes does (cfg.dir is always capture()'s working directory). An
// operator who cd's into a finished issue's worktree and points -dir there
// instead of at the main checkout must be refused, not have the ground
// removed out from under the rest of the sweep.
func TestReclaimRefusesToRemoveTheWorktreeItIsRunningFrom(t *testing.T) {
	_, checkout := upstream(t)
	mergeIssueBranch(t, checkout, "issue-8", "feature-8")
	wt := filepath.Join(t.TempDir(), "issue-8-worktree")
	gitAt(t, checkout, "worktree", "add", wt, "issue-8")

	tidyGh(t, &ghState{Issues: map[string]*fakeIssue{"8": {Open: false}}})
	cfg := tidyCfg(t, wt) // -dir points at the linked worktree itself

	results, err := reclaim(context.Background(), cfg, true, 0)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	r := findTidyResult(t, results, 8)
	if r.reclaimed {
		t.Fatalf("tidy must not remove the worktree it is running from: %+v", r)
	}
	if !strings.Contains(r.reason, "running from") {
		t.Errorf("reason = %q, want it to name this as the worktree tidy is running from", r.reason)
	}
	if _, err := os.Stat(wt); err != nil {
		t.Errorf("the worktree tidy is running from must still be there: %v", err)
	}
	if out := gitAt(t, checkout, "branch", "--list", "issue-8"); out == "" {
		t.Error("branch issue-8 was deleted, but tidy must not touch the worktree it is running from")
	}
}

// The one assertion worth writing twice: -dry-run — apply=false — never
// touches disk, whether or not everything about the issue says it safely
// could.
func TestReclaimDryRunWritesNothingToDisk(t *testing.T) {
	_, checkout := upstream(t)
	mergeIssueBranch(t, checkout, "issue-7", "feature-7")
	wt := filepath.Join(t.TempDir(), "issue-7-worktree")
	gitAt(t, checkout, "worktree", "add", wt, "issue-7")

	tidyGh(t, &ghState{Issues: map[string]*fakeIssue{"7": {Open: false}}})
	cfg := tidyCfg(t, checkout)

	for i := 0; i < 2; i++ {
		results, err := reclaim(context.Background(), cfg, false, 0)
		if err != nil {
			t.Fatalf("reclaim (pass %d): %v", i, err)
		}
		r := findTidyResult(t, results, 7)
		if !r.reclaimed {
			t.Fatalf("pass %d: issue #7 should report as reclaimable: %+v", i, r)
		}
		if _, err := os.Stat(wt); err != nil {
			t.Errorf("pass %d: -dry-run removed the worktree: %v", i, err)
		}
		if out := gitAt(t, checkout, "branch", "--list", "issue-7"); out == "" {
			t.Errorf("pass %d: -dry-run deleted the branch", i)
		}
	}
}

// A repository with no issue-N branch at all is the simplest possible
// report: one line, not an empty pair of tables.
func TestRenderTidyNothingToReclaim(t *testing.T) {
	var out strings.Builder
	renderTidy(&out, report{}, config{repo: "example/repo", branchPrefix: "issue-"}, true, nil)
	want := "example/repo — nothing to reclaim: no issue-N branch found\n"
	if out.String() != want {
		t.Errorf("renderTidy() = %q, want %q", out.String(), want)
	}
}

// Both halves the acceptance criteria ask for — what was reclaimed and what
// was skipped, with the reason — in one report, and the dry-run heading says
// out loud that nothing has actually happened yet.
func TestRenderTidyReportsBothHalves(t *testing.T) {
	results := []tidyResult{
		{issue: 1, branch: "issue-1", reclaimed: true, why: "closed", worktreePath: "/tmp/issue-1"},
		{issue: 5, branch: "issue-5", reclaimed: true, why: "merged (PR #9)"},
		{issue: 2, branch: "issue-2", reason: "still open"},
		{issue: 3, branch: "issue-3", reason: "2 uncommitted files", worktreePath: "/tmp/issue-3"},
	}
	var out strings.Builder
	renderTidy(&out, report{}, config{repo: "example/repo", branchPrefix: "issue-"}, false, results)
	printed := out.String()
	for _, want := range []string{
		"example/repo",
		"would reclaim (-apply to do it)",
		"#1     issue-1  closed          worktree removed, branch deleted",
		"#5     issue-5  merged (PR #9)  branch deleted",
		"skipped",
		"#2     issue-2  still open",
		// a skip with a live worktree names its directory in the reason cell
		"#3     issue-3  2 uncommitted files — /tmp/issue-3",
	} {
		if !strings.Contains(printed, want) {
			t.Errorf("report is missing %q\ngot:\n%s", want, printed)
		}
	}
}
