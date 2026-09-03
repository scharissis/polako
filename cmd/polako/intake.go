package main

// The flags and config `polako plan` and `polako health` share. Both verbs run
// one skill unattended behind the `proposed` curation gate labelpass.go
// enforces; they differ in what they plan from and whether a milestone gets
// attached, not in the 15 knobs they expose or the lightweight config they
// build from them. That common part lives here, the way the shared label pass
// lives in labelpass.go.

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os/exec"
	"path/filepath"
	"time"
)

// intakeOptions is the flag set both intake verbs register identically:
// `polako plan` wraps it with -vision/-brief/-milestone, `polako health` uses
// it as-is. Field order matches health's registration order.
type intakeOptions struct {
	focus          string
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

// healthOptions is exactly the shared set — review-health adds no flag of its
// own, so `polako health` has nothing to wrap intakeOptions with.
type healthOptions = intakeOptions

// intakeVerb is the per-verb wording and defaults registerIntakeFlags cannot
// hardcode: the two verbs describe the same flags in their own terms, and
// default -skill and -tools to their own skill and allowlist.
type intakeVerb struct {
	name          string // "plan" / "health" — the noun in -tools and -notify help
	skillDefault  string // -skill default
	toolsDefault  string // -tools default
	dirHelp       string // -dir: plan takes a checkout, health a repo to audit
	focusExample  string // -focus example steer
	modelCadence  string // -model: "happens once per batch" / "happens periodically"
	dryRunSubject string // -dry-run: "the document" / "the repository"
}

// registerIntakeFlags registers the 15 flags plan and health share on fs,
// binding them into opt. Each flag's name, default and help string is exactly
// what the two verbs registered separately before — intakeVerb carries every
// phrase that differed.
func registerIntakeFlags(fs *flag.FlagSet, opt *intakeOptions, v intakeVerb) {
	fs.StringVar(&opt.focus, "focus", "", "free-text steer for the run, e.g. \""+v.focusExample+"\"")
	fs.IntVar(&opt.maxIssues, "max-issues", 10, "ceiling on the issues a run may create, epics included")
	fs.StringVar(&opt.dir, "dir", ".", v.dirHelp)
	fs.StringVar(&opt.claudeBin, "claude", "claude", "claude binary to invoke")
	fs.StringVar(&opt.skill, "skill", v.skillDefault, "skill to run")
	fs.StringVar(&opt.model, "model", "opus",
		"claude --model — an alias, not a pinned id: a "+v.name+" run "+v.modelCadence+" and steers every run downstream")
	fs.StringVar(&opt.permissionMode, "permission-mode", "acceptEdits", "claude --permission-mode")
	fs.StringVar(&opt.tools, "tools", v.toolsDefault,
		"comma-separated --allowedTools for the run (replaces the default "+v.name+" set)")
	fs.StringVar(&opt.addTools, "add-tools", "", "extra --allowedTools entries, appended to -tools instead of replacing it")
	fs.DurationVar(&opt.stall, "stall", 15*time.Minute, "kill a run with no output events for this long (0 disables)")
	fs.Float64Var(&opt.maxCost, "max-cost", 0, "warn once the run has cost this many dollars (0 disables; advisory — a one-shot run has no next run to bound)")
	fs.StringVar(&opt.metrics, "metrics", "", `directory for the run-data record, or "off" (default ~/.polako/metrics)`)
	fs.StringVar(&opt.tag, "run-tag", "", "label recorded with the run, for comparing one batch against another")
	fs.StringVar(&opt.notifyCmd, "notify", "",
		"command run when a "+v.name+" run finishes with proposals to curate (see docs/reference.md)")
	fs.BoolVar(&opt.dryRun, "dry-run", false,
		"resolve "+v.dryRunSubject+", print the claude invocation the run would get, and exit without running or writing anything")
}

// intakeConfig builds the config the gh helpers take, the lightweight way
// status's and tidy's are built rather than work's heavier preflight. planConfig
// wraps it with its vision/brief validation; healthConfig is a thin call.
func intakeConfig(opt *intakeOptions) (config, error) {
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

// intakePreflight is the preflight `polako plan` and `polako health` share:
// the binaries on PATH, a checked notify command, a git checkout at -dir, a
// GitHub repo reachable from it (its nameWithOwner filled into cfg), and the
// `gh issue create --parent` capability probe whose result a dry run reports.
// For a real run it also pins the CLI and skill versions the record carries
// and declares the `proposed` label GitHub would otherwise refuse. plan wraps
// this with its -vision stat and the batch milestone; health calls it and adds
// nothing. verb is the noun in the binaries-missing error.
func intakePreflight(ctx context.Context, cfg *config, opt *intakeOptions, verb string) (hierarchical bool, err error) {
	for _, bin := range []string{cfg.claudeBin, cfg.ghBin, "git"} {
		if _, err := exec.LookPath(bin); err != nil {
			return false, fmt.Errorf("%q not found on PATH: %w — %s needs it to run the skill and read GitHub", bin, err, verb)
		}
	}
	if err := checkNotifyCommand(cfg.notifyCmd); err != nil {
		return false, err
	}
	if _, err := git(ctx, *cfg, "rev-parse", "--git-dir"); err != nil {
		return false, fmt.Errorf("-dir %s is not a git checkout: %w", cfg.dir, err)
	}
	raw, err := gh(ctx, *cfg, "repo", "view", "--json", "nameWithOwner")
	if err != nil {
		return false, fmt.Errorf("no GitHub repository reachable from %s (is gh authenticated?): %w", cfg.dir, err)
	}
	var repoView struct {
		NameWithOwner string `json:"nameWithOwner"`
	}
	if err := json.Unmarshal(raw, &repoView); err != nil {
		return false, fmt.Errorf("unreadable `gh repo view` reply (is gh current?): %w", err)
	}
	cfg.repo, cfg.ghRepo = repoView.NameWithOwner, repoView.NameWithOwner

	// Probed here so a dry run can report the shape a real run would take. The
	// run itself does not need telling: the skill derives flat-vs-hierarchical
	// from its own `gh issue create` error (see planPrompt).
	hierarchical = ghCreatesSubIssues(ctx, *cfg)

	if !opt.dryRun {
		// The record pins which CLI and which installed skill produced the
		// run's numbers, the same two the drain's preflight reads. Best-effort:
		// a version that will not answer leaves the field empty rather than
		// stopping the run over telemetry.
		cfg.claudeVersion = claudeVersion(ctx, *cfg)
		cfg.pluginVersion, cfg.pluginID = pluginVersion(ctx, *cfg)

		// Best-effort like the drain's awaiting-answer declaration: an
		// "already exists" is the healthy repeat case, not a failure.
		_ = ensureLabel(ctx, *cfg, proposedLabel, proposedLabelColor, proposedLabelDesc)
	}
	return hierarchical, nil
}

// intakeRunSpec is the per-verb part of a real `polako plan` / `polako health`:
// everything intakeRun cannot compute for itself. The two verbs differ only in
// the prompt they pass the skill, the milestone they attach ("" for a verb
// with no milestone concept), the log line that announces the run, the
// narration prefix, and which record they write.
type intakeRunSpec struct {
	verb           string                                 // "plan" / "health": narration prefix, normaliseProposals tag, exit-ladder wording
	milestone      string                                 // batch milestone, "" for a run with no milestone concept (health)
	announceTarget string                                 // the middle of "running <skill> <target> — capped at N": "from <doc>" / "against <dir>"
	prompt         string                                 // the -p string execClaude is invoked with
	record         func(config, runReport, proposalFacts) // writes the one run-data record, wrapping proposalFacts in the verb's own facts type
}

// intakeRun is the shared body of a real `polako plan` and `polako health`:
// snapshot the open backlog, spawn the skill through execClaude, then —
// whatever the run's own outcome — normalise every issue this account created
// since, price the batch, record it, and notify. The snapshot has to be taken
// here rather than inside normaliseProposals, before the run rather than
// after, so an issue a person files by hand mid-run is told apart from one the
// skill created.
func intakeRun(ctx context.Context, cfg config, opt intakeOptions, spec intakeRunSpec) error {
	before, err := openIssuesBefore(ctx, cfg)
	if err != nil {
		return fmt.Errorf("could not read the backlog before the run (is gh authenticated for %s?): %w",
			cfg.repo, err)
	}

	log.Printf("running %s %s — capped at %s", cfg.skill, spec.announceTarget, plural(opt.maxIssues, "issue"))
	started := time.Now()
	rep, runErr := execClaude(ctx, cfg, spec.prompt, "", cfg.skill, 0)
	// Timed here, before the label pass, so the record's wall time is the run's
	// own — the same boundary the drain draws around runClaude. The pass that
	// follows is supervisor overhead and can spend up to two minutes on gh.
	ended := time.Now()

	// The label pass runs no matter how the run ended — a clean finish, the cap
	// kill, a crash, a Ctrl+C. The curation gate is the whole point of the two
	// verbs, and an issue the run filed but this process never labelled is an
	// unguarded proposal an unattended drain would pick up. So it always gets
	// its own context, detached from the parent's cancellation and bounded on
	// its own — a shutdown signal that lands mid-pass must not be what strands
	// a proposal unlabelled.
	passCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancel()
	if ctx.Err() != nil {
		narrate(sevWarning, "the run was interrupted — still normalising anything it created before exiting")
	}
	pass := normaliseProposals(passCtx, cfg, before, spec.milestone, spec.verb)
	pass.report(spec.verb, opt.maxCost, rep)

	// The pricing line: what the operator's own history says this batch will
	// cost to implement. After the label pass, because it prices the proposals
	// the pass just counted; only when there are proposals, because a batch of
	// nothing has nothing to price and nothing to curate.
	if pass.created > 0 {
		narrate(sevProgress, "%s: %s", spec.verb,
			proposalPricingLine(cfg.rec.metricsDir(), cfg.repo, pass.created, time.Now()))
	}

	// The two traces the run leaves, both after the label pass so they carry
	// what it found: one record whatever the run's status, and — only when the
	// run actually proposed something — one notification. Both detached from
	// the parent context: a Ctrl+C during the run must not be what swallows the
	// record of it or the note that a backlog is now waiting to be curated.
	spec.record(cfg, rep, proposalFacts{
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
	case errors.Is(runErr, errIssueCap):
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
		return fmt.Errorf("the %s run did not finish cleanly: %w — any %s it did create are labelled; "+
			"rerun `polako %s` to continue, the skill deduplicates from GitHub", spec.verb, runErr, proposedLabel, spec.verb)
	}
}
