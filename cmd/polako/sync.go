package main

// Keeping the local checkout in step with the remote. syncDefaultBranch
// fast-forwards the main checkout's default branch (--ff-only, never a merge or
// a rebase) so a review resolves this branch's base correctly, and
// cleanupWorktree removes a merged issue's worktree once nothing uncommitted is
// left in it.

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
)

// planFile is the note the skill writes before it implements anything, and
// which nothing afterwards commits, deletes or ignores. It is left out of the
// count deliberately: counted, "the run left work behind" would be true of
// every run that got as far as planning — which is every run that got anywhere
// at all — and a message that is always true tells nobody anything. Naming the
// other half's file here is the same kind of contract as the issue-N branch
// name, and holds for the same reason: the two halves ship from one commit.
const planFile = "PLAN.md"

// porcelainPath is the path out of one `git status --porcelain` line, or "" for
// a blank one. The format is two status columns and a space, then the path.
func porcelainPath(line string) string {
	line = strings.TrimRight(line, "\r")
	if len(line) < 4 {
		return ""
	}
	return line[3:]
}

// worktreeFor finds the worktree holding branch in `git worktree list
// --porcelain` output. Asked rather than assumed, by every caller: a park that
// names the wrong directory sends a person to an empty one, cleanup that guesses
// wrong discards its own removal silently, and a run driven from the desktop app
// puts its worktree somewhere the sibling convention would never look.
func worktreeFor(list, branch string) string {
	var path string
	for _, line := range strings.Split(list, "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case strings.HasPrefix(line, "worktree "):
			path = strings.TrimPrefix(line, "worktree ")
		case line == "branch refs/heads/"+branch:
			return path
		}
	}
	return ""
}

// syncDefaultBranch brings the main checkout's default branch up to whatever
// origin has. A drain never pulls — a human merges on GitHub and the drain only
// watches — so the local ref falls a commit behind on every merge, and anything
// that resolves "this branch's base" from the checkout then reads a base that
// predates it. The review gate does exactly that: against a stale base it folds
// an already-merged PR into the diff under review, so a change that shipped
// days ago reads as part of the branch being reviewed — and, if a finding
// against it gets "fixed", as an edit landing inside this branch's own commits.
//
// Merges are not the only source. A teammate's push, a hotfix, or a drain
// restarted after days down all leave the same gap, which is why this runs when
// an issue is picked up and not only after a merge.
//
// --ff-only is the whole safety story: it advances a local mirror to a state a
// human already created on the remote, and refuses rather than moving anything
// it cannot advance cleanly. It creates no commit, merges no PR, rewrites
// nothing somebody committed here — so it stays on the right side of "nothing
// merges itself". Every failure is best-effort and logged rather than fatal: a
// checkout on another branch, or with work in the way, is the operator's to
// sort out and none of it is worth ending an overnight drain over. It is logged
// loudly because a skipped sync is what puts a stale base under the next review.
func syncDefaultBranch(ctx context.Context, cfg config) {
	if _, err := git(ctx, cfg, "fetch", "origin", "--quiet"); err != nil {
		narrate(sevWarning, "could not fetch origin, so the default branch may be behind "+
			"and a review may run against a stale base: %v", err)
		return
	}
	head, err := git(ctx, cfg, "symbolic-ref", "refs/remotes/origin/HEAD", "--short")
	if err != nil {
		narrate(sevWarning, "could not resolve origin's default branch, so %s is left as it is "+
			"— run `git remote set-head origin -a` there if reviews look mis-scoped: %v", cfg.dir, err)
		return
	}
	remote := strings.TrimSpace(string(head))
	local := strings.TrimPrefix(remote, "origin/")
	on, err := git(ctx, cfg, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return
	}
	if got := strings.TrimSpace(string(on)); got != local {
		log.Printf("%s is on %s, not %s — leaving it alone, but a run's review base "+
			"comes from %s, so check it before trusting a review's scope", cfg.dir, got, local, local)
		return
	}
	before, _ := git(ctx, cfg, "rev-parse", "HEAD")
	if _, err := git(ctx, cfg, "merge", "--ff-only", remote); err != nil {
		narrate(sevWarning, "could not fast-forward %s to %s, so a review may run against a stale "+
			"base — commit, stash or discard whatever is in the way in %s: %v",
			local, remote, cfg.dir, err)
		return
	}
	if after, _ := git(ctx, cfg, "rev-parse", "HEAD"); string(after) != string(before) {
		detail.Printf("fast-forwarded %s to %s", local, remote)
	}
}

// cleanupWorktree removes the worktree the skill worked this issue in, once its
// PR has merged. The path is resolved from `git worktree list`, never built: the
// sibling convention holds for maybe half the worktrees that exist — a
// desktop-app run puts its own somewhere else entirely — and a constructed path
// that misses just discards the removal silently. Best-effort throughout: a
// worktree that cannot be removed is the operator's to clear, not worth ending a
// drain over, but it is said rather than swallowed.
func cleanupWorktree(ctx context.Context, cfg config, issue int) {
	// prune runs whichever way the rest goes: it is what clears the admin
	// entries whose directories a run already deleted by hand, and those exist
	// whether or not there is a live worktree to remove this time.
	defer func() { _, _ = git(ctx, cfg, "worktree", "prune") }()

	branch := fmt.Sprintf("%s%d", cfg.branchPrefix, issue)
	list, err := git(ctx, cfg, "worktree", "list", "--porcelain")
	if err != nil {
		narrate(sevWarning, "could not list worktrees to clean up %s's — remove it by hand: %v", branch, err)
		return
	}
	path := worktreeFor(string(list), branch)
	if path == "" {
		return // a desktop-app run's worktree is elsewhere, or there never was one
	}
	if _, err := os.Stat(path); err != nil {
		return // directory already gone — the deferred prune clears the entry
	}
	// The merged PR records what was committed on the branch and nothing else.
	// An uncommitted edit or an untracked file beside it is not merged with it,
	// so a worktree still holding one is not this process's to delete. leftWork
	// already discounts PLAN.md, which the skill leaves untracked in every
	// worktree it creates and never cleans up.
	w := inspectLeftWork(ctx, cfg, issue)
	if w.path == "" {
		// inspectLeftWork blanks the path when `git status` in the worktree
		// could not be read — an index lock, a half-broken checkout. The
		// directory is here (we just stat'd it), so this is "could not tell
		// whether there is work in it", and forcing over an unknown is the
		// thing this change exists to stop.
		narrate(sevWarning, "could not check the worktree for %s at %s for uncommitted work — "+
			"left it in place; inspect it and remove it by hand with `git worktree remove --force %s`",
			branch, path, path)
		return
	}
	if w.dirty > 0 {
		narrate(sevWarning, "left the worktree for %s in place — %s has uncommitted changes in %s "+
			"that the merge did not take; save or discard them, then `git worktree remove --force %s`",
			branch, path, plural(w.dirty, "file"), path)
		return
	}
	// --force, but only past that check: the untracked PLAN.md above is the one
	// thing between a clean worktree and a plain remove, and forcing over it is
	// the whole reason this ever removed anything.
	if _, err := git(ctx, cfg, "worktree", "remove", path, "--force"); err != nil {
		narrate(sevWarning, "could not remove the worktree for %s at %s — clear it by hand with "+
			"`git worktree remove --force %s`: %v", branch, path, path, err)
		return
	}
	detail.Printf("removed worktree %s", path)
}
