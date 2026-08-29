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

	results, err := reclaim(context.Background(), cfg, true)
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

// An open issue is left entirely alone, whatever its branch looks like.
func TestReclaimSkipsAnOpenIssue(t *testing.T) {
	_, checkout := upstream(t)
	mergeIssueBranch(t, checkout, "issue-2", "feature-2")

	tidyGh(t, &ghState{Issues: map[string]*fakeIssue{"2": {Open: true}}})
	cfg := tidyCfg(t, checkout)

	results, err := reclaim(context.Background(), cfg, true)
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

	results, err := reclaim(context.Background(), cfg, true)
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
	if _, err := os.Stat(wt); err != nil {
		t.Errorf("worktree %s should still be there: %v", wt, err)
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

	results, err := reclaim(context.Background(), cfg, true)
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

// A branch that never got its own worktree still has its branch deleted —
// the common case for a run that finished cleanly and whose worktree a
// drain already cleaned up on merge.
func TestReclaimDeletesABranchWithNoWorktree(t *testing.T) {
	_, checkout := upstream(t)
	mergeIssueBranch(t, checkout, "issue-5", "feature-5")

	tidyGh(t, &ghState{Issues: map[string]*fakeIssue{"5": {Open: false}}})
	cfg := tidyCfg(t, checkout)

	results, err := reclaim(context.Background(), cfg, true)
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

	results, err := reclaim(context.Background(), cfg, true)
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

	results, err := reclaim(context.Background(), cfg, true)
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

	results, err := reclaim(context.Background(), cfg, true)
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
		results, err := reclaim(context.Background(), cfg, false)
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
		{issue: 3, branch: "issue-3", reason: "2 uncommitted files"},
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
		"#3     issue-3  2 uncommitted files",
	} {
		if !strings.Contains(printed, want) {
			t.Errorf("report is missing %q\ngot:\n%s", want, printed)
		}
	}
}
