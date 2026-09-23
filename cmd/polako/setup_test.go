package main

// `polako setup` never writes, so its tests read the report the way tidy's
// own tests read reclaim: build a config against the fake gh and a real git
// checkout (readSetup needs origin/HEAD to resolve, the same fixture
// sync_test.go's upstream provides), call the reader directly, and check the
// rows — the same shape TestRunTidyRejectsAnArgument and reclaim's own tests
// already hold to, rather than driving the whole binary through PATH.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// setupRepoOKRow calls queueGate rather than re-implementing its condition,
// so the note here is exactly what a real `polako work` run would refuse
// on — proved by checking for queueGate's own wording rather than a copy of
// it.
func TestSetupRepoOKRowNotesTheQueueGate(t *testing.T) {
	t.Parallel()
	row := setupRepoOKRow(setupRepoView{Visibility: "PUBLIC"}, "")
	gateErr := queueGate("PUBLIC", "", false, "")
	if !strings.Contains(row.detail, gateErr.Error()) {
		t.Errorf("detail = %q, want it to contain queueGate's own message %q", row.detail, gateErr.Error())
	}

	labelled := setupRepoOKRow(setupRepoView{Visibility: "PUBLIC"}, "ready")
	if strings.Contains(labelled.detail, "pass -label") {
		t.Errorf("detail = %q, a -label already given should not repeat queueGate's advice", labelled.detail)
	}
}

// -dir not being a git checkout at all is a different failure than
// origin/HEAD merely being unset, and needs a different remedy: `git
// remote set-head origin -a` would itself fail with the same error.
func TestSetupOriginHeadRowDistinguishesANonCheckout(t *testing.T) {
	t.Parallel()
	cfg := config{dir: t.TempDir()} // no .git here at all
	row := setupOriginHeadRow(context.Background(), cfg, true)
	if row.status != setupMissing || !row.required {
		t.Errorf("row = %+v, want a required missing row", row)
	}
	if !strings.Contains(row.detail, "not a git checkout") {
		t.Errorf("detail = %q, want it to name -dir as not a git checkout rather than suggest "+
			"`git remote set-head`, which would fail here too", row.detail)
	}
}

// The bare invocation's verb table has to list setup now that it exists.
func TestVerbUsageListsSetup(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	verbUsage(&b)
	if !strings.Contains(b.String(), "\n  setup ") {
		t.Errorf("verbUsage does not list `setup`:\n%s", b.String())
	}
}

// The same flags-only contract every other verb's entry point holds to; see
// TestRunTidyRejectsAnArgument.
func TestRunSetupRejectsAnArgument(t *testing.T) {
	t.Parallel()
	err := runSetup(context.Background(), []string{"12"}, strings.NewReader(""), false, &strings.Builder{}, report{})
	if err == nil || !strings.Contains(err.Error(), "setup takes flags only") {
		t.Errorf("err = %v, want a complaint about the argument", err)
	}
}

// setupCfg writes st to a fake gh state file and returns a config wired to
// it, the handshake carried on config.env the way tidyCfg's is, so no test
// here needs t.Setenv.
func setupCfg(t *testing.T, st *ghState, checkout string) config {
	t.Helper()
	if st.Repo == "" {
		st.Repo = "example/repo"
	}
	path := filepath.Join(t.TempDir(), "gh-state.json")
	if err := writeGhState(path, st); err != nil {
		t.Fatalf("writing fake gh state: %v", err)
	}
	return config{
		dir: checkout,
		// Any claude mode does: every claude call setup makes (--version,
		// --help, plugin list) is dispatched inside fakeClaude before its
		// mode switch is ever reached. Without one at all, TestMain cannot
		// tell the child is meant to impersonate claude and falls through to
		// re-running the whole suite as a subprocess instead (main_test.go's
		// TestMain).
		env:         fakeEnv(fakeGhEnv, path, fakeClaudeEnv, "stream"),
		ui:          testUI(t),
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
	t.Parallel()
	_, checkout := upstream(t)
	cfg := setupCfg(t, &ghState{}, checkout)

	_, rows, _ := readSetup(context.Background(), cfg, false)

	for _, name := range []string{needsHumanLabel, proposedLabel, awaitingAnswerLabel} {
		r := findSetupRow(t, rows, name)
		if r.status != setupMissing || !r.required {
			t.Errorf("row %q = %+v, want a required missing row", name, r)
		}
	}
	// design is offered but optional: a repo with no design requests loses
	// nothing by lacking it.
	if r := findSetupRow(t, rows, designLabel); r.status != setupMissing || r.required {
		t.Errorf("row %q = %+v, want missing and not required", designLabel, r)
	}
	if !setupFailed(rows) {
		t.Error("setupFailed(rows) = false, want true with every label missing")
	}
}

// With every label already there, the same report is clean end to end.
func TestReadSetupWithAllLabelsSucceeds(t *testing.T) {
	t.Parallel()
	_, checkout := upstream(t)
	cfg := setupCfg(t, &ghState{Labels: []string{needsHumanLabel, proposedLabel, awaitingAnswerLabel}}, checkout)

	_, rows, _ := readSetup(context.Background(), cfg, false)

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
	t.Parallel()
	_, checkout := upstream(t)
	cfg := setupCfg(t, &ghState{Repo: "example/widgets"}, checkout)
	if cfg.repo != "" {
		t.Fatalf("test setup: cfg.repo = %q, want empty before readSetup resolves it", cfg.repo)
	}

	resolved, _, _ := readSetup(context.Background(), cfg, false)

	if resolved.repo != "example/widgets" {
		t.Errorf("resolved.repo = %q, want %q", resolved.repo, "example/widgets")
	}
}

// A repository with Issues turned off is a required, failing row — not
// "couldn't tell", since this gh answered the question and the answer was
// no.
func TestReadSetupIssuesDisabledFailsTheReport(t *testing.T) {
	t.Parallel()
	disabled := false
	_, checkout := upstream(t)
	cfg := setupCfg(t, &ghState{
		Labels:        []string{needsHumanLabel, proposedLabel, awaitingAnswerLabel},
		IssuesEnabled: &disabled,
	}, checkout)

	_, rows, _ := readSetup(context.Background(), cfg, false)

	r := findSetupRow(t, rows, "issues enabled")
	if r.status != setupMissing || !r.required {
		t.Errorf("issues enabled row = %+v, want a required missing row", r)
	}
	if !setupFailed(rows) {
		t.Error("setupFailed(rows) = false, want true with Issues disabled")
	}
}

// A gh too old to serve hasIssuesEnabled must not take the rest of the repo
// row down with it — the same unknownJSONField fallback listOpenIssues
// already uses for a gh too old for sub-issues.
func TestReadSetupIssuesEnabledIsUnknownOnAnOldGh(t *testing.T) {
	t.Parallel()
	_, checkout := upstream(t)
	cfg := setupCfg(t, &ghState{
		Labels:               []string{needsHumanLabel, proposedLabel, awaitingAnswerLabel},
		NoIssuesEnabledField: true,
	}, checkout)

	_, rows, _ := readSetup(context.Background(), cfg, false)

	if r := findSetupRow(t, rows, "issues enabled"); r.status != setupUnknown {
		t.Errorf("issues enabled row = %+v, want %q on a gh that doesn't report it", r, setupUnknown)
	}
	if r := findSetupRow(t, rows, "gh repo view"); r.status != setupOK {
		t.Errorf("gh repo view row = %+v, want ok — the fallback should still answer the rest of the read", r)
	}
	if setupFailed(rows) {
		t.Errorf("an old gh must not fail the report on its own: %+v", rows)
	}
}

// -label names a fourth label outside the fixed table — checked as its own
// row, and required, since the operator is about to point `polako work` at
// it.
func TestReadSetupChecksTheGateLabelToo(t *testing.T) {
	t.Parallel()
	_, checkout := upstream(t)
	cfg := setupCfg(t, &ghState{Labels: []string{needsHumanLabel, proposedLabel, awaitingAnswerLabel}}, checkout)
	cfg.label = "ready"

	_, rows, _ := readSetup(context.Background(), cfg, false)

	r := findSetupRow(t, rows, "ready")
	if r.status != setupMissing || !r.required {
		t.Errorf("row %q = %+v, want a required missing row for the ungranted gate label", "ready", r)
	}
	if !setupFailed(rows) {
		t.Error("setupFailed(rows) = false, want true with the named gate label missing")
	}
}

// The gate-label row compares an existing label's own description against
// setup's marker — an operator's hand-made "ready" from before this feature
// existed reads as "exists, not marked as the gate label", not plain ok.
func TestReadSetupReportsAnUnmarkedGateLabel(t *testing.T) {
	t.Parallel()
	_, checkout := upstream(t)
	cfg := setupCfg(t, &ghState{
		Labels: []string{needsHumanLabel, proposedLabel, awaitingAnswerLabel, "ready"},
	}, checkout)
	cfg.label = "ready"

	_, rows, _ := readSetup(context.Background(), cfg, false)

	r := findSetupRow(t, rows, "ready")
	if r.status != setupOK || r.detail != unmarkedGateLabelDetail {
		t.Errorf("row %q = %+v, want ok with detail %q", "ready", r, unmarkedGateLabelDetail)
	}
	if setupFailed(rows) {
		t.Error("setupFailed(rows) = true, an existing-but-unmarked gate label must not fail the report")
	}
}

// A gate label that already carries the marker — created by a previous
// `setup -apply`, or marked by an earlier one — reads as plain ok, with
// nothing more to do.
func TestReadSetupGateLabelAlreadyMarkedIsPlainOK(t *testing.T) {
	t.Parallel()
	_, checkout := upstream(t)
	cfg := setupCfg(t, &ghState{
		Labels:            []string{needsHumanLabel, proposedLabel, awaitingAnswerLabel, "ready"},
		LabelDescriptions: map[string]string{"ready": gateLabelDescription},
	}, checkout)
	cfg.label = "ready"

	_, rows, _ := readSetup(context.Background(), cfg, false)

	r := findSetupRow(t, rows, "ready")
	if r.status != setupOK || r.detail != "" {
		t.Errorf("row %q = %+v, want plain ok", "ready", r)
	}
}

// -apply -yes on an unmarked existing gate label marks it with exactly one
// `gh label edit` call — the acceptance criteria's own example.
func TestApplySetupMarksAnExistingUnmarkedGateLabel(t *testing.T) {
	t.Parallel()
	_, checkout := upstream(t)
	cfg := setupCfg(t, &ghState{
		Labels: []string{needsHumanLabel, proposedLabel, awaitingAnswerLabel, "ready"},
	}, checkout)
	cfg.label = "ready"

	logPath := filepath.Join(t.TempDir(), "gh.log")
	setFakeEnv(&cfg, fakeGhLogEnv, logPath)

	cfg, rows, defs := readSetup(context.Background(), cfg, false)
	var out strings.Builder
	rows, _ = applySetup(context.Background(), newSetupPrompt(strings.NewReader(""), &out, true), cfg, rows, defs)

	r := findSetupRow(t, rows, "ready")
	if r.status != setupOK || r.detail != "" {
		t.Errorf("row %q = %+v after -apply -yes, want plain ok", "ready", r)
	}

	b, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("reading %s: %v", logPath, err)
	}
	edits := 0
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if strings.HasPrefix(line, "label edit ") {
			edits++
		}
	}
	if edits != 1 {
		t.Errorf("label edit calls = %d, want exactly 1:\n%s", edits, b)
	}
}

// Declining the mark prompt leaves the label unmarked — applySetup must not
// call `gh label edit` when the operator says no.
func TestApplySetupDecliningTheMarkPromptLeavesItUnmarked(t *testing.T) {
	t.Parallel()
	_, checkout := upstream(t)
	cfg := setupCfg(t, &ghState{
		Labels: []string{needsHumanLabel, proposedLabel, awaitingAnswerLabel, "ready"},
	}, checkout)
	cfg.label = "ready"

	cfg, rows, defs := readSetup(context.Background(), cfg, false)
	var out strings.Builder
	rows, _ = applySetup(context.Background(), newSetupPrompt(strings.NewReader("n\n"), &out, false), cfg, rows, defs)

	r := findSetupRow(t, rows, "ready")
	if r.detail != unmarkedGateLabelDetail {
		t.Errorf("row %q = %+v after declining, want it to stay unmarked", "ready", r)
	}
}

// A read gh cannot answer is "couldn't tell", never a failure — the fake
// CLI answers nothing for `claude plugin list --json` unless a test opts in
// (fakePluginEnv), which this one does not.
func TestReadSetupPluginRowIsUnknownRatherThanFailingWithNoFixture(t *testing.T) {
	t.Parallel()
	_, checkout := upstream(t)
	cfg := setupCfg(t, &ghState{Labels: []string{needsHumanLabel, proposedLabel, awaitingAnswerLabel}}, checkout)

	_, rows, _ := readSetup(context.Background(), cfg, false)

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
	t.Parallel()
	_, checkout := upstream(t)
	cfg := setupCfg(t, &ghState{}, checkout)

	logPath := filepath.Join(t.TempDir(), "gh.log")
	setFakeEnv(&cfg, fakeGhLogEnv, logPath)

	readSetup(context.Background(), cfg, false)

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

// -policy-labels adds the tier-alias set to the table — the report itself,
// not only -apply's own offer — so an operator can preview it before writing
// anything.
func TestPolicyLabelDefsAreTierAliasesOnly(t *testing.T) {
	t.Parallel()
	defs := policyLabelDefs()
	var names []string
	for _, d := range defs {
		names = append(names, d.name)
	}
	for _, want := range []string{"model:opus", "model:sonnet", "model:haiku", "model:default",
		"effort:low", "effort:medium", "effort:high", "effort:xhigh", "effort:max"} {
		if !slices.Contains(names, want) {
			t.Errorf("policyLabelDefs() = %v, missing %q", names, want)
		}
	}
	for _, unwanted := range []string{"model:best", "model:claude-opus-5"} {
		if slices.Contains(names, unwanted) {
			t.Errorf("policyLabelDefs() = %v, must not offer %q — tier aliases only", names, unwanted)
		}
	}
}

// -label naming a policy-label name (an odd but real invocation) must not
// produce two labelDefs for the same name — the second create attempt would
// otherwise fail "already exists" right after the first one's own success.
// required is promoted to true, since a name given through -label is always
// required, whichever source in the list carries that flag.
func TestSetupLabelDefsDedupesALabelMatchingAPolicyName(t *testing.T) {
	t.Parallel()
	cfg := config{label: "model:opus"}
	defs := setupLabelDefs(cfg, true)

	var matches []labelDef
	for _, d := range defs {
		if d.name == "model:opus" {
			matches = append(matches, d)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("setupLabelDefs() has %d entries named %q, want 1: %+v", len(matches), "model:opus", matches)
	}
	if !matches[0].required {
		t.Errorf("model:opus = %+v, want required — it came in through -label", matches[0])
	}
}

// readSetup shows the policy labels in the report too, not only when -apply
// is given — an operator previewing what -policy-labels would offer.
func TestReadSetupIncludesPolicyLabelRowsWhenRequested(t *testing.T) {
	t.Parallel()
	_, checkout := upstream(t)
	cfg := setupCfg(t, &ghState{}, checkout)

	_, rows, _ := readSetup(context.Background(), cfg, true)

	if r := findSetupRow(t, rows, "model:opus"); r.status != setupMissing || r.required {
		t.Errorf("model:opus row = %+v, want missing and not required", r)
	}

	_, plain, _ := readSetup(context.Background(), cfg, false)
	for _, r := range plain {
		if r.name == "model:opus" {
			t.Error("readSetup(..., false) must not add policy label rows")
		}
	}
}

// The point of issue #415: -apply -yes creates exactly the missing required
// labels, and running it again creates nothing more.
func TestApplySetupCreatesMissingRequiredLabelsIdempotently(t *testing.T) {
	t.Parallel()
	_, checkout := upstream(t)
	cfg := setupCfg(t, &ghState{}, checkout)

	cfg, rows, defs := readSetup(context.Background(), cfg, false)
	var out strings.Builder
	rows, _ = applySetup(context.Background(), newSetupPrompt(strings.NewReader(""), &out, true), cfg, rows, defs)

	for _, name := range []string{needsHumanLabel, proposedLabel, awaitingAnswerLabel} {
		r := findSetupRow(t, rows, name)
		if r.status != setupOK {
			t.Errorf("row %q = %+v, want ok after -apply -yes", name, r)
		}
	}
	if setupFailed(rows) {
		t.Errorf("setupFailed(rows) = true after -apply -yes created every required label: %+v", rows)
	}

	// A second pass finds nothing left to create.
	_, rows2, _ := readSetup(context.Background(), cfg, false)
	var out2 strings.Builder
	rows2, _ = applySetup(context.Background(), newSetupPrompt(strings.NewReader(""), &out2, true), cfg, rows2, defs)
	if out2.String() != "" {
		t.Errorf("second -apply -yes run wrote %q, want nothing left to create", out2.String())
	}
}

// An EOF on stdin (a closed terminal, or Ctrl-D) is not an empty line: it
// must decline rather than accept the default, and every remaining step in
// the same run must decline too, since a Scanner keeps returning false once
// its reader is exhausted. Before this fix, EOF read as "yes" and every
// missing required label got created silently.
func TestApplySetupEOFDeclinesRatherThanAcceptsTheDefault(t *testing.T) {
	t.Parallel()
	_, checkout := upstream(t)
	cfg := setupCfg(t, &ghState{}, checkout)

	cfg, rows, defs := readSetup(context.Background(), cfg, false)
	var out strings.Builder
	rows, _ = applySetup(context.Background(), newSetupPrompt(strings.NewReader(""), &out, false), cfg, rows, defs)

	for _, name := range []string{needsHumanLabel, proposedLabel, awaitingAnswerLabel} {
		if r := findSetupRow(t, rows, name); r.status != setupMissing {
			t.Errorf("row %q = %+v, want still missing — EOF must decline, not accept the default", name, r)
		}
	}
}

// Declining a step leaves that label missing — applySetup must not create it,
// and the row it hands back still says so.
func TestApplySetupAnsweringNoLeavesTheLabelMissing(t *testing.T) {
	t.Parallel()
	_, checkout := upstream(t)
	cfg := setupCfg(t, &ghState{}, checkout)

	cfg, rows, defs := readSetup(context.Background(), cfg, false)
	var out strings.Builder
	// One "n" per required label in the table, isTTY-style answers, not -yes.
	in := strings.NewReader("n\nn\nn\n")
	rows, _ = applySetup(context.Background(), newSetupPrompt(in, &out, false), cfg, rows, defs)

	for _, name := range []string{needsHumanLabel, proposedLabel, awaitingAnswerLabel} {
		r := findSetupRow(t, rows, name)
		if r.status != setupMissing {
			t.Errorf("row %q = %+v, want still missing after declining", name, r)
		}
	}
	if !setupFailed(rows) {
		t.Error("setupFailed(rows) = false, want true — every required label was declined")
	}

	_, rows2, _ := readSetup(context.Background(), cfg, false)
	for _, name := range []string{needsHumanLabel, proposedLabel, awaitingAnswerLabel} {
		if r := findSetupRow(t, rows2, name); r.status != setupMissing {
			t.Errorf("second read: row %q = %+v, want still missing — nothing should have been created", name, r)
		}
	}
}

// A public repository with no -label given gets asked to name one, "ready"
// suggested — and -yes takes that suggestion without asking.
func TestApplySetupPromptsForGateLabelOnPublicRepoWithNoLabel(t *testing.T) {
	t.Parallel()
	_, checkout := upstream(t)
	cfg := setupCfg(t, &ghState{Visibility: "PUBLIC",
		Labels: []string{needsHumanLabel, proposedLabel, awaitingAnswerLabel}}, checkout)

	cfg, rows, defs := readSetup(context.Background(), cfg, false)
	if cfg.visibility != "PUBLIC" {
		t.Fatalf("test setup: cfg.visibility = %q, want PUBLIC", cfg.visibility)
	}
	var out strings.Builder
	rows, gateLabel := applySetup(context.Background(), newSetupPrompt(strings.NewReader(""), &out, true), cfg, rows, defs)

	if gateLabel != "ready" {
		t.Errorf("gateLabel = %q, want %q — the caller needs this to update the suggested `polako work` line", gateLabel, "ready")
	}
	r := findSetupRow(t, rows, "ready")
	if r.status != setupOK {
		t.Errorf("row %q = %+v, want ok — -yes should have taken the suggested name and created it", "ready", r)
	}

	exists, err := labelExists(context.Background(), cfg, "ready")
	if err != nil || !exists {
		t.Errorf("labelExists(ready) = %v, %v, want true, nil", exists, err)
	}
}

// A label created by someone else between the read pass and applySetup's own
// create (simulated here by creating it directly first, so applySetup's own
// ensureLabel call fails "already exists") is not a write failure: the row
// ends up ok, and the output never says the run needs write access.
func TestApplySetupTreatsAlreadyExistsAsSuccess(t *testing.T) {
	t.Parallel()
	_, checkout := upstream(t)
	cfg := setupCfg(t, &ghState{}, checkout)

	cfg, rows, defs := readSetup(context.Background(), cfg, false)
	l := labelByName(needsHumanLabel)
	if err := ensureLabel(context.Background(), cfg, l.name, l.color, l.description); err != nil {
		t.Fatalf("test setup: ensureLabel: %v", err)
	}

	var out strings.Builder
	rows, _ = applySetup(context.Background(), newSetupPrompt(strings.NewReader(""), &out, true), cfg, rows, defs)

	r := findSetupRow(t, rows, needsHumanLabel)
	if r.status != setupOK {
		t.Errorf("row %q = %+v, want ok — the label exists, which is what the create wanted", needsHumanLabel, r)
	}
	if strings.Contains(out.String(), "needs write access") {
		t.Errorf("output = %q, an already-existing label must not read as a write failure", out.String())
	}
}

// A create the repository refuses (no write access) says so in plain words,
// never the raw gh stderr — which would otherwise be an HTTP 403 body an
// operator has to decode.
func TestApplySetupRefusedCreateNamesWriteAccessNotRawStderr(t *testing.T) {
	t.Parallel()
	_, checkout := upstream(t)
	cfg := setupCfg(t, &ghState{DenyLabelCreate: true}, checkout)

	cfg, rows, defs := readSetup(context.Background(), cfg, false)
	var out strings.Builder
	rows, _ = applySetup(context.Background(), newSetupPrompt(strings.NewReader(""), &out, true), cfg, rows, defs)

	if !strings.Contains(out.String(), "needs write access to "+cfg.repo) {
		t.Errorf("output = %q, want it to name %q needing write access", out.String(), cfg.repo)
	}
	if strings.Contains(out.String(), "HTTP 403") {
		t.Errorf("output = %q, leaked raw gh stderr", out.String())
	}
	for _, name := range []string{needsHumanLabel, proposedLabel, awaitingAnswerLabel} {
		if r := findSetupRow(t, rows, name); r.status != setupMissing {
			t.Errorf("row %q = %+v, want still missing — the create was refused", name, r)
		}
	}
}

// -apply with stdin that is not a terminal and no -yes refuses before making
// any call, naming -yes, exit code 2 (errFlagsReported) — never a blocking
// read.
func TestRunSetupApplyNeedsYesWithoutATerminal(t *testing.T) {
	t.Parallel()
	var out strings.Builder
	err := runSetup(context.Background(), []string{"-apply"}, strings.NewReader(""), false, &out, report{})
	if !errors.Is(err, errFlagsReported) {
		t.Errorf("err = %v, want errFlagsReported", err)
	}
	if !strings.Contains(out.String(), "-yes") {
		t.Errorf("output = %q, want it to name -yes", out.String())
	}
}

// The same refusal must not fire on a terminal, or once -yes is given, or
// without -apply at all — only the combination of a write that would ask and
// no way to ask it blocks.
func TestSetupApplyNeedsYes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		apply, yes, isTTY bool
		want              bool
	}{
		{apply: true, yes: false, isTTY: false, want: true},
		{apply: true, yes: true, isTTY: false, want: false},
		{apply: true, yes: false, isTTY: true, want: false},
		{apply: true, yes: true, isTTY: true, want: false},
		{apply: false, yes: false, isTTY: false, want: false},
	}
	for _, c := range cases {
		if got := setupApplyNeedsYes(c.apply, c.yes, c.isTTY); got != c.want {
			t.Errorf("setupApplyNeedsYes(%v, %v, %v) = %v, want %v", c.apply, c.yes, c.isTTY, got, c.want)
		}
	}
}
