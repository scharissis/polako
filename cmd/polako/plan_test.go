package main

// `polako plan` resolves a vision document, runs its preflight probes, then
// either prints the invocation a run would make (-dry-run) or makes it: spawn
// the skill, cap it at -max-issues, and normalise every issue it created to
// carry exactly the `proposed` label. Every case here runs on the same fake
// `gh` and fake `claude` the drain loop does — no network, no real gh, no claude.

import (
	"context"
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

// planRunConfig is planTestConfig plus what a real run needs the preflight
// would otherwise fill in: the resolved repository, and a fake claude in the
// mode the case wants.
func planRunConfig(t *testing.T, st *ghState, claudeMode string) (config, string) {
	t.Helper()
	cfg, statePath, checkout := planTestConfig(t, st)
	cfg.repo, cfg.ghRepo = "example/repo", "example/repo"
	t.Setenv(fakeClaudeEnv, claudeMode)
	writeVision(t, checkout, "VISION.md")
	return cfg, statePath
}

// The label pass forces every issue the run created to carry *exactly*
// proposedLabel — a missing one added, any other stripped — attaches the batch
// milestone to the ones without it, and leaves another account's issue alone.
func TestPlanNormaliseForcesExactlyProposed(t *testing.T) {
	cfg, statePath, _ := planTestConfig(t, &ghState{
		Labels:     []string{proposedLabel, "enhancement"},
		Milestones: []string{"Batch 1"},
		Issues: map[string]*fakeIssue{
			"1":  {Open: true},                                                             // there before the run
			"10": {Open: true, Mine: true},                                                 // new, the skill forgot the label
			"11": {Open: true, Mine: true, Labels: []string{proposedLabel}},                // new, already right
			"12": {Open: true, Mine: true, Labels: []string{proposedLabel, "enhancement"}}, // new, a label smuggled on
			"13": {Open: true, Labels: []string{"enhancement"}},                            // new, another account's
		},
	})
	cfg.repo, cfg.ghRepo = "example/repo", "example/repo"

	out := planNormalise(context.Background(), cfg, map[int]bool{1: true}, "Batch 1")
	if out.err() != nil {
		t.Fatalf("planNormalise reported failures on a healthy pass: %v", out.err())
	}
	if out.created != 3 || len(out.labelled) != 3 || out.stripped != 1 || len(out.milestone) != 3 {
		t.Errorf("outcome = %+v, want created 3 / labelled 3 / stripped 1 / milestone 3", out)
	}

	st, err := readGhState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"10", "11", "12"} {
		is := st.Issues[n]
		if !slices.Equal(is.Labels, []string{proposedLabel}) {
			t.Errorf("#%s labels = %v, want exactly [%s]", n, is.Labels, proposedLabel)
		}
		if is.Milestone != "Batch 1" {
			t.Errorf("#%s milestone = %q, want %q", n, is.Milestone, "Batch 1")
		}
	}
	if is := st.Issues["13"]; !slices.Equal(is.Labels, []string{"enhancement"}) || is.Milestone != "" {
		t.Errorf("#13 was touched: labels %v, milestone %q — another account's issue must be left alone",
			is.Labels, is.Milestone)
	}
}

// A label edit that does not take is collected, surfaced, and turned into a
// nonzero exit — never swallowed, because an unlabelled proposal a drain would
// pick up is the worst thing a plan run can leave behind.
func TestPlanNormaliseReportsLabelFailuresLoudly(t *testing.T) {
	// The repository never declared proposedLabel, so `gh issue edit --add-label`
	// fails exactly as GitHub's would.
	cfg, _, _ := planTestConfig(t, &ghState{
		Issues: map[string]*fakeIssue{"10": {Open: true, Mine: true}},
	})
	cfg.repo, cfg.ghRepo = "example/repo", "example/repo"

	out := planNormalise(context.Background(), cfg, map[int]bool{}, "")
	if len(out.failures) == 0 || out.err() == nil {
		t.Fatalf("a failed label add was swallowed: %+v", out)
	}
	for _, want := range []string{"#10", "unguarded"} {
		if !strings.Contains(out.err().Error(), want) {
			t.Errorf("the loud error omits %q: %v", want, out.err())
		}
	}
	if slices.Contains(out.labelled, 10) {
		t.Error("#10 was counted as normalised despite the add failing")
	}
}

// End to end: a real run spawns the skill through execClaude and the pass
// normalises what it filed. The fake skill creates three proposals, only one
// of them labelled.
func TestPlanRunSpawnsTheSkillAndNormalisesWhatItCreated(t *testing.T) {
	buf := captureLog(t)
	cfg, statePath := planRunConfig(t, &ghState{Labels: []string{proposedLabel}}, "plan")

	opt := planOptions{vision: "VISION.md", maxIssues: 10}
	cfg.maxIssues = opt.maxIssues
	if err := planRun(context.Background(), cfg, opt, "VISION", io.Discard); err != nil {
		t.Fatalf("planRun: %v", err)
	}

	st, err := readGhState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	mine := 0
	for _, is := range st.Issues {
		if !is.Mine {
			continue
		}
		mine++
		if !slices.Equal(is.Labels, []string{proposedLabel}) {
			t.Errorf("a created issue carries %v, want exactly [%s]", is.Labels, proposedLabel)
		}
		if is.Milestone != "VISION" {
			t.Errorf("a created issue has milestone %q, want %q", is.Milestone, "VISION")
		}
	}
	if mine != 3 {
		t.Errorf("the run created %d issues, want 3", mine)
	}
	if !strings.Contains(buf.String(), "3 issues created, 3 normalised to "+proposedLabel) {
		t.Errorf("the pass summary is missing from the log:\n%s", buf.String())
	}
}

// The cap: dispatchClaude kills the run at -max-issues, planRun reports it
// rather than raising it, and the label pass still normalises everything that
// was filed. Nothing is closed.
func TestPlanRunCapsIssueCreationAndStillNormalises(t *testing.T) {
	buf := captureLog(t)
	cfg, statePath := planRunConfig(t, &ghState{Labels: []string{proposedLabel}}, "plancap")

	opt := planOptions{vision: "VISION.md", maxIssues: 3}
	cfg.maxIssues = opt.maxIssues
	if err := planRun(context.Background(), cfg, opt, "", io.Discard); err != nil {
		t.Fatalf("a cap hit is reported, not raised: %v", err)
	}

	st, err := readGhState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	created := 0
	for _, is := range st.Issues {
		if !is.Mine {
			continue
		}
		created++
		if !is.Open {
			t.Error("the cap closed an issue — it must be loud, never destructive")
		}
		if !slices.Contains(is.Labels, proposedLabel) {
			t.Errorf("a capped run's issue is unlabelled: %v", is.Labels)
		}
	}
	if created < opt.maxIssues {
		t.Errorf("the fake skill filed %d issues, want at least the cap of %d", created, opt.maxIssues)
	}
	if !strings.Contains(buf.String(), "-max-issues") {
		t.Errorf("the log does not mention the cap:\n%s", buf.String())
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
