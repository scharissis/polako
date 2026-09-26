package main

// `polako plan` resolves a design document, runs its preflight probes, then
// either prints the invocation a run would make (-dry-run) or makes it: spawn
// the skill, cap it at -max-issues, and normalise every issue it created to
// carry exactly the `proposed` label. Every case here runs on the same fake
// `gh` and fake `claude` the drain loop does — no network, no real gh, no claude.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
		env:            slices.Clone(drainCfg.env), // the fake gh and claude handshake, for the child
		ui:             testUI(t),
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

func writeDesignFile(t *testing.T, checkout, rel string) {
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
	t.Parallel()
	var b strings.Builder
	verbUsage(&b)
	if !strings.Contains(b.String(), "\n  plan ") {
		t.Errorf("verbUsage does not list `plan`:\n%s", b.String())
	}
}

// Exactly one of -design / -brief, and never a does-the-file-exist heuristic on
// -design: a typo'd path has to fail loudly rather than become "no document".
func TestPlanConfigRequiresExactlyOneSource(t *testing.T) {
	t.Parallel()
	sane := intakeOptions{maxIssues: 10}
	if _, err := planConfig(&planOptions{intakeOptions: sane}); err == nil {
		t.Error("planConfig accepted neither -design nor -brief")
	}
	if _, err := planConfig(&planOptions{intakeOptions: sane, design: "V.md", brief: "a horse app"}); err == nil {
		t.Error("planConfig accepted both -design and -brief")
	}
	if _, err := planConfig(&planOptions{intakeOptions: sane, design: "docs/V.md"}); err != nil {
		t.Errorf("planConfig rejected -design alone: %v", err)
	}
	if _, err := planConfig(&planOptions{intakeOptions: sane, brief: "a dating app for horses"}); err != nil {
		t.Errorf("planConfig rejected -brief alone: %v", err)
	}
	long := strings.Repeat("x ", planBriefMax)
	if _, err := planConfig(&planOptions{intakeOptions: sane, brief: long}); err == nil {
		t.Error("planConfig accepted a -brief long enough to be a document")
	}
}

// The milestone title: -milestone verbatim, "" for "off", else derived from the
// document name or the brief's opening words.
func TestPlanMilestoneTitle(t *testing.T) {
	t.Parallel()
	cases := []struct {
		opt  planOptions
		want string
	}{
		{planOptions{milestone: "Batch 3"}, "Batch 3"},
		{planOptions{milestone: "off", design: "docs/VISION.md"}, ""},
		{planOptions{design: "docs/roadmap-2026.md"}, "roadmap-2026"},
		// Over the cap: cut at the last whole word inside it, then the
		// trailing "and" — a dangling connective — is trimmed off too.
		{planOptions{brief: "a dating app for horses, with barn matching and hay reviews"}, "a dating app for horses, with barn matching"},
		// Under the cap: passes through unchanged, no trim applied.
		{planOptions{brief: "a dating app for horses"}, "a dating app for horses"},
		// The cut lands right after a comma: the stray punctuation is
		// trimmed off the end along with the word boundary.
		{planOptions{brief: "a dating app for horses and donkeys and mules, with barn matching"}, "a dating app for horses and donkeys and mules"},
		// No space near the cap: the cut must land on a rune boundary, not a
		// byte one, or the title comes out as invalid UTF-8.
		{planOptions{brief: strings.Repeat("x", 48) + "日本語テスト"}, strings.Repeat("x", 48) + "日本"},
		// A pasted list item with quotes (issue #584): the marker and every
		// `"` go, so the cut can't leave one unpaired to break the review link.
		{planOptions{brief: `- Ambiguous logs "18:56:19 reclaimed 1 finished issue: issue-563" - what does reclaimed mean?`},
			"Ambiguous logs 18:56:19 reclaimed 1 finished"},
		{planOptions{brief: `* a "short" one`}, "a short one"},
		// An explicit -milestone is the operator's own spelling: kept as is.
		{planOptions{milestone: `Batch "3"`}, `Batch "3"`},
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
	t.Parallel()
	newGh := func(st *ghState) (config, *planOptions, string) {
		cfg, _, checkout := planTestConfig(t, st)
		writeDesignFile(t, checkout, "VISION.md")
		return cfg, &planOptions{
			intakeOptions: intakeOptions{maxIssues: 10, dryRun: true},
			design:        "VISION.md", milestone: "off",
		}, checkout
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
	t.Parallel()
	cfg, _, _ := planTestConfig(t, &ghState{})
	_, _, err := planPreflight(context.Background(), &cfg,
		&planOptions{
			intakeOptions: intakeOptions{maxIssues: 10, dryRun: true},
			design:        "docs/not-here.md", milestone: "off",
		})
	if err == nil {
		t.Fatal("planPreflight accepted a -design path with no file behind it")
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
	t.Parallel()
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
	t.Parallel()
	cfg, statePath, checkout := planTestConfig(t, &ghState{})
	writeDesignFile(t, checkout, "VISION.md")

	if _, _, err := planPreflight(context.Background(), &cfg,
		&planOptions{intakeOptions: intakeOptions{maxIssues: 10}, design: "VISION.md"}); err != nil {
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
	writeDesignFile(t, offCheckout, "VISION.md")
	if _, _, err := planPreflight(context.Background(), &offCfg,
		&planOptions{
			intakeOptions: intakeOptions{maxIssues: 10},
			design:        "VISION.md", milestone: "off",
		}); err != nil {
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
	t.Parallel()
	cfg, statePath, checkout := planTestConfig(t, &ghState{})
	writeDesignFile(t, checkout, "docs/VISION.md")
	before, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}

	// A recorder and a notify command are in the config, so this also pins
	// that a dry run reaches neither: it prints the invocation and stops.
	records := t.TempDir()
	cfg.rec = newRecorder(records)
	cfg.shiftID = "planshift"
	told := notifyLog(t, &cfg)

	opt := planOptions{
		intakeOptions: intakeOptions{focus: "the observability section", maxIssues: 7, dryRun: true},
		design:        "docs/VISION.md",
	}
	buf := captureLog(t)
	milestone, hierarchical, err := planPreflight(context.Background(), &cfg, &opt)
	if err != nil {
		t.Fatalf("planPreflight: %v", err)
	}

	var out strings.Builder
	if err := planDryRun(cfg, opt, milestone, hierarchical, &out); err != nil {
		t.Fatalf("planDryRun: %v", err)
	}

	if entries, _ := os.ReadDir(records); len(entries) != 0 {
		t.Errorf("a dry run wrote run data: %v", entries)
	}
	if got := told(); got != nil {
		t.Errorf("a dry run fired a notification: %v", got)
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
		`'/polako:plan-backlog docs/VISION.md "the observability section" 7'`,
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
	setFakeEnv(&cfg, fakeClaudeEnv, claudeMode)
	writeDesignFile(t, checkout, "VISION.md")
	return cfg, statePath
}

// The label pass forces every issue the run created to carry *exactly*
// proposedLabel — a missing one added, any other stripped — attaches the batch
// milestone to the ones without it, and leaves another account's issue alone.
func TestPlanNormaliseForcesExactlyProposed(t *testing.T) {
	t.Parallel()
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

	out := normaliseProposals(context.Background(), cfg, map[int]bool{1: true}, "Batch 1", "plan")
	if out.err() != nil {
		t.Fatalf("normaliseProposals reported failures on a healthy pass: %v", out.err())
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

// An old issue this account filed that fell off the end of the `before` page —
// the listing is capped at 1000 — is not mistaken for the run's own output and
// stripped: the `> maxBefore` guard holds because GitHub issue numbers only
// ever climb.
func TestPlanNormaliseLeavesPreRunIssuesBelowTheHighWaterMark(t *testing.T) {
	t.Parallel()
	cfg, statePath, _ := planTestConfig(t, &ghState{
		Labels: []string{proposedLabel, "enhancement"},
		Issues: map[string]*fakeIssue{
			"5":   {Open: true, Mine: true, Labels: []string{"enhancement"}}, // old, absent from a truncated `before`
			"900": {Open: true, Mine: true},                                  // newest before the run — the high-water mark
			"901": {Open: true, Mine: true},                                  // the run's own
		},
	})
	cfg.repo, cfg.ghRepo = "example/repo", "example/repo"

	// `before` is missing #5, as a 1000-row cap would drop it.
	out := normaliseProposals(context.Background(), cfg, map[int]bool{900: true}, "", "plan")
	if out.created != 1 || len(out.labelled) != 1 {
		t.Errorf("outcome = %+v, want only #901 treated as created", out)
	}
	st, err := readGhState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if is := st.Issues["5"]; !slices.Equal(is.Labels, []string{"enhancement"}) {
		t.Errorf("#5 (pre-run, below the high-water mark) was normalised: %v", is.Labels)
	}
	if is := st.Issues["901"]; !slices.Contains(is.Labels, proposedLabel) {
		t.Errorf("#901 (the run's own) was not labelled: %v", is.Labels)
	}
}

// A label edit that does not take is collected, surfaced, and turned into a
// nonzero exit — never swallowed, because an unlabelled proposal a drain would
// pick up is the worst thing a plan run can leave behind.
func TestPlanNormaliseReportsLabelFailuresLoudly(t *testing.T) {
	t.Parallel()
	// The repository never declared proposedLabel, so `gh issue edit --add-label`
	// fails exactly as GitHub's would.
	cfg, _, _ := planTestConfig(t, &ghState{
		Issues: map[string]*fakeIssue{"10": {Open: true, Mine: true}},
	})
	cfg.repo, cfg.ghRepo = "example/repo", "example/repo"

	out := normaliseProposals(context.Background(), cfg, map[int]bool{}, "", "plan")
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

// The record needs to know how far the run fell short of the curation gate
// and how many of its issues are epics. normaliseProposals counts both: label
// edits (adds plus strips), and created issues that turned out to be
// containers.
func TestPlanNormaliseCountsTheEnforcementAndTheEpics(t *testing.T) {
	t.Parallel()
	cfg, _, _ := planTestConfig(t, &ghState{
		Labels:     []string{proposedLabel, "enhancement"},
		Milestones: []string{"Batch 1"},
		Issues: map[string]*fakeIssue{
			"1":  {Open: true},                                                             // there before the run
			"10": {Open: true, Mine: true, SubIssues: 3},                                   // an epic, the label missing
			"11": {Open: true, Mine: true, Labels: []string{proposedLabel}},                // a child, already right
			"12": {Open: true, Mine: true, Labels: []string{proposedLabel, "enhancement"}}, // a child, a stray label
		},
	})
	cfg.repo, cfg.ghRepo = "example/repo", "example/repo"

	out := normaliseProposals(context.Background(), cfg, map[int]bool{1: true}, "Batch 1", "plan")
	if out.err() != nil {
		t.Fatalf("healthy pass reported failures: %v", out.err())
	}
	if out.created != 3 || out.epics != 1 {
		t.Errorf("created %d / epics %d, want 3 / 1", out.created, out.epics)
	}
	if out.added != 1 || out.stripped != 1 || out.labelsEnforced() != 2 {
		t.Errorf("added %d / stripped %d / enforced %d, want 1 / 1 / 2",
			out.added, out.stripped, out.labelsEnforced())
	}
}

// A gh too old for subIssuesSummary rejects the whole listing rather than the
// one field. The pass retries without it and still normalises — epics_created
// then reads 0, the same degradation the drain's container skip takes.
func TestPlanNormaliseFallsBackForAnOldGh(t *testing.T) {
	t.Parallel()
	cfg, _, _ := planTestConfig(t, &ghState{
		OldGh:  true,
		Labels: []string{proposedLabel},
		Issues: map[string]*fakeIssue{
			"1":  {Open: true},
			"10": {Open: true, Mine: true, SubIssues: 3},
		},
	})
	cfg.repo, cfg.ghRepo = "example/repo", "example/repo"

	out := normaliseProposals(context.Background(), cfg, map[int]bool{1: true}, "", "plan")
	if out.listErr != nil {
		t.Fatalf("the old-gh listing was not retried without the field: %v", out.listErr)
	}
	if out.created != 1 || out.epics != 0 || out.added != 1 {
		t.Errorf("outcome = %+v, want created 1 / epics 0 (unknowable) / added 1", out)
	}
}

// End to end: a real run spawns the skill through execClaude and the pass
// normalises what it filed. The fake skill creates three proposals, only one
// of them labelled.
func TestPlanRunSpawnsTheSkillAndNormalisesWhatItCreated(t *testing.T) {
	t.Parallel()
	var term, buf bytes.Buffer
	captureUI(t, &ui{terminal: &term, file: &buf})
	cfg, statePath := planRunConfig(t, &ghState{Labels: []string{proposedLabel}}, "plan")

	opt := planOptions{intakeOptions: intakeOptions{maxIssues: 10}, design: "VISION.md"}
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
	for _, want := range []string{"filed 3 issues — #", "all labelled " + proposedLabel, "review them "} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("the run's report is missing %q:\n%s", want, buf.String())
		}
	}
	// The milestone is attached per issue but said once: seven identical lines
	// above the summary was the noise this run's report used to open with.
	if strings.Contains(term.String(), "attached the") {
		t.Errorf("per-issue milestone lines belong in the shift log, not the terminal:\n%s", term.String())
	}
	if !strings.Contains(buf.String(), `attached the "VISION" milestone to #`) {
		t.Errorf("the shift log lost the per-issue milestone lines:\n%s", buf.String())
	}
	if !strings.Contains(term.String(), `milestone "VISION"`) {
		t.Errorf("the summary does not name the batch milestone:\n%s", buf.String())
	}
}

// When it ends, a plan run leaves the two traces every run leaves: one
// kind:"plan" record whatever its status, and — because it proposed
// something — one `proposed` notification naming what awaits curation.
func TestPlanRunRecordsAndNotifies(t *testing.T) {
	t.Parallel()
	captureLog(t)
	cfg, _ := planRunConfig(t, &ghState{Labels: []string{proposedLabel}}, "plan")
	records := t.TempDir()
	cfg.rec = newRecorder(records)
	cfg.shiftID = "planshift"
	cfg.tag = "terse"
	told := notifyLog(t, &cfg)

	opt := planOptions{intakeOptions: intakeOptions{maxIssues: 10}, design: "VISION.md"}
	cfg.maxIssues = opt.maxIssues
	if err := planRun(context.Background(), cfg, opt, "VISION", io.Discard); err != nil {
		t.Fatalf("planRun: %v", err)
	}

	lines := readRecords(t, records, cfg.repo)
	if len(lines) != 1 {
		t.Fatalf("wrote %d records, want exactly one plan record", len(lines))
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatalf("record is not JSON: %v", err)
	}
	for key, want := range map[string]any{
		"kind": "plan", "shift": "planshift", "repo": "example/repo",
		"status": "ok", "tag": "terse", "design": "VISION.md", "milestone": "VISION",
		"issues_created": float64(3), "epics_created": float64(0),
		"cap": float64(10), "labels_enforced": float64(2),
	} {
		if rec[key] != want {
			t.Errorf("record[%q] = %v, want %v", key, rec[key], want)
		}
	}

	got := told()
	if len(got) != 1 {
		t.Fatalf("notifications = %v, want one for the proposals awaiting curation", got)
	}
	for _, want := range []string{
		notifyPrefix + "EVENT=proposed",
		notifyPrefix + "ISSUE= ", // the whole batch, not one issue
		"3 proposals await curation",
		"remove the " + proposedLabel + " label",
	} {
		if !strings.Contains(got[0], want) {
			t.Errorf("the proposed notification is missing %q\ngot: %s", want, got[0])
		}
	}
}

// A plan run that proposed nothing fires no notification — nobody is waiting
// on a backlog that does not exist — but still writes its record.
func TestPlanRunWithNoProposalsRecordsButDoesNotNotify(t *testing.T) {
	t.Parallel()
	captureLog(t)
	cfg, _ := planRunConfig(t, &ghState{Labels: []string{proposedLabel}}, "planempty")
	records := t.TempDir()
	cfg.rec = newRecorder(records)
	cfg.shiftID = "planshift"
	told := notifyLog(t, &cfg)

	opt := planOptions{intakeOptions: intakeOptions{maxIssues: 10}, design: "VISION.md"}
	cfg.maxIssues = opt.maxIssues
	if err := planRun(context.Background(), cfg, opt, "VISION", io.Discard); err != nil {
		t.Fatalf("planRun: %v", err)
	}

	if got := told(); got != nil {
		t.Errorf("a run that proposed nothing still notified: %v", got)
	}
	lines := readRecords(t, records, cfg.repo)
	if len(lines) != 1 {
		t.Fatalf("wrote %d records, want the one plan record regardless", len(lines))
	}
	if !strings.Contains(lines[0], `"issues_created":0`) {
		t.Errorf("record does not show zero issues created:\n%s", lines[0])
	}
}

// The cap: dispatchClaude kills the run at -max-issues, planRun reports it
// rather than raising it, and the label pass still normalises everything that
// was filed. Nothing is closed.
func TestPlanRunCapsIssueCreationAndStillNormalises(t *testing.T) {
	t.Parallel()
	buf := captureLog(t)
	cfg, statePath := planRunConfig(t, &ghState{Labels: []string{proposedLabel}}, "plancap")

	opt := planOptions{intakeOptions: intakeOptions{maxIssues: 3}, design: "VISION.md"}
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
	if created != opt.maxIssues {
		t.Errorf("the fake skill filed %d issues, want exactly the cap of %d", created, opt.maxIssues)
	}
	if !strings.Contains(buf.String(), "-max-issues") {
		t.Errorf("the log does not mention the cap:\n%s", buf.String())
	}
}

// A shutdown signal mid-run surfaces as context.Canceled, not the run's raw
// "did not finish cleanly" error — the CLI's process is killed through the
// context and Wait then returns a bare "signal: killed", so planRun has to read
// the interrupt from the context itself. The label pass still runs, on its own
// detached deadline.
func TestPlanRunInterruptReportsAsCancelled(t *testing.T) {
	t.Parallel()
	captureLog(t)
	// "plancap" pauses between its create iterations, so the context kill —
	// not the process's own exit — is what ends the run.
	cfg, statePath := planRunConfig(t, &ghState{Labels: []string{proposedLabel}}, "plancap")

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()

	opt := planOptions{intakeOptions: intakeOptions{maxIssues: 10}, design: "VISION.md"}
	cfg.maxIssues = opt.maxIssues
	err := planRun(ctx, cfg, opt, "VISION", io.Discard)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("an interrupted plan run returned %v, want context.Canceled", err)
	}

	// The pass ran anyway: whatever the fake skill filed is labelled, not stranded.
	st, err := readGhState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	for n, is := range st.Issues {
		if is.Mine && !slices.Contains(is.Labels, proposedLabel) {
			t.Errorf("#%s was created but left unlabelled by an interrupted run: %v", n, is.Labels)
		}
	}
}

// isIssueCreate is what the -max-issues counter keys on: a Bash `gh issue
// create`, in whatever shell dressing, but never the `--help` capability probe
// and never a lookalike subcommand.
func TestIsIssueCreate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, cmd string
		want      bool
	}{
		{"plain", "gh issue create --title T --label proposed", true},
		{"body-file and parent", "gh issue create --title T --body-file B.md --parent 4", true},
		{"extra spaces", "gh  issue   create --title T", true},
		{"after a cd", "cd /repo && gh issue create --title T", true},
		{"help probe", "gh issue create --help", false},
		{"help probe piped", "gh issue create --help | cat", false},
		{"a list, not a create", "gh issue list --state open", false},
		{"lookalike subcommand", "gh issue create-template --title T", false},
		{"a path ending in gh", "/opt/bin/megh issue create", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := []byte(fmt.Sprintf(`{"command":%q}`, tc.cmd))
			if got := isIssueCreate("Bash", in); got != tc.want {
				t.Errorf("isIssueCreate(%q) = %v, want %v", tc.cmd, got, tc.want)
			}
		})
	}
	if isIssueCreate("Write", []byte(`{"command":"gh issue create"}`)) {
		t.Error("isIssueCreate matched a non-Bash tool")
	}
}

// The -skill default resolves to the skill this repo actually ships.
func TestPlanSkillDefaultMatchesTheShippedSkill(t *testing.T) {
	t.Parallel()
	if defaultPlanSkill != "polako:"+planSkillDir {
		t.Fatalf("defaultPlanSkill = %q, want polako:%s", defaultPlanSkill, planSkillDir)
	}
	if _, err := os.Stat(filepath.Join(repoRoot(), "skills", planSkillDir, "SKILL.md")); err != nil {
		t.Errorf("-skill defaults to %q but skills/%s/SKILL.md is not there: %v", defaultPlanSkill, planSkillDir, err)
	}
}

// The pricing-line fixture is deliberately mixed, the way stats's own is: two
// merged issues that priced, a parked one and an in-flight one that must not
// count, an issue for another repo the -repo filter drops, a torn tail line
// and a record kind this reader has never seen. The medians are $3.00 (of
// $2.00 and $4.00) and 40m (of 30m and 50m).
const pricingFixture = `
{"v":1,"kind":"run","ts":"2026-08-20T09:00:00Z","ended":"2026-08-20T09:30:00Z","repo":"scharissis/polako","issue":20,"reason":"implement","status":"ok","subtype":"success","outcome":"opened_pr","cost_usd":2.00,"usage_source":"result","wall_ms":1800000,"tokens":{"in":1,"out":1}}
{"v":1,"kind":"issue","ts":"2026-08-20T10:00:00Z","repo":"scharissis/polako","issue":20,"pr":50,"outcome":"merged"}
{"v":1,"kind":"run","ts":"2026-08-21T09:00:00Z","ended":"2026-08-21T09:50:00Z","repo":"scharissis/polako","issue":21,"reason":"implement","status":"ok","subtype":"success","outcome":"opened_pr","cost_usd":4.00,"usage_source":"result","wall_ms":3000000,"tokens":{"in":1,"out":1}}
{"v":1,"kind":"issue","ts":"2026-08-21T11:00:00Z","repo":"scharissis/polako","issue":21,"pr":51,"outcome":"merged"}
{"v":1,"kind":"run","ts":"2026-08-22T09:00:00Z","ended":"2026-08-22T09:40:00Z","repo":"scharissis/polako","issue":22,"reason":"implement","status":"ok","subtype":"success","outcome":"posted_questions","cost_usd":9.00,"usage_source":"result","wall_ms":9000000,"tokens":{"in":1,"out":1}}
{"v":1,"kind":"issue","ts":"2026-08-22T10:00:00Z","repo":"scharissis/polako","issue":22,"pr":0,"outcome":"needs_human","park_reason":"produced_nothing"}
{"v":1,"kind":"run","ts":"2026-08-23T09:00:00Z","ended":"2026-08-23T09:20:00Z","repo":"scharissis/polako","issue":23,"reason":"implement","status":"ok","subtype":"success","outcome":"opened_pr","cost_usd":7.00,"usage_source":"result","wall_ms":1200000,"tokens":{"in":1,"out":1}}
{"v":1,"kind":"digest","ts":"2026-08-23T09:30:00Z","repo":"scharissis/polako","note":"a record kind a newer writer added"}
{"v":1,"kind":"run","ts":"2026-08-24T09:00:00Z","ended":"2026-08-2`

// pricingOtherRepo is a merged issue in a different repository: the -repo
// filter has to leave it out, or the batch is priced against the wrong history.
const pricingOtherRepo = `
{"v":1,"kind":"run","ts":"2026-08-20T09:00:00Z","ended":"2026-08-20T10:00:00Z","repo":"scharissis/other","issue":9,"reason":"implement","status":"ok","subtype":"success","outcome":"opened_pr","cost_usd":99.00,"usage_source":"result","wall_ms":6000000,"tokens":{"in":1,"out":1}}
{"v":1,"kind":"issue","ts":"2026-08-20T11:00:00Z","repo":"scharissis/other","issue":9,"pr":1,"outcome":"merged"}
`

func writePricingFixture(t *testing.T, bodies map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range bodies {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(strings.TrimPrefix(body, "\n")), 0o600); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}
	return dir
}

func TestPlanPricingLineFromHistory(t *testing.T) {
	t.Parallel()
	dir := writePricingFixture(t, map[string]string{
		"scharissis--polako.jsonl": pricingFixture,
		"scharissis--other.jsonl":  pricingOtherRepo,
	})
	got := proposalPricingLine(dir, "scharissis/polako", 5, 0, fixtureNow)
	want := "working all 5 would cost about $15 and 3½h — a merged issue here runs $3.00 and 40m (median of your last 2)"
	if got != want {
		t.Errorf("proposalPricingLine:\n got %q\nwant %q", got, want)
	}
}

func TestPlanPricingLineWithNoHistory(t *testing.T) {
	t.Parallel()
	if got := proposalPricingLine(t.TempDir(), "scharissis/polako", 5, 0, fixtureNow); got != noPricingHistory {
		t.Errorf("empty directory: got %q, want the no-history line", got)
	}
}

func TestPlanPricingLineWithMetricsOff(t *testing.T) {
	t.Parallel()
	// -metrics off resolves to an empty dir string: no file is opened to find
	// out there is nothing to read.
	if got := proposalPricingLine("", "scharissis/polako", 5, 0, fixtureNow); got != noPricingHistory {
		t.Errorf("-metrics off: got %q, want the no-history line", got)
	}
}

func TestPlanPricingLineTreatsUnpricedCrashesAsNoHistory(t *testing.T) {
	t.Parallel()
	// The only merged issue's runs all died before reporting a cost: a real
	// record, a useless estimate. Priced at nothing ⇒ no history to price
	// against, said as such rather than "≈ $0".
	crashOnly := `
{"v":1,"kind":"run","ts":"2026-08-20T09:00:00Z","ended":"2026-08-20T09:05:00Z","repo":"scharissis/polako","issue":40,"reason":"implement","status":"crash","exit_code":7,"outcome":"nothing","cost_usd":0,"usage_source":"observed","wall_ms":300000,"tokens":{"in":1,"out":1}}
{"v":1,"kind":"issue","ts":"2026-08-20T10:00:00Z","repo":"scharissis/polako","issue":40,"pr":0,"outcome":"merged"}
`
	dir := writePricingFixture(t, map[string]string{"scharissis--polako.jsonl": crashOnly})
	if got := proposalPricingLine(dir, "scharissis/polako", 5, 0, fixtureNow); got != noPricingHistory {
		t.Errorf("crash-only history: got %q, want the no-history line", got)
	}
}

func TestPlanPricingLineSkipsUnpricedIssuesInAMixedHistory(t *testing.T) {
	t.Parallel()
	// One real merged issue ($6.00, 60m) and one merged issue whose only run
	// crashed at $0 after 5m. The $0 issue must not be averaged in — the
	// estimate is $6.00/60m, not the $3.00/32m a per-issue skip would avoid but
	// an aggregate-only guard would not.
	mixed := `
{"v":1,"kind":"run","ts":"2026-08-20T09:00:00Z","ended":"2026-08-20T10:00:00Z","repo":"scharissis/polako","issue":60,"reason":"implement","status":"ok","subtype":"success","outcome":"opened_pr","cost_usd":6.00,"usage_source":"result","wall_ms":3600000,"tokens":{"in":1,"out":1}}
{"v":1,"kind":"issue","ts":"2026-08-20T11:00:00Z","repo":"scharissis/polako","issue":60,"pr":70,"outcome":"merged"}
{"v":1,"kind":"run","ts":"2026-08-21T09:00:00Z","ended":"2026-08-21T09:05:00Z","repo":"scharissis/polako","issue":61,"reason":"implement","status":"crash","exit_code":7,"outcome":"nothing","cost_usd":0,"usage_source":"observed","wall_ms":300000,"tokens":{"in":1,"out":1}}
{"v":1,"kind":"issue","ts":"2026-08-21T10:00:00Z","repo":"scharissis/polako","issue":61,"pr":0,"outcome":"merged"}
`
	dir := writePricingFixture(t, map[string]string{"scharissis--polako.jsonl": mixed})
	got := proposalPricingLine(dir, "scharissis/polako", 2, 0, fixtureNow)
	want := "working all 2 would cost about $12 and 2h — a merged issue here runs $6.00 and 1h (median of your last 1)"
	if got != want {
		t.Errorf("mixed history:\n got %q\nwant %q", got, want)
	}
}

func TestPlanPricingLineOnlyPrintsForABatch(t *testing.T) {
	t.Parallel()
	// Zero proposals never reaches proposalPricingLine in planRun, but the median
	// half of the sentence should still read sanely if it ever did.
	dir := writePricingFixture(t, map[string]string{"scharissis--polako.jsonl": pricingFixture})
	got := proposalPricingLine(dir, "scharissis/polako", 1, 0, fixtureNow)
	want := "working it would cost about $3.00 and 40m — a merged issue here runs $3.00 and 40m (median of your last 2)"
	if got != want {
		t.Errorf("single proposal:\n got %q\nwant %q", got, want)
	}
}

func TestPlanPricingLineSaysWhyItsCountIsShortOfTheSummary(t *testing.T) {
	t.Parallel()
	// An epic is a container, never worked, so the caller prices created minus
	// epics — and the line names the gap, or "filed 7" above "all 6" reads as
	// a miscount.
	dir := writePricingFixture(t, map[string]string{"scharissis--polako.jsonl": pricingFixture})
	for _, c := range []struct {
		workable, epics int
		want            string
	}{
		{6, 1, "working the 6 that aren't epics would cost about $18 and 4h — a merged issue here runs $3.00 and 40m (median of your last 2)"},
		{1, 1, "working the 1 that isn't an epic would cost about $3.00 and 40m — a merged issue here runs $3.00 and 40m (median of your last 2)"},
	} {
		if got := proposalPricingLine(dir, "scharissis/polako", c.workable, c.epics, fixtureNow); got != c.want {
			t.Errorf("workable %d, epics %d:\n got %q\nwant %q", c.workable, c.epics, got, c.want)
		}
	}
}

func TestMedianDurRoundsToTheMinuteOnceItIsWorthOne(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		in   time.Duration
		want string
	}{
		{14*time.Minute + 46*time.Second, "15m"},
		{40 * time.Minute, "40m"},
		{time.Hour + 10*time.Minute + 20*time.Second, "1h10m"},
		{42 * time.Second, "42s"},
	} {
		if got := medianDur(c.in); got != c.want {
			t.Errorf("medianDur(%s) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestLabelPassSummaryVariants(t *testing.T) {
	t.Parallel()
	seven := []int{405, 404, 403, 402, 401, 400, 399}
	for _, c := range []struct {
		name string
		o    labelPassOutcome
		rep  runReport
		want string
	}{
		{"a clean plan batch",
			labelPassOutcome{created: 7, numbers: seven, labelled: seven, epics: 1, milestone: seven, title: "visual-evidence"},
			runReport{},
			`filed 7 issues — #399–#405, all labelled proposed, 1 epic, milestone "visual-evidence"`},
		{"health has no milestone",
			labelPassOutcome{created: 2, numbers: []int{12, 11}, labelled: []int{12, 11}},
			runReport{},
			"filed 2 issues — #11–#12, all labelled proposed"},
		{"a milestone only some needed",
			labelPassOutcome{created: 3, numbers: []int{9, 8, 7}, labelled: []int{9, 8, 7}, milestone: []int{9}, title: "m"},
			runReport{},
			`filed 3 issues — #7–#9, all labelled proposed, milestone "m" attached to 1`},
		{"a quote in the title prints plain, not Go-escaped",
			labelPassOutcome{created: 1, numbers: []int{5}, labelled: []int{5}, milestone: []int{5}, title: `Batch "3"`},
			runReport{},
			`filed 1 issue — #5, all labelled proposed, milestone "Batch "3""`},
		{"a label that did not take, strays stripped, capped",
			labelPassOutcome{created: 3, numbers: []int{9, 8, 7}, labelled: []int{9, 8}, stripped: 2,
				failures: []string{"could not add proposed to #7: boom"}},
			runReport{capped: true},
			"filed 3 issues — #7–#9, 2 of 3 labelled proposed (2 stray labels stripped) — stopped at the -max-issues cap — 1 action FAILED, see below"},
		{"one issue", labelPassOutcome{created: 1, numbers: []int{5}, labelled: []int{5}}, runReport{},
			"filed 1 issue — #5, all labelled proposed"},
		{"nothing filed", labelPassOutcome{}, runReport{}, "the run created no issues"},
		{"capped before filing", labelPassOutcome{}, runReport{capped: true}, "the run was capped before it created anything"},
	} {
		if got := c.o.summary(c.rep); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}

func TestIssueRanges(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		in   []int
		want string
	}{
		{[]int{405, 399, 400, 401, 402, 403, 404}, "#399–#405"},
		{[]int{403, 399, 401, 402}, "#399, #401–#403"},
		{[]int{7, 9}, "#7, #9"},
		{[]int{7, 8}, "#7–#8"},
		{[]int{7}, "#7"},
		{nil, ""},
	} {
		if got := issueRanges(c.in); got != c.want {
			t.Errorf("issueRanges(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCurationLine(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, repo, milestone, want string
	}{
		{"plan, with its batch milestone", "scharissis/polako", "visual-evidence",
			"review them at https://github.com/scharissis/polako/issues?q=is%3Aopen+label%3Aproposed+milestone%3A%22visual-evidence%22 — remove the proposed label to queue them"},
		{"a title with a space survives the query", "o/r", "a dating app",
			"review them at https://github.com/o/r/issues?q=is%3Aopen+label%3Aproposed+milestone%3A%22a+dating+app%22 — remove the proposed label to queue them"},
		{"a title with a quote links the unnarrowed search", "o/r", `Batch "3"`,
			"review them at https://github.com/o/r/issues?q=is%3Aopen+label%3Aproposed — remove the proposed label to queue them"},
		{"health, no milestone", "o/r", "",
			"review them at https://github.com/o/r/issues?q=is%3Aopen+label%3Aproposed — remove the proposed label to queue them"},
		{"a slug that is not owner/name gets no invented link", "ghe.example.com/o/r", "m",
			"review them with `gh issue list --label proposed` — remove the proposed label to queue them"},
	} {
		if got := curationLine(c.repo, c.milestone); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}

func TestApproxUSDAndDur(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		f    float64
		want string
	}{{3, "$3.00"}, {9.99, "$9.99"}, {15, "$15"}, {18.9, "$19"}, {250, "$250"}} {
		if got := approxUSD(c.f); got != c.want {
			t.Errorf("approxUSD(%v) = %q, want %q", c.f, got, c.want)
		}
	}
	for _, c := range []struct {
		d    time.Duration
		want string
	}{
		{20 * time.Minute, "20m"},
		{44 * time.Minute, "44m"},
		{75 * time.Minute, "1½h"},
		{200 * time.Minute, "3½h"},
		{4 * time.Hour, "4h"},
		{10 * time.Minute, "10m"},
	} {
		if got := approxDur(c.d); got != c.want {
			t.Errorf("approxDur(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

// A brief run's milestone title comes from the run's own `Milestone:` line,
// cleaned and capped the way a brief is; no usable line falls back.
func TestPlanRunMilestone(t *testing.T) {
	t.Parallel()
	const fallback = "i dont like the location of the PR provider/model"
	for _, c := range []struct {
		name, text, want string
	}{
		{"the run's line", "Filed 2.\n\nMilestone: Ran-On Lines Section", "Ran-On Lines Section"},
		{"quotes stripped", `Milestone: The "Ran-On" Block`, "The Ran-On Block"},
		{"capped at a word boundary", "Milestone: " + strings.Repeat("word ", 20), strings.TrimSpace(strings.Repeat("word ", 10))},
		{"no line", "Filed 2.", fallback},
		{"blank line", "Milestone: \n", fallback},
		{"only quotes", `Milestone: ""`, fallback},
		{"no result at all", "", fallback},
	} {
		if got := planRunMilestone(c.text, fallback); got != c.want {
			t.Errorf("%s: planRunMilestone = %q, want %q", c.name, got, c.want)
		}
	}
}

// A brief with no -milestone leaves the title to the run, so preflight creates
// nothing — it only hands back the fallback. An explicit -milestone on a brief
// is still ensured at preflight.
func TestPlanPreflightLeavesABriefMilestoneToTheRun(t *testing.T) {
	t.Parallel()
	cfg, statePath, _ := planTestConfig(t, &ghState{})
	opt := &planOptions{intakeOptions: intakeOptions{maxIssues: 10}, brief: "a dating app for horses"}
	milestone, _, err := planPreflight(context.Background(), &cfg, opt)
	if err != nil {
		t.Fatalf("planPreflight: %v", err)
	}
	if milestone != "a dating app for horses" {
		t.Errorf("fallback milestone = %q, want the brief's words", milestone)
	}
	if st, _ := readGhState(statePath); len(st.Milestones) != 0 {
		t.Errorf("preflight created a milestone the run was meant to name: %v", st.Milestones)
	}

	setCfg, setState, _ := planTestConfig(t, &ghState{})
	if _, _, err := planPreflight(context.Background(), &setCfg,
		&planOptions{intakeOptions: intakeOptions{maxIssues: 10}, brief: "a dating app for horses", milestone: "Horses"}); err != nil {
		t.Fatalf("planPreflight -milestone: %v", err)
	}
	if st, _ := readGhState(setState); !slices.Equal(st.Milestones, []string{"Horses"}) {
		t.Errorf("an explicit -milestone on a brief was not ensured at preflight: %v", st.Milestones)
	}
}

// End to end for a brief: the fake skill's report ends `Milestone: Horse
// "Barn" Matching`, so that — quote-stripped — is the milestone created,
// attached, linked and recorded, not the brief's first words.
func TestPlanRunBriefUsesTheRunsMilestone(t *testing.T) {
	t.Parallel()
	var term, buf bytes.Buffer
	captureUI(t, &ui{terminal: &term, file: &buf})
	cfg, statePath := planRunConfig(t, &ghState{Labels: []string{proposedLabel}}, "plan")
	records := t.TempDir()
	cfg.rec = newRecorder(records)

	opt := planOptions{intakeOptions: intakeOptions{maxIssues: 10}, brief: "a dating app for horses"}
	cfg.maxIssues = opt.maxIssues
	if err := planRun(context.Background(), cfg, opt, planMilestoneTitle(&opt), io.Discard); err != nil {
		t.Fatalf("planRun: %v", err)
	}

	const want = "Horse Barn Matching"
	st, err := readGhState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(st.Milestones, []string{want}) {
		t.Errorf("milestones = %v, want only %q", st.Milestones, want)
	}
	for n, is := range st.Issues {
		if is.Mine && is.Milestone != want {
			t.Errorf("#%s milestone = %q, want %q", n, is.Milestone, want)
		}
	}
	if !strings.Contains(term.String(), "milestone%3A%22Horse+Barn+Matching%22") {
		t.Errorf("the curation link is not narrowed to the run's milestone:\n%s", term.String())
	}
	lines := readRecords(t, records, cfg.repo)
	if len(lines) != 1 || !strings.Contains(lines[0], `"milestone":"`+want+`"`) {
		t.Errorf("the record does not carry the milestone used:\n%v", lines)
	}
}

// A brief run whose report names no milestone falls back to the brief's
// first words, and the record says so.
func TestPlanRunBriefFallsBackWithNoMilestoneLine(t *testing.T) {
	t.Parallel()
	captureLog(t)
	cfg, statePath := planRunConfig(t, &ghState{Labels: []string{proposedLabel}}, "planempty")
	records := t.TempDir()
	cfg.rec = newRecorder(records)

	opt := planOptions{intakeOptions: intakeOptions{maxIssues: 10}, brief: "a dating app for horses"}
	cfg.maxIssues = opt.maxIssues
	if err := planRun(context.Background(), cfg, opt, planMilestoneTitle(&opt), io.Discard); err != nil {
		t.Fatalf("planRun: %v", err)
	}
	if st, _ := readGhState(statePath); !slices.Equal(st.Milestones, []string{"a dating app for horses"}) {
		t.Errorf("milestones = %v, want the brief fallback", st.Milestones)
	}
	lines := readRecords(t, records, cfg.repo)
	if len(lines) != 1 || !strings.Contains(lines[0], `"milestone":"a dating app for horses"`) {
		t.Errorf("the record does not carry the fallback milestone:\n%v", lines)
	}
}

// A dry run of a brief says the run names the milestone, and what it falls
// back to.
func TestPlanDryRunSaysABriefMilestoneIsNamedByTheRun(t *testing.T) {
	t.Parallel()
	cfg, _, _ := planTestConfig(t, &ghState{})
	opt := planOptions{intakeOptions: intakeOptions{maxIssues: 10, dryRun: true}, brief: "a dating app for horses"}
	buf := captureLog(t)
	milestone, hierarchical, err := planPreflight(context.Background(), &cfg, &opt)
	if err != nil {
		t.Fatalf("planPreflight: %v", err)
	}
	if err := planDryRun(cfg, opt, milestone, hierarchical, io.Discard); err != nil {
		t.Fatalf("planDryRun: %v", err)
	}
	want := `milestone: named by the run once it has shaped the batch — "a dating app for horses" if it names none`
	if !strings.Contains(buf.String(), want) {
		t.Errorf("narration is missing %q\ngot:\n%s", want, buf.String())
	}
}
