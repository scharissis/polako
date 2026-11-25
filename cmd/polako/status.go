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
//   - Reads only. Every call it makes is a read — the drain's own startup
//     reads, plus the label list and visibility when no -label is given — so
//     nothing here can move an issue, a label or a PR.
//   - Three details of run data, and nothing else from it. status is the
//     third reader of the metrics files, beside `stats` and the pricing line
//     `plan` and `health` print: the "last shift here" line
//     (statuslastshift.go), the ready row's price and the reason beside each
//     parked issue, all read after the GitHub snapshot and feeding no queue
//     row, `next` or `needs you`. The last-shift line says
//     "here" because the drain being asked about may be running on somebody
//     else's machine, which leaves no record on this one.
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
	"os"
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
	metrics      string
	design       string
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
	fs.StringVar(&opt.label, "label", "", "only count issues carrying this label, as polako work would (empty = all)")
	fs.StringVar(&opt.branchPrefix, "branch-prefix", "issue-", "branch name prefix the skill uses")
	fs.BoolVar(&opt.strictOrder, "strict-order", false,
		"report as a work run with -strict-order would: an issue awaiting an answer keeps its place in the queue")
	fs.BoolVar(&opt.json, "json", false,
		"print one JSON document to stdout instead of the text report — see docs/reference.md for the schema")
	fs.StringVar(&opt.metrics, "metrics", "",
		`directory holding the run-data records the last-shift line reads, or "off" (default ~/.polako/metrics)`)
	fs.StringVar(&opt.design, "design", "",
		"print the reconciliation table for this plan doc instead (a path under -dir, e.g. docs/VISION.md)")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "Usage: polako status [flags]\n\n"+
			"Prints where the backlog stands, derived from GitHub: the queue in the\n"+
			"order `polako work` would take it, what is waiting on you, and any open PR on\n"+
			"a branch the skill named. Reads only — nothing here changes anything,\n"+
			"and it says the same thing whether or not a shift is running. One line,\n"+
			"the last shift here, comes from this machine's run data instead.\n\n"+envUsage("REPO=owner/name")+"\nFlags:\n")
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
	metricsDir, err := statusMetricsDir(opt.metrics)
	if err != nil {
		return err
	}

	if opt.design != "" && opt.repo != "" {
		return errors.New("status -design reads the doc from a checkout — pass -dir, not -repo")
	}
	cfg, err := statusConfig(ctx, opt)
	if err != nil {
		return err
	}
	if opt.design != "" {
		return statusDesign(ctx, cfg, fs, opt, out, rpt)
	}
	// Best-effort, like every claude-CLI read this binary makes: read here so
	// both renderStatus and renderStatusJSON see the same cfg.pluginVersion the
	// update notice compares against. status carries no -skill of its own, so
	// this asks by name (pluginName) rather than manufacturing a -skill value
	// just to satisfy pluginVersion's own cfg.skill-driven lookup.
	cfg.pluginVersion, _, _ = statusPluginVersion(ctx, cfg)

	// labelFromFlag/labelFromEnv say where opt.label (now cfg.label) came
	// from, if it's set at all — flagWasSet, not applyEnvDefaults's own
	// f.Value.Set, which never touches fs.Visit's set, so a flag left at an
	// env-supplied value is not mistaken for one typed on this invocation.
	labelFromFlag := flagWasSet(fs, "label")
	labelFromEnv := !labelFromFlag && os.Getenv(envVarName("label")) != ""

	var labelSource string
	var notes []string
	cfg, labelSource, notes = resolveStatusScope(ctx, cfg, labelFromFlag, labelFromEnv)

	snap, err := readStatus(ctx, cfg, now)
	if err != nil {
		return err
	}
	snap.notes = notes
	snap.labelSource = labelSource
	// After readStatus has returned, not inside it: nothing the GitHub read
	// derives — queues, next, needs you — can see run data this way.
	snap.runData = readStatusRunData(metricsDir, cfg.repo, now)
	if opt.json {
		return renderStatusJSON(out, cfg, snap)
	}
	renderStatus(out, rpt, cfg, snap)
	return nil
}

// resolveStatusScope decides what scopes this status report and where that
// scope came from, and the notes that decision itself produces. cfg.label
// is already whatever the flag or environment set (labelFromFlag/labelFromEnv
// say which, or neither); this only widens it further, from GitHub's own
// marked gate label, and only when neither of those named one. Split out of
// runStatus so it's testable against a hand-built cfg — the same way
// statusLabelNote already is — without needing a real flag parse or a
// resolvable repository.
func resolveStatusScope(ctx context.Context, cfg config, labelFromFlag, labelFromEnv bool) (config, string, []string) {
	// cfg.label != "" as well as labelFromFlag/labelFromEnv: an explicitly
	// empty -label (or an env var interpolated empty) still trips fs.Visit,
	// but names nothing to report a source for — leaving labelSource set
	// here would print {"label":"","source":"flag"}, contradicting
	// statusDocScope's own doc comment that Source is omitted when Label is
	// "".
	labelSource := ""
	switch {
	case labelFromFlag && cfg.label != "":
		labelSource = "flag"
	case labelFromEnv && cfg.label != "":
		labelSource = "env"
	}

	var notes []string
	if cfg.label == "" {
		// No -label and no POLAKO_LABEL: the one case status tries to scope
		// itself, from the same marker setup -apply stamps. Best-effort like
		// every other probe here — a lookup gh cannot answer just leaves the
		// report unscoped, the same as no label carrying the marker at all.
		switch found, ambiguous, mErr := markedGateLabel(ctx, cfg); {
		case mErr == nil && found != "":
			cfg.label = found
			labelSource = "github"
		case mErr == nil && ambiguous:
			notes = append(notes, "note: more than one label carries setup's gate-label marker, "+
				"so status can't scope itself to one — pass -label, or see `polako setup`")
		}
	}
	switch {
	case cfg.label == "":
		// Still unscoped: the same note a real run would refuse on, on a
		// public repository — statusLabelNote's own shape, but for "no label
		// at all" rather than "-label names one the repository never
		// defined". One extra best-effort `gh repo view` read, made only
		// here — a label-scoped report never pays for it.
		if vis, vErr := repoVisibility(ctx, cfg); vErr == nil {
			if gateErr := queueGate(vis, "", false, ""); gateErr != nil {
				notes = append(notes, fmt.Sprintf("note: a real run would refuse to start here — %v", gateErr))
			}
		}
	case labelSource != "github":
		// A github-sourced label was just read off a listing, so it exists
		// by construction — asking labelExists again would be a second,
		// redundant read of the same fact.
		if note := statusLabelNote(ctx, cfg); note != "" {
			notes = append(notes, note)
		}
	}
	return cfg, labelSource, notes
}

// repoVisibility is the one extra `gh repo view` read resolveStatusScope
// makes when it is still unscoped after the marker lookup above: whether
// the repository is public, so the same queueGate wording work's own
// preflight would refuse on can appear here as a note. Best-effort like
// every other probe in this file: a read that fails just leaves the note
// out, rather than failing the whole snapshot.
func repoVisibility(ctx context.Context, cfg config) (string, error) {
	return retryRead(ctx, cfg, "repo visibility", func() (string, error) {
		out, err := gh(ctx, cfg, "repo", "view", "--json", "visibility", "--jq", ".visibility")
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(out)), nil
	})
}

// statusLabelNote calls out a -label the repository has never defined —
// the same thing preflight refuses `work` for, downgraded to a note here
// because status only ever reads: it says what a real run would refuse
// (labelGate), then carries on regardless. Returned rather than narrated —
// status prints one report, on stdout, and this note belongs under its
// header like every other one, not on stderr above it. Best-effort like the
// usage and plan-doc reads below: a lookup that fails for a real reason
// returns "" rather than failing the whole snapshot.
func statusLabelNote(ctx context.Context, cfg config) string {
	if cfg.label == "" {
		return ""
	}
	exists, err := labelExists(ctx, cfg, cfg.label)
	if err != nil {
		return ""
	}
	if err := labelGate(cfg.label, exists); err != nil {
		return fmt.Sprintf("note: a real run would refuse to start here — %v", err)
	}
	return ""
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
	// status's own queuePairs already prints a `proposed` row, so
	// sayProposals's "ignoring N proposed issue(s)" would be both redundant
	// and misleading here — it reads as "won't show these" right above where
	// the report shows them. readParkedIssues (unpark.go) marks the same memo
	// the same way, for the same reason: this verb tells the proposed count
	// its own way. The design row, likewise.
	cfg.queue.saidProposed.Store(true)
	cfg.queue.saidDesign.Store(true)
	return resolveRepoConfig(ctx, cfg, opt.dir, opt.repo, "status reads GitHub")
}

// statusPluginVersion is installedPluginVersion bounded the way probeUsage
// bounds its own claude-CLI read: status's own doc comment promises a fast,
// GitHub-only snapshot, and an unbounded `claude plugin list` call — the CLI
// blocked on a first-run prompt, say — would break that promise the way an
// unbounded gh call already can't (every gh read here goes through
// retryRead's own timeouts). Reuses usageTimeout rather than adding a
// second knob for what is, like the usage probe, a best-effort claude-CLI
// read on the same snapshot.
func statusPluginVersion(ctx context.Context, cfg config) (version, id, scope string) {
	timeout := cfg.usageTimeout
	if timeout <= 0 {
		timeout = defaultUsageProbeTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return installedPluginVersion(ctx, cfg, pluginName)
}

// --- reading ---

// statusSnapshot is the whole answer, derived and then rendered separately so
// the derivation can be tested without reading columns out of a table.
type statusSnapshot struct {
	queues issueQueues
	gate   gateSplit // what a gated report sees beyond the queues (statusgate.go)
	// next is the issue a drain starting now would pick up, or 0 for none: the
	// lowest ready one, else the lowest one waiting on an answer — which a
	// drain runs to find out whether the reply is already on the thread.
	next int
	// quiet is how long each blocked issue's thread has been silent, keyed by
	// issue. Absent for a thread whose age could not be read.
	quiet map[int]time.Duration
	// idle is how long each parked and proposed issue has gone untouched
	// (idleSpans, statusdetail.go) — from the listing, no comment read.
	idle map[int]time.Duration
	prs  []statusPR
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
	// plans is the docs/designs/ derivation (plans.go). Best-effort like usage
	// above: a failed read leaves it zero-valued rather than failing the
	// whole snapshot.
	plans planDocsSnapshot
	// published is the release polako has actually published, the same read
	// `update -check` makes — "" when the read failed or timed out, the same
	// silence updateAvailableLine keeps for the text notice it also drives.
	published string
	// selfVersion is this running binary's own polakoVersion(), read once
	// here rather than by the renderers themselves — the same "read once,
	// render from the snapshot" shape usage and plans already follow, and
	// what lets a test drive the comparison without needing a real release
	// build to run the suite from.
	selfVersion string
	// parks is every parked issue's latest park comment, keyed by issue —
	// unpark's own read (readParkListItems, unpark.go), reused rather than
	// copied. Best-effort like usage and plans below: nil when the read
	// failed, which needsYouParts and statusDocFrom both treat as "no
	// footer on any parked issue" rather than failing the whole snapshot.
	// Only ever holds parkListItem.entries and .category — never .reason,
	// which is clipped comment text and would break the "no comment text
	// reaches the terminal" rule this report holds to everywhere else.
	// .category is a fixed identifier (metrics.go), not comment text, so
	// reading it here doesn't.
	parks map[int]parkListItem
	// notes is everything status would otherwise have narrated to stderr —
	// today just statusLabelNote's — printed on stdout under the header
	// instead, so `polako status > file` carries the whole report and a
	// terminal reads it in order rather than above the header it explains.
	notes []string
	// labelSource says where cfg.label came from: "flag", "env", "github"
	// (found via markedGateLabel with no -label or POLAKO_LABEL given), or
	// "" when the report is unscoped. Set by runStatus, read by statusScope
	// (the header) and statusDocFrom (-json's scope.source).
	labelSource string
	// runData is everything here from run data rather than GitHub
	// (statuslastshift.go). Set by runStatus after readStatus returns, so
	// nothing above can depend on it.
	runData statusRunData
	// fillHint names the commands that fill the backlog (fillBacklogHint),
	// set only when no issue is open at all — a backlog with every issue
	// held already names its hold instead.
	fillHint string
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
	snap := statusSnapshot{quiet: map[int]time.Duration{}, selfVersion: polakoVersion()}

	// The drain's own listing, exclusions and all: what `status` says a drain
	// would work has to be derived the way the drain derives it, or the two
	// disagree the moment one of them learns a new exclusion.
	queues, gate, err := statusQueues(ctx, cfg)
	if err != nil {
		return snap, err
	}
	snap.queues, snap.gate = queues, gate
	snap.idle = idleSpans(queues, now)
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

	// Best-effort like the plans read below: a failed gh-viewer-login read
	// leaves parks nil, and every parked issue falls back to today's
	// batched clause rather than failing the whole snapshot.
	if len(snap.queues.parked) > 0 {
		if parks, err := readParkListItems(ctx, cfg, snap.queues.parked); err == nil {
			snap.parks = parks
		} else if ctx.Err() != nil {
			return snap, ctx.Err()
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
	if len(snap.queues.open()) == 0 && !snap.gate.open() {
		snap.fillHint = fillBacklogHint(cfg.dir, snap.plans)
	}
	// Ungated, unlike work's own readPublishedVersion: status carries no
	// -skill to gate on, and always means this project's own plugin.
	if published, ok := publishedVersionQuiet(ctx, cfg); ok {
		snap.published = published
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

// The text report (renderStatus and the rows under it) lives in
// statusrender.go, and the JSON one in statusjson.go.
