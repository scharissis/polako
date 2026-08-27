package main

// `polako status` answers "where does my backlog stand right now?" in
// one snapshot, from GitHub alone.
//
// Everything worth knowing is already there — the queue, the parked issues, the
// awaiting-answer label, the open PR and its checks — because all orchestration
// state lives in GitHub by design. What was missing was a single place to read
// it: an operator away from the terminal a drain runs in had to reassemble the
// picture a page at a time.
//
// Three rules shape it:
//
//   - Reads only. Every call it makes is one of the read subcommands the drain
//     itself re-derives state with at startup, so nothing here can move an
//     issue, a label or a PR.
//   - No run data. The metrics files are write-only outside `stats`, and a
//     status that read them would be wrong anyway: the drain being asked about
//     is quite possibly running on somebody else's machine.
//   - State, not liveness. It never asks whether a drain is running, and says
//     the same thing whether one is or not. What it prints is what a drain
//     starting now would do next — which is the same thing a running drain is
//     already doing.
//
// It also prints no issue, PR or comment text: numbers, branches, labels and
// states only. That is what an operator needs in order to decide where to go
// next, and it keeps attacker-controllable text — on any repo that accepts
// issues from outside the team — out of the terminal it lands in.

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
	"time"
)

// statusPRs bounds how many open PRs get their mergeable/checks/review state
// looked up, one `gh pr view` apiece. One issue is in flight at a time, so the
// matching set is normally none or one; a repository carrying a pile of
// abandoned issue branches would otherwise turn one snapshot into a hundred
// round trips. Anything past the cap is still listed, by number, and the report
// says so rather than quietly showing less than it found.
const statusPRs = 8

type statusOptions struct {
	dir          string
	repo         string
	label        string
	branchPrefix string
	strictOrder  bool
}

// runStatus is the `status` subcommand: parse its own flags, read GitHub, print
// one snapshot. now is passed in so the "quiet for" spans are testable.
func runStatus(ctx context.Context, args []string, out io.Writer, now time.Time) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(out)
	var opt statusOptions
	fs.StringVar(&opt.dir, "dir", ".", "path to the repository's main checkout, when -repo is not given")
	fs.StringVar(&opt.repo, "repo", "",
		"repository to report on (owner/name), instead of whichever -dir is a checkout of")
	fs.StringVar(&opt.label, "label", "", "only count issues carrying this label, as `polako work` would (empty = all)")
	fs.StringVar(&opt.branchPrefix, "branch-prefix", "issue-", "branch name prefix the skill uses")
	fs.BoolVar(&opt.strictOrder, "strict-order", false,
		"report as a work run with -strict-order would: an issue awaiting an answer keeps its place in the queue")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "Usage: polako status [flags]\n\n"+
			"Prints where the backlog stands, derived from GitHub: the queue in the\n"+
			"order `polako work` would take it, what is waiting on you, and any open PR on\n"+
			"a branch the skill named. Reads only — nothing here changes anything,\n"+
			"and it says the same thing whether or not a shift is running.\n\n"+envUsage+"\nFlags:\n")
		fs.PrintDefaults()
	}
	// The same environment defaults the drain honours, so a POLAKO_LABEL
	// that scopes the drain scopes the report of it too.
	if err := applyEnvDefaults(fs); err != nil {
		return err
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil // -h is how a person finds the flags, not a failure
		}
		return errFlagsReported
	}
	if rest := fs.Args(); len(rest) > 0 {
		return fmt.Errorf("unexpected argument %q — status takes flags only", rest[0])
	}

	cfg, err := statusConfig(ctx, opt)
	if err != nil {
		return err
	}
	snap, err := readStatus(ctx, cfg, now)
	if err != nil {
		return err
	}
	renderStatus(out, cfg, snap)
	return nil
}

// statusConfig builds the config the shared GitHub readers take, and settles
// which repository is being reported on. -repo names it outright, which is what
// makes the command usable from a directory that is not a checkout of anything;
// without it, gh is asked to resolve it from -dir the way a drain does.
//
// Either way the answer is pinned into ghRepo, so every later call names the
// repository explicitly and the report cannot silently describe a different one
// than its own header says.
func statusConfig(ctx context.Context, opt statusOptions) (config, error) {
	cfg := config{
		ghBin:        "gh",
		ghRetryWait:  ghRetryDelay,
		label:        opt.label,
		branchPrefix: opt.branchPrefix,
		strictOrder:  opt.strictOrder,
	}
	if _, err := exec.LookPath(cfg.ghBin); err != nil {
		return cfg, fmt.Errorf("%q not found on PATH (%w) — status reads GitHub through it", cfg.ghBin, err)
	}
	abs, err := filepath.Abs(opt.dir)
	if err != nil {
		return cfg, fmt.Errorf("resolving -dir: %w", err)
	}
	cfg.dir = abs

	if repo := strings.TrimSpace(opt.repo); repo != "" {
		// Both halves, not just the separator: "owner/" reaches gh as a
		// repository with no name and comes back as a lookup failure nobody can
		// trace to the flag that caused it.
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

// --- reading ---

// statusSnapshot is the whole answer, derived and then rendered separately so
// the derivation can be tested without reading columns out of a table.
type statusSnapshot struct {
	queues issueQueues
	// next is the issue a drain starting now would pick up, or 0 for none: the
	// lowest ready one, else the lowest one waiting on an answer — which a
	// drain runs to find out whether the reply is already on the thread.
	next int
	// quiet is how long each blocked issue's thread has been silent, keyed by
	// issue. Absent for a thread whose age could not be read.
	quiet map[int]time.Duration
	prs   []statusPR
	// undetailed is how many open PRs on issue branches were left as numbers
	// alone because statusPRs was reached.
	undetailed []int
	// strictOrder is the -strict-order the queue was read under, kept because it
	// changes what "next" means rather than what is in the queues: a flagged
	// issue holds its place, and everything behind it waits on it.
	strictOrder bool
}

// statusPR is one open PR on a branch the skill named, and what GitHub says
// about its readiness to merge.
type statusPR struct {
	number int
	branch string
	issue  int // the issue its branch names, or 0 if the suffix is not a number
	url    string
	view   prView
	// detailed is false for a PR past the statusPRs cap, whose view was never
	// looked up. Rendering it as though everything were unknown would be a
	// claim about GitHub that nobody made.
	detailed bool
}

func readStatus(ctx context.Context, cfg config, now time.Time) (statusSnapshot, error) {
	snap := statusSnapshot{quiet: map[int]time.Duration{}}

	// The drain's own listing, exclusions and all: what `status` says a drain
	// would work has to be derived the way the drain derives it, or the two
	// disagree the moment one of them learns a new exclusion.
	queues, err := openQueues(ctx, cfg)
	if err != nil {
		return snap, err
	}
	snap.queues = queues
	// The drain's own rule, in dryRun's words: the lowest ready issue, and with
	// none, the lowest issue waiting on an answer. -strict-order is the one
	// thing that changes it — openIssues folds the two queues into one there, so
	// a flagged issue keeps its place and everything behind it waits.
	snap.strictOrder = cfg.strictOrder
	if cfg.strictOrder {
		snap.next = pickLowest(append(slices.Clone(snap.queues.ready), snap.queues.blocked...), nil)
	} else if snap.next = pickLowest(snap.queues.ready, nil); snap.next == 0 {
		snap.next = pickLowest(snap.queues.blocked, nil)
	}

	for _, issue := range snap.queues.blocked {
		comments, err := issueComments(ctx, cfg, issue)
		if err != nil {
			if ctx.Err() != nil {
				return snap, ctx.Err()
			}
			// One thread that would not answer is not a reason to report
			// nothing: the issue is still listed, without its span.
			continue
		}
		if d, ok := quietFor(comments, now); ok {
			snap.quiet[issue] = d
		}
	}

	snap.prs, snap.undetailed, err = readStatusPRs(ctx, cfg, snap.queues)
	if err != nil {
		return snap, err
	}
	return snap, nil
}

// quietFor is how long the thread has been silent: the age of its newest
// comment.
//
// A proxy for "how long has the question waited", and deliberately an honest
// one. Which comment is the skill's question is exactly what cannot be told
// from here — the drain authenticates as the person being asked, so authorship
// does not separate them — so this reports what it can actually see. On a
// thread nobody has replied to, the newest comment is the question, which is
// the case the number is wanted for.
func quietFor(comments []issueComment, now time.Time) (time.Duration, bool) {
	if len(comments) == 0 {
		return 0, false
	}
	at := recTime(comments[len(comments)-1].CreatedAt)
	if at.IsZero() {
		return 0, false
	}
	d := now.Sub(at)
	if d < 0 {
		// A clock disagreement, not a comment from the future. Report it as
		// fresh rather than as a negative age.
		return 0, true
	}
	return d, true
}

// readStatusPRs finds the open PRs on branches the skill named, and looks up
// what state each is in.
//
// One `pr list` for all of them rather than one per issue: a backlog of two
// hundred issues would otherwise be two hundred round trips, and the drain's
// own per-branch lookup is the right call for the one issue it is working, not
// for a whole-backlog snapshot. The detail is still `pr view`, exactly as
// supervisePR reads it.
//
// A PR whose issue is closed is left out: it is finished business, and a
// snapshot of where the backlog stands is not the place for it.
func readStatusPRs(ctx context.Context, cfg config, q issueQueues) ([]statusPR, []int, error) {
	raw, err := retryRead(ctx, cfg, "listing open PRs", func() ([]byte, error) {
		return gh(ctx, cfg, "pr", "list", "--state", "open", "--limit", "200",
			"--json", "number,headRefName,url")
	})
	if err != nil {
		return nil, nil, err
	}
	prs, err := branchPRs(raw, cfg.branchPrefix, q)
	if err != nil {
		return nil, nil, err
	}
	var undetailed []int
	for i := range prs {
		if i >= statusPRs {
			undetailed = append(undetailed, prs[i].number)
			continue
		}
		// Retried like the two list reads above it: one blip on `pr view` would
		// otherwise drop the PR out of the `needs you` line, which is the whole
		// point of the report.
		view, err := retryRead(ctx, cfg, fmt.Sprintf("reading PR #%d", prs[i].number),
			func() (prView, error) { return prStatus(ctx, cfg, prs[i].number) })
		if err != nil {
			if ctx.Err() != nil {
				// Ctrl+C is not a PR whose state would not read. Reporting a
				// table of "not read" and exiting 0 would claim GitHub answered.
				return nil, nil, ctx.Err()
			}
			// Same tolerance as a thread that would not answer: the PR is
			// listed, without the state nobody could read.
			continue
		}
		prs[i].view, prs[i].detailed = view, true
	}
	return prs, undetailed, nil
}

// branchPRs reduces a `pr list` payload to the PRs whose head branch is one the
// skill would have named for an issue still in the backlog, ordered by that
// issue so the report reads in queue order.
func branchPRs(raw []byte, prefix string, q issueQueues) ([]statusPR, error) {
	var listed []struct {
		Number      int    `json:"number"`
		HeadRefName string `json:"headRefName"`
		URL         string `json:"url"`
	}
	if err := json.Unmarshal(raw, &listed); err != nil {
		return nil, fmt.Errorf("parsing PR list: %w", err)
	}
	open := append(append(slices.Clone(q.ready), q.blocked...), q.parked...)
	var out []statusPR
	for _, p := range listed {
		issue, ok := issueForBranch(p.HeadRefName, prefix)
		if !ok || !slices.Contains(open, issue) {
			continue
		}
		out = append(out, statusPR{number: p.Number, branch: p.HeadRefName, issue: issue, url: p.URL})
	}
	slices.SortFunc(out, func(a, b statusPR) int {
		if a.issue != b.issue {
			return a.issue - b.issue
		}
		return a.number - b.number
	})
	return out, nil
}

// issueForBranch reads the issue number back out of a branch name. It is the
// other end of the naming contract the skill holds up: the supervisor finds a
// PR by its head branch, and this finds the issue by the same rule, so
// -branch-prefix keeps working here too.
//
// An empty prefix would match every branch in the repository and read the whole
// name as a number, so it matches nothing instead.
func issueForBranch(branch, prefix string) (int, bool) {
	if prefix == "" {
		return 0, false
	}
	rest, ok := strings.CutPrefix(branch, prefix)
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(rest)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// --- rendering ---

func renderStatus(w io.Writer, cfg config, snap statusSnapshot) {
	fmt.Fprintf(w, "%s%s\n", cfg.repo, statusScope(cfg))
	printPairs(w, "", queuePairs(snap))
	printStatusPRs(w, snap)
	if line := needsYou(snap); line != "" {
		fmt.Fprintf(w, "\n%s\n", line)
	}
}

// statusScope names what narrowed or reordered the report, so a snapshot that
// covers less than the whole backlog cannot be read as one that covers all of
// it — the flags are settable from the environment, and one forgotten in a
// profile is otherwise invisible here.
func statusScope(cfg config) string {
	var parts []string
	if cfg.label != "" {
		parts = append(parts, "issues labelled "+cfg.label)
	}
	if cfg.strictOrder {
		parts = append(parts, "-strict-order")
	}
	if len(parts) == 0 {
		return ""
	}
	return " — " + strings.Join(parts, ", ")
}

func queuePairs(snap statusSnapshot) [][2]string {
	q := snap.queues
	if len(q.ready)+len(q.blocked)+len(q.parked) == 0 {
		return [][2]string{{"queue", "nothing open — a shift starting now would find the backlog cleared"}}
	}
	pairs := [][2]string{{"ready", queueLine(q.ready)}}
	if len(q.blocked) > 0 {
		refs := make([]string, 0, len(q.blocked))
		for _, n := range q.blocked {
			ref := "#" + strconv.Itoa(n)
			if d, ok := snap.quiet[n]; ok {
				ref += " (quiet " + dur(d) + ")"
			}
			refs = append(refs, ref)
		}
		pairs = append(pairs, [2]string{"awaiting you",
			fmt.Sprintf("%s — %s", plural(len(q.blocked), "issue"), strings.Join(refs, ", "))})
	}
	if len(q.parked) > 0 {
		pairs = append(pairs, [2]string{"parked",
			fmt.Sprintf("%s — %s, labelled %s", plural(len(q.parked), "issue"),
				issueRefs(q.parked), needsHumanLabel)})
	}
	// Last, because it is the answer the rest of the table is context for.
	pairs = append(pairs, [2]string{"next", nextLine(snap)})
	return pairs
}

func queueLine(ready []int) string {
	if len(ready) == 0 {
		return "no issue is workable right now"
	}
	return fmt.Sprintf("%s — %s", plural(len(ready), "issue"), issueRefs(ready))
}

// nextLine says what a drain starting now would do first. The cases are the
// drain's own, in the drain's own order: nothing to do at all, else restart
// safety — an existing PR means the skill is never re-run, whichever queue the
// issue came out of — else work the lowest ready issue, or, with none, run the
// lowest issue waiting on an answer to find out whether the reply is already on
// the thread.
//
// Restart safety comes before the awaiting-answer wording because processIssue
// puts it first: a flagged issue whose branch already carries a PR is
// supervised, not re-run.
func nextLine(snap statusSnapshot) string {
	if snap.next == 0 {
		return "nothing — every open issue is parked"
	}
	if pr := findPR(snap, snap.next); pr != nil {
		return fmt.Sprintf("#%d — its branch already has PR #%d, so it would wait on that rather "+
			"than run the skill again", snap.next, pr.number)
	}
	if slices.Contains(snap.queues.blocked, snap.next) {
		why := "nothing else is workable"
		if snap.strictOrder {
			// It is not that nothing else is workable — issues behind it are.
			// -strict-order is why they wait.
			why = "-strict-order holds the queue behind it"
		}
		return fmt.Sprintf("#%d — %s, so it would re-run that issue "+
			"to see whether your reply is on the thread", snap.next, why)
	}
	return fmt.Sprintf("#%d", snap.next)
}

func findPR(snap statusSnapshot, issue int) *statusPR {
	if i := slices.IndexFunc(snap.prs, func(p statusPR) bool { return p.issue == issue }); i >= 0 {
		return &snap.prs[i]
	}
	return nil
}

func printStatusPRs(w io.Writer, snap statusSnapshot) {
	if len(snap.prs) == 0 {
		return
	}
	rows := make([][]string, 0, len(snap.prs))
	for _, p := range snap.prs {
		rows = append(rows, []string{
			"#" + strconv.Itoa(p.number), p.branch, "#" + strconv.Itoa(p.issue),
			mergeableCell(p), checksCell(p), reviewCell(p), p.url,
		})
	}
	// Every column left-aligned: these are names and states, not figures, and a
	// right-aligned URL is a column nobody can scan.
	header := []string{"pr", "branch", "issue", "mergeable", "checks", "review", "url"}
	printTable(w, "open prs on issue branches", header, rows, len(header))
	if len(snap.undetailed) > 0 {
		fmt.Fprintf(w, "  (%s past the first %d, listed without state: %s)\n",
			plural(len(snap.undetailed), "PR"), statusPRs, issueRefs(snap.undetailed))
	}
}

// unknownCell is what every column of a PR whose state was not read says. It is
// deliberately not "none" or "passing": nobody looked, and a snapshot must not
// invent the answer it went there for.
const unknownCell = "not read"

func mergeableCell(p statusPR) string {
	if !p.detailed {
		return unknownCell
	}
	if p.view.mergeable == "" {
		return "unknown"
	}
	return strings.ToLower(p.view.mergeable)
}

func checksCell(p statusPR) string {
	if !p.detailed {
		return unknownCell
	}
	if p.view.checks == checksFailing {
		return fmt.Sprintf("%s (%s)", p.view.checks, strings.Join(p.view.failing, ", "))
	}
	return p.view.checks
}

// reviewCell says where a PR's reviews stand, in all four cases — including the
// one prView.reviewNote leaves blank, because supervisePR is about to log a
// remediation for it and has somewhere else to say so. A snapshot has nowhere
// else.
func reviewCell(p statusPR) string {
	switch {
	case !p.detailed:
		return unknownCell
	case !p.view.changesRequested:
		return "clear"
	case p.view.reviewOutstanding():
		return "changes requested"
	case p.view.reviewedAt.IsZero():
		// reviewDecision said so and no individual review did, so there is no
		// date to hold the branch against — a block, without a claim about
		// whether anyone has answered it.
		return "changes requested"
	default:
		return "answered, awaiting re-review"
	}
}

// needsYou is the closing line: the things on GitHub that only a person can
// move, in the order they are usually dealt with. Absent when there are none,
// because a line that reads "needs you: nothing" on every healthy backlog is
// one an operator learns to skip past.
func needsYou(snap statusSnapshot) string {
	var parts []string
	if len(snap.queues.blocked) > 0 {
		parts = append(parts, "reply on "+issueRefs(snap.queues.blocked))
	}
	var mergeable, stuck []string
	for _, p := range snap.prs {
		switch {
		case !p.detailed:
		case p.view.remediable():
			// A drain would dispatch a run at this, so it is not yours yet.
		case p.view.checks == checksHuman:
			stuck = append(stuck, "#"+strconv.Itoa(p.number))
		case p.view.checks != checksPending:
			mergeable = append(mergeable, "#"+strconv.Itoa(p.number))
		}
	}
	if len(mergeable) > 0 {
		parts = append(parts, "review and merge PR "+strings.Join(mergeable, ", "))
	}
	if len(stuck) > 0 {
		parts = append(parts, "approve the checks waiting on you on PR "+strings.Join(stuck, ", "))
	}
	if len(snap.queues.parked) > 0 {
		parts = append(parts, fmt.Sprintf("decide what to do about %s (drop %s to requeue)",
			issueRefs(snap.queues.parked), needsHumanLabel))
	}
	if len(parts) == 0 {
		return ""
	}
	return "needs you: " + strings.Join(parts, "; ")
}
