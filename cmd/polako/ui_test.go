package main

import (
	"bytes"
	"errors"
	"log"
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

// wireSinks points both narration loggers at one test ui for the duration of
// a test, so terminal-versus-log presentation can be asserted rather than the
// union captureLog collapses them into.
func wireSinks(t *testing.T, u *ui) {
	t.Helper()
	log.SetOutput(milestoneWriter{u: u})
	detail.SetOutput(detailWriter{u: u})
	t.Cleanup(func() {
		log.SetOutput(os.Stderr)
		detail.SetOutput(detailWriter{u: sinks})
	})
}

// The terminal's audience is an operator glancing over: a healthy run is a
// pair of lines, and the conversation between them belongs to the shift log.
func TestQuietTerminalShowsARunAsMilestones(t *testing.T) {
	var term, file bytes.Buffer
	wireSinks(t, &ui{terminal: &term, stamp: true, file: &file})

	for _, line := range []string{
		`{"type":"system","subtype":"init","model":"claude-opus-5","session_id":"sess-1"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Gathering context on issue #48."}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"gh issue view 48"}}]}}`,
		`{"type":"result","subtype":"success","num_turns":74,"duration_ms":1141000,"total_cost_usd":4.12,"result":"Done."}`,
	} {
		ev, ok := parseEvent([]byte(line))
		if !ok {
			t.Fatalf("parseEvent rejected %s", line)
		}
		logEvent(ev)
	}

	for _, want := range []string{"session started (model claude-opus-5", "finished (ok) — 74 turns"} {
		if !strings.Contains(term.String(), want) {
			t.Errorf("terminal missing the %q milestone\ngot:\n%s", want, term.String())
		}
	}
	for _, quiet := range []string{"Gathering context", "→ Bash", "Done."} {
		if strings.Contains(term.String(), quiet) {
			t.Errorf("the terminal should not carry %q — that is the shift log's job\ngot:\n%s", quiet, term.String())
		}
	}
	for _, want := range []string{"session started", "Gathering context", "→ Bash: gh issue view 48", "Done.", "finished (ok)"} {
		if !strings.Contains(file.String(), want) {
			t.Errorf("shift log missing %q\ngot:\n%s", want, file.String())
		}
	}
}

// An error's result text is the whole diagnosis for a run the CLI answered
// itself, so unlike a healthy run's it stays on the terminal.
func TestQuietTerminalStillShowsAnErrorsResultText(t *testing.T) {
	var term, file bytes.Buffer
	wireSinks(t, &ui{terminal: &term, stamp: true, file: &file})

	ev, ok := parseEvent([]byte(`{"type":"result","subtype":"success","is_error":true,"result":"Unknown skill: polako:implement-issue"}`))
	if !ok {
		t.Fatal("a result event should parse")
	}
	logEvent(ev)

	if !strings.Contains(term.String(), "Unknown skill") {
		t.Errorf("an error's result text is the diagnosis and belongs on the terminal\ngot:\n%s", term.String())
	}
}

func TestVerboseMirrorsDetailToTheTerminal(t *testing.T) {
	var term, file bytes.Buffer
	u := &ui{terminal: &term, stamp: true, file: &file, verbose: true}

	detailWriter{u: u}.Write([]byte("[claude] → Bash: gh issue view 48\n"))

	if !strings.Contains(term.String(), "→ Bash") {
		t.Errorf("-verbose should mirror detail to the terminal\ngot:\n%s", term.String())
	}
	if !strings.Contains(file.String(), "→ Bash") {
		t.Errorf("-verbose must not divert detail away from the shift log\ngot:\n%s", file.String())
	}
}

// A child's stderr arrives in arbitrary chunks; the shift log gets it back as
// whole attributed lines, blank ones dropped, the unterminated remainder
// flushed when the run ends.
func TestLineWriterSplitsPrefixesAndFlushes(t *testing.T) {
	var term, file bytes.Buffer
	wireSinks(t, &ui{terminal: &term, stamp: true, file: &file})

	w := &lineWriter{prefix: "[claude stderr]"}
	w.Write([]byte("first li"))
	w.Write([]byte("ne\n\nsecond line\ntrail"))
	w.Write([]byte("ing"))
	w.flush()

	got := file.String()
	for _, want := range []string{
		"[claude stderr] first line\n",
		"[claude stderr] second line\n",
		"[claude stderr] trailing\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("shift log missing %q\ngot:\n%s", want, got)
		}
	}
	if strings.Contains(got, "[claude stderr] \n") {
		t.Errorf("a blank stderr line is not worth a timestamp and a prefix\ngot:\n%s", got)
	}
	if term.Len() != 0 {
		t.Errorf("stderr chatter is detail and should stay off the quiet terminal\ngot:\n%s", term.String())
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
