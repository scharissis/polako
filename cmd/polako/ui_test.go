package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// stamped is the shape every shift-log line (and, until TTY detection says
// otherwise, every terminal line) must carry — the exact prefix the default
// logger's Ldate|Ltime flags used to produce.
var stamped = regexp.MustCompile(`^\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2} `)

func TestMilestonesReachTerminalAndShiftLog(t *testing.T) {
	var term, file bytes.Buffer
	u := &ui{terminal: &term, stamp: true, file: &file}

	milestoneWriter{u: u}.Write([]byte("PR #61 merged — cleaning up and advancing\n"))

	for name, buf := range map[string]*bytes.Buffer{"terminal": &term, "shift log": &file} {
		if !strings.Contains(buf.String(), "PR #61 merged") {
			t.Errorf("milestone missing from the %s: %q", name, buf.String())
		}
		if !stamped.MatchString(buf.String()) {
			t.Errorf("%s line not timestamped: %q", name, buf.String())
		}
	}
}

func TestDetailReachesTheShiftLogAlone(t *testing.T) {
	var term, file bytes.Buffer
	u := &ui{terminal: &term, stamp: true, file: &file}

	detailWriter{u: u}.Write([]byte("[claude] → Bash: gh issue view 48\n"))

	if term.Len() != 0 {
		t.Errorf("detail reached the terminal: %q", term.String())
	}
	if !strings.Contains(file.String(), "→ Bash") || !stamped.MatchString(file.String()) {
		t.Errorf("shift log should hold the detail line, timestamped: %q", file.String())
	}
}

// failWriter refuses every write, like a full disk.
type failWriter struct{ n int }

func (f *failWriter) Write(p []byte) (int, error) {
	f.n++
	return 0, errors.New("disk full")
}

func TestShiftLogFailureWarnsOnceAndNeverStopsNarration(t *testing.T) {
	var term bytes.Buffer
	fw := &failWriter{}
	u := &ui{terminal: &term, stamp: true, file: fw}

	milestoneWriter{u: u}.Write([]byte("=== issue #1 ===\n"))
	milestoneWriter{u: u}.Write([]byte("=== issue #2 ===\n"))

	if got := strings.Count(term.String(), "shift log not written"); got != 1 {
		t.Errorf("warned %d times, want exactly once: %q", got, term.String())
	}
	if !strings.Contains(term.String(), "-log off") {
		t.Errorf("the warning should say how to silence it: %q", term.String())
	}
	if !strings.Contains(term.String(), "=== issue #2 ===") {
		t.Errorf("narration should continue past a dead log: %q", term.String())
	}
	if fw.n < 2 {
		t.Errorf("writes stopped being attempted after the warning (%d), so a disk that recovers stays lost", fw.n)
	}
}

func TestOpenShiftLogNamesAndProtectsTheFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")
	var term bytes.Buffer
	u := &ui{terminal: &term, stamp: true}

	path, err := u.openShiftLog(dir, "scharissis/polako", "a1b2c3d4")
	if err != nil {
		t.Fatalf("openShiftLog: %v", err)
	}
	if want := filepath.Join(dir, "scharissis--polako--a1b2c3d4.log"); path != want {
		t.Errorf("path = %q, want %q", path, want)
	}

	milestoneWriter{u: u}.Write([]byte("backlog cleared\n"))
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the shift log back: %v", err)
	}
	if !strings.Contains(string(got), "backlog cleared") {
		t.Errorf("shift log = %q, want the milestone in it", got)
	}

	if runtime.GOOS == "windows" {
		return // unix permission bits are not how Windows decides this
	}
	for _, p := range []string{dir, path} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatalf("stat %s: %v", p, err)
		}
		if mode := fi.Mode().Perm(); mode&0o077 != 0 {
			t.Errorf("%s is mode %04o — the log holds transcript text; group and other must have no access", p, mode)
		}
	}
}

func TestResolveLogDirHonoursOff(t *testing.T) {
	if got := resolveLogDir("off"); got != "" {
		t.Errorf(`resolveLogDir("off") = %q, want ""`, got)
	}
	if got := resolveLogDir(" OFF "); got != "" {
		t.Errorf(`resolveLogDir(" OFF ") = %q, want "" — the recorder's spelling rules apply here too`, got)
	}
	dir := t.TempDir()
	if got := resolveLogDir(dir); got != dir {
		t.Errorf("resolveLogDir(%q) = %q, want the directory back", dir, got)
	}
}
