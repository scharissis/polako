package main

// docs/plans/setup.md ticket 5's template half. setupTemplatesRow is a pure
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

func TestSetupTemplatesRowFlagsAPolicyPrefix(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeIssueTemplate(t, dir, "epic.yml", "name: Epic\nlabels: [\"model:opus\"]\nbody: []\n")
	row := setupTemplatesRow(config{dir: dir})
	if row.status != setupMissing || !row.required {
		t.Errorf("row = %+v, want a required missing row for a model: label", row)
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
