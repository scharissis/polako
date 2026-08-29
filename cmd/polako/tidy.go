package main

// `polako tidy` reclaims the worktrees and local branches of issues that are
// finished — closed, or merged and never cleaned up. Cleanup today runs only
// from inside a drain that watched a merge itself; every other run, and every
// interactive one, leaves its worktree and branch behind. This is the sweep an
// operator can point at that backlog once, and it will be the sibling issue's
// hook underneath a future drain.
//
// It proves a branch safe to remove before removing it — closed or merged,
// merged into the default branch, its worktree clean, nothing unpushed — and
// reports what it refused exactly as loudly as what it did: a skip is always
// recoverable, a wrong removal is not. -dry-run is the default for the same
// reason: every other verb here defaults to acting, but this is the one verb
// whose actions cannot be undone.

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

type tidyOptions struct {
	dir          string
	repo         string
	branchPrefix string
	apply        bool
}

// runTidy is the `tidy` subcommand: parse its own flags, sweep, report. Its
// config is built the lightweight way status's is — resolve -repo/-dir, check
// gh is on PATH — not work's heavier preflight, which readies a shift to run
// the skill.
func runTidy(ctx context.Context, args []string, out io.Writer, rpt report) error {
	fs := flag.NewFlagSet("tidy", flag.ContinueOnError)
	fs.SetOutput(out)
	var opt tidyOptions
	fs.StringVar(&opt.dir, "dir", ".", "path to the repository's main checkout, when -repo is not given")
	fs.StringVar(&opt.repo, "repo", "",
		"repository to reclaim in (owner/name), instead of whichever -dir is a checkout of")
	fs.StringVar(&opt.branchPrefix, "branch-prefix", "issue-", "branch name prefix the skill uses")
	fs.BoolVar(&opt.apply, "apply", false,
		"remove worktrees and delete branches, rather than only reporting what would happen")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "Usage: polako tidy [flags]\n\n"+
			"Reclaims the worktrees and local branches of issues that are finished:\n"+
			"closed, or merged with the PR gone. Proves each one safe before touching it,\n"+
			"and reports what it skipped as loudly as what it reclaimed.\n\n"+
			"-dry-run by default — the one verb here whose actions cannot be undone.\n\n"+envUsage+"\nFlags:\n")
		fs.PrintDefaults()
	}
	if err := applyEnvDefaults(fs); err != nil {
		return err
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return errFlagsReported
	}
	if rest := fs.Args(); len(rest) > 0 {
		return fmt.Errorf("unexpected argument %q — tidy takes flags only", rest[0])
	}

	cfg, err := tidyConfig(ctx, opt)
	if err != nil {
		return err
	}
	results, err := reclaim(ctx, cfg, opt.apply)
	if err != nil {
		return err
	}
	renderTidy(out, rpt, cfg, opt.apply, results)
	return nil
}

// tidyConfig resolves which repository is being reclaimed in, the same way
// statusConfig does: -repo names it outright, or gh resolves it from -dir.
func tidyConfig(ctx context.Context, opt tidyOptions) (config, error) {
	cfg := config{
		ghBin:        "gh",
		ghRetryWait:  ghRetryDelay,
		branchPrefix: opt.branchPrefix,
	}
	if _, err := exec.LookPath(cfg.ghBin); err != nil {
		return cfg, fmt.Errorf("%q not found on PATH (%w) — tidy reads GitHub through it", cfg.ghBin, err)
	}
	abs, err := filepath.Abs(opt.dir)
	if err != nil {
		return cfg, fmt.Errorf("resolving -dir: %w", err)
	}
	cfg.dir = abs

	if repo := strings.TrimSpace(opt.repo); repo != "" {
		owner, name, _ := strings.Cut(repo, "/")
		if strings.Count(repo, "/") != 1 || owner == "" || name == "" {
			return cfg, fmt.Errorf("-repo %q is not owner/name — e.g. -repo %s", repo, "octocat/hello-world")
		}
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

// tidyResult is one candidate branch's verdict: reclaimed (or, under
// -dry-run, would be), or skipped with the reason a human would act on.
type tidyResult struct {
	issue        int
	branch       string
	reclaimed    bool
	why          string // set when reclaimed: "closed" or "merged (PR #N)"
	worktreePath string // "" when the branch carries no live worktree
	reason       string // set when not reclaimed: why it was left alone
}

// reclaim is the whole sweep: every local branch matching cfg.branchPrefix
// plus a number, judged and — if -apply says so — removed.
func reclaim(ctx context.Context, cfg config, apply bool) ([]tidyResult, error) {
	// The ancestor check below means nothing against a stale mirror, so the
	// sweep runs after this and never before it — the same rule the drain
	// itself follows picking up an issue.
	syncDefaultBranch(ctx, cfg)

	if apply {
		// A worktree whose directory was removed by hand still marks its
		// branch checked out, by git's own admin records, until pruned —
		// which would otherwise fail that branch's delete below for a reason
		// nothing there would explain. Once for the whole sweep, before it
		// looks at any branch, rather than once per worktree-less branch it
		// finds along the way: cheap either way, but there is no reason to
		// pay for it twice per branch let alone N times.
		_, _ = git(ctx, cfg, "worktree", "prune")
	}

	raw, err := git(ctx, cfg, "for-each-ref", "--format=%(refname:short)", "refs/heads/")
	if err != nil {
		return nil, fmt.Errorf("listing local branches: %w", err)
	}
	var results []tidyResult
	for _, branch := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		branch = strings.TrimSpace(branch)
		issue, ok := issueForBranch(branch, cfg.branchPrefix)
		if !ok {
			continue
		}
		results = append(results, reclaimOne(ctx, cfg, issue, branch, apply))
	}
	slices.SortFunc(results, func(a, b tidyResult) int { return a.issue - b.issue })
	return results, nil
}

// reclaimOne judges one candidate branch and, if it passes every check and
// apply is true, removes its worktree and deletes it. The checks run in the
// order that establishes nothing is lost: finished on GitHub, merged into the
// default branch, worktree clean, nothing unpushed — first failure is the
// reported reason, and none of it reasons past that failure.
func reclaimOne(ctx context.Context, cfg config, issue int, branch string, apply bool) tidyResult {
	res := tidyResult{issue: issue, branch: branch}

	why, err := issueFinished(ctx, cfg, issue, branch)
	if err != nil {
		res.reason = fmt.Sprintf("could not read GitHub's state for #%d: %v", issue, err)
		return res
	}
	if why == "" {
		res.reason = "still open"
		return res
	}

	w := inspectLeftWork(ctx, cfg, issue)
	if w.path != "" && samePath(w.path, cfg.dir) {
		// git refuses to remove the main working tree, but a linked one gets
		// no such protection — `git worktree remove` deletes it even while
		// its own process is running with that directory as its cwd, which is
		// exactly what every other git/gh call in this sweep does (cfg.dir is
		// always the working directory capture() runs in). An operator who
		// cd's into a finished issue's worktree and runs tidy from right
		// there would otherwise have the ground removed out from under the
		// rest of this very sweep.
		res.reason = fmt.Sprintf("its worktree (%s) is the checkout tidy is running from — "+
			"rerun from the main checkout instead", w.path)
		return res
	}
	if !w.counted {
		res.reason = "could not tell whether it's merged into the default branch"
		return res
	}
	if w.commits > 0 {
		res.reason = fmt.Sprintf("not merged into the default branch (%s ahead) — "+
			"left alone rather than reasoned about a squash merge", plural(w.commits, "commit"))
		return res
	}
	if w.dirty > 0 {
		res.reason = plural(w.dirty, "uncommitted file")
		return res
	}
	if reason := unpushedReason(ctx, cfg, branch); reason != "" {
		res.reason = reason
		return res
	}

	res.worktreePath = w.path
	res.why = why
	if !apply {
		res.reclaimed = true
		return res
	}
	if w.path != "" {
		if _, err := git(ctx, cfg, "worktree", "remove", w.path); err != nil {
			res.reason = fmt.Sprintf("could not remove worktree %s: %v", w.path, err)
			return res
		}
	}
	// -d, not -D: it re-checks that branch is merged, against the checkout's
	// own HEAD rather than the origin ancestor check above, and refusing here
	// is safe — the worktree is already gone, and the branch is picked up
	// again on the next run. -D would skip that check instead of merely
	// duplicating it, and this is the one verb whose actions cannot be undone,
	// so a redundant safety net stays rather than being traded for a tidier
	// exit on the rare run where cfg.dir is not sitting on the default branch.
	if _, err := git(ctx, cfg, "branch", "-d", branch); err != nil {
		res.reason = fmt.Sprintf("removed its worktree but could not delete branch %s: %v", branch, err)
		return res
	}
	res.reclaimed = true
	return res
}

// issueFinished reports why GitHub considers this issue done — "closed" or
// "merged (PR #N)" — or "" when it is neither, which is the caller's
// signal to leave it alone. GitHub is the authority, as always: this never
// reasons from anything local.
func issueFinished(ctx context.Context, cfg config, issue int, branch string) (string, error) {
	out, err := retryRead(ctx, cfg, fmt.Sprintf("reading #%d's state", issue), func() ([]byte, error) {
		return gh(ctx, cfg, "issue", "view", strconv.Itoa(issue), "--json", "state")
	})
	if err != nil {
		return "", err
	}
	var v struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return "", fmt.Errorf("parsing issue state: %w", err)
	}
	if v.State == "CLOSED" {
		return "closed", nil
	}

	raw, err := retryRead(ctx, cfg, fmt.Sprintf("reading PRs on %s", branch), func() ([]byte, error) {
		return gh(ctx, cfg, "pr", "list", "--head", branch, "--state", "all", "--json", "number,state")
	})
	if err != nil {
		return "", err
	}
	var prs []struct {
		Number int    `json:"number"`
		State  string `json:"state"`
	}
	if err := json.Unmarshal(raw, &prs); err != nil {
		return "", fmt.Errorf("parsing PR list: %w", err)
	}
	for _, p := range prs {
		if p.State == "MERGED" {
			return fmt.Sprintf("merged (PR #%d)", p.Number), nil
		}
	}
	return "", nil
}

// unpushedReason reports whether branch carries commits its own
// remote-tracking ref does not, or "" when there is nothing to say. Absent
// entirely is common and not itself a problem — GitHub can delete a head
// branch on merge — and not a reason to refuse: the ancestor check already
// established the content is on the default branch, which is the only
// guarantee this sweep makes.
func unpushedReason(ctx context.Context, cfg config, branch string) string {
	remoteSHA, err := git(ctx, cfg, "rev-parse", "--verify", "-q", "refs/remotes/origin/"+branch)
	if err != nil {
		return ""
	}
	localSHA, err := git(ctx, cfg, "rev-parse", branch)
	if err != nil {
		return ""
	}
	if strings.TrimSpace(string(remoteSHA)) != strings.TrimSpace(string(localSHA)) {
		return fmt.Sprintf("has commits not pushed to origin/%s", branch)
	}
	return ""
}

// samePath reports whether a and b name the same location on disk, resolving
// symlinks first so a path through /tmp and the same path through its
// resolved form (e.g. /private/tmp on macOS) still compare equal. Falls back
// to a plain Clean comparison when either side does not resolve — treating an
// unresolvable path as possibly-the-same is the side to err on here, since
// this exists to refuse a removal rather than allow one.
func samePath(a, b string) bool {
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	if errA == nil && errB == nil {
		return ra == rb
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

// --- rendering ---

func renderTidy(w io.Writer, rpt report, cfg config, apply bool, results []tidyResult) {
	if len(results) == 0 {
		fmt.Fprintf(w, "%s — nothing to reclaim: no %sN branch found\n", cfg.repo, cfg.branchPrefix)
		return
	}
	fmt.Fprintf(w, "%s\n", rpt.bold(cfg.repo))

	var reclaimedRows, skippedRows [][]string
	for _, r := range results {
		if r.reclaimed {
			detail := "branch deleted"
			if r.worktreePath != "" {
				detail = "worktree removed, branch deleted"
			}
			reclaimedRows = append(reclaimedRows,
				[]string{"#" + strconv.Itoa(r.issue), r.branch, r.why, detail})
			continue
		}
		skippedRows = append(skippedRows, []string{"#" + strconv.Itoa(r.issue), r.branch, r.reason})
	}

	reclaimedTitle := "reclaimed"
	if !apply {
		reclaimedTitle = "would reclaim (-apply to do it)"
	}
	if len(reclaimedRows) > 0 {
		printTable(w, rpt, reclaimedTitle, []string{"issue", "branch", "why", "action"}, reclaimedRows, 4)
	}
	if len(skippedRows) > 0 {
		printTable(w, rpt, "skipped", []string{"issue", "branch", "reason"}, skippedRows, 3)
	}
}
