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
//   - No run data. The metrics files are read only by `stats` and `plan`'s
//     pricing line, and a status that read them would be wrong anyway: the
//     drain being asked about is quite possibly running on somebody else's
//     machine.
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
	json         bool
}

// runStatus is the `status` subcommand: parse its own flags, read GitHub, print
// one snapshot. now is passed in so the "quiet for" spans are testable; rpt is
// the styler stats/status share, TTY-detected on stdout at the dispatch in main.
func runStatus(ctx context.Context, args []string, out io.Writer, now time.Time, rpt report) error {
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
	fs.BoolVar(&opt.json, "json", false,
		"print one JSON document to stdout instead of the text report — see docs/reference.md for the schema")
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
	if opt.json {
		return renderStatusJSON(out, cfg, snap)
	}
	renderStatus(out, rpt, cfg, snap)
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
		claudeBin:    "claude",
		usageTimeout: defaultUsageProbeTimeout,
		label:        opt.label,
		branchPrefix: opt.branchPrefix,
		strictOrder:  opt.strictOrder,
		// The same memo a shift carries, so a snapshot that ever grows a second
		// listing pays for an old gh once rather than once per call.
		queue: new(queueMemo),
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
	// usage is the account's own plan, as probeUsage answered it — nil when
	// the probe could not (see config.usage, which this mirrors).
	usage *usageSnapshot
	// plans is the docs/plans/ derivation (plans.go). Best-effort like usage
	// above: a failed read leaves it zero-valued rather than failing the
	// whole snapshot.
	plans planDocsSnapshot
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
	// Best-effort, like every claude-CLI read this binary makes: a probe
	// that fails leaves the row out of the report rather than failing the
	// snapshot, on a machine that is possibly not even the one running a
	// drain.
	if usage, ok := probeUsage(ctx, cfg); ok {
		snap.usage = &usage
	}
	// Same tolerance: a plans-section read that fails (an old gh, a search
	// hiccup) drops the section rather than the whole report — it is
	// supplementary to the queue above, which already propagated its own
	// read failures.
	if plans, err := readPlanDocs(ctx, cfg); err == nil {
		snap.plans = plans
	} else if ctx.Err() != nil {
		return snap, ctx.Err()
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
	// Every open issue, not only the workable ones: a PR opened before its issue
	// was labelled `proposed` or grew sub-issues is still a PR waiting to be
	// merged, and dropping it here would take the one line telling the operator
	// so out of the report.
	open := q.open()
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

func renderStatus(w io.Writer, rpt report, cfg config, snap statusSnapshot) {
	fmt.Fprintf(w, "%s\n", rpt.bold(fmt.Sprintf("%s%s", cfg.repo, statusScope(cfg))))
	printPairs(w, rpt, "", queuePairs(snap))
	printStatusPRs(w, rpt, snap)
	printPlanDocs(w, rpt, snap.plans)
	if line := statusPlanLine(snap); line != "" {
		fmt.Fprintf(w, "%s\n", line)
	}
	if line := needsYou(snap); line != "" {
		fmt.Fprintf(w, "\n%s\n", rpt.bold(line))
	}
}

// statusPlanLine is the same row usageLine builds for work's startup
// banner, reused rather than re-derived: the "second renderer, not a second
// pipeline" rule this file already applies to statusDocFrom, held to a
// single fact source here too.
func statusPlanLine(snap statusSnapshot) string {
	if snap.usage == nil {
		return ""
	}
	return usageLine(*snap.usage)
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
	if len(q.open()) == 0 {
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
	// The curation gate and the containers, said here rather than left to be
	// inferred from an issue's absence: a batch of proposals nobody has looked at
	// is exactly the thing a snapshot exists to surface.
	if len(q.proposed) > 0 {
		pairs = append(pairs, [2]string{"proposed",
			fmt.Sprintf("%s — %s, labelled %s", plural(len(q.proposed), "issue"),
				issueRefs(q.proposed), proposedLabel)})
	}
	if len(q.containers) > 0 {
		pairs = append(pairs, [2]string{"containers",
			fmt.Sprintf("%s — %s", plural(len(q.containers), "issue"), containerRefs(q.containers))})
	}
	// Last, because it is the answer the rest of the table is context for.
	pairs = append(pairs, [2]string{"next", nextLine(snap)})
	return pairs
}

// containerRefs renders each container with its sub-issue rollup, so the
// containers row tells a finished epic — every child closed — from one still in
// progress, and a finished one polako will close on its next pass from one a
// human has held open with needs-human or proposed.
func containerRefs(containers []containerInfo) string {
	refs := make([]string, len(containers))
	for i, c := range containers {
		ref := fmt.Sprintf("#%d (%d/%d closed", c.number, c.completed, c.total)
		switch {
		case c.finished() && c.held:
			ref += " — yours to close"
		case c.finished():
			ref += " — the next shift closes it"
		}
		refs[i] = ref + ")"
	}
	return strings.Join(refs, ", ")
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
		// Which exclusion is holding the backlog matters, because each one is
		// released differently — and naming the wrong one sends an operator to
		// take off a label the issues do not carry.
		var held []string
		if len(snap.queues.parked) > 0 {
			held = append(held, "parked")
		}
		if len(snap.queues.proposed) > 0 {
			held = append(held, "awaiting curation")
		}
		if len(snap.queues.containers) > 0 {
			held = append(held, "a tracking container")
		}
		if len(held) == 0 {
			return "nothing — no open issue at all"
		}
		return "nothing — every open issue is " + strings.Join(held, " or ")
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

func printStatusPRs(w io.Writer, rpt report, snap statusSnapshot) {
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
	printTable(w, rpt, "open prs on issue branches", header, rows, len(header))
	if len(snap.undetailed) > 0 {
		fmt.Fprintf(w, "  (%s past the first %d, listed without state: %s)\n",
			plural(len(snap.undetailed), "PR"), statusPRs, issueRefs(snap.undetailed))
	}
}

// printPlanDocs prints the docs/plans/ section built by readPlanDocs
// (plans.go): one row per document, its derived state, its container
// issues, and how many of those containers' children are still open. Absent
// when there is nothing to say — no local docs/plans and no gone footer
// either — the same "no line on a healthy backlog" rule needsYou follows.
func printPlanDocs(w io.Writer, rpt report, plans planDocsSnapshot) {
	if len(plans.docs) == 0 && len(plans.gone) == 0 {
		return
	}
	if len(plans.docs) > 0 {
		rows := make([][]string, 0, len(plans.docs))
		for _, d := range plans.docs {
			rows = append(rows, []string{d.path, string(d.state), planContainersCell(d), planOpenChildrenCell(d)})
		}
		header := []string{"doc", "state", "containers", "open children"}
		printTable(w, rpt, "plan documents", header, rows, len(header))
	} else {
		fmt.Fprintf(w, "\n%s\n", rpt.bold("plan documents"))
	}
	if len(plans.gone) > 0 {
		refs := make([]string, len(plans.gone))
		for i, g := range plans.gone {
			refs[i] = fmt.Sprintf("%s (%s)", g.path, issueRefs(g.issues))
		}
		fmt.Fprintf(w, "  (gone — footer names a document no longer on disk: %s)\n", strings.Join(refs, ", "))
	}
	if plans.truncated {
		fmt.Fprintf(w, "  (past the first %d issues carrying the plan footer — state above may be incomplete)\n",
			planDocsLimit)
	}
}

func planContainersCell(d planDocStatus) string {
	if len(d.containers) == 0 {
		return "—"
	}
	return containerRefs(d.containers)
}

func planOpenChildrenCell(d planDocStatus) string {
	if len(d.containers) == 0 {
		return "—"
	}
	return strconv.Itoa(d.openChildren)
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
	parts := needsYouParts(snap)
	if len(parts) == 0 {
		return ""
	}
	return "needs you: " + strings.Join(parts, "; ")
}

// needsYouParts is needsYou's derivation on its own, one clause per item, so
// the JSON renderer can carry the same list structured rather than joined
// into prose — the "second renderer, not a second pipeline" rule applied to
// this line specifically.
func needsYouParts(snap statusSnapshot) []string {
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
	// Curation is a person's job by construction — nothing else takes the label
	// off — so a backlog of proposals is one of the things only a person moves.
	if len(snap.queues.proposed) > 0 {
		parts = append(parts, fmt.Sprintf("curate %s (drop %s to queue them)",
			issueRefs(snap.queues.proposed), proposedLabel))
	}
	// A finished container polako would close itself, so it is not yours — but
	// one a human has held with needs-human or proposed it will not touch, and
	// that one is now the operator's to close.
	var heldEpics []int
	for _, c := range snap.queues.containers {
		if c.finished() && c.held {
			heldEpics = append(heldEpics, c.number)
		}
	}
	if len(heldEpics) > 0 {
		parts = append(parts, fmt.Sprintf("close %s (every sub-issue closed; held open by %s or %s)",
			issueRefs(heldEpics), needsHumanLabel, proposedLabel))
	}
	return parts
}

// --- JSON rendering ---
//
// A second renderer over statusSnapshot, exactly as buildHTMLReport
// (statshtml.go) is a second renderer over a stats dataset: every fact below
// is read out of the same snapshot the text report walks, or computed by the
// same helper (nextLine, needsYouParts, the PR cell functions) the text
// report calls — never re-derived. The two can disagree about layout; they
// cannot disagree about a fact, because there is only one place each fact is
// computed.

// statusDoc is the whole answer to `polako status -json`, in explicit typed
// fields rather than map[string]any: a schema that is reviewable, per the
// acceptance criteria on #118. Reviewable does not mean frozen — #168 widened
// `queue.containers` from bare issue numbers to objects once the sub-issue
// rollup's completed count was worth reporting, and said so in docs/reference.md.
type statusDoc struct {
	Repo          string         `json:"repo"`
	Scope         statusDocScope `json:"scope"`
	Queue         statusDocQueue `json:"queue"`
	Next          statusDocNext  `json:"next"`
	PRs           []statusDocPR  `json:"prs"`
	UndetailedPRs []int          `json:"undetailed_prs"`
	NeedsYou      []string       `json:"needs_you"`
	Plans         statusDocPlans `json:"plans"`
	// Plan is the same line the text report prints, or nil when the usage
	// probe could not answer — never an empty string standing in for "no
	// usage", which would be indistinguishable from a genuine 0%.
	Plan *string `json:"plan,omitempty"`
}

type statusDocScope struct {
	Label       string `json:"label"`
	StrictOrder bool   `json:"strict_order"`
}

type statusDocQueue struct {
	Ready      []int                `json:"ready"`
	Blocked    []statusDocBlocked   `json:"blocked"`
	Parked     []int                `json:"parked"`
	Proposed   []int                `json:"proposed"`
	Containers []statusDocContainer `json:"containers"`
}

// statusDocContainer is one container issue with its sub-issue rollup, so a
// caller can tell #113 (6 of 6 closed) from #147 (1 of 5) without a second
// call — the same widening `blocked` already carries for quiet_seconds.
// Finished is containerInfo.finished() verbatim: the one place that decides
// "done" is a Go method, and repeating its comparison in every jq script that
// reads this document would be exactly the "children invent their own"
// outcome that method exists to prevent.
type statusDocContainer struct {
	Issue     int  `json:"issue"`
	Total     int  `json:"total"`
	Completed int  `json:"completed"`
	Finished  bool `json:"finished"`
	// Held is containerInfo.held: a human has put needs-human or proposed on the
	// container, so the drain leaves it alone rather than closing it once
	// finished. Without this a caller cannot tell a finished container that is
	// about to be closed from one it must close itself.
	Held bool `json:"held"`
}

// statusDocBlocked is one issue awaiting an answer. QuietSeconds is a pointer
// because 0 (a reply just landed) and "the thread's age could not be read"
// are both real, distinct states — the same distinction unknownCell exists to
// preserve for a PR's fields, applied here to a duration instead of a string.
type statusDocBlocked struct {
	Issue        int    `json:"issue"`
	QuietSeconds *int64 `json:"quiet_seconds,omitempty"`
}

// statusDocNext is the issue a drain starting now would pick up, and why.
// Issue is 0 for none; Reason is nextLine(snap) verbatim, which already
// covers the 0 case in words.
type statusDocNext struct {
	Issue  int    `json:"issue"`
	Reason string `json:"reason"`
}

// statusDocPR mirrors the text report's table columns. Mergeable, Checks and
// Review are the same cell strings the table prints, unknownCell ("not read")
// included: reusing them rather than inventing a JSON-specific sentinel means
// there is exactly one meaning of "not read", not two.
type statusDocPR struct {
	Number    int    `json:"number"`
	Branch    string `json:"branch"`
	Issue     int    `json:"issue"`
	URL       string `json:"url"`
	Mergeable string `json:"mergeable"`
	Checks    string `json:"checks"`
	Review    string `json:"review"`
}

// statusDocPlans mirrors planDocsSnapshot (plans.go) field for field: Docs is
// the text report's table rows, Gone the same footers-with-no-file the
// "gone" note lists, Truncated the same warning the note prints.
type statusDocPlans struct {
	Docs      []statusDocPlan `json:"docs"`
	Gone      []statusDocGone `json:"gone"`
	Truncated bool            `json:"truncated"`
}

// statusDocPlan is one document's line: State is planDocState's string form
// ("draft", "proposed", "active", "done"); Containers reuses
// statusDocContainer, the same shape the queue's own containers carry, so a
// caller reads a container issue's rollup the same way in both places.
type statusDocPlan struct {
	Path         string               `json:"path"`
	State        string               `json:"state"`
	Containers   []statusDocContainer `json:"containers"`
	OpenChildren int                  `json:"open_children"`
}

// statusDocGone is a footer naming a document with no matching file on disk,
// and the issue numbers whose footer names it.
type statusDocGone struct {
	Path   string `json:"path"`
	Issues []int  `json:"issues"`
}

// renderStatusJSON writes statusDoc as the whole of stdout: one document, no
// header, no trailing prose, so `polako status -json | jq` works.
func renderStatusJSON(w io.Writer, cfg config, snap statusSnapshot) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(statusDocFrom(cfg, snap)); err != nil {
		return fmt.Errorf("could not encode the status report as JSON (%w) — this is a bug in polako; "+
			"dropping -json still gets you the text report", err)
	}
	return nil
}

func statusDocFrom(cfg config, snap statusSnapshot) statusDoc {
	blocked := make([]statusDocBlocked, 0, len(snap.queues.blocked))
	for _, issue := range snap.queues.blocked {
		b := statusDocBlocked{Issue: issue}
		if d, ok := snap.quiet[issue]; ok {
			secs := int64(d.Seconds())
			b.QuietSeconds = &secs
		}
		blocked = append(blocked, b)
	}

	containers := make([]statusDocContainer, 0, len(snap.queues.containers))
	for _, c := range snap.queues.containers {
		containers = append(containers, statusDocContainer{
			Issue: c.number, Total: c.total, Completed: c.completed, Finished: c.finished(), Held: c.held,
		})
	}

	prs := make([]statusDocPR, 0, len(snap.prs))
	for _, p := range snap.prs {
		prs = append(prs, statusDocPR{
			Number:    p.number,
			Branch:    p.branch,
			Issue:     p.issue,
			URL:       p.url,
			Mergeable: mergeableCell(p),
			Checks:    checksCell(p),
			Review:    reviewCell(p),
		})
	}

	planDocs := make([]statusDocPlan, 0, len(snap.plans.docs))
	for _, d := range snap.plans.docs {
		dcontainers := make([]statusDocContainer, 0, len(d.containers))
		for _, c := range d.containers {
			dcontainers = append(dcontainers, statusDocContainer{
				Issue: c.number, Total: c.total, Completed: c.completed, Finished: c.finished(), Held: c.held,
			})
		}
		planDocs = append(planDocs, statusDocPlan{
			Path: d.path, State: string(d.state), Containers: nonNilSlice(dcontainers), OpenChildren: d.openChildren,
		})
	}
	gone := make([]statusDocGone, 0, len(snap.plans.gone))
	for _, g := range snap.plans.gone {
		gone = append(gone, statusDocGone{Path: g.path, Issues: nonNilSlice(g.issues)})
	}

	doc := statusDoc{
		Repo:  cfg.repo,
		Scope: statusDocScope{Label: cfg.label, StrictOrder: cfg.strictOrder},
		Queue: statusDocQueue{
			Ready:      nonNilSlice(snap.queues.ready),
			Blocked:    blocked,
			Parked:     nonNilSlice(snap.queues.parked),
			Proposed:   nonNilSlice(snap.queues.proposed),
			Containers: nonNilSlice(containers),
		},
		Next:          statusDocNext{Issue: snap.next, Reason: nextLine(snap)},
		PRs:           prs,
		UndetailedPRs: nonNilSlice(snap.undetailed),
		NeedsYou:      nonNilSlice(needsYouParts(snap)),
		Plans: statusDocPlans{
			Docs: nonNilSlice(planDocs), Gone: nonNilSlice(gone), Truncated: snap.plans.truncated,
		},
	}
	if line := statusPlanLine(snap); line != "" {
		doc.Plan = &line
	}
	return doc
}

// nonNilSlice keeps every array field a `[]`, never a JSON `null`, so a
// script can `.[]` into any of them without special-casing the empty case.
func nonNilSlice[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
