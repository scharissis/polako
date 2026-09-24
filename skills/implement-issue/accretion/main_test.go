package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMeasureSource(t *testing.T) {
	t.Parallel()
	src := "package x\n\n// one\nfunc f() {} // trailing, not counted\n/* two\nthree */\nvar y = 1"
	got := measureSource([]byte(src), slash)
	if got.lines != 7 || got.comments != 3 {
		t.Errorf("measureSource = %+v, want 7 lines, 3 comment lines", got)
	}
	for _, c := range []struct {
		ext, src string
		comments int
	}{
		{".py", "# a\nx = 1\n", 1},
		{".lua", "--[[ a\nb\n]]\nx = 1\n-- c\n", 4},
		{".sql", "/* a\nb */\nselect 1;\n-- c\n", 3},
		{".hs", "{- a\nb -}\nx = 1\n", 2},
		{".php", "# a\n// b\n$x = 1;\n", 2},
		{".vue", "<!-- a\nb -->\n<div/>\n", 2},
	} {
		st, _ := sourceStyle("f" + c.ext)
		if got := measureSource([]byte(c.src), st); got.comments != c.comments {
			t.Errorf("%s: %d comment lines, want %d", c.ext, got.comments, c.comments)
		}
	}
}

func TestVerdict(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		before, head, bound float64
		want                string
	}{
		{0, 50, 100, "ok"},
		{0, 150, 100, "act"},   // a new file born over the bound
		{90, 150, 100, "act"},  // pushed past it
		{150, 160, 100, "act"}, // already over, and grew
		{150, 140, 100, "over, not worse"},
	} {
		if got := verdict(c.before, c.head, c.bound); got != c.want {
			t.Errorf("verdict(%v, %v, %v) = %q, want %q", c.before, c.head, c.bound, got, c.want)
		}
	}
}

func TestMedianBoundedByCeiling(t *testing.T) {
	t.Parallel()
	if got := median([]float64{3, 1, 2, 10}); got != 2.5 {
		t.Errorf("median = %v, want 2.5", got)
	}
}

// A real git repo in a temp dir: the helper's whole job is reading one, and a
// fake can't say whether merge-base and diff are wired right.
func TestRunReportsChangedFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(name string, n int) {
		t.Helper()
		body := strings.Repeat("x = 1\n", n)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q", "-b", "main")
	write("a.py", 10)
	write("b.py", 10)
	write("c.py", 10)
	write("notes.txt", 500)
	git("add", ".")
	git("commit", "-q", "-m", "base")
	git("checkout", "-q", "-b", "issue-1")
	write("a.py", 40)
	write("d.py", 5)
	git("mv", "c.py", "e.py")
	git("add", ".")
	git("commit", "-q", "-m", "change")

	var out strings.Builder
	if err := run(&out, dir, "main"); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{
		"3 source files; median 10 lines", // the base's files, not the branch's
		"file length 10 lines (median)",
		"a.py", "10→40", "act",
		"d.py", "new→5",
		"e.py  10→10", // a rename is measured against its old self
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report lacks %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"notes.txt", "b.py"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("report names %s, which is unchanged or not source:\n%s", unwanted, got)
		}
	}
}

// SKILL.md states the ceilings in prose for the fallback path; the helper
// states them in code. Two spellings of one number drift unless held together.
func TestSkillStatesTheSameCeilings(t *testing.T) {
	t.Parallel()
	b, err := os.ReadFile("../SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	flat := strings.Join(strings.Fields(string(b)), " ")
	for _, want := range []string{"absolute ceiling of 1,000 lines", "absolute ceiling of 40%"} {
		if !strings.Contains(flat, want) {
			t.Errorf("SKILL.md no longer says %q — keep it matching fileCeiling/commentCeiling", want)
		}
	}
	if fileCeiling != 1000 || commentCeiling != 0.40 {
		t.Error("a ceiling changed here; change SKILL.md's step 2e to match")
	}
}
