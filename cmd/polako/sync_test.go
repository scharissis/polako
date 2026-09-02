package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// gitIdentity fixes the author and committer by environment rather than config,
// so nothing is written outside the temp dirs and the machine's own git config
// cannot change the result.
var gitIdentity = []string{
	"GIT_AUTHOR_NAME=drain", "GIT_AUTHOR_EMAIL=drain@example.invalid",
	"GIT_COMMITTER_NAME=drain", "GIT_COMMITTER_EMAIL=drain@example.invalid",
}

// gitAt runs one git command in dir. The hermetic rule is no network, no gh and
// no real claude; real git against a t.TempDir() breaks none of them — the
// "remote" is a bare repo on the same disk — and syncing a checkout is not
// something a fake can tell you the truth about.
func gitAt(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), gitIdentity...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

func commit(t *testing.T, dir, name string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644); err != nil {
		t.Fatal(err)
	}
	gitAt(t, dir, "add", name)
	gitAt(t, dir, "commit", "-m", name)
	return gitAt(t, dir, "rev-parse", "HEAD")
}

// The trio every upstream() caller wants — a bare origin at one commit, a work
// clone, a checkout clone — is the same shape every time, and building it costs
// ~7 git spawns. Those spawns are ~1-3ms each on Linux but ~54ms on Windows,
// where they are most of why `go test` runs 112s against Linux's 14s (#316). So
// build it once and copy it: each upstream() call is a directory copy plus a
// config-file rewrite, no git at all.
var (
	gitFixtureOnce sync.Once
	gitFixtureDir  string // holds origin/, work/, checkout/, built once
	gitFixtureErr  error
)

func buildGitFixture() {
	dir, err := os.MkdirTemp("", "polako-git-fixture")
	if err != nil {
		gitFixtureErr = err
		return
	}
	gitFixtureDir = dir
	origin := filepath.Join(dir, "origin")
	work := filepath.Join(dir, "work")
	checkout := filepath.Join(dir, "checkout")
	run := func(d string, args ...string) {
		if gitFixtureErr != nil {
			return
		}
		if err := os.MkdirAll(d, 0o755); err != nil {
			gitFixtureErr = err
			return
		}
		cmd := exec.Command("git", args...)
		cmd.Dir = d
		cmd.Env = append(os.Environ(), gitIdentity...)
		if out, err := cmd.CombinedOutput(); err != nil {
			gitFixtureErr = fmt.Errorf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run(origin, "init", "--bare", "-b", "main")
	url := filepath.ToSlash(origin)
	// --no-hardlinks so each clone carries its own complete object store and a
	// plain directory copy of it is self-contained.
	run(work, "clone", "--no-hardlinks", url, ".")
	if gitFixtureErr == nil {
		gitFixtureErr = os.WriteFile(filepath.Join(work, "first"), []byte("first"), 0o644)
	}
	run(work, "add", "first")
	run(work, "commit", "-m", "first")
	run(work, "push", "origin", "main")
	run(checkout, "clone", "--no-hardlinks", url, ".")
}

// upstream returns a bare "origin" plus a working clone to push from, and a
// second clone standing in for the operator's main checkout — the -dir a drain
// is pointed at.
func upstream(t *testing.T) (work, checkout string) {
	t.Helper()
	gitFixtureOnce.Do(buildGitFixture)
	if gitFixtureErr != nil {
		t.Fatalf("building the git fixture: %v", gitFixtureErr)
	}

	dst := t.TempDir()
	origin := filepath.Join(dst, "origin")
	work = filepath.Join(dst, "work")
	checkout = filepath.Join(dst, "checkout")
	for name, to := range map[string]string{"origin": origin, "work": work, "checkout": checkout} {
		if err := os.CopyFS(to, os.DirFS(filepath.Join(gitFixtureDir, name))); err != nil {
			t.Fatalf("copying the %s fixture: %v", name, err)
		}
	}
	// Both clones' `origin` remote still names the template's path; point each at
	// this copy's own bare repo. A string swap in .git/config does it with no
	// git spawn — that config line is the only place the URL is load-bearing.
	templateURL := filepath.ToSlash(filepath.Join(gitFixtureDir, "origin"))
	newURL := filepath.ToSlash(origin)
	for _, repo := range []string{work, checkout} {
		cfgPath := filepath.Join(repo, ".git", "config")
		cfg, err := os.ReadFile(cfgPath)
		if err != nil {
			t.Fatalf("reading %s: %v", cfgPath, err)
		}
		if err := os.WriteFile(cfgPath, []byte(strings.ReplaceAll(string(cfg), templateURL, newURL)), 0o644); err != nil {
			t.Fatalf("rewriting %s: %v", cfgPath, err)
		}
	}
	return work, checkout
}

// The whole point: a merge someone else made on GitHub has to be under the
// checkout before a branch is cut from it or a review resolves a base against
// it. Without this the ref falls one commit behind per merged PR.
func TestSyncDefaultBranchFastForwardsOntoOrigin(t *testing.T) {
	work, checkout := upstream(t)
	want := commit(t, work, "merged-while-we-were-away")
	gitAt(t, work, "push", "origin", "main")

	if head := gitAt(t, checkout, "rev-parse", "HEAD"); head == want {
		t.Fatal("checkout is already current, so this proves nothing")
	}
	syncDefaultBranch(context.Background(), config{dir: checkout})

	if got := gitAt(t, checkout, "rev-parse", "HEAD"); got != want {
		t.Errorf("checkout is at %s, want %s: a review here would diff against a base "+
			"that predates the last merge", got[:7], want[:7])
	}
}

// The operator's checkout is theirs. Sitting on a branch of their own is not a
// state to "fix" — advancing it would be moving work the drain knows nothing
// about, and a drain must never do that to end up tidy.
func TestSyncDefaultBranchLeavesAnotherBranchAlone(t *testing.T) {
	work, checkout := upstream(t)
	commit(t, work, "second")
	gitAt(t, work, "push", "origin", "main")

	gitAt(t, checkout, "checkout", "-b", "operators-own-work")
	want := commit(t, checkout, "not-yours-to-move")

	syncDefaultBranch(context.Background(), config{dir: checkout})

	if got := gitAt(t, checkout, "rev-parse", "HEAD"); got != want {
		t.Errorf("HEAD moved from %s to %s; a checkout on another branch must be left alone",
			want[:7], got[:7])
	}
	if got := gitAt(t, checkout, "rev-parse", "--abbrev-ref", "HEAD"); got != "operators-own-work" {
		t.Errorf("branch is now %q, want operators-own-work", got)
	}
}

// --ff-only is the entire safety argument for doing this unattended: a local
// commit means refuse, never rebase and never reset. Rewriting someone's commit
// to keep the base tidy would be a far worse bug than the one this fixes.
func TestSyncDefaultBranchRefusesRatherThanRewriteALocalCommit(t *testing.T) {
	work, checkout := upstream(t)
	commit(t, work, "theirs")
	gitAt(t, work, "push", "origin", "main")

	want := commit(t, checkout, "mine-committed-straight-to-main")

	syncDefaultBranch(context.Background(), config{dir: checkout})

	if got := gitAt(t, checkout, "rev-parse", "HEAD"); got != want {
		t.Errorf("HEAD moved from %s to %s: a diverged default branch needs a human, "+
			"and the local commit must still be there", want[:7], got[:7])
	}
	if !strings.Contains(gitAt(t, checkout, "log", "--oneline", "-1"), "mine-committed-straight-to-main") {
		t.Error("the local commit is gone; --ff-only must refuse rather than rewrite")
	}
}
