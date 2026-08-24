package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// fakeClaudeEnv makes the test binary impersonate the claude CLI: when it is
// set, TestMain streams canned events instead of running the suite. That lets
// execClaude be exercised end to end on every platform, with no shell scripts.
const fakeClaudeEnv = "BACKLOG_DRAIN_FAKE_CLAUDE"

func TestMain(m *testing.M) {
	if mode := os.Getenv(fakeClaudeEnv); mode != "" {
		os.Exit(fakeClaude(mode))
	}
	os.Exit(m.Run())
}

// fakeClaude stands in for `claude -p ... --output-format stream-json`.
func fakeClaude(mode string) int {
	emit := func(line string) {
		fmt.Fprintln(os.Stdout, line)
	}
	switch mode {
	case "stream":
		emit(`{"type":"system","subtype":"init","session_id":"sess-xyz","model":"claude-opus-5"}`)
		emit(`{"type":"assistant","session_id":"sess-xyz","message":{"content":[{"type":"text","text":"Reading the issue."}]}}`)
		emit(`{"type":"result","subtype":"success","session_id":"sess-xyz","duration_ms":1000,"num_turns":3,"total_cost_usd":0.5}`)
		return 0
	case "crash":
		emit(`{"type":"system","subtype":"init","session_id":"sess-crash","model":"claude-opus-5"}`)
		return 7
	case "hang":
		emit(`{"type":"system","subtype":"init","session_id":"sess-hang","model":"claude-opus-5"}`)
		time.Sleep(30 * time.Second) // the stall watchdog is expected to kill this
		return 0
	}
	fmt.Fprintf(os.Stderr, "unknown fake claude mode %q\n", mode)
	return 2
}

// captureLog redirects the standard logger for one test and returns the buffer.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return &buf
}

func TestLogEventRendersProgressLines(t *testing.T) {
	buf := captureLog(t)

	events := []string{
		`{"type":"system","subtype":"init","model":"claude-opus-5"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Gathering context on issue #48.\nStarting now."}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"gh issue view 48 --json body,comments"}}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{"file_path":"PLAN.md","content":"..."}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","content":"ignored"}]}}`,
		`not even json`,
		`{"type":"result","subtype":"success","duration_ms":1141000,"num_turns":74,"total_cost_usd":4.12,"is_error":false}`,
	}
	for _, e := range events {
		logEvent([]byte(e))
	}

	out := buf.String()
	for _, want := range []string{
		"session started (model claude-opus-5)",
		"Gathering context on issue #48. Starting now.",
		"→ Bash: gh issue view 48",
		"→ Write: PLAN.md",
		"finished (ok) — 74 turns, 19m1s, $4.12",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\ngot:\n%s", want, out)
		}
	}
	if strings.Contains(out, "ignored") || strings.Contains(out, "not even json") {
		t.Errorf("tool results / junk lines should not be rendered\ngot:\n%s", out)
	}
}

func TestLogEventMarksErrorResults(t *testing.T) {
	buf := captureLog(t)
	logEvent([]byte(`{"type":"result","subtype":"error_max_turns","num_turns":9,"is_error":true}`))
	if got := buf.String(); !strings.Contains(got, "finished (ERROR: error_max_turns)") {
		t.Errorf("error results should be flagged\ngot: %s", got)
	}
}

func TestExtractSessionID(t *testing.T) {
	cases := map[string]string{
		`{"type":"system","subtype":"init","session_id":"abc-123","model":"claude-opus-5"}`: "abc-123",
		`{"type":"assistant","session_id":"abc-123","message":{"content":[]}}`:              "abc-123",
		`{"type":"result","subtype":"success"}`:                                             "",
		`garbage`:                                                                           "",
	}
	for line, want := range cases {
		if got := extractSessionID([]byte(line)); got != want {
			t.Errorf("extractSessionID(%s) = %q, want %q", line, got, want)
		}
	}
}

func TestToolDetailPrefersTheMostUsefulField(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{`{"command":"go test ./..."}`, ": go test ./..."},
		{`{"file_path":"PLAN.md","content":"long body"}`, ": PLAN.md"},
		{`{"pattern":"func main"}`, ": func main"},
		{`{"content":"nothing addressable"}`, ""},
		{`not json`, ""},
		{`{"command":""}`, ""},
	}
	for _, c := range cases {
		if got := toolDetail([]byte(c.in)); got != c.want {
			t.Errorf("toolDetail(%s) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestClipFlattensAndTruncates(t *testing.T) {
	if got := clip("  one\n two\tthree  ", 40); got != "one two three" {
		t.Errorf("clip should collapse whitespace, got %q", got)
	}
	got := clip(strings.Repeat("x", 50), 10)
	if want := strings.Repeat("x", 10) + "…"; got != want {
		t.Errorf("clip = %q, want %q", got, want)
	}
}

func TestParseSkipIgnoresJunk(t *testing.T) {
	got := parseSkip(" 12, 34 ,,notanumber,56,")
	want := map[int]bool{12: true, 34: true, 56: true}
	if len(got) != len(want) {
		t.Fatalf("parseSkip = %v, want %v", got, want)
	}
	for n := range want {
		if !got[n] {
			t.Errorf("parseSkip missing %d (got %v)", n, got)
		}
	}
	if len(parseSkip("")) != 0 {
		t.Errorf("empty -skip should produce an empty set")
	}
}

func TestResolveToolsAppendsWithoutDuplicating(t *testing.T) {
	got := resolveTools("Read,Write,", " Bash(cargo:*) ,Read")
	if want := "Read,Write,Bash(cargo:*)"; got != want {
		t.Errorf("resolveTools = %q, want %q", got, want)
	}
	if got := resolveTools(defaultTools, ""); got != defaultTools {
		t.Errorf("an empty -add-tools must leave -tools untouched\ngot:  %q\nwant: %q", got, defaultTools)
	}
}

// The skill reads code, writes files, and invokes /code-review as a mandatory
// gate. Unattended runs die silently if any of those tools needs a prompt.
func TestDefaultToolsCoverWhatTheSkillNeeds(t *testing.T) {
	have := strings.Split(defaultTools, ",")
	for _, want := range []string{"Bash(git:*)", "Bash(gh:*)", "Read", "Write", "Edit", "Glob", "Grep", "Skill"} {
		if !slices.Contains(have, want) {
			t.Errorf("defaultTools is missing %q", want)
		}
	}
}

func TestPickLowestHonoursSkip(t *testing.T) {
	cases := []struct {
		name    string
		numbers []int
		skip    map[int]bool
		want    int
	}{
		{"unordered input", []int{9, 3, 17}, nil, 3},
		{"skips the head of the line", []int{9, 3, 17}, map[int]bool{3: true}, 9},
		{"everything skipped", []int{3}, map[int]bool{3: true}, 0},
		{"no issues", nil, nil, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := pickLowest(c.numbers, c.skip); got != c.want {
				t.Errorf("pickLowest(%v, %v) = %d, want %d", c.numbers, c.skip, got, c.want)
			}
		})
	}
}

func TestPickPRPrefersOpenThenMerged(t *testing.T) {
	closed := pullRequest{Number: 1, State: "CLOSED"}
	merged := pullRequest{Number: 2, State: "MERGED"}
	open := pullRequest{Number: 3, State: "OPEN"}

	cases := []struct {
		name string
		in   []pullRequest
		want int
	}{
		{"open wins", []pullRequest{closed, merged, open}, 3},
		{"merged beats closed", []pullRequest{closed, merged}, 2},
		{"falls back to the first", []pullRequest{closed}, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := pickPR(c.in)
			if got == nil || got.Number != c.want {
				t.Errorf("pickPR(%v) = %v, want PR #%d", c.in, got, c.want)
			}
		})
	}
	if pickPR(nil) != nil {
		t.Error("no PRs should yield nil, so the caller runs the skill")
	}
}

func TestBuildArgs(t *testing.T) {
	cfg := config{permissionMode: "acceptEdits", tools: "Read,Write"}

	fresh := buildArgs(cfg, "/implement-issue 4", "")
	want := []string{
		"-p", "/implement-issue 4",
		"--permission-mode", "acceptEdits",
		"--allowedTools", "Read,Write",
		"--output-format", "stream-json",
		"--verbose",
	}
	if !slices.Equal(fresh, want) {
		t.Errorf("buildArgs (fresh) = %v, want %v", fresh, want)
	}

	resumed := buildArgs(cfg, "continue", "sess-1")
	if !slices.Equal(resumed[:2], []string{"--resume", "sess-1"}) {
		t.Errorf("a resume must lead with --resume, got %v", resumed)
	}
	if slices.Contains(fresh, "--resume") {
		t.Error("a fresh run must not pass --resume")
	}
}

func TestBuildArgsAppliesAddTools(t *testing.T) {
	cfg := config{permissionMode: "plan", tools: "Read", addTools: "Bash(zig:*)"}
	args := buildArgs(cfg, "p", "")
	i := slices.Index(args, "--allowedTools")
	if i < 0 || args[i+1] != "Read,Bash(zig:*)" {
		t.Errorf("-add-tools should reach the invocation, got %v", args)
	}
}

func TestWatchTickScalesWithStall(t *testing.T) {
	cases := []struct {
		stall, want time.Duration
	}{
		{15 * time.Minute, 30 * time.Second}, // capped, so long runs stay quiet
		{40 * time.Second, 10 * time.Second},
		{time.Millisecond, 50 * time.Millisecond}, // floored, so tests can't spin
	}
	for _, c := range cases {
		if got := watchTick(c.stall); got != c.want {
			t.Errorf("watchTick(%s) = %s, want %s", c.stall, got, c.want)
		}
	}
}

func TestSleepReturnsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if err := sleep(ctx, time.Hour); err == nil {
		t.Fatal("sleep should surface the cancellation")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("sleep ignored the cancelled context for %s", elapsed)
	}
}

// --- end-to-end runs against the fake claude CLI ---

func fakeClaudeConfig(t *testing.T, mode string) config {
	t.Helper()
	t.Setenv(fakeClaudeEnv, mode) // inherited by the child process
	return config{
		dir:            t.TempDir(),
		claudeBin:      os.Args[0], // this test binary, re-entered via TestMain
		skill:          defaultSkill,
		permissionMode: "acceptEdits",
		tools:          "Read",
		stall:          10 * time.Second,
	}
}

func TestExecClaudeStreamsEventsAndCapturesSession(t *testing.T) {
	buf := captureLog(t)
	cfg := fakeClaudeConfig(t, "stream")

	id, err := execClaude(context.Background(), cfg, "/implement-issue 7", "")
	if err != nil {
		t.Fatalf("execClaude: %v", err)
	}
	if id != "sess-xyz" {
		t.Errorf("session id = %q, want %q", id, "sess-xyz")
	}
	for _, want := range []string{"session started", "Reading the issue.", "finished (ok)"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("stream not rendered: missing %q\ngot:\n%s", want, buf.String())
		}
	}
}

func TestExecClaudeReportsCrashesWithTheSessionToResume(t *testing.T) {
	captureLog(t)
	cfg := fakeClaudeConfig(t, "crash")

	id, err := execClaude(context.Background(), cfg, "/implement-issue 7", "")
	if err == nil {
		t.Fatal("a nonzero exit must surface as an error")
	}
	// The ID matters more than the error: it is what the retry resumes.
	if id != "sess-crash" {
		t.Errorf("session id = %q, want %q so the retry can resume it", id, "sess-crash")
	}
}

func TestExecClaudeKillsAStalledRun(t *testing.T) {
	captureLog(t)
	cfg := fakeClaudeConfig(t, "hang")
	cfg.stall = 300 * time.Millisecond

	start := time.Now()
	id, err := execClaude(context.Background(), cfg, "/implement-issue 7", "")
	if err == nil || !strings.Contains(err.Error(), "stalled") {
		t.Fatalf("a silent run should be killed as stalled, got err=%v", err)
	}
	if id != "sess-hang" {
		t.Errorf("session id = %q, want %q so the retry can resume it", id, "sess-hang")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("watchdog took %s to fire", elapsed)
	}
}

func TestExecClaudeStopsWhenTheContextIsCancelled(t *testing.T) {
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	cfg := fakeClaudeConfig(t, "hang")

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := execClaude(ctx, cfg, "/implement-issue 7", ""); err == nil {
		t.Fatal("cancelling the context should end the run with an error")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("Ctrl+C took %s to take effect", elapsed)
	}
}

func TestRunClaudeResumesRatherThanRestartingTheSkill(t *testing.T) {
	buf := captureLog(t)
	cfg := fakeClaudeConfig(t, "stream")

	if _, err := runClaude(context.Background(), cfg, 12, ""); err != nil {
		t.Fatalf("fresh run: %v", err)
	}
	if !strings.Contains(buf.String(), "-p /implement-issue 12") {
		t.Errorf("a fresh run should invoke the skill\ngot:\n%s", buf.String())
	}

	buf.Reset()
	if _, err := runClaude(context.Background(), cfg, 12, "sess-old"); err != nil {
		t.Fatalf("resumed run: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "--resume sess-old") {
		t.Errorf("a resume should pass --resume\ngot:\n%s", out)
	}
	if strings.Contains(out, "-p /implement-issue 12") {
		t.Errorf("a resume must not restart the skill from scratch\ngot:\n%s", out)
	}
}
