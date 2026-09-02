package main

// `polako plan` resolves a vision document, runs its preflight probes, then
// either prints the invocation a run would make (-dry-run) or makes it: spawn
// the skill, cap it at -max-issues, and normalise every issue it created to
// carry exactly the `proposed` label. Every case here runs on the same fake
// `gh` and fake `claude` the drain loop does — no network, no real gh, no claude.

import (
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

	// A recorder and a notify command are in the config, so this also pins
	// that a dry run reaches neither: it prints the invocation and stops.
	records := t.TempDir()
	cfg.rec = newRecorder(records)
	cfg.shiftID = "planshift"
	told := notifyLog(t, &cfg)

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

// When it ends, a plan run leaves the two traces every run leaves: one
// kind:"plan" record whatever its status, and — because it proposed
// something — one `proposed` notification naming what awaits curation.
func TestPlanRunRecordsAndNotifies(t *testing.T) {
	captureLog(t)
	cfg, _ := planRunConfig(t, &ghState{Labels: []string{proposedLabel}}, "plan")
	records := t.TempDir()
	cfg.rec = newRecorder(records)
	cfg.shiftID = "planshift"
	cfg.tag = "terse"
	told := notifyLog(t, &cfg)

	opt := planOptions{vision: "VISION.md", maxIssues: 10}
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
		"status": "ok", "tag": "terse", "vision": "VISION.md", "milestone": "VISION",
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
	captureLog(t)
	cfg, _ := planRunConfig(t, &ghState{Labels: []string{proposedLabel}}, "planempty")
	records := t.TempDir()
	cfg.rec = newRecorder(records)
	cfg.shiftID = "planshift"
	told := notifyLog(t, &cfg)

	opt := planOptions{vision: "VISION.md", maxIssues: 10}
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

// A shutdown signal mid-run surfaces as context.Canceled, not the run's raw
// "did not finish cleanly" error — the CLI's process is killed through the
// context and Wait then returns a bare "signal: killed", so planRun has to read
// the interrupt from the context itself. The label pass still runs, on its own
// detached deadline.
func TestPlanRunInterruptReportsAsCancelled(t *testing.T) {
	captureLog(t)
	// "plancap" lingers after it has emitted its events, so the context kill —
	// not the process's own exit — is what ends the run.
	cfg, statePath := planRunConfig(t, &ghState{Labels: []string{proposedLabel}}, "plancap")

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()

	opt := planOptions{vision: "VISION.md", maxIssues: 10}
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
	dir := writePricingFixture(t, map[string]string{
		"scharissis--polako.jsonl": pricingFixture,
		"scharissis--other.jsonl":  pricingOtherRepo,
	})
	got := proposalPricingLine(dir, "scharissis/polako", 5, fixtureNow)
	want := "your last 2 merged issues ran $3.00 and 40m median — 5 proposals ≈ $15 and 3½h of run time, before curation cuts"
	if got != want {
		t.Errorf("proposalPricingLine:\n got %q\nwant %q", got, want)
	}
}

func TestPlanPricingLineWithNoHistory(t *testing.T) {
	if got := proposalPricingLine(t.TempDir(), "scharissis/polako", 5, fixtureNow); got != noPricingHistory {
		t.Errorf("empty directory: got %q, want the no-history line", got)
	}
}

func TestPlanPricingLineWithMetricsOff(t *testing.T) {
	// -metrics off resolves to an empty dir string: no file is opened to find
	// out there is nothing to read.
	if got := proposalPricingLine("", "scharissis/polako", 5, fixtureNow); got != noPricingHistory {
		t.Errorf("-metrics off: got %q, want the no-history line", got)
	}
}

func TestPlanPricingLineTreatsUnpricedCrashesAsNoHistory(t *testing.T) {
	// The only merged issue's runs all died before reporting a cost: a real
	// record, a useless estimate. Priced at nothing ⇒ no history to price
	// against, said as such rather than "≈ $0".
	crashOnly := `
{"v":1,"kind":"run","ts":"2026-08-20T09:00:00Z","ended":"2026-08-20T09:05:00Z","repo":"scharissis/polako","issue":40,"reason":"implement","status":"crash","exit_code":7,"outcome":"nothing","cost_usd":0,"usage_source":"observed","wall_ms":300000,"tokens":{"in":1,"out":1}}
{"v":1,"kind":"issue","ts":"2026-08-20T10:00:00Z","repo":"scharissis/polako","issue":40,"pr":0,"outcome":"merged"}
`
	dir := writePricingFixture(t, map[string]string{"scharissis--polako.jsonl": crashOnly})
	if got := proposalPricingLine(dir, "scharissis/polako", 5, fixtureNow); got != noPricingHistory {
		t.Errorf("crash-only history: got %q, want the no-history line", got)
	}
}

func TestPlanPricingLineSkipsUnpricedIssuesInAMixedHistory(t *testing.T) {
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
	got := proposalPricingLine(dir, "scharissis/polako", 2, fixtureNow)
	want := "your last 1 merged issue ran $6.00 and 1h median — 2 proposals ≈ $12 and 2h of run time, before curation cuts"
	if got != want {
		t.Errorf("mixed history:\n got %q\nwant %q", got, want)
	}
}

func TestPlanPricingLineOnlyPrintsForABatch(t *testing.T) {
	// Zero proposals never reaches proposalPricingLine in planRun, but the median
	// half of the sentence should still read sanely if it ever did.
	dir := writePricingFixture(t, map[string]string{"scharissis--polako.jsonl": pricingFixture})
	got := proposalPricingLine(dir, "scharissis/polako", 1, fixtureNow)
	want := "your last 2 merged issues ran $3.00 and 40m median — 1 proposal ≈ $3.00 and 40m of run time, before curation cuts"
	if got != want {
		t.Errorf("single proposal:\n got %q\nwant %q", got, want)
	}
}

func TestApproxUSDAndDur(t *testing.T) {
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
