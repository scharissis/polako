package main

// Ticket 4's own half of `polako setup` (docs/plans/setup.md): the one tree
// row that reads the checkout directly rather than gh, and -apply's first
// write beyond a label — one commit, on a `polako-setup` branch, built in a
// worktree, pushed, behind one PR a human merges. Split out of setup.go and
// setup_apply.go so both stay closer to the repo's own median length as this
// grows; see setup.go for the read-only report and setup_apply.go for the
// label half of -apply these functions sit beside.
//
// Out of scope here: the CLAUDE.md block and the docs/VISION.md scaffold
// (docs/plans/setup.md, the rest of ticket 4) — a later ticket, not this one.

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
// docs/plans/setup.md's own wording for ticket 4.
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

// applySetupFiles is -apply's file-proposal pass, run once after the label
// pass (applySetup) using the same prompt — see setupPrompt's own doc
// comment for why one Scanner has to serve every question this run asks.
// Best-effort like applySetup's own creates: a failure here is reported and
// setup moves on, never ends the run.
func applySetupFiles(ctx context.Context, prompt *setupPrompt, cfg config, rows []setupRow) []setupRow {
	idx := slices.IndexFunc(rows, func(r setupRow) bool { return r.name == ".gitignore" })
	if idx < 0 || rows[idx].status != setupMissing {
		return rows
	}
	if !prompt.confirm(fmt.Sprintf("propose the missing .gitignore lines through a PR on %q?", setupBranch)) {
		return rows
	}
	// Restart safety, the same rule the drain holds to for issue-N: an
	// existing PR from this branch means report it and stop, never a second
	// one. That covers OPEN (still waiting on a human) and CLOSED (a human
	// declined it — reopening the exact same proposal would override that
	// decision) alike. MERGED is the one state that falls through: the
	// worktree below is cut from a freshly fetched default branch, so if the
	// merge already landed the lines, proposeSetupFiles finds nothing left
	// to add and reports the row ok instead of stopping on a stale "missing".
	pr, err := prForBranch(ctx, cfg, setupBranch)
	if err != nil {
		fmt.Fprintf(prompt.out, "  could not check for an existing %s PR (%v) — skipping\n", setupBranch, err)
		return rows
	}
	if pr != nil && pr.State != "MERGED" {
		verb := "already proposed"
		if pr.State == "CLOSED" {
			verb = "already proposed, then closed without merging"
		}
		fmt.Fprintf(prompt.out, "  .gitignore fix %s: %s\n", verb, pr.URL)
		rows[idx].detail = verb + ": " + pr.URL
		return rows
	}
	url, err := proposeSetupFiles(ctx, cfg)
	switch {
	case err != nil:
		fmt.Fprintf(prompt.out, "  could not propose the .gitignore fix (%v)\n", err)
	case url == "":
		// Nothing left to add — a previous polako-setup PR already merged
		// and the worktree, cut fresh from origin's default branch, proves
		// it. The row read from cfg.dir was simply stale.
		rows[idx] = setupRow{name: ".gitignore", status: setupOK}
		fmt.Fprintln(prompt.out, "  .gitignore already has every line — nothing to propose")
	default:
		fmt.Fprintf(prompt.out, "  proposed the .gitignore fix: %s\n", url)
		rows[idx].detail = "proposed: " + url
	}
	return rows
}

// proposeSetupFiles does the write pass ticket 4 draws: fetch, resolve
// origin's default branch, and `worktree add` all run in the main checkout
// (cfg); everything past that — writing the file, `add`, `commit`, `push` —
// runs in the worktree, through a copy of cfg pointed at its path. Returns
// the PR's URL, or "" when there was nothing left to add (see the caller).
func proposeSetupFiles(ctx context.Context, cfg config) (string, error) {
	// -repo lets the read-only report check a repository -dir isn't a
	// checkout of ("instead of whichever -dir is a checkout of" — setup.go's
	// own -repo flag doc). Every git write below operates on cfg.dir's local
	// origin, not cfg.repo, so that combination has to be refused here or it
	// silently pushes a branch to a repository the operator never named.
	if local, err := repoFromOriginURL(ctx, cfg); err == nil && local != "" && !strings.EqualFold(local, cfg.repo) {
		return "", fmt.Errorf("-dir is a checkout of %s, not %s (-repo) — the file-proposal write needs "+
			"-dir to be a checkout of the repository being set up", local, cfg.repo)
	}
	if _, err := git(ctx, cfg, "fetch", "origin", "--quiet"); err != nil {
		return "", fmt.Errorf("fetching origin: %w", err)
	}
	head, err := git(ctx, cfg, "symbolic-ref", "refs/remotes/origin/HEAD", "--short")
	if err != nil {
		return "", fmt.Errorf("resolving origin's default branch: %w", err)
	}
	remoteDefault := strings.TrimSpace(string(head)) // "origin/main"
	defaultBranch := strings.TrimPrefix(remoteDefault, "origin/")

	path, err := setupWorktree(ctx, cfg, remoteDefault)
	if err != nil {
		return "", err
	}
	wtCfg := cfg
	wtCfg.dir = path

	missing := missingGitignoreLines(readGitignoreLines(path))
	if len(missing) > 0 {
		if err := appendGitignoreLines(path, missing); err != nil {
			return "", fmt.Errorf("writing .gitignore: %w", err)
		}
		if _, err := git(ctx, wtCfg, "add", ".gitignore"); err != nil {
			return "", fmt.Errorf("staging .gitignore: %w", err)
		}
		if _, err := git(ctx, wtCfg, "commit", "-m", setupFilesCommitSubject); err != nil {
			return "", fmt.Errorf("committing: %w", err)
		}
	}

	ahead, err := git(ctx, wtCfg, "rev-list", "--count", remoteDefault+"..HEAD")
	if err != nil {
		return "", fmt.Errorf("checking %s against %s: %w", setupBranch, remoteDefault, err)
	}
	if strings.TrimSpace(string(ahead)) == "0" {
		return "", nil // nothing to propose — see the caller
	}

	// Never --force: a rerun that finds the branch already pushed (a dead
	// run's own push, or a human's edit) builds on it rather than
	// overwriting it.
	if _, err := git(ctx, wtCfg, "push", "-u", "origin", setupBranch); err != nil {
		return "", fmt.Errorf("pushing %s: %w", setupBranch, err)
	}

	out, err := gh(ctx, cfg, "pr", "create", "--head", setupBranch, "--base", defaultBranch,
		"--title", setupFilesCommitSubject, "--body", setupFilesPRBody())
	if err != nil {
		return "", fmt.Errorf("opening the PR: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
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

// setupFilesPRBody lists what was added and why, per docs/plans/setup.md's
// own "Done when" for this ticket — always the fixed set of lines, not just
// the ones this particular run happened to add, since the PR is about the
// same three lines whichever run's push landed them.
func setupFilesPRBody() string {
	return "`polako setup -apply` proposed this.\n\n" +
		"Adds the .gitignore lines the `implement-issue` skill needs so its own " +
		"scratch never gets committed by accident:\n\n" +
		"- `/.worktrees/` — the worktree the skill creates per issue\n" +
		"- `/PLAN.md` — the resume note it writes before implementing\n" +
		"- `/.polako-scratch/` — everything else throwaway: PR bodies, diff dumps\n"
}
