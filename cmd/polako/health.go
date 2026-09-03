package main

// `polako health` runs the review-health skill unattended, the way `polako
// plan` runs plan-backlog: point it at a repository (via -dir, like every
// other verb) and it measures that repo's own shape — file and function
// sizes, duplicated helpers, abstractions nothing uses — and files what it
// finds as `proposed` issues, the same curation gate and sizing contract
// plan-backlog uses. Where plan-backlog needs a vision document to plan
// from, review-health needs nothing but the repository in front of it — so
// this verb differs from `plan` only in which skill it runs, what it passes
// that skill, and that it attaches no milestone.
//
// The run is one claude invocation through execClaude, bracketed by the same
// two enforcement mechanisms plan.go's comment describes and labelpass.go
// defines: the -max-issues cap, and the label pass that normalises every
// issue this account created since the run started to carry *exactly* the
// `proposed` label.
//
// When it ends, whatever its status, it leaves the two traces every other
// run leaves: one `kind:"health"` run-data record (metrics.go), and — when
// it created proposals — one `proposed` notification (notify.go).

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os/exec"
	"time"
)

// healthSkillDir is the intake-side skill under skills/, the twin of
// planSkillDir. It lived in health_skill_test.go while the binary had no verb
// to run it; the verb is here now, so the constant is too.
const healthSkillDir = "review-health"

// defaultHealthSkill is the -skill default: the plugin-namespaced slash
// command, the same shape defaultPlanSkill has.
const defaultHealthSkill = "polako:" + healthSkillDir

// healthTools is the --allowedTools for an unattended health run: review-health's
// SKILL.md bounds its own `gh` surface to exactly three call shapes — the two
// `issue list` reads (open backlog, recently-closed) and the one `issue
// create` — so the allowlist grants only those, plus the repo-reading and
// scratch-file tools a measuring pass needs. No `gh issue view` or `gh search
// issues`: review-health never spells either.
const healthTools = "Bash(git log:*),Bash(git show:*),Bash(git status:*),Bash(git branch:*)," +
	"Bash(gh issue list:*),Bash(gh issue create:*)," +
	"Read,Glob,Grep,TodoWrite,Write"

// healthVerb is the per-verb wording and defaults registerIntakeFlags takes for
// `polako health`. healthOptions itself is the bare shared set — see intake.go.
var healthVerb = intakeVerb{
	name:          "health",
	skillDefault:  defaultHealthSkill,
	toolsDefault:  healthTools,
	dirHelp:       "path to the repository to audit",
	focusExample:  "only cmd/polako",
	modelCadence:  "happens periodically",
	dryRunSubject: "the repository",
}

// runHealth is the `health` subcommand: parse its own flags, preflight, then
// either print the invocation a run would make (-dry-run) or make it —
// snapshot the backlog, spawn the skill through execClaude, and run the label
// pass over whatever it created. Its config is built the lightweight way
// plan's is, not work's heavier preflight.
func runHealth(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("health", flag.ContinueOnError)
	fs.SetOutput(out)
	var opt healthOptions
	registerIntakeFlags(fs, &opt, healthVerb)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "Usage: polako health [flags]\n\n"+
			"Propose a curated backlog from the repository's own shape: run the\n"+
			"review-health skill unattended, filing epics and one-PR issues behind the\n"+
			"`proposed` label a human lifts to queue the work. Every issue the run creates\n"+
			"is normalised to carry exactly that label before this exits, and the run is\n"+
			"capped at -max-issues.\n\n"+
			"`polako health -dry-run` prints the invocation a run would make and touches nothing.\n\n"+envUsage+"\nFlags:\n")
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
		return fmt.Errorf("unexpected argument %q — health takes flags only", rest[0])
	}

	cfg, err := healthConfig(&opt)
	if err != nil {
		return err
	}
	hierarchical, err := healthPreflight(ctx, &cfg, &opt)
	if err != nil {
		return err
	}
	if opt.dryRun {
		return healthDryRun(cfg, opt, hierarchical, out)
	}
	return healthRun(ctx, cfg, opt, out)
}

// healthRun is a real `polako health`: snapshot the open backlog, spawn the
// skill through execClaude, then — whatever the run's own outcome —
// normalise every issue this account created since. Mirrors planRun, minus
// the milestone half of the label pass: review-health attaches none.
func healthRun(ctx context.Context, cfg config, opt healthOptions, out io.Writer) error {
	before, err := openIssuesBefore(ctx, cfg)
	if err != nil {
		return fmt.Errorf("could not read the backlog before the run (is gh authenticated for %s?): %w",
			cfg.repo, err)
	}

	prompt := healthPrompt(cfg, opt)
	log.Printf("running %s against %s — capped at %s", cfg.skill, cfg.dir, plural(opt.maxIssues, "issue"))
	started := time.Now()
	rep, runErr := execClaude(ctx, cfg, prompt, "", cfg.skill, 0)
	ended := time.Now()

	// The label pass runs no matter how the run ended — see planRun's own
	// comment on this same shape in plan.go.
	passCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancel()
	if ctx.Err() != nil {
		narrate(sevWarning, "the run was interrupted — still normalising anything it created before exiting")
	}
	pass := normaliseProposals(passCtx, cfg, before, "", "health")
	pass.report("health", opt.maxCost, rep)

	if pass.created > 0 {
		narrate(sevProgress, "health: %s", proposalPricingLine(cfg.rec.metricsDir(), cfg.repo, pass.created, time.Now()))
	}

	cfg.rec.recordHealth(cfg, rep, healthFacts{proposalFacts: proposalFacts{
		issuesCreated:  pass.created,
		epicsCreated:   pass.epics,
		cap:            opt.maxIssues,
		labelsEnforced: pass.labelsEnforced(),
		started:        started,
		ended:          ended,
	}})
	if pass.created > 0 {
		notify(context.WithoutCancel(ctx), cfg, notification{
			event: notifyProposed, reason: proposedNotifyReason(pass.created, pass.epics)})
	}

	// Same exit-condition order planRun uses: a failed label pass outranks an
	// interrupt, which outranks the run's own failure.
	if perr := pass.err(); perr != nil {
		return perr
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	switch {
	case runErr == nil:
		return nil
	case errors.Is(runErr, errIssueCap):
		return nil
	case errors.Is(runErr, errNoWork):
		return fmt.Errorf("%w — check that -skill %q names a skill this installation has; "+
			"plugin skills are namespaced <plugin>:<skill>", runErr, cfg.skill)
	case errors.Is(runErr, errAuth):
		return authAdvice(runErr)
	default:
		return fmt.Errorf("the health run did not finish cleanly: %w — any %s it did create are labelled; "+
			"rerun `polako health` to continue, the skill deduplicates from GitHub", runErr, proposedLabel)
	}
}

// healthConfig is a thin call to intakeConfig: unlike planConfig there is no
// vision/brief pair to validate first, since review-health takes only -dir
// (already required by every verb) and an optional -focus.
func healthConfig(opt *healthOptions) (config, error) {
	return intakeConfig(opt)
}

// healthPreflight mirrors planPreflight minus the vision-file check and the
// milestone half: binaries, a reachable repo, a checked notify command, the
// `--parent` capability probe, and — for a real run only — the `proposed`
// label declared.
func healthPreflight(ctx context.Context, cfg *config, opt *healthOptions) (hierarchical bool, err error) {
	for _, bin := range []string{cfg.claudeBin, cfg.ghBin, "git"} {
		if _, err := exec.LookPath(bin); err != nil {
			return false, fmt.Errorf("%q not found on PATH: %w — health needs it to run the skill and read GitHub", bin, err)
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

	// Probed here so a dry run can report the shape a real run would take, the
	// same reason planPreflight probes it.
	hierarchical = ghCreatesSubIssues(ctx, *cfg)

	if !opt.dryRun {
		cfg.claudeVersion = claudeVersion(ctx, *cfg)
		cfg.pluginVersion, cfg.pluginID = pluginVersion(ctx, *cfg)

		// Best-effort like plan's: an "already exists" is the healthy repeat
		// case, not a failure.
		_ = ensureLabel(ctx, *cfg, proposedLabel, proposedLabelColor, proposedLabelDesc)
	}
	return hierarchical, nil
}

// healthDryRun narrates what a run would do — to the log, i.e. stderr — and
// prints the exact claude invocation to out. It declares no label and
// records nothing: pointing it at an unfamiliar repository leaves that
// repository exactly as it found it.
func healthDryRun(cfg config, opt healthOptions, hierarchical bool, out io.Writer) error {
	log.Printf("auditing %s", cfg.dir)
	if opt.focus != "" {
		log.Printf("focus: %s", opt.focus)
	}
	log.Printf("issue cap: %d, epics included", opt.maxIssues)
	if hierarchical {
		log.Println("issue shape: hierarchical — epics with sub-issues")
	} else {
		log.Println("issue shape: flat — this gh has no `gh issue create --parent`, so a tracking issue holds the design")
	}
	log.Println("dry run — no proposed label, no run data; the invocation follows on stdout")

	_, err := fmt.Fprintln(out, commandLine(cfg.claudeBin, healthArgs(cfg, opt)))
	return err
}

// healthArgs is the headless health invocation, built through buildArgs so a
// dry run prints the exact argv healthRun's real dispatch makes.
func healthArgs(cfg config, opt healthOptions) []string {
	return buildArgs(cfg, healthPrompt(cfg, opt), "")
}

// healthPrompt is the -p string the skill is invoked with: the slash command
// and review-health's two declared arguments, repo and focus. The repo
// argument is always left empty — execClaude already runs the process with
// cwd set to cfg.dir (see dispatchClaude), and review-health's own SKILL.md
// says an empty $repo means "the repository the session is already in", so
// passing it explicitly would only repeat what -dir already established. An
// explicit "" placeholder holds that argument's position open when -focus is
// set, so $focus still binds to the second argument rather than the first.
func healthPrompt(cfg config, opt healthOptions) string {
	prompt := "/" + cfg.skill
	if opt.focus != "" {
		prompt += ` "" ` + planSlashArg(opt.focus)
	}
	return prompt
}
