package main

// `polako setup` is a read-only readiness report: what a repository has for
// polako and what it is missing, one row per check, ending with the
// `polako work` line to run once it looks ready. `-apply` is the one thing
// that writes: it creates the labels the report found missing (ticket 3),
// asking `[Y/n]` per step on a terminal (or `-yes`, for a script), and
// proposes the repo files it found missing through one PR (ticket 4,
// setup_files.go). Still no issue is ever touched, and no repo setting is
// changed.
//
// Every check degrades rather than fails the whole report: a tool missing
// from PATH, a gh too old for a field, a repository this gh cannot reach —
// each is its own row, "couldn't tell" where nothing here can honestly say
// more, so one broken probe never hides the rest of the picture.

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
)

type setupOptions struct {
	dir          string
	repo         string
	label        string
	apply        bool
	yes          bool
	policyLabels bool
}

// runSetup is the `setup` subcommand: parse its own flags, read what the
// checks below can tell, print the report, then — with -apply — write what
// it found missing. isTTY is whether in is a terminal: main passes
// isTerminal(os.Stdin); tests pass it directly, since a strings.Reader is
// never one.
func runSetup(ctx context.Context, args []string, in io.Reader, isTTY bool, out io.Writer, rpt report) error {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	fs.SetOutput(out)
	var opt setupOptions
	fs.StringVar(&opt.dir, "dir", ".", "path to the repository's main checkout, when -repo is not given")
	fs.StringVar(&opt.repo, "repo", "",
		"repository to check readiness for (owner/name), instead of whichever -dir is a checkout of")
	fs.StringVar(&opt.label, "label", "",
		"gate label `polako work -label` would use — checked as one more row, and named in the suggested command")
	fs.BoolVar(&opt.apply, "apply", false,
		"create the labels the report found missing, asking [Y/n] first — needs a terminal, or -yes")
	fs.BoolVar(&opt.yes, "yes", false,
		"with -apply, take the default answer for every step without asking — required when stdin isn't a terminal")
	fs.BoolVar(&opt.policyLabels, "policy-labels", false,
		"show the model:/effort: policy labels in the report too, and, with -apply, offer to create them "+
			"(tier aliases only) — see docs/behaviour.md")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "Usage: polako setup [flags]\n\n"+
			"Prints a read-only readiness report: what this repository has for polako\n"+
			"and what it's missing, ending with the `polako work` line to run once it's\n"+
			"ready. -apply is the one thing that writes — see docs/setup.md.\n\n"+envUsage+"\nFlags:\n")
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
		return fmt.Errorf("unexpected argument %q — setup takes flags only", rest[0])
	}
	// Unattended means no prompts: -apply reads stdin, and a read that would
	// block forever with nobody there to answer it is exactly the failure
	// mode -yes exists to rule out. Checked before any gh call, the same as
	// every other flag-shape refusal here — errFlagsReported for the same
	// exit code, even though what follows is prose rather than usage.
	if setupApplyNeedsYes(opt.apply, opt.yes, isTTY) {
		fmt.Fprintln(out, "-apply is reading stdin that is not a terminal — pass -yes to take the "+
			"defaults for every step without asking, or run this where stdin is a terminal")
		return errFlagsReported
	}

	cfg, err := setupConfig(opt)
	if err != nil {
		return err
	}
	cfg, rows, defs := readSetup(ctx, cfg, opt.policyLabels)
	renderSetup(out, rpt, cfg, rows)
	if opt.apply {
		// One prompt for both write passes below — see setupPrompt's own
		// doc comment for why a second Scanner over the same in would
		// silently drop whatever the first had already read ahead.
		prompt := newSetupPrompt(in, out, opt.yes)
		var gateLabel string
		rows, gateLabel = applySetup(ctx, prompt, cfg, rows, defs)
		if gateLabel != "" {
			// The line renderSetup already printed named no -label: nothing
			// had been chosen yet when it ran. Now something has.
			cfg.label = gateLabel
			fmt.Fprintf(out, "\n%s\n", suggestedWorkLine(cfg))
		}
		rows = applySetupFiles(ctx, prompt, cfg, rows)
	}
	if setupFailed(rows) {
		return errSetupNotReady
	}
	return nil
}

// errSetupNotReady marks a report that found a required row missing, so
// dispatchVerb's runReport exits nonzero. The report itself already said
// which row and what to do about it, so nothing more needs saying here.
var errSetupNotReady = errors.New("one or more required checks failed — see the rows above")

// setupApplyNeedsYes reports whether -apply would have to read a stdin
// nobody is there to answer: not -yes, and not a terminal either. Pulled out
// of runSetup so it can be tested as a pure function, without the flag-shape
// check needing an actual gh or claude on PATH to prove it lets a run past.
func setupApplyNeedsYes(apply, yes, isTTY bool) bool {
	return apply && !yes && !isTTY
}

// setupConfig resolves -dir and, when named, validates -repo's shape. Unlike
// tidyConfig and statusConfig it makes no gh call and requires no binary on
// PATH: every one of those is itself a row this report renders, not a reason
// to refuse before it can say so.
func setupConfig(opt setupOptions) (config, error) {
	cfg := config{
		ghBin:       "gh",
		claudeBin:   "claude",
		ghRetryWait: ghRetryDelay,
		skill:       defaultSkill,
		label:       opt.label,
	}
	abs, err := filepath.Abs(opt.dir)
	if err != nil {
		return cfg, fmt.Errorf("resolving -dir: %w", err)
	}
	cfg.dir = abs

	repo, err := parseRepoFlag(opt.repo)
	if err != nil {
		return cfg, err
	}
	if repo != "" {
		cfg.repo, cfg.ghRepo = repo, repo
	}
	return cfg, nil
}

// --- reading ---

// setupRow is one line of the report: a name, a status ("ok", "missing" or
// "couldn't tell"), and the detail a human reads to act on it. required marks
// a row whose "missing" state fails the whole report — see setupFailed.
type setupRow struct {
	name     string
	status   string
	detail   string
	required bool
}

const (
	setupOK      = "ok"
	setupMissing = "missing"
	setupUnknown = "couldn't tell"
)

// setupFailed reports whether any required row came back missing. A
// "couldn't tell" row never fails the report — a read gh cannot answer is
// exactly not a claim that something is wrong.
func setupFailed(rows []setupRow) bool {
	return slices.ContainsFunc(rows, func(r setupRow) bool { return r.status == setupMissing && r.required })
}

// readSetup runs every check in the order the report renders them: the three
// binaries first, since nothing past them can run without one; then what gh
// can say about the repository and whether Issues are on; then the
// independent git and claude checks; then sub-issue support, the tree checks
// ticket 5 added (build tools, issue templates, CI workflow, branch
// protection, delete-branch-on-merge), and the labels, the last two of which
// need gh and the repository resolved. It hands back cfg too,
// with cfg.repo/cfg.ghRepo filled in when -repo was not given — the caller's
// own copy stops at whatever setupConfig resolved, and renderSetup's header
// needs the name this function discovered, not that earlier, possibly-empty
// one. It also hands back the label defs it checked, so -apply (applySetup)
// works from the exact same list this read pass did rather than building its
// own second copy — two independent calls to setupLabelDefs would have to
// stay byte-for-byte in sync for applySetup's rows/defs correlation to hold.
func readSetup(ctx context.Context, cfg config, policyLabels bool) (config, []setupRow, []labelDef) {
	claudeOK := onPath(cfg.claudeBin)
	ghOK := onPath(cfg.ghBin)
	gitOK := onPath("git")
	rows := []setupRow{
		pathRow("claude", cfg.claudeBin, claudeOK),
		pathRow("gh", cfg.ghBin, ghOK),
		pathRow("git", "git", gitOK),
	}

	var reposOK bool
	if !ghOK {
		rows = append(rows,
			setupRow{name: "gh repo view", status: setupUnknown, detail: "gh isn't on PATH"},
			setupRow{name: "issues enabled", status: setupUnknown, detail: "gh isn't on PATH"})
	} else {
		result, err := retryRead(ctx, cfg, "gh repo view", func() (setupRepoViewResult, error) {
			return readSetupRepoView(ctx, cfg)
		})
		if err != nil {
			rows = append(rows,
				setupRow{name: "gh repo view", status: setupMissing, required: true,
					detail: fmt.Sprintf("could not read the repository (%v) — is gh authenticated, "+
						"and does -dir/-repo name a real repository?", err)},
				setupRow{name: "issues enabled", status: setupUnknown, detail: "the repository could not be read"})
		} else {
			reposOK = true
			cfg.repo, cfg.ghRepo = result.view.NameWithOwner, result.view.NameWithOwner
			cfg.visibility = result.view.Visibility
			rows = append(rows, setupRepoOKRow(result.view, cfg.label), setupIssuesEnabledRow(result))
		}
	}

	rows = append(rows, setupOriginHeadRow(ctx, cfg, gitOK))
	rows = append(rows, setupGitignoreRow(cfg))
	rows = append(rows, setupClaudeMdRow(cfg))
	rows = append(rows, setupVisionRow(cfg))
	rows = append(rows, setupPluginRow(ctx, cfg, claudeOK))
	rows = append(rows, setupSubIssueRow(ctx, cfg, reposOK))
	rows = append(rows, setupBuildToolsRow(cfg))
	rows = append(rows, setupTemplatesRow(cfg))
	rows = append(rows, setupCIWorkflowRow(cfg))
	rows = append(rows, setupBranchProtectionRow(ctx, cfg, reposOK, gitOK))
	rows = append(rows, setupDeleteBranchOnMergeRow(ctx, cfg, reposOK))
	defs := setupLabelDefs(cfg, policyLabels)
	rows = append(rows, setupLabelRows(ctx, cfg, reposOK, defs)...)
	return cfg, rows, defs
}

func onPath(bin string) bool {
	_, err := exec.LookPath(bin)
	return err == nil
}

func pathRow(name, bin string, ok bool) setupRow {
	if ok {
		return setupRow{name: name, status: setupOK}
	}
	return setupRow{name: name, status: setupMissing, required: true,
		detail: fmt.Sprintf("%q not found on PATH", bin)}
}

// setupRepoView is the pair every other verb's own repo-view read already
// asks for (see repoView in main.go, tidyConfig, statusConfig), plus
// hasIssuesEnabled — asked for here alone, since a gh too old for it must not
// take that pair down with it. See readSetupRepoView.
type setupRepoView struct {
	NameWithOwner    string `json:"nameWithOwner"`
	Visibility       string `json:"visibility"`
	HasIssuesEnabled *bool  `json:"hasIssuesEnabled"`
}

// setupRepoViewResult carries readSetupRepoView's answer through retryRead,
// which returns exactly one value: the view, and whether this gh answered
// hasIssuesEnabled at all.
type setupRepoViewResult struct {
	view        setupRepoView
	issuesKnown bool
}

// readSetupRepoView reads the repository, falling back to the smaller --json
// set every other verb already uses when a gh too old for hasIssuesEnabled
// rejects the whole call before it asks GitHub anything — the same shape
// listOpenIssues falls back through on an old gh's sub-issue fields.
func readSetupRepoView(ctx context.Context, cfg config) (setupRepoViewResult, error) {
	out, err := gh(ctx, cfg, "repo", "view", "--json", "nameWithOwner,visibility,hasIssuesEnabled")
	issuesKnown := true
	if unknownJSONField(err) {
		issuesKnown = false
		out, err = gh(ctx, cfg, "repo", "view", "--json", "nameWithOwner,visibility")
	}
	if err != nil {
		return setupRepoViewResult{}, err
	}
	var v setupRepoView
	if err := json.Unmarshal(out, &v); err != nil {
		return setupRepoViewResult{}, fmt.Errorf("parsing repository view: %w", err)
	}
	return setupRepoViewResult{view: v, issuesKnown: issuesKnown}, nil
}

// setupRepoOKRow is what "gh repo view" reports once it has answered: the
// visibility, plus queueGate's own verdict — called rather than
// re-implemented, so this note can never drift from what a real `polako
// work` run would actually refuse on. Said here as advice: this report
// never refuses anything itself.
func setupRepoOKRow(view setupRepoView, label string) setupRow {
	detail := view.Visibility
	if err := queueGate(view.Visibility, label, false); err != nil {
		detail += " — " + err.Error()
	}
	return setupRow{name: "gh repo view", status: setupOK, detail: detail}
}

func setupIssuesEnabledRow(result setupRepoViewResult) setupRow {
	const name = "issues enabled"
	switch {
	case !result.issuesKnown:
		return setupRow{name: name, status: setupUnknown, detail: "this gh does not report hasIssuesEnabled"}
	case result.view.HasIssuesEnabled != nil && *result.view.HasIssuesEnabled:
		return setupRow{name: name, status: setupOK}
	default:
		return setupRow{name: name, status: setupMissing, required: true,
			detail: "enable Issues in the repository's settings"}
	}
}

// setupOriginHeadRow checks the one thing the skill assumes without asking:
// that `git symbolic-ref refs/remotes/origin/HEAD` resolves. A checkout made
// with `git init` plus `git remote add` has no such ref, and the first run
// would find out the hard way (docs/plans/setup.md).
//
// -dir not being a git checkout at all is a different problem from origin/
// HEAD merely being unset — `git remote set-head origin -a` fails with the
// same error a non-checkout gives `symbolic-ref`, so it is checked apart
// and named for what it actually is, the same distinction preflight's own
// `git rev-parse --git-dir` check draws (main.go).
func setupOriginHeadRow(ctx context.Context, cfg config, gitOK bool) setupRow {
	const name = "origin/HEAD"
	if !gitOK {
		return setupRow{name: name, status: setupUnknown, detail: "git isn't on PATH"}
	}
	if _, err := git(ctx, cfg, "rev-parse", "--git-dir"); err != nil {
		return setupRow{name: name, status: setupMissing, required: true,
			detail: fmt.Sprintf("-dir %s is not a git checkout", cfg.dir)}
	}
	if _, err := git(ctx, cfg, "symbolic-ref", "refs/remotes/origin/HEAD", "--short"); err != nil {
		return setupRow{name: name, status: setupMissing, required: true,
			detail: "run `git remote set-head origin -a`"}
	}
	return setupRow{name: name, status: setupOK}
}

// setupPluginRow reuses pluginVersion and skewComparison — the same reads
// preflight's own version-skew gate makes — so a repository is flagged here
// exactly when a real `polako work` run would warn or refuse over it.
func setupPluginRow(ctx context.Context, cfg config, claudeOK bool) setupRow {
	const name = "plugin"
	if !claudeOK {
		return setupRow{name: name, status: setupUnknown, detail: "claude isn't on PATH"}
	}
	cfg.pluginVersion, _, _ = pluginVersion(ctx, cfg)
	if cfg.pluginVersion == "" {
		return setupRow{name: name, status: setupUnknown,
			detail: fmt.Sprintf("couldn't read the installed %s plugin's version (`claude plugin list --json`)", pluginName)}
	}
	self, plugin, behind, ok := skewComparison(polakoVersion(), cfg)
	if ok && behind {
		return setupRow{name: name, status: setupMissing, required: true,
			detail: fmt.Sprintf("installed plugin (%s) is behind this binary (%s) — %s", plugin, self, skewRemedy())}
	}
	return setupRow{name: name, status: setupOK, detail: cfg.pluginVersion}
}

// setupSubIssueRow is advisory only: a gh that cannot file `--parent` still
// runs `polako work` and `polako plan` fine, epics filed flat instead — see
// ghCreatesSubIssues.
func setupSubIssueRow(ctx context.Context, cfg config, reposOK bool) setupRow {
	const name = "sub-issue support"
	if !reposOK {
		return setupRow{name: name, status: setupUnknown, detail: "the repository could not be read"}
	}
	if ghCreatesSubIssues(ctx, cfg) {
		return setupRow{name: name, status: setupOK}
	}
	return setupRow{name: name, status: setupMissing,
		detail: "this gh can't file a child issue with `--parent` — epics work flat instead"}
}

// setupLabelDefs, policyLabelDefs, checkLabelDef, setupLabelRows and -apply's
// own write pass live in setup_apply.go — split out to keep this file closer
// to the repo's own median length.

// --- rendering ---

func renderSetup(w io.Writer, rpt report, cfg config, rows []setupRow) {
	header := cfg.repo
	if header == "" {
		header = cfg.dir
	}
	fmt.Fprintf(w, "%s\n", rpt.bold(header))
	tableRows := make([][]string, len(rows))
	for i, r := range rows {
		tableRows[i] = []string{r.name, r.status, r.detail}
	}
	printTable(w, rpt, "setup", []string{"check", "status", "detail"}, tableRows, 3)
	fmt.Fprintf(w, "\n%s\n", suggestedWorkLine(cfg))
}

// suggestedWorkLine is the report's last line: the `polako work` invocation
// this repository is ready (or not yet ready) for, -label included whenever
// one was named and -add-tools included whenever the build-tools row found
// something uncovered, so the suggestion matches what was actually checked.
func suggestedWorkLine(cfg config) string {
	cmd := fmt.Sprintf("polako work -dir %s", cfg.dir)
	if cfg.label != "" {
		cmd += " -label " + cfg.label
	}
	if tools := missingBuildTools(cfg); len(tools) > 0 {
		cmd += " " + addToolsFlag(tools)
	}
	return cmd + " -dry-run"
}
