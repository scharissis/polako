package main

// `polako health` resolves a repository, runs its preflight probes, then
// either prints the invocation a run would make (-dry-run) or makes it: spawn
// the skill, cap it at -max-issues, and normalise every issue it created to
// carry exactly the `proposed` label. Every case here runs on the same fake
// `gh` and fake `claude` the drain loop and plan_test.go do — no network, no
// real gh, no claude. The fake-claude modes are plan_test.go's own
// ("plan"/"plancap"/"planempty"): they just emit generic `gh issue create`
// tool calls and count against -max-issues, nothing plan-specific, so health
// reuses them rather than adding a parallel set.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// healthTestConfig builds the config healthPreflight and healthDryRun take,
// pointed at a fake gh and a real (temp) checkout, the way planTestConfig does.
func healthTestConfig(t *testing.T, st *ghState) (cfg config, statePath, checkout string) {
	t.Helper()
	drainCfg, statePath := drainConfig(t, "stream", st)
	_, checkout = upstream(t)
	cfg = config{
		dir:            checkout,
		ghBin:          drainCfg.ghBin,
		claudeBin:      drainCfg.claudeBin,
		ghRetryWait:    time.Millisecond,
		skill:          defaultHealthSkill,
		model:          "opus",
		permissionMode: "acceptEdits",
		tools:          healthTools,
		queue:          new(queueMemo),
	}
	return cfg, statePath, checkout
}

// The bare invocation's verb table has to list health now that it exists.
func TestVerbUsageListsHealth(t *testing.T) {
	var b strings.Builder
	verbUsage(&b)
	if !strings.Contains(b.String(), "\n  health ") {
		t.Errorf("verbUsage does not list `health`:\n%s", b.String())
	}
}

// healthConfig needs nothing but a sane -max-issues: unlike plan there is no
// vision/brief pair to require.
func TestHealthConfigRequiresAtLeastOneIssue(t *testing.T) {
	if _, err := healthConfig(&healthOptions{maxIssues: 0}); err == nil {
		t.Error("healthConfig accepted -max-issues 0")
	}
	if _, err := healthConfig(&healthOptions{maxIssues: 10}); err != nil {
		t.Errorf("healthConfig rejected a sane -max-issues: %v", err)
	}
}

// The capability probe reads `gh issue create --help`, exactly like plan's.
func TestHealthPreflightProbesParentSupport(t *testing.T) {
	cfg, _, _ := healthTestConfig(t, &ghState{})
	if hierarchical, err := healthPreflight(context.Background(), &cfg, &healthOptions{maxIssues: 10, dryRun: true}); err != nil {
		t.Fatalf("healthPreflight: %v", err)
	} else if !hierarchical {
		t.Error("probe missed `--parent` in a modern gh's help")
	}

	oldCfg, _, _ := healthTestConfig(t, &ghState{NoSubIssues: true})
	if hierarchical, err := healthPreflight(context.Background(), &oldCfg, &healthOptions{maxIssues: 10, dryRun: true}); err != nil {
		t.Fatalf("healthPreflight (old gh): %v", err)
	} else if hierarchical {
		t.Error("probe reported `--parent` on a gh whose help omits it")
	}
}

// A real run's preflight declares the `proposed` label and nothing else —
// review-health attaches no milestone, unlike plan.
func TestHealthPreflightDeclaresOnlyTheLabel(t *testing.T) {
	cfg, statePath, _ := healthTestConfig(t, &ghState{})

	if _, err := healthPreflight(context.Background(), &cfg, &healthOptions{maxIssues: 10}); err != nil {
		t.Fatalf("healthPreflight: %v", err)
	}
	st, err := readGhState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(st.Labels, proposedLabel) {
		t.Errorf("preflight did not declare the %s label: %v", proposedLabel, st.Labels)
	}
	if len(st.Milestones) != 0 {
		t.Errorf("health preflight created a milestone, which review-health never uses: %v", st.Milestones)
	}
}

// A dry run declares nothing and prints the exact invocation a real run would
// make. With no -focus, the prompt is the bare slash command: -dir already
// positions cwd at the target repo (see healthPrompt), so there is nothing
// else to pass.
func TestHealthDryRunWritesNothingAndPrintsTheInvocation(t *testing.T) {
	cfg, statePath, _ := healthTestConfig(t, &ghState{})
	before, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}

	records := t.TempDir()
	cfg.rec = newRecorder(records)
	cfg.shiftID = "healthshift"
	told := notifyLog(t, &cfg)

	opt := healthOptions{focus: "only cmd/polako", maxIssues: 7, dryRun: true}
	buf := captureLog(t)
	hierarchical, err := healthPreflight(context.Background(), &cfg, &opt)
	if err != nil {
		t.Fatalf("healthPreflight: %v", err)
	}

	var out strings.Builder
	if err := healthDryRun(cfg, opt, hierarchical, &out); err != nil {
		t.Fatalf("healthDryRun: %v", err)
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
	if len(st.Labels) != 0 {
		t.Errorf("a dry run declared something: labels %v", st.Labels)
	}

	printed := strings.TrimSpace(out.String())
	if strings.Contains(printed, "\n") {
		t.Errorf("want one invocation and nothing else on stdout, got:\n%s", printed)
	}
	for _, want := range []string{
		`'/polako:review-health "" "only cmd/polako"'`,
		"--model opus",
		"Bash(gh issue create:*)",
		"--output-format stream-json",
	} {
		if !strings.Contains(printed, want) {
			t.Errorf("printed invocation is missing %q\ngot: %s", want, printed)
		}
	}
	for _, unwanted := range []string{"Bash(go:*)", "gh pr create", "gh issue edit", "gh api", "gh issue view", "gh search issues"} {
		if strings.Contains(printed, unwanted) {
			t.Errorf("health invocation carries %q, which its allowlist must not", unwanted)
		}
	}

	said := buf.String()
	for _, want := range []string{
		"focus: only cmd/polako",
		"issue cap: 7",
		"dry run — no proposed label",
	} {
		if !strings.Contains(said, want) {
			t.Errorf("narration is missing %q\ngot:\n%s", want, said)
		}
	}
}

// With no -focus, healthPrompt is the bare slash command — no trailing `""`
// placeholder nobody needed.
func TestHealthPromptOmitsArgumentsWhenFocusIsEmpty(t *testing.T) {
	cfg, _, _ := healthTestConfig(t, &ghState{})
	cfg.skill = defaultHealthSkill
	if got, want := healthPrompt(cfg, healthOptions{}), "/"+defaultHealthSkill; got != want {
		t.Errorf("healthPrompt with no focus = %q, want %q", got, want)
	}
}

// healthRunConfig is healthTestConfig plus what a real run needs the
// preflight would otherwise fill in.
func healthRunConfig(t *testing.T, st *ghState, claudeMode string) (config, string) {
	t.Helper()
	cfg, statePath, _ := healthTestConfig(t, st)
	cfg.repo, cfg.ghRepo = "example/repo", "example/repo"
	t.Setenv(fakeClaudeEnv, claudeMode)
	return cfg, statePath
}

// End to end: a real run spawns the skill through execClaude and the pass
// normalises what it filed — no milestone attached, unlike plan's.
func TestHealthRunSpawnsTheSkillAndNormalisesWhatItCreated(t *testing.T) {
	buf := captureLog(t)
	cfg, statePath := healthRunConfig(t, &ghState{Labels: []string{proposedLabel}}, "plan")

	opt := healthOptions{maxIssues: 10}
	cfg.maxIssues = opt.maxIssues
	if err := healthRun(context.Background(), cfg, opt, io.Discard); err != nil {
		t.Fatalf("healthRun: %v", err)
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
		if is.Milestone != "" {
			t.Errorf("a created issue has milestone %q, want none", is.Milestone)
		}
	}
	if mine != 3 {
		t.Errorf("the run created %d issues, want 3", mine)
	}
	if !strings.Contains(buf.String(), "3 issues created, 3 normalised to "+proposedLabel) {
		t.Errorf("the pass summary is missing from the log:\n%s", buf.String())
	}
}

// When it ends, a health run leaves the two traces every run leaves: one
// kind:"health" record whatever its status, and — because it proposed
// something — one `proposed` notification.
func TestHealthRunRecordsAndNotifies(t *testing.T) {
	captureLog(t)
	cfg, _ := healthRunConfig(t, &ghState{Labels: []string{proposedLabel}}, "plan")
	records := t.TempDir()
	cfg.rec = newRecorder(records)
	cfg.shiftID = "healthshift"
	cfg.tag = "terse"
	told := notifyLog(t, &cfg)

	opt := healthOptions{maxIssues: 10}
	cfg.maxIssues = opt.maxIssues
	if err := healthRun(context.Background(), cfg, opt, io.Discard); err != nil {
		t.Fatalf("healthRun: %v", err)
	}

	lines := readRecords(t, records, cfg.repo)
	if len(lines) != 1 {
		t.Fatalf("wrote %d records, want exactly one health record", len(lines))
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatalf("record is not JSON: %v", err)
	}
	for key, want := range map[string]any{
		"kind": "health", "shift": "healthshift", "repo": "example/repo",
		"status": "ok", "tag": "terse",
		"issues_created": float64(3), "epics_created": float64(0),
		"cap": float64(10), "labels_enforced": float64(2),
	} {
		if rec[key] != want {
			t.Errorf("record[%q] = %v, want %v", key, rec[key], want)
		}
	}
	if _, present := rec["vision"]; present {
		t.Error("a health record carries a vision field, which only plan's has")
	}

	got := told()
	if len(got) != 1 {
		t.Fatalf("notifications = %v, want one for the proposals awaiting curation", got)
	}
	for _, want := range []string{
		notifyPrefix + "EVENT=proposed",
		"3 proposals await curation",
		"remove the " + proposedLabel + " label",
	} {
		if !strings.Contains(got[0], want) {
			t.Errorf("the proposed notification is missing %q\ngot: %s", want, got[0])
		}
	}
}

// The cap: dispatchClaude kills the run at -max-issues, healthRun reports it
// rather than raising it, and the label pass still normalises everything that
// was filed. Nothing is closed.
func TestHealthRunCapsIssueCreationAndStillNormalises(t *testing.T) {
	buf := captureLog(t)
	cfg, statePath := healthRunConfig(t, &ghState{Labels: []string{proposedLabel}}, "plancap")

	opt := healthOptions{maxIssues: 3}
	cfg.maxIssues = opt.maxIssues
	if err := healthRun(context.Background(), cfg, opt, io.Discard); err != nil {
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

// A health run that proposed nothing fires no notification but still writes
// its record.
func TestHealthRunWithNoProposalsRecordsButDoesNotNotify(t *testing.T) {
	captureLog(t)
	cfg, _ := healthRunConfig(t, &ghState{Labels: []string{proposedLabel}}, "planempty")
	records := t.TempDir()
	cfg.rec = newRecorder(records)
	cfg.shiftID = "healthshift"
	told := notifyLog(t, &cfg)

	opt := healthOptions{maxIssues: 10}
	cfg.maxIssues = opt.maxIssues
	if err := healthRun(context.Background(), cfg, opt, io.Discard); err != nil {
		t.Fatalf("healthRun: %v", err)
	}

	if got := told(); got != nil {
		t.Errorf("a run that proposed nothing still notified: %v", got)
	}
	lines := readRecords(t, records, cfg.repo)
	if len(lines) != 1 {
		t.Fatalf("wrote %d records, want the one health record regardless", len(lines))
	}
	if !strings.Contains(lines[0], `"issues_created":0`) {
		t.Errorf("record does not show zero issues created:\n%s", lines[0])
	}
}

// A shutdown signal mid-run surfaces as context.Canceled, and the label pass
// still ran on its own detached deadline — the same contract planRun holds.
func TestHealthRunInterruptReportsAsCancelled(t *testing.T) {
	captureLog(t)
	cfg, statePath := healthRunConfig(t, &ghState{Labels: []string{proposedLabel}}, "plancap")

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()

	opt := healthOptions{maxIssues: 10}
	cfg.maxIssues = opt.maxIssues
	err := healthRun(ctx, cfg, opt, io.Discard)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("an interrupted health run returned %v, want context.Canceled", err)
	}

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

// The -skill default resolves to the skill this repo actually ships.
func TestHealthSkillDefaultMatchesTheShippedSkill(t *testing.T) {
	if defaultHealthSkill != "polako:"+healthSkillDir {
		t.Fatalf("defaultHealthSkill = %q, want polako:%s", defaultHealthSkill, healthSkillDir)
	}
	if _, err := os.Stat(filepath.Join(repoRoot(), "skills", healthSkillDir, "SKILL.md")); err != nil {
		t.Errorf("-skill defaults to %q but skills/%s/SKILL.md is not there: %v", defaultHealthSkill, healthSkillDir, err)
	}
}
