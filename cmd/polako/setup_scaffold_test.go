package main

// setup_scaffold.go's own tests: pure functions over a directory, no git or
// gh needed.

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSetupVisionRow(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if r := setupVisionRow(config{dir: dir}); r.status != setupMissing || r.required {
		t.Errorf("row with no docs/VISION.md = %+v, want missing and not required", r)
	}
	if err := os.MkdirAll(filepath.Join(dir, "docs"), 0o755); err != nil {
		t.Fatalf("mkdir docs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "docs", "VISION.md"), []byte("# Vision\n"), 0o644); err != nil {
		t.Fatalf("writing docs/VISION.md: %v", err)
	}
	if r := setupVisionRow(config{dir: dir}); r.status != setupOK {
		t.Errorf("row with docs/VISION.md present = %+v, want ok", r)
	}
}

func TestWriteScaffoldWritesBothPages(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if !scaffoldNeedsWrite(dir) {
		t.Fatalf("test setup: scaffoldNeedsWrite should be true on an empty dir")
	}
	if err := writeScaffold(dir); err != nil {
		t.Fatalf("writeScaffold: %v", err)
	}
	if scaffoldNeedsWrite(dir) {
		t.Errorf("scaffoldNeedsWrite still true after writeScaffold")
	}
	for _, rel := range []string{"docs/VISION.md", "docs/plans/README.md"} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel))); err != nil {
			t.Errorf("%s not written: %v", rel, err)
		}
	}
}

// A human's own docs/VISION.md is never overwritten — writeScaffold only
// fills in what's missing.
func TestWriteScaffoldNeverOverwritesAnExistingPage(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "docs"), 0o755); err != nil {
		t.Fatalf("mkdir docs: %v", err)
	}
	const human = "# My own vision\n"
	if err := os.WriteFile(filepath.Join(dir, "docs", "VISION.md"), []byte(human), 0o644); err != nil {
		t.Fatalf("writing docs/VISION.md: %v", err)
	}
	if err := writeScaffold(dir); err != nil {
		t.Fatalf("writeScaffold: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "docs", "VISION.md"))
	if err != nil {
		t.Fatalf("reading docs/VISION.md: %v", err)
	}
	if string(got) != human {
		t.Errorf("docs/VISION.md = %q, want the human's own content untouched", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "docs", "plans", "README.md")); err != nil {
		t.Errorf("docs/plans/README.md not written: %v", err)
	}
}
