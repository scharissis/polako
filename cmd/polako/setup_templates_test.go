package main

// issue #418's template half. setupTemplatesRow is a pure
// function over a directory, tested directly the same way setupGitignoreRow's
// own tests are.

import (
	"os"
	"path/filepath"
	"testing"
)

func writeIssueTemplate(t *testing.T, dir, name, content string) {
	t.Helper()
	full := filepath.Join(dir, ".github", "ISSUE_TEMPLATE")
	if err := os.MkdirAll(full, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", full, err)
	}
	if err := os.WriteFile(filepath.Join(full, name), []byte(content), 0o644); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
}

func TestSetupTemplatesRowOKWithNoTemplatesDir(t *testing.T) {
	t.Parallel()
	if row := setupTemplatesRow(config{dir: t.TempDir()}); row.status != setupOK {
		t.Errorf("row = %+v, want ok with no .github/ISSUE_TEMPLATE at all", row)
	}
}

// A review finding: a real read failure (ISSUE_TEMPLATE existing as a plain
// file, here — permission-denied is the same class) must not read as "no
// templates" on this required row.
func TestSetupTemplatesRowUnknownOnAReadFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".github"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// ISSUE_TEMPLATE as a plain file, not a directory: os.ReadDir fails with
	// ENOTDIR, not ErrNotExist.
	if err := os.WriteFile(filepath.Join(dir, ".github", "ISSUE_TEMPLATE"), nil, 0o644); err != nil {
		t.Fatalf("writing: %v", err)
	}
	row := setupTemplatesRow(config{dir: dir})
	if row.status != setupUnknown {
		t.Errorf("row = %+v, want %q — a real read failure, not \"no templates\"", row, setupUnknown)
	}
}

func TestSetupTemplatesRowOKWithCleanTemplates(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeIssueTemplate(t, dir, "bug.yml", "name: Bug\nlabels: [\"bug\"]\nbody: []\n")
	if row := setupTemplatesRow(config{dir: dir}); row.status != setupOK {
		t.Errorf("row = %+v, want ok — %q is not orchestration state", row, "bug")
	}
}

// The plan's own "done when": a template naming -label's own gate label fails
// the row and, since it's required, the exit code.
func TestSetupTemplatesRowFlagsTheGateLabel(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeIssueTemplate(t, dir, "bug.yml", "name: Bug\nlabels: [\"ready\"]\nbody: []\n")
	row := setupTemplatesRow(config{dir: dir, label: "ready"})
	if row.status != setupMissing || !row.required {
		t.Errorf("row = %+v, want a required missing row", row)
	}
	if !setupFailed([]setupRow{row}) {
		t.Error("setupFailed = false, want true — a leaky template must fail the report")
	}
}

func TestSetupTemplatesRowFlagsAManagedLabel(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeIssueTemplate(t, dir, "feature.yml", "name: Feature\nlabels:\n  - "+proposedLabel+"\nbody: []\n")
	row := setupTemplatesRow(config{dir: dir})
	if row.status != setupMissing || !row.required {
		t.Errorf("row = %+v, want a required missing row for a dash-list %q", row, proposedLabel)
	}
}

// A review finding: the label-line scan must not depend on quoting — an
// unquoted flow item, a bare scalar, and a single-quoted dash item are all
// valid YAML spellings of the same leak.
func TestSetupTemplatesRowFlagsEveryYAMLQuotingForm(t *testing.T) {
	t.Parallel()
	cases := []string{
		"name: Bug\nlabels: [proposed]\nbody: []\n",
		"name: Bug\nlabels: proposed\nbody: []\n",
		"name: Bug\nlabels:\n  - 'proposed'\nbody: []\n",
	}
	for _, content := range cases {
		dir := t.TempDir()
		writeIssueTemplate(t, dir, "bug.yml", content)
		row := setupTemplatesRow(config{dir: dir})
		if row.status != setupMissing || !row.required {
			t.Errorf("content %q: row = %+v, want a required missing row", content, row)
		}
	}
}

func TestSetupTemplatesRowFlagsAPolicyPrefix(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeIssueTemplate(t, dir, "epic.yml", "name: Epic\nlabels: [\"model:opus\"]\nbody: []\n")
	row := setupTemplatesRow(config{dir: dir})
	if row.status != setupMissing || !row.required {
		t.Errorf("row = %+v, want a required missing row for a model: label", row)
	}
}

// A review finding: the model:/effort: check must anchor to the start of the
// label token, not match anywhere in the line — an unrelated custom label
// that merely contains "model:" isn't a policy label.
func TestSetupTemplatesRowIgnoresALabelThatOnlyContainsModelPrefix(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeIssueTemplate(t, dir, "epic.yml", "name: Epic\nlabels: [\"risk-model:high\"]\nbody: []\n")
	if row := setupTemplatesRow(config{dir: dir}); row.status != setupOK {
		t.Errorf("row = %+v, want ok — %q is not a model:/effort: policy label", row, "risk-model:high")
	}
}

// A non-YAML file, or a labels: key nobody named as forbidden, must not trip
// the row.
func TestSetupTemplatesRowIgnoresUnrelatedFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeIssueTemplate(t, dir, "config.yml", "blank_issues_enabled: false\n")
	writeIssueTemplate(t, dir, "README.md", "labels: [\"proposed\"]\n") // not .yml/.yaml
	if row := setupTemplatesRow(config{dir: dir}); row.status != setupOK {
		t.Errorf("row = %+v, want ok — no .yml/.yaml template names a forbidden label", row)
	}
}
