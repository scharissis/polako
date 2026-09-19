package main

// `polako setup` never writes, so its tests read the report the way tidy's
// own tests read reclaim: build a config against the fake gh and a real git
// checkout (readSetup needs origin/HEAD to resolve, the same fixture
// sync_test.go's upstream provides), call the reader directly, and check the
// rows — the same shape TestRunTidyRejectsAnArgument and reclaim's own tests
// already hold to, rather than driving the whole binary through PATH.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// setupRepoOKRow calls queueGate rather than re-implementing its condition,
// so the note here is exactly what a real `polako work` run would refuse
// on — proved by checking for queueGate's own wording rather than a copy of
// it.
func TestSetupRepoOKRowNotesTheQueueGate(t *testing.T) {
	row := setupRepoOKRow(setupRepoView{Visibility: "PUBLIC"}, "")
	gateErr := queueGate("PUBLIC", "", false)
	if !strings.Contains(row.detail, gateErr.Error()) {
		t.Errorf("detail = %q, want it to contain queueGate's own message %q", row.detail, gateErr.Error())
	}

	labelled := setupRepoOKRow(setupRepoView{Visibility: "PUBLIC"}, "ready")
	if strings.Contains(labelled.detail, "pass -label") {
		t.Errorf("detail = %q, a -label already given should not repeat queueGate's advice", labelled.detail)
	}
}

// The bare invocation's verb table has to list setup now that it exists.
func TestVerbUsageListsSetup(t *testing.T) {
	var b strings.Builder
	verbUsage(&b)
	if !strings.Contains(b.String(), "\n  setup ") {
		t.Errorf("verbUsage does not list `setup`:\n%s", b.String())
	}
}

// The same flags-only contract every other verb's entry point holds to; see
// TestRunTidyRejectsAnArgument.
func TestRunSetupRejectsAnArgument(t *testing.T) {
	err := runSetup(context.Background(), []string{"12"}, strings.NewReader(""), &strings.Builder{}, report{})
	if err == nil || !strings.Contains(err.Error(), "setup takes flags only") {
		t.Errorf("err = %v, want a complaint about the argument", err)
	}
}

func setupCfg(t *testing.T, checkout string) config {
	t.Helper()
	// Any mode does: every claude call setup makes (--version, --help,
	// plugin list) is dispatched inside fakeClaude before its mode switch is
	// ever reached. Without this set at all, TestMain cannot tell the child
	// is meant to impersonate claude and falls through to re-running the
	// whole suite as a subprocess instead (main_test.go's TestMain).
	t.Setenv(fakeClaudeEnv, "stream")
	return config{
		dir:         checkout,
		ghBin:       fakeCLI(t),
		claudeBin:   fakeCLI(t),
		skill:       defaultSkill,
		ghRetryWait: time.Millisecond,
	}
}

func findSetupRow(t *testing.T, rows []setupRow, name string) setupRow {
	t.Helper()
	for _, r := range rows {
		if r.name == name {
			return r
		}
	}
	t.Fatalf("no row named %q among %+v", name, rows)
	return setupRow{}
}

// The point of the whole issue: a repository with none of the three labels
// names all three as missing and fails the report.
func TestReadSetupNamesMissingLabelsAndFails(t *testing.T) {
	_, checkout := upstream(t)
	tidyGh(t, &ghState{})
	cfg := setupCfg(t, checkout)

	_, rows := readSetup(context.Background(), cfg)

	for _, name := range []string{needsHumanLabel, proposedLabel, awaitingAnswerLabel} {
		r := findSetupRow(t, rows, name)
		if r.status != setupMissing || !r.required {
			t.Errorf("row %q = %+v, want a required missing row", name, r)
		}
	}
	if !setupFailed(rows) {
		t.Error("setupFailed(rows) = false, want true with every label missing")
	}
}

// With every label already there, the same report is clean end to end.
func TestReadSetupWithAllLabelsSucceeds(t *testing.T) {
	_, checkout := upstream(t)
	tidyGh(t, &ghState{Labels: []string{needsHumanLabel, proposedLabel, awaitingAnswerLabel}})
	cfg := setupCfg(t, checkout)

	_, rows := readSetup(context.Background(), cfg)

	for _, name := range []string{needsHumanLabel, proposedLabel, awaitingAnswerLabel} {
		r := findSetupRow(t, rows, name)
		if r.status != setupOK {
			t.Errorf("row %q = %+v, want ok", name, r)
		}
	}
	if setupFailed(rows) {
		t.Errorf("setupFailed(rows) = true, want false with every label present: %+v", rows)
	}
}

// readSetup resolves the repository name when -repo was not given, and
// hands that back in cfg — runSetup passes this returned cfg to
// renderSetup, not its own pre-resolution copy, so the report's header
// names the repo it actually checked rather than falling back to -dir.
func TestReadSetupResolvesTheRepoName(t *testing.T) {
	_, checkout := upstream(t)
	tidyGh(t, &ghState{Repo: "example/widgets"})
	cfg := setupCfg(t, checkout)
	if cfg.repo != "" {
		t.Fatalf("test setup: cfg.repo = %q, want empty before readSetup resolves it", cfg.repo)
	}

	resolved, _ := readSetup(context.Background(), cfg)

	if resolved.repo != "example/widgets" {
		t.Errorf("resolved.repo = %q, want %q", resolved.repo, "example/widgets")
	}
}

// -label names a fourth label outside the fixed table — checked as its own
// row, and required, since the operator is about to point `polako work` at
// it.
func TestReadSetupChecksTheGateLabelToo(t *testing.T) {
	_, checkout := upstream(t)
	tidyGh(t, &ghState{Labels: []string{needsHumanLabel, proposedLabel, awaitingAnswerLabel}})
	cfg := setupCfg(t, checkout)
	cfg.label = "ready"

	_, rows := readSetup(context.Background(), cfg)

	r := findSetupRow(t, rows, "ready")
	if r.status != setupMissing || !r.required {
		t.Errorf("row %q = %+v, want a required missing row for the ungranted gate label", "ready", r)
	}
	if !setupFailed(rows) {
		t.Error("setupFailed(rows) = false, want true with the named gate label missing")
	}
}

// A read gh cannot answer is "couldn't tell", never a failure — the fake
// CLI answers nothing for `claude plugin list --json` unless a test opts in
// (fakePluginEnv), which this one does not.
func TestReadSetupPluginRowIsUnknownRatherThanFailingWithNoFixture(t *testing.T) {
	_, checkout := upstream(t)
	tidyGh(t, &ghState{Labels: []string{needsHumanLabel, proposedLabel, awaitingAnswerLabel}})
	cfg := setupCfg(t, checkout)

	_, rows := readSetup(context.Background(), cfg)

	r := findSetupRow(t, rows, "plugin")
	if r.status != setupUnknown {
		t.Errorf("plugin row = %+v, want %q", r, setupUnknown)
	}
	if setupFailed(rows) {
		t.Errorf("an unknown plugin version must not fail the report: %+v", rows)
	}
}

// Every call setup makes is a read — no `gh label create`, no `gh issue
// create` past its own --help probe — proved the way POLAKO_FAKE_GH_LOG
// proves it for every other verb here.
func TestReadSetupMakesReadsOnly(t *testing.T) {
	_, checkout := upstream(t)
	tidyGh(t, &ghState{})
	cfg := setupCfg(t, checkout)

	logPath := filepath.Join(t.TempDir(), "gh.log")
	t.Setenv(fakeGhLogEnv, logPath)

	readSetup(context.Background(), cfg)

	b, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("reading %s: %v", logPath, err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == "create" && !strings.Contains(line, "--help") {
			t.Errorf("setup made a write call: %q", line)
		}
	}
}
