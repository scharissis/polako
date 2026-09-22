package main

// Issue #416's own half of `polako setup`: the tree rows
// that read the checkout directly rather than gh, and -apply's first write
// beyond a label — one commit, on a `polako-setup` branch, built in a
// worktree, pushed, behind one PR a human merges. Split out of setup.go and
// setup_apply.go so both stay closer to the repo's own median length as this
// grows; see setup.go for the read-only report and setup_apply.go for the
// label half of -apply these functions sit beside. The .gitignore row and
// write live here; the CLAUDE.md block (setup_claude.go) and the
// docs/VISION.md scaffold (setup_scaffold.go) are split into their own files
// but share this file's write pass and restart-safety rules — one PR from
// `polako-setup` covers whichever of the three a run accepts.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// setupGitignoreLines is the .gitignore content the skill's own scratch
// needs to stay untracked in a fresh repo: /.worktrees/ (implement-issue's
// own worktree, one per issue), /PLAN.md (the resume note it writes into the
// worktree root) and /.polako-scratch/ (everything else throwaway — PR
// bodies, diff dumps — which the skill also makes self-ignoring per
// worktree by writing a nested `.polako-scratch/.gitignore` of its own, but
// a top-level line is the second line of defense before that write has
// happened). Mirrors this repo's own .gitignore.
var setupGitignoreLines = []string{"/.worktrees/", "/PLAN.md", "/.polako-scratch/"}

// setupBranch is the one branch -apply ever proposes repo files on — never
// issue-N, which is the skill's own naming contract, not setup's.
const setupBranch = "polako-setup"

// setupFilesCommitSubject is both the commit subject and the PR title —
// issue #416's own wording.
const setupFilesCommitSubject = "chore: set up polako"

// setupGitignoreRow reads .gitignore from the checkout directly — no git, no
// gh — and reports which of setupGitignoreLines it's missing. Not required:
// a repo without it still runs `polako work` fine, it only risks the
// skill's own scratch getting committed by an operator's stray `git add -A`.
func setupGitignoreRow(cfg config) setupRow {
	const name = ".gitignore"
	missing := missingGitignoreLines(readGitignoreLines(cfg.dir))
	if len(missing) == 0 {
		return setupRow{name: name, status: setupOK}
	}
	return setupRow{name: name, status: setupMissing,
		detail: fmt.Sprintf("missing %s — `polako setup -apply` proposes it through a PR", strings.Join(missing, ", "))}
}

// missingGitignoreLines is setupGitignoreLines minus whatever have already
// carries, shared by the read-only row above and proposeSetupFiles' own
// check against the worktree's copy of the file.
func missingGitignoreLines(have []string) []string {
	var missing []string
	for _, want := range setupGitignoreLines {
		if !slices.Contains(have, want) {
			missing = append(missing, want)
		}
	}
	return missing
}

// readGitignoreLines reads dir's .gitignore, trimmed line by line. A missing
// file reads as no lines at all — not an error, since "no .gitignore yet" is
// exactly the state this row and -apply both have to handle.
func readGitignoreLines(dir string) []string {
	b, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		return nil
	}
	var lines []string
	for line := range strings.SplitSeq(string(b), "\n") {
		lines = append(lines, strings.TrimSpace(line))
	}
	return lines
}

// setupFileWants is which of the three files this run asked to propose —
// built from the three prompts applySetupFiles asks, in order, before it
// ever looks for an existing PR.
type setupFileWants struct {
	gitignore, claudeMd, scaffold bool
}

func (w setupFileWants) any() bool { return w.gitignore || w.claudeMd || w.scaffold }

// setupFilesResult is what the branch proposeSetupFiles pushed actually
// carries over remoteDefault — distinct from setupFileWants because a
// requested item can turn out to already be satisfied on the freshly-fetched
// worktree (the merged-PR fallthrough case .gitignore already had),
// independently of whether the other requested items changed anything. Each
// row needs its own verdict, not one shared by the whole PR. Decided from a
// diff against remoteDefault, not from whether this call's own write step
// did anything — a worktree reused from a dead run's own earlier commit
// already carries an item this call finds nothing left to write, and that
// item is still on the branch and still belongs in the PR.
type setupFilesResult struct {
	url                                                   string
	proposedGitignore, proposedClaudeMd, proposedScaffold bool
}

// applySetupFiles is -apply's file-proposal pass, run once after the label
// pass (applySetup) using the same prompt — see setupPrompt's own doc
// comment for why one Scanner has to serve every question this run asks.
// Best-effort like applySetup's own creates: a failure here is reported and
// setup moves on, never ends the run.
func applySetupFiles(ctx context.Context, prompt *setupPrompt, cfg config, rows []setupRow) []setupRow {
	gitignoreIdx := slices.IndexFunc(rows, func(r setupRow) bool { return r.name == ".gitignore" })
	claudeIdx := slices.IndexFunc(rows, func(r setupRow) bool { return r.name == "CLAUDE.md" })
	visionIdx := slices.IndexFunc(rows, func(r setupRow) bool { return r.name == visionMdPath })

	var want setupFileWants
	if gitignoreIdx >= 0 && rows[gitignoreIdx].status == setupMissing {
		want.gitignore = prompt.confirm(fmt.Sprintf("propose the missing .gitignore lines through a PR on %q?", setupBranch))
	}
	if claudeIdx >= 0 && rows[claudeIdx].status == setupMissing {
		want.claudeMd = prompt.confirm(fmt.Sprintf("propose the CLAUDE.md polako block through a PR on %q?", setupBranch))
	}
	if visionIdx >= 0 && rows[visionIdx].status == setupMissing {
		want.scaffold = prompt.confirmDefault(
			"also scaffold docs/VISION.md and docs/plans/README.md through the same PR?", false)
	}
	if !want.any() {
		return rows
	}

	// Restart safety, the same rule the drain holds to for issue-N: an
	// existing PR from this branch means report it and stop, never a second
	// one. That covers OPEN (still waiting on a human) and CLOSED (a human
	// declined it — reopening the exact same proposal would override that
	// decision) alike. MERGED is the one state that falls through: the
	// worktree below is cut from a freshly fetched default branch, so
	// whatever already landed there is found already satisfied rather than
	// stopping on a stale "missing". A third case never reaches this check
	// at all: a branch pushed but with no PR yet (a dead run that died
	// between the push and gh pr create) finds pr == nil here and falls
	// through to proposeSetupFiles, which reuses that branch (setupWorktree)
	// and opens the PR that never got opened — its rows and body built from
	// a fresh diff against remoteDefault, not from whatever this call would
	// have written itself, so an item the dead run already committed still
	// gets reported and named.
	pr, err := prForBranch(ctx, cfg, setupBranch)
	if err != nil {
		fmt.Fprintf(prompt.out, "  could not check for an existing %s PR (%v) — skipping\n", setupBranch, err)
		return rows
	}
	if pr != nil && pr.State != "MERGED" {
		// prForBranch fetches no file list, so this can't say which of the
		// wanted items the open PR actually holds — a rerun that now also
		// wants CLAUDE.md, say, would otherwise be told it's "already
		// proposed" when it isn't. Say the PR itself is open instead, and
		// tell the human what to do about it; that self-heals once they
		// merge or close it.
		verb := "a setup PR is already proposed"
		if pr.State == "CLOSED" {
			verb = "a setup PR was already proposed, then closed without merging"
		}
		detail := verb + " — merge or close it, then rerun: " + pr.URL
		fmt.Fprintf(prompt.out, "  %s: %s\n", verb, pr.URL)
		if gitignoreIdx >= 0 && want.gitignore {
			rows[gitignoreIdx].detail = detail
		}
		if claudeIdx >= 0 && want.claudeMd {
			rows[claudeIdx].detail = detail
		}
		if visionIdx >= 0 && want.scaffold {
			rows[visionIdx].detail = detail
		}
		return rows
	}

	result, err := proposeSetupFiles(ctx, cfg, want)
	if err != nil {
		fmt.Fprintf(prompt.out, "  could not propose the setup PR (%v)\n", err)
		return rows
	}
	if want.gitignore {
		rows[gitignoreIdx] = resolveSetupFileRow(".gitignore", result.proposedGitignore, result.url)
	}
	if want.claudeMd {
		rows[claudeIdx] = resolveSetupFileRow("CLAUDE.md", result.proposedClaudeMd, result.url)
	}
	if want.scaffold {
		rows[visionIdx] = resolveSetupFileRow(visionMdPath, result.proposedScaffold, result.url)
	}
	if result.url == "" {
		fmt.Fprintln(prompt.out, "  nothing left to propose — already on the default branch")
	} else {
		fmt.Fprintf(prompt.out, "  proposed the setup PR: %s\n", result.url)
	}
	return rows
}

// resolveSetupFileRow is one requested item's row once proposeSetupFiles has
// run: ok when the freshly-fetched worktree already had it and the pushed
// branch carries no change for it, missing-with-a-link when the branch does
// (whether this call's own write step added it or an earlier dead run's
// commit already had) and a PR is now waiting on a human.
func resolveSetupFileRow(name string, proposed bool, url string) setupRow {
	if !proposed {
		return setupRow{name: name, status: setupOK}
	}
	return setupRow{name: name, status: setupMissing, detail: "proposed: " + url}
}

// proposeSetupFiles does the write pass issue #416 draws: fetch, resolve
// origin's default branch, and `worktree add` all run in the main checkout
// (cfg); everything past that — writing the files, `add`, `commit`, `push` —
// runs in the worktree, through a copy of cfg pointed at its path. Each
// requested item is checked against the freshly-fetched worktree before
// being written, so one already satisfied there (a previous polako-setup PR
// that merged some but not all of them) is skipped rather than redone.
func proposeSetupFiles(ctx context.Context, cfg config, want setupFileWants) (setupFilesResult, error) {
	// -repo lets the read-only report check a repository -dir isn't a
	// checkout of ("instead of whichever -dir is a checkout of" — setup.go's
	// own -repo flag doc). Every git write below operates on cfg.dir's local
	// origin, not cfg.repo, so that combination has to be refused here or it
	// silently pushes a branch to a repository the operator never named.
	if local, err := repoFromOriginURL(ctx, cfg); err == nil && local != "" && !strings.EqualFold(local, cfg.repo) {
		return setupFilesResult{}, fmt.Errorf("-dir is a checkout of %s, not %s (-repo) — the file-proposal "+
			"write needs -dir to be a checkout of the repository being set up", local, cfg.repo)
	}
	// Retried like syncDefaultBranch's own fetch: waking from sleep is
	// exactly when the network hasn't reassociated yet, and a fetch is safe
	// to repeat.
	if _, err := retryRead(ctx, cfg, "git fetch origin", func() ([]byte, error) {
		return git(ctx, cfg, "fetch", "origin", "--quiet")
	}); err != nil {
		return setupFilesResult{}, fmt.Errorf("fetching origin: %w", err)
	}
	head, err := git(ctx, cfg, "symbolic-ref", "refs/remotes/origin/HEAD", "--short")
	if err != nil {
		return setupFilesResult{}, fmt.Errorf("resolving origin's default branch: %w", err)
	}
	remoteDefault := strings.TrimSpace(string(head)) // "origin/main"
	defaultBranch := strings.TrimPrefix(remoteDefault, "origin/")

	path, err := setupWorktree(ctx, cfg, remoteDefault)
	if err != nil {
		return setupFilesResult{}, err
	}
	wtCfg := cfg
	wtCfg.dir = path

	var wroteGitignore, wroteClaudeMd, wroteScaffold bool
	if want.gitignore {
		missing := missingGitignoreLines(readGitignoreLines(path))
		if len(missing) > 0 {
			if err := appendGitignoreLines(path, missing); err != nil {
				return setupFilesResult{}, fmt.Errorf("writing .gitignore: %w", err)
			}
			if _, err := git(ctx, wtCfg, "add", ".gitignore"); err != nil {
				return setupFilesResult{}, fmt.Errorf("staging .gitignore: %w", err)
			}
			wroteGitignore = true
		}
	}
	if want.claudeMd && claudeMdNeedsUpdate(path) {
		if err := writeClaudeMdBlock(path); err != nil {
			return setupFilesResult{}, fmt.Errorf("writing CLAUDE.md: %w", err)
		}
		if _, err := git(ctx, wtCfg, "add", "CLAUDE.md"); err != nil {
			return setupFilesResult{}, fmt.Errorf("staging CLAUDE.md: %w", err)
		}
		wroteClaudeMd = true
	}
	if want.scaffold && scaffoldNeedsWrite(path) {
		if err := writeScaffold(path); err != nil {
			return setupFilesResult{}, fmt.Errorf("writing the scaffold: %w", err)
		}
		if _, err := git(ctx, wtCfg, "add", visionMdPath, plansReadmePath); err != nil {
			return setupFilesResult{}, fmt.Errorf("staging the scaffold: %w", err)
		}
		wroteScaffold = true
	}
	if wroteGitignore || wroteClaudeMd || wroteScaffold {
		if _, err := git(ctx, wtCfg, "commit", "-m", setupFilesCommitSubject); err != nil {
			return setupFilesResult{}, fmt.Errorf("committing: %w", err)
		}
	}

	// Checked unconditionally, not just when this call itself committed
	// something: a worktree reused from a dead run (setupWorktree, the
	// existing-local/remote-branch cases) can already carry a commit ahead
	// of remoteDefault that never got pushed. Without this, a resumed run
	// that finds every item already satisfied on disk would report "nothing
	// to propose" and leave that earlier commit stranded, unpushed.
	ahead, err := git(ctx, wtCfg, "rev-list", "--count", remoteDefault+"..HEAD")
	if err != nil {
		return setupFilesResult{}, fmt.Errorf("checking %s against %s: %w", setupBranch, remoteDefault, err)
	}
	if strings.TrimSpace(string(ahead)) == "0" {
		return setupFilesResult{}, nil // nothing to propose — see the caller
	}

	// What actually goes into the PR: read off the branch itself against
	// remoteDefault, not the wrote* flags above. Those only cover what this
	// call's own write step did — a worktree reused from a dead run's own
	// earlier, unpushed commit can carry an item that step found nothing
	// left to write for, and that item is on the branch regardless and
	// still belongs in the row and the PR body.
	diffOut, err := git(ctx, wtCfg, "diff", "--name-only", remoteDefault+"..HEAD")
	if err != nil {
		return setupFilesResult{}, fmt.Errorf("diffing %s against %s: %w", setupBranch, remoteDefault, err)
	}
	changed := strings.Fields(string(diffOut))
	result := setupFilesResult{
		proposedGitignore: want.gitignore && slices.Contains(changed, ".gitignore"),
		proposedClaudeMd:  want.claudeMd && slices.Contains(changed, "CLAUDE.md"),
		proposedScaffold: want.scaffold &&
			(slices.Contains(changed, visionMdPath) || slices.Contains(changed, plansReadmePath)),
	}

	// Never --force: a rerun that finds the branch already pushed (a dead
	// run's own push, or a human's edit) builds on it rather than
	// overwriting it.
	if _, err := git(ctx, wtCfg, "push", "-u", "origin", setupBranch); err != nil {
		return setupFilesResult{}, fmt.Errorf("pushing %s: %w", setupBranch, err)
	}

	out, err := gh(ctx, cfg, "pr", "create", "--head", setupBranch, "--base", defaultBranch,
		"--title", setupFilesCommitSubject, "--body", setupFilesPRBody(result))
	if err != nil {
		return setupFilesResult{}, fmt.Errorf("opening the PR: %w", err)
	}
	result.url = strings.TrimSpace(string(out))
	return result, nil
}

// repoFromOriginURL extracts "owner/repo" from cfg.dir's local origin
// remote — the same shapes GitHub hands out its own clone URLs in:
// https://host/owner/repo(.git), ssh://git@host/owner/repo(.git), and the
// scp-like git@host:owner/repo(.git). "", nil for anything else, including a
// local filesystem path (a bare repo on disk, as every test fixture here
// uses): never a guess, since the caller only acts on a positive, confident
// mismatch.
func repoFromOriginURL(ctx context.Context, cfg config) (string, error) {
	out, err := git(ctx, cfg, "remote", "get-url", "origin")
	if err != nil {
		return "", err
	}
	url := strings.TrimSuffix(strings.TrimSpace(string(out)), ".git")
	switch {
	case strings.HasPrefix(url, "https://"), strings.HasPrefix(url, "http://"), strings.HasPrefix(url, "ssh://"):
		_, url, _ = strings.Cut(url, "://")
		if _, rest, ok := strings.Cut(url, "@"); ok {
			url = rest // ssh://git@host/owner/repo -> host/owner/repo
		}
	case strings.Contains(url, "@") && strings.Contains(url, ":"):
		_, rest, _ := strings.Cut(url, "@")
		url = strings.Replace(rest, ":", "/", 1) // git@host:owner/repo -> host/owner/repo
	default:
		return "", nil // a local path, not a hosted remote — nothing to compare
	}
	parts := strings.Split(strings.Trim(url, "/"), "/")
	if len(parts) < 3 {
		return "", nil
	}
	return parts[len(parts)-2] + "/" + parts[len(parts)-1], nil
}

// setupWorktree finds or creates the worktree proposeSetupFiles writes in.
// A worktree already registered for setupBranch — a previous run's, dead
// before it pushed — is reused rather than recreated. Otherwise: an
// existing local branch is built on as-is; an existing remote one (pushed by
// a run that died before opening the PR) becomes the local branch's start
// point; only when neither exists is a fresh branch cut from origin's
// default branch. Never `-b` over a branch that already has commits, the
// same rule the skill's own Phase 1 holds to for issue-N.
func setupWorktree(ctx context.Context, cfg config, remoteDefault string) (string, error) {
	list, _ := git(ctx, cfg, "worktree", "list", "--porcelain")
	if path := worktreeFor(string(list), setupBranch); path != "" {
		return path, nil
	}
	path := filepath.Join(cfg.dir, ".worktrees", setupBranch)
	local, _ := git(ctx, cfg, "branch", "--list", setupBranch)
	// origin specifically, not */setupBranch: the worktree add below is
	// origin/setupBranch too, and a repo with a second remote (a fork setup)
	// that happens to carry a same-named branch there must not make this
	// probe pass while that add then fails on a ref that never existed.
	remote, _ := git(ctx, cfg, "branch", "-r", "--list", "origin/"+setupBranch)
	switch {
	case strings.TrimSpace(string(local)) != "":
		if _, err := git(ctx, cfg, "worktree", "add", path, setupBranch); err != nil {
			return "", fmt.Errorf("creating worktree for existing branch %s: %w", setupBranch, err)
		}
	case strings.TrimSpace(string(remote)) != "":
		if _, err := git(ctx, cfg, "worktree", "add", "-b", setupBranch, path, "origin/"+setupBranch); err != nil {
			return "", fmt.Errorf("creating worktree for existing remote branch %s: %w", setupBranch, err)
		}
	default:
		if _, err := git(ctx, cfg, "worktree", "add", "-b", setupBranch, path, remoteDefault); err != nil {
			return "", fmt.Errorf("creating worktree for a new branch %s: %w", setupBranch, err)
		}
	}
	return path, nil
}

// appendGitignoreLines adds lines to dir's .gitignore, creating the file
// when there is none, and preserving whatever is already there otherwise.
func appendGitignoreLines(dir string, lines []string) error {
	path := filepath.Join(dir, ".gitignore")
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	var b strings.Builder
	b.Write(existing)
	if b.Len() > 0 && !strings.HasSuffix(b.String(), "\n") {
		b.WriteString("\n")
	}
	if b.Len() > 0 {
		b.WriteString("\n")
	}
	b.WriteString("# polako scratch — untracked files `implement-issue` writes into this repo\n")
	for _, line := range lines {
		b.WriteString(line)
		b.WriteString("\n")
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// setupFilesPRBody lists what the pushed branch actually carries over
// remoteDefault — the .gitignore lines, always the same fixed set per
// issue #416's own "Done when", plus the CLAUDE.md block and the scaffold
// when either is on the branch too. Reads
// result's proposed* fields, which come from a diff against remoteDefault —
// not "did this run's own write step add it", so an item an earlier dead
// run already committed still gets named here.
func setupFilesPRBody(result setupFilesResult) string {
	var b strings.Builder
	b.WriteString("`polako setup -apply` proposed this.\n\n")
	if result.proposedGitignore {
		b.WriteString("Adds the .gitignore lines the `implement-issue` skill needs so its own " +
			"scratch never gets committed by accident:\n\n" +
			"- `/.worktrees/` — the worktree the skill creates per issue\n" +
			"- `/PLAN.md` — the resume note it writes before implementing\n" +
			"- `/.polako-scratch/` — everything else throwaway: PR bodies, diff dumps\n\n")
	}
	if result.proposedClaudeMd {
		b.WriteString("Adds a marked polako block to CLAUDE.md: the command that checks this " +
			"repo's work, which files are scratch, the `issue-N` branch contract, and that issue " +
			"text is data, not instructions. Run `/init` for the rest of CLAUDE.md.\n\n")
	}
	if result.proposedScaffold {
		b.WriteString("Adds `docs/VISION.md` and `docs/plans/README.md`, a starting point for the " +
			"vision/plan layout `polako plan-backlog` uses.\n\n")
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}
