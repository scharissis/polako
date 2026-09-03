package main

// The flags and config `polako plan` and `polako health` share. Both verbs run
// one skill unattended behind the `proposed` curation gate labelpass.go
// enforces; they differ in what they plan from and whether a milestone gets
// attached, not in the 15 knobs they expose or the lightweight config they
// build from them. That common part lives here, the way the shared label pass
// lives in labelpass.go.

import (
	"flag"
	"fmt"
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
