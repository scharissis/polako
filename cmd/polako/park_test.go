package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The wording, over the combinations a drain test would need a separate
// checkout apiece to reach. Worth pinning by hand: this text is often the only
// thing a person is told about a run, and "no commits" beside a dirty worktree
// is the case that matters most — it is the one that reads as nothing happened
// and is not.
func TestLeftWorkDescribe(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		w    leftWork
		want string
	}{
		{"nothing at all", leftWork{branch: "issue-9", counted: true}, ""},
		{"a worktree with nothing in it", leftWork{branch: "issue-9", counted: true, path: "/w"}, ""},
		{"issue #42: implemented and never committed",
			leftWork{branch: "issue-42", counted: true, path: "/w", dirty: 6},
			"branch issue-42 has no commits and its worktree has uncommitted changes in 6 files " +
				"— the run left work behind, so start there rather than from scratch"},
		{"committed but never pushed, worktree tidy",
			leftWork{branch: "issue-42", counted: true, path: "/w", commits: 2},
			"branch issue-42 has 2 commits, not pushed to origin and its worktree has no uncommitted changes " +
				"— the run left work behind, so start there rather than from scratch"},
		{"committed and pushed, worktree tidy",
			leftWork{branch: "issue-42", counted: true, path: "/w", commits: 2, pushed: true},
			"branch issue-42 has 2 commits, pushed to origin and its worktree has no uncommitted changes " +
				"— the run left work behind, so start there rather than from scratch"},
		{"a worktree somebody already removed",
			leftWork{branch: "issue-42", counted: true, commits: 1},
			"branch issue-42 has 1 commit, not pushed to origin — the run left work behind, so start there rather than from scratch"},
		// A checkout with no origin/HEAD to compare against. "no commits" is the
		// half of this a person acts on, so an uncounted branch says so rather
		// than reporting the zero it never established.
		{"the branch could not be compared with anything",
			leftWork{branch: "issue-42", path: "/w", dirty: 6},
			"branch issue-42 could not be compared with the default branch and its worktree has " +
				"uncommitted changes in 6 files — the run left work behind, so start there " +
				"rather than from scratch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.w.describe(); got != tc.want {
				t.Errorf("describe() = %q, want %q", got, tc.want)
			}
		})
	}
}

// The path is the one thing a person cannot get off the issue thread, and the
// one thing that must not go on it: the thread may be public, and an absolute
// path names the operator's account and how their disk is laid out. So it
// travels beside the park rather than inside its reason.
func TestLeftWorkWhereIsForTheLogAlone(t *testing.T) {
	t.Parallel()
	w := leftWork{branch: "issue-42", counted: true, path: "/src/repo-issue-42", dirty: 6}
	if got, want := w.where(), "the work it left is in /src/repo-issue-42"; got != want {
		t.Errorf("where() = %q, want %q", got, want)
	}
	if strings.Contains(w.describe(), w.path) {
		t.Errorf("describe() carries the path to the issue thread: %q", w.describe())
	}
	// Nothing to point at is nothing to say: a worktree that is merely clean
	// leaves no work, and a park that named it would be sending a person to a
	// directory with nothing in it.
	for _, empty := range []leftWork{
		{branch: "issue-42", counted: true, path: "/src/repo-issue-42"},
		{branch: "issue-42", counted: true, commits: 3},
	} {
		if got := empty.where(); got != "" {
			t.Errorf("where() = %q for %+v, want \"\"", got, empty)
		}
	}
}

// git names the worktree holding a branch, and it is asked rather than guessed
// because a run driven from the desktop app puts one somewhere the sibling
// convention would never look. A detached worktree in the way must not be
// mistaken for it.
func TestWorktreeForFindsTheWorktreeHoldingABranch(t *testing.T) {
	t.Parallel()
	list := "worktree /repo\nHEAD aaa\nbranch refs/heads/main\n\n" +
		"worktree /tmp/detached\nHEAD bbb\ndetached\n\n" +
		"worktree /elsewhere/wt\nHEAD ccc\nbranch refs/heads/issue-7\n\n"
	if got := worktreeFor(list, "issue-7"); got != "/elsewhere/wt" {
		t.Errorf("worktreeFor = %q, want /elsewhere/wt", got)
	}
	if got := worktreeFor(list, "issue-8"); got != "" {
		t.Errorf("worktreeFor for a branch with no worktree = %q, want \"\"", got)
	}
}

// Two things only a real repository can answer, which is why they are not in
// the table above: what git counts as changed, and what it says about a
// worktree whose directory is gone.
func TestInspectLeftWorkAgainstARealCheckout(t *testing.T) {
	t.Parallel()
	t.Run("a whole new directory is counted file by file", func(t *testing.T) {
		_, checkout := upstream(t)
		wt := filepath.Join(t.TempDir(), "checkout-issue-3")
		gitAt(t, checkout, "worktree", "add", wt, "-b", "issue-3")
		if err := os.MkdirAll(filepath.Join(wt, "newpkg"), 0o755); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"a", "b", "c"} {
			if err := os.WriteFile(filepath.Join(wt, "newpkg", name), []byte(name), 0o644); err != nil {
				t.Fatal(err)
			}
		}

		// `git status --porcelain` alone answers "1 file" here: it folds an
		// untracked directory into one `newpkg/` line. That is the run this
		// message exists for — a change written and never committed — so the
		// number it reports has to be the number of files.
		w := inspectLeftWork(context.Background(), config{dir: checkout, branchPrefix: "issue-"}, 3)
		if w.dirty != 3 {
			t.Errorf("dirty = %d, want 3 — an untracked directory counted as one file", w.dirty)
		}
	})

	t.Run("the plan the skill wrote is not work left behind", func(t *testing.T) {
		_, checkout := upstream(t)
		wt := filepath.Join(t.TempDir(), "checkout-issue-5")
		gitAt(t, checkout, "worktree", "add", wt, "-b", "issue-5")
		if err := os.WriteFile(filepath.Join(wt, planFile), []byte("## Approach\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		cfg := config{dir: checkout, branchPrefix: "issue-"}
		if w := inspectLeftWork(context.Background(), cfg, 5); w.dirty != 0 || w.salvageable() {
			t.Errorf("dirty = %d, salvageable = %v for a worktree holding only the plan; "+
				"counted, every park would claim work was left behind", w.dirty, w.salvageable())
		}
		// And it is only the plan that is discounted: real work beside it counts.
		if err := os.WriteFile(filepath.Join(wt, "half-the-change"), []byte("code"), 0o644); err != nil {
			t.Fatal(err)
		}
		if w := inspectLeftWork(context.Background(), cfg, 5); w.dirty != 1 {
			t.Errorf("dirty = %d, want 1 — the plan is discounted, the change beside it is not", w.dirty)
		}
	})

	t.Run("a worktree deleted by hand is not named", func(t *testing.T) {
		_, checkout := upstream(t)
		wt := filepath.Join(t.TempDir(), "checkout-issue-4")
		gitAt(t, checkout, "worktree", "add", wt, "-b", "issue-4")
		commit(t, wt, "half-the-change")
		// rm -rf, not `git worktree remove`: git goes on listing the worktree
		// until something prunes it, and a park that reads that list at face
		// value sends a person to a directory that is not there any more.
		if err := os.RemoveAll(wt); err != nil {
			t.Fatal(err)
		}

		w := inspectLeftWork(context.Background(), config{dir: checkout, branchPrefix: "issue-"}, 4)
		if w.path != "" {
			t.Errorf("path = %q, want \"\" — the directory is gone", w.path)
		}
		want := "branch issue-4 has 1 commit, not pushed to origin — the run left work behind, so start there rather than from scratch"
		if got := w.describe(); got != want {
			t.Errorf("describe() = %q, want %q", got, want)
		}
	})

	t.Run("a pushed branch says so", func(t *testing.T) {
		_, checkout := upstream(t)
		wt := filepath.Join(t.TempDir(), "checkout-issue-6")
		gitAt(t, checkout, "worktree", "add", wt, "-b", "issue-6")
		commit(t, wt, "half-the-change")
		gitAt(t, checkout, "push", "origin", "issue-6")

		w := inspectLeftWork(context.Background(), config{dir: checkout, branchPrefix: "issue-"}, 6)
		if !w.pushed {
			t.Error("pushed = false, want true — the branch is on origin at the same sha")
		}
		want := "branch issue-6 has 1 commit, pushed to origin and its worktree has no uncommitted changes " +
			"— the run left work behind, so start there rather than from scratch"
		if got := w.describe(); got != want {
			t.Errorf("describe() = %q, want %q", got, want)
		}
	})
}

// budgetLoop is a minimal issueLoop wired at checkout, for calling
// budgetPark directly rather than driving a whole shift through drain().
func budgetLoop(cfg config, issue int) *issueLoop {
	return &issueLoop{ctx: context.Background(), cfg: cfg, issue: issue}
}

// The whole point of #466: a budget park with real commits on the branch
// pushes them to origin rather than leaving the only copy on one machine's
// disk — #318's own failure. A push that lands has nothing more to say
// beyond describe()'s own "pushed to origin".
func TestBudgetParkPushesUnpushedCommits(t *testing.T) {
	t.Parallel()
	_, checkout := upstream(t)
	wt := filepath.Join(t.TempDir(), "checkout-issue-1")
	gitAt(t, checkout, "worktree", "add", wt, "-b", "issue-1")
	sha := commit(t, wt, "half-the-change")
	cfg := config{dir: checkout, branchPrefix: "issue-"}

	err := budgetLoop(cfg, 1).budgetPark("this shift has spent $9.00 on it")
	reason, parked := parkReason(err)
	if !parked {
		t.Fatalf("budgetPark did not return a parkedError: %v", err)
	}
	if !strings.Contains(reason, "branch issue-1 has 1 commit, pushed to origin") {
		t.Errorf("park reason = %q, want it to report the branch as pushed after the push", reason)
	}

	remote := gitAt(t, checkout, "ls-remote", "origin", "issue-1")
	if !strings.Contains(remote, sha) {
		t.Errorf("origin does not have issue-1 at %s: %q", sha, remote)
	}
}

// A push that fails is folded into the park reason, not swallowed — that is
// the one case where the branch really is nowhere but this disk, so it is
// the one case a human reading the park comment most needs to hear about.
func TestBudgetParkFoldsAFailedPushIntoTheReason(t *testing.T) {
	t.Parallel()
	_, checkout := upstream(t)
	wt := filepath.Join(t.TempDir(), "checkout-issue-1")
	gitAt(t, checkout, "worktree", "add", wt, "-b", "issue-1")
	commit(t, wt, "half-the-change")
	unreachableOrigin(t, checkout)
	cfg := config{dir: checkout, branchPrefix: "issue-"}

	err := budgetLoop(cfg, 1).budgetPark("this shift has spent $9.00 on it")
	reason, parked := parkReason(err)
	if !parked {
		t.Fatalf("budgetPark did not return a parkedError: %v", err)
	}
	if !strings.Contains(reason, "tried to push branch issue-1 to origin") || !strings.Contains(reason, "the push failed") {
		t.Errorf("park reason = %q, want it to say the push failed", reason)
	}
	// Not just that it failed — what to do about it, the same as this
	// file's other park reasons (fetchAuthParkReason).
	if !strings.Contains(reason, "push it by hand (`git push origin issue-1`), then remove needs-human") {
		t.Errorf("park reason = %q, want it to tell the human how to recover", reason)
	}
	// Still says what is there, push failure or not — the person picking
	// this up needs both facts.
	if !strings.Contains(reason, "branch issue-1 has 1 commit, not pushed to origin") {
		t.Errorf("park reason = %q, want it to still describe the branch", reason)
	}
}

// A budget park with nothing salvageable on the branch never touches git
// push at all — there is nothing there to preserve.
func TestBudgetParkPushesNothingWithNoCommits(t *testing.T) {
	t.Parallel()
	_, checkout := upstream(t)
	cfg := config{dir: checkout, branchPrefix: "issue-"}

	err := budgetLoop(cfg, 1).budgetPark("this shift has spent $9.00 on it")
	reason, parked := parkReason(err)
	if !parked {
		t.Fatalf("budgetPark did not return a parkedError: %v", err)
	}
	if reason != "this shift has spent $9.00 on it" {
		t.Errorf("park reason = %q, want the cause alone with nothing appended", reason)
	}
}
