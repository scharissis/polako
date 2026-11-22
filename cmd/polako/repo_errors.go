package main

// The errors every verb stops on when -dir isn't a GitHub repo polako can
// use (issue #735). A new project is a folder before it's a repo, so these
// are the first errors it hits; they reuse setup's own fixes
// (setupCheckoutGap) so a verb and setup never give different advice. The
// raw git or gh error always follows, for debugging.

import (
	"context"
	"fmt"
	"net/url"
	"os/exec"
	"strings"
)

// checkoutError is nil when git reads -dir as a checkout. Asked before any gh
// call, which would only blame auth for a folder that has no repo.
func checkoutError(ctx context.Context, cfg config) error {
	_, err := git(ctx, cfg, "rev-parse", "--git-dir")
	if err == nil {
		return nil
	}
	gap := setupCheckoutGap(ctx, cfg, true)
	if gap.fix == "" {
		// rev-parse failed, then worked: a race nobody can act on but a rerun.
		return fmt.Errorf("-dir %s is not a git checkout — git said: %w", cfg.dir, err)
	}
	return gapError(gap, "", err)
}

// repoViewError explains a failed `gh repo view` from -dir. hint is extra
// advice the caller adds before the raw error ("" for none).
//
// The checkout is asked first: gh's own errors for a folder with no repo, or
// a repo with no remote, read like auth trouble, and they aren't.
func repoViewError(ctx context.Context, cfg config, ghErr error, hint string) error {
	if _, err := exec.LookPath("git"); err == nil {
		if gap := setupCheckoutGap(ctx, cfg, true); gap.fix != "" && !gap.ghCanTry {
			return gapError(gap, hint, ghErr)
		}
	}
	host := ""
	if out, err := git(ctx, cfg, "config", "--get", "remote.origin.url"); err == nil {
		host = remoteHost(strings.TrimSpace(string(out)))
	}
	return fmt.Errorf("%s%s — gh said: %w", repoViewAdvice(cfg.dir, host, ghErr), hint, ghErr)
}

// remoteHost is the host a remote URL names, "" when it can't tell: the scp
// form (git@host:owner/repo) and URL forms (https://, ssh://).
func remoteHost(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return u.Hostname()
	}
	if at, rest, ok := strings.Cut(raw, "@"); ok && !strings.Contains(at, "/") {
		raw = rest
	}
	// len > 1 keeps a Windows drive letter (C:\repo) from reading as a host.
	if host, _, ok := strings.Cut(raw, ":"); ok && len(host) > 1 && !strings.ContainsAny(host, `/\`) {
		return host
	}
	return ""
}

// gapError is a checkoutGap as an error: the fix, the caller's hint, then
// the raw error — the gap's own cause when it has one, else fallback.
func gapError(gap checkoutGap, hint string, fallback error) error {
	cause := gap.cause
	if cause == nil {
		cause = fallback
	}
	return fmt.Errorf("%s%s — the error was: %w", gap.fix, hint, cause)
}

// repoViewAdvice reads gh's error text, the only signal gh gives, plus
// origin's host. gh's "no known GitHub host" means no remote is on a host gh
// is logged in to — a logged-out gh on a github.com remote says the same as a
// GitLab remote — so the host decides which it is. On github.com it's auth;
// on another host it may be GitHub Enterprise or not GitHub at all, and the
// advice names both. A checkout with no remote never gets auth advice.
func repoViewAdvice(dir, host string, ghErr error) string {
	msg := ""
	if ghErr != nil {
		msg = strings.ToLower(ghErr.Error())
	}
	contains := func(subs ...string) bool {
		for _, s := range subs {
			if strings.Contains(msg, s) {
				return true
			}
		}
		return false
	}
	const notLoggedIn = "gh isn't logged in, or GitHub refused its token — run `gh auth login`, then rerun"
	switch {
	case contains("no git remotes"):
		return fmt.Sprintf("-dir %s has no GitHub remote — run `gh repo create <name> --private --source . --push` "+
			"or `git remote add origin <github url>`, then `git remote set-head origin -a`", dir)
	case contains("known github host") && host == "github.com":
		return notLoggedIn
	case contains("known github host") && host != "":
		return fmt.Sprintf("-dir %s's origin is on %s, which gh isn't logged in to — if that's GitHub Enterprise, "+
			"run `gh auth login --hostname %s`; if it isn't GitHub, run `gh repo create <name> --private` and "+
			"`git remote set-url origin <github url>`, then `git remote set-head origin -a`", dir, host, host)
	case contains("known github host"):
		return fmt.Sprintf("-dir %s has no GitHub remote — run `gh repo create <name> --private --source . --push` "+
			"or `git remote set-url origin <github url>`, then `git remote set-head origin -a`", dir)
	case contains("gh auth login", "not logged in", "http 401", "bad credentials", "authentication"):
		return notLoggedIn
	default:
		return fmt.Sprintf("could not read the GitHub repository at -dir %s — check that origin names a "+
			"repository you can see, and that `gh auth status` is happy", dir)
	}
}
