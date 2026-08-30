package main

import (
	"bytes"
	"errors"
	"io"
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
	u := &ui{terminal: &term, file: &file}

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
	u := &ui{terminal: &term, file: &file}

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
	u := &ui{terminal: &term, file: fw}

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
	u := &ui{terminal: &term}

	path, err := u.openShiftLog(dir, "scharissis/polako", "a1b2c3d4")
	if err != nil {
		t.Fatalf("openShiftLog: %v", err)
	}
	// Production holds the handle for the process lifetime; here it has to be
	// closed before TempDir's cleanup, because Windows cannot delete an open
	// file.
	t.Cleanup(func() {
		if c, ok := u.file.(io.Closer); ok {
			c.Close()
		}
	})
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

// replayStream feeds a stream-json transcript through the two consumers
// dispatchClaude wires up — the event renderer and the report — and then
// emits the run's one finish milestone from the report the way dispatchClaude
// does once the scan loop ends. It is the closest a unit test gets to a real
// dispatch.
func replayStream(t *testing.T, lines ...string) *runReport {
	t.Helper()
	var el eventLog
	rep := &runReport{turns: -1}
	for _, l := range lines {
		ev, ok := parseEvent([]byte(l))
		if !ok {
			t.Fatalf("parseEvent rejected %s", l)
		}
		rep.observe(ev)
		el.event(ev)
	}
	if rep.hasResult {
		sev, line := finishLine(rep)
		narrate(sev, "%s", line)
	}
	return rep
}

// The terminal's audience is an operator glancing over: a healthy run is a
// pair of lines, and the conversation between them belongs to the shift log.
func TestQuietTerminalShowsARunAsMilestones(t *testing.T) {
	var term, file bytes.Buffer
	wireSinks(t, &ui{terminal: &term, file: &file})

	replayStream(t,
		`{"type":"system","subtype":"init","model":"claude-opus-5","session_id":"sess-1"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Gathering context on issue #48."}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"gh issue view 48"}}]}}`,
		`{"type":"result","subtype":"success","num_turns":74,"duration_ms":1141000,"total_cost_usd":4.12,"result":"Done."}`,
	)

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
	wireSinks(t, &ui{terminal: &term, file: &file})

	ev, ok := parseEvent([]byte(`{"type":"result","subtype":"success","is_error":true,"result":"Unknown skill: polako:implement-issue"}`))
	if !ok {
		t.Fatal("a result event should parse")
	}
	(&eventLog{}).event(ev)

	if !strings.Contains(term.String(), "Unknown skill") {
		t.Errorf("an error's result text is the diagnosis and belongs on the terminal\ngot:\n%s", term.String())
	}
}

// The CLI emits an init and a result per dequeued prompt, so a run whose
// review gate wakes the main loop over and over streams several of each. Only
// the run as a whole starts and finishes once; the rest are background tasks
// returning and belong in the shift log, or a healthy run reads as a crash
// loop that cost several times what it did (issue #224).
func TestBackgroundTaskWakeupsDoNotRepeatTheMilestones(t *testing.T) {
	var term, file bytes.Buffer
	wireSinks(t, &ui{terminal: &term, file: &file})

	replayStream(t,
		`{"type":"system","subtype":"init","model":"claude-opus-5","session_id":"sess-1"}`,
		`{"type":"result","subtype":"success","num_turns":16,"duration_ms":61000,"total_cost_usd":12.00,"result":"a"}`,
		`{"type":"system","subtype":"init","model":"claude-opus-5","session_id":"sess-1"}`,
		`{"type":"result","subtype":"success","num_turns":74,"duration_ms":1141000,"total_cost_usd":29.57,"result":"b"}`,
	)

	if got := strings.Count(term.String(), "session started"); got != 1 {
		t.Errorf("terminal shows %d \"session started\", want 1\ngot:\n%s", got, term.String())
	}
	if got := strings.Count(term.String(), "finished ("); got != 1 {
		t.Errorf("terminal shows %d finish lines, want 1\ngot:\n%s", got, term.String())
	}
	// The one finish line is the whole run's standing — turns and wall summed
	// across both result events (16+74, 61s+1141s), cost the last event's
	// cumulative figure (issue #227).
	if !strings.Contains(term.String(), "finished (ok) — 90 turns, 20m2s, $29.57") {
		t.Errorf("terminal finish line is not the run's standing\ngot:\n%s", term.String())
	}
	// The shift log still keeps every wakeup, worded for what it is.
	if !strings.Contains(file.String(), "resumed after a background task") {
		t.Errorf("shift log missing the repeated init\ngot:\n%s", file.String())
	}
}

// The single finish line reflects how the run actually ended, not an earlier
// result event: a transient error mid-stream must not bury a successful
// finish, and a run that ends in error must say so however healthy it looked
// on the way.
func TestFinishLineReflectsTheFinalState(t *testing.T) {
	t.Run("a later ok finish is not buried by an earlier error", func(t *testing.T) {
		var term, file bytes.Buffer
		wireSinks(t, &ui{terminal: &term, file: &file})
		replayStream(t,
			`{"type":"system","subtype":"init","model":"claude-opus-5"}`,
			`{"type":"result","subtype":"error_max_turns","num_turns":9,"is_error":true}`,
			`{"type":"result","subtype":"success","num_turns":40,"duration_ms":600000,"total_cost_usd":5.00}`,
		)
		if strings.Contains(term.String(), "ERROR") {
			t.Errorf("an earlier error result must not be the operator's last word\ngot:\n%s", term.String())
		}
		// is_error is the final event's (a clean success); turns are summed
		// across both result events, 9 + 40 (issue #227).
		if !strings.Contains(term.String(), "finished (ok) — 49 turns") {
			t.Errorf("terminal missing the run's real finish\ngot:\n%s", term.String())
		}
	})
	t.Run("a run that ends in error says so", func(t *testing.T) {
		var term, file bytes.Buffer
		wireSinks(t, &ui{terminal: &term, file: &file})
		replayStream(t,
			`{"type":"system","subtype":"init","model":"claude-opus-5"}`,
			`{"type":"result","subtype":"success","num_turns":10}`,
			`{"type":"result","subtype":"error_max_turns","num_turns":9,"is_error":true}`,
		)
		if !strings.Contains(term.String(), "finished (ERROR: error_max_turns)") {
			t.Errorf("a run ending in error must say so on the terminal\ngot:\n%s", term.String())
		}
	})
}

func TestVerboseMirrorsDetailToTheTerminal(t *testing.T) {
	var term, file bytes.Buffer
	u := &ui{terminal: &term, file: &file, verbose: true}

	detailWriter{u: u}.Write([]byte("[claude] → Bash: gh issue view 48\n"))

	if !strings.Contains(term.String(), "→ Bash") {
		t.Errorf("-verbose should mirror detail to the terminal\ngot:\n%s", term.String())
	}
	if !strings.Contains(file.String(), "→ Bash") {
		t.Errorf("-verbose must not divert detail away from the shift log\ngot:\n%s", file.String())
	}
}

// ttyStamped is the shape a TTY's dim, time-only gutter must carry: 8
// characters plus a trailing space, dimmed as a block.
var ttyStamped = regexp.MustCompile(`^\x1b\[2m\d{2}:\d{2}:\d{2} \x1b\[0m`)

func TestVerboseTTYStampsDetailTimeOnlyAndDim(t *testing.T) {
	var term, file bytes.Buffer
	u := &ui{terminal: &term, stamp: stampTTYDim, style: styler{on: true}, file: &file, verbose: true}

	detailWriter{u: u}.Write([]byte("[claude] → Bash: gh issue view 48\n"))

	if !ttyStamped.MatchString(term.String()) {
		t.Errorf("terminal detail line should carry a dim time-only stamp, got: %q", term.String())
	}
	if !stamped.MatchString(file.String()) {
		t.Errorf("shift log should keep the full stamp regardless of the terminal's stampKind, got: %q", file.String())
	}
}

func TestTTYStampIsTimeOnlyAndDim(t *testing.T) {
	var term, file bytes.Buffer
	u := &ui{terminal: &term, stamp: stampTTYDim, style: styler{on: true}, file: &file}

	milestoneWriter{u: u}.Write([]byte("PR #61 merged — cleaning up and advancing\n"))

	if !ttyStamped.MatchString(term.String()) {
		t.Errorf("terminal line should carry a dim time-only stamp, got: %q", term.String())
	}
	if !stamped.MatchString(file.String()) {
		t.Errorf("shift log should keep the full stamp on a TTY too, got: %q", file.String())
	}
}

// TestTTYStampUnstyledWithoutColour covers NO_COLOR, TERM=dumb and Windows:
// all three land here, at the styler being off. The stamp must stay
// time-only rather than either reverting to the full layout or disappearing.
func TestTTYStampUnstyledWithoutColour(t *testing.T) {
	var term bytes.Buffer
	u := &ui{terminal: &term, stamp: stampTTYDim, file: &bytes.Buffer{}}

	milestoneWriter{u: u}.Write([]byte("PR #61 merged\n"))

	got := term.String()
	if strings.Contains(got, "\x1b[") {
		t.Errorf("styler off must not emit ANSI codes, got: %q", got)
	}
	if !regexp.MustCompile(`^\d{2}:\d{2}:\d{2} `).MatchString(got) {
		t.Errorf("stamp should stay time-only even unstyled, got: %q", got)
	}
	if stamped.MatchString(got) {
		t.Errorf("a TTY must not fall back to the full stampLayout, got: %q", got)
	}
}

func TestPipedStampStaysFullLayout(t *testing.T) {
	var term, file bytes.Buffer
	u := &ui{terminal: &term, file: &file} // stampFull, the zero value

	milestoneWriter{u: u}.Write([]byte("PR #61 merged\n"))

	if !stamped.MatchString(term.String()) {
		t.Errorf("a non-tty terminal should keep the full stampLayout, got: %q", term.String())
	}
	if strings.Contains(term.String(), "\x1b[") {
		t.Errorf("a piped terminal should carry no ANSI codes, got: %q", term.String())
	}
}

func TestStampOffOmitsTheTerminalStampEntirely(t *testing.T) {
	var term bytes.Buffer
	u := &ui{terminal: &term, stamp: stampOff}

	milestoneWriter{u: u}.Write([]byte("ignoring 6 proposed issue(s) awaiting curation\n"))

	if strings.HasPrefix(term.String(), "\x1b") || regexp.MustCompile(`^\d`).MatchString(term.String()) {
		t.Errorf("stampOff should carry no stamp at all (status/stats piped output never had one), got: %q", term.String())
	}
}

func TestShiftLogFailureWarningStampsTTYTimeOnlyAndDim(t *testing.T) {
	var term bytes.Buffer
	fw := &failWriter{}
	u := &ui{terminal: &term, stamp: stampTTYDim, style: styler{on: true}, file: fw}

	milestoneWriter{u: u}.Write([]byte("=== issue #1 ===\n"))

	if !strings.Contains(term.String(), "shift log not written") {
		t.Errorf("the warning should still fire on a TTY, got: %q", term.String())
	}
	for _, line := range strings.Split(strings.TrimRight(term.String(), "\n"), "\n") {
		if !ttyStamped.MatchString(line) {
			t.Errorf("every TTY line, including the warning, should carry a dim time-only stamp, got: %q", line)
		}
	}
}

// A child's stderr arrives in arbitrary chunks; the shift log gets it back as
// whole attributed lines, blank ones dropped, the unterminated remainder
// flushed when the run ends.
func TestLineWriterSplitsPrefixesAndFlushes(t *testing.T) {
	var term, file bytes.Buffer
	wireSinks(t, &ui{terminal: &term, file: &file})

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

func TestStyleForGatesColour(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "x") // registers restoration of the real value…
	os.Unsetenv("NO_COLOR")   // …so the test can run with it truly unset
	if s := styleFor(false); s.on {
		t.Error("no colour off a TTY")
	}
	if s := styleFor(true); s.on != ansiCapable {
		t.Errorf("on a clean TTY, colour should follow the platform (ansiCapable=%v)", ansiCapable)
	}
	// Presence disables, even set to nothing — per no-color.org.
	t.Setenv("NO_COLOR", "")
	if s := styleFor(true); s.on {
		t.Error("NO_COLOR set (even empty) must disable colour")
	}
	os.Unsetenv("NO_COLOR")
	t.Setenv("TERM", "dumb")
	if s := styleFor(true); s.on {
		t.Error("TERM=dumb must disable colour")
	}
}

// TestRenderStylesBySeverityNotWording replaces the old wording-matching
// assertion: the exact same wording renders a different colour depending on
// the severity declared alongside it, and unrelated wording sharing a
// severity gets the same colour — proving render never looks at the text.
func TestRenderStylesBySeverityNotWording(t *testing.T) {
	s := styler{on: true}
	cases := []struct {
		sev  severity
		in   string
		want string
	}{
		{sevSection, "=== issue #14 ===\n", "\x1b[1m=== issue #14 ===\x1b[0m\n"},
		{sevSuccess, "finished (ok) — 74 turns, 19m1s, $4.12\n", "\x1b[32mfinished (ok) — 74 turns, 19m1s, $4.12\x1b[0m\n"},
		{sevError, "[claude] finished (ERROR: error_max_turns)\n", "\x1b[31m[claude] finished (ERROR: error_max_turns)\x1b[0m\n"},
		{sevWarning, "transient: listing open issues failed\n", "\x1b[33mtransient: listing open issues failed\x1b[0m\n"},
		{sevSettings, "-remote is on, but no claude CLI registers\n", "\x1b[2m-remote is on, but no claude CLI registers\x1b[0m\n"},
		{sevProgress, "PR #61 open — waiting for merge\n", "PR #61 open — waiting for merge\n"}, // default: plain
		// The same wording a semantic severity would colour, declared as
		// progress instead, must stay plain — proving the colour follows the
		// declared severity, not anything render can read off the text.
		{sevProgress, "finished (ok) — 1 turn, 1s, $0.00\n", "finished (ok) — 1 turn, 1s, $0.00\n"},
	}
	for _, tc := range cases {
		if got := s.render(tc.in, true, tc.sev); got != tc.want {
			t.Errorf("render(%q, sev=%v) = %q, want %q", tc.in, tc.sev, got, tc.want)
		}
	}
	if got := s.render("[claude] → Bash: ls\n", false, sevError); got != "\x1b[2m[claude] → Bash: ls\x1b[0m\n" {
		t.Errorf("detail renders dim regardless of severity, got %q", got)
	}
	off := styler{}
	if got := off.render("=== issue #14 ===\n", true, sevSection); got != "=== issue #14 ===\n" {
		t.Errorf("a styler that is off must pass lines through untouched, got %q", got)
	}
}

// newReport goes through styleFor, the one decision point for NO_COLOR,
// TERM=dumb and the Windows plain rule — so a report inherits every one of
// those without repeating the logic.
func TestNewReportGoesThroughStyleFor(t *testing.T) {
	t.Setenv("TERM", "dumb")
	if rpt := newReport(true); rpt.style.on {
		t.Error("TERM=dumb must disable a report's colour too")
	}
}

func TestReportRendersPlainAtTheZeroValue(t *testing.T) {
	var rpt report
	for _, s := range []string{"by issue", "not read", "failing (build)", "1 issue — parked"} {
		if got := rpt.bold(s); got != s {
			t.Errorf("bold(%q) = %q, want it unchanged at the zero value", s, got)
		}
		if got := rpt.dim(s); got != s {
			t.Errorf("dim(%q) = %q, want it unchanged at the zero value", s, got)
		}
		if got := rpt.cell(s); got != s {
			t.Errorf("cell(%q) = %q, want it unchanged at the zero value", s, got)
		}
	}
}

func TestReportBoldAndDimWrapWholeStrings(t *testing.T) {
	rpt := report{style: styler{on: true}}
	if got, want := rpt.bold("by issue"), "\x1b[1mby issue\x1b[0m"; got != want {
		t.Errorf("bold = %q, want %q", got, want)
	}
	if got, want := rpt.dim("issue"), "\x1b[2missue\x1b[0m"; got != want {
		t.Errorf("dim = %q, want %q", got, want)
	}
}

func TestReportCellHighlightsAttentionMarkersOnly(t *testing.T) {
	rpt := report{style: styler{on: true}}
	for in, want := range map[string]string{
		"failing (build, lint)":         "\x1b[33mfailing (build, lint)\x1b[0m",
		"changes requested":             "\x1b[33mchanges requested\x1b[0m",
		"1 issue — #9, labelled parked": "\x1b[33m1 issue — #9, labelled parked\x1b[0m",
		"not read":                      "\x1b[33mnot read\x1b[0m",
		"clear":                         "clear", // no marker: passed through plain
		"mergeable":                     "mergeable",
	} {
		if got := rpt.cell(in); got != want {
			t.Errorf("cell(%q) = %q, want %q", in, got, want)
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
