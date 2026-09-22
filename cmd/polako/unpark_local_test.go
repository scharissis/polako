package main

// Tests for the local-work half of unpark (unpark_work.go's renderLocalWork,
// and readParkedIssues' localWork param) — issue #534.
// Unlike the rest of unpark's tests, these need a real checkout: local,
// unpushed work is exactly what a gh fixture can't represent, since GitHub
// never sees it either.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The ticket's own example: a branch with unpushed commits and dirty files
// prints `local: ... in <path>` in the one-issue view, and the next-shift
// line says the next run resumes from that worktree rather than starting
// over. The same read against the same checkout, but with localWork=false
// (what runUnpark passes when -repo was given), finds nothing local at all —
// proving the field stays the zero value, i.e. inspectLeftWork never ran.
func TestUnparkShowsLocalUnpushedWork(t *testing.T) {
	t.Parallel()
	_, checkout := upstream(t)
	wt := filepath.Join(t.TempDir(), "checkout-issue-77")
	gitAt(t, checkout, "worktree", "add", wt, "-b", "issue-77")
	commit(t, wt, "half-the-change")
	commit(t, wt, "more-of-it")
	commit(t, wt, "still-going")
	for _, name := range []string{"scratch-a", "scratch-b"} {
		if err := os.WriteFile(filepath.Join(wt, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	st := &ghState{
		DefaultBranch: "main",
		Issues: map[string]*fakeIssue{
			"77": {Open: true, Labels: []string{needsHumanLabel}},
		},
	}
	cfg := unparkCfg(t, st)
	cfg.dir = checkout

	items, err := readParkedIssues(context.Background(), cfg, 77, true)
	if err != nil {
		t.Fatalf("readParkedIssues: %v", err)
	}
	// git resolves symlinks in a worktree's path (macOS's /var -> /private/var
	// among them), so `wt` itself may not be the exact string inspectLeftWork
	// reports even though it names the same directory. inspectLeftWork's path
	// comes from `git worktree list --porcelain`, which prints forward
	// slashes even on Windows (see
	// TestDrainWarnsWhenTheMergedWorktreeHoldsUncommittedWork), so the
	// comparison needs the same normalization.
	resolved, err := filepath.EvalSymlinks(wt)
	if err != nil {
		t.Fatalf("resolving %s: %v", wt, err)
	}
	resolved = filepath.ToSlash(resolved)

	got := findParkListItem(t, items, 77)
	if got.local.commits != 3 || got.local.pushed || got.local.dirty != 2 || got.local.path != resolved {
		t.Errorf("local = %+v, want 3 unpushed commits, 2 dirty files, path %s", got.local, resolved)
	}

	var one strings.Builder
	renderUnpark(&one, report{}, cfg, []parkListItem{got}, true)
	if !strings.Contains(one.String(), "3 commits not pushed, 2 files uncommitted, in "+resolved) {
		t.Errorf("one-issue view missing the local line:\n%s", one.String())
	}
	if !strings.Contains(one.String(), "resumes from the local worktree") {
		t.Errorf("one-issue view missing the local-worktree next-shift line:\n%s", one.String())
	}

	itemsNoLocal, err := readParkedIssues(context.Background(), cfg, 77, false)
	if err != nil {
		t.Fatalf("readParkedIssues: %v", err)
	}
	gotNoLocal := findParkListItem(t, itemsNoLocal, 77)
	if gotNoLocal.local != (leftWork{}) {
		t.Errorf("local = %+v with localWork=false, want the zero value — inspectLeftWork must not run", gotNoLocal.local)
	}
	var oneNoLocal strings.Builder
	renderUnpark(&oneNoLocal, report{}, cfg, []parkListItem{gotNoLocal}, true)
	if strings.Contains(oneNoLocal.String(), "local") || strings.Contains(oneNoLocal.String(), "uncommitted") {
		t.Errorf("one-issue view with localWork=false should not mention local work:\n%s", oneNoLocal.String())
	}
	if strings.Contains(oneNoLocal.String(), "resumes from the local worktree") {
		t.Errorf("next-shift line with localWork=false should not claim a local worktree:\n%s", oneNoLocal.String())
	}
}

// A fully pushed branch's commits are already visible through the PR/branch
// read (parkWorkSummary, renderParkWorkDetail) — renderLocalWork repeats only
// what GitHub can't see, so a clean, fully pushed worktree prints nothing.
func TestRenderLocalWorkSaysNothingWhenFullyPushed(t *testing.T) {
	t.Parallel()
	_, checkout := upstream(t)
	wt := filepath.Join(t.TempDir(), "checkout-issue-6")
	gitAt(t, checkout, "worktree", "add", wt, "-b", "issue-6")
	commit(t, wt, "half-the-change")
	gitAt(t, checkout, "push", "origin", "issue-6")

	local := inspectLeftWork(context.Background(), config{dir: checkout, branchPrefix: "issue-"}, 6)
	var out strings.Builder
	renderLocalWork(&out, report{}, local)
	if out.Len() != 0 {
		t.Errorf("renderLocalWork = %q, want nothing for a fully pushed, clean branch", out.String())
	}
}
