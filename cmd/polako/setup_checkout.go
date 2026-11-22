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
// origin/HEAD row is affected.
type checkoutGap struct {
	fix      string
	why      string
	ghCanTry bool
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
	if info, err := os.Stat(cfg.dir); err != nil || !info.IsDir() {
		return checkoutGap{fix: fmt.Sprintf("-dir %s doesn't exist or isn't a folder — check the path", cfg.dir),
			why: noCheckout}
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
				"and point -dir at it", cfg.dir, cfg.repo, cfg.repo), why: noCheckout}
		default:
			return checkoutGap{fix: fmt.Sprintf("-dir %s isn't a git repo yet — run `git init`, commit, then "+
				"`gh repo create <name> --private --source . --push` and `git remote set-head origin -a`", cfg.dir),
				why: setupNoRepoYet}
		}
	}
	if _, err := git(ctx, cfg, "remote", "get-url", "origin"); err == nil {
		return checkoutGap{}
	}
	const setHead = ", then `git remote set-head origin -a`"
	if out, err := git(ctx, cfg, "remote"); err == nil && strings.TrimSpace(string(out)) != "" {
		return checkoutGap{fix: fmt.Sprintf("-dir %s has no origin remote — run `git remote rename <name> origin` "+
			"or `git remote add origin <url>`"+setHead, cfg.dir), ghCanTry: true}
	}
	if cfg.repo != "" {
		return checkoutGap{fix: fmt.Sprintf("-dir %s has no origin remote — run `git remote add origin <url of %s>`"+
			setHead, cfg.dir, cfg.repo), why: "no origin remote at -dir"}
	}
	return checkoutGap{fix: fmt.Sprintf("-dir %s has no origin remote — run `gh repo create <name> --private "+
		"--source . --push` or `git remote add origin <url>`"+setHead, cfg.dir), why: setupNoRepoYet}
}
