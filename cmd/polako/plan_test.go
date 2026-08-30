package main

// `polako plan` is a skeleton: it resolves a vision document, runs its
// preflight probes, and either prints the invocation a run would make
// (-dry-run) or refuses until #103 lands the run path. Every case here runs on
// the same fake `gh` the drain loop does — no network, no real gh, no claude.

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// planTestConfig builds the config planPreflight and planDryRun take, pointed
// at a fake gh and a real (temp) checkout — real git against a t.TempDir()
// breaks no hermetic rule, and preflight needs a checkout to stand in.
func planTestConfig(t *testing.T, st *ghState) (cfg config, statePath, checkout string) {
	t.Helper()
	drainCfg, statePath := drainConfig(t, "stream", st)
	_, checkout = upstream(t)
	cfg = config{
		dir:            checkout,
		ghBin:          drainCfg.ghBin,
		claudeBin:      drainCfg.claudeBin,
		ghRetryWait:    time.Millisecond,
		skill:          defaultPlanSkill,
		model:          "opus",
		permissionMode: "acceptEdits",
		tools:          planTools,
		queue:          new(queueMemo),
	}
	return cfg, statePath, checkout
}

func writeVision(t *testing.T, checkout, rel string) {
	t.Helper()
	full := filepath.Join(checkout, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("# Vision\n\nsomewhere to go.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The bare invocation's verb table has to list plan now that it exists — the
// usage never advertises a verb that errors, and never omits one that works.
func TestVerbUsageListsPlan(t *testing.T) {
	var b strings.Builder
	verbUsage(&b)
	if !strings.Contains(b.String(), "\n  plan ") {
		t.Errorf("verbUsage does not list `plan`:\n%s", b.String())
	}
}

// Exactly one of -vision / -brief, and never a does-the-file-exist heuristic on
// -vision: a typo'd path has to fail loudly rather than become "no document".
func TestPlanConfigRequiresExactlyOneSource(t *testing.T) {
	if _, err := planConfig(&planOptions{maxIssues: 10}); err == nil {
		t.Error("planConfig accepted neither -vision nor -brief")
	}
	if _, err := planConfig(&planOptions{vision: "V.md", brief: "a horse app", maxIssues: 10}); err == nil {
		t.Error("planConfig accepted both -vision and -brief")
	}
	if _, err := planConfig(&planOptions{vision: "docs/V.md", maxIssues: 10}); err != nil {
		t.Errorf("planConfig rejected -vision alone: %v", err)
	}
	if _, err := planConfig(&planOptions{brief: "a dating app for horses", maxIssues: 10}); err != nil {
		t.Errorf("planConfig rejected -brief alone: %v", err)
	}
	long := strings.Repeat("x ", planBriefMax)
	if _, err := planConfig(&planOptions{brief: long, maxIssues: 10}); err == nil {
		t.Error("planConfig accepted a -brief long enough to be a document")
	}
}

// The milestone title: -milestone verbatim, "" for "off", else derived from the
// document name or the brief's opening words.
func TestPlanMilestoneTitle(t *testing.T) {
	cases := []struct {
		opt  planOptions
		want string
	}{
		{planOptions{milestone: "Batch 3"}, "Batch 3"},
		{planOptions{milestone: "off", vision: "docs/VISION.md"}, ""},
		{planOptions{vision: "docs/roadmap-2026.md"}, "roadmap-2026"},
		{planOptions{brief: "a dating app for horses, with barn matching and hay reviews"}, "a dating app for horses, with barn matching"},
	}
	for _, tc := range cases {
		if got := planMilestoneTitle(&tc.opt); got != tc.want {
			t.Errorf("planMilestoneTitle(%+v) = %q, want %q", tc.opt, got, tc.want)
		}
	}
}

// The capability probe reads `gh issue create --help`: a gh that lists
// `--parent` files hierarchically, one that does not works flat.
func TestPlanPreflightProbesParentSupport(t *testing.T) {
	newGh := func(st *ghState) (config, *planOptions, string) {
		cfg, _, checkout := planTestConfig(t, st)
		writeVision(t, checkout, "VISION.md")
		return cfg, &planOptions{vision: "VISION.md", milestone: "off", maxIssues: 10, dryRun: true}, checkout
	}

	cfg, opt, _ := newGh(&ghState{})
	if _, hierarchical, err := planPreflight(context.Background(), &cfg, opt); err != nil {
		t.Fatalf("planPreflight: %v", err)
	} else if !hierarchical {
		t.Error("probe missed `--parent` in a modern gh's help")
	}

	oldCfg, oldOpt, _ := newGh(&ghState{NoSubIssues: true})
	if _, hierarchical, err := planPreflight(context.Background(), &oldCfg, oldOpt); err != nil {
		t.Fatalf("planPreflight (old gh): %v", err)
	} else if hierarchical {
		t.Error("probe reported `--parent` on a gh whose help omits it")
	}
}

// A missing document is a loud, advice-carrying failure, not a silent fallback.
func TestPlanPreflightFailsWithAdvice(t *testing.T) {
	cfg, _, _ := planTestConfig(t, &ghState{})
	_, _, err := planPreflight(context.Background(), &cfg,
		&planOptions{vision: "docs/not-here.md", milestone: "off", maxIssues: 10, dryRun: true})
	if err == nil {
		t.Fatal("planPreflight accepted a -vision path with no file behind it")
	}
	for _, want := range []string{"not-here.md", "-brief"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("failure does not mention %q: %v", want, err)
		}
	}
}

// ensureMilestone is find-or-create and nothing more: a title that already
// exists is left untouched, an absent one is POSTed.
func TestEnsureMilestoneIsIdempotent(t *testing.T) {
	cfg, statePath, _ := planTestConfig(t, &ghState{Milestones: []string{"Roadmap Q3"}})
	cfg.repo, cfg.ghRepo = "example/repo", "example/repo"

	if err := ensureMilestone(context.Background(), cfg, "Roadmap Q3"); err != nil {
		t.Fatalf("ensureMilestone on an existing title: %v", err)
	}
	st, err := readGhState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Milestones) != 1 {
		t.Errorf("ensureMilestone re-created an existing milestone: %v", st.Milestones)
	}

	if err := ensureMilestone(context.Background(), cfg, "New Batch"); err != nil {
		t.Fatalf("ensureMilestone on an absent title: %v", err)
	}
	if st, _ = readGhState(statePath); !slices.Contains(st.Milestones, "New Batch") {
		t.Errorf("ensureMilestone did not create an absent milestone: %v", st.Milestones)
	}
}

// A real run's preflight declares the gate: the `proposed` label GitHub would
// otherwise refuse, and the batch milestone the run attaches issues to.
// `-milestone off` skips only the milestone.
func TestPlanPreflightDeclaresTheGateForARealRun(t *testing.T) {
	cfg, statePath, checkout := planTestConfig(t, &ghState{})
	writeVision(t, checkout, "VISION.md")

	if _, _, err := planPreflight(context.Background(), &cfg,
		&planOptions{vision: "VISION.md", maxIssues: 10}); err != nil {
		t.Fatalf("planPreflight: %v", err)
	}
	st, err := readGhState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(st.Labels, proposedLabel) {
		t.Errorf("preflight did not declare the %s label: %v", proposedLabel, st.Labels)
	}
	if !slices.Contains(st.Milestones, "VISION") {
		t.Errorf("preflight did not create the batch milestone: %v", st.Milestones)
	}

	offCfg, offState, offCheckout := planTestConfig(t, &ghState{})
	writeVision(t, offCheckout, "VISION.md")
	if _, _, err := planPreflight(context.Background(), &offCfg,
		&planOptions{vision: "VISION.md", milestone: "off", maxIssues: 10}); err != nil {
		t.Fatalf("planPreflight -milestone off: %v", err)
	}
	if st, _ = readGhState(offState); len(st.Milestones) != 0 {
		t.Errorf("-milestone off still created a milestone: %v", st.Milestones)
	} else if !slices.Contains(st.Labels, proposedLabel) {
		t.Errorf("-milestone off dropped the label too: %v", st.Labels)
	}
}

// -dry-run's promise: it prints the invocation a run would make and does none
// of it — no label, no milestone, no change to the repository at all.
func TestPlanDryRunWritesNothingAndPrintsTheInvocation(t *testing.T) {
	cfg, statePath, checkout := planTestConfig(t, &ghState{})
	writeVision(t, checkout, "docs/VISION.md")
	before, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}

	opt := planOptions{vision: "docs/VISION.md", focus: "the observability section", maxIssues: 7, dryRun: true}
	buf := captureLog(t)
	milestone, hierarchical, err := planPreflight(context.Background(), &cfg, &opt)
	if err != nil {
		t.Fatalf("planPreflight: %v", err)
	}

	var out strings.Builder
	if err := planDryRun(cfg, opt, milestone, hierarchical, &out); err != nil {
		t.Fatalf("planDryRun: %v", err)
	}

	after, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Errorf("a dry run changed the repository:\nbefore %s\nafter  %s", before, after)
	}
	st, err := readGhState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Labels) != 0 || len(st.Milestones) != 0 {
		t.Errorf("a dry run declared something: labels %v, milestones %v", st.Labels, st.Milestones)
	}

	printed := strings.TrimSpace(out.String())
	if strings.Contains(printed, "\n") {
		t.Errorf("want one invocation and nothing else on stdout, got:\n%s", printed)
	}
	for _, want := range []string{
		`'/polako:plan-backlog docs/VISION.md "the observability section"'`,
		"--model opus",
		"Bash(gh issue create:*)",
		"--output-format stream-json",
	} {
		if !strings.Contains(printed, want) {
			t.Errorf("printed invocation is missing %q\ngot: %s", want, printed)
		}
	}
	// The plan allowlist is a fraction of the drain's: nothing that writes code.
	for _, unwanted := range []string{"Bash(go:*)", "gh pr create", "gh issue edit", "gh api"} {
		if strings.Contains(printed, unwanted) {
			t.Errorf("plan invocation carries %q, which its allowlist must not", unwanted)
		}
	}

	said := buf.String()
	for _, want := range []string{
		"focus: the observability section",
		"issue cap: 7",
		`milestone: "VISION"`,
		"dry run — no proposed label",
	} {
		if !strings.Contains(said, want) {
			t.Errorf("narration is missing %q\ngot:\n%s", want, said)
		}
	}
}

// The refusal a non-dry run gets is the operator's whole briefing: it names the
// issue the run path arrives with, and the flag that works meanwhile.
func TestPlanRefusalNamesTheFollowUp(t *testing.T) {
	for _, want := range []string{"#103", "dry-run"} {
		if !strings.Contains(planNotRunnableErr.Error(), want) {
			t.Errorf("planNotRunnableErr does not mention %q: %v", want, planNotRunnableErr)
		}
	}
}

// A real (non-dry) invocation refuses ahead of preflight, so a forgotten
// -dry-run against an unfamiliar repo touches nothing there — no gh call is
// made at all. The bare "gh" in the config would fail loudly if one were.
func TestPlanRefusesARealRunBeforeTouchingGitHub(t *testing.T) {
	err := runPlan(context.Background(), []string{"-vision", "docs/VISION.md"}, io.Discard)
	if !errors.Is(err, planNotRunnableErr) {
		t.Fatalf("a non-dry `polako plan` returned %v, want the #103 refusal", err)
	}
}

// The -skill default resolves to the skill this repo actually ships.
func TestPlanSkillDefaultMatchesTheShippedSkill(t *testing.T) {
	if defaultPlanSkill != "polako:"+planSkillDir {
		t.Fatalf("defaultPlanSkill = %q, want polako:%s", defaultPlanSkill, planSkillDir)
	}
	if _, err := os.Stat(filepath.Join(repoRoot(), "skills", planSkillDir, "SKILL.md")); err != nil {
		t.Errorf("-skill defaults to %q but skills/%s/SKILL.md is not there: %v", defaultPlanSkill, planSkillDir, err)
	}
}
