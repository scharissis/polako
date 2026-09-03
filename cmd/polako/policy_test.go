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

func labelsFrom(names ...string) []ghLabel {
	ls := make([]ghLabel, len(names))
	for i, n := range names {
		ls[i] = ghLabel{Name: n}
	}
	return ls
}

// labelPolicy reads model:/effort: off an issue's labels: prefix matched
// case-insensitively, model value passed through if it is shaped like a name,
// effort checked against the closed set, a typo or a duplicate warned and
// dropped, model:default set-but-empty.
func TestLabelPolicy(t *testing.T) {
	captureLog(t)
	cases := []struct {
		name                string
		labels              []string
		model, effort       string
		modelSet, effortSet bool
	}{
		{name: "no labels"},
		{name: "effort:low", labels: []string{"effort:low"}, effort: "low", effortSet: true},
		{name: "model passed through", labels: []string{"model:opus"}, model: "opus", modelSet: true},
		{name: "full id passed through", labels: []string{"model:claude-opus-4-1"}, model: "claude-opus-4-1", modelSet: true},
		{name: "both families", labels: []string{"model:sonnet", "effort:high"},
			model: "sonnet", modelSet: true, effort: "high", effortSet: true},
		{name: "case-insensitive prefix", labels: []string{"MODEL:opus", "Effort:max"},
			model: "opus", modelSet: true, effort: "max", effortSet: true},
		{name: "space after the colon is trimmed", labels: []string{"model: sonnet", "effort: high"},
			model: "sonnet", modelSet: true, effort: "high", effortSet: true},
		{name: "model:default is set but empty", labels: []string{"model:default"}, modelSet: true},
		{name: "model:DEFAULT too", labels: []string{"model:DEFAULT"}, modelSet: true},
		{name: "two model labels drop the pair", labels: []string{"model:opus", "model:sonnet"}},
		{name: "two effort labels drop the pair", labels: []string{"effort:low", "effort:high"}},
		{name: "effort typo drops the pair", labels: []string{"effort:medim"}},
		{name: "bad model shape drops the pair", labels: []string{"model:opus!"}},
		{name: "unrelated labels ignored", labels: []string{"bug", "needs-human"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := labelPolicy(labelsFrom(tc.labels...))
			if got.model != tc.model || got.modelSet != tc.modelSet ||
				got.effort != tc.effort || got.effortSet != tc.effortSet {
				t.Errorf("labelPolicy(%v) = %+v, want model=%q(%v) effort=%q(%v)",
					tc.labels, got, tc.model, tc.modelSet, tc.effort, tc.effortSet)
			}
		})
	}
}

// A model:/effort: label is level 1: it beats the -model/-effort flags and the
// -remediation-* cell alike. model:default stops resolution at inherit rather
// than falling through to -model.
func TestRunPolicyLabelBeatsFlagAndRemediation(t *testing.T) {
	p := runPolicy{
		model: "opus", effort: "high",
		remediationModel: "sonnet", remediationEffort: "medium",
		labels: labelChoice{model: "haiku", modelSet: true, effort: "low", effortSet: true},
	}
	for _, reason := range []string{reasonImplement, reasonRemediate} {
		got := p.choose(reason)
		if got.model != "haiku" || got.modelSource != sourceLabel ||
			got.effort != "low" || got.effortSource != sourceLabel {
			t.Errorf("choose(%q) = %+v, want the label cell for both knobs", reason, got)
		}
	}

	def := runPolicy{model: "opus", labels: labelChoice{modelSet: true}}.choose(reasonImplement)
	if def.model != "" || def.modelSource != sourceInherit {
		t.Errorf("model:default choose = %+v, want empty model / inherit (no --model)", def)
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

// The six source strings are what a run record's model_source / effort_source
// carry, and a stats reader keys on them. epic/size are unused until #364;
// pinning the whole set now means that ticket adds code, not a vocabulary a
// record consumer has to relearn.
func TestSourceConstantsAreStable(t *testing.T) {
	for got, want := range map[string]string{
		sourceInherit: "inherit", sourceFlag: "flag", sourceRemediation: "remediation",
		sourceLabel: "label", sourceEpic: "epic", sourceSize: "size",
	} {
		if got != want {
			t.Errorf("source constant = %q, want %q", got, want)
		}
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
