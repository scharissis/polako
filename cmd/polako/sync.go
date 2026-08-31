package main

// Keeping the local checkout in step with the remote. syncDefaultBranch
// fast-forwards the main checkout's default branch (--ff-only, never a merge or
// a rebase) so a review resolves this branch's base correctly. Reclaiming a
// merged issue's worktree lives in tidy.go now (tidySweep / reclaim), which the
// drain runs at shift start and after every merge.

import (
	"context"
	"log"
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
// puts its worktree somewhere no fixed convention — old sibling folder or
// current `.worktrees/issue-N` — would look.
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
