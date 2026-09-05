package main

// `polako work -dry-run`: resolve the next issue exactly as the drain would and
// print the claude invocation it would get — or the PR it would wait on
// instead. Every GitHub call it makes is a read, and it writes nothing.

import (
	"context"
	"fmt"
	"io"
	"log"
	"slices"
	"strings"
)

// dryRun answers "what would this do here?" without doing any of it. It
// resolves the next issue exactly as the drain would and reports what that
// issue would get: an invocation, or the PR it would wait on instead.
//
// Nothing is run and nothing is written. Every GitHub call it makes is a read,
// preflight declares no label, and the recorder is off — so pointing it at an
// unfamiliar repository leaves that repository exactly as it found it.
//
// The narration goes to the log and the invocation alone to out — stdout for a
// real invocation — so the command can be piped somewhere useful instead of
// being fished out of a transcript.
func dryRun(ctx context.Context, cfg config, out io.Writer) error {
	// A dry run says what the next issue would be, and a container is never that
	// issue — but a real run also closes a finished, unheld container before it
	// picks one up, so this names the containers it would close and writes
	// nothing, the same promise the rest of the command keeps.
	ready, blocked, heldBack, containers, err := openIssues(ctx, cfg)
	if err != nil {
		return err
	}
	for _, c := range containers {
		if c.finished() && !c.held {
			log.Printf("epic #%d: all %s closed — would comment on it and close it", c.number, plural(c.total, "sub-issue"))
		}
	}
	// What the operator would see if they ran it for real, in the two queues
	// the drain actually keeps. -skip is applied here as well, so a queue this
	// prints is a queue this would work.
	workable := func(ns []int) []int {
		queue := slices.DeleteFunc(slices.Clone(ns), func(n int) bool { return cfg.skip[n] })
		slices.Sort(queue)
		return queue
	}
	if queue := workable(ready); len(queue) > 0 {
		log.Printf("ready: %s", issueRefs(queue))
	}
	if waiting := workable(blocked); len(waiting) > 0 {
		log.Printf("waiting on an answer: %s", issueRefs(waiting))
	}
	logHeldBack(heldBack, cfg.skip)
	// The drain takes the lowest ready issue. With none, it runs the lowest
	// issue waiting on an answer, to find out whether the reply is already on
	// the thread — which is what awaitAnswer does on a drain that flagged none
	// of them itself.
	issue := pickLowest(ready, cfg.skip)
	if issue == 0 {
		issue = pickLowest(blocked, cfg.skip)
	}
	if issue == 0 {
		log.Println("no open issues — nothing to work")
		return nil
	}
	// Restart safety is the first thing an issue is put through, and the answer
	// an operator most wants from a dry run: an issue whose branch already has
	// a PR never gets a claude run at all.
	branch := fmt.Sprintf("%s%d", cfg.branchPrefix, issue)
	pr, err := prForBranch(ctx, cfg, branch)
	if err != nil {
		return err
	}
	if pr != nil {
		// Which of the three the drain would do depends on the PR's state, and
		// naming the wrong one is the single thing this flag must not do: a
		// merged PR is the case where it would write to GitHub at all.
		next := "wait on that PR"
		switch pr.State {
		case "MERGED":
			next = "close the issue and move on"
		case "OPEN":
		default:
			next = "park the issue for a human"
		}
		log.Printf("issue #%d already has PR #%d (%s) on branch %s — it would %s "+
			"rather than run claude: %s", issue, pr.Number, pr.State, branch, next, pr.URL)
		return nil
	}
	runCfg, prompt, _ := issueRun(cfg, issue)
	// Route the implement dispatch through the same policy seam a real run
	// uses, so -dry-run keeps its promise of printing the exact invocation: the
	// issue's model:/effort: labels and its Estimate: size resolve here just as
	// they would at pickup, so --model and --effort show the value a real run
	// would get — the epic's labels (#364) included, since issuePickupPolicy
	// reads them.
	policy := newRunPolicy(cfg)
	policy.labels, policy.size = issuePickupPolicy(ctx, cfg, issue)
	runCfg = policy.choose(reasonImplement).apply(runCfg)
	log.Printf("issue #%d would be worked next; the invocation follows on stdout", issue)
	_, err = fmt.Fprintln(out, commandLine(cfg.claudeBin, buildArgs(runCfg, prompt, "")))
	return err
}

// commandLine renders an argv the way a person would type it, so what a dry run
// prints can be pasted into a shell rather than merely read. The quoting is
// POSIX because a printed command line has to pick a dialect; nothing here is
// ever run through a shell, so this is documentation of an argv and not the
// argv itself.
func commandLine(bin string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	for _, a := range append([]string{bin}, args...) {
		parts = append(parts, shellQuote(a))
	}
	return strings.Join(parts, " ")
}

// shellQuote wraps anything a shell would not take literally in single quotes —
// the one form needing no further escaping, bar the single quote itself, which
// is spelled by closing, escaping and reopening. The allowlist is deliberate:
// guessing at which punctuation is safe is how a printed command line ends up
// meaning something else.
func shellQuote(s string) string {
	safe := func(r rune) bool {
		return r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' ||
			strings.ContainsRune("-_./:=+,@", r)
	}
	if s != "" && !strings.ContainsFunc(s, func(r rune) bool { return !safe(r) }) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
