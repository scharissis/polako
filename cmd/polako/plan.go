package main

// `polako plan` runs the plan-backlog skill unattended, the way `polako work`
// runs implement-issue: point it at a vision document (or an inline -brief) and
// it proposes a curated backlog — epics and one-PR issues — behind the
// `proposed` label a human lifts to queue the work.
//
// This is the verb's skeleton: dispatch, its own FlagSet, preflight with the
// `--parent` capability probe and the batch milestone, and a working
// `-dry-run`. The run itself — spawning claude, counting issues against the
// cap, and the label pass that normalises what the run created so the curation
// gate does not depend on the model remembering — arrives with issue #103. So
// until then a real (non-`-dry-run`) invocation refuses: at no commit can
// `polako plan` start a run the label pass does not police.

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

// planNotRunnableErr is what a real (non-`-dry-run`) `polako plan` returns
// until issue #103 lands the run path and its label pass. Naming #103 keeps
// the operator from filing "plan is broken" and points them at `-dry-run`
// meanwhile.
var planNotRunnableErr = errors.New("polako plan cannot start a run yet: the run path — spawning the skill, " +
	"holding it to -max-issues, and the label pass that normalises what it created so the curation gate " +
	"does not depend on the model — arrives with issue #103. Until then, `polako plan -dry-run` prints the " +
	"invocation it will make.")

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
// print the invocation a run would make (-dry-run) or refuse until #103 lands
// the run path. Its config is built the lightweight way status's and tidy's
// are, not work's heavier preflight.
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
	fs.DurationVar(&opt.stall, "stall", 15*time.Minute, "kill and resume a run with no output events for this long (0 disables)")
	fs.Float64Var(&opt.maxCost, "max-cost", 0, "stop the run once it has cost this many dollars (0 disables)")
	fs.StringVar(&opt.metrics, "metrics", "", `directory for run-data records, or "off" (default ~/.polako/metrics)`)
	fs.StringVar(&opt.tag, "run-tag", "", "label recorded with the run, for comparing one batch against another")
	fs.StringVar(&opt.notifyCmd, "notify", "",
		"command to run when the run needs a human, with context in "+notifyPrefix+"* (see docs/reference.md)")
	fs.BoolVar(&opt.dryRun, "dry-run", false,
		"resolve the document, print the claude invocation the run would get, and exit without running or writing anything")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "Usage: polako plan (-vision <doc> | -brief \"<text>\") [flags]\n\n"+
			"Propose a curated backlog from a vision document: run the plan-backlog skill\n"+
			"unattended, filing epics and one-PR issues behind the `proposed` label a human\n"+
			"lifts to queue the work.\n\n"+
			"Skeleton for now — a real run refuses until the enforcing label pass lands (#103);\n"+
			"`polako plan -dry-run` prints the invocation it will make.\n\n"+envUsage+"\nFlags:\n")
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
	if !opt.dryRun {
		return planNotRunnableErr
	}
	return planDryRun(cfg, opt, milestone, hierarchical, out)
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
		tag:            opt.tag,
		notifyCmd:      opt.notifyCmd,
		dryRun:         opt.dryRun,
		queue:          new(queueMemo),
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

	// Discovered here rather than mid-run: an older gh with no sub-issue support
	// would raise a permission prompt on `--parent` that an unattended run has
	// nobody to answer. The skill takes its own fallback from the flag it is
	// handed once #103 wires the spawn.
	hierarchical = ghCreatesSubIssues(ctx, *cfg)

	milestone = planMilestoneTitle(opt)

	if !opt.dryRun {
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

// planArgs is the headless plan invocation, built through buildArgs so a
// dry run prints the argv #103's real dispatch will make rather than a second
// rendering of it — the pair that drifts the first time either side changes.
func planArgs(cfg config, opt planOptions) []string {
	prompt := "/" + cfg.skill + " " + planSlashArg(planPromptSubject(opt))
	if opt.focus != "" {
		prompt += " " + planSlashArg(opt.focus)
	}
	return buildArgs(cfg, prompt, "")
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
// whitespace, so the skill sees one argument rather than several.
func planSlashArg(s string) string {
	if strings.ContainsAny(s, " \t") {
		return `"` + s + `"`
	}
	return s
}
