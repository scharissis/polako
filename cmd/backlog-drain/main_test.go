package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
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

// fakePluginEnv is the version `claude plugin list --json` should report for
// the installed backlog-drain plugin. Unset means the subcommand fails, which
// is what a CLI too old to have it does.
const fakePluginEnv = "BACKLOG_DRAIN_FAKE_PLUGIN_VERSION"

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
	// `claude plugin list --json` is a different call on the same binary, so it
	// dispatches on argv rather than on mode: any run can make it.
	if len(os.Args) > 2 && os.Args[1] == "plugin" && os.Args[2] == "list" {
		v := os.Getenv(fakePluginEnv)
		if v == "" {
			return 1 // no such subcommand on this CLI
		}
		// Two entries, so the match is proved to be by name and not by luck.
		emit(`[{"id":"some-other-plugin@elsewhere","version":"9.9.9"},` +
			`{"id":"backlog-drain@scharissis","version":"` + v + `","scope":"user","enabled":true}]`)
		return 0
	}
	switch mode {
	case "stream":
		// The init event carries the session's command inventory (2.1.85+);
		// both spellings of the skill are listed so the healthy path proves
		// the missing-skill tripwire stays quiet when the command exists.
		emit(`{"type":"system","subtype":"init","session_id":"sess-xyz","model":"claude-opus-5",` +
			`"slash_commands":["compact","context","cost","backlog-drain:implement-issue","implement-issue"]}`)
		emit(`{"type":"assistant","session_id":"sess-xyz","message":{"content":[{"type":"text","text":"Reading the issue."}],` +
			`"usage":{"input_tokens":10,"output_tokens":20,"cache_read_input_tokens":30,"cache_creation_input_tokens":40}}}`)
		emit(`{"type":"assistant","session_id":"sess-xyz","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"go test ./..."}}],` +
			`"usage":{"input_tokens":1,"output_tokens":2,"cache_read_input_tokens":3,"cache_creation_input_tokens":4}}}`)
		emit(`{"type":"result","subtype":"success","session_id":"sess-xyz","duration_ms":1000,"duration_api_ms":800,` +
			`"num_turns":3,"total_cost_usd":0.5,"result":"Opened a PR for issue 7.",` +
			`"usage":{"input_tokens":100,"output_tokens":200,"cache_read_input_tokens":300,"cache_creation_input_tokens":400},` +
			`"modelUsage":{"claude-opus-5":{"inputTokens":100,"outputTokens":190,"cacheReadInputTokens":300,` +
			`"cacheCreationInputTokens":400,"costUSD":0.45},` +
			`"claude-haiku-4-5":{"inputTokens":0,"outputTokens":10,"costUSD":0.05}}}`)
		return 0
	case "oldcli":
		// A CLI old enough to report a result with no per-model breakdown.
		emit(`{"type":"system","subtype":"init","session_id":"sess-old","model":"claude-opus-5"}`)
		emit(`{"type":"result","subtype":"success","session_id":"sess-old","duration_ms":10,"num_turns":2,` +
			`"total_cost_usd":0.25,"usage":{"input_tokens":5,"output_tokens":6}}`)
		return 0
	case "partial":
		// Died mid-stream: real tokens burned, no result event to report them.
		emit(`{"type":"system","subtype":"init","session_id":"sess-partial","model":"claude-opus-5"}`)
		emit(`{"type":"assistant","session_id":"sess-partial","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"main.go"}}],` +
			`"usage":{"input_tokens":7,"output_tokens":8,"cache_read_input_tokens":9,"cache_creation_input_tokens":11}}}`)
		emit(`{"type":"assistant","session_id":"sess-partial","message":{"content":[{"type":"text","text":"Still working."}],` +
			`"usage":{"input_tokens":1,"output_tokens":1,"cache_read_input_tokens":1,"cache_creation_input_tokens":1}}}`)
		return 9
	case "crash":
		emit(`{"type":"system","subtype":"init","session_id":"sess-crash","model":"claude-opus-5"}`)
		return 7
	case "oddshape":
		// content as a plain string rather than an array: valid JSON the event
		// schema cannot hold, and here the only line carrying the session.
		emit(`{"type":"user","session_id":"sess-odd","message":{"content":"a string, not an array"}}`)
		return 3
	case "noturns":
		// How CLIs before 2.1.85 reported an unresolvable slash command: a
		// clean exit at zero turns, and no command inventory on the init
		// event. Newer CLIs look like "unknownskill" instead, but this
		// fallback keeps catching installations still on an old CLI.
		emit(`{"type":"system","subtype":"init","session_id":"sess-none","model":"claude-opus-5"}`)
		emit(`{"type":"assistant","session_id":"sess-none","message":{"content":[{"type":"text","text":"Unknown command: /nope"}]}}`)
		emit(`{"type":"result","subtype":"success","session_id":"sess-none","duration_ms":100,"num_turns":0,"total_cost_usd":0}`)
		return 0
	case "unknownskill":
		// Claude Code 2.1.85 on an unknown slash command: a success result,
		// nonzero num_turns, no error flag. Only the init event's command
		// inventory and the result text give the misconfiguration away.
		emit(`{"type":"system","subtype":"init","session_id":"sess-unk","model":"claude-opus-5",` +
			`"slash_commands":["compact","context","cost","init","todos"]}`)
		emit(`{"type":"result","subtype":"success","is_error":false,"session_id":"sess-unk","duration_ms":11,` +
			`"num_turns":2,"total_cost_usd":0,"result":"Unknown skill: backlog-drain:implement-issue"}`)
		// Both lines are already in the pipe; linger so the supervisor's
		// deliberate kill — not this process's own exit — ends the run, and
		// tests observe the killed path deterministically instead of racing.
		time.Sleep(500 * time.Millisecond)
		return 0
	case "authfail":
		// Claude Code on a rejected OAuth token: one turn, no cost, and a
		// result flagged is_error whose subtype is nonetheless "success".
		// The 401 body is the CLI's, quoted exactly as it was logged.
		emit(`{"type":"system","subtype":"init","session_id":"sess-auth","model":"claude-sonnet-4-6"}`)
		emit(`{"type":"result","subtype":"success","is_error":true,"session_id":"sess-auth",` +
			`"duration_ms":183000,"num_turns":1,"total_cost_usd":0,` +
			`"result":"Failed to authenticate. API Error: 401 {\"type\":\"error\",\"error\":` +
			`{\"type\":\"authentication_error\",\"message\":\"OAuth access token is invalid.\"},` +
			`\"request_id\":null}"}`)
		return 1
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
		`{"type":"result","subtype":"success","duration_ms":1141000,"num_turns":74,"total_cost_usd":4.12,"is_error":false,` +
			`"result":"Unknown skill: backlog-drain:implement-issue"}`,
	}
	for _, e := range events {
		if ev, ok := parseEvent([]byte(e)); ok {
			logEvent(ev)
		}
	}

	out := buf.String()
	for _, want := range []string{
		"session started (model claude-opus-5)",
		"Gathering context on issue #48. Starting now.",
		"→ Bash: gh issue view 48",
		"→ Write: PLAN.md",
		// The result text must reach the log: for a run the CLI answered
		// itself, it is the only place the diagnosis ever appears.
		"Unknown skill: backlog-drain:implement-issue",
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
	ev, ok := parseEvent([]byte(`{"type":"result","subtype":"error_max_turns","num_turns":9,"is_error":true}`))
	if !ok {
		t.Fatal("a result event should parse")
	}
	logEvent(ev)
	got := buf.String()
	if !strings.Contains(got, "finished (ERROR: error_max_turns)") {
		t.Errorf("error results should be flagged\ngot: %s", got)
	}
	// This event carries no result text — the shape old CLIs still send on
	// every run — so nothing but the finished line may render.
	if strings.Contains(got, "[claude] \n") {
		t.Errorf("an absent result text must not render an empty line\ngot: %s", got)
	}
}

// One parse per line feeds both the progress log and the run report, so the
// session ID every consumer depends on now comes out of parseEvent.
func TestParseEventReadsTheSessionAndRejectsJunk(t *testing.T) {
	cases := map[string]string{
		`{"type":"system","subtype":"init","session_id":"abc-123","model":"claude-opus-5"}`: "abc-123",
		`{"type":"assistant","session_id":"abc-123","message":{"content":[]}}`:              "abc-123",
		`{"type":"result","subtype":"success"}`:                                             "",
	}
	for line, want := range cases {
		ev, ok := parseEvent([]byte(line))
		if !ok {
			t.Errorf("parseEvent(%s) rejected a valid event", line)
			continue
		}
		if ev.SessionID != want {
			t.Errorf("parseEvent(%s).SessionID = %q, want %q", line, ev.SessionID, want)
		}
	}
	for _, junk := range []string{`garbage`, `["not", "an", "event"]`, `"a string"`, ``} {
		if _, ok := parseEvent([]byte(junk)); ok {
			t.Errorf("parseEvent(%q) should reject a line that is not an event", junk)
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
	for _, want := range []string{
		"Bash(git:*)", "Bash(gh issue view:*)", "Bash(gh issue comment:*)", "Bash(gh pr create:*)",
		"Read", "Write", "Edit", "Glob", "Grep", "Skill",
	} {
		if !slices.Contains(have, want) {
			t.Errorf("defaultTools is missing %q", want)
		}
	}
}

// The gh grant is per subcommand on purpose, and the positive test above still
// passes with a broader one present — so the narrowing needs its own check.
// Each of these would hand attacker-supplied issue text something the skill
// never needs and the design forbids.
func TestDefaultToolsDoNotGrantGhWholesale(t *testing.T) {
	have := strings.Split(defaultTools, ",")
	for entry, why := range map[string]string{
		"Bash(gh:*)":       "gh api, gh secret set and gh repo delete",
		"Bash(gh pr:*)":    "gh pr merge — nothing may merge itself",
		"Bash(gh issue:*)": "gh issue edit --add-label — that reopens a -label-gated queue",
	} {
		if slices.Contains(have, entry) {
			t.Errorf("defaultTools grants %s, which permits %s; grant the subcommands the "+
				"skill needs and leave -add-tools as the escape hatch for projects that need more",
				entry, why)
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

// -model is what varies the thing being measured; a run that silently ignored
// it would make every model comparison a comparison of the same model.
func TestBuildArgsPassesTheRequestedModel(t *testing.T) {
	cfg := config{permissionMode: "acceptEdits", tools: "Read", model: "claude-haiku-4-5"}
	args := buildArgs(cfg, "p", "")
	i := slices.Index(args, "--model")
	if i < 0 || args[i+1] != "claude-haiku-4-5" {
		t.Errorf("-model should reach the invocation, got %v", args)
	}
	if slices.Contains(buildArgs(config{tools: "Read"}, "p", ""), "--model") {
		t.Error("an unset -model must leave the CLI's own default alone")
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

	rep, err := execClaude(context.Background(), cfg, "/implement-issue 7", "", "implement-issue")
	if err != nil {
		t.Fatalf("execClaude: %v", err)
	}
	if rep.sessionID != "sess-xyz" {
		t.Errorf("session id = %q, want %q", rep.sessionID, "sess-xyz")
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

	rep, err := execClaude(context.Background(), cfg, "/implement-issue 7", "", "implement-issue")
	if err == nil {
		t.Fatal("a nonzero exit must surface as an error")
	}
	// The report matters more than the error: it is what the retry resumes
	// from, and what the run record is built out of.
	if rep.sessionID != "sess-crash" {
		t.Errorf("session id = %q, want %q so the retry can resume it", rep.sessionID, "sess-crash")
	}
	if rep.exitCode != 7 {
		t.Errorf("exit code = %d, want 7", rep.exitCode)
	}
	if got := rep.status(); got != "crash" {
		t.Errorf("status = %q, want %q", got, "crash")
	}
}

func TestExecClaudeKillsAStalledRun(t *testing.T) {
	captureLog(t)
	cfg := fakeClaudeConfig(t, "hang")
	cfg.stall = 300 * time.Millisecond

	start := time.Now()
	rep, err := execClaude(context.Background(), cfg, "/implement-issue 7", "", "implement-issue")
	if err == nil || !strings.Contains(err.Error(), "stalled") {
		t.Fatalf("a silent run should be killed as stalled, got err=%v", err)
	}
	if rep.sessionID != "sess-hang" {
		t.Errorf("session id = %q, want %q so the retry can resume it", rep.sessionID, "sess-hang")
	}
	// A killed process also exits nonzero; "stalled" is the more specific
	// answer, and the one that explains the retry.
	if got := rep.status(); got != "stalled" {
		t.Errorf("status = %q, want %q", got, "stalled")
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
	if _, err := execClaude(ctx, cfg, "/implement-issue 7", "", "implement-issue"); err == nil {
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
	want := fmt.Sprintf("-p /%s 12", defaultSkill)
	if !strings.Contains(buf.String(), want) {
		t.Errorf("a fresh run should invoke %q\ngot:\n%s", want, buf.String())
	}

	buf.Reset()
	if _, err := runClaude(context.Background(), cfg, 12, "sess-old"); err != nil {
		t.Fatalf("resumed run: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "--resume sess-old") {
		t.Errorf("a resume should pass --resume\ngot:\n%s", out)
	}
	if strings.Contains(out, want) {
		t.Errorf("a resume must not restart the skill from scratch\ngot:\n%s", out)
	}
}

// The failure both of these guard against: -skill naming a slash command the
// installation does not have. CLIs before 2.1.85 answered with a clean exit
// at zero turns, and without this fallback the supervisor reports only "no PR
// and no questions".
func TestExecClaudeFlagsARunThatTookNoTurns(t *testing.T) {
	captureLog(t)
	cfg := fakeClaudeConfig(t, "noturns")

	_, err := execClaude(context.Background(), cfg, "/nope 1", "", "nope")
	if !errors.Is(err, errNoWork) {
		t.Fatalf("a clean exit at 0 turns should report errNoWork, got %v", err)
	}
}

// From 2.1.85 the CLI answers an unknown slash command with a success result
// — two turns on paper, nothing done — so the zero-turn fallback never fires.
// The init event's command inventory is the tell that still does.
func TestExecClaudeFlagsASkillTheSessionDoesNotList(t *testing.T) {
	captureLog(t)
	cfg := fakeClaudeConfig(t, "unknownskill")

	rep, err := execClaude(context.Background(), cfg, "/"+defaultSkill+" 1", "", defaultSkill)
	if !errors.Is(err, errNoWork) {
		t.Fatalf("a session that does not list the skill should report errNoWork, got %v", err)
	}
	if !strings.Contains(err.Error(), defaultSkill) {
		t.Errorf("the error should name the missing command, got %v", err)
	}
	if got := rep.status(); got != "no-skill" {
		t.Errorf("status = %q, want %q — the deliberate stop must not be recorded as a crash", got, "no-skill")
	}
}

// Resume and conflict-remediation prompts are plain English — their callers
// pass an empty invokes — and must run even in a session that does not list
// the -skill, or a misconfigured -skill would break remediation of a PR that
// already exists.
func TestExecClaudeLeavesPlainPromptsAloneWhenTheSkillIsMissing(t *testing.T) {
	captureLog(t)
	cfg := fakeClaudeConfig(t, "unknownskill")

	if _, err := execClaude(context.Background(), cfg,
		"PR #3 has merge conflicts with the remote default branch.", "", ""); err != nil {
		t.Fatalf("a plain prompt must not trip the missing-skill check, got %v", err)
	}
}

// The near-match hint is what turns "no such command" into a fix, and the
// bare-vs-namespaced confusion is symmetrical, so both directions must hit.
func TestNearMatchesBridgesPluginNamespacing(t *testing.T) {
	inv := []string{"compact", "backlog-drain:implement-issue"}
	if got := nearMatches(inv, "implement-issue"); !slices.Equal(got, []string{"/backlog-drain:implement-issue"}) {
		t.Errorf("a bare -skill should surface the namespaced spelling, got %v", got)
	}
	if got := nearMatches([]string{"compact", "implement-issue"}, "backlog-drain:implement-issue"); !slices.Equal(got, []string{"/implement-issue"}) {
		t.Errorf("a namespaced -skill should surface the bare spelling, got %v", got)
	}
	if got := nearMatches(inv, "something-else"); got != nil {
		t.Errorf("no relative should mean no hint, got %v", got)
	}
}

// A wrong "missing" verdict kills a healthy run, so the check may only fire
// on positive evidence: an inventory that is present and lacks the command.
func TestLacksCommandNeedsPositiveEvidence(t *testing.T) {
	list := []string{"compact", "cost", "backlog-drain:implement-issue"}
	if lacksCommand(list, "backlog-drain:implement-issue") {
		t.Error("a listed command must be found")
	}
	if !lacksCommand(list, "implement-issue") {
		t.Error("a command absent from a populated inventory is missing")
	}
	if lacksCommand(nil, "implement-issue") {
		t.Error("an absent inventory (older CLIs) must not read as missing")
	}
	if lacksCommand(list, "") {
		t.Error("a plain prompt invokes nothing, so nothing can be missing")
	}
	if lacksCommand([]string{"/compact", "/implement-issue"}, "implement-issue") {
		t.Error("a leading slash on inventory entries must not hide the command")
	}
}

// --- run-data capture, end to end against the fake CLI ---

func TestExecClaudeCapturesTheResultUsage(t *testing.T) {
	captureLog(t)
	cfg := fakeClaudeConfig(t, "stream")

	rep, err := execClaude(context.Background(), cfg, "/implement-issue 7", "", "implement-issue")
	if err != nil {
		t.Fatalf("execClaude: %v", err)
	}
	if rep.model != "claude-opus-5" {
		t.Errorf("model = %q, want the one the init event reported", rep.model)
	}
	if rep.turns != 3 || rep.wallMS != 1000 || rep.apiMS != 800 || rep.costUSD != 0.5 {
		t.Errorf("result fields = %d turns, %dms wall, %dms api, $%v", rep.turns, rep.wallMS, rep.apiMS, rep.costUSD)
	}
	// The tally is the stream's, not the result's: the result reports no tool count.
	if rep.toolUses != 1 {
		t.Errorf("tool_uses = %d, want 1", rep.toolUses)
	}
	want := tokenCounts{In: 100, Out: 200, CacheRead: 300, CacheWrite: 400}
	if rep.usage != want {
		t.Errorf("usage = %+v, want the result's block %+v", rep.usage, want)
	}
	if len(rep.modelUsage) != 2 {
		t.Fatalf("model_usage = %+v, want both models", rep.modelUsage)
	}
	// modelUsage is camelCase where the block above is snake_case.
	if opus := rep.modelUsage["claude-opus-5"]; opus.In != 100 || opus.CostUSD != 0.45 {
		t.Errorf("per-model entry = %+v, want the camelCase keys read", opus)
	}
}

// Losing the session ID means a retry restarts the skill from scratch instead
// of resuming, throwing away the crashed run's research context — so one
// unparseable line must not cost it.
func TestExecClaudeSalvagesTheSessionFromAnUnparseableLine(t *testing.T) {
	captureLog(t)
	cfg := fakeClaudeConfig(t, "oddshape")

	rep, err := execClaude(context.Background(), cfg, "/implement-issue 7", "", "implement-issue")
	if err == nil {
		t.Fatal("a nonzero exit must surface as an error")
	}
	if rep.sessionID != "sess-odd" {
		t.Errorf("session id = %q, want %q — the retry has nothing to resume without it",
			rep.sessionID, "sess-odd")
	}
}

// Older CLI versions report a result with no per-model breakdown at all.
func TestExecClaudeToleratesAResultWithoutModelUsage(t *testing.T) {
	captureLog(t)
	cfg := fakeClaudeConfig(t, "oldcli")

	rep, err := execClaude(context.Background(), cfg, "/implement-issue 7", "", "implement-issue")
	if err != nil {
		t.Fatalf("execClaude: %v", err)
	}
	if rep.modelUsage != nil {
		t.Errorf("model_usage = %+v, want it absent rather than invented", rep.modelUsage)
	}
	if rep.usage.In != 5 || rep.costUSD != 0.25 || rep.status() != "ok" {
		t.Errorf("the rest of the result should still be read, got %+v", rep)
	}
	if rec := newRunRecord(cfg, runContext{started: time.Now(), ended: time.Now()}, rep); rec.UsageSource != usageResult {
		t.Errorf("usage_source = %q, want %q — the run did report one", rec.UsageSource, usageResult)
	}
}

// The bias this guards against: a run that crashes mid-flight burned real
// tokens. Recording zero for it would make whatever configuration crashes most
// look like the cheapest one.
func TestExecClaudeKeepsTheUsageObservedBeforeACrash(t *testing.T) {
	captureLog(t)
	cfg := fakeClaudeConfig(t, "partial")

	rep, err := execClaude(context.Background(), cfg, "/implement-issue 7", "", "implement-issue")
	if err == nil {
		t.Fatal("a nonzero exit must surface as an error")
	}
	if rep.hasResult {
		t.Fatal("a crashed run has no result event to report")
	}
	want := tokenCounts{In: 8, Out: 9, CacheRead: 10, CacheWrite: 12}
	if rep.observed != want {
		t.Errorf("observed usage = %+v, want the streamed tally %+v", rep.observed, want)
	}
	if rep.observedTurns != 2 || rep.toolUses != 1 {
		t.Errorf("observed %d turns and %d tool uses, want 2 and 1", rep.observedTurns, rep.toolUses)
	}

	rec := newRunRecord(cfg, runContext{issue: 7, started: time.Now(), ended: time.Now()}, rep)
	if rec.UsageSource != usageObserved || rec.Tokens != want {
		t.Errorf("record = %q / %+v, want the observed tally, flagged as observed", rec.UsageSource, rec.Tokens)
	}
	if rec.Status != "crash" || rec.ExitCode != 9 {
		t.Errorf("record status = %q, exit %d, want crash / 9", rec.Status, rec.ExitCode)
	}
}

// Records hold numbers, identifiers and operator-chosen labels only. Issue and
// PR text is sensitive and, on a repo open to outside issues, attacker
// controlled — so nothing the model said may reach a record file.
func TestRecordsNeverCarryWhatTheRunSaid(t *testing.T) {
	captureLog(t)
	cfg := fakeClaudeConfig(t, "stream")
	dir := t.TempDir()
	cfg.repo, cfg.rec = "owner/repo", newRecorder(dir)

	rep, err := execClaude(context.Background(), cfg, "/implement-issue 7", "", "implement-issue")
	if err != nil {
		t.Fatalf("execClaude: %v", err)
	}
	cfg.rec.recordRun(cfg, runContext{issue: 7, reason: reasonImplement,
		outcome: outcomeOpenedPR, started: time.Now(), ended: time.Now()}, rep)

	written := strings.Join(readRecords(t, dir, cfg.repo), "\n")
	// One representative per kind of text the stream carried: assistant text,
	// tool input, the command inventory, the final result text. The record
	// counts tool uses, but must never say what any of them were.
	for _, leaked := range []string{"Reading the issue", "go test ./...", "Bash", "compact", "Opened a PR"} {
		if strings.Contains(written, leaked) {
			t.Errorf("record carries %q from the stream:\n%s", leaked, written)
		}
	}
}

// A rejected token is not a crash: resuming it spends minutes reaching the
// identical 401, so the classification has to survive at the boundary where
// the retry decision is made.
func TestExecClaudeStopsOnRefusedCredentials(t *testing.T) {
	buf := captureLog(t)
	cfg := fakeClaudeConfig(t, "authfail")

	rep, err := execClaude(context.Background(), cfg, "/implement-issue 2", "", "implement-issue")
	if !errors.Is(err, errAuth) {
		t.Fatalf("a refused token should report errAuth, got %v", err)
	}
	if rep.status() != "auth" {
		t.Errorf("status = %q, want %q — recording it as a crash hides the cause", rep.status(), "auth")
	}
	// The session still has to come back: it is what the run record is keyed
	// on, and what a human resumes by hand once the token works again.
	if rep.sessionID != "sess-auth" {
		t.Errorf("session id = %q, want %q", rep.sessionID, "sess-auth")
	}
	if got := buf.String(); strings.Contains(got, "ERROR: success") {
		t.Errorf("a failed run must not report the subtype as its status\ngot: %s", got)
	}
}

// The advice is the whole point of stopping early, so it has to name the
// commands that fix it rather than only what broke.
func TestAuthAdviceSaysHowToFixIt(t *testing.T) {
	got := authAdvice(errAuth).Error()
	for _, want := range []string{"could not authenticate", "claude auth status", "claude auth login"} {
		if !strings.Contains(got, want) {
			t.Errorf("advice is missing %q\ngot: %s", want, got)
		}
	}
}

// Two independent gates keep the matcher off a healthy run, and this repo's
// own backlog is what makes both necessary: it contains OAuth issues, so runs
// legitimately talk about authentication errors while succeeding.
func TestAuthFailureNeedsAFailedResultThatLeadsWithIt(t *testing.T) {
	const refused = `Failed to authenticate. API Error: 401 {"type":"error",` +
		`"error":{"type":"authentication_error","message":"OAuth access token is invalid."}}`
	const mentions = "Fixed the OAuth issue: an authentication_error no longer retries."

	cases := []struct {
		name   string
		ev     streamEvent
		want   bool
		reason string
	}{
		{"refused credentials", streamEvent{Type: "result", Subtype: "success", IsError: true, Result: refused},
			true, "the CLI's own refusal has to be recognised"},
		{"success mentioning auth", streamEvent{Type: "result", Subtype: "success", Result: mentions},
			false, "a run that succeeded is not a failure to authenticate"},
		{"failure mentioning auth", streamEvent{Type: "result", Subtype: "success", IsError: true, Result: mentions},
			false, "a failed run that merely discusses auth must still be retried"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var rep runReport
			rep.observe(c.ev)
			if rep.authFailed != c.want {
				t.Errorf("authFailed = %v, want %v — %s", rep.authFailed, c.want, c.reason)
			}
		})
	}
}

func TestAuthFailureMatchesTheWaysTheCLIReportsIt(t *testing.T) {
	refused := []string{
		`Failed to authenticate. API Error: 401 {"type":"error","error":{"type":"authentication_error","message":"OAuth access token is invalid."},"request_id":null}`,
		"OAuth token has expired. Please run /login",
		"API Error: 401 Unauthorized",
		"Invalid x-api-key",
	}
	for _, r := range refused {
		if !authFailure(r) {
			t.Errorf("should read as refused credentials: %s", clip(r, 80))
		}
	}
	fine := []string{
		"Opened a PR for issue 2.",
		"API Error: 500 Internal Server Error", // transient, and worth every retry
		"API Error: 429 rate limit exceeded",   // likewise
		"Unknown skill: backlog-drain:implement-issue",
		// The reason the match is anchored: this repo's own backlog contains
		// OAuth issues, and a run that quotes one while failing for an
		// unrelated reason must still be retried, not treated as a token
		// this process cannot fix.
		"I reproduced the report in issue #13, where the drain logged " +
			`Failed to authenticate. API Error: 401 {"type":"error","error":` +
			`{"type":"authentication_error","message":"OAuth access token is invalid."}} ` +
			"and then retried three times. I ran out of turns before opening the PR.",
	}
	for _, r := range fine {
		if authFailure(r) {
			t.Errorf("should not read as refused credentials: %s", clip(r, 80))
		}
	}
}

// --- version skew between the two halves ---

func TestPluginVersionReadsTheInstalledPlugin(t *testing.T) {
	cfg := fakeClaudeConfig(t, "stream")
	t.Setenv(fakePluginEnv, "0.3.0")

	if got := pluginVersion(context.Background(), cfg); got != "0.3.0" {
		t.Errorf("pluginVersion = %q, want the installed plugin's version", got)
	}
}

// A -skill with no plugin prefix names a skill copied into ~/.claude/skills.
// It carries no version, and asking the CLI about a plugin by that name would
// answer about something else.
func TestPluginVersionIsEmptyForAHandInstalledSkill(t *testing.T) {
	cfg := fakeClaudeConfig(t, "stream")
	cfg.skill = skillDir
	t.Setenv(fakePluginEnv, "0.3.0")

	if got := pluginVersion(context.Background(), cfg); got != "" {
		t.Errorf("pluginVersion = %q, want empty: a hand-installed skill has no version", got)
	}
}

func TestPluginVersionIsEmptyWhenTheCLICannotAnswer(t *testing.T) {
	cfg := fakeClaudeConfig(t, "stream")

	if got := pluginVersion(context.Background(), cfg); got != "" {
		t.Errorf("pluginVersion = %q, want empty rather than a guess", got)
	}
}

func TestWarnOnVersionSkew(t *testing.T) {
	for _, tc := range []struct {
		name           string
		skill          string
		binary, plugin string
		warn           bool
	}{
		{name: "matched release", binary: "0.4.0", plugin: "0.4.0"},
		{name: "matched despite the module's v prefix", binary: "v0.4.0", plugin: "0.4.0"},
		{name: "skewed", binary: "0.4.0", plugin: "0.3.0", warn: true},
		// A build from a clone reports a revision. That is an unreleased
		// binary, not a skew, and warning every time would train the operator
		// to ignore the message that matters.
		{name: "unreleased binary", binary: "a1b2c3d4e5f6", plugin: "0.3.0"},
		{name: "dirty clone build", binary: "a1b2c3d4e5f6+dirty", plugin: "0.3.0"},
		{name: "nothing to compare", binary: "", plugin: "0.3.0"},
		{name: "no plugin installed", binary: "0.4.0", plugin: ""},
		// -skill may name another plugin entirely, which has its own version
		// line. Comparing it against this binary would warn on every run of a
		// deliberate configuration, and name the wrong plugin doing it.
		{name: "another plugin's skill", skill: "my-fork:implement-issue", binary: "0.4.0", plugin: "1.2.0"},
		{name: "hand-installed skill", skill: skillDir, binary: "0.4.0", plugin: "1.2.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			buf := captureLog(t)
			skill := tc.skill
			if skill == "" {
				skill = defaultSkill
			}
			warnOnVersionSkew(tc.binary, config{skill: skill, pluginVersion: tc.plugin})

			got := strings.Contains(buf.String(), "version skew")
			if got != tc.warn {
				t.Errorf("warned = %v, want %v\nlog: %s", got, tc.warn, buf.String())
			}
			if tc.warn && !strings.Contains(buf.String(), tc.plugin) {
				t.Errorf("the warning has to name both versions, got: %s", buf.String())
			}
		})
	}
}

// The manifests are the source of truth for the version, so a release binary
// and the plugin it drives compare equal only if this holds.
func TestParseSemverRejectsWhatIsNotARelease(t *testing.T) {
	for _, ok := range []string{"0.0.0", "0.4.0", "10.20.30"} {
		if _, err := parseSemver(ok); err != nil {
			t.Errorf("parseSemver(%q) = %v, want it accepted", ok, err)
		}
	}
	for _, bad := range []string{"", "0.4", "0.4.0.1", "v0.4.0", "0.4.0-rc1", "01.4.0", "-1.4.0", "a.b.c"} {
		if _, err := parseSemver(bad); err == nil {
			t.Errorf("parseSemver(%q) succeeded, want it rejected", bad)
		}
	}
}

// The pseudo-version a plain `go build` inside a module records is the shape
// most likely to be mistaken for a release, and warning on it would fire on
// every developer build.
func TestReleaseVersionRejectsAPseudoVersion(t *testing.T) {
	if v, ok := releaseVersion("v0.0.0-20260825064232-a0aabd243c60"); ok {
		t.Errorf("releaseVersion accepted a pseudo-version as %q", v)
	}
	if v, ok := releaseVersion("v0.4.0"); !ok || v != "0.4.0" {
		t.Errorf("releaseVersion(v0.4.0) = %q, %v; want the bare version", v, ok)
	}
}

func TestParsePRFactsCountsReviewsWithoutQuotingThem(t *testing.T) {
	// The shape `gh pr view --json additions,deletions,changedFiles,createdAt,mergedAt,reviews`
	// returns, reviews and all.
	raw := []byte(`{"additions":412,"deletions":38,"changedFiles":7,` +
		`"createdAt":"2026-08-24T10:34:02Z","mergedAt":"2026-08-24T14:02:00Z",` +
		`"reviews":[{"author":{"login":"someone"},"state":"APPROVED","body":"ship it"},` +
		`{"author":{"login":"else"},"state":"COMMENTED","body":"one nit, otherwise fine"}]}`)

	got, err := parsePRFacts(raw)
	if err != nil {
		t.Fatalf("parsePRFacts: %v", err)
	}
	want := prFacts{Additions: 412, Deletions: 38, ChangedFiles: 7, Reviews: 2,
		Opened: "2026-08-24T10:34:02Z", Merged: "2026-08-24T14:02:00Z"}
	if got != want {
		t.Errorf("facts = %+v, want %+v", got, want)
	}
	// prFacts has nowhere to put a review body, and that is the point: what a
	// reviewer wrote is text, and text never reaches a record.
	if strings.Contains(fmt.Sprintf("%+v", got), "nit") {
		t.Errorf("facts carry review text: %+v", got)
	}
}

func TestParsePRFactsToleratesAPRThatNeverMerged(t *testing.T) {
	// mergedAt is null on a closed-unmerged PR, and a PR nobody reviewed comes
	// back with an empty list.
	got, err := parsePRFacts([]byte(`{"additions":3,"deletions":1,"changedFiles":1,` +
		`"createdAt":"2026-08-24T10:34:02Z","mergedAt":null,"reviews":[]}`))
	if err != nil {
		t.Fatalf("parsePRFacts: %v", err)
	}
	if got.Merged != "" || got.Reviews != 0 || got.Additions != 3 {
		t.Errorf("facts = %+v, want the numbers with no merge timestamp", got)
	}
	if _, err := parsePRFacts([]byte("not json")); err == nil {
		t.Error("junk from gh must be an error, so the outcome is recorded without it")
	}
}

// -post-summary is the one path that shows run data to anybody but the
// operator, so it keeps the discipline the records keep: what the run said
// never reaches it. The recorder is off here, which is also the combination
// the README offers to an operator who wants no local files at all.
func TestSummaryCommentNeverCarriesWhatTheRunSaid(t *testing.T) {
	captureLog(t)
	cfg := fakeClaudeConfig(t, "stream")
	cfg.repo, cfg.rec = "owner/repo", newRecorder(metricsOff)

	rep, err := execClaude(context.Background(), cfg, "/implement-issue 7", "", "implement-issue")
	if err != nil {
		t.Fatalf("execClaude: %v", err)
	}
	var tally issueTally
	tally.add(cfg.rec.recordRun(cfg, runContext{issue: 7, reason: reasonImplement,
		outcome: outcomeOpenedPR, started: time.Now(), ended: time.Now()}, rep))

	body := summaryComment(tally)
	for _, leaked := range []string{"Reading the issue", "go test ./...", "Bash", "compact", "Opened a PR"} {
		if strings.Contains(body, leaked) {
			t.Errorf("summary carries %q from the stream:\n%s", leaked, body)
		}
	}
	// The numbers the canned result reported, which is all it may carry.
	for _, want := range []string{"1 run", "$0.50"} {
		if !strings.Contains(body, want) {
			t.Errorf("summary is missing %q:\n%s", want, body)
		}
	}
}

// --- environment defaults ---

func TestEnvVarNameMapsAFlagToItsVariable(t *testing.T) {
	cases := map[string]string{
		"post-summary": "BACKLOG_DRAIN_POST_SUMMARY",
		"metrics":      "BACKLOG_DRAIN_METRICS",
		"retry-wait":   "BACKLOG_DRAIN_RETRY_WAIT",
	}
	for flagName, want := range cases {
		if got := envVarName(flagName); got != want {
			t.Errorf("envVarName(%q) = %q, want %q", flagName, got, want)
		}
	}
}

// A flag set shaped like the drain's: one of each kind the environment has to
// be able to carry.
func envFlagSet() (*flag.FlagSet, *bool, *string, *time.Duration) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	post := fs.Bool("post-summary", false, "")
	model := fs.String("model", "", "")
	poll := fs.Duration("poll", 5*time.Minute, "")
	return fs, post, model, poll
}

func TestEnvDefaultsSetWhatWasNotPassed(t *testing.T) {
	t.Setenv("BACKLOG_DRAIN_POST_SUMMARY", "1")
	t.Setenv("BACKLOG_DRAIN_MODEL", "claude-opus-5")
	t.Setenv("BACKLOG_DRAIN_POLL", "90s")

	fs, post, model, poll := envFlagSet()
	if err := applyEnvDefaults(fs); err != nil {
		t.Fatalf("applyEnvDefaults: %v", err)
	}
	if err := fs.Parse(nil); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !*post || *model != "claude-opus-5" || *poll != 90*time.Second {
		t.Errorf("flags = %v / %q / %s, want the environment's values", *post, *model, *poll)
	}
	// -h prints DefValue, so the default it reports has to be the one in force.
	if got := fs.Lookup("poll").DefValue; got != "1m30s" {
		t.Errorf("printed default = %q, want the environment's 1m30s", got)
	}
}

// The environment is a preference; an argument is a decision about this run.
func TestCommandLineBeatsTheEnvironment(t *testing.T) {
	t.Setenv("BACKLOG_DRAIN_MODEL", "claude-opus-5")
	t.Setenv("BACKLOG_DRAIN_POST_SUMMARY", "1")

	fs, post, model, _ := envFlagSet()
	if err := applyEnvDefaults(fs); err != nil {
		t.Fatalf("applyEnvDefaults: %v", err)
	}
	if err := fs.Parse([]string{"-model", "claude-haiku-4-5", "-post-summary=false"}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if *model != "claude-haiku-4-5" || *post {
		t.Errorf("flags = %q / %v, want the arguments to win", *model, *post)
	}
}

// A preference that was set, looks set, and silently does nothing is the worst
// outcome for a run nobody is watching.
func TestEnvDefaultsRejectAValueTheFlagCannotParse(t *testing.T) {
	t.Setenv("BACKLOG_DRAIN_POLL", "banana")

	fs, _, _, _ := envFlagSet()
	err := applyEnvDefaults(fs)
	if err == nil {
		t.Fatal("an unparseable value must stop the process, not be skipped")
	}
	// The message has to name both halves: the variable to fix, and the flag
	// it was trying to set.
	for _, want := range []string{"BACKLOG_DRAIN_POLL", "banana", "-poll"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// BACKLOG_DRAIN_VERSION is what a Dockerfile or CI job pins an install with.
// Honouring it would turn every drain on that machine into a version print.
func TestEnvDefaultsIgnoreVersion(t *testing.T) {
	t.Setenv("BACKLOG_DRAIN_VERSION", "0.6.0")

	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	showVersion := fs.Bool("version", false, "")
	if err := applyEnvDefaults(fs); err != nil {
		t.Fatalf("applyEnvDefaults: %v", err)
	}
	if *showVersion {
		t.Error("-version must not be settable from the environment")
	}
}
