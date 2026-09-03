package main

// `polako plan` runs the plan-backlog skill unattended, the way `polako work`
// runs implement-issue: point it at a vision document (or an inline -brief) and
// it proposes a curated backlog — epics and one-PR issues — behind the
// `proposed` label a human lifts to queue the work.
//
// The run is one claude invocation through execClaude — the same entry `work`
// uses — bracketed by two enforcement mechanisms that make the curation gate
// structural rather than a thing the model has to remember, both shared with
// `polako health` and defined in labelpass.go:
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

// planOptions is the shared intake flag set plus the three flags that are
// plan's alone: the vision document (or inline brief) to plan from, and the
// batch milestone the label pass attaches.
type planOptions struct {
	intakeOptions
	vision    string
	brief     string
	milestone string
}

// planVerb is the per-verb wording and defaults registerIntakeFlags takes for
// `polako plan`.
var planVerb = intakeVerb{
	name:          "plan",
	skillDefault:  defaultPlanSkill,
	toolsDefault:  planTools,
	dirHelp:       "path to the repository's main checkout",
	focusExample:  "only the observability section",
	modelCadence:  "happens once per batch",
	dryRunSubject: "the document",
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
	fs.StringVar(&opt.milestone, "milestone", "",
		"batch milestone title, or \"off\" to skip it (default: the vision file's name, or the brief's first words)")
	registerIntakeFlags(fs, &opt.intakeOptions, planVerb)
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
// than inside normaliseProposals, before the run rather than after, so an
// issue a person files by hand mid-run is told apart from one the skill
// created.
func planRun(ctx context.Context, cfg config, opt planOptions, milestone string, out io.Writer) error {
	before, err := openIssuesBefore(ctx, cfg)
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
	pass := normaliseProposals(passCtx, cfg, before, milestone, "plan")
	pass.report("plan", opt.maxCost, rep)

	// The pricing line: what the operator's own history says this batch will
	// cost to implement. After the label pass, because it prices the proposals
	// the pass just counted; only when there are proposals, because a batch of
	// nothing has nothing to price and nothing to curate.
	if pass.created > 0 {
		narrate(sevProgress, "plan: %s", proposalPricingLine(cfg.rec.metricsDir(), cfg.repo, pass.created, time.Now()))
	}

	// The two traces the run leaves, both after the label pass so they carry
	// what it found: one record whatever the run's status, and — only when the
	// run actually proposed something — one notification. Both detached from
	// the parent context for the same reason the pass is: a Ctrl+C during the
	// run must not be what swallows the record of it or the note that a backlog
	// is now waiting to be curated.
	cfg.rec.recordPlan(cfg, rep, planFacts{
		proposalFacts: proposalFacts{
			issuesCreated:  pass.created,
			epicsCreated:   pass.epics,
			cap:            opt.maxIssues,
			labelsEnforced: pass.labelsEnforced(),
			started:        started,
			ended:          ended,
		},
		vision:    planVisionField(opt),
		milestone: milestone,
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

// planConfig validates the vision/brief pair, then hands off to intakeConfig
// for the config the gh helpers take. It never stats -vision to decide whether
// it was given: a does-the-file-exist heuristic would turn a typo'd path into a
// silent "no document" instead of a loud failure. Preflight stats it once it is
// settled that a path was the intent.
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
	return intakeConfig(&opt.intakeOptions)
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
		_ = ensureLabel(ctx, *cfg, proposedLabel, proposedLabelColor, proposedLabelDesc)
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
	return briefTitle(strings.TrimSpace(opt.brief))
}

// briefTitleMax is the character cap on a brief-derived milestone title —
// long enough to read as a title, short enough to render on one line.
const briefTitleMax = 50

// briefTitle derives a milestone title from a brief: unchanged if it already
// fits under briefTitleMax, otherwise cut at the last whole word inside the
// cap and trimmed so the cut doesn't leave a dangling connective or stray
// punctuation at the end.
func briefTitle(s string) string {
	runes := []rune(s)
	if len(runes) <= briefTitleMax {
		return s
	}
	// Cut on runes, not bytes: a byte-index slice can split a multi-byte
	// character in two and leave invalid UTF-8 in the milestone title.
	cut := string(runes[:briefTitleMax])
	if i := strings.LastIndexByte(cut, ' '); i >= 0 {
		cut = cut[:i]
	}
	return trimDangling(cut)
}

// danglingConnectives are the words most likely to be left stranded at the
// end of a brief cut off mid-sentence.
var danglingConnectives = map[string]bool{
	"and": true, "or": true, "with": true, "for": true, "to": true, "the": true,
}

// trimDangling strips trailing punctuation and a trailing connective word off
// s, repeating until neither applies — a comma can sit right after a
// connective ("horses, and,"), so one pass of each isn't always enough.
func trimDangling(s string) string {
	for {
		trimmed := strings.TrimRight(s, " ,.;:!?-")
		fields := strings.Fields(trimmed)
		if n := len(fields); n > 0 && danglingConnectives[strings.ToLower(fields[n-1])] {
			trimmed = strings.Join(fields[:n-1], " ")
		}
		if trimmed == s {
			return s
		}
		s = trimmed
	}
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

// proposalPricingLine, approxUSD and approxDur now live in labelpass.go,
// shared with `polako health` — the pricing line prices what history says
// implementing a batch of proposals costs, and has nothing plan-specific in
// it: it reads ordinary issue-run records, not the plan/health run's own.
