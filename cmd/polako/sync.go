package main

// Keeping the local checkout in step with the remote. syncDefaultBranch
// fast-forwards the main checkout's default branch (--ff-only, never a merge or
// a rebase) so a review resolves this branch's base correctly. Reclaiming a
// merged issue's worktree lives in tidy.go now (tidySweep / reclaim), which the
// drain runs at shift start and after every merge.

import (
	"context"
	"fmt"
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

// evidenceDir is the scratch directory the visual-evidence capture flow
// writes shots to between capture and publish (docs/plans/visual-evidence.md,
// ticket 1). Untracked by design, so left-work counting has to know to
// ignore it the same way it already ignores planFile — a dead run's shots
// would otherwise make tidy refuse the worktree and pad a park message.
const evidenceDir = ".polako-evidence"

// scratchDir is where the skill tells a run — and the review agent it forks —
// to put every throwaway file: a diff too big to read from Bash output, the
// body file behind a `--body-file` comment. Discounted like evidenceDir, and
// for a measured reason: the review agent's first reach is /tmp, which the
// session refuses, and its fallback was an improvised name in the worktree
// root (`issue425.diff`, `.review389.diff`, `pr_diff.txt`...), which no
// pattern could discount and nothing deleted — so tidy refused the worktree
// of every merged issue whose review had dumped a diff. One fixed directory
// is the only spelling both halves can agree on; discounting untracked
// `*.diff` instead would loosen the check on the one verb that can't be undone.
const scratchDir = ".polako-scratch"

// gitAuthFailure reports whether a git fetch's error is git's own credentials
// being refused, SSH or HTTPS, rather than a dead remote, a down network or a
// bad path. Substring rather than head-anchored like authFailure in
// refusals.go: git's captured stderr here wraps a transport's own wording
// (ssh, or libcurl for https) ahead of git's own "fatal: Could not read..."
// line, so there is no fixed head to anchor on the way there is for the CLI's
// own text.
func gitAuthFailure(err error) bool {
	msg := err.Error()
	for _, sig := range []string{
		"Permission denied (publickey)",
		"Authentication failed",
		"could not read Username",
	} {
		if strings.Contains(msg, sig) {
			return true
		}
	}
	return false
}

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
// merges itself".
//
// The local refusals are best-effort and logged rather than fatal: a checkout
// on another branch, or with work in the way, is the operator's to sort out and
// none of it is worth ending an overnight drain over. The fetch worked, so the
// issue's branch is still cut from a fresh origin ref and its push will land;
// only a review's base may be off, which is why it is logged loudly.
//
// An origin that cannot be fetched is the one failure returned, for the caller
// to stop on. A run started then works from a base of unknown age, and the
// remote that refused the fetch refuses the run's push too — the money is spent
// and nothing ships. It is not a park either: every later issue meets the same
// dead remote, so parking would label the whole backlog needs-human one issue
// at a time. No origin remote at all is a different condition — there is no
// mirror to keep — and stays a warning.
//
// Git refusing polako's own credentials is a third case, carved out from the
// fatal one by issue #425: unlike a dead remote it is often transient — five
// straight auth failures on 2026-09-19 sat in the same window #396 and #400
// both opened PRs in — so stopping the whole shift over it is the wrong fix.
// This warns (raw stderr is fine in the log, an operator's to read) and
// records it on st instead of returning the fatal error, so the run this
// precedes goes on; st is nil for the tidy sweep's call, which isn't about
// any one about-to-run issue. If that run then ends with no PR, the park
// leads with the real cause — see parkCleanExit — rather than whatever else
// it drew along the way, which is what actually misled #390.
//
// st.fetchAuthFailed is reset to false on every call before anything can
// fail: it means "this leg's own pickup fetch just failed to authenticate",
// not "ever did". An issue can carry the same *issueState across legs (put
// down for a human answer, then resumed once one lands), and without the
// reset a leg whose own fetch succeeded would still inherit an earlier leg's
// stale true — leading that leg's own clean-exit park with a cause that
// wasn't its.
func syncDefaultBranch(ctx context.Context, cfg config, st *issueState) error {
	if st != nil {
		st.fetchAuthFailed = false
	}
	if _, err := git(ctx, cfg, "remote", "get-url", "origin"); err != nil {
		narrate(sevWarning, "no origin remote to fetch in %s, so the default branch is left as it is "+
			"and a review may run against a stale base: %v", cfg.dir, err)
		return nil
	}
	// Retried like a GitHub read, and for the same reason: waking from sleep is
	// exactly when the network is not back yet. A fetch is safe to repeat.
	if _, err := retryRead(ctx, cfg, "git fetch origin", func() ([]byte, error) {
		return git(ctx, cfg, "fetch", "origin", "--quiet")
	}); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if gitAuthFailure(err) {
			narrate(sevWarning, "polako's own git fetch could not authenticate in %s — the run will "+
				"go on, but expect a stale base or a failed push until git access is fixed: %v", cfg.dir, err)
			if st != nil {
				st.fetchAuthFailed = true
			}
			return nil
		}
		return fmt.Errorf("could not fetch origin, so a run would start from a base of unknown age "+
			"and could not push its work — check the network and git's credentials (is the ssh-agent "+
			"unlocked? does `git -C %s fetch origin` work?), then start the drain again: %w", cfg.dir, err)
	}
	head, err := git(ctx, cfg, "symbolic-ref", "refs/remotes/origin/HEAD", "--short")
	if err != nil {
		narrate(sevWarning, "could not resolve origin's default branch, so %s is left as it is "+
			"— run `git remote set-head origin -a` there if reviews look mis-scoped: %v", cfg.dir, err)
		return nil
	}
	remote := strings.TrimSpace(string(head))
	local := strings.TrimPrefix(remote, "origin/")
	on, err := git(ctx, cfg, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return nil
	}
	if got := strings.TrimSpace(string(on)); got != local {
		log.Printf("%s is on %s, not %s — leaving it alone, but a run's review base "+
			"comes from %s, so check it before trusting a review's scope", cfg.dir, got, local, local)
		return nil
	}
	before, _ := git(ctx, cfg, "rev-parse", "HEAD")
	if _, err := git(ctx, cfg, "merge", "--ff-only", remote); err != nil {
		narrate(sevWarning, "could not fast-forward %s to %s, so a review may run against a stale "+
			"base — commit, stash or discard whatever is in the way in %s: %v",
			local, remote, cfg.dir, err)
		return nil
	}
	if after, _ := git(ctx, cfg, "rev-parse", "HEAD"); string(after) != string(before) {
		detail.Printf("fast-forwarded %s to %s", local, remote)
	}
	return nil
}
