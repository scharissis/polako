package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
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
	t.Parallel()
	work, checkout := upstream(t)
	want := commit(t, work, "merged-while-we-were-away")
	gitAt(t, work, "push", "origin", "main")

	if head := gitAt(t, checkout, "rev-parse", "HEAD"); head == want {
		t.Fatal("checkout is already current, so this proves nothing")
	}
	if err := syncDefaultBranch(context.Background(), config{dir: checkout, ui: testUI(t)}, nil); err != nil {
		t.Fatalf("a reachable origin must never stop a pickup: %v", err)
	}

	if got := gitAt(t, checkout, "rev-parse", "HEAD"); got != want {
		t.Errorf("checkout is at %s, want %s: a review here would diff against a base "+
			"that predates the last merge", got[:7], want[:7])
	}
}

// The operator's checkout is theirs. Sitting on a branch of their own is not a
// state to "fix" — advancing it would be moving work the drain knows nothing
// about, and a drain must never do that to end up tidy.
func TestSyncDefaultBranchLeavesAnotherBranchAlone(t *testing.T) {
	t.Parallel()
	work, checkout := upstream(t)
	commit(t, work, "second")
	gitAt(t, work, "push", "origin", "main")

	gitAt(t, checkout, "checkout", "-b", "operators-own-work")
	want := commit(t, checkout, "not-yours-to-move")

	if err := syncDefaultBranch(context.Background(), config{dir: checkout, ui: testUI(t)}, nil); err != nil {
		t.Fatalf("a reachable origin must never stop a pickup: %v", err)
	}

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
	t.Parallel()
	work, checkout := upstream(t)
	commit(t, work, "theirs")
	gitAt(t, work, "push", "origin", "main")

	want := commit(t, checkout, "mine-committed-straight-to-main")

	if err := syncDefaultBranch(context.Background(), config{dir: checkout, ui: testUI(t)}, nil); err != nil {
		t.Fatalf("a reachable origin must never stop a pickup: %v", err)
	}

	if got := gitAt(t, checkout, "rev-parse", "HEAD"); got != want {
		t.Errorf("HEAD moved from %s to %s: a diverged default branch needs a human, "+
			"and the local commit must still be there", want[:7], got[:7])
	}
	if !strings.Contains(gitAt(t, checkout, "log", "--oneline", "-1"), "mine-committed-straight-to-main") {
		t.Error("the local commit is gone; --ff-only must refuse rather than rewrite")
	}
}

// unreachableOrigin points checkout's origin at a path with nothing behind it:
// the hermetic stand-in for a locked ssh-agent or a network that is down.
func unreachableOrigin(t *testing.T, checkout string) {
	t.Helper()
	gitAt(t, checkout, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "gone"))
}

// The one sync failure a caller hears about. A run started now would work from
// a base nobody can date and then fail its push against the same remote, so the
// error has to reach processIssue — and say what to go and fix.
func TestSyncDefaultBranchReportsAnUnreachableOrigin(t *testing.T) {
	t.Parallel()
	buf := captureLog(t)
	_, checkout := upstream(t)
	unreachableOrigin(t, checkout)

	err := syncDefaultBranch(context.Background(), config{dir: checkout, ui: testUI(t), ghRetryWait: 1}, nil)
	if err == nil {
		t.Fatal("err = nil, want the failed fetch reported so the pickup can stop on it")
	}
	for _, want := range []string{"could not fetch origin", "ssh-agent", "fetch origin` work", "start the drain again"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error is missing %q, so it does not say what to do:\n%v", want, err)
		}
	}
	// Bounded patience first: a network still waking up is not a dead remote.
	if got := strings.Count(buf.String(), "transient: git fetch origin failed"); got != ghReads-1 {
		t.Errorf("fetch was retried %d times, want %d\n%s", got, ghReads-1, buf)
	}
}

var (
	fakeSSHDenyOnce sync.Once
	fakeSSHDenyDir  string
	fakeSSHDenyBin  string
	fakeSSHDenyErr  error
)

// fakeGitSSHDeny builds the binary denyGitAuth points GIT_SSH_COMMAND at: a
// standalone program, not another fakeCLI mode, because GIT_SSH_COMMAND is
// inherited by every child a drain test's git, gh and claude calls spawn —
// gating this by an env var the way fakeGh/fakeClaude gate on argv would
// make it fire for those too, since ssh's own argv gives nothing recognisable
// to gate on. A dedicated binary that always answers the same way, regardless
// of env or argv, sidesteps that: it is never asked to be anything else.
func fakeGitSSHDeny(t *testing.T) string {
	t.Helper()
	fakeSSHDenyOnce.Do(buildFakeGitSSHDeny)
	if fakeSSHDenyErr != nil {
		t.Fatal(fakeSSHDenyErr)
	}
	return fakeSSHDenyBin
}

func buildFakeGitSSHDeny() {
	dir, err := os.MkdirTemp("", "polako-fake-ssh-deny")
	if err != nil {
		fakeSSHDenyErr = fmt.Errorf("fake ssh deny: %v", err)
		return
	}
	fakeSSHDenyDir = dir
	src := filepath.Join(dir, "main.go")
	// With no counter it always refuses. With one, it refuses the first
	// POLAKO_FAKE_SSH_FAILS calls and then serves POLAKO_FAKE_SSH_REPO the way
	// sshd would, running the git service git asked for against it — an agent
	// that comes back on its own (issue #595).
	const source = `package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

func main() {
	if counter := os.Getenv("POLAKO_FAKE_SSH_COUNTER"); counter != "" {
		b, _ := os.ReadFile(counter)
		n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
		n++
		if err := os.WriteFile(counter, []byte(strconv.Itoa(n)), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "fake ssh:", err)
			os.Exit(1)
		}
		fails, _ := strconv.Atoi(os.Getenv("POLAKO_FAKE_SSH_FAILS"))
		if n > fails {
			service := strings.Fields(os.Args[len(os.Args)-1])[0]
			cmd := exec.Command("git", strings.TrimPrefix(service, "git-"), os.Getenv("POLAKO_FAKE_SSH_REPO"))
			cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
			if err := cmd.Run(); err != nil {
				os.Exit(1)
			}
			return
		}
	}
	fmt.Fprintln(os.Stderr, "git@github.com: Permission denied (publickey).")
	os.Exit(1)
}
`
	if err := os.WriteFile(src, []byte(source), 0o644); err != nil {
		fakeSSHDenyErr = fmt.Errorf("fake ssh deny: %v", err)
		return
	}
	bin := filepath.Join(dir, "fake-ssh-deny")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if out, err := exec.Command("go", "build", "-o", bin, src).CombinedOutput(); err != nil {
		fakeSSHDenyErr = fmt.Errorf("fake ssh deny: building it needs a working `go` on PATH: %v\n%s", err, out)
		return
	}
	fakeSSHDenyBin = bin
}

// denyGitAuth points checkout's origin at an ssh URL and swaps in a fake ssh
// command (GIT_SSH_COMMAND) that always refuses with a publickey rejection —
// a real `git fetch` failing this specific way, hermetically: no network, no
// real sshd, no real key. example.invalid is never looked up at all: the fake
// answers before git gets far enough to resolve it. The variable rides on
// cfg.env, not t.Setenv: git is a child this config spawns, which is the case
// that seam exists for, and it leaves the caller free to run t.Parallel().
func denyGitAuth(t *testing.T, cfg *config, checkout string) {
	t.Helper()
	gitAt(t, checkout, "remote", "set-url", "origin", "ssh://git@example.invalid/repo.git")
	// Git for Windows runs GIT_SSH_COMMAND through its bundled sh, which
	// treats backslashes as escapes — a native filepath.Join path there
	// gets mangled ("C:\Users\..." becomes "C:Users...", "command not
	// found"). Forward slashes work in both that sh and a Windows exec.
	setFakeEnv(cfg, "GIT_SSH_COMMAND", filepath.ToSlash(fakeGitSSHDeny(t)))
}

// flakyGitAuth is denyGitAuth for an agent that comes back: the first fails
// ssh calls are refused, and every one after that fetches from checkout's own
// bare origin. One call per fetch attempt, so a failed sync costs ghReads.
func flakyGitAuth(t *testing.T, cfg *config, checkout string, fails int) {
	t.Helper()
	origin := gitAt(t, checkout, "config", "--get", "remote.origin.url")
	denyGitAuth(t, cfg, checkout)
	setFakeEnv(cfg,
		// Else git probes an unknown ssh command with -G first, a second
		// call per fetch that would throw the count off.
		"GIT_SSH_VARIANT", "simple",
		"POLAKO_FAKE_SSH_COUNTER", filepath.Join(t.TempDir(), "ssh-calls"),
		"POLAKO_FAKE_SSH_FAILS", strconv.Itoa(fails),
		"POLAKO_FAKE_SSH_REPO", origin)
}

// Issue #425: git's own credentials being refused is a narrower, likelier-
// transient case than a dead remote, so it must not stop the caller the way
// TestSyncDefaultBranchReportsAnUnreachableOrigin's does — st remembers it
// instead, for processIssue to hold the run on (issue #595).
func TestSyncDefaultBranchDoesNotStopOnAnAuthFailure(t *testing.T) {
	t.Parallel()
	buf := captureLog(t)
	_, checkout := upstream(t)
	cfg := config{dir: checkout, ui: testUI(t), ghRetryWait: 1}
	denyGitAuth(t, &cfg, checkout)

	st := &issueState{}
	if err := syncDefaultBranch(context.Background(), cfg, st); err != nil {
		t.Fatalf("an auth failure must not stop the caller: %v", err)
	}
	if !st.fetchAuthFailed {
		t.Error("st.fetchAuthFailed = false, want true — processIssue needs it to hold the run")
	}
	if !strings.Contains(buf.String(), "could not authenticate") {
		t.Errorf("log does not say the fetch could not authenticate:\n%s", buf)
	}
}

// A nil st is what the tidy sweep passes — it isn't about any one about-to-run
// issue — and must not panic just because there is nothing to remember this
// against.
func TestSyncDefaultBranchDoesNotStopOnAnAuthFailureWithNilState(t *testing.T) {
	t.Parallel()
	_, checkout := upstream(t)
	cfg := config{dir: checkout, ui: testUI(t), ghRetryWait: 1}
	denyGitAuth(t, &cfg, checkout)

	if err := syncDefaultBranch(context.Background(), cfg, nil); err != nil {
		t.Fatalf("an auth failure must not stop the caller: %v", err)
	}
}

// No origin at all is not an unreachable one: there is no mirror to keep, which
// is a warning today and stays one. It is also what every drain test relies on,
// running as they do in a directory that is not a checkout.
func TestSyncDefaultBranchWithoutAnOriginIsNotFatal(t *testing.T) {
	t.Parallel()
	buf := captureLog(t)
	_, checkout := upstream(t)
	gitAt(t, checkout, "remote", "remove", "origin")

	if err := syncDefaultBranch(context.Background(), config{dir: checkout, ui: testUI(t)}, nil); err != nil {
		t.Fatalf("err = %v, want a warning and nothing more", err)
	}
	if !strings.Contains(buf.String(), "no origin remote to fetch") {
		t.Errorf("log does not say the sync was skipped:\n%s", buf)
	}
}
