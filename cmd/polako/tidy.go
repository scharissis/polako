package main

// `polako tidy` reclaims the worktrees and local branches of issues that are
// finished — closed, or merged and never cleaned up. It is also what the drain
// runs (via tidySweep) at shift start and after every merge it observes, so a
// hand-merge between shifts no longer strands a worktree and branch. This is
// the sweep an operator can also point at that backlog by hand.
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
	"log"
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
	results, err := reclaim(ctx, cfg, opt.apply, 0)
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
	heldBy       string // label name when the skip reason is a human hold (needs-human/proposed)
}

// reclaim is the whole sweep: every local branch matching cfg.branchPrefix
// plus a number, judged and — if -apply says so — removed. watched is the issue
// whose PR the caller just saw merge (0 for none, always so from `polako
// tidy`): for that one the merge is GitHub's own event rather than a guess from
// the branch shape, so the "ancestor of the default branch" test — which a
// squash merge fails even with nothing lost — is not applied, provided the
// local branch still matches what was pushed to origin.
func reclaim(ctx context.Context, cfg config, apply bool, watched int) ([]tidyResult, error) {
	// What origin's copy of the watched branch points at, read before the fetch
	// below prunes it. GitHub deletes a merged PR's head branch, and a fetch
	// with prune set drops the remote-tracking ref the instant it does — so the
	// witnessed-merge shortcut captures the tip here, while it is still around,
	// to confirm the local branch is exactly what merged before it force-deletes
	// past `git branch -d`'s reachability check. Stale is fine: it still names
	// the commit that shipped.
	watchedRemoteTip := ""
	if watched != 0 {
		wb := fmt.Sprintf("%s%d", cfg.branchPrefix, watched)
		if out, err := git(ctx, cfg, "rev-parse", "--verify", "-q", "refs/remotes/origin/"+wb); err == nil {
			watchedRemoteTip = strings.TrimSpace(string(out))
		}
	}

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
		// Witnessed only when the merge event covers the whole local branch:
		// origin's tip was readable before the fetch, and the local branch has
		// not moved past it. A branch with unpushed commits on top is not
		// witnessed — the merge vouches for what was pushed, nothing more — and
		// takes the ordinary conservative path instead.
		witnessed := false
		if issue == watched && watchedRemoteTip != "" {
			if out, err := git(ctx, cfg, "rev-parse", "--verify", "-q", "refs/heads/"+branch); err == nil &&
				strings.TrimSpace(string(out)) == watchedRemoteTip {
				witnessed = true
			}
		}
		results = append(results, reclaimOne(ctx, cfg, issue, branch, apply, witnessed))
	}
	slices.SortFunc(results, func(a, b tidyResult) int { return a.issue - b.issue })
	return results, nil
}

// reclaimOne judges one candidate branch and, if it passes every check and
// apply is true, removes its worktree and deletes it. The checks run in the
// order that establishes nothing is lost: finished on GitHub, merged into the
// default branch, worktree clean, nothing unpushed — first failure is the
// reported reason, and none of it reasons past that failure. watched is set
// only for the branch whose merge the caller witnessed *and* whose local tip
// its caller has already confirmed equal to origin's — so GitHub's merge event
// covers the whole branch. For that one it skips the ancestor-of-default check
// (a squash merge fails it with nothing lost) and deletes with -D, since -d's
// reachability recheck is moot once the merge event is the evidence. That tip
// comparison is the unpushed-commit check, done in the caller; the
// uncommitted-work check still runs here, since a merge event says nothing
// about a dirty worktree.
func reclaimOne(ctx context.Context, cfg config, issue int, branch string, apply, watched bool) tidyResult {
	res := tidyResult{issue: issue, branch: branch}

	why, held, err := issueFinished(ctx, cfg, issue, branch)
	if err != nil {
		res.reason = fmt.Sprintf("could not read GitHub's state for #%d: %v", issue, err)
		return res
	}
	if held != "" {
		// A human put needs-human or proposed on it, and only a human takes
		// either off — the label is the one durable trace that they mean to
		// come back to this, worktree and all. It outranks even a merged PR:
		// someone who finished a parked issue by hand still gets to clear the
		// label themselves. heldBy is set too, so tidySweep can tell this
		// benign, deliberate hold from a worktree that genuinely would not
		// reclaim.
		res.heldBy = held
		res.reason = fmt.Sprintf("held by a human (%s)", held)
		return res
	}
	if why == "" {
		res.reason = "still open"
		return res
	}
	// Set on every path from here down, not just the reclaimed one: a skipped
	// result feeds tidySweep's warning and renderTidy's skipped table, both of
	// which need the PR reference and the worktree directory to tell an
	// operator what to look at and how to clear it.
	res.why = why

	w := inspectLeftWork(ctx, cfg, issue)
	res.worktreePath = w.path
	if w.unreadable {
		// A worktree is here for this branch but its `git status` would not run.
		// Whatever a forced removal would discard cannot be checked first, and an
		// unknown is exactly what this sweep refuses to act past — the same
		// judgement the merge-moment cleanup this replaced made for this case.
		res.reason = "its worktree could not be read for uncommitted work — " +
			"inspect it and remove it by hand with `git worktree remove --force`"
		return res
	}
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
	if !watched {
		if !w.counted {
			res.reason = "could not tell whether it's merged into the default branch"
			return res
		}
		if w.commits > 0 {
			res.reason = fmt.Sprintf("not merged into the default branch (%s ahead) — "+
				"left alone rather than reasoned about a squash merge", plural(w.commits, "commit"))
			return res
		}
	}
	if w.dirty > 0 {
		res.reason = plural(w.dirty, "uncommitted file")
		return res
	}
	if reason := unpushedReason(ctx, cfg, branch); reason != "" {
		res.reason = reason
		return res
	}

	if !apply {
		res.reclaimed = true
		return res
	}
	if w.path != "" {
		// --force, but only past the dirty check above — which already discounts
		// the skill's own untracked PLAN.md, the one thing a clean worktree still
		// carries and the whole reason a plain `worktree remove` would refuse it.
		// Anything git would actually lose here was already counted and skipped.
		if _, err := git(ctx, cfg, "worktree", "remove", "--force", w.path); err != nil {
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
	// The witnessed-merge case is the exception: a squash merge is not
	// reachable from HEAD, so -d would refuse a branch whose work provably
	// shipped, and the merge event GitHub already reported is the check -d
	// wanted. The caller only set watched after confirming the local tip is
	// exactly origin's, so -D here cannot outrun what merged.
	del := "-d"
	if watched {
		del = "-D"
	}
	if _, err := git(ctx, cfg, "branch", del, branch); err != nil {
		res.reason = fmt.Sprintf("removed its worktree but could not delete branch %s: %v", branch, err)
		return res
	}
	res.reclaimed = true
	return res
}

// tidySweep reclaims the worktrees and local branches of every issue it can
// prove finished — closed, or merged into the default branch, worktree clean,
// nothing unpushed — the same judgement `polako tidy` makes. The drain runs it
// once at shift start (between two shifts a human merges PRs by hand, and every
// one of those leaves a worktree no merge-moment cleanup will ever revisit) and
// after every merge it observes, where it reclaims the just-merged issue as one
// case among the rest — one code path, so a bug there is fixed once.
//
// Never fatal, and quiet in the common case: a leftover branch it refuses is
// the operator's to look at later and only earns a shift-log line, not a
// reason to interrupt a backlog that is otherwise clearing — the invariant is
// that a tidy-up must not take a backlog down. Two things are said out loud:
// a sweep that could not run at all (nothing was reclaimed and nothing will be
// until a human looks), and the watched issue's own worktree failing to
// reclaim right after its merge (almost always uncommitted work the merge did
// not take, which is the operator's to deal with now).
//
// `reclaim` runs its own `syncDefaultBranch` first, so the "is an ancestor of
// the default branch" test never runs against a stale mirror — and that same
// refresh is the post-merge sync the merge arm used to make by hand. watched
// is the issue whose merge this call follows (0 at shift start): for that one
// the merge is GitHub's own event, so the ancestor check — which a squash
// merge fails with nothing lost — does not apply to it.
func tidySweep(ctx context.Context, cfg config, watched int) {
	results, err := reclaim(ctx, cfg, true, watched)
	if err != nil {
		narrate(sevWarning, "could not sweep finished worktrees (%v) — nothing was reclaimed; "+
			"run `polako tidy` by hand to clear them", err)
		return
	}
	var reclaimed []string
	for _, r := range results {
		if r.reclaimed {
			reclaimed = append(reclaimed, r.branch)
			continue
		}
		switch {
		case r.heldBy != "":
			// A deliberate human hold — needs-human or proposed — and it
			// outranks the merge state, so even a just-merged issue lands here.
			// Nothing is wrong: the operator clears the label when they are
			// ready, and that is their cue, not something for the drain to
			// raise an alarm about.
			detail.Printf("left %s%d alone: %s", cfg.branchPrefix, r.issue, r.reason)
		case r.issue == watched:
			// The issue whose merge just advanced the drain, and its worktree
			// could not be reclaimed — almost always uncommitted work the merge
			// did not take. That is the operator's to resolve now, not a line to
			// bury, so it names what a person needs to act: the PR it came from
			// (r.why), the directory to look in, and the exact command that
			// clears it once they have saved or discarded what is there.
			fix := "clear it by hand once you have dealt with what is in it"
			// Some skip reasons already spell out the path and the fix (the
			// checkout-tidy-runs-from refusal, a failed `worktree remove`) —
			// don't repeat it after them.
			if r.worktreePath != "" && !strings.Contains(r.reason, r.worktreePath) {
				fix = fmt.Sprintf("deal with what is in %s, then clear it with `git worktree remove --force %s`",
					r.worktreePath, r.worktreePath)
			}
			// r.why is "" only when GitHub's state could not be read at all — in
			// which case r.reason already says so, and leading with a merge that
			// was never confirmed would be wrong.
			lead := fmt.Sprintf("%s%d could not be reclaimed", cfg.branchPrefix, r.issue)
			if r.why != "" {
				lead = fmt.Sprintf("%s%d %s but its worktree could not be reclaimed",
					cfg.branchPrefix, r.issue, r.why)
			}
			narrate(sevWarning, "%s: %s — %s", lead, r.reason, fix)
		case r.reason == "still open":
			// The overwhelmingly common verdict — an issue in the queue, in
			// flight or parked — and it means nothing is wrong. Saying it every
			// pass is the housekeeping noise the drain's narration must not fill
			// with.
		default:
			// A branch that looked finished but failed a safety check: worth a
			// line in the shift log, though it is nobody's to act on mid-drain.
			detail.Printf("left %s%d alone: %s", cfg.branchPrefix, r.issue, r.reason)
		}
	}
	if len(reclaimed) > 0 {
		// The branch is always deleted, the worktree only when one was there —
		// so this counts issues, and names them by branch.
		log.Printf("reclaimed %s: %s", plural(len(reclaimed), "finished issue"), strings.Join(reclaimed, ", "))
	}
}

// issueFinished reports why GitHub considers this issue done — "closed" or
// "merged (PR #N)" — with why "" when it is neither, and held set to the label
// name when a human has put needs-human or proposed on it, which outranks
// everything else: the caller leaves those alone whatever their merge state.
// GitHub is the authority, as always: this never reasons from anything local.
func issueFinished(ctx context.Context, cfg config, issue int, branch string) (why, held string, err error) {
	out, err := retryRead(ctx, cfg, fmt.Sprintf("reading #%d's state", issue), func() ([]byte, error) {
		return gh(ctx, cfg, "issue", "view", strconv.Itoa(issue), "--json", "state,labels")
	})
	if err != nil {
		return "", "", err
	}
	var v struct {
		State  string `json:"state"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return "", "", fmt.Errorf("parsing issue state: %w", err)
	}
	for _, l := range v.Labels {
		if l.Name == needsHumanLabel || l.Name == proposedLabel {
			return "", l.Name, nil
		}
	}
	if v.State == "CLOSED" {
		return "closed", "", nil
	}

	raw, err := retryRead(ctx, cfg, fmt.Sprintf("reading PRs on %s", branch), func() ([]byte, error) {
		return gh(ctx, cfg, "pr", "list", "--head", branch, "--state", "all", "--json", "number,state")
	})
	if err != nil {
		return "", "", err
	}
	var prs []struct {
		Number int    `json:"number"`
		State  string `json:"state"`
	}
	if err := json.Unmarshal(raw, &prs); err != nil {
		return "", "", fmt.Errorf("parsing PR list: %w", err)
	}
	for _, p := range prs {
		if p.State == "MERGED" {
			return fmt.Sprintf("merged (PR #%d)", p.Number), "", nil
		}
	}
	return "", "", nil
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
		reason := r.reason
		if r.worktreePath != "" && !strings.Contains(reason, r.worktreePath) {
			// The path is half of what an operator does next — which directory to
			// look in before `git worktree remove --force` clears it. Some
			// reasons already name it themselves; don't print it twice.
			reason += " — " + r.worktreePath
		}
		skippedRows = append(skippedRows, []string{"#" + strconv.Itoa(r.issue), r.branch, reason})
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
