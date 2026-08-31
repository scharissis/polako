package main

// How a run on one issue ends short of a merge: parkedError hands the issue to
// a human without stopping the drain, deferredError puts it down until someone
// answers a question, and leftWork is the on-disk probe that tells "the run
// decided nothing" apart from "the run did the work and never committed it".

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// deferredError puts an issue down without giving it up: a run asked something,
// the question is flagged on GitHub, and there is nothing more to do here until
// a person replies. It is the one non-terminal way a run can end — not a park,
// because nobody has decided anything, and not fatal, because every issue
// behind it is still perfectly workable.
//
// baseline is the newest comment on the thread when the question was flagged,
// so a later check can tell a reply from silence. See commentBaseline.
type deferredError struct{ baseline int64 }

func (e *deferredError) Error() string { return "waiting for an answer on the issue thread" }

// deferReason reports whether an error leaves its issue waiting on a human,
// and what the thread looked like at that point.
func deferReason(err error) (*deferredError, bool) {
	var de *deferredError
	if errors.As(err, &de) {
		return de, true
	}
	return nil, false
}

// parkedError ends work on one issue without ending the drain. The distinction
// is the whole point: an unimplementable issue is a fact about that issue, and
// stopping the session over it strands every later issue too — typically hours
// before anyone looks at the terminal. Fatal is reserved for conditions where
// no further progress is possible at all: a bad -dir, a gh that cannot answer,
// a -skill this installation does not have, a token the API refuses.
type parkedError struct {
	reason string
	// category is the same park in one identifier, for the terminal issue
	// record: the reason above is written for a person and quotes issue
	// numbers, dollars and branch names, none of which a record may hold. It is
	// an argument rather than something derived from the text so that a park
	// added later cannot forget to classify itself.
	category string
	// aside is for the operator's terminal and goes nowhere else. The reason is
	// posted to the issue thread verbatim, so anything that is nobody's business
	// but the operator's — a local absolute path, which names their account and
	// the layout of their disk — travels here instead, for the same reason the
	// resume id is kept out of the reason where the park is logged.
	aside string
}

func (e *parkedError) Error() string { return e.reason }

// park stops an issue and states why in terms a person can act on — the text
// goes on the issue thread and into the exit summary, so it is written for a
// reader who was not watching. why is the same thing said in one of the park
// reason identifiers, for the record.
func park(why, format string, a ...any) error {
	return &parkedError{reason: fmt.Sprintf(format, a...), category: why}
}

// parkAside is park with one line the log gets and the issue thread does not.
func parkAside(why, aside, format string, a ...any) error {
	return &parkedError{reason: fmt.Sprintf(format, a...), category: why, aside: aside}
}

// parkCategoryOf classifies an error that ended an issue, for the record that
// says how it ended. A park carries its own category; the two fatal conditions
// that also end an issue name themselves; anything else genuinely cannot say,
// and says so rather than being filed under a category it did not earn.
func parkCategoryOf(err error) string {
	var pe *parkedError
	switch {
	case errors.As(err, &pe) && pe.category != "":
		return pe.category
	case errors.Is(err, errAuth):
		return parkAuth
	case errors.Is(err, errNoWork):
		return parkNoSkill
	}
	return parkUnknown
}

// parkReason reports whether an error parks its issue, and why.
func parkReason(err error) (string, bool) {
	var pe *parkedError
	if errors.As(err, &pe) {
		return pe.reason, true
	}
	return "", false
}

// parkAsideOf reports what a park has to say to the operator alone, or "".
func parkAsideOf(err error) string {
	var pe *parkedError
	if errors.As(err, &pe) {
		return pe.aside
	}
	return ""
}

// leftWork is what a run left on disk for one issue: commits on the branch the
// skill works on, and uncommitted changes in the worktree it works in.
//
// A run that implemented the whole change and never committed it exits exactly
// as cleanly as one that decided nothing, so the park message is the same for
// both — and the two are completely different jobs for the human it is
// addressed to. The first is half an hour of rebase and review; the second
// needs the issue re-specified. Everything needed to tell them apart is on
// disk, one git call from a supervisor that already knows the branch name.
type leftWork struct {
	branch string
	path   string // the worktree holding that branch, "" when none does
	// commits on the branch the default branch does not have, and whether that
	// comparison could be made at all. A checkout with no origin/HEAD to compare
	// against must not be reported as a branch with nothing on it: "no commits"
	// is the half of this message a person acts on, so it is only ever said when
	// it was actually counted.
	commits int
	counted bool
	dirty   int // paths that worktree reports as changed or untracked
	// a worktree is listed for this branch and its directory is on disk, but
	// `git status` there would not run — an index lock, a half-broken checkout.
	// Distinct from "no worktree" (path ""), which the blanked path reads as
	// once this is set: reclaim must leave the first for a human rather than
	// force past a state it could not inspect.
	unreadable bool
}

// salvageable reports whether a run got far enough that a person should start
// from what is there rather than from scratch. It is also the discriminator a
// caller deciding resume-versus-park would want, which is why it is a predicate
// on the probe rather than a condition spelled out at the one call site.
func (w leftWork) salvageable() bool { return w.commits > 0 || w.dirty > 0 }

// describe says what is there, in the words the park reason carries to the log,
// the run summary and the issue thread alike. Empty when nothing is there, so
// the message that means "the run decided nothing" still means only that.
//
// The worktree's path is deliberately not in here; see where.
func (w leftWork) describe() string {
	if !w.salvageable() {
		return ""
	}
	branch := fmt.Sprintf("branch %s could not be compared with the default branch", w.branch)
	if w.counted {
		commits := "no commits"
		if w.commits > 0 {
			commits = plural(w.commits, "commit")
		}
		branch = fmt.Sprintf("branch %s has %s", w.branch, commits)
	}
	clauses := []string{branch}
	if w.path != "" {
		changes := "no uncommitted changes"
		if w.dirty > 0 {
			changes = "uncommitted changes in " + plural(w.dirty, "file")
		}
		clauses = append(clauses, "its worktree has "+changes)
	}
	return strings.Join(clauses, " and ") +
		" — the run left work behind, so start there rather than from scratch"
}

// where names the worktree on disk, for the log and nothing else. It is the one
// part of this a person cannot get from the issue thread, and the one part that
// must not go there: an absolute path names the operator's account and how their
// disk is laid out, and the thread may be public. See parkedError.aside.
func (w leftWork) where() string {
	if w.path == "" || !w.salvageable() {
		return ""
	}
	return "the work it left is in " + w.path
}

// inspectLeftWork reads what is on disk for one issue. Best-effort in the same
// way as claudeVersion: every git call that fails leaves the field it would
// have filled at zero, so the worst case is the message this had before — a
// park never becomes an error over its own diagnosis.
//
// It reads git and writes nothing, and every answer is derived from the working
// copy rather than from anything this process remembers between runs, so the
// orchestration state still lives entirely in GitHub.
func inspectLeftWork(ctx context.Context, cfg config, issue int) leftWork {
	w := leftWork{branch: fmt.Sprintf("%s%d", cfg.branchPrefix, issue)}
	// origin's default branch rather than the local one: the count is the same
	// either way, and the remote ref does not depend on the operator's checkout
	// being on the right branch when the park happens.
	if head, err := git(ctx, cfg, "symbolic-ref", "refs/remotes/origin/HEAD", "--short"); err == nil {
		base := strings.TrimSpace(string(head))
		if out, err := git(ctx, cfg, "rev-list", "--count", base+".."+w.branch); err == nil {
			if n, err := strconv.Atoi(strings.TrimSpace(string(out))); err == nil {
				w.commits, w.counted = n, true
			}
		}
	}
	if list, err := git(ctx, cfg, "worktree", "list", "--porcelain"); err == nil {
		w.path = worktreeFor(string(list), w.branch)
	}
	if w.path != "" {
		// --untracked-files=all, because the default folds a whole new directory
		// into one `?? pkg/` line — and a run that wrote a new package and never
		// committed it is exactly the case this message exists for, so "1 file"
		// there would understate it by however many files it added.
		out, err := capture(ctx, w.path, "git", "status", "--porcelain", "--untracked-files=all")
		if err != nil {
			// git goes on listing a worktree whose directory somebody deleted by
			// hand until the next prune, and sending a person to a path that is
			// not there is worse than sending them nowhere. A worktree that
			// cannot be read is reported as no worktree at all — the branch
			// clause still says what is there. When the directory is still on
			// disk, though, `git status` failing means a lock or a half-broken
			// checkout rather than a stale admin entry: reclaim records that
			// separately, because a worktree it cannot vet is not one it may
			// force past.
			if _, statErr := os.Stat(w.path); statErr == nil {
				w.unreadable = true
			}
			w.path = ""
		}
		for _, line := range strings.Split(string(out), "\n") {
			if p := porcelainPath(line); p != "" && p != planFile {
				w.dirty++
			}
		}
	}
	return w
}
