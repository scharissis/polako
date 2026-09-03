package main

import "testing"

// choose sorts the seven run reasons into two classes and resolves model and
// effort per the operator's cells: -remediation-* for a remediation run,
// -model/-effort otherwise, inherit when the relevant cell is empty.
func TestRunPolicyChoose(t *testing.T) {
	cases := []struct {
		name                string
		policy              runPolicy
		reason              string
		model, effort       string
		modelSrc, effortSrc string
	}{
		{
			name:   "implement with -model/-effort takes the flag cell",
			policy: runPolicy{model: "opus", effort: "high"},
			reason: reasonImplement,
			model:  "opus", effort: "high",
			modelSrc: sourceFlag, effortSrc: sourceFlag,
		},
		{
			name:   "implement ignores the remediation cell",
			policy: runPolicy{remediationModel: "sonnet", remediationEffort: "medium"},
			reason: reasonImplement,
			model:  "", effort: "",
			modelSrc: sourceInherit, effortSrc: sourceInherit,
		},
		{
			name:   "remediation prefers -remediation-*",
			policy: runPolicy{model: "opus", effort: "high", remediationModel: "sonnet", remediationEffort: "medium"},
			reason: reasonRemediate,
			model:  "sonnet", effort: "medium",
			modelSrc: sourceRemediation, effortSrc: sourceRemediation,
		},
		{
			name:   "remediation without its own cell falls back to -model/-effort",
			policy: runPolicy{model: "opus", effort: "high"},
			reason: reasonChecks,
			model:  "opus", effort: "high",
			modelSrc: sourceFlag, effortSrc: sourceFlag,
		},
		{
			name:   "remediation with nothing set inherits",
			policy: runPolicy{},
			reason: reasonReview,
			model:  "", effort: "",
			modelSrc: sourceInherit, effortSrc: sourceInherit,
		},
		{
			name:   "one cell set, the other inherits",
			policy: runPolicy{remediationEffort: "medium"},
			reason: reasonRemediate,
			model:  "", effort: "medium",
			modelSrc: sourceInherit, effortSrc: sourceRemediation,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.policy.choose(tc.reason)
			if got.model != tc.model || got.effort != tc.effort ||
				got.modelSource != tc.modelSrc || got.effortSource != tc.effortSrc {
				t.Errorf("choose(%q) = %+v, want model=%q effort=%q modelSrc=%q effortSrc=%q",
					tc.reason, got, tc.model, tc.effort, tc.modelSrc, tc.effortSrc)
			}
		})
	}
}

// resume, unfinished and answers are implementation class: a resumed
// implement run must not suddenly get the remediation cell.
func TestRunPolicyResumesAreImplementationClass(t *testing.T) {
	p := runPolicy{model: "opus", remediationModel: "sonnet"}
	for _, reason := range []string{reasonResume, reasonUnfinished, reasonAnswers} {
		if got := p.choose(reason); got.model != "opus" || got.modelSource != sourceFlag {
			t.Errorf("choose(%q) = %+v, want the -model cell, not the remediation one", reason, got)
		}
	}
}

func TestRunChoiceApply(t *testing.T) {
	cfg := config{model: "opus", effort: "high", skill: "x"}
	got := runChoice{model: "sonnet", effort: "medium"}.apply(cfg)
	if got.model != "sonnet" || got.effort != "medium" {
		t.Errorf("apply left model/effort = %q/%q, want sonnet/medium", got.model, got.effort)
	}
	if got.skill != "x" {
		t.Error("apply should return a copy, leaving every other field alone")
	}
	if cfg.model != "opus" {
		t.Error("apply mutated the caller's config")
	}
}

func TestRunChoiceDispatchLine(t *testing.T) {
	cases := []struct {
		name   string
		choice runChoice
		want   string
	}{
		{"both resolved", runChoice{model: "sonnet", effort: "medium", modelSource: sourceFlag, effortSource: sourceRemediation},
			"issue #42: model sonnet (flag), effort medium (remediation)"},
		{"one knob", runChoice{effort: "medium", modelSource: sourceInherit, effortSource: sourceRemediation},
			"issue #42: effort medium (remediation)"},
		{"both inherit", runChoice{modelSource: sourceInherit, effortSource: sourceInherit}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.choice.dispatchLine(42); got != tc.want {
				t.Errorf("dispatchLine = %q, want %q", got, tc.want)
			}
		})
	}
}
