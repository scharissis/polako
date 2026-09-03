package main

// The policy seam: which model and effort a run gets, and where that choice
// came from. It sorts the seven run reasons (metrics.go) into two classes —
// implementation and remediation — and resolves a model, an effort and a
// source for each.
//
// Today it holds one lever the CLI does not: a remediation run — a rebase, a
// red-check fix, a review reply — is a shorter, narrower task than
// implementing an issue, so -remediation-model / -remediation-effort can point
// it somewhere cheaper. Everything else resolves against the -model/-effort
// cell, then to inherit (the flag omitted, the CLI's own default).
//
// #363 and #364 add label, epic and size inputs. The source enum spells them
// now so a record's model_source / effort_source value set is whole and those
// tickets add no new spellings.

import (
	"fmt"
	"strings"
)

// Where a run's model or effort was decided. label, epic and size are declared
// now but unused until #363/#364 — see the file comment.
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
// back to, and the -remediation-* pair a remediation run prefers.
type runPolicy struct {
	model             string
	effort            string
	remediationModel  string
	remediationEffort string
}

func newRunPolicy(cfg config) runPolicy {
	return runPolicy{
		model:             cfg.model,
		effort:            cfg.effort,
		remediationModel:  cfg.remediationModel,
		remediationEffort: cfg.remediationEffort,
	}
}

// choose resolves the model and effort for a run of the given reason.
// Remediation run: -remediation-* if set, else -model/-effort, else inherit.
// Implementation run: -model/-effort if set, else inherit.
func (p runPolicy) choose(reason string) runChoice {
	remediation := remediationReasons[reason]
	pick := func(remediationCell, baseCell string) (string, string) {
		if remediation && remediationCell != "" {
			return remediationCell, sourceRemediation
		}
		if baseCell != "" {
			return baseCell, sourceFlag
		}
		return "", sourceInherit
	}
	m, ms := pick(p.remediationModel, p.model)
	e, es := pick(p.remediationEffort, p.effort)
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
