package main

// `polako setup` is a read-only readiness report: what a repository has for
// polako and what it is missing, one row per check, ending with the
// `polako work` line to run once it looks ready. Nothing here is written —
// `-apply` (docs/plans/setup.md, ticket 3) is a later ticket, and `in` is
// already threaded through the entry point so that ticket's stdin prompt
// costs no signature change.
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
	"strings"
)

type setupOptions struct {
	dir   string
	repo  string
	label string
}

// runSetup is the `setup` subcommand: parse its own flags, read what the
// checks below can tell, print the report. in is unused today — see the
// package comment.
func runSetup(ctx context.Context, args []string, in io.Reader, out io.Writer, rpt report) error {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	fs.SetOutput(out)
	var opt setupOptions
	fs.StringVar(&opt.dir, "dir", ".", "path to the repository's main checkout, when -repo is not given")
	fs.StringVar(&opt.repo, "repo", "",
		"repository to check readiness for (owner/name), instead of whichever -dir is a checkout of")
	fs.StringVar(&opt.label, "label", "",
		"gate label `polako work -label` would use — checked as one more row, and named in the suggested command")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "Usage: polako setup [flags]\n\n"+
			"Prints a read-only readiness report: what this repository has for polako\n"+
			"and what it's missing, ending with the `polako work` line to run once it's\n"+
			"ready. Reads only — nothing here is written.\n\n"+envUsage+"\nFlags:\n")
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

	cfg, err := setupConfig(opt)
	if err != nil {
		return err
	}
	cfg, rows := readSetup(ctx, cfg)
	renderSetup(out, rpt, cfg, rows)
	if setupFailed(rows) {
		return errSetupNotReady
	}
	return nil
}

// errSetupNotReady marks a report that found a required row missing, so
// dispatchVerb's runReport exits nonzero. The report itself already said
// which row and what to do about it, so nothing more needs saying here.
var errSetupNotReady = errors.New("one or more required checks failed — see the rows above")

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

	if repo := strings.TrimSpace(opt.repo); repo != "" {
		owner, name, _ := strings.Cut(repo, "/")
		if strings.Count(repo, "/") != 1 || owner == "" || name == "" {
			return cfg, fmt.Errorf("-repo %q is not owner/name — e.g. -repo %s", repo, "octocat/hello-world")
		}
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
// independent git and claude checks; then sub-issue support and the labels,
// both of which need gh and the repository resolved. It hands back cfg too,
// with cfg.repo/cfg.ghRepo filled in when -repo was not given — the caller's
// own copy stops at whatever setupConfig resolved, and renderSetup's header
// needs the name this function discovered, not that earlier, possibly-empty
// one.
func readSetup(ctx context.Context, cfg config) (config, []setupRow) {
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
			rows = append(rows, setupRepoOKRow(result.view, cfg.label), setupIssuesEnabledRow(result))
		}
	}

	rows = append(rows, setupOriginHeadRow(ctx, cfg, gitOK))
	rows = append(rows, setupPluginRow(ctx, cfg, claudeOK))
	rows = append(rows, setupSubIssueRow(ctx, cfg, reposOK))
	rows = append(rows, setupLabelRows(ctx, cfg, reposOK)...)
	return cfg, rows
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
func setupOriginHeadRow(ctx context.Context, cfg config, gitOK bool) setupRow {
	const name = "origin/HEAD"
	if !gitOK {
		return setupRow{name: name, status: setupUnknown, detail: "git isn't on PATH"}
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

// setupLabelRows checks every label in labelTable, plus -label's own when it
// names one the table does not already cover — the queue gate the operator
// is about to point `polako work` at.
func setupLabelRows(ctx context.Context, cfg config, reposOK bool) []setupRow {
	check := func(l labelDef) setupRow {
		if !reposOK {
			return setupRow{name: l.name, status: setupUnknown, required: l.required,
				detail: "the repository could not be read"}
		}
		exists, err := labelExists(ctx, cfg, l.name)
		switch {
		case err != nil:
			return setupRow{name: l.name, status: setupUnknown, required: l.required, detail: err.Error()}
		case exists:
			return setupRow{name: l.name, status: setupOK, required: l.required}
		default:
			return setupRow{name: l.name, status: setupMissing, required: l.required,
				detail: fmt.Sprintf("gh label create %s --color %s --description %q", l.name, l.color, l.description)}
		}
	}
	rows := make([]setupRow, 0, len(labelTable)+1)
	for _, l := range labelTable {
		rows = append(rows, check(l))
	}
	if cfg.label != "" && !slices.ContainsFunc(labelTable, func(l labelDef) bool { return l.name == cfg.label }) {
		rows = append(rows, check(labelDef{name: cfg.label, color: "ededed",
			description: "gate label for `polako work -label`", required: true}))
	}
	return rows
}

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
// one was named so the suggestion matches what was actually checked.
func suggestedWorkLine(cfg config) string {
	cmd := fmt.Sprintf("polako work -dir %s", cfg.dir)
	if cfg.label != "" {
		cmd += " -label " + cfg.label
	}
	return cmd + " -dry-run"
}
