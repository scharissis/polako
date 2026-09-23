package main

// The lowest layer: run gh or git in the repo directory and hand back stdout,
// retry a read-only GitHub lookup a few times so a drain does not die on the
// network not having reassociated after a wake, and sleep in a way a shutdown
// signal cancels.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

func gh(ctx context.Context, cfg config, args ...string) ([]byte, error) {
	return capture(ctx, cfg.dir, cfg.env, cfg.ghBin, ghArgs(cfg.ghRepo, args)...)
}

// parseRepoFlag validates -repo's shape: owner/name, both halves non-empty.
// tidyConfig, statusConfig and setupConfig each let -repo name the
// repository outright instead of resolving it from -dir, and all three
// checked it the same way independently before this — one copy means the
// rule and its wording can't drift between them. Empty in, empty out with no
// error: an unset -repo is not this function's business, only the caller's
// (fall back to -dir, most often). "owner/" alone is checked apart from a
// bare missing slash — it reaches gh as a repository with no name and comes
// back as a lookup failure nobody can trace to the flag that caused it.
func parseRepoFlag(repo string) (string, error) {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return "", nil
	}
	owner, name, _ := strings.Cut(repo, "/")
	if strings.Count(repo, "/") != 1 || owner == "" || name == "" {
		return "", fmt.Errorf("-repo %q is not owner/name — e.g. -repo %s", repo, "octocat/hello-world")
	}
	return repo, nil
}

// resolveRepoConfig finishes a starting config the same way tidyConfig,
// unparkConfig and statusConfig each did independently before this: check gh
// is on PATH, make -dir absolute, and pin the repository into ghRepo — from
// -repo if given, else resolved with `gh repo view`. cfg already carries
// ghBin and whatever verb-specific fields the caller set; this only adds
// dir, repo and ghRepo.
//
// access is the clause naming the caller in the PATH error — "tidy reads
// GitHub", "unpark reads and writes GitHub", "status reads GitHub" — since
// the wording isn't identical between verbs; each caller passes its own so
// the error text this produces is byte-identical to what it replaced.
func resolveRepoConfig(ctx context.Context, cfg config, dir, repoFlag, access string) (config, error) {
	if _, err := exec.LookPath(cfg.ghBin); err != nil {
		return cfg, fmt.Errorf("%q not found on PATH (%w) — %s through it", cfg.ghBin, err, access)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return cfg, fmt.Errorf("resolving -dir: %w", err)
	}
	cfg.dir = abs

	repo, err := parseRepoFlag(repoFlag)
	if err != nil {
		return cfg, err
	}
	if repo != "" {
		cfg.repo, cfg.ghRepo = repo, repo
		return cfg, nil
	}
	out, err := gh(ctx, cfg, "repo", "view", "--json", "nameWithOwner", "--jq", ".nameWithOwner")
	if err != nil {
		return cfg, fmt.Errorf("no GitHub repository reachable from %s (is gh authenticated?): %w — "+
			"or name one with -repo owner/name", cfg.dir, err)
	}
	cfg.repo = strings.TrimSpace(string(out))
	cfg.ghRepo = cfg.repo
	return cfg, nil
}

// ghArgs names the repository on a call that would otherwise be resolved from
// the working directory. Three shapes, because gh itself has three: most
// subcommands here take --repo; `gh api` takes none — it substitutes {owner}
// and {repo} into the path from the repository it resolved, so naming one
// means doing that substitution here instead; and `gh repo view` takes
// neither — its repository is a bare positional argument, and passing it
// --repo the way every other subcommand accepts fails outright ("unknown
// flag: --repo"), caught only by running the built binary against a real
// repository — the fake gh the suite tests against had no cause to reject an
// extra flag the same way.
//
// With no repo — the drain, always — the argv is handed back untouched, which
// is the repo-implicit call every path here has always made.
func ghArgs(repo string, args []string) []string {
	if repo == "" || len(args) == 0 {
		return args
	}
	switch {
	case args[0] == "api":
		owner, name, _ := strings.Cut(repo, "/")
		sub := strings.NewReplacer("{owner}", owner, "{repo}", name)
		out := slices.Clone(args)
		for i := range out {
			out[i] = sub.Replace(out[i])
		}
		return out
	case len(args) >= 2 && args[0] == "repo" && args[1] == "view":
		out := make([]string, 0, len(args)+1)
		out = append(out, args[0], args[1], repo)
		return append(out, args[2:]...)
	default:
		return append(slices.Clone(args), "--repo", repo)
	}
}

// ghReads is how many times a read-only GitHub lookup is attempted before its
// failure is taken for real.
//
// The paths that wait on something — supervisePR, waitForReply — have always
// shrugged a failed gh call off and tried again. The lookups that decide what to
// work next never did: one of them failing ends the whole drain, and waking from
// sleep is exactly when a gh call fails for a few seconds because the network
// has not reassociated yet. A backlog that stops overnight for that is the
// failure; this is the same tolerance the wait paths have, bounded.
//
// "A gh that cannot answer" stays fatal. preflight catches the permanent cases
// at startup, and this only ever covers failing a few times in a row.
const ghReads = 3

// ghRetryDelay is the wait between those attempts. Sized for the thing it is
// really waiting on: a laptop's network reassociating after the lid opens,
// which takes seconds rather than milliseconds. Three attempts spread over it
// cost a healthy drain nothing, because a healthy drain never reaches the
// second one.
const ghRetryDelay = 3 * time.Second

// retryRead repeats a read-only GitHub lookup until it answers, at most ghReads
// times. what names the lookup in the log, in the same "transient: … — will
// retry" words the waiting paths use, so one drain reads the same way wherever
// the flakiness lands.
//
// Reads only, deliberately. A retried write is a write that can happen twice —
// a second park comment on a thread, a duplicate close — and none of the writes
// here is idempotent enough to be worth that. `git fetch origin` counts as a
// read: it fails for the same just-woken network, and twice is the same as once.
func retryRead[T any](ctx context.Context, cfg config, what string, read func() (T, error)) (T, error) {
	var zero T
	for attempt := 1; ; attempt++ {
		v, err := read()
		if err == nil {
			return v, nil
		}
		// Ctrl+C is not flakiness, and neither is a lookup that has now failed
		// as often as it is allowed to.
		if ctx.Err() != nil {
			return zero, ctx.Err()
		}
		if attempt >= ghReads {
			return zero, err
		}
		cfg.narrate(sevWarning, "transient: %s failed (%v) — will retry in %s (%d of %d)",
			what, err, dur(cfg.ghRetryWait), attempt, ghReads)
		if err := sleep(ctx, cfg.ghRetryWait); err != nil {
			return zero, err
		}
	}
}

func git(ctx context.Context, cfg config, args ...string) ([]byte, error) {
	return capture(ctx, cfg.dir, cfg.env, "git", args...)
}

// originHead resolves origin's default branch, in both forms callers have
// wanted: remote ("origin/main") and local, its "origin/" prefix stripped
// ("main"). The only non-test code that runs
// `git symbolic-ref refs/remotes/origin/HEAD` — syncDefaultBranch,
// inspectLeftWork, proposeSetupFiles and originDefaultBranch all go through
// this instead of running and trimming it themselves.
func originHead(ctx context.Context, cfg config) (remote, local string, err error) {
	out, err := git(ctx, cfg, "symbolic-ref", "refs/remotes/origin/HEAD", "--short")
	if err != nil {
		return "", "", err
	}
	remote = strings.TrimSpace(string(out))
	return remote, strings.TrimPrefix(remote, "origin/"), nil
}

// childEnv is the environment for a child process: nil when extra is empty —
// production, always — so os/exec passes the parent's environment through
// untouched, which docs/hardening.md's egress-proxy flow relies on; the
// parent's plus extra when the suite has fake-CLI handshake variables to hand
// a child without t.Setenv on the parent. See config.env.
func childEnv(extra []string) []string {
	if len(extra) == 0 {
		return nil
	}
	return append(os.Environ(), extra...)
}

// capture runs name in dir and hands back its stdout. env is extra
// "KEY=value" entries for the child; see childEnv.
func capture(ctx context.Context, dir string, env []string, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = childEnv(env)
	var errBuf strings.Builder
	cmd.Stderr = &errBuf
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err,
			strings.TrimSpace(errBuf.String()))
	}
	return out, nil
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
