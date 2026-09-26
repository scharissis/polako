package main

// Fetching origin, and telling its failures apart: git refusing polako's
// credentials, git losing a ref-lock race to another process, and everything
// else. syncDefaultBranch and proposeSetupFiles both fetch through here.

import (
	"context"
	"fmt"
	"strings"
)

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

// gitRefLockRace reports whether a git fetch failed only because another git
// process in the same repo updated a remote-tracking ref in the same moment:
// "cannot lock ref 'refs/remotes/origin/main': is at X but expected Y".
// Worktrees share refs/remotes, so an operator's terminal, IDE or a second
// shift fetching right after a merge is enough (issue #695: 3 hits in ~168
// post-merge syncs, each healed by the next attempt). Git detected the race
// and changed nothing, so it's not a fault. Both halves are required: "cannot
// lock ref" alone also covers a stale .lock file, which a retry won't fix.
func gitRefLockRace(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "cannot lock ref") && strings.Contains(msg, "but expected")
}

// fetchOrigin is the one `git fetch origin` every caller retries through.
func fetchOrigin(ctx context.Context, cfg config) error {
	return retryFetch(ctx, cfg, func() ([]byte, error) {
		return git(ctx, cfg, "fetch", "origin", "--quiet")
	})
}

// retryFetch is retryRead with one extra: a fetch that lost a ref-lock race
// is retried at once, logged as detail, since the other process has already
// moved the ref and there's nothing to wait for. A second failure goes back to
// retryRead like any other, so a race that somehow persists still warns,
// waits and, past the budget, is returned — nothing new is swallowed.
func retryFetch(ctx context.Context, cfg config, fetch func() ([]byte, error)) error {
	_, err := retryRead(ctx, cfg, "git fetch origin", func() ([]byte, error) {
		out, err := fetch()
		if err != nil && ctx.Err() == nil && gitRefLockRace(err) {
			cfg.detailf("git fetch origin raced another git process on a ref in %s — retrying now", cfg.dir)
			out, err = fetch()
		}
		return out, err
	})
	return err
}

// fetchOriginError is the shift-ending error for an origin polako can't fetch:
// a dead remote on the first try, or an auth failure that outlasted
// authHoldLimit pickups.
func fetchOriginError(dir string, err error) error {
	return fmt.Errorf("could not fetch origin, so a run would start from a base of unknown age "+
		"and could not push its work — check the network and git's credentials (is the ssh-agent "+
		"unlocked? does `git -C %s fetch origin` work?), then start the drain again: %w", dir, err)
}
