package main

// -apply's own half of `polako setup`: which labels a run is offering
// (setupLabelDefs, policyLabelDefs), how each is checked (checkLabelDef,
// shared with the read-only report's setupLabelRows), and the write pass
// itself (setupPrompt, applySetup) — split out of setup.go so that file stays
// closer to the repo's own median length as this grows; see setup.go for the
// read-only report these functions extend.

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
)

// setupLabelDefs is every label this run cares about: the fixed table,
// -label's own when it names one the table does not already cover, and, with
// policyLabels, the tier-alias set -policy-labels offers to -apply. Shared by
// setupLabelRows (the read path) and applySetup (the write path), so both
// agree on exactly which labels are in scope for one invocation.
func setupLabelDefs(cfg config, policyLabels bool) []labelDef {
	defs := slices.Clone(labelTable)
	if cfg.label != "" && !slices.ContainsFunc(labelTable, func(l labelDef) bool { return l.name == cfg.label }) {
		defs = append(defs, labelDef{name: cfg.label, color: "ededed",
			description: "gate label for `polako work -label`", required: true})
	}
	if policyLabels {
		defs = append(defs, policyLabelDefs()...)
	}
	return defs
}

// policyLabelDefs is what -policy-labels adds: the model: family, tier
// aliases only (no model:best, no raw id — those aren't policy this binary
// ever creates), and one effort: label per effortLevels. Reversing
// docs/behaviour.md's "polako never creates these labels" is opt-in and
// tier-alias-only on purpose — see that doc.
func policyLabelDefs() []labelDef {
	const color = "5319E7"
	defs := []labelDef{
		{name: "model:opus", color: color, description: "run this issue on the opus tier"},
		{name: "model:sonnet", color: color, description: "run this issue on the sonnet tier"},
		{name: "model:haiku", color: color, description: "run this issue on the haiku tier"},
		{name: "model:default", color: color, description: "run this issue on the account's default model"},
	}
	for _, e := range effortLevels {
		defs = append(defs, labelDef{name: "effort:" + e, color: color, description: "run this issue at " + e + " effort"})
	}
	return defs
}

// checkLabelDef reads one label's row — ok, missing, or "couldn't tell" —
// shared by setupLabelRows and applySetup's own gate-label-name prompt, so
// the two can never disagree about what "missing" means for the same label.
func checkLabelDef(ctx context.Context, cfg config, reposOK bool, l labelDef) setupRow {
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

// setupLabelRows is setupLabelDefs checked one by one — the queue gate the
// operator is about to point `polako work` at, plus whatever -policy-labels
// asked for.
func setupLabelRows(ctx context.Context, cfg config, reposOK, policyLabels bool) []setupRow {
	defs := setupLabelDefs(cfg, policyLabels)
	rows := make([]setupRow, len(defs))
	for i, l := range defs {
		rows[i] = checkLabelDef(ctx, cfg, reposOK, l)
	}
	return rows
}

// setupPrompt asks the questions -apply needs, sharing one bufio.Scanner
// over in across every question this run asks — two Scanners over the same
// reader would each buffer independently, and the second would silently
// drop whatever the first had already read ahead.
type setupPrompt struct {
	scan *bufio.Scanner
	out  io.Writer
	yes  bool
}

func newSetupPrompt(in io.Reader, out io.Writer, yes bool) *setupPrompt {
	return &setupPrompt{scan: bufio.NewScanner(in), out: out, yes: yes}
}

// confirm asks "step [Y/n] ", default yes. -yes skips the question outright,
// same as an unanswered EOF: -apply's own preflight above already refused a
// non-terminal stdin without -yes, so an EOF reached from here is either
// -yes itself or a terminal that closed mid-run, and neither should hang.
func (p *setupPrompt) confirm(step string) bool {
	if p.yes {
		return true
	}
	fmt.Fprintf(p.out, "%s [Y/n] ", step)
	if !p.scan.Scan() {
		return true
	}
	a := strings.ToLower(strings.TrimSpace(p.scan.Text()))
	return a == "" || a == "y" || a == "yes"
}

// name asks question, offering suggestion as the default an empty answer (or
// -yes, or EOF) takes. The one free-text prompt this run has, for the gate
// label's own name.
func (p *setupPrompt) name(question, suggestion string) string {
	if p.yes {
		return suggestion
	}
	fmt.Fprintf(p.out, "%s [%s] ", question, suggestion)
	if !p.scan.Scan() {
		return suggestion
	}
	if v := strings.TrimSpace(p.scan.Text()); v != "" {
		return v
	}
	return suggestion
}

// applySetup is -apply's write pass: create every row readSetup found
// missing, asking first unless yes says to skip asking. A public repository
// with no -label named gets one more question before that loop — what to
// call the gate label, defaulting to "ready" — since the report above never
// checked a label nobody named yet; the answer is appended to both defs and
// rows together, so the trailing len(defs) rows of rows keep lining up
// positionally with defs one-for-one (matching by name instead could collide
// with an unrelated row that happens to share the label's name).
//
// Mutates and returns rows so the caller's setupFailed check reflects what
// this pass actually created, without a second full read pass.
func applySetup(ctx context.Context, in io.Reader, out io.Writer, cfg config, rows []setupRow, defs []labelDef, yes bool) []setupRow {
	prompt := newSetupPrompt(in, out, yes)
	if cfg.label == "" && strings.EqualFold(cfg.visibility, "PUBLIC") {
		name := prompt.name("this repository is public and has no gate label — name one to create", "ready")
		def := labelDef{name: name, color: "ededed", description: "gate label for `polako work -label`", required: true}
		defs = append(defs, def)
		rows = append(rows, checkLabelDef(ctx, cfg, true, def))
	}
	start := len(rows) - len(defs)
	for i, def := range defs {
		ri := start + i
		if rows[ri].status != setupMissing {
			continue
		}
		if !prompt.confirm(fmt.Sprintf("create the %q label?", def.name)) {
			continue
		}
		if err := ensureLabel(ctx, cfg, def.name, def.color, def.description); err != nil {
			// Named rather than relayed: the likeliest cause by far is a
			// token without push access, and raw gh stderr for that is a
			// wall of JSON an operator has to decode to reach the same
			// conclusion.
			fmt.Fprintf(out, "  could not create %q — needs write access to %s\n", def.name, cfg.repo)
			continue
		}
		rows[ri] = setupRow{name: def.name, status: setupOK, required: def.required}
		fmt.Fprintf(out, "  created %q\n", def.name)
	}
	return rows
}
