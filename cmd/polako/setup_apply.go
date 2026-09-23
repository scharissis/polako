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
// -label's own, and, with policyLabels, the tier-alias set -policy-labels
// offers to -apply. Shared by setupLabelRows (the read path) and applySetup
// (the write path), so both agree on exactly which labels are in scope for
// one invocation.
func setupLabelDefs(cfg config, policyLabels bool) []labelDef {
	defs := slices.Clone(labelTable)
	if cfg.label != "" {
		defs = append(defs, labelDef{name: cfg.label, color: "ededed",
			description: gateLabelDescription, required: true, isGateLabel: true})
	}
	if policyLabels {
		defs = append(defs, policyLabelDefs()...)
	}
	return dedupeLabelDefs(defs)
}

// dedupeLabelDefs drops a later entry that repeats an earlier one's name.
// -label can name anything, including a name labelTable or the policy set
// already carries — -label model:opus is a real, if odd, invocation — so the
// three sources above can produce the same name twice. Keeps the first
// occurrence; required is promoted to true if any duplicate asked for it, so
// a required -label colliding with an optional policy label stays required.
// Without this, applySetup would offer to create the same label twice in one
// run: the second attempt fails "already exists" right after the first
// attempt's own success.
func dedupeLabelDefs(defs []labelDef) []labelDef {
	out := make([]labelDef, 0, len(defs))
	for _, d := range defs {
		if i := slices.IndexFunc(out, func(o labelDef) bool { return o.name == d.name }); i >= 0 {
			out[i].required = out[i].required || d.required
			continue
		}
		out = append(out, d)
	}
	return out
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

// unmarkedGateLabelDetail is checkLabelDef's row detail for a gate label
// that exists but predates setup's own marker — an operator's own -label
// value, hand-created before this feature, or one made outside `polako
// setup` entirely. applySetup reads this same string back to decide which
// rows to offer marking for; a test holds both to the same wording.
const unmarkedGateLabelDetail = "exists, not marked as the gate label"

// checkLabelDef reads one label's row — ok, missing, or "couldn't tell" —
// shared by setupLabelRows and applySetup's own gate-label-name prompt, so
// the two can never disagree about what "missing" means for the same label.
// A gate-label def (isGateLabel) is checked against gateLabelDescription
// too, since an existing-but-unmarked one is exactly what `-apply`'s
// marking pass exists to fix, and reading it here means setupLabelRows (the
// read-only report) shows the same row `-apply` acts on.
func checkLabelDef(ctx context.Context, cfg config, reposOK bool, l labelDef) setupRow {
	if !reposOK {
		return setupRow{name: l.name, status: setupUnknown, required: l.required,
			detail: "the repository could not be read"}
	}
	if l.isGateLabel {
		desc, exists, err := labelDescription(ctx, cfg, l.name)
		switch {
		case err != nil:
			return setupRow{name: l.name, status: setupUnknown, required: l.required, detail: err.Error()}
		case !exists:
			return setupRow{name: l.name, status: setupMissing, required: l.required,
				detail: fmt.Sprintf("gh label create %s --color %s --description %q", l.name, l.color, l.description)}
		case desc != gateLabelDescription:
			return setupRow{name: l.name, status: setupOK, required: l.required, detail: unmarkedGateLabelDetail}
		default:
			return setupRow{name: l.name, status: setupOK, required: l.required}
		}
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

// setupLabelRows is defs checked one by one — the queue gate the operator is
// about to point `polako work` at, plus whatever -policy-labels asked for.
// Takes defs rather than building it, so readSetup's one setupLabelDefs call
// is the only one — see readSetup's own comment for why that matters.
func setupLabelRows(ctx context.Context, cfg config, reposOK bool, defs []labelDef) []setupRow {
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

// confirm asks "step [Y/n] ", default yes on an empty line — confirmDefault
// with defaultYes true. Every step but the scaffold prompt (setup_files.go)
// defaults to yes.
func (p *setupPrompt) confirm(step string) bool {
	return p.confirmDefault(step, true)
}

// confirmDefault is confirm generalized to a caller-chosen default. -yes
// means "take the default answer for every step without asking" — not
// "answer yes to everything" — so a defaultYes-false step (the scaffold
// prompt) has to decline under -yes, the same as an operator hitting enter
// on it would. An EOF is not the same as an empty line either way: it means
// stdin closed — an operator's Ctrl-D meant to back out, or a terminal that
// died mid-run — so it declines rather than accepting the default, and every
// later confirmDefault() in the same run declines too, since bufio.Scanner
// keeps returning false once its reader is exhausted. -apply's own preflight
// above already refused a non-terminal stdin without -yes, so this path is
// never reached by -yes itself; only a live terminal reaches it.
func (p *setupPrompt) confirmDefault(step string, defaultYes bool) bool {
	if p.yes {
		return defaultYes
	}
	hint := "[Y/n]"
	if !defaultYes {
		hint = "[y/N]"
	}
	fmt.Fprintf(p.out, "%s %s ", step, hint)
	if !p.scan.Scan() {
		return false
	}
	a := strings.ToLower(strings.TrimSpace(p.scan.Text()))
	if a == "" {
		return defaultYes
	}
	return a == "y" || a == "yes"
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
// missing, asking first unless prompt.yes says to skip asking. A public
// repository with no -label named gets one more question before that loop —
// what to call the gate label, defaulting to "ready" — since the report
// above never checked a label nobody named yet; the answer is appended to
// both defs and rows together, so the trailing len(defs) rows of rows keep
// lining up positionally with defs one-for-one (matching by name instead
// could collide with an unrelated row that happens to share the label's
// name).
//
// prompt is built once by the caller (runSetup) and shared with
// applySetupFiles — see setupPrompt's own doc comment for why two Scanners
// over the same stdin would silently drop buffered input.
//
// Mutates and returns rows so the caller's setupFailed check reflects what
// this pass actually created, without a second full read pass. gateLabel is
// the name chosen by the public-repo prompt below, or "" if that branch
// never ran — the caller's suggested `polako work` line was printed before
// this ran and so cannot have named a label nobody had chosen yet; this is
// how it finds out what to add.
func applySetup(ctx context.Context, prompt *setupPrompt, cfg config, rows []setupRow, defs []labelDef) ([]setupRow, string) {
	var gateLabel string
	// queueGate rather than a hand-rolled visibility check, the same reason
	// setup.go's setupRepoOKRow calls it instead of reimplementing it: this
	// note can never drift from what a real `polako work` run would refuse
	// on. cfg.label is always "" here (the branch below only widens it), so
	// this is exactly "public, no gate label, not -ungated".
	if cfg.label == "" && queueGate(cfg.visibility, cfg.label, false, "") != nil {
		gateLabel = prompt.name("this repository is public and has no gate label — name one to create", "ready")
		def := labelDef{name: gateLabel, color: "ededed", description: gateLabelDescription, required: true, isGateLabel: true}
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
			if isAlreadyExistsError(err) {
				// Someone else created it between the read pass and here —
				// a concurrent operator, or (with -policy-labels) an -label
				// that collided with a name dedupeLabelDefs let through
				// under a different required flag. Either way the label
				// exists now, which is what this step wanted.
				rows[ri] = setupRow{name: def.name, status: setupOK, required: def.required}
				fmt.Fprintf(prompt.out, "  %q already exists\n", def.name)
				continue
			}
			// Named rather than relayed: the likeliest cause by far is a
			// token without push access, and raw gh stderr for that is a
			// wall of JSON an operator has to decode to reach the same
			// conclusion.
			fmt.Fprintf(prompt.out, "  could not create %q — needs write access to %s\n", def.name, cfg.repo)
			continue
		}
		rows[ri] = setupRow{name: def.name, status: setupOK, required: def.required}
		fmt.Fprintf(prompt.out, "  created %q\n", def.name)
	}
	// A second pass, not folded into the loop above: this one acts on rows
	// the create loop skips outright (status is setupOK, not setupMissing) —
	// a gate label that already exists but predates setup's own marker, on
	// this run or an earlier one that named -label without -apply.
	for i, def := range defs {
		if !def.isGateLabel || rows[start+i].detail != unmarkedGateLabelDetail {
			continue
		}
		ri := start + i
		if !prompt.confirm(fmt.Sprintf("mark %q as the gate label?", def.name)) {
			continue
		}
		if err := ensureLabelMarked(ctx, cfg, def.name); err != nil {
			fmt.Fprintf(prompt.out, "  could not mark %q — needs write access to %s\n", def.name, cfg.repo)
			continue
		}
		rows[ri] = setupRow{name: def.name, status: setupOK, required: def.required}
		fmt.Fprintf(prompt.out, "  marked %q as the gate label\n", def.name)
	}
	return rows, gateLabel
}
