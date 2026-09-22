package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// The one label command the skill needs is granted per run, pinned to the issue
// that run was dispatched for. In defaultTools it would have to be
// `Bash(gh issue edit:*)`, which reaches every other issue in the repository.
func TestIssueLabelToolsStayPinnedToOneIssue(t *testing.T) {
	t.Parallel()
	got := strings.Split(issueLabelTools(7), ",")
	for _, want := range []string{
		"Bash(gh issue edit 7 --add-label:*)",
		"Bash(gh issue edit 7 --remove-label:*)",
	} {
		if !slices.Contains(got, want) {
			t.Errorf("issueLabelTools(7) = %v, missing %q", got, want)
		}
	}
	if strings.Contains(defaultTools, "gh issue edit") {
		t.Error("defaultTools grants gh issue edit; it belongs in issueLabelTools, " +
			"where it is bounded to the issue the run is already working on")
	}
	// The pinned entries have to survive alongside an operator's own -add-tools,
	// since that is how they reach the run.
	merged := resolveTools(defaultTools, resolveTools("Bash(bazel:*)", issueLabelTools(7)))
	for _, want := range []string{"Bash(bazel:*)", "Bash(gh issue edit 7 --add-label:*)"} {
		if !strings.Contains(merged, want) {
			t.Errorf("resolved allowlist is missing %q\ngot: %s", want, merged)
		}
	}
}

func TestBuildArgs(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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

// -effort is the other lever a batch comparison varies, and the one Anthropic
// says to pull before a model cascade — a run that dropped it silently would
// make every effort comparison a comparison of the same effort.
func TestBuildArgsPassesTheRequestedEffort(t *testing.T) {
	t.Parallel()
	cfg := config{permissionMode: "acceptEdits", tools: "Read", effort: "medium"}
	args := buildArgs(cfg, "p", "")
	i := slices.Index(args, "--effort")
	if i < 0 || args[i+1] != "medium" {
		t.Errorf("-effort should reach the invocation, got %v", args)
	}
	if slices.Contains(buildArgs(config{tools: "Read"}, "p", ""), "--effort") {
		t.Error("an unset -effort must leave the CLI's own default alone")
	}
	// Not on a resume: the session already has its effort, and re-passing it
	// risks a usage error against a CLI that fixes effort at session start.
	if slices.Contains(buildArgs(cfg, "continue", "sess-1"), "--effort") {
		t.Error("a resume must not re-pass --effort")
	}
}

// The effort enum is closed because the CLI's is: a word polako let through
// that the CLI then rejected would fail a run for a typo. ultracode is the one
// the CLI's docs list that is refused on purpose.
func TestValidateEffort(t *testing.T) {
	t.Parallel()
	for _, ok := range []string{"", "low", "medium", "high", "xhigh", "max"} {
		if err := validateEffort("-effort", ok); err != nil {
			t.Errorf("validateEffort(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"ultracode", "ultra", "medim", "HIGH", "extreme", "5"} {
		if err := validateEffort("-effort", bad); err == nil {
			t.Errorf("validateEffort(%q) = nil, want it refused", bad)
		}
	}
	if err := validateEffort("-effort", "ultracode"); err == nil || !strings.Contains(err.Error(), "fleet") {
		t.Errorf("ultracode should be refused with its own reason, got %v", err)
	}
	// The flag name comes through so the operator knows which one they got
	// wrong — the two share the enum but not the spelling.
	if err := validateEffort("-remediation-effort", "medim"); err == nil ||
		!strings.Contains(err.Error(), "-remediation-effort") {
		t.Errorf("a bad -remediation-effort should name that flag, got %v", err)
	}
}

// -remote=false has to keep today's argv byte for byte — the one thing the
// flag's re-arm on issue #471 must not disturb. -remote=true moves the
// prompt off argv onto stdin (see startClaude/remoteStdin) and adds -n; old
// invocations never carried --remote-control either (that was the retired
// flag issue #82 found inert) and the re-armed ones don't carry it now —
// the channel changed, not the argument.
func TestBuildArgsNeverAsksForRemoteControl(t *testing.T) {
	t.Parallel()
	off := config{permissionMode: "acceptEdits", tools: "Read", repo: "example/repo"}
	on := off
	on.remote, on.remoteName = true, "polako example/repo#52"

	for _, tc := range []struct {
		name string
		cfg  config
	}{{"-remote on", on}, {"-remote off", off}} {
		if got := buildArgs(tc.cfg, "/implement-issue 52", ""); slices.Contains(got, "--remote-control") {
			t.Errorf("%s: that flag was retired on issue #82, got %v", tc.name, got)
		}
	}

	offArgs := buildArgs(off, "/implement-issue 52", "")
	if !slices.Contains(offArgs, "/implement-issue 52") || slices.Contains(offArgs, "--input-format") ||
		slices.Contains(offArgs, "-n") {
		t.Errorf("-remote=false must invoke claude exactly as before the re-arm, got %v", offArgs)
	}

	onArgs := buildArgs(on, "/implement-issue 52", "")
	if slices.Contains(onArgs, "/implement-issue 52") {
		t.Errorf("-remote=true must not carry the prompt on argv — it travels over stdin instead, got %v", onArgs)
	}
	if !slices.Contains(onArgs, "--input-format") {
		t.Errorf("-remote=true must ask for stream-json input, got %v", onArgs)
	}
	i := slices.Index(onArgs, "-n")
	if i < 0 || onArgs[i+1] != "polako example/repo#52" {
		t.Errorf("-remote=true must name the session -n polako <repo>#<issue>, got %v", onArgs)
	}
}

func TestBuildArgsAppliesAddTools(t *testing.T) {
	t.Parallel()
	cfg := config{permissionMode: "plan", tools: "Read", addTools: "Bash(zig:*)"}
	args := buildArgs(cfg, "p", "")
	i := slices.Index(args, "--allowedTools")
	if i < 0 || args[i+1] != "Read,Bash(zig:*)" {
		t.Errorf("-add-tools should reach the invocation, got %v", args)
	}
}

func TestSampleTickScalesWithInterval(t *testing.T) {
	t.Parallel()
	cases := []struct {
		interval, want time.Duration
	}{
		{15 * time.Minute, 30 * time.Second}, // capped, so long runs stay quiet
		{40 * time.Second, 10 * time.Second},
		{time.Millisecond, 50 * time.Millisecond}, // floored, so tests can't spin
	}
	for _, c := range cases {
		if got := sampleTick(c.interval); got != c.want {
			t.Errorf("sampleTick(%s) = %s, want %s", c.interval, got, c.want)
		}
	}
}

// TestPreflightPairsSaysNothingUnasked pins the gate each row promises: unset
// (the zero-value config, no log path) produces exactly one row — the
// unconditional epics disclosure — and nothing else. Every other row earns its
// keep only when there is something to disclose.
func TestPreflightPairsSaysNothingUnasked(t *testing.T) {
	t.Parallel()
	got := preflightPairs(config{})
	if len(got) != 1 || got[0][0] != "epics" {
		t.Errorf("preflightPairs(zero value) = %v, want only the epics row", got)
	}
}

// TestPreflightPairsGatesAndOrdersEveryRow is the regression test the code
// review that shipped this function found missing: preflightPairs replaced
// eight independently-gated narrate/log calls with one function, and nothing
// pinned either the gating or the row order that resulted. One config with
// every condition set exercises every row in one pass; the order asserted
// here is the order the old sentences printed in, which the row order is
// meant to preserve (see preflightPairs's doc comment).
func TestPreflightPairsGatesAndOrdersEveryRow(t *testing.T) {
	t.Parallel()
	cfg := config{
		label:       "ready",
		model:       "opus",
		effort:      "medium",
		dryRun:      true,
		maxCost:     15,
		usage:       &usageSnapshot{pools: []usagePool{{name: "session", percent: 10}}},
		postSummary: true,
		notifyCmd:   "notify-send",
		remote:      true,
		rec:         &recorder{dir: "/tmp/metrics"},
		shiftID:     "abc123",
		logPath:     "/tmp/logs/shift.log",
	}
	got := preflightPairs(cfg)
	wantLabels := []string{
		"epics", "queue", "model", "dry-run", "caps", "plan", "post-summary", "notify", "remote", "run data", "shift", "shift log",
	}
	if len(got) != len(wantLabels) {
		t.Fatalf("preflightPairs with every condition set = %d rows %v, want %d rows %v",
			len(got), got, len(wantLabels), wantLabels)
	}
	for i, want := range wantLabels {
		if got[i][0] != want {
			t.Errorf("row %d label = %q, want %q (full: %v)", i, got[i][0], want, got)
		}
	}
	if !strings.Contains(got[1][1], `"ready"`) {
		t.Errorf("queue row = %q, want it to name the -label value", got[1][1])
	}
	if !strings.Contains(got[2][1], "model opus") || !strings.Contains(got[2][1], "effort medium") {
		t.Errorf("model row = %q, want it to name both dispatch knobs", got[2][1])
	}
	if !strings.Contains(got[5][1], "session 10%") {
		t.Errorf("plan row = %q, want the usage snapshot rendered", got[5][1])
	}
	if !strings.Contains(got[9][1], "/tmp/metrics") || !strings.Contains(got[10][1], "abc123") {
		t.Errorf("run data/shift rows = %v, want the recorder dir and shift id named", got[9:11])
	}
}

// modelEffortLine discloses the by-size cells too, since POLAKO_MODEL_BY_SIZE
// / POLAKO_EFFORT_BY_SIZE can set them silently — same reasoning as every
// other unprompted preflight row.
func TestModelEffortLine(t *testing.T) {
	t.Parallel()
	if got := modelEffortLine(config{}); got != "" {
		t.Errorf("modelEffortLine(zero value) = %q, want empty", got)
	}
	got := modelEffortLine(config{
		model: "opus", effort: "medium",
		modelBySize: "S=sonnet,L=opus", effortBySize: "S=medium,L=max",
	})
	for _, want := range []string{"model opus", "effort medium", "model-by-size S=sonnet,L=opus", "effort-by-size S=medium,L=max"} {
		if !strings.Contains(got, want) {
			t.Errorf("modelEffortLine = %q, missing %q", got, want)
		}
	}
	if got := modelEffortLine(config{modelBySize: "S=sonnet"}); got != "model-by-size S=sonnet" {
		t.Errorf("modelEffortLine(model-by-size alone) = %q, want just that part", got)
	}
}

// The cap -stall cannot stand in for: this run is not silent, it just never
// stops. The kill has to read as a deliberate stop rather than as the crash it
// looks like from the exit code, or the supervisor resumes it into the same
// wall.
func TestExecClaudeKillsARunPastItsBudget(t *testing.T) {
	t.Parallel()
	buf := captureLog(t)
	cfg := fakeClaudeConfig(t, "hang")
	cfg.stall = 0 // only the budget may end this run

	// Half a second rather than the tightest budget that works: the clock
	// starts before the child does, so anything close to a warm start races
	// process startup for the init event this test then asserts on. buildFakeCLI
	// pays the expensive first exec, and this leaves room for the rest.
	start := time.Now()
	rep, err := execClaude(context.Background(), cfg, "/implement-issue 7", "", "implement-issue",
		500*time.Millisecond)
	if !errors.Is(err, errBudget) {
		t.Fatalf("a run past -max-issue-time should report errBudget, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("the run took %v — the budget watchdog did not kill it", elapsed)
	}
	if !rep.overBudget {
		t.Error("the report should say the budget stopped this run")
	}
	if got := rep.status(); got != "budget" {
		t.Errorf("status = %q, want %q so the run data can tell it from a crash", got, "budget")
	}
	// The session survives the kill even though nothing else about the run
	// does: a later drain that raises the cap has something to resume.
	if rep.sessionID != "sess-hang" {
		t.Errorf("sessionID = %q, want the session the run reported before it was killed", rep.sessionID)
	}
	if !strings.Contains(buf.String(), "-max-issue-time") {
		t.Errorf("the log should say why the run was killed\ngot:\n%s", buf.String())
	}
}

func TestSleepReturnsOnCancel(t *testing.T) {
	t.Parallel()
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

func TestExecClaudeStreamsEventsAndCapturesSession(t *testing.T) {
	t.Parallel()
	buf := captureLog(t)
	cfg := fakeClaudeConfig(t, "stream")

	rep, err := execClaude(context.Background(), cfg, "/implement-issue 7", "", "implement-issue", 0)
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

// The child's stderr is narration too: it reaches the shift log as whole
// attributed lines rather than tearing raw across the terminal.
func TestExecClaudeCarriesChildStderrIntoTheNarration(t *testing.T) {
	t.Parallel()
	buf := captureLog(t)
	cfg := fakeClaudeConfig(t, "deadsession")

	if _, err := execClaude(context.Background(), cfg, "/implement-issue 7", "sess-dead", "implement-issue", 0); err == nil {
		t.Fatal("a dead session should end the attempt with an error")
	}
	if want := "[claude stderr] No conversation found"; !strings.Contains(buf.String(), want) {
		t.Errorf("child stderr missing from the narration: want %q\ngot:\n%s", want, buf.String())
	}
	// The recap milestone, from execClaude itself so remediation dispatches
	// get it too — for a crash it is often the only cause on record.
	if want := "last stderr: No conversation found"; !strings.Contains(buf.String(), want) {
		t.Errorf("a failed run should surface its stderr tail: want %q\ngot:\n%s", want, buf.String())
	}
}

// The mirror image of the -remote=true dispatch test below: -remote=false
// must leave the child's actual stdin untouched, not just buildArgs's argv —
// TestBuildArgsNeverAsksForRemoteControl already pins the argv layer, this
// pins the exec.Cmd wiring a refactor of startClaude's `if cfg.remote`
// gating could otherwise regress silently.
func TestDispatchUnderRemoteFalseLeavesStdinUntouched(t *testing.T) {
	t.Parallel()
	cfg := fakeClaudeConfig(t, "stream")
	stdin := watchClaudeStdin(t, &cfg)

	if _, err := execClaude(context.Background(), cfg, "/implement-issue 7", "", "implement-issue", 0); err != nil {
		t.Fatalf("a healthy run: %v", err)
	}
	if got := stdin(); got != "" {
		t.Errorf("-remote=false must leave stdin empty, got %q", got)
	}
}

// buildArgs is asserted directly above, but the argv and stdin a child
// actually receives is the thing the promise was made about, so pin that end
// too: a real dispatch under -remote must reach the CLI carrying the
// control_request and the prompt as a user message, named on argv, and never
// the literal prompt on argv itself. issue #471 re-armed what issue #82 found
// inert.
func TestDispatchUnderRemoteRegistersAndLogsTheSessionURL(t *testing.T) {
	t.Parallel()
	buf := captureLog(t)
	cfg := fakeClaudeConfig(t, "remoteok")
	cfg.remote, cfg.repo, cfg.remoteName = true, "example/repo", "polako example/repo#7"
	args := watchClaudeArgs(t, &cfg)
	stdin := watchClaudeStdin(t, &cfg)

	rep, err := execClaude(context.Background(), cfg, "/implement-issue 7", "", "implement-issue", 0)
	if err != nil {
		t.Fatalf("a healthy run under -remote: %v", err)
	}
	got := args()
	if len(got) != 1 {
		t.Fatalf("want exactly one dispatch — there is nothing left to re-dispatch for — got %v", got)
	}
	if strings.Contains(got[0], "--remote-control") {
		t.Errorf("that flag was retired on issue #82: %s", got[0])
	}
	if strings.Contains(got[0], "/implement-issue 7") {
		t.Errorf("the prompt must travel over stdin, not argv, under -remote: %s", got[0])
	}
	if !strings.Contains(got[0], "-n polako example/repo#7") {
		t.Errorf("the session must be named on argv: %s", got[0])
	}

	lines := strings.Split(strings.TrimSuffix(stdin(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("stdin should carry exactly two lines — the control_request and the prompt — got %v", lines)
	}
	if !strings.Contains(lines[0], `"subtype":"remote_control"`) {
		t.Errorf("stdin's first line should be the control_request, got %q", lines[0])
	}
	if !strings.Contains(lines[1], `"content":"/implement-issue 7"`) {
		t.Errorf("stdin's second line should carry the prompt as a user message, got %q", lines[1])
	}

	if !rep.remoteRegistered || rep.remoteURL != "https://claude.ai/code/session/abc123" {
		t.Errorf("a success reply should register and capture the session URL, got %+v", rep)
	}
	if !strings.Contains(buf.String(), "registered with Remote Control: https://claude.ai/code/session/abc123") {
		t.Errorf("the session URL should be narrated once, got:\n%s", buf.String())
	}
}

// A reply is not guaranteed either way — the CLI may refuse the request, or
// never answer at all — and neither may hang, fail or re-dispatch the run.
func TestDispatchUnderRemoteLogsAnErrorReply(t *testing.T) {
	t.Parallel()
	buf := captureLog(t)
	cfg := fakeClaudeConfig(t, "remoteerror")
	cfg.remote, cfg.repo, cfg.remoteName = true, "example/repo", "polako example/repo#7"

	rep, err := execClaude(context.Background(), cfg, "/implement-issue 7", "", "implement-issue", 0)
	if err != nil {
		t.Fatalf("a healthy run under -remote: %v", err)
	}
	if rep.remoteRegistered || rep.remoteError != "Remote Control initialization failed" {
		t.Errorf("an error reply should be captured, not registered, got %+v", rep)
	}
	if !strings.Contains(buf.String(), "Remote Control did not register: Remote Control initialization failed") {
		t.Errorf("the error should be narrated once, got:\n%s", buf.String())
	}
}

func TestDispatchUnderRemoteLogsNoReply(t *testing.T) {
	t.Parallel()
	buf := captureLog(t)
	// "stream" never emits a control_response at all — the CLI simply never
	// answers, which is as valid an outcome as a reply either way.
	cfg := fakeClaudeConfig(t, "stream")
	cfg.remote, cfg.repo, cfg.remoteName = true, "example/repo", "polako example/repo#7"

	rep, err := execClaude(context.Background(), cfg, "/implement-issue 7", "", "implement-issue", 0)
	if err != nil {
		t.Fatalf("a healthy run under -remote: %v", err)
	}
	if rep.remoteRegistered || rep.remoteError != "" {
		t.Errorf("no reply should leave both unset, got %+v", rep)
	}
	if !strings.Contains(buf.String(), "no Remote Control reply — this run stayed unwatched") {
		t.Errorf("the silence should be narrated once, got:\n%s", buf.String())
	}
}

// A control_response carrying some other request's id must be read as no
// reply at all, not as this run's own registration outcome — the guard
// issue #471's own review added after a finder pointed out request_id was
// parsed but never checked.
func TestDispatchUnderRemoteIgnoresAMismatchedRequestID(t *testing.T) {
	t.Parallel()
	buf := captureLog(t)
	cfg := fakeClaudeConfig(t, "remotewrongid")
	cfg.remote, cfg.repo, cfg.remoteName = true, "example/repo", "polako example/repo#7"

	rep, err := execClaude(context.Background(), cfg, "/implement-issue 7", "", "implement-issue", 0)
	if err != nil {
		t.Fatalf("a healthy run under -remote: %v", err)
	}
	if rep.remoteRegistered || rep.remoteURL != "" || rep.remoteError != "" {
		t.Errorf("a mismatched request_id must not be read as this run's own reply, got %+v", rep)
	}
	if !strings.Contains(buf.String(), "no Remote Control reply — this run stayed unwatched") {
		t.Errorf("a mismatched reply should still be narrated as no reply, got:\n%s", buf.String())
	}
	if strings.Contains(buf.String(), "unrelated") {
		t.Errorf("the unrelated reply's own session URL must never be narrated, got:\n%s", buf.String())
	}
}

// docs/hardening.md tells an operator to wrap a shift in an egress proxy by
// exporting HTTPS_PROXY and a CA path around `polako work`, and that only
// reaches the model because the dispatch leaves cmd.Env nil and os/exec hands
// the child the parent's environment. Setting cmd.Env for any reason — one
// variable a run wanted, added the obvious way — would take the proxy back out
// silently: the run would still pass, and simply stop being watched. So pin the
// passthrough rather than the absence of an assignment.
func TestDispatchGivesTheChildTheOperatorsEnvironment(t *testing.T) {
	buf := captureLog(t)
	cfg := fakeClaudeConfig(t, "envcanary")
	t.Setenv(envCanaryVar, "http://localhost:8443")

	if _, err := execClaude(context.Background(), cfg, "/implement-issue 7", "", "implement-issue", 0); err != nil {
		t.Fatalf("execClaude: %v", err)
	}
	if want := "canary=http://localhost:8443"; !strings.Contains(buf.String(), want) {
		t.Errorf("the claude child did not inherit the environment it was started with: want %q\ngot:\n%s", want, buf.String())
	}
}

// The same promise one funnel over: every gh and git the supervisor runs goes
// through capture, and that is the likelier place for a cmd.Env to appear — a
// GH_TOKEN, a GIT_TERMINAL_PROMPT=0, added for a reason that has nothing to do
// with proxies. An assignment there takes the branch pushes out of the
// firewall, which docs/hardening.md calls the flow most worth watching, and
// every other test in this package still passes.
func TestGhAndGitInheritTheOperatorsEnvironmentToo(t *testing.T) {
	t.Setenv(fakeClaudeEnv, "envcanaryout") // inherited by the child process
	t.Setenv(envCanaryVar, "http://localhost:8443")

	out, err := capture(context.Background(), t.TempDir(), nil, fakeCLI(t), "status")
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if want := "canary=http://localhost:8443"; !strings.Contains(string(out), want) {
		t.Errorf("a gh or git child did not inherit the environment polako was started with: want %q\ngot:\n%s", want, out)
	}
}

// The tail is bounded because a six-hour run's stderr is not, and the bytes a
// usage error lives in are the last ones.
func TestTailWriterKeepsTheEnd(t *testing.T) {
	t.Parallel()
	w := &tailWriter{}
	fmt.Fprint(w, strings.Repeat("x", maxStderrTail))
	fmt.Fprint(w, "the last thing it said")
	got := w.String()
	if len(got) != maxStderrTail {
		t.Errorf("tail length = %d, want it capped at %d", len(got), maxStderrTail)
	}
	if !strings.HasSuffix(got, "the last thing it said") {
		t.Errorf("tail = %q…, want it to end with the newest output", got[:40])
	}
}

func TestExecClaudeReportsCrashesWithTheSessionToResume(t *testing.T) {
	t.Parallel()
	captureLog(t)
	cfg := fakeClaudeConfig(t, "crash")

	rep, err := execClaude(context.Background(), cfg, "/implement-issue 7", "", "implement-issue", 0)
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
	t.Parallel()
	captureLog(t)
	cfg := fakeClaudeConfig(t, "hang")
	cfg.stall = 300 * time.Millisecond

	start := time.Now()
	rep, err := execClaude(context.Background(), cfg, "/implement-issue 7", "", "implement-issue", 0)
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

// While the terminal is quiet but the run is not — a stream of tool calls the
// default terminal filters out — the heartbeat says one "still working" line
// per -heartbeat, naming the stage and counting the tool calls, and never
// after the finish line.
func TestExecClaudeHeartbeatSpeaksWhileTheTerminalIsQuiet(t *testing.T) {
	t.Parallel()
	buf := captureLog(t)
	cfg := fakeClaudeConfig(t, "heartbeat")
	cfg.heartbeat = 300 * time.Millisecond

	if _, err := execClaude(context.Background(), cfg, "/implement-issue 7", "", "implement-issue", 0); err != nil {
		t.Fatalf("execClaude: %v", err)
	}
	got := buf.String()
	if !strings.Contains(got, "still working") {
		t.Fatalf("no heartbeat in a run quiet on the terminal for ~1s\ngot:\n%s", got)
	}
	for _, want := range []string{"reading the code", "tool call"} {
		if !strings.Contains(got, want) {
			t.Errorf("heartbeat missing %q\ngot:\n%s", want, got)
		}
	}
	if i, j := strings.LastIndex(got, "still working"), strings.Index(got, "finished ("); j >= 0 && i > j {
		t.Errorf("a heartbeat landed after the finish line\ngot:\n%s", got)
	}
}

// -verbose puts every event on the terminal, so its clock never expires and
// the heartbeat stays silent — a consequence of measuring terminal silence,
// not a special case.
func TestExecClaudeHeartbeatIsSilentUnderVerbose(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	captureUI(t, &ui{terminal: &buf, verbose: true})
	cfg := fakeClaudeConfig(t, "heartbeat")
	cfg.heartbeat = 300 * time.Millisecond

	if _, err := execClaude(context.Background(), cfg, "/implement-issue 7", "", "implement-issue", 0); err != nil {
		t.Fatalf("execClaude: %v", err)
	}
	if strings.Contains(buf.String(), "still working") {
		t.Errorf("the heartbeat fired under -verbose, where the terminal is never quiet\ngot:\n%s", buf.String())
	}
}

// The heartbeat keeps speaking while the event stream is quiet, not just the
// terminal: a stalled run's last heartbeats are the run-up that makes the
// -stall kill legible rather than a first word at fifteen minutes.
func TestExecClaudeHeartbeatIsTheRunUpToAStallKill(t *testing.T) {
	t.Parallel()
	buf := captureLog(t)
	cfg := fakeClaudeConfig(t, "hang") // an init event, then total silence
	cfg.heartbeat = 200 * time.Millisecond
	cfg.stall = time.Second

	if _, err := execClaude(context.Background(), cfg, "/implement-issue 7", "", "implement-issue", 0); err == nil {
		t.Fatal("the hung run should still be killed as stalled")
	}
	got := buf.String()
	if !strings.Contains(got, "still working") {
		t.Fatalf("the stall kill had no heartbeat run-up\ngot:\n%s", got)
	}
	if i, j := strings.Index(got, "still working"), strings.Index(got, "killing the run"); i < 0 || j < 0 || i > j {
		t.Errorf("the heartbeat should land before the stall kill, not after\ngot:\n%s", got)
	}
}

// An event too large for the reader used to end the scan silently. The child
// then blocked writing into a pipe nobody was draining, cmd.Wait never
// returned, and the run died as a -stall kill a quarter of an hour later —
// reported as a stall, which is the one thing it was not. It has to fail
// straight away, and say what actually happened.
func TestExecClaudeReportsAnEventTooLargeToRead(t *testing.T) {
	t.Parallel()
	captureLog(t)
	cfg := fakeClaudeConfig(t, "giant")
	cfg.stall = 0 // the watchdog must not be what rescues this

	// A backstop, not the mechanism: without the fix nothing ends the run, and
	// a suite that hangs for ten minutes says less than one that fails.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	start := time.Now()
	rep, err := execClaude(ctx, cfg, "/implement-issue 7", "", "implement-issue", 0)
	if err == nil || !strings.Contains(err.Error(), "could not read the event stream") {
		t.Fatalf("an unreadable event should be reported as one, got err=%v", err)
	}
	if !errors.Is(err, bufio.ErrTooLong) {
		t.Errorf("err = %v, want the scanner's own cause wrapped in it", err)
	}
	if rep.sessionID != "sess-giant" {
		t.Errorf("session id = %q, want %q so the retry can resume it", rep.sessionID, "sess-giant")
	}
	if elapsed := time.Since(start); elapsed > 15*time.Second {
		t.Errorf("took %s to give up, so the child was waited out rather than killed", elapsed)
	}
}

func TestExecClaudeStopsWhenTheContextIsCancelled(t *testing.T) {
	t.Parallel()
	cfg := fakeClaudeConfig(t, "hang")

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := execClaude(ctx, cfg, "/implement-issue 7", "", "implement-issue", 0); err == nil {
		t.Fatal("cancelling the context should end the run with an error")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("Ctrl+C took %s to take effect", elapsed)
	}
}

func TestRunClaudeResumesRatherThanRestartingTheSkill(t *testing.T) {
	t.Parallel()
	buf := captureLog(t)
	cfg := fakeClaudeConfig(t, "stream")

	if _, err := runClaude(context.Background(), cfg, 12, "", reasonImplement, 0); err != nil {
		t.Fatalf("fresh run: %v", err)
	}
	want := fmt.Sprintf("-p /%s 12", defaultSkill)
	if !strings.Contains(buf.String(), want) {
		t.Errorf("a fresh run should invoke %q\ngot:\n%s", want, buf.String())
	}

	buf.Reset()
	if _, err := runClaude(context.Background(), cfg, 12, "sess-old", reasonResume, 0); err != nil {
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

// What a resume used to be told was "continue exactly where it stopped", which
// reads as an assurance that the last step landed. It never was one: the
// interruption arrives mid-action, so the resumed run has to look before it
// carries on.
func TestResumePromptAsksTheRunToRederiveItsState(t *testing.T) {
	t.Parallel()
	// Both flavours, because the re-derive discipline and the two structural
	// properties below belong to every resume, not to the crash one.
	for _, reason := range []string{reasonResume, reasonUnfinished} {
		t.Run(reason, func(t *testing.T) {
			prompt := resumePrompt(defaultSkill, 12, reason)

			// The label is in the list for the same reason the workspace checks
			// are: a kill between `gh issue comment` and the label leaves a
			// question the supervisor cannot see, and parks a healthy issue
			// over it.
			for _, want := range []string{"git status", "branch", "pull request",
				"issue thread", awaitingAnswerLabel} {
				if !strings.Contains(prompt, want) {
					t.Errorf("a resume prompt should mention %q, so the run checks rather than assumes\ngot: %s",
						want, prompt)
				}
			}

			// execClaude takes the slash command a prompt invokes as an argument
			// rather than parsing it back out, and runClaude passes "" for a
			// resume. A prompt that opened with "/" would make that a lie.
			if strings.HasPrefix(prompt, "/") {
				t.Errorf("a resume prompt is plain text and must not open with a slash command\ngot: %s", prompt)
			}

			// fakeClaude reads the issue number off the end of the prompt. A
			// second number after it would not fail here — it would quietly
			// point every resume in the suite at the wrong issue.
			if got := lastNumber(prompt); got != "12" {
				t.Errorf("last number in the prompt = %q, want the issue number %q\ngot: %s", got, "12", prompt)
			}
		})
	}
}

// The whole reason the second flavour needs its own words. A run that ended its
// turn believing something would bring it back, resumed with a prompt that says
// it was "interrupted part-way through an action", is told nothing that
// contradicts the belief — so it has every reason to end its turn waiting
// again, and the resume buys a second identical run.
func TestUnfinishedResumePromptContradictsTheBeliefThatItWasPaused(t *testing.T) {
	t.Parallel()
	prompt := resumePrompt(defaultSkill, 12, reasonUnfinished)

	for _, want := range []string{
		"ended its turn",     // what happened
		"no later turn",      // and what that means
		"nothing will wake",  // said outright, because a monitor is what it waited on
		"finish it in this",  // the only turn there is
		"open the pull requ", // named, rather than left as "continue the workflow"
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the unfinished resume prompt should say %q\ngot: %s", want, prompt)
		}
	}
	// The crash wording is not merely unhelpful here, it is false: nothing
	// interrupted this run. Left in, it is the sentence the run would reconcile
	// its own memory against.
	if strings.Contains(prompt, "interrupted part-way") {
		t.Errorf("a clean exit was not interrupted, and must not be told it was\ngot: %s", prompt)
	}
	// And the crash flavour keeps saying what is true of it.
	if crash := resumePrompt(defaultSkill, 12, reasonResume); !strings.Contains(crash, "incomplete") {
		t.Errorf("the crash resume prompt stopped warning that the last action may be incomplete\ngot: %s", crash)
	}
}

// -visual-evidence is a second slash argument the skill reads, not a claude
// flag, so issueRun is the whole of its wiring: on (the default) leaves the
// prompt exactly as it was before this flag existed, off appends the literal
// value SKILL.md's `evidence` argument checks for.
func TestIssueRunPassesTheEvidenceSwitch(t *testing.T) {
	t.Parallel()
	cfg := config{skill: defaultSkill, visualEvidence: true}
	if _, prompt, _ := issueRun(cfg, 12); prompt != fmt.Sprintf("/%s 12", defaultSkill) {
		t.Errorf("-visual-evidence on should leave the prompt unchanged, got %q", prompt)
	}

	cfg.visualEvidence = false
	want := fmt.Sprintf("/%s 12 no-evidence", defaultSkill)
	if _, prompt, _ := issueRun(cfg, 12); prompt != want {
		t.Errorf("-visual-evidence off should append %q, got %q", "no-evidence", prompt)
	}
}

// The failure both of these guard against: -skill naming a slash command the
// installation does not have. CLIs before 2.1.85 answered with a clean exit
// at zero turns, and without this fallback the supervisor reports only "no PR
// and no questions".
func TestExecClaudeFlagsARunThatTookNoTurns(t *testing.T) {
	t.Parallel()
	captureLog(t)
	cfg := fakeClaudeConfig(t, "noturns")

	_, err := execClaude(context.Background(), cfg, "/nope 1", "", "nope", 0)
	if !errors.Is(err, errNoWork) {
		t.Fatalf("a clean exit at 0 turns should report errNoWork, got %v", err)
	}
}

// From 2.1.85 the CLI answers an unknown slash command with a success result
// — two turns on paper, nothing done — so the zero-turn fallback never fires.
// The init event's command inventory is the tell that still does.
func TestExecClaudeFlagsASkillTheSessionDoesNotList(t *testing.T) {
	t.Parallel()
	captureLog(t)
	cfg := fakeClaudeConfig(t, "unknownskill")

	rep, err := execClaude(context.Background(), cfg, "/"+defaultSkill+" 1", "", defaultSkill, 0)
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
	t.Parallel()
	captureLog(t)
	cfg := fakeClaudeConfig(t, "unknownskill")

	if _, err := execClaude(context.Background(), cfg,
		"PR #3 has merge conflicts with the remote default branch.", "", "", 0); err != nil {
		t.Fatalf("a plain prompt must not trip the missing-skill check, got %v", err)
	}
}

// --- run-data capture, end to end against the fake CLI ---

func TestExecClaudeCapturesTheResultUsage(t *testing.T) {
	t.Parallel()
	captureLog(t)
	cfg := fakeClaudeConfig(t, "stream")

	rep, err := execClaude(context.Background(), cfg, "/implement-issue 7", "", "implement-issue", 0)
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
	t.Parallel()
	captureLog(t)
	cfg := fakeClaudeConfig(t, "oddshape")

	rep, err := execClaude(context.Background(), cfg, "/implement-issue 7", "", "implement-issue", 0)
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
	t.Parallel()
	captureLog(t)
	cfg := fakeClaudeConfig(t, "oldcli")

	rep, err := execClaude(context.Background(), cfg, "/implement-issue 7", "", "implement-issue", 0)
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
	t.Parallel()
	captureLog(t)
	cfg := fakeClaudeConfig(t, "partial")

	rep, err := execClaude(context.Background(), cfg, "/implement-issue 7", "", "implement-issue", 0)
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
	t.Parallel()
	captureLog(t)
	cfg := fakeClaudeConfig(t, "stream")
	dir := t.TempDir()
	cfg.repo, cfg.rec = "owner/repo", newRecorder(dir)

	rep, err := execClaude(context.Background(), cfg, "/implement-issue 7", "", "implement-issue", 0)
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
	t.Parallel()
	buf := captureLog(t)
	cfg := fakeClaudeConfig(t, "authfail")

	rep, err := execClaude(context.Background(), cfg, "/implement-issue 2", "", "implement-issue", 0)
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
	t.Parallel()
	got := authAdvice(errAuth).Error()
	for _, want := range []string{"could not authenticate", "claude auth status", "claude auth login"} {
		if !strings.Contains(got, want) {
			t.Errorf("advice is missing %q\ngot: %s", want, got)
		}
	}
}
