package main

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// A folder that isn't a repo yet gets setup's fix, not a raw git error — and
// the raw error still follows it (issue #735).
func TestCheckoutErrorOnAFolderThatIsNotARepo(t *testing.T) {
	t.Parallel()
	err := checkoutError(context.Background(), config{dir: t.TempDir()})
	if err == nil {
		t.Fatal("checkoutError = nil, want an error for a folder with no repo")
	}
	msg := err.Error()
	for _, want := range []string{"isn't a git repo yet", "git init", "gh repo create <name> --private --source . --push",
		"git remote set-head origin -a", "point -dir at a checkout", "not a git repository"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error = %q, want it to contain %q", msg, want)
		}
	}
	if strings.Index(msg, "not a git repository") < strings.Index(msg, "git init") {
		t.Errorf("error = %q, want the raw git error after the advice", msg)
	}
	if strings.Contains(msg, "gh auth login") {
		t.Errorf("error = %q, want no auth advice for a folder with no repo", msg)
	}
}

func TestCheckoutErrorOnACheckout(t *testing.T) {
	t.Parallel()
	_, checkout := upstream(t)
	if err := checkoutError(context.Background(), config{dir: checkout}); err != nil {
		t.Errorf("checkoutError = %v, want nil on a checkout", err)
	}
}

// A checkout with no remote: gh would say "no git remotes found", which is
// not an auth problem.
func TestRepoViewErrorOnACheckoutWithNoRemote(t *testing.T) {
	t.Parallel()
	_, checkout := upstream(t)
	gitAt(t, checkout, "remote", "remove", "origin")
	ghErr := errors.New("exit status 1: no git remotes found")

	msg := repoViewError(context.Background(), config{dir: checkout}, ghErr, "").Error()
	for _, want := range []string{"no origin remote", "gh repo create", "git remote set-head origin -a"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error = %q, want it to contain %q", msg, want)
		}
	}
	if strings.Contains(msg, "gh auth login") {
		t.Errorf("error = %q, want no auth advice for a missing remote", msg)
	}
}

// Status & co never run checkoutError first, so repoViewError has to catch a
// folder with no repo itself, and keep the caller's hint before the raw error.
func TestRepoViewErrorOnAFolderKeepsTheHint(t *testing.T) {
	t.Parallel()
	ghErr := errors.New("exit status 1: failed to run git: fatal: not a git repository")
	msg := repoViewError(context.Background(), config{dir: t.TempDir()}, ghErr, "; or name one with -repo owner/name").Error()
	for _, want := range []string{"git init", "-repo owner/name", "not a git repository"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error = %q, want it to contain %q", msg, want)
		}
	}
	if strings.Index(msg, "-repo owner/name") > strings.Index(msg, "the error was") {
		t.Errorf("error = %q, want the hint before the raw error", msg)
	}
}

// On a checkout with an origin, gh's own text decides. A remote that isn't on
// GitHub and a gh that isn't logged in get different advice; only the second
// says `gh auth login` — even though gh's remote error mentions it too.
func TestRepoViewErrorTellsNoGitHubRemoteFromNotLoggedIn(t *testing.T) {
	t.Parallel()
	_, checkout := upstream(t)
	cfg := config{dir: checkout}
	cases := []struct {
		name, ghSaid string
		want, avoid  []string
	}{
		{"remote not on GitHub",
			"exit status 1: none of the git remotes configured for this repository point to a known GitHub host. " +
				"To tell gh about a new GitHub host, please use `gh auth login`",
			[]string{"has no GitHub remote", "gh repo create", "git remote set-url origin"},
			[]string{"isn't logged in"}},
		{"not logged in",
			"exit status 4: To get started with GitHub CLI, please run:  gh auth login",
			[]string{"isn't logged in", "run `gh auth login`"},
			[]string{"has no GitHub remote"}},
		{"bad token",
			"exit status 1: HTTP 401: Bad credentials (https://api.github.com/graphql)",
			[]string{"refused its token", "gh auth login"},
			nil},
		{"anything else",
			"exit status 1: GraphQL: Could not resolve to a Repository",
			[]string{"could not read the GitHub repository", "gh auth status"},
			[]string{"gh auth login"}},
	}
	for _, c := range cases {
		msg := repoViewError(context.Background(), cfg, errors.New(c.ghSaid), "").Error()
		for _, w := range c.want {
			if !strings.Contains(msg, w) {
				t.Errorf("%s: error = %q, want it to contain %q", c.name, msg, w)
			}
		}
		// The raw gh text follows the advice; mask it before checking what
		// the advice itself avoids saying.
		advice, raw, ok := strings.Cut(msg, " — gh said: ")
		if !ok || raw != c.ghSaid {
			t.Errorf("%s: error = %q, want the raw gh error after \" — gh said: \"", c.name, msg)
		}
		for _, a := range c.avoid {
			if strings.Contains(advice, a) {
				t.Errorf("%s: advice = %q, want it not to contain %q", c.name, advice, a)
			}
		}
	}
}
