package main

// The policy seam: which model and effort a run gets, and where that choice
// came from. It sorts the seven run reasons (metrics.go) into implementation
// and remediation classes and resolves a model, an effort and a source for
// each. Two levers on top of -model/-effort: a model:/effort: label on the
// issue (#363), a maintainer's judgement about this one ticket, which beats
// everything below it; and -remediation-model / -remediation-effort, which
// point a narrower remediation run (rebase, red-check fix, review reply)
// somewhere cheaper. Everything else resolves against -model/-effort, then
// inherit. #364 adds the epic's labels; size is still to be argued for — the
// source enum spells both now so a record's model_source / effort_source
// value set never has to grow.

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Where a run's model or effort was decided. epic and size are declared now
// but unused until #364 — see the file comment.
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
}

// labelChoice is what an issue's model: and effort: labels resolved to. A
// family with no label, an unreadable value, or two labels leaves its pair
// unset, and choose falls through to the flags. model:default is the one set
// value that is empty — an explicit "use the account default", which still
// stops resolution rather than letting -model through.
type labelChoice struct {
	model     string
	effort    string
	modelSet  bool
	effortSet bool
}

// modelLabelValue is the shape a model: label's value must match to be passed
// to the CLI — the CLI's own alias/id charset. Whether the name resolves is
// the CLI's business; a name polako refused would be a list it had to keep.
var modelLabelValue = regexp.MustCompile(`^[A-Za-z0-9._\[\]-]+$`)

// labelPolicy reads the model: and effort: families off an issue's labels. The
// prefix matches case-insensitively, the way GitHub treats label names; a
// model: value is checked only for shape and passed through, an effort: value
// against the closed level set. A typo or a second label of the same family
// warns and leaves that pair unset — picking one of two would be guessing
// which the maintainer meant.
func labelPolicy(labels []ghLabel) labelChoice {
	var lc labelChoice

	switch model := labelValues(labels, "model:"); {
	case len(model) == 0:
	case len(model) > 1:
		narrate(sevWarning, "issue carries %d model: labels (%s) — model falls through to the flags",
			len(model), strings.Join(model, ", "))
	case strings.EqualFold(model[0], "default"):
		lc.modelSet = true // set, but empty: the account default
	case modelLabelValue.MatchString(model[0]):
		lc.model, lc.modelSet = model[0], true
	default:
		narrate(sevWarning, "model:%s is not a valid model name — model falls through to the flags", model[0])
	}

	switch effort := labelValues(labels, "effort:"); {
	case len(effort) == 0:
	case len(effort) > 1:
		narrate(sevWarning, "issue carries %d effort: labels (%s) — effort falls through to the flags",
			len(effort), strings.Join(effort, ", "))
	case slices.Contains(effortLevels, effort[0]):
		lc.effort, lc.effortSet = effort[0], true
	default:
		narrate(sevWarning, "effort:%s is not a claude effort level — effort falls through to the flags", effort[0])
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
	}
}

// choose resolves the model and effort for a run of the given reason. The
// issue's model:/effort: label wins if it is set — a maintainer's call about
// this ticket outranks the operator's flags and the remediation cell alike.
// model:default lands here as set-but-empty: it stops resolution at inherit
// rather than falling through to -model. Then, for a remediation run,
// -remediation-* if set; then -model/-effort; then inherit.
func (p runPolicy) choose(reason string) runChoice {
	remediation := remediationReasons[reason]
	pick := func(labelVal string, labelSet bool, remediationCell, baseCell string) (string, string) {
		if labelSet {
			if labelVal == "" {
				return "", sourceInherit // model:default
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
	m, ms := pick(p.labels.model, p.labels.modelSet, p.remediationModel, p.model)
	e, es := pick(p.labels.effort, p.labels.effortSet, p.remediationEffort, p.effort)
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
