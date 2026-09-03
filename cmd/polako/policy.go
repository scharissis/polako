package main

// The policy seam: which model and effort a run gets, and where that choice
// came from. It sorts the seven run reasons (metrics.go) into implementation
// and remediation classes and resolves a model, an effort and a source for
// each. Two levers on top of -model/-effort: a model:/effort: label on the
// issue (#363), a maintainer's judgement about this one ticket, which beats
// everything below it; and -remediation-model / -remediation-effort, which
// point a narrower remediation run (rebase, red-check fix, review reply)
// somewhere cheaper. Everything else resolves against -model/-effort, then
// inherit. #364 added the epic's labels — a family an issue leaves unset is
// filled from its parent's own model:/effort: labels. Below the label and
// above the flag sits -effort-by-size (#366): an implementation run's effort
// keyed off the issue body's Estimate: line, off by default.

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Where a run's model or effort was decided — see the file comment.
const (
	sourceInherit     = "inherit"
	sourceFlag        = "flag"
	sourceRemediation = "remediation"
	sourceLabel       = "label"
	sourceEpic        = "epic"
	sourceSize        = "size"
)

// remediationReasons are the run reasons that resolve against the remediation
// cell. Every other reason is implementation class.
var remediationReasons = map[string]bool{
	reasonRemediate: true,
	reasonChecks:    true,
	reasonReview:    true,
}

// runChoice is what the policy resolved for one run: a model, an effort, and
// the source of each. It rides on runContext so newRunRecord reads the
// record's requested_* and *_source off it rather than off cfg — which for a
// remediation run are not necessarily cfg.model / cfg.effort.
type runChoice struct {
	model        string
	effort       string
	modelSource  string
	effortSource string
}

// runPolicy is the operator's cells: the -model/-effort pair every run falls
// back to, and the -remediation-* pair a remediation run prefers — plus the
// model:/effort: labels on the issue, resolved once at pickup (issue.go) and
// beating both.
type runPolicy struct {
	model             string
	effort            string
	remediationModel  string
	remediationEffort string
	labels            labelChoice
	// size is the issue body's Estimate: letter (S/M/L, or "" when the flag is
	// off or the body carries no line); sizeEffort is -effort-by-size parsed
	// into size→level. An implementation-class run whose size has a cell takes
	// that cell's effort — below an effort: label, above the -effort flag.
	size       string
	sizeEffort map[string]string
}

// labelChoice is what an issue's model: and effort: labels resolved to. A
// family with no label, an unreadable value, or two labels leaves its pair
// unset, and choose falls through to the flags. model:default is the one set
// value that is empty — an explicit "use the account default", which still
// stops resolution rather than letting -model through.
//
// modelFromEpic / effortFromEpic mark a family the issue left unset that its
// parent epic's own labels filled (#364) — choose names that source epic
// rather than label.
type labelChoice struct {
	model          string
	effort         string
	modelSet       bool
	effortSet      bool
	modelFromEpic  bool
	effortFromEpic bool
}

// complete reports whether the issue settled both families itself, so its
// parent epic need not be read at all.
func (lc labelChoice) complete() bool { return lc.modelSet && lc.effortSet }

// inheritFrom fills any family the issue left unset from its parent epic's own
// model:/effort: labels, marking it epic-sourced. A family the issue settled
// itself — model:default included, which is the child's deliberate escape from
// the epic — is left untouched.
func (lc labelChoice) inheritFrom(epic labelChoice) labelChoice {
	if !lc.modelSet && epic.modelSet {
		lc.model, lc.modelSet, lc.modelFromEpic = epic.model, true, true
	}
	if !lc.effortSet && epic.effortSet {
		lc.effort, lc.effortSet, lc.effortFromEpic = epic.effort, true, true
	}
	return lc
}

// modelLabelValue is the shape a model: label's value must match to be passed
// to the CLI — the CLI's own alias/id charset. Whether the name resolves is
// the CLI's business; a name polako refused would be a list it had to keep.
var modelLabelValue = regexp.MustCompile(`^[A-Za-z0-9._\[\]-]+$`)

// labelPolicy reads the model: and effort: families off one issue's labels —
// the issue being worked or, through inheritFrom, its parent epic. The prefix
// matches case-insensitively, the way GitHub treats label names; a model: value
// is checked only for shape and passed through, an effort: value against the
// closed level set. A typo or a second label of the same family warns and
// leaves that pair unset — picking one of two would be guessing which the
// maintainer meant. "Falls through" then means the epic's own label if the
// issue has a parent that carries one, and the -model/-effort flags otherwise.
func labelPolicy(labels []ghLabel) labelChoice {
	var lc labelChoice

	switch model := labelValues(labels, "model:"); {
	case len(model) == 0:
	case len(model) > 1:
		narrate(sevWarning, "issue carries %d model: labels (%s) — model falls through",
			len(model), strings.Join(model, ", "))
	case strings.EqualFold(model[0], "default"):
		lc.modelSet = true // set, but empty: the account default
	case modelLabelValue.MatchString(model[0]):
		lc.model, lc.modelSet = model[0], true
	default:
		narrate(sevWarning, "model:%s is not a valid model name — model falls through", model[0])
	}

	switch effort := labelValues(labels, "effort:"); {
	case len(effort) == 0:
	case len(effort) > 1:
		narrate(sevWarning, "issue carries %d effort: labels (%s) — effort falls through",
			len(effort), strings.Join(effort, ", "))
	case slices.Contains(effortLevels, effort[0]):
		lc.effort, lc.effortSet = effort[0], true
	default:
		narrate(sevWarning, "effort:%s is not a claude effort level — effort falls through", effort[0])
	}

	return lc
}

// labelValues returns the suffix of every label whose name begins with prefix,
// matched case-insensitively on the prefix alone. The suffix is trimmed, so
// `model: opus` — the spacing GitHub's label UI nudges toward — resolves like
// `model:opus` rather than failing the shape check.
func labelValues(labels []ghLabel, prefix string) []string {
	var vs []string
	for _, l := range labels {
		if len(l.Name) >= len(prefix) && strings.EqualFold(l.Name[:len(prefix)], prefix) {
			vs = append(vs, strings.TrimSpace(l.Name[len(prefix):]))
		}
	}
	return vs
}

func newRunPolicy(cfg config) runPolicy {
	return runPolicy{
		model:             cfg.model,
		effort:            cfg.effort,
		remediationModel:  cfg.remediationModel,
		remediationEffort: cfg.remediationEffort,
		sizeEffort:        cfg.sizeEffort,
	}
}

// estimateLine is plan-backlog's per-ticket size signal: `Estimate:` then one
// of S/M/L on a line of its own. Anchored and enum-bounded on purpose — this
// is the only issue body text the supervisor reads, docs/hardening.md lists
// bodies as attacker-editable, and the match only picks which operator-set
// -effort-by-size cell applies. A hand-written issue owes no line, so no match
// is silent, not a warning.
var estimateLine = regexp.MustCompile(`(?m)^Estimate:\s*([SML])\b`)

func sizeFromBody(body string) string {
	if m := estimateLine.FindStringSubmatch(body); m != nil {
		return m[1]
	}
	return ""
}

// choose resolves the model and effort for a run of the given reason. A
// model:/effort: label wins if it is set — a maintainer's call about this
// ticket outranks the operator's flags and the remediation cell alike — and
// is named source label when the issue carries it, source epic when it came
// from the issue's parent (#364). model:default lands here as set-but-empty:
// it stops resolution at inherit rather than falling through to -model. Then,
// for a remediation run, -remediation-* if set; then -model/-effort; then
// -effort-by-size (#366, effort only); then inherit.
func (p runPolicy) choose(reason string) runChoice {
	remediation := remediationReasons[reason]
	pick := func(labelVal string, labelSet, fromEpic bool, remediationCell, baseCell string) (string, string) {
		if labelSet {
			if labelVal == "" {
				return "", sourceInherit // model:default, the issue's or the epic's
			}
			if fromEpic {
				return labelVal, sourceEpic
			}
			return labelVal, sourceLabel
		}
		if remediation && remediationCell != "" {
			return remediationCell, sourceRemediation
		}
		if baseCell != "" {
			return baseCell, sourceFlag
		}
		return "", sourceInherit
	}
	m, ms := pick(p.labels.model, p.labels.modelSet, p.labels.modelFromEpic, p.remediationModel, p.model)
	e, es := pick(p.labels.effort, p.labels.effortSet, p.labels.effortFromEpic, p.remediationEffort, p.effort)
	// Effort by size: one rung below a maintainer's effort: label (the issue's
	// own or its epic's), above the -effort flag, and only on an
	// implementation-class run — a remediation run keeps the cell pick() just
	// gave it (#362's seam). The issue body chooses which -effort-by-size cell
	// applies, never a level directly.
	if !remediation && !p.labels.effortSet {
		if cell, ok := p.sizeEffort[p.size]; ok {
			e, es = cell, sourceSize
		}
	}
	return runChoice{model: m, effort: e, modelSource: ms, effortSource: es}
}

// apply returns a copy of cfg with model and effort set to this choice, the
// way issueRun and remediateReview already copy cfg for a per-run tweak.
func (c runChoice) apply(cfg config) config {
	cfg.model = c.model
	cfg.effort = c.effort
	return cfg
}

// dispatchLine is the one line a dispatch logs, and only when something other
// than inherit resolved: "issue #42: model sonnet (flag), effort medium
// (remediation)". Empty when both knobs inherit, and the caller logs nothing.
func (c runChoice) dispatchLine(issue int) string {
	var parts []string
	if c.modelSource != sourceInherit {
		parts = append(parts, fmt.Sprintf("model %s (%s)", c.model, c.modelSource))
	}
	if c.effortSource != sourceInherit {
		parts = append(parts, fmt.Sprintf("effort %s (%s)", c.effort, c.effortSource))
	}
	if len(parts) == 0 {
		return ""
	}
	return fmt.Sprintf("issue #%d: %s", issue, strings.Join(parts, ", "))
}
