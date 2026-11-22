package main

// What -dir lacks before polako can use it (issue #733). A new project is a
// folder before it's a repo, and setup is the first command the README
// sends it to — so this is asked before any gh call, which would only retry
// a lookup that can't work and blame auth.

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// setupNoRepoYet is the detail on every row that needs a repo when -dir
// isn't one yet — the fix itself sits on the first such row.
const setupNoRepoYet = "no repo yet"

// checkoutGap is setupCheckoutGap's answer. fix is what a human runs, ""
// when -dir is ready. why is the detail for each row that can't tell
// because of it. ghCanTry marks a checkout whose remote just isn't called
// origin: gh may still find the repository through it, so only the
// origin/HEAD row is affected. cause is the raw error behind fix, nil when
// fix already quotes it — the other verbs print it after the advice.
type checkoutGap struct {
	fix      string
	why      string
	ghCanTry bool
	cause    error
}

// setupCheckoutGap reads -dir with git alone. Nothing to say when git isn't
// on PATH: its own row already does. `gh repo create` leaves origin/HEAD
// unset, hence the set-head step on every fix that ends with a remote.
func setupCheckoutGap(ctx context.Context, cfg config, gitOK bool) checkoutGap {
	if !gitOK {
		return checkoutGap{}
	}
	const noCheckout = "no checkout at -dir"
	// Only git's own "not a git repository" earns the git-init advice. A typo'd
	// -dir, or a real repo git refuses (safe.directory, permissions), would be
	// told to make a second repo.
	info, err := os.Stat(cfg.dir)
	if err == nil && !info.IsDir() {
		err = fmt.Errorf("%s is a file", cfg.dir)
	}
	if err != nil {
		return checkoutGap{fix: fmt.Sprintf("-dir %s doesn't exist or isn't a folder — check the path", cfg.dir),
			why: noCheckout, cause: err}
	}
	if _, err := git(ctx, cfg, "rev-parse", "--git-dir"); err != nil {
		switch {
		case !strings.Contains(err.Error(), "not a git repository"):
			return checkoutGap{fix: fmt.Sprintf("git can't read -dir %s (%v) — fix that, then rerun", cfg.dir, err),
				why: noCheckout}
		case cfg.repo != "":
			// -repo names a repository that already exists: clone it, don't
			// make a second one.
			return checkoutGap{fix: fmt.Sprintf("-dir %s isn't a checkout of %s — run `gh repo clone %s` "+
				"and point -dir at it", cfg.dir, cfg.repo, cfg.repo), why: noCheckout, cause: err}
		default:
			return checkoutGap{fix: fmt.Sprintf("-dir %s isn't a git repo yet — run `git init`, commit, then "+
				"`gh repo create <name> --private --source . --push` and `git remote set-head origin -a`; "+
				"or point -dir at a checkout", cfg.dir),
				why: setupNoRepoYet, cause: err}
		}
	}
	_, originErr := git(ctx, cfg, "remote", "get-url", "origin")
	if originErr == nil {
		return checkoutGap{}
	}
	const setHead = ", then `git remote set-head origin -a`"
	if out, err := git(ctx, cfg, "remote"); err == nil && strings.TrimSpace(string(out)) != "" {
		return checkoutGap{fix: fmt.Sprintf("-dir %s has no origin remote — run `git remote rename <name> origin` "+
			"or `git remote add origin <url>`"+setHead, cfg.dir), ghCanTry: true, cause: originErr}
	}
	if cfg.repo != "" {
		return checkoutGap{fix: fmt.Sprintf("-dir %s has no origin remote — run `git remote add origin <url of %s>`"+
			setHead, cfg.dir, cfg.repo), why: "no origin remote at -dir", cause: originErr}
	}
	return checkoutGap{fix: fmt.Sprintf("-dir %s has no origin remote — run `gh repo create <name> --private "+
		"--source . --push` or `git remote add origin <url>`"+setHead, cfg.dir), why: setupNoRepoYet, cause: originErr}
}
