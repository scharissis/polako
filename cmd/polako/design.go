package main

// `polako design -issue N` runs the design-plan skill on one design request
// and supervises its PR to merge, the way `work` does for implement-issue:
// restart safety, parks, the question hand-off, PR supervision and the sweep
// all come from processIssue, called once. What the verb adds is only what
// drain wraps around that call — the four exits — and a preflight that
// checks the one issue it was named rather than gating a queue it doesn't
// have (docs/designs/done/design.md, ticket 5).

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
)

// designSkillDir is the skill under skills/ a design run invokes.
const designSkillDir = "design-plan"

// defaultDesignSkill is the plugin-namespaced slash command, the same shape
// defaultPlanSkill and defaultHealthSkill have.
const defaultDesignSkill = "polako:" + designSkillDir

// designTools is defaultTools plus the two backlog reads plan-backlog already
// holds: every plan document's "What exists today" cites the open backlog.
// The build and test tools stay because a design run measures what exists by
// running it. A narrowing, not a sandbox — the same caveat defaultTools has.
const designTools = defaultTools + ",Bash(gh issue list:*),Bash(gh search issues:*)"

// designModel is -model's default. One design run steers every ticket filed
// from its document, so it gets the strongest tier — the intake verbs'
// reasoning. A tier alias, never an id.
const designModel = "opus"

// designOptions are the three flags that are design's alone. Exactly one of
// issue and brief is set once parseDesignFlags returns.
type designOptions struct {
	issue int
	brief string
	wait  bool
}

// designTrailer ends every issue -brief files: it tells whoever opens the
// thread what the thread is for, since nobody wrote the issue by hand.
const designTrailer = "Filed by polako design — reply on this thread when the run asks something."

// designFlagSet registers design's flags: its own two, work's per-issue ones
// with work's help strings, and -model/-effort. The queue, session and policy
// flags are not registered — there is no queue to gate, skip or order, and
// one issue is not a session.
func designFlagSet(out io.Writer, cfg *config, opt *designOptions, local *localFlags) *flag.FlagSet {
	fs := flag.NewFlagSet("design", flag.ContinueOnError)
	fs.SetOutput(out)
	fs.IntVar(&opt.issue, "issue", 0, "the design request to work: an open issue number")
	fs.StringVar(&opt.brief, "brief", "",
		"inline request text, filed as a new design issue and then worked — exactly one of -issue / -brief is required")
	fs.BoolVar(&opt.wait, "wait", false,
		"when the run asks a question, wait on the thread for a reply instead of exiting")
	registerIssueFlags(fs, cfg, defaultDesignSkill, designTools, local)
	registerModelFlags(fs, cfg, designModel)
	fs.BoolVar(&cfg.dryRun, "dry-run", false,
		"print the claude invocation -issue would get, or the issue -brief would file, and exit without running or writing anything")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "Usage: polako design (-issue N | -brief \"<text>\") [flags]\n\n"+
			"Work one design request into a plan document: run the design-plan skill on\n"+
			"issue N unattended and wait for its PR to merge — or park it for a human.\n"+
			"-brief files that issue first, labelled design, then works it the same way.\n"+
			"A question on the thread exits 0; reply there and rerun. Nothing here merges.\n\n"+
			envUsage+"\nFlags:\n")
		fs.PrintDefaults()
	}
	return fs
}

// parseDesignFlags parses design's argv into a config ready for preflight.
// flag.ErrHelp means -h was asked for and already printed.
func parseDesignFlags(args []string, out io.Writer) (config, designOptions, error) {
	var cfg config
	var opt designOptions
	var local localFlags
	fs := designFlagSet(out, &cfg, &opt, &local)
	if err := applyEnvDefaults(fs); err != nil {
		return config{}, opt, err
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return config{}, opt, err
		}
		return config{}, opt, errFlagsReported
	}
	if rest := fs.Args(); len(rest) > 0 {
		return config{}, opt, fmt.Errorf("unexpected argument %q — design takes flags only; name the issue with -issue N", rest[0])
	}
	if err := validateDesignSource(&opt); err != nil {
		return config{}, opt, err
	}
	if err := validateEffort("-effort", cfg.effort); err != nil {
		return config{}, opt, err
	}
	cfg, err := designConfig(cfg, opt, local)
	return cfg, opt, err
}

// validateDesignSource is planConfig's either/or on -issue and -brief: exactly
// one, and a brief short enough to be a request rather than a document. It
// trims the brief in place, so the filed body never starts or ends with the
// shell's stray whitespace.
func validateDesignSource(opt *designOptions) error {
	opt.brief = strings.TrimSpace(opt.brief)
	haveIssue := opt.issue > 0
	haveBrief := opt.brief != ""
	switch {
	case haveIssue && haveBrief:
		return errors.New("-issue and -brief are mutually exclusive — pass an issue number or inline text, not both")
	case !haveIssue && !haveBrief:
		return errors.New("design needs something to design from — pass -issue N (an open issue) " +
			"or -brief \"<one sentence>\"")
	case len(opt.brief) > planBriefMax:
		return fmt.Errorf("-brief is %d characters — past %d that is a design document's worth: "+
			"put it in an issue and pass -issue", len(opt.brief), planBriefMax)
	}
	return nil
}

// designConfig pins what parseFlags pins for work, then the three things that
// make processIssue a design run.
func designConfig(cfg config, opt designOptions, local localFlags) (config, error) {
	if err := pinConfig(&cfg, local); err != nil {
		return config{}, err
	}
	// Record kinds: design-run and design-issue, which stats and the pricing
	// line skip, so a merged plan document never reads as a merged fix.
	cfg.verb = designVerb
	// Not a preference: issueRun appends `no-evidence` when this is false, and
	// design-plan declares no evidence argument. True keeps the prompt bare.
	cfg.visualEvidence = true
	// -wait rides on strictOrder because handOffQuestion is its one reader
	// inside processIssue: set, a question polls the thread instead of
	// returning deferredError. A second reader there would break this mapping.
	cfg.strictOrder = opt.wait
	return cfg, nil
}

// runDesignVerb is main's `design` arm: parse, take work's sinks — stamped,
// since a design run opens a shift log the same way — then run, exiting the
// way work does.
func runDesignVerb(args []string) {
	cfg, opt, err := parseDesignFlags(args, os.Stdout)
	switch {
	case errors.Is(err, flag.ErrHelp):
		return
	case errors.Is(err, errFlagsReported):
		os.Exit(2) // the usage is already on screen
	case err != nil:
		sinks.fatal("design: %v", err)
	}
	useWorkSinks(cfg.verbose)
	ctx, stop := signal.NotifyContext(context.Background(), shutdownSignals()...)
	defer stop()
	if err := runDesign(ctx, cfg, opt, os.Stdout); err != nil {
		exitOnRunError(err)
	}
}

// runDesign is the verb after parsing: preflight, then either the dry-run
// invocation or the run itself. A dry run from a brief ends in preflight —
// there is no issue number to print an invocation for.
func runDesign(ctx context.Context, cfg config, opt designOptions, out io.Writer) error {
	issue, err := designPreflight(ctx, &cfg, opt)
	if err != nil || issue == 0 {
		return err
	}
	if cfg.dryRun {
		return printIssueInvocation(ctx, cfg, issue, out)
	}
	return designRun(ctx, cfg, issue)
}

// designPreflight is preflightShared with no queue gate — the operator named
// the issue on the command line, or wrote it in -brief, the same opt-in
// -label stands for — then the issue -brief files, then the two labels a
// design run can write, then the one issue itself. It returns the issue the
// run works: -issue's, or the number -brief just filed. 0 with a nil error is
// a dry run from a brief, which files nothing and so has no issue to work.
func designPreflight(ctx context.Context, cfg *config, opt designOptions) (int, error) {
	if err := preflightShared(ctx, cfg, nil); err != nil {
		return 0, err
	}
	issue := opt.issue
	if opt.brief != "" {
		// Before the label declarations below: the create's own retry declares
		// the one label it needs, and a dry run declares nothing.
		if cfg.dryRun {
			cfg.logf("would file an issue titled %q, labelled %s, and work it — nothing filed on a dry run",
				designIssueTitle(opt.brief), designLabel)
			return 0, nil
		}
		n, err := fileDesignIssue(ctx, *cfg, opt.brief)
		if err != nil {
			return 0, fmt.Errorf("could not file the design issue: %w — open it by hand and pass -issue", err)
		}
		cfg.logf("filed issue #%d for the brief, labelled %s — rerun with -issue %d if this run stops early", n, designLabel, n)
		issue = n
	}
	// The issue first, so a refusal leaves the repository as it found it.
	labelled, err := checkDesignIssue(ctx, *cfg, issue)
	if err != nil {
		return 0, err
	}
	if !labelled {
		// Unlabelled but already holding a PR on its branch means work opened
		// it, and restart safety would supervise that code PR as if it were the
		// design. Every design run labels its issue before it starts, so a
		// labelled issue's PR is safe to take over.
		pr, err := prForBranch(ctx, *cfg, fmt.Sprintf("%s%d", cfg.branchPrefix, issue))
		if err != nil {
			return 0, err
		}
		if pr != nil {
			return 0, fmt.Errorf("issue #%d already has PR #%d (%s) from a run without the %s label — "+
				"that's implementation work, not a design; open a separate issue for the design",
				issue, pr.Number, pr.State, designLabel)
		}
	}
	// Declared before the run for the reason workGates gives awaiting-answer:
	// the run that applies it holds no grant that could create it. Not on a
	// dry run, which declares nothing.
	if !cfg.dryRun {
		for _, name := range []string{awaitingAnswerLabel, designLabel} {
			l := labelByName(name)
			_ = ensureLabel(ctx, *cfg, l.name, l.color, l.description)
		}
	}
	if !labelled {
		labelDesignIssue(ctx, *cfg, issue)
	}
	cfg.logf("%s — running /%s on issue #%d, polling every %s", cfg.repo, cfg.skill, issue, cfg.poll)
	settingsBlock(*cfg, designPairs(*cfg))
	return issue, nil
}

// designIssueTitle is the title -brief files under: the brief's opening
// words, cut the way plan cuts a milestone title from one.
func designIssueTitle(brief string) string {
	return "design: " + briefTitle(brief)
}

// fileDesignIssue files the request -brief carries as a design issue and
// returns its number — the one issue the binary creates without `proposed`:
// the operator authored it at the command line, and the design label already
// keeps work off it. Same shape as fileRetireIssue, retry included.
func fileDesignIssue(ctx context.Context, cfg config, brief string) (int, error) {
	title := designIssueTitle(brief)
	body := brief + "\n\n" + designTrailer
	raw, err := gh(ctx, cfg, "issue", "create", "--title", title, "--body", body, "--label", designLabel)
	if err != nil {
		// A repository that has never had a design run has no `design` label
		// yet, and GitHub refuses to attach one that doesn't exist. Declare it
		// and retry the one call that failed, as fileRetireIssue does.
		l := labelByName(designLabel)
		if cerr := ensureLabel(ctx, cfg, l.name, l.color, l.description); cerr == nil {
			raw, err = gh(ctx, cfg, "issue", "create", "--title", title, "--body", body, "--label", designLabel)
		}
	}
	if err != nil {
		return 0, err
	}
	return issueNumberFromCreateOutput(raw)
}

// designIssueView is the one read designPreflight makes of the issue.
type designIssueView struct {
	State  string `json:"state"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
	SubIssuesSummary struct {
		Total int `json:"total"`
	} `json:"subIssuesSummary"`
}

// checkDesignIssue refuses an issue no design run should touch, each with the
// move that fixes it, and reports whether it already carries the design
// label. A read only. These refuse on -dry-run too: they're facts about the
// issue, not a policy gate a preview could look past.
func checkDesignIssue(ctx context.Context, cfg config, issue int) (labelled bool, err error) {
	n := strconv.Itoa(issue)
	out, err := gh(ctx, cfg, "issue", "view", n, "--json", "state,labels,"+subIssuesField)
	if unknownJSONField(err) {
		// A gh from before sub-issues. Losing the container check costs a
		// refusal that would otherwise have fired; the other three still do.
		out, err = gh(ctx, cfg, "issue", "view", n, "--json", "state,labels")
	}
	if err != nil {
		return false, fmt.Errorf("could not read issue #%d — check it exists in %s: %w", issue, cfg.repo, err)
	}
	var v designIssueView
	if err := json.Unmarshal(out, &v); err != nil {
		return false, fmt.Errorf("unreadable `gh issue view` reply for #%d (is gh current?): %w", issue, err)
	}
	var labels []string
	for _, l := range v.Labels {
		labels = append(labels, l.Name)
	}
	switch {
	case !strings.EqualFold(v.State, "OPEN"):
		return false, fmt.Errorf("issue #%d is closed — reopen it to design it", issue)
	case v.SubIssuesSummary.Total > 0:
		return false, fmt.Errorf("issue #%d has sub-issues, so it's a container — a design request has no sub-issues; "+
			"open a separate issue for the design", issue)
	case slices.Contains(labels, needsHumanLabel):
		return false, fmt.Errorf("issue #%d is parked (%s) — `polako unpark %d` says why; "+
			"clear it with `polako unpark -apply %d` first", issue, needsHumanLabel, issue, issue)
	case slices.Contains(labels, proposedLabel):
		return false, fmt.Errorf("issue #%d still carries %s — drop %s first; exclusion beats inclusion",
			issue, proposedLabel, proposedLabel)
	}
	return slices.Contains(labels, designLabel), nil
}

// labelDesignIssue puts the design label on an issue named without it.
// Naming the issue on the command line is the human act; the label's job
// from here on is keeping work off it. A failed add isn't fatal — the design
// run itself doesn't need it.
func labelDesignIssue(ctx context.Context, cfg config, issue int) {
	if cfg.dryRun {
		cfg.logf("issue #%d has no %q label — a real run would add it", issue, designLabel)
		return
	}
	if _, err := gh(ctx, cfg, "issue", "edit", strconv.Itoa(issue), "--add-label", designLabel); err != nil {
		cfg.narrate(sevWarning, "could not add the %q label to #%d (%v) — add it by hand, or polako work may pick the issue up",
			designLabel, issue, err)
		return
	}
	cfg.logf("issue #%d: added the %q label, so polako work leaves it alone", issue, designLabel)
}

// designPairs is work's startup recap minus what doesn't apply to one named
// issue, plus -wait when it's on.
func designPairs(cfg config) [][2]string {
	var pairs [][2]string
	for _, p := range preflightPairs(cfg) {
		switch p[0] {
		case "epics":
			continue // design closes no containers
		case "shift":
			// stats skips design kinds, so its -shift pointer would name an
			// empty report — resumeHint drops it for the same reason.
			continue
		case "dry-run":
			p[1] = "printing the invocation only — no claude run, no GitHub write, no run data"
		case "notify":
			p[1] = fmt.Sprintf("on — `%s` runs when the issue parks, asks a question, or the run stops early",
				cfg.notifyCmd)
		}
		pairs = append(pairs, p)
	}
	if cfg.strictOrder {
		pairs = append(pairs, [2]string{"wait", "on — a question waits on the thread for a reply instead of exiting"})
	}
	return pairs
}

// designRun is processIssue once, wrapped in the four exits drain gives it.
// Only the fatal one returns an error; a question or a park is this verb
// finishing, not failing. The summary and the stopped notification go
// through drain's own shift.finish, so an unfinished issue is left out of
// the summary here exactly as it is there.
func designRun(ctx context.Context, cfg config, issue int) error {
	s := newShift(cfg)
	// Before the pickup, as drain does: a design issue merged by hand since
	// the last run left a worktree nothing else revisits.
	tidySweep(ctx, cfg, 0)
	cfg.narrate(sevSection, "=== issue #%d ===", issue)
	st := &issueState{}
	err := processIssue(ctx, cfg, issue, st)
	reason, parked := parkReason(err)
	if deferred, ok := deferReason(err); ok {
		// Held in states rather than results: finish reads it back through
		// stillWaiting, the same as a drain's put-down issue.
		st.awaiting, st.baseline = true, deferred.baseline
		s.states[issue] = st
		cfg.logf("issue #%d is waiting on your answer — reply on the thread, then rerun polako design -issue %d",
			issue, issue)
		err = nil
	} else if parked {
		s.results = append(s.results, spend(st, issueResult{
			issue: issue, parked: true, reason: reason, parkEntries: parkEntriesOf(err),
		}))
		parkAndMoveOn(ctx, cfg, issue, st, reason, err)
		err = nil
	} else if err != nil {
		resumeHint(cfg, issue, st)
		err = fmt.Errorf("issue #%d: %w", issue, err)
	} else {
		s.results = append(s.results, spend(st, issueResult{issue: issue, closedNoChange: st.closedNoChange}))
		designHandOff(ctx, cfg, issue, st)
	}
	return s.finish(ctx, err)
}

// designHandOff is the success line: which PR landed the document, and the
// next command. The binary doesn't know the document's path — the skill
// chose it — so the command names where to find it rather than guessing.
// Printed, never run: merging is a human gate, and chaining plan onto it
// would turn the merge into a trigger.
func designHandOff(ctx context.Context, cfg config, issue int, st *issueState) {
	if st.closedNoChange {
		cfg.logf("issue #%d was closed with no change needed — no plan document to hand off", issue)
		return
	}
	merged := "its PR merged"
	if pr, err := prForBranch(ctx, cfg, fmt.Sprintf("%s%d", cfg.branchPrefix, issue)); err == nil && pr != nil {
		merged = fmt.Sprintf("PR #%d merged", pr.Number)
	}
	cfg.logf("issue #%d: %s — `polako status` lists the new plan document as draft; "+
		"`polako plan -design <that document>` proposes its tickets", issue, merged)
}
