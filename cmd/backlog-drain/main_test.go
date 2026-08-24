package main

import (
	"bytes"
	"context"
	"errors"
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
		emit(`{"type":"assistant","session_id":"sess-xyz","message":{"content":[{"type":"text","text":"Reading the issue."}],` +
			`"usage":{"input_tokens":10,"output_tokens":20,"cache_read_input_tokens":30,"cache_creation_input_tokens":40}}}`)
		emit(`{"type":"assistant","session_id":"sess-xyz","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"go test ./..."}}],` +
			`"usage":{"input_tokens":1,"output_tokens":2,"cache_read_input_tokens":3,"cache_creation_input_tokens":4}}}`)
		emit(`{"type":"result","subtype":"success","session_id":"sess-xyz","duration_ms":1000,"duration_api_ms":800,` +
			`"num_turns":3,"total_cost_usd":0.5,` +
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
		// What an unresolvable slash command looks like: a clean exit, no work.
		emit(`{"type":"system","subtype":"init","session_id":"sess-none","model":"claude-opus-5"}`)
		emit(`{"type":"assistant","session_id":"sess-none","message":{"content":[{"type":"text","text":"Unknown command: /nope"}]}}`)
		emit(`{"type":"result","subtype":"success","session_id":"sess-none","duration_ms":100,"num_turns":0,"total_cost_usd":0}`)
		return 0
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
	if got := buf.String(); !strings.Contains(got, "finished (ERROR: error_max_turns)") {
		t.Errorf("error results should be flagged\ngot: %s", got)
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

	rep, err := execClaude(context.Background(), cfg, "/implement-issue 7", "")
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

	rep, err := execClaude(context.Background(), cfg, "/implement-issue 7", "")
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
	rep, err := execClaude(context.Background(), cfg, "/implement-issue 7", "")
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

// The failure this guards against: -skill naming a slash command the
// installation does not have. Claude answers "Unknown command", exits 0, and
// without this the supervisor reports only "no PR and no questions".
func TestExecClaudeFlagsARunThatTookNoTurns(t *testing.T) {
	captureLog(t)
	cfg := fakeClaudeConfig(t, "noturns")

	_, err := execClaude(context.Background(), cfg, "/nope 1", "")
	if !errors.Is(err, errNoWork) {
		t.Fatalf("a clean exit at 0 turns should report errNoWork, got %v", err)
	}
}

// --- run-data capture, end to end against the fake CLI ---

func TestExecClaudeCapturesTheResultUsage(t *testing.T) {
	captureLog(t)
	cfg := fakeClaudeConfig(t, "stream")

	rep, err := execClaude(context.Background(), cfg, "/implement-issue 7", "")
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

	rep, err := execClaude(context.Background(), cfg, "/implement-issue 7", "")
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

	rep, err := execClaude(context.Background(), cfg, "/implement-issue 7", "")
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

	rep, err := execClaude(context.Background(), cfg, "/implement-issue 7", "")
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

	rep, err := execClaude(context.Background(), cfg, "/implement-issue 7", "")
	if err != nil {
		t.Fatalf("execClaude: %v", err)
	}
	cfg.rec.recordRun(cfg, runContext{issue: 7, reason: reasonImplement,
		outcome: outcomeOpenedPR, started: time.Now(), ended: time.Now()}, rep)

	written := strings.Join(readRecords(t, dir, cfg.repo), "\n")
	// Both the assistant text and the tool input the stream carried; the
	// record counts tool uses, but must never say what they were.
	for _, leaked := range []string{"Reading the issue", "go test ./...", "Bash"} {
		if strings.Contains(written, leaked) {
			t.Errorf("record carries %q from the stream:\n%s", leaked, written)
		}
	}
}
