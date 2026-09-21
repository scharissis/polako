package main

// docs/plans/setup.md ticket 5's allowlist half. setupBuildToolsRow is a pure
// function over a directory, tested directly the same way setupGitignoreRow's
// own tests are.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The point of the whole check: an entry in buildToolTable must not repeat a
// tool defaultTools already grants, or the advice would tell an operator to
// add something they already have.
func TestBuildToolTableDoesNotDuplicateDefaultTools(t *testing.T) {
	t.Parallel()
	for _, d := range buildToolTable {
		grant := "Bash(" + d.tool + ":*)"
		if strings.Contains(defaultTools, grant) {
			t.Errorf("buildToolTable has %q (marker %q), but defaultTools already grants %q",
				d.tool, d.marker, grant)
		}
	}
}

// The plan's own "done when": a checkout with a justfile prints
// -add-tools "Bash(just:*)" in the row.
func TestSetupBuildToolsRowNamesTheMissingGrant(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "justfile"), []byte("test:\n\tgo test ./...\n"), 0o644); err != nil {
		t.Fatalf("writing justfile: %v", err)
	}
	row := setupBuildToolsRow(config{dir: dir})
	if row.status != setupMissing || row.required {
		t.Errorf("row = %+v, want missing and not required (advisory)", row)
	}
	want := `-add-tools "Bash(just:*)"`
	if !strings.Contains(row.detail, want) {
		t.Errorf("detail = %q, want it to contain %q", row.detail, want)
	}
}

// BUILD.bazel and WORKSPACE both mean bazel — one grant, not two.
func TestSetupBuildToolsRowDedupesBazel(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, name := range []string{"BUILD.bazel", "WORKSPACE"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}
	row := setupBuildToolsRow(config{dir: dir})
	if got := strings.Count(row.detail, "bazel"); got != 1 {
		t.Errorf("detail = %q, want exactly one mention of bazel, got %d", row.detail, got)
	}
}

func TestSetupBuildToolsRowOKWithNoMarkers(t *testing.T) {
	t.Parallel()
	if row := setupBuildToolsRow(config{dir: t.TempDir()}); row.status != setupOK {
		t.Errorf("row = %+v, want ok with no build-tool markers", row)
	}
}

// The suggested `work` line carries the same -add-tools value the row does —
// the plan's own "done when" names both.
func TestSuggestedWorkLineCarriesAddTools(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "justfile"), nil, 0o644); err != nil {
		t.Fatalf("writing justfile: %v", err)
	}
	line := suggestedWorkLine(config{dir: dir})
	if !strings.Contains(line, `-add-tools "Bash(just:*)"`) {
		t.Errorf("suggestedWorkLine = %q, want it to carry -add-tools", line)
	}
}

func TestSuggestedWorkLineOmitsAddToolsWithNothingMissing(t *testing.T) {
	t.Parallel()
	line := suggestedWorkLine(config{dir: t.TempDir()})
	if strings.Contains(line, "-add-tools") {
		t.Errorf("suggestedWorkLine = %q, want no -add-tools with nothing uncovered", line)
	}
}
