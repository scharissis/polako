package main

// The lowest layer: run gh or git in the repo directory and hand back stdout,
// retry a read-only GitHub lookup a few times so a drain does not die on the
// network not having reassociated after a wake, and sleep in a way a shutdown
// signal cancels.

import (
	"context"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"time"
)

func gh(ctx context.Context, cfg config, args ...string) ([]byte, error) {
	return capture(ctx, cfg.dir, cfg.ghBin, ghArgs(cfg.ghRepo, args)...)
}

// ghArgs names the repository on a call that would otherwise be resolved from
// the working directory. Two spellings, because gh has two: every subcommand
// here takes --repo, and `gh api` takes none — it substitutes {owner} and
// {repo} into the path from the repository it resolved, so naming one means
// doing that substitution here instead.
//
// With no repo — the drain, always — the argv is handed back untouched, which
// is the repo-implicit call every path here has always made.
func ghArgs(repo string, args []string) []string {
	if repo == "" || len(args) == 0 {
		return args
	}
	if args[0] != "api" {
		return append(slices.Clone(args), "--repo", repo)
	}
	owner, name, _ := strings.Cut(repo, "/")
	sub := strings.NewReplacer("{owner}", owner, "{repo}", name)
	out := slices.Clone(args)
	for i := range out {
		out[i] = sub.Replace(out[i])
	}
	return out
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
// here is idempotent enough to be worth that.
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
		narrate(sevWarning, "transient: %s failed (%v) — will retry in %s (%d of %d)",
			what, err, dur(cfg.ghRetryWait), attempt, ghReads)
		if err := sleep(ctx, cfg.ghRetryWait); err != nil {
			return zero, err
		}
	}
}

func git(ctx context.Context, cfg config, args ...string) ([]byte, error) {
	return capture(ctx, cfg.dir, "git", args...)
}

func capture(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
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
