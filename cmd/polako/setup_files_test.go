package main

// Ticket 4's own tests (docs/plans/setup.md): the .gitignore row is a pure
// function over a directory, so it's tested directly; the write pass runs
// real git against upstream()'s bare origin (the same fixture sync_test.go
// uses) plus the fake gh — hermetic, no network, but a genuine push and pull
// prove the worktree/branch/PR plumbing rather than a mock of it.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupGitignoreRowNamesMissingLines(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	row := setupGitignoreRow(config{dir: dir})
	if row.status != setupMissing || row.required {
		t.Errorf("row = %+v, want missing and not required with no .gitignore at all", row)
	}
	for _, want := range setupGitignoreLines {
		if !strings.Contains(row.detail, want) {
			t.Errorf("detail = %q, want it to name %q", row.detail, want)
		}
	}

	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("/.worktrees/\n/PLAN.md\n"), 0o644); err != nil {
		t.Fatalf("writing .gitignore: %v", err)
	}
	row = setupGitignoreRow(config{dir: dir})
	if row.status != setupMissing || !strings.Contains(row.detail, "/.polako-scratch/") {
		t.Errorf("row = %+v, want missing and naming only /.polako-scratch/", row)
	}

	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("/.worktrees/\n/PLAN.md\n/.polako-scratch/\n"), 0o644); err != nil {
		t.Fatalf("writing .gitignore: %v", err)
	}
	if row := setupGitignoreRow(config{dir: dir}); row.status != setupOK {
		t.Errorf("row = %+v, want ok with every line present", row)
	}
}

// The point of the whole ticket: -apply -yes on a repo missing the
// .gitignore lines leaves one branch, one commit, one PR — and the push
// really landed on origin, not just the local worktree.
func TestApplySetupFilesProposesAPR(t *testing.T) {
	t.Parallel()
	work, checkout := upstream(t)
	cfg := setupCfg(t, &ghState{}, checkout)
	cfg.env = append(cfg.env, gitIdentity...)

	cfg, rows, _ := readSetup(context.Background(), cfg, false)
	if r := findSetupRow(t, rows, ".gitignore"); r.status != setupMissing {
		t.Fatalf("test setup: .gitignore row = %+v, want missing", r)
	}

	var out strings.Builder
	rows = applySetupFiles(context.Background(), newSetupPrompt(strings.NewReader(""), &out, true), cfg, rows)

	r := findSetupRow(t, rows, ".gitignore")
	if r.status != setupMissing || !strings.Contains(r.detail, "proposed: https://example.invalid/pr/") {
		t.Errorf(".gitignore row = %+v, want it to name the proposed PR", r)
	}
	if !strings.Contains(out.String(), "proposed the setup PR") {
		t.Errorf("output = %q, want it to say the fix was proposed", out.String())
	}

	gitAt(t, work, "fetch", "origin", "polako-setup")
	if got := gitAt(t, work, "log", "--oneline", "-1", "FETCH_HEAD"); !strings.Contains(got, setupFilesCommitSubject) {
		t.Errorf("origin's polako-setup tip = %q, want the %q commit", got, setupFilesCommitSubject)
	}
	content := gitAt(t, work, "show", "FETCH_HEAD:.gitignore")
	for _, want := range setupGitignoreLines {
		if !strings.Contains(content, want) {
			t.Errorf("pushed .gitignore = %q, missing %q", content, want)
		}
	}
	// checkout's own default branch must be untouched — the write surface
	// is a branch and a PR, never a commit where the operator is checked
	// out. .worktrees/ itself shows up untracked, which is expected: that's
	// exactly the line this PR proposes, not yet merged into checkout's own
	// .gitignore.
	if got := gitAt(t, checkout, "status", "--porcelain", "--untracked-files=no"); got != "" {
		t.Errorf("checkout has tracked changes: %q", got)
	}
	if got := gitAt(t, checkout, "rev-parse", "--abbrev-ref", "HEAD"); got != "main" {
		t.Errorf("checkout moved off main to %q", got)
	}
}

// -yes takes each step's own default: yes for .gitignore and the CLAUDE.md
// block, no for the scaffold — so a plain -yes run proposes the first two
// and leaves docs/VISION.md and docs/plans/README.md alone.
func TestApplySetupFilesProposesTheClaudeMdBlockButNotTheScaffoldUnderYes(t *testing.T) {
	t.Parallel()
	work, checkout := upstream(t)
	cfg := setupCfg(t, &ghState{}, checkout)
	cfg.env = append(cfg.env, gitIdentity...)

	cfg, rows, _ := readSetup(context.Background(), cfg, false)
	if r := findSetupRow(t, rows, "CLAUDE.md"); r.status != setupMissing {
		t.Fatalf("test setup: CLAUDE.md row = %+v, want missing", r)
	}
	if r := findSetupRow(t, rows, visionMdPath); r.status != setupMissing {
		t.Fatalf("test setup: %s row = %+v, want missing", visionMdPath, r)
	}

	var out strings.Builder
	rows = applySetupFiles(context.Background(), newSetupPrompt(strings.NewReader(""), &out, true), cfg, rows)

	r := findSetupRow(t, rows, "CLAUDE.md")
	if r.status != setupMissing || !strings.Contains(r.detail, "proposed: https://example.invalid/pr/") {
		t.Errorf("CLAUDE.md row = %+v, want it to name the proposed PR", r)
	}
	if r := findSetupRow(t, rows, visionMdPath); r.status != setupMissing || strings.Contains(r.detail, "proposed") {
		t.Errorf("%s row = %+v, want it untouched — -yes must not accept the scaffold's own default-no prompt",
			visionMdPath, r)
	}

	gitAt(t, work, "fetch", "origin", "polako-setup")
	block := gitAt(t, work, "show", "FETCH_HEAD:CLAUDE.md")
	if !strings.Contains(block, claudeMdBeginMarker) || !strings.Contains(block, claudeMdCheckCommandUnknown) {
		t.Errorf("pushed CLAUDE.md = %q, want the marked block naming the fallback check command", block)
	}
	if tree := gitAt(t, work, "ls-tree", "-r", "--name-only", "FETCH_HEAD"); strings.Contains(tree, "docs/VISION.md") {
		t.Errorf("pushed tree = %q, the scaffold must not be included under plain -yes", tree)
	}
}

// The scaffold's own prompt has to be answered explicitly — it's the one
// question in this pass whose default is no.
func TestApplySetupFilesScaffoldWhenAcceptedExplicitly(t *testing.T) {
	t.Parallel()
	work, checkout := upstream(t)
	cfg := setupCfg(t, &ghState{}, checkout)
	cfg.env = append(cfg.env, gitIdentity...)

	cfg, rows, _ := readSetup(context.Background(), cfg, false)
	var out strings.Builder
	// In order: decline .gitignore, decline CLAUDE.md, accept the scaffold.
	rows = applySetupFiles(context.Background(), newSetupPrompt(strings.NewReader("n\nn\ny\n"), &out, false), cfg, rows)

	if r := findSetupRow(t, rows, ".gitignore"); r.status != setupMissing || strings.Contains(r.detail, "proposed") {
		t.Errorf(".gitignore row = %+v, want it untouched (declined)", r)
	}
	if r := findSetupRow(t, rows, visionMdPath); !strings.Contains(r.detail, "proposed: https://example.invalid/pr/") {
		t.Errorf("%s row = %+v, want it to name the proposed PR", visionMdPath, r)
	}

	gitAt(t, work, "fetch", "origin", "polako-setup")
	tree := gitAt(t, work, "ls-tree", "-r", "--name-only", "FETCH_HEAD")
	for _, want := range []string{"docs/VISION.md", "docs/plans/README.md"} {
		if !strings.Contains(tree, want) {
			t.Errorf("pushed tree = %q, missing %q", tree, want)
		}
	}
	if strings.Contains(tree, "CLAUDE.md") {
		t.Errorf("pushed tree = %q, CLAUDE.md must not be included — it was declined", tree)
	}
}

// Restart safety: an open PR from polako-setup means report its URL and
// write nothing — never a second PR, never a second branch.
func TestApplySetupFilesReportsAnAlreadyOpenPR(t *testing.T) {
	t.Parallel()
	_, checkout := upstream(t)
	cfg := setupCfg(t, &ghState{PRs: map[string]*fakePR{
		setupBranch: {Number: 7, State: "OPEN"},
	}}, checkout)
	cfg.env = append(cfg.env, gitIdentity...)

	cfg, rows, _ := readSetup(context.Background(), cfg, false)
	var out strings.Builder
	rows = applySetupFiles(context.Background(), newSetupPrompt(strings.NewReader(""), &out, true), cfg, rows)

	if !strings.Contains(out.String(), "already proposed") {
		t.Errorf("output = %q, want it to say the fix was already proposed", out.String())
	}
	r := findSetupRow(t, rows, ".gitignore")
	if !strings.Contains(r.detail, "already proposed") {
		t.Errorf(".gitignore row = %+v, want it to say so", r)
	}
	if got := gitAt(t, checkout, "worktree", "list", "--porcelain"); strings.Contains(got, "branch refs/heads/"+setupBranch) {
		t.Errorf("worktree list = %q, an open PR must not make setup create a worktree", got)
	}
}

// A PR closed without merging is a human's decision to decline the
// proposal — rerunning -apply must not silently reopen the same fix.
func TestApplySetupFilesDoesNotReproposeAfterAHumanClosesThePR(t *testing.T) {
	t.Parallel()
	_, checkout := upstream(t)
	cfg := setupCfg(t, &ghState{PRs: map[string]*fakePR{
		setupBranch: {Number: 9, State: "CLOSED"},
	}}, checkout)
	cfg.env = append(cfg.env, gitIdentity...)

	cfg, rows, _ := readSetup(context.Background(), cfg, false)
	var out strings.Builder
	rows = applySetupFiles(context.Background(), newSetupPrompt(strings.NewReader(""), &out, true), cfg, rows)

	if !strings.Contains(out.String(), "closed without merging") {
		t.Errorf("output = %q, want it to say the PR was closed without merging", out.String())
	}
	r := findSetupRow(t, rows, ".gitignore")
	if !strings.Contains(r.detail, "closed without merging") {
		t.Errorf(".gitignore row = %+v, want it to say so", r)
	}
	if got := gitAt(t, checkout, "worktree", "list", "--porcelain"); strings.Contains(got, "branch refs/heads/"+setupBranch) {
		t.Errorf("worktree list = %q, a closed PR must not make setup create a new one", got)
	}
}

// A merged PR falls through to the normal flow rather than being treated as
// "already handled": the worktree, cut from a freshly fetched default
// branch, already carries the merged lines, so proposeSetupFiles finds
// nothing left to add and the row reports ok instead of staying "missing".
func TestApplySetupFilesFallsThroughOnAMergedPR(t *testing.T) {
	t.Parallel()
	work, checkout := upstream(t)
	if err := os.WriteFile(filepath.Join(work, ".gitignore"), []byte("/.worktrees/\n/PLAN.md\n/.polako-scratch/\n"), 0o644); err != nil {
		t.Fatalf("writing .gitignore in work: %v", err)
	}
	gitAt(t, work, "add", ".gitignore")
	gitAt(t, work, "commit", "-m", setupFilesCommitSubject)
	gitAt(t, work, "push", "origin", "main")

	cfg := setupCfg(t, &ghState{PRs: map[string]*fakePR{
		setupBranch: {Number: 11, State: "MERGED"},
	}}, checkout)
	cfg.env = append(cfg.env, gitIdentity...)

	cfg, rows, _ := readSetup(context.Background(), cfg, false)
	var out strings.Builder
	rows = applySetupFiles(context.Background(), newSetupPrompt(strings.NewReader(""), &out, true), cfg, rows)

	r := findSetupRow(t, rows, ".gitignore")
	if r.status != setupOK {
		t.Errorf(".gitignore row = %+v, want ok — a merged PR must fall through to the normal detection", r)
	}
}

// Declining the prompt writes nothing — the row stays exactly as read.
func TestApplySetupFilesDeclineWritesNothing(t *testing.T) {
	t.Parallel()
	_, checkout := upstream(t)
	cfg := setupCfg(t, &ghState{}, checkout)
	cfg.env = append(cfg.env, gitIdentity...)

	cfg, rows, _ := readSetup(context.Background(), cfg, false)
	before := findSetupRow(t, rows, ".gitignore")
	var out strings.Builder
	rows = applySetupFiles(context.Background(), newSetupPrompt(strings.NewReader("n\n"), &out, false), cfg, rows)

	after := findSetupRow(t, rows, ".gitignore")
	if after != before {
		t.Errorf(".gitignore row changed after declining: before %+v, after %+v", before, after)
	}
	if got := gitAt(t, checkout, "worktree", "list", "--porcelain"); strings.Contains(got, "branch refs/heads/"+setupBranch) {
		t.Errorf("worktree list = %q, declining must not create a worktree", got)
	}
}

// A rerun after the PR already merged — the operator's checkout just hasn't
// fast-forwarded yet — finds nothing left to add once the worktree is cut
// from a freshly fetched origin, and reports the row ok rather than opening
// a PR with no diff.
func TestApplySetupFilesNothingLeftToAddAfterAMerge(t *testing.T) {
	t.Parallel()
	work, checkout := upstream(t)
	// Simulate the earlier polako-setup PR having already merged upstream —
	// all three files this time, so there's truly nothing left for any of
	// them; production code writes the CLAUDE.md block and the scaffold so
	// this fixture can't drift from what a real run would have produced.
	if err := os.WriteFile(filepath.Join(work, ".gitignore"), []byte("/.worktrees/\n/PLAN.md\n/.polako-scratch/\n"), 0o644); err != nil {
		t.Fatalf("writing .gitignore in work: %v", err)
	}
	if err := writeClaudeMdBlock(work); err != nil {
		t.Fatalf("writing CLAUDE.md in work: %v", err)
	}
	if err := writeScaffold(work); err != nil {
		t.Fatalf("writing the scaffold in work: %v", err)
	}
	gitAt(t, work, "add", ".gitignore", "CLAUDE.md", visionMdPath, plansReadmePath)
	gitAt(t, work, "commit", "-m", setupFilesCommitSubject)
	gitAt(t, work, "push", "origin", "main")

	cfg := setupCfg(t, &ghState{}, checkout) // checkout's own main is still behind
	cfg.env = append(cfg.env, gitIdentity...)

	cfg, rows, _ := readSetup(context.Background(), cfg, false)
	if r := findSetupRow(t, rows, ".gitignore"); r.status != setupMissing {
		t.Fatalf("test setup: .gitignore row = %+v, want missing (checkout hasn't fast-forwarded)", r)
	}

	var out strings.Builder
	rows = applySetupFiles(context.Background(), newSetupPrompt(strings.NewReader(""), &out, true), cfg, rows)

	r := findSetupRow(t, rows, ".gitignore")
	if r.status != setupOK {
		t.Errorf(".gitignore row = %+v, want ok — the lines were already on origin's default branch", r)
	}
	if !strings.Contains(out.String(), "nothing left to propose") {
		t.Errorf("output = %q, want it to say there was nothing left to add", out.String())
	}
}

func TestRepoFromOriginURLParsesEveryCloneShape(t *testing.T) {
	t.Parallel()
	cases := []struct {
		url  string
		want string
	}{
		{"https://github.com/scharissis/polako.git", "scharissis/polako"},
		{"https://github.com/scharissis/polako", "scharissis/polako"},
		{"git@github.com:scharissis/polako.git", "scharissis/polako"},
		{"ssh://git@github.com/scharissis/polako.git", "scharissis/polako"},
	}
	for _, c := range cases {
		_, checkout := upstream(t)
		gitAt(t, checkout, "remote", "set-url", "origin", c.url)
		cfg := config{dir: checkout}
		got, err := repoFromOriginURL(context.Background(), cfg)
		if err != nil || got != c.want {
			t.Errorf("repoFromOriginURL(%q) = %q, %v, want %q, nil", c.url, got, err, c.want)
		}
	}
}

// -repo naming a different repository than -dir is a checkout of is a real,
// documented combination for the read-only report ("instead of whichever
// -dir is a checkout of") — but the write path operates on -dir's own local
// origin, so it has to refuse rather than silently push a branch to the
// wrong repository.
func TestProposeSetupFilesRefusesWhenDirAndRepoDisagree(t *testing.T) {
	t.Parallel()
	_, checkout := upstream(t)
	gitAt(t, checkout, "remote", "set-url", "origin", "https://github.com/someone/unrelated.git")
	cfg := config{dir: checkout, repo: "example/repo", ghRepo: "example/repo", env: gitIdentity}

	_, err := proposeSetupFiles(context.Background(), cfg, setupFileWants{gitignore: true})
	if err == nil || !strings.Contains(err.Error(), "someone/unrelated") || !strings.Contains(err.Error(), "example/repo") {
		t.Errorf("proposeSetupFiles err = %v, want it to name both someone/unrelated and example/repo", err)
	}
	if got := gitAt(t, checkout, "worktree", "list", "--porcelain"); strings.Contains(got, "branch refs/heads/"+setupBranch) {
		t.Errorf("worktree list = %q, a repo mismatch must not create a worktree", got)
	}
}

// A second remote (a fork setup) that happens to already carry a branch
// named polako-setup must not make setupWorktree try to build from
// origin/polako-setup, which never existed — it should cut a fresh branch
// from origin's default branch instead, the same as if no polako-setup
// branch existed anywhere.
func TestSetupWorktreeIgnoresANonOriginRemoteBranch(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	gitAt(t, base, "init", "--bare", "fork")
	_, checkout := upstream(t)
	gitAt(t, checkout, "remote", "add", "fork", filepath.Join(base, "fork"))
	gitAt(t, checkout, "push", "fork", "HEAD:refs/heads/"+setupBranch)
	gitAt(t, checkout, "fetch", "fork")

	cfg := config{dir: checkout, env: gitIdentity}
	path, err := setupWorktree(context.Background(), cfg, "origin/main")
	if err != nil {
		t.Fatalf("setupWorktree with a same-named branch on a non-origin remote: %v", err)
	}
	if got := gitAt(t, path, "log", "--oneline", "-1"); !strings.Contains(got, "first") {
		t.Errorf("worktree HEAD = %q, want it cut from origin/main, not the fork remote's branch", got)
	}
}
