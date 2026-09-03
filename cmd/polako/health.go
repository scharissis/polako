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
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
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

// healthRun is a real `polako health`: the shared intakeRun body (snapshot,
// spawn, label pass, price, record, notify, exit ladder — all in intake.go)
// fed health's own pieces: the review-health prompt, no milestone, the
// "against <dir>" announce, and a healthFacts record. Mirrors planRun minus
// the milestone half.
func healthRun(ctx context.Context, cfg config, opt healthOptions, _ io.Writer) error {
	return intakeRun(ctx, cfg, opt, intakeRunSpec{
		verb:           "health",
		milestone:      "",
		announceTarget: "against " + cfg.dir,
		prompt:         healthPrompt(cfg, opt),
		record: func(cfg config, rep runReport, pf proposalFacts) {
			cfg.rec.recordHealth(cfg, rep, healthFacts{proposalFacts: pf})
		},
	})
}

// healthConfig is a thin call to intakeConfig: unlike planConfig there is no
// vision/brief pair to validate first, since review-health takes only -dir
// (already required by every verb) and an optional -focus.
func healthConfig(opt *healthOptions) (config, error) {
	return intakeConfig(opt)
}

// healthPreflight is a thin call to intakePreflight: unlike planPreflight there
// is no vision-file check and no milestone half — review-health plans from the
// repository in front of it and attaches nothing.
func healthPreflight(ctx context.Context, cfg *config, opt *healthOptions) (hierarchical bool, err error) {
	return intakePreflight(ctx, cfg, opt, "health")
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
