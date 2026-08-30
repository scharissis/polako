package main

// `polako plan` runs the plan-backlog skill unattended, the way `polako work`
// runs implement-issue: point it at a vision document (or an inline -brief) and
// it proposes a curated backlog — epics and one-PR issues — behind the
// `proposed` label a human lifts to queue the work.
//
// The run is one claude invocation through execClaude — the same entry `work`
// uses — bracketed by two enforcement mechanisms that make the curation gate
// structural rather than a thing the model has to remember:
//
//   - the cap. dispatchClaude counts `gh issue create` tool calls and kills the
//     run at -max-issues, the way it kills a stalled one.
//   - the label pass. runPlan snapshots the open backlog before the run and,
//     after it — always, even on a crash, a cap kill or a Ctrl+C — normalises
//     every issue this account created since to carry *exactly* the `proposed`
//     label, and attaches the batch milestone to any the skill missed. A
//     failure to label is reported loudly, never swallowed.
//
// When it ends, whatever its status, it leaves the two traces every other run
// leaves: one `kind:"plan"` run-data record (metrics.go), and — when it
// created proposals — one `proposed` notification (notify.go), the same quiet
// "the tool did the right thing and nobody is watching" moment `-notify`
// exists for.

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// planSkillDir is the intake-side skill under skills/, the twin of skillDir.
// It lived in repo_test.go while the binary had no verb to run it; the verb
// is here now, so the constant is too.
const planSkillDir = "plan-backlog"

// defaultPlanSkill is the -skill default: the plugin-namespaced slash command,
// the same shape defaultSkill has for the drain.
const defaultPlanSkill = "polako:" + planSkillDir

// The `proposed` label is declared at plan preflight and nowhere else — the
// drain only ever excludes it. GitHub refuses to apply a label the repository
// never defined, and the headless plan run holds no grant that could create
// one, so the supervisor mints it up front exactly as it mints awaiting-answer.
const (
	planProposedColor = "1D76DB"
	planProposedDesc  = "proposed by polako plan — a human removes this label to queue it"
)

// planTools is the --allowedTools for an unattended plan run: a fraction of the
// drain's defaultTools. A plan run reads a vision document and an entire open
// backlog — attacker-editable on any repo that takes outside issues — and its
// whole write surface is creating labelled proposals. Repo reads, GitHub issue
// reads, Write for the scratch body file, and `gh issue create`. Nothing that
// commits, pushes, opens or merges a PR, edits a thread, or reaches `gh api`;
// the milestone `gh api` needs is the supervisor's to run, never the run's.
const planTools = "Bash(git log:*),Bash(git show:*),Bash(git status:*),Bash(git branch:*)," +
	"Bash(gh issue list:*),Bash(gh issue view:*),Bash(gh search issues:*),Bash(gh issue create:*)," +
	"Read,Glob,Grep,TodoWrite,Write"

// planBriefMax is where -h stops taking a -brief and starts advising a file: a
// vision past a couple of thousand characters is a document, and belongs in one
// so the provenance footer can carry its sha.
const planBriefMax = 2000

type planOptions struct {
	vision         string
	brief          string
	focus          string
	milestone      string
	maxIssues      int
	dir            string
	claudeBin      string
	skill          string
	model          string
	permissionMode string
	tools          string
	addTools       string
	stall          time.Duration
	maxCost        float64
	metrics        string
	tag            string
	notifyCmd      string
	dryRun         bool
}

// runPlan is the `plan` subcommand: parse its own flags, preflight, then either
// print the invocation a run would make (-dry-run) or make it — snapshot the
// backlog, spawn the skill through execClaude, and run the label pass over
// whatever it created. Its config is built the lightweight way status's and
// tidy's are, not work's heavier preflight.
func runPlan(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("plan", flag.ContinueOnError)
	fs.SetOutput(out)
	var opt planOptions
	fs.StringVar(&opt.vision, "vision", "", "path (under -dir) to the vision or roadmap document to plan from")
	fs.StringVar(&opt.brief, "brief", "", "inline vision text, in place of -vision — exactly one of the two is required")
	fs.StringVar(&opt.focus, "focus", "", "free-text steer for the run, e.g. \"only the observability section\"")
	fs.StringVar(&opt.milestone, "milestone", "",
		"batch milestone title, or \"off\" to skip it (default: the vision file's name, or the brief's first words)")
	fs.IntVar(&opt.maxIssues, "max-issues", 10, "ceiling on the issues a run may create, epics included")
	fs.StringVar(&opt.dir, "dir", ".", "path to the repository's main checkout")
	fs.StringVar(&opt.claudeBin, "claude", "claude", "claude binary to invoke")
	fs.StringVar(&opt.skill, "skill", defaultPlanSkill, "skill to run")
	fs.StringVar(&opt.model, "model", "opus",
		"claude --model — an alias, not a pinned id: a plan run happens once per batch and steers every run downstream")
	fs.StringVar(&opt.permissionMode, "permission-mode", "acceptEdits", "claude --permission-mode")
	fs.StringVar(&opt.tools, "tools", planTools, "comma-separated --allowedTools for the run (replaces the default plan set)")
	fs.StringVar(&opt.addTools, "add-tools", "", "extra --allowedTools entries, appended to -tools instead of replacing it")
	fs.DurationVar(&opt.stall, "stall", 15*time.Minute, "kill a run with no output events for this long (0 disables)")
	fs.Float64Var(&opt.maxCost, "max-cost", 0, "warn once the run has cost this many dollars (0 disables; advisory — a one-shot run has no next run to bound)")
	fs.StringVar(&opt.metrics, "metrics", "", `directory for the run-data record, or "off" (default ~/.polako/metrics)`)
	fs.StringVar(&opt.tag, "run-tag", "", "label recorded with the run, for comparing one batch against another")
	fs.StringVar(&opt.notifyCmd, "notify", "",
		"command run when a plan run finishes with proposals to curate (see docs/reference.md)")
	fs.BoolVar(&opt.dryRun, "dry-run", false,
		"resolve the document, print the claude invocation the run would get, and exit without running or writing anything")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "Usage: polako plan (-vision <doc> | -brief \"<text>\") [flags]\n\n"+
			"Propose a curated backlog from a vision document: run the plan-backlog skill\n"+
			"unattended, filing epics and one-PR issues behind the `proposed` label a human\n"+
			"lifts to queue the work. Every issue the run creates is normalised to carry\n"+
			"exactly that label before this exits, and the run is capped at -max-issues.\n\n"+
			"`polako plan -dry-run` prints the invocation a run would make and touches nothing.\n\n"+envUsage+"\nFlags:\n")
		fs.PrintDefaults()
	}
	if err := applyEnvDefaults(fs); err != nil {
		return err
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return errFlagsReported
	}
	if rest := fs.Args(); len(rest) > 0 {
		return fmt.Errorf("unexpected argument %q — plan takes flags only", rest[0])
	}

	cfg, err := planConfig(&opt)
	if err != nil {
		return err
	}
	milestone, hierarchical, err := planPreflight(ctx, &cfg, &opt)
	if err != nil {
		return err
	}
	if opt.dryRun {
		return planDryRun(cfg, opt, milestone, hierarchical, out)
	}
	return planRun(ctx, cfg, opt, milestone, out)
}

// planRun is a real `polako plan`: snapshot the open backlog, spawn the skill
// through execClaude, then — whatever the run's own outcome — normalise every
// issue this account created since. The snapshot has to be taken here rather
// than inside planNormalise, before the run rather than after, so an issue a
// person files by hand mid-run is told apart from one the skill created.
func planRun(ctx context.Context, cfg config, opt planOptions, milestone string, out io.Writer) error {
	before, err := planOpenIssues(ctx, cfg)
	if err != nil {
		return fmt.Errorf("could not read the backlog before the run (is gh authenticated for %s?): %w",
			cfg.repo, err)
	}

	prompt := planPrompt(cfg, opt)
	log.Printf("running %s from %s — capped at %s", cfg.skill, planPromptLabel(opt), plural(opt.maxIssues, "issue"))
	started := time.Now()
	rep, runErr := execClaude(ctx, cfg, prompt, "", cfg.skill, 0)
	// Timed here, before the label pass, so the record's wall time is the run's
	// own — the same boundary the drain draws around runClaude. The pass that
	// follows is supervisor overhead and can spend up to two minutes on gh.
	ended := time.Now()

	// The label pass runs no matter how the run ended — a clean finish, the cap
	// kill, a crash, a Ctrl+C. The curation gate is the whole point of the verb,
	// and an issue the run filed but this process never labelled is an
	// unguarded proposal an unattended drain would pick up. So it always gets
	// its own context, detached from the parent's cancellation and bounded on
	// its own — a shutdown signal that lands mid-pass must not be what strands
	// a proposal unlabelled.
	passCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancel()
	if ctx.Err() != nil {
		narrate(sevWarning, "the run was interrupted — still normalising anything it created before exiting")
	}
	pass := planNormalise(passCtx, cfg, before, milestone)
	pass.report(opt, rep)

	// The pricing line: what the operator's own history says this batch will
	// cost to implement. After the label pass, because it prices the proposals
	// the pass just counted; only when there are proposals, because a batch of
	// nothing has nothing to price and nothing to curate.
	if pass.created > 0 {
		narrate(sevProgress, "plan: %s", planPricingLine(cfg.rec.metricsDir(), cfg.repo, pass.created, time.Now()))
	}

	// The two traces the run leaves, both after the label pass so they carry
	// what it found: one record whatever the run's status, and — only when the
	// run actually proposed something — one notification. Both detached from
	// the parent context for the same reason the pass is: a Ctrl+C during the
	// run must not be what swallows the record of it or the note that a backlog
	// is now waiting to be curated.
	cfg.rec.recordPlan(cfg, rep, planFacts{
		vision:         planVisionField(opt),
		milestone:      milestone,
		issuesCreated:  pass.created,
		epicsCreated:   pass.epics,
		cap:            opt.maxIssues,
		labelsEnforced: pass.labelsEnforced(),
		started:        started,
		ended:          ended,
	})
	if pass.created > 0 {
		notify(context.WithoutCancel(ctx), cfg, notification{
			event: notifyProposed, reason: proposedNotifyReason(pass.created, pass.epics)})
	}

	// Order of the exit conditions: a failed label pass is the loudest, because
	// it means a proposal may be sitting unguarded. Then an interrupt — the
	// operator's own doing, reported as itself and not as a crash. Then the
	// run's own failure.
	if perr := pass.err(); perr != nil {
		return perr
	}
	if ctx.Err() != nil {
		// dispatchClaude returns the raw "signal: killed" wait error on a
		// context kill, not context.Canceled, so the interrupt has to be read
		// from the context itself — the same check processIssue makes.
		return ctx.Err()
	}
	switch {
	case runErr == nil:
		return nil
	case errors.Is(runErr, errPlanCap):
		// Not an error to the operator: the cap is a setting they chose, the
		// run did what it was told, and the pass above already normalised
		// everything. Reported, not raised.
		return nil
	case errors.Is(runErr, errNoWork):
		return fmt.Errorf("%w — check that -skill %q names a skill this installation has; "+
			"plugin skills are namespaced <plugin>:<skill>", runErr, cfg.skill)
	case errors.Is(runErr, errAuth):
		return authAdvice(runErr)
	default:
		return fmt.Errorf("the plan run did not finish cleanly: %w — any %s it did create are labelled; "+
			"rerun `polako plan` to continue, the skill deduplicates from GitHub", runErr, proposedLabel)
	}
}

// planPromptLabel names what a run is planning from, for the one log line that
// announces it — the document path, or that it is an inline brief.
func planPromptLabel(opt planOptions) string {
	if opt.vision != "" {
		return opt.vision
	}
	return "an inline brief"
}

// planVisionField is what the record's `vision` carries: the -vision path the
// operator typed, or the literal "(brief)" for an inline one. A path is an
// operator-chosen string and fair game; a brief can run to two thousand
// characters of roadmap prose, which is document content and has no place in a
// record — the standing recorder rule. (The batch `milestone` beside it is a
// bounded identifier, the name of a real GitHub object the run attaches to
// issues, so it is recorded as typed or derived.)
func planVisionField(opt planOptions) string {
	if opt.vision != "" {
		return opt.vision
	}
	return "(brief)"
}

// proposedNotifyReason is the one line of English the `proposed` hook receives:
// how many proposals await curation, how many are epics, and the one move that
// queues them. Numbers only, like every notification reason.
func proposedNotifyReason(created, epics int) string {
	verb := "await"
	if created == 1 {
		verb = "awaits"
	}
	s := plural(created, "proposal")
	if epics > 0 {
		s += fmt.Sprintf(" (%s)", plural(epics, "epic"))
	}
	return fmt.Sprintf("%s %s curation — remove the %s label to queue them", s, verb, proposedLabel)
}

// planOpenIssues is the set of open issue numbers before the run — the baseline
// the label pass diffs against, so an issue a person files by hand while the
// run is going is told apart from one the skill created. Open issues only: a
// proposal is created open, and a closed one is nothing an unattended drain
// would ever pick up.
func planOpenIssues(ctx context.Context, cfg config) (map[int]bool, error) {
	raw, err := gh(ctx, cfg, "issue", "list", "--state", "open", "--limit", "1000", "--json", "number")
	if err != nil {
		return nil, err
	}
	var rows []struct {
		Number int `json:"number"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("unreadable issue list: %w", err)
	}
	seen := make(map[int]bool, len(rows))
	for _, r := range rows {
		seen[r.Number] = true
	}
	return seen, nil
}

// planPassOutcome is what the enforcing label pass did — reported once, and
// turned into planRun's exit status. failures is the load-bearing field: an
// action that did not take means a proposal may be sitting unguarded, so it is
// collected here rather than swallowed and it makes `polako plan` exit nonzero.
type planPassOutcome struct {
	created   int      // new issues this account was found to have filed
	epics     int      // of those, the ones that are containers (sub-issues > 0)
	labelled  []int    // issues confirmed to carry exactly proposedLabel afterwards
	added     int      // missing proposedLabel labels the pass applied
	stripped  int      // stray labels removed, across all of them
	milestone []int    // issues the batch milestone was newly attached to
	failures  []string // one line per action that did not take — loud
	listErr   error    // the after-listing itself failed: nothing could be checked
}

// labelsEnforced is how many label edits the pass had to make — adds plus
// strips — which is the measure of how far the run fell short of self-applying
// the curation gate, and the number the `plan` record carries for it.
func (o planPassOutcome) labelsEnforced() int { return o.added + o.stripped }

// planNormalise is the enforcing label pass. It lists the issues this gh
// account has open, keeps the ones absent from `before` and numbered above
// everything that was there — the run's own output — and forces each to carry
// *exactly* proposedLabel, attaching the batch milestone to any that has none.
// This is what keeps the `-label` queue-gate humans-only: `Bash(gh issue
// create:*)` is a prefix and no prefix can say "create, but not with that
// flag", so the create stays wide and the cleanup happens here.
//
// The `> maxBefore` guard is what makes the truncation of either listing safe:
// GitHub issue numbers only ever climb, so anything the run filed outnumbers
// everything open before it, and an old issue this account filed that fell off
// the end of the `before` page is never mistaken for the run's own.
func planNormalise(ctx context.Context, cfg config, before map[int]bool, milestone string) planPassOutcome {
	var out planPassOutcome
	maxBefore := 0
	for n := range before {
		if n > maxBefore {
			maxBefore = n
		}
	}
	// subIssuesSummary rides along so the record can say how many of the run's
	// own issues are epics. A gh too old for the field rejects the whole call
	// before it asks GitHub anything, so fall back to the listing without it —
	// epics_created then reads 0, the same degradation the drain's container
	// skip takes, and a flat run has no epics to miss anyway.
	fields := "number,labels,milestone,subIssuesSummary"
	raw, err := gh(ctx, cfg, "issue", "list", "--author", "@me", "--state", "open",
		"--limit", "1000", "--json", fields)
	if unknownJSONField(err) {
		fields = "number,labels,milestone"
		raw, err = gh(ctx, cfg, "issue", "list", "--author", "@me", "--state", "open",
			"--limit", "1000", "--json", fields)
	}
	if err != nil {
		out.listErr = err
		return out
	}
	var rows []struct {
		Number int `json:"number"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
		Milestone *struct {
			Title string `json:"title"`
		} `json:"milestone"`
		SubIssues struct {
			Total int `json:"total"`
		} `json:"subIssuesSummary"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		out.listErr = fmt.Errorf("unreadable issue list: %w", err)
		return out
	}

	for _, r := range rows {
		if before[r.Number] || r.Number <= maxBefore {
			continue // there before the run — not ours to touch
		}
		out.created++
		if r.SubIssues.Total > 0 {
			out.epics++
		}
		n := strconv.Itoa(r.Number)
		clean := true

		hasProposed := false
		for _, l := range r.Labels {
			if l.Name == proposedLabel {
				hasProposed = true
				continue
			}
			if _, err := gh(ctx, cfg, "issue", "edit", n, "--remove-label", l.Name); err != nil {
				out.failures = append(out.failures,
					fmt.Sprintf("could not strip %q from #%d: %v", l.Name, r.Number, err))
				clean = false
				continue
			}
			log.Printf("plan: stripped %q from #%d — a proposal carries only %s", l.Name, r.Number, proposedLabel)
			out.stripped++
		}
		if !hasProposed {
			if _, err := gh(ctx, cfg, "issue", "edit", n, "--add-label", proposedLabel); err != nil {
				out.failures = append(out.failures,
					fmt.Sprintf("could not add %s to #%d: %v", proposedLabel, r.Number, err))
				clean = false
			} else {
				log.Printf("plan: labelled #%d %s", r.Number, proposedLabel)
				out.added++
			}
		}
		if clean {
			out.labelled = append(out.labelled, r.Number)
		}

		if milestone != "" && (r.Milestone == nil || strings.TrimSpace(r.Milestone.Title) == "") {
			if _, err := gh(ctx, cfg, "issue", "edit", n, "--milestone", milestone); err != nil {
				out.failures = append(out.failures,
					fmt.Sprintf("could not attach the %q milestone to #%d: %v", milestone, r.Number, err))
			} else {
				log.Printf("plan: attached the %q milestone to #%d", milestone, r.Number)
				out.milestone = append(out.milestone, r.Number)
			}
		}
	}
	return out
}

// report says what the pass did, at the severity the outcome earns: an error
// when it could not even list what to check, a warning when the run was capped
// or overspent, success otherwise. The -max-cost check lives here because a
// one-shot run has no next run for it to bound — unlike `work`, where the same
// flag stops the drain dispatching another — so all it can do is say so.
func (o planPassOutcome) report(opt planOptions, rep runReport) {
	if o.listErr != nil {
		narrate(sevError, "plan: could not list what the run created to normalise it (%v) — "+
			"check the backlog for issues missing the %s label", o.listErr, proposedLabel)
		return
	}
	if opt.maxCost > 0 && rep.costUSD >= opt.maxCost {
		narrate(sevWarning, "plan: the run cost $%.2f, at or past the -max-cost of $%.2f", rep.costUSD, opt.maxCost)
	}
	sev := sevSuccess
	if rep.capped || len(o.failures) > 0 {
		sev = sevWarning
	}
	narrate(sev, "plan: %s", o.summary(rep))
}

func (o planPassOutcome) summary(rep runReport) string {
	if o.created == 0 {
		if rep.capped {
			return "the run was capped before it created anything"
		}
		return "the run created no issues"
	}
	s := fmt.Sprintf("%s created, %d normalised to %s",
		plural(o.created, "issue"), len(o.labelled), proposedLabel)
	if o.stripped > 0 {
		s += fmt.Sprintf(" (%s stripped)", plural(o.stripped, "stray label"))
	}
	if len(o.milestone) > 0 {
		s += fmt.Sprintf(", milestone attached to %d", len(o.milestone))
	}
	if rep.capped {
		s += " — stopped at the -max-issues cap"
	}
	if len(o.failures) > 0 {
		s += fmt.Sprintf(" — %s FAILED, see above", plural(len(o.failures), "action"))
	}
	return s
}

// err is the pass's verdict as planRun's exit status: nil unless something did
// not take, loud otherwise. A failed pass outranks a failed run in planRun,
// because an unguarded proposal is the worse outcome to leave unsaid.
func (o planPassOutcome) err() error {
	if o.listErr != nil {
		return fmt.Errorf("the label pass could not run (%w) — issues the run created may be unlabelled; "+
			"list the backlog and add the %s label to any proposal missing it", o.listErr, proposedLabel)
	}
	if len(o.failures) == 0 {
		return nil
	}
	return fmt.Errorf("the label pass left %s unapplied, so a proposal may be unguarded — "+
		"fix by hand:\n  %s", plural(len(o.failures), "issue action"), strings.Join(o.failures, "\n  "))
}

// planConfig validates the vision/brief pair and builds the config the gh
// helpers take. It never stats -vision to decide whether it was given: a
// does-the-file-exist heuristic would turn a typo'd path into a silent "no
// document" instead of a loud failure. Preflight stats it once it is settled
// that a path was the intent.
func planConfig(opt *planOptions) (config, error) {
	haveVision := strings.TrimSpace(opt.vision) != ""
	haveBrief := strings.TrimSpace(opt.brief) != ""
	switch {
	case haveVision && haveBrief:
		return config{}, errors.New("-vision and -brief are mutually exclusive — pass a document path or inline text, not both")
	case !haveVision && !haveBrief:
		return config{}, errors.New("plan needs something to plan from — pass -vision <doc> (a path under -dir) " +
			"or -brief \"<one sentence>\"")
	}
	if haveBrief && len(strings.TrimSpace(opt.brief)) > planBriefMax {
		return config{}, fmt.Errorf("-brief is %d characters — past %d that is a document: put it in a file and pass -vision",
			len(strings.TrimSpace(opt.brief)), planBriefMax)
	}
	if opt.maxIssues < 1 {
		return config{}, fmt.Errorf("-max-issues %d makes no sense — a run has to be allowed at least one issue", opt.maxIssues)
	}

	cfg := config{
		ghBin:          "gh",
		ghRetryWait:    ghRetryDelay,
		claudeBin:      opt.claudeBin,
		skill:          opt.skill,
		model:          opt.model,
		permissionMode: opt.permissionMode,
		tools:          opt.tools,
		addTools:       opt.addTools,
		stall:          opt.stall,
		maxCost:        opt.maxCost,
		maxIssues:      opt.maxIssues,
		tag:            opt.tag,
		notifyCmd:      opt.notifyCmd,
		dryRun:         opt.dryRun,
		queue:          new(queueMemo),
		// The run leaves the same two traces a drain run does. The shift id
		// stamps the record so `stats -shift` can single this batch out; the
		// recorder resolves -metrics exactly as the drain's does, and is a
		// no-op under "off" or with no home directory.
		shiftID: newShiftID(),
		rec:     newRecorder(opt.metrics),
	}
	abs, err := filepath.Abs(opt.dir)
	if err != nil {
		return cfg, fmt.Errorf("resolving -dir: %w", err)
	}
	cfg.dir = abs
	return cfg, nil
}

// planPreflight mirrors the drain's — binaries, a reachable repo, a checked
// notify command — plus what a plan run needs: the document resolves to a file
// under -dir, the `--parent` capability probe decides flat vs. hierarchical
// before a permission prompt can, and — for a real run only — the `proposed`
// label and the batch milestone are ensured. A dry run declares nothing: it
// runs the probe (a read) but creates no label and no milestone.
func planPreflight(ctx context.Context, cfg *config, opt *planOptions) (milestone string, hierarchical bool, err error) {
	for _, bin := range []string{cfg.claudeBin, cfg.ghBin, "git"} {
		if _, err := exec.LookPath(bin); err != nil {
			return "", false, fmt.Errorf("%q not found on PATH: %w — plan needs it to run the skill and read GitHub", bin, err)
		}
	}
	if err := checkNotifyCommand(cfg.notifyCmd); err != nil {
		return "", false, err
	}
	if _, err := git(ctx, *cfg, "rev-parse", "--git-dir"); err != nil {
		return "", false, fmt.Errorf("-dir %s is not a git checkout: %w", cfg.dir, err)
	}
	raw, err := gh(ctx, *cfg, "repo", "view", "--json", "nameWithOwner")
	if err != nil {
		return "", false, fmt.Errorf("no GitHub repository reachable from %s (is gh authenticated?): %w", cfg.dir, err)
	}
	var repoView struct {
		NameWithOwner string `json:"nameWithOwner"`
	}
	if err := json.Unmarshal(raw, &repoView); err != nil {
		return "", false, fmt.Errorf("unreadable `gh repo view` reply (is gh current?): %w", err)
	}
	cfg.repo, cfg.ghRepo = repoView.NameWithOwner, repoView.NameWithOwner

	if opt.vision != "" {
		full := filepath.Join(cfg.dir, opt.vision)
		info, statErr := os.Stat(full)
		if statErr != nil {
			return "", false, fmt.Errorf("-vision %s does not resolve to a file under -dir (%s): %w — "+
				"check the path, or pass -brief for an inline vision", opt.vision, cfg.dir, statErr)
		}
		if info.IsDir() {
			return "", false, fmt.Errorf("-vision %s is a directory, not a document", opt.vision)
		}
	}

	// Probed here so a dry run can report the shape a real run would take. The
	// run itself does not need telling: the skill derives flat-vs-hierarchical
	// from its own `gh issue create` error (see planPrompt).
	hierarchical = ghCreatesSubIssues(ctx, *cfg)

	milestone = planMilestoneTitle(opt)

	if !opt.dryRun {
		// The record pins which CLI and which installed skill produced the
		// run's numbers, the same two the drain's preflight reads. Best-effort:
		// a version that will not answer leaves the field empty rather than
		// stopping a plan run over telemetry.
		cfg.claudeVersion = claudeVersion(ctx, *cfg)
		cfg.pluginVersion, cfg.pluginID = pluginVersion(ctx, *cfg)

		// Best-effort like the drain's awaiting-answer declaration: an
		// "already exists" is the healthy repeat case, not a failure.
		_ = ensureLabel(ctx, *cfg, proposedLabel, planProposedColor, planProposedDesc)
		if milestone != "" {
			if err := ensureMilestone(ctx, *cfg, milestone); err != nil {
				return "", false, fmt.Errorf("could not ensure the %q milestone: %w — "+
					"pass -milestone off to skip it", milestone, err)
			}
		}
	}
	return milestone, hierarchical, nil
}

// planMilestoneTitle is the batch milestone's title: -milestone when set,
// "" when it is "off", and otherwise derived — the vision file's base name
// without its extension, or the brief's first words.
func planMilestoneTitle(opt *planOptions) string {
	if m := strings.TrimSpace(opt.milestone); m != "" {
		if strings.EqualFold(m, "off") {
			return ""
		}
		return m
	}
	if opt.vision != "" {
		base := filepath.Base(filepath.ToSlash(opt.vision))
		if ext := filepath.Ext(base); ext != "" {
			base = strings.TrimSuffix(base, ext)
		}
		return base
	}
	return firstWords(strings.TrimSpace(opt.brief), 8)
}

// firstWords is the first n whitespace-separated words of s, joined by single
// spaces — enough of a brief to name a milestone after.
func firstWords(s string, n int) string {
	fields := strings.Fields(s)
	if len(fields) > n {
		fields = fields[:n]
	}
	return strings.Join(fields, " ")
}

// ghCreatesSubIssues reports whether this gh can file a child issue in one
// command — `gh issue create --parent`. False means the run works flat, with a
// tracking issue holding the design instead of an epic.
func ghCreatesSubIssues(ctx context.Context, cfg config) bool {
	out, err := gh(ctx, cfg, "issue", "create", "--help")
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "--parent")
}

// ensureMilestone makes the batch milestone the plan run attaches issues to.
// gh has no milestone verb, so this is the one place the REST API is reached
// for — the same find-or-create shape ensureLabel has for a label. Idempotent:
// a title that already exists is left exactly as it is, never re-created and
// never edited.
func ensureMilestone(ctx context.Context, cfg config, title string) error {
	raw, err := gh(ctx, cfg, "api", "--paginate", "repos/{owner}/{repo}/milestones?state=all")
	if err != nil {
		return fmt.Errorf("listing milestones: %w", err)
	}
	var existing []struct {
		Title string `json:"title"`
	}
	if err := json.Unmarshal(raw, &existing); err != nil {
		return fmt.Errorf("unreadable milestones list: %w", err)
	}
	want := strings.TrimSpace(title)
	for _, m := range existing {
		if strings.EqualFold(strings.TrimSpace(m.Title), want) {
			return nil // already there — nothing to do
		}
	}
	if _, err := gh(ctx, cfg, "api", "repos/{owner}/{repo}/milestones", "-f", "title="+title); err != nil {
		return fmt.Errorf("creating milestone %q: %w", title, err)
	}
	return nil
}

// planDryRun narrates what a run would do — to the log, i.e. stderr — and
// prints the exact claude invocation to out, so it can be piped somewhere
// useful rather than fished out of a transcript. It declares no label, ensures
// no milestone and records nothing: pointing it at an unfamiliar repository
// leaves that repository exactly as it found it.
func planDryRun(cfg config, opt planOptions, milestone string, hierarchical bool, out io.Writer) error {
	if opt.vision != "" {
		log.Printf("planning from %s", opt.vision)
	} else {
		log.Printf("planning from an inline brief (%d characters)", len(strings.TrimSpace(opt.brief)))
	}
	if opt.focus != "" {
		log.Printf("focus: %s", opt.focus)
	}
	log.Printf("issue cap: %d, epics included", opt.maxIssues)
	if milestone == "" {
		log.Println("milestone: off")
	} else {
		log.Printf("milestone: %q — a real run would create it at preflight", milestone)
	}
	if hierarchical {
		log.Println("issue shape: hierarchical — epics with sub-issues")
	} else {
		log.Println("issue shape: flat — this gh has no `gh issue create --parent`, so a tracking issue holds the design")
	}
	log.Println("dry run — no proposed label, no milestone, no run data; the invocation follows on stdout")

	_, err := fmt.Fprintln(out, commandLine(cfg.claudeBin, planArgs(cfg, opt)))
	return err
}

// planArgs is the headless plan invocation, built through buildArgs so a dry
// run prints the exact argv planRun's real dispatch makes rather than a second
// rendering of it — the pair that drifts the first time either side changes.
func planArgs(cfg config, opt planOptions) []string {
	return buildArgs(cfg, planPrompt(cfg, opt), "")
}

// planPrompt is the -p string the skill is invoked with: the slash command and
// its two declared arguments, the document (or brief) and the optional focus.
// Just those two — the shipped plan-backlog SKILL.md declares `arguments:
// [vision, focus]` and nothing more: it derives flat-vs-hierarchical from its
// own `gh issue create` error, and the batch milestone is attached binary-side
// by the label pass, so there is nothing more for the prompt to carry.
func planPrompt(cfg config, opt planOptions) string {
	prompt := "/" + cfg.skill + " " + planSlashArg(planPromptSubject(opt))
	if opt.focus != "" {
		prompt += " " + planSlashArg(opt.focus)
	}
	return prompt
}

// planPromptSubject is the skill's first argument: the document path, or the
// brief standing in its place.
func planPromptSubject(opt planOptions) string {
	if opt.vision != "" {
		return opt.vision
	}
	return strings.TrimSpace(opt.brief)
}

// planSlashArg wraps a slash-command argument in double quotes when it carries
// whitespace, so the skill sees one argument rather than several, escaping any
// embedded quote so the wrapping is not closed early. A vision path or focus
// steer carrying a literal quote is the rare case the escaping hedges against.
func planSlashArg(s string) string {
	if !strings.ContainsAny(s, " \t\"") {
		return s
	}
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}

// planNoHistory is what the pricing line prints when history cannot price the
// batch — no records, or none that was ever priced. Fixed wording, never a
// guessed number: the estimate is history's to state or nobody's.
const planNoHistory = "no run history to price against — work a few issues and future plans will estimate themselves"

// planPricingLine is the one line the plan report prints after the label pass:
// what the operator's own run records say this batch will cost to implement —
// the median cost and median run time of a merged issue in this repository,
// times the number of proposals. It never invents a figure; with no usable
// history it says exactly that and stops.
//
// This is the second of telemetry's two readers, the one named beside `stats`
// in CLAUDE.md's write-only-telemetry invariant: human-facing rendering,
// computed after the run has ended, deciding nothing the supervisor does.
// Delete the metrics directory mid-run and the only change is this line
// falling back to planNoHistory. It reuses loadRecords + rollUpIssues — the
// torn-line, unknown-kind and latest-wins-dedupe rules `stats` already keeps
// — rather than parsing the records a second way.
func planPricingLine(metricsDir, repo string, proposals int, now time.Time) string {
	if metricsDir == "" {
		return planNoHistory // -metrics off, or no home directory: nothing to read, no file opened to find out
	}
	ds, err := loadRecords(metricsDir, statsOptions{repo: repo}, now)
	if err != nil {
		return planNoHistory
	}
	var costs []float64
	var times []time.Duration
	var priced float64
	for _, is := range rollUpIssues(ds) {
		if is.terminal == nil || is.terminal.Outcome != issueMerged || len(is.runs) == 0 {
			continue
		}
		costs = append(costs, is.cost)
		times = append(times, time.Duration(is.wallMS)*time.Millisecond)
		priced += is.cost
	}
	// priced == 0 covers both "no merged issue with runs" and "every merged
	// issue's runs crashed before reporting a cost" — the second is a real
	// record and a useless estimate, so it is treated as no history rather
	// than projecting the batch at $0.
	if priced == 0 {
		return planNoHistory
	}
	n := len(costs)
	costMedian := median(costs)
	timeMedian := median(times)
	return fmt.Sprintf("your last %s ran %s and %s median — %s ≈ %s and %s of run time, before curation cuts",
		plural(n, "merged issue"), usd(costMedian), dur(timeMedian),
		plural(proposals, "proposal"),
		approxUSD(float64(proposals)*costMedian),
		approxDur(time.Duration(proposals)*timeMedian))
}

// approxUSD renders a projected batch cost — a median times a count — at the
// resolution an estimate earns: whole dollars once it is worth more than a
// few, so "≈ $19" is not dressed up as "$18.90".
func approxUSD(f float64) string {
	if f < 10 {
		return usd(f)
	}
	return fmt.Sprintf("$%.0f", f)
}

// approxDur renders a projected batch run time in half-hour steps once it is
// worth hours, for the same reason approxUSD rounds: "4½h" is honest about a
// median-times-count projection where "4h26m" would feign precision. Under an
// hour it falls back to dur rounded to the minute.
func approxDur(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	if d < time.Hour {
		return dur(d.Round(time.Minute))
	}
	halves := (d + 15*time.Minute) / (30 * time.Minute)
	whole, half := halves/2, halves%2 == 1
	switch {
	case whole == 0:
		return "½h"
	case half:
		return fmt.Sprintf("%d½h", whole)
	default:
		return fmt.Sprintf("%dh", whole)
	}
}
