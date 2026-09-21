package main

// setup_claude.go's own tests: the check-command detection is a pure
// function over a directory (table-tested, per the issue's own acceptance
// criteria), and the merge logic is tested directly against strings —
// neither needs git or gh.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClaudeMdCheckCommand(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"nothing detected", nil, claudeMdCheckCommandUnknown},
		{"scripts/check.sh", map[string]string{"scripts/check.sh": "#!/bin/sh\n"}, "./scripts/check.sh"},
		{"Makefile test target", map[string]string{"Makefile": "build:\n\tgo build ./...\n\ntest:\n\tgo test ./...\n"}, "make test"},
		{"Makefile without a test target", map[string]string{"Makefile": "build:\n\tgo build ./...\n"}, claudeMdCheckCommandUnknown},
		{"go.mod", map[string]string{"go.mod": "module example\n"}, "go test ./..."},
		{"package.json with a test script", map[string]string{"package.json": `{"scripts":{"test":"jest"}}`}, "npm test"},
		{"package.json without a test script", map[string]string{"package.json": `{"scripts":{"build":"tsc"}}`}, claudeMdCheckCommandUnknown},
		{"package.json npm init placeholder", map[string]string{
			"package.json": `{"scripts":{"test":"echo \"Error: no test specified\" && exit 1"}}`,
		}, claudeMdCheckCommandUnknown},
		{"package.json malformed", map[string]string{"package.json": `not json`}, claudeMdCheckCommandUnknown},
		{"Cargo.toml", map[string]string{"Cargo.toml": "[package]\nname = \"example\"\n"}, "cargo test"},
		{"pyproject.toml", map[string]string{"pyproject.toml": "[project]\nname = \"example\"\n"}, "pytest"},
		{"scripts/check.sh wins over go.mod", map[string]string{
			"scripts/check.sh": "#!/bin/sh\n", "go.mod": "module example\n",
		}, "./scripts/check.sh"},
		{"Makefile test target wins over go.mod", map[string]string{
			"Makefile": "test:\n\tgo test ./...\n", "go.mod": "module example\n",
		}, "make test"},
		{"GNUmakefile test target", map[string]string{"GNUmakefile": "test:\n\tgo test ./...\n"}, "make test"},
		{"lowercase makefile test target", map[string]string{"makefile": "test:\n\tgo test ./...\n"}, "make test"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			for rel, content := range c.files {
				full := filepath.Join(dir, rel)
				if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
					t.Fatalf("mkdir for %s: %v", rel, err)
				}
				if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
					t.Fatalf("writing %s: %v", rel, err)
				}
			}
			if got := claudeMdCheckCommand(dir); got != c.want {
				t.Errorf("claudeMdCheckCommand(%q) = %q, want %q", c.name, got, c.want)
			}
		})
	}
}

// The block itself: under the issue's 25-line cap, marked, and mentions the
// detected check command.
func TestClaudeMdBlockIsShortAndNamesTheCheckCommand(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example\n"), 0o644); err != nil {
		t.Fatalf("writing go.mod: %v", err)
	}
	block := claudeMdBlock(dir)
	if !strings.HasPrefix(block, claudeMdBeginMarker) || !strings.HasSuffix(block, claudeMdEndMarker) {
		t.Errorf("block = %q, want it to start and end with the markers", block)
	}
	if !strings.Contains(block, "go test ./...") {
		t.Errorf("block = %q, want it to name the detected check command", block)
	}
	if n := strings.Count(block, "\n") + 1; n > 25 {
		t.Errorf("block is %d lines, want at most 25", n)
	}
}

// Ties the block's wording to setupGitignoreLines directly, so the two can't
// drift the way they did before (the block once named a root-level PR_BODY.md
// that was never the real scratch path).
func TestClaudeMdBlockNamesEachGitignoreLine(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	block := claudeMdBlock(dir)
	for _, want := range setupGitignoreLines {
		if !strings.Contains(block, want) {
			t.Errorf("block = %q, want it to name %q", block, want)
		}
	}
}

func TestMergeClaudeMdCreatesWhenNoFile(t *testing.T) {
	t.Parallel()
	got := mergeClaudeMd("", "<!-- polako:begin -->\nx\n<!-- polako:end -->")
	if got != "<!-- polako:begin -->\nx\n<!-- polako:end -->\n" {
		t.Errorf("mergeClaudeMd with no existing content = %q", got)
	}
}

func TestMergeClaudeMdAppendsWhenMarkersAbsent(t *testing.T) {
	t.Parallel()
	got := mergeClaudeMd("# My repo\n\nSome notes.\n", "<!-- polako:begin -->\nx\n<!-- polako:end -->")
	want := "# My repo\n\nSome notes.\n\n<!-- polako:begin -->\nx\n<!-- polako:end -->\n"
	if got != want {
		t.Errorf("mergeClaudeMd appending = %q, want %q", got, want)
	}
}

func TestMergeClaudeMdReplacesInPlace(t *testing.T) {
	t.Parallel()
	existing := "# My repo\n\n<!-- polako:begin -->\nold\n<!-- polako:end -->\n\nMore notes below.\n"
	got := mergeClaudeMd(existing, "<!-- polako:begin -->\nnew\n<!-- polako:end -->")
	want := "# My repo\n\n<!-- polako:begin -->\nnew\n<!-- polako:end -->\n\nMore notes below.\n"
	if got != want {
		t.Errorf("mergeClaudeMd replacing = %q, want %q", got, want)
	}
}

// The acceptance criterion this issue names outright: a rerun on a CLAUDE.md
// that already carries the block leaves it byte-identical.
func TestWriteClaudeMdBlockIsByteIdenticalOnRerun(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("# My repo\n\nSome notes.\n"), 0o644); err != nil {
		t.Fatalf("writing CLAUDE.md: %v", err)
	}
	if err := writeClaudeMdBlock(dir); err != nil {
		t.Fatalf("writeClaudeMdBlock (first run): %v", err)
	}
	first, err := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	if err != nil {
		t.Fatalf("reading CLAUDE.md: %v", err)
	}
	if err := writeClaudeMdBlock(dir); err != nil {
		t.Fatalf("writeClaudeMdBlock (rerun): %v", err)
	}
	second, err := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	if err != nil {
		t.Fatalf("reading CLAUDE.md after rerun: %v", err)
	}
	if string(first) != string(second) {
		t.Errorf("CLAUDE.md changed on a rerun:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
	if claudeMdNeedsUpdate(dir) {
		t.Errorf("claudeMdNeedsUpdate is still true after writing the block")
	}
}

func TestSetupClaudeMdRow(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if r := setupClaudeMdRow(config{dir: dir}); r.status != setupMissing || r.required {
		t.Errorf("row with no CLAUDE.md = %+v, want missing and not required", r)
	}
	if err := writeClaudeMdBlock(dir); err != nil {
		t.Fatalf("writeClaudeMdBlock: %v", err)
	}
	if r := setupClaudeMdRow(config{dir: dir}); r.status != setupOK {
		t.Errorf("row after writing the block = %+v, want ok", r)
	}
}
