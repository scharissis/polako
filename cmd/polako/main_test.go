package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// fakeClaudeEnv makes the test binary impersonate the claude CLI: when it is
// set, TestMain streams canned events instead of running the suite. That lets
// execClaude be exercised end to end on every platform, with no shell scripts.
const fakeClaudeEnv = "POLAKO_FAKE_CLAUDE"

// fakePluginEnv is the version `claude plugin list --json` should report for
// the installed polako plugin. Unset means the subcommand fails, which
// is what a CLI too old to have it does.
const fakePluginEnv = "POLAKO_FAKE_PLUGIN_VERSION"

// envCanaryVar is a variable the "envcanary" mode echoes back, standing in for
// the HTTPS_PROXY and CA-certificate variables docs/hardening.md tells an
// operator to export around a shift. Nothing but a name: what is under test is
// that the child saw it at all.
const envCanaryVar = "POLAKO_TEST_ENV_CANARY"

// fakeArgsLogEnv names a file every fake claude run appends its argv to, one
// line per invocation. It is how a test sees what was actually dispatched —
// including a run the supervisor threw away and re-dispatched, which by design
// leaves no other trace at all.
const fakeArgsLogEnv = "POLAKO_FAKE_ARGS_LOG"

func TestMain(m *testing.M) {
	// A notify command inherits every variable the drain has, the fake-CLI ones
	// included, so what says this invocation is the notifier is the one variable
	// only a notification carries. Checked first for that reason: it takes no
	// arguments, so argv cannot tell it apart from a bare claude run.
	if dest := os.Getenv(fakeNotifyEnv); dest != "" && os.Getenv(notifyPrefix+"EVENT") != "" {
		os.Exit(fakeNotify(dest))
	}
	// A drain test exports both fake-CLI variables, and every child process
	// inherits both, so argv is what decides which CLI this invocation is.
	if state := os.Getenv(fakeGhEnv); state != "" && len(os.Args) > 1 && slices.Contains(ghSubcommands, os.Args[1]) {
		os.Exit(fakeGh(state, os.Args[1:]))
	}
	if mode := os.Getenv(fakeClaudeEnv); mode != "" {
		// Here rather than inside fakeClaude, which recurses: a mode that
		// delegates to another must still count as the one invocation it is.
		recordFakeArgs()
		os.Exit(fakeClaude(mode))
	}
	code := m.Run()
	if fakeCLIDir != "" {
		os.RemoveAll(fakeCLIDir)
	}
	os.Exit(code)
}

var (
	fakeCLIOnce sync.Once
	fakeCLIDir  string
	fakeCLIBin  string
	fakeCLIErr  error
)

// fakeCLI is the binary the drain runs as `claude`, as `gh` and as the notify
// command: this same test package, compiled once more without the race
// detector. The child re-enters TestMain and picks its impersonation off the
// POLAKO_FAKE_* variables and argv exactly as before.
//
// Re-executing os.Args[0] would say the same thing in one word, and did. But
// under `go test -race` os.Args[0] is race-instrumented, and a race-instrumented
// binary burns about a second in runtime startup before it reaches main — an
// empty one measures the same. The suite makes some 300 fake-CLI calls, so that
// second was most of the race step's wall clock, and every drain test added
// bought another. What is given up is race coverage of a test fixture: each
// child is a single-purpose emitter, the concurrency the detector exists for
// lives in the parent drain loop, which is still built with -race, and neither
// real `gh` nor real `claude` is an instrumented Go binary either.
func fakeCLI(t *testing.T) string {
	t.Helper()
	fakeCLIOnce.Do(buildFakeCLI)
	if fakeCLIErr != nil {
		t.Fatal(fakeCLIErr)
	}
	return fakeCLIBin
}

// buildFakeCLI compiles the test binary rather than running it. Nothing is
// fetched and nothing but the standard library is linked, so the suite stays as
// hermetic as it was; what it now needs is the Go toolchain that is already
// running it.
func buildFakeCLI() {
	dir, err := os.MkdirTemp("", "polako-fake-cli")
	if err != nil {
		fakeCLIErr = fmt.Errorf("fake CLI: %v", err)
		return
	}
	bin := filepath.Join(dir, "fake-cli")
	if runtime.GOOS == "windows" {
		bin += ".exe" // or exec refuses to run it
	}
	// -race=false spelled out rather than merely omitted: a GOFLAGS carrying
	// -race would otherwise put the instrumentation straight back, and silently
	// — the suite would still pass, five minutes slower. -vet=off because `go
	// test` vets by default and check.sh and CI both run `go vet` as a step of
	// their own; vetting this package twice per run lengthens the build for
	// nothing.
	out, err := exec.Command("go", "test", "-race=false", "-vet=off", "-c", "-o", bin, ".").CombinedOutput()
	if err != nil {
		os.RemoveAll(dir)
		fakeCLIErr = fmt.Errorf("fake CLI: building it needs a working `go` on PATH: %v\n%s", err, out)
		return
	}
	fakeCLIDir, fakeCLIBin = dir, bin

	// The first exec of a binary just written to disk pays for paging it in —
	// some 200ms here against the ~10ms every exec after it costs. That is
	// invisible in the full suite, where whichever test goes first is untimed;
	// under a `-run` filter it lands inside the first watchdog window instead
	// and kills the child before its init event is ever scanned (issue #109).
	// Paying it here, once, keeps it out of every timed test. Best-effort: a
	// binary that cannot run has a real exec along shortly to say so.
	//
	// The environment is built rather than inherited, because which seam the
	// child impersonates comes off these variables and the caller has already
	// set some: mode last is not enough if a future one dispatches earlier.
	//
	// Bounded because best-effort has to include the child that never returns:
	// a dispatch that stopped recognising "warmup" falls through to m.Run and
	// runs the whole suite in here, output discarded, and the suite reads as
	// hung inside whichever test happened to build the fake CLI first.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	warm := exec.CommandContext(ctx, bin)
	warm.Env = append(slices.DeleteFunc(os.Environ(), func(kv string) bool {
		return strings.HasPrefix(kv, "POLAKO_")
	}), fakeClaudeEnv+"=warmup")
	_ = warm.Run()
}

// fakeUsageEnv picks which `/usage` fixture fakeClaude answers with. Unset
// means "no such command" — an old CLI without /usage — which is also why
// every existing stream-mode test is unaffected by this dispatch existing
// at all.
const fakeUsageEnv = "POLAKO_FAKE_USAGE"

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
			`{"id":"polako@scharissis","version":"` + v + `","scope":"user","enabled":true}]`)
		return 0
	}
	// probeUsage's call — `claude -p "/usage" --output-format json` — is a
	// different call on the same binary too, dispatched on argv for the same
	// reason plugin list is: it never goes through execClaude, so no mode a
	// drain test picks selects it.
	if slices.Contains(os.Args, "-p") && slices.Contains(os.Args, "/usage") {
		return fakeUsageProbe()
	}
	switch mode {
	case "warmup":
		// buildFakeCLI's throwaway first exec. It exists to be a process, not
		// to say anything — but it still has to be a mode, because a child with
		// no POLAKO_FAKE_* variable set runs the whole suite over again.
		return 0
	case "stream":
		// The init event carries the session's command inventory (2.1.85+);
		// both spellings of the skill are listed so the healthy path proves
		// the missing-skill tripwire stays quiet when the command exists.
		emit(`{"type":"system","subtype":"init","session_id":"sess-xyz","model":"claude-opus-5",` +
			`"slash_commands":["compact","context","cost","polako:implement-issue","implement-issue"]}`)
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
	case "envcanary":
		// Reports what the parent's environment looked like from in here.
		// stderr rather than an event, because the narration carries it
		// verbatim and no event type exists to hold it.
		fmt.Fprintf(os.Stderr, "canary=%s\n", os.Getenv(envCanaryVar))
		return fakeClaude("stream")
	case "envcanaryout":
		// The same canary standing in for gh and git rather than for claude:
		// capture() runs those for their stdout and throws stderr away on
		// success, and no stream-json is expected of them.
		emit("canary=" + os.Getenv(envCanaryVar))
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
	case "deathrattle":
		// Issue #166's shape: a --resume the CLI kills on arrival still
		// emits one assistant event before it dies — an empty usage
		// block, no tool use — which used to be enough to count as
		// progress and reset the -retries budget every single attempt.
		// Every invocation looks like this, fresh or resumed, so the
		// loop never finds a fruitful one.
		emit(`{"type":"system","subtype":"init","session_id":"sess-death","model":"claude-opus-5"}`)
		emit(`{"type":"assistant","session_id":"sess-death","message":{"content":[],"usage":{}}}`)
		return 1
	case "deathrattlemixed":
		// One resume in the middle is a clean exit with real work left on
		// disk — the fresh crash before it and the crashes after it are
		// death rattles. Whether the resume ceiling's park sentence
		// credits that middle run is exactly what this shape tests: the
		// clean-exit resume counts against the same resumes counter the
		// crash arm's ceiling reads, so it must count as progress too.
		n, err := countClaudeRun()
		if err != nil {
			fmt.Fprintf(os.Stderr, "fake claude: %v\n", err)
			return 1
		}
		if n == 2 {
			return fakeClaude("stream")
		}
		return fakeClaude("crash")
	case "costlycrash":
		// Reported what it spent and then died anyway. The combination is what
		// a cost cap needs to be exercised end to end: a run that leaves a bill
		// behind and a supervisor that would otherwise resume it.
		emit(`{"type":"system","subtype":"init","session_id":"sess-costly","model":"claude-opus-5"}`)
		emit(`{"type":"result","subtype":"success","session_id":"sess-costly","duration_ms":1000,` +
			`"num_turns":4,"total_cost_usd":9,"usage":{"input_tokens":5,"output_tokens":6}}`)
		return 7
	case "crashthenships":
		// Dies on the fresh attempt and finishes the job on the resume — the
		// half of the retry decision no run in the suite performed. Which run
		// this is comes off argv rather than off the pretend repository,
		// because what it is there to prove is that the supervisor resumed a
		// session instead of starting one over.
		if !slices.Contains(os.Args, "--resume") {
			return fakeClaude("crash")
		}
		if err := plantPR("MERGED"); err != nil {
			fmt.Fprintf(os.Stderr, "fake claude: %v\n", err)
			return 1
		}
		return fakeClaude("stream")
	case "crashthenwaits":
		// One of each, so the two resume budgets can be proved to be one budget:
		// the fresh run dies, and the resume it is granted ends its turn cleanly
		// with the work still uncommitted.
		if !slices.Contains(os.Args, "--resume") {
			return fakeClaude("crash")
		}
		return fakeClaude("stream")
	case "limited":
		// The CLI refusing to work over the account's usage limit: an init
		// that names a session, the refusal streamed as the only turn, and an
		// error result whose text is the refusal — the shape observed on #67.
		// Deliberately no reset clause the parser can read, because a drain
		// test on this mode exercises the poll fallback; a readable clock
		// would have the suite sleeping into real wall time.
		emit(`{"type":"system","subtype":"init","session_id":"sess-limited","model":"claude-opus-5"}`)
		emit(`{"type":"assistant","session_id":"sess-limited","message":{"content":[{"type":"text","text":"You've hit your session limit"}]}}`)
		emit(`{"type":"result","subtype":"error_during_execution","is_error":true,"session_id":"sess-limited",` +
			`"num_turns":1,"duration_ms":100,"total_cost_usd":0,"result":"You've hit your session limit"}`)
		return 1
	case "limitedthenships":
		// Refused over the limit once, and the resume after the wait finishes
		// the job. Which run this is comes off argv, because what it proves is
		// that the supervisor resumed the refused session rather than parking
		// its issue or starting over.
		if !slices.Contains(os.Args, "--resume") {
			return fakeClaude("limited")
		}
		if err := plantPR("MERGED"); err != nil {
			fmt.Fprintf(os.Stderr, "fake claude: %v\n", err)
			return 1
		}
		return fakeClaude("stream")
	case "limitedrepeatthenships":
		// Refused over the limit more times than -retries would forgive, then
		// ships — the proof that limit waits are charged to neither retry
		// budget. Counted off the pretend repository, because every refusal
		// after the first arrives on a --resume and argv cannot tell them
		// apart.
		n, err := countClaudeRun()
		if err != nil {
			fmt.Fprintf(os.Stderr, "fake claude: %v\n", err)
			return 1
		}
		if n <= 5 {
			return fakeClaude("limited")
		}
		if err := plantPR("MERGED"); err != nil {
			fmt.Fprintf(os.Stderr, "fake claude: %v\n", err)
			return 1
		}
		return fakeClaude("stream")
	case "waitsthenships":
		// Issue #42's shape: the fresh run implements the change, ends its turn
		// believing something will bring it back, and so exits cleanly with no
		// PR — and the resume is what actually opens one. Which run this is
		// comes off argv rather than off the pretend repository, because what
		// it is there to prove is that a *resume* happened at all.
		if !slices.Contains(os.Args, "--resume") {
			return fakeClaude("stream")
		}
		if err := plantPR("MERGED"); err != nil {
			fmt.Fprintf(os.Stderr, "fake claude: %v\n", err)
			return 1
		}
		return fakeClaude("stream")
	case "deadsession":
		// A --resume the CLI cannot honour: the session's JSONL was truncated by
		// a hard kill mid-append, or it has aged out of retention. It says so on
		// stderr and exits without emitting a single event — no init, so nothing
		// ever started, which is the whole tell the supervisor has.
		if slices.Contains(os.Args, "--resume") {
			fmt.Fprintln(os.Stderr, "No conversation found with the given session ID")
			return 1
		}
		// Which fresh run this is has to come off the pretend repository: the
		// first one dies leaving a session behind, and the fallback the
		// supervisor is supposed to reach is the one that finishes the job.
		n, err := countClaudeRun()
		if err != nil {
			fmt.Fprintf(os.Stderr, "fake claude: %v\n", err)
			return 1
		}
		if n == 1 {
			return fakeClaude("crash")
		}
		if err := plantPR("MERGED"); err != nil {
			fmt.Fprintf(os.Stderr, "fake claude: %v\n", err)
			return 1
		}
		return fakeClaude("stream")
	case "giant":
		// One event past the reader's ceiling. The write blocks as soon as the
		// pipe fills, which is the whole failure: nothing drains it any more, so
		// a supervisor that waits on this process waits forever.
		emit(`{"type":"system","subtype":"init","session_id":"sess-giant","model":"claude-opus-5"}`)
		fmt.Fprint(os.Stdout, `{"type":"assistant","session_id":"sess-giant","message":{"content":[{"type":"text","text":"`)
		chunk := strings.Repeat("x", 1<<16)
		for written := 0; written < maxEventBytes+len(chunk); written += len(chunk) {
			fmt.Fprint(os.Stdout, chunk)
		}
		emit(`"}]}}`)
		return 0
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
			`"num_turns":2,"total_cost_usd":0,"result":"Unknown skill: polako:implement-issue"}`)
		// Both lines are already in the pipe; linger so the supervisor's
		// deliberate kill — not this process's own exit — ends the run, and
		// tests observe the killed path deterministically instead of racing.
		time.Sleep(500 * time.Millisecond)
		return 0
	case "permissionblocked":
		// Issue #138's shape: a clean, successful exit — is_error absent, a
		// real turn count — whose only turn ended by asking the operator to
		// approve a tool --allowedTools never granted. Nothing else on the
		// stream says so; only the result text does.
		emit(`{"type":"system","subtype":"init","session_id":"sess-blocked","model":"claude-opus-5"}`)
		emit(`{"type":"assistant","session_id":"sess-blocked","message":{"content":[` +
			`{"type":"tool_use","name":"EnterWorktree","input":{}}]}}`)
		emit(`{"type":"result","subtype":"success","session_id":"sess-blocked","duration_ms":100,` +
			`"num_turns":2,"total_cost_usd":0.1,"result":"This requires user confirmation to enter the worktree. Can you approve?"}`)
		return 0
	case "permissionmidrun":
		// Issue #182 / #169: the ask lands in a turn partway through, and the
		// run then ends on a sentence the head anchor cannot match. Same clean
		// exit as "permissionblocked" — no PR, nothing on disk — only the tell
		// has moved off the result event onto an earlier turn.
		emit(`{"type":"system","subtype":"init","session_id":"sess-midrun","model":"claude-opus-5"}`)
		emit(`{"type":"assistant","session_id":"sess-midrun","message":{"content":[` +
			`{"type":"text","text":"This requires user confirmation to proceed. I'll wait for approval before continuing."}]}}`)
		emit(`{"type":"result","subtype":"success","session_id":"sess-midrun","duration_ms":100,` +
			`"num_turns":3,"total_cost_usd":0.2,"result":"The sandbox restricts Bash to the launch directory, so I need the EnterWorktree tool to move into the worktree."}`)
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
	case "asksthenauth":
		// Asks on the first run, and is refused credentials on the run
		// dispatched once the answer lands. That second run is the one exit
		// after an issue is picked back up that is neither a merge nor a park,
		// and so the only one that can leave the drain's summary describing a
		// question nobody is waiting on any more.
		flagged, err := issueFlagged()
		if err != nil {
			fmt.Fprintf(os.Stderr, "fake claude: %v\n", err)
			return 1
		}
		if flagged {
			return fakeClaude("authfail")
		}
		if err := fakeSkillEffect("asks"); err != nil {
			fmt.Fprintf(os.Stderr, "fake claude: %v\n", err)
			return 1
		}
		return fakeClaude("stream")
	case "asks", "noisy", "askscrash", "asksbot":
		// A run that leaves something behind on the thread. Both then stream
		// like a healthy one, because the supervisor's whole reading of what
		// happened comes from GitHub afterwards, not from the events.
		if err := fakeSkillEffect(mode); err != nil {
			fmt.Fprintf(os.Stderr, "fake claude: %v\n", err)
			return 1
		}
		return fakeClaude("stream")
	case "fixci":
		// A CI remediation that found the cause and pushed.
		if err := fakeCIFix(); err != nil {
			fmt.Fprintf(os.Stderr, "fake claude: %v\n", err)
			return 1
		}
		return fakeClaude("stream")
	case "fixreview":
		// A review remediation that made the changes and pushed.
		if err := fakeReviewFix(); err != nil {
			fmt.Fprintf(os.Stderr, "fake claude: %v\n", err)
			return 1
		}
		return fakeClaude("stream")
	}
	fmt.Fprintf(os.Stderr, "unknown fake claude mode %q\n", mode)
	return 2
}

// fakeUsageProbe answers `claude -p "/usage" --output-format json` per
// fakeUsageEnv: "sub" is usageSample, the full subscription output quoted
// verbatim in issue #138's own body (usage_test.go reuses the same constant
// so the fixture and the parser tests can never drift apart), and "timeout"
// sleeps well past any sane usageTimeout so it is the caller's own bound
// that ends the call rather than this. Unset exits 1 — a CLI too old to have
// /usage — which is also why every stream-mode test is unaffected by this
// dispatch existing at all. The other shapes parseUsage has to tolerate
// (no-subscription prose, a wording change, a partially-readable payload)
// are exercised directly against parseUsage in usage_test.go, with no need
// for a subprocess round trip.
func fakeUsageProbe() int {
	var result string
	switch os.Getenv(fakeUsageEnv) {
	case "":
		return 1
	case "sub":
		result = usageSample
	case "timeout":
		time.Sleep(5 * time.Second)
		result = usageSample
	}
	out, err := json.Marshal(map[string]any{"result": result, "is_error": false})
	if err != nil {
		fmt.Fprintf(os.Stderr, "fake claude: %v\n", err)
		return 1
	}
	fmt.Fprintln(os.Stdout, string(out))
	return 0
}

// recordFakeArgs appends this invocation's argv to the file fakeArgsLogEnv
// names, if a test asked for one. Best-effort and silent: a fake CLI that
// cannot write its own bookkeeping should fail the assertion that reads it,
// not the run it is impersonating.
func recordFakeArgs() {
	path := os.Getenv(fakeArgsLogEnv)
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintln(f, strings.Join(os.Args[1:], " "))
}

// watchClaudeArgs points the fake CLI at a fresh log and returns the reader for
// it: one string per invocation the supervisor made, in order.
func watchClaudeArgs(t *testing.T) func() []string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "claude-args.log")
	t.Setenv(fakeArgsLogEnv, path)
	return func() []string {
		b, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			t.Fatalf("reading the fake CLI's argv log: %v", err)
		}
		return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	}
}

// errFakeCrash is a simulated death, not a broken harness: the run exits
// nonzero having changed nothing on the pretend repository.
var errFakeCrash = errors.New("died before it could clear the flag")

// fakeSkillEffect is what a run leaves on the pretend repository, written
// through the same fake gh the supervisor talks to — so a label the repository
// never declared is refused here exactly as it would be on GitHub.
//
// It reads which run this is off the state itself rather than a counter, so one
// mode answers both halves of a question round: the run that asks, and the
// re-run that finds the answer.
func fakeSkillEffect(mode string) error {
	path := os.Getenv(fakeGhEnv)
	st, err := readGhState(path)
	if err != nil {
		return err
	}
	n := promptIssue()
	is := st.Issues[n]
	if is == nil {
		return fmt.Errorf("no issue #%q to work on", n)
	}
	call := func(args ...string) error {
		if _, _, code := answerGh(st, append([]string{"issue"}, args...)); code != 0 {
			return fmt.Errorf("fake gh refused %v", args)
		}
		return nil
	}
	switch {
	case mode == "noisy":
		// CI, a bot, a linked-PR notice: a comment the run did not write, and
		// one nobody is waiting for an answer to.
		err = call("comment", n, "--body", "Build #1234 passed.")
	case mode == "asksbot" && !slices.Contains(is.Labels, awaitingAnswerLabel):
		// Asks the same question, but the thread then moves twice while the
		// supervisor polls: CI comments first, and only later the person who was
		// actually asked. Far enough apart that a poll falls between them.
		if err = call("comment", n, "--body", "Which of the two should it do?"); err == nil {
			if err = call("edit", n, "--add-label", awaitingAnswerLabel); err == nil {
				is.BotOnRead, is.ReplyOnRead = 2, 4
			}
		}
	case slices.Contains(is.Labels, awaitingAnswerLabel) && mode == "askscrash":
		// Dispatched to fold the answer in and dies on the way, leaving the
		// asking run's flag standing.
		return errFakeCrash
	case slices.Contains(is.Labels, awaitingAnswerLabel):
		// The question has been answered, so this run folds it in and ships.
		plantPRIn(st, n, "MERGED")
		err = call("edit", n, "--remove-label", awaitingAnswerLabel)
	default:
		if err = call("comment", n, "--body", "Which of the two should it do?"); err == nil {
			if err = call("edit", n, "--add-label", awaitingAnswerLabel); err == nil {
				is.ReplyOnRead = 2 // a human answers while the supervisor is polling
			}
		}
	}
	if err != nil {
		return err
	}
	return writeGhState(path, st)
}

// plantPRIn is the PR a finished run would have opened on the issue's branch.
// `gh pr create` is not in the fake gh's repertoire, and nothing downstream
// reads more than that the branch has one, in the state given.
func plantPRIn(st *ghState, issue, state string) {
	if st.PRs == nil {
		st.PRs = map[string]*fakePR{}
	}
	st.PRs["issue-"+issue] = &fakePR{Number: 42, State: state}
}

// countClaudeRun records that another claude invocation happened and reports
// which one this is. A fake CLI whose answer depends on how far the supervisor
// has got needs a counter, and the pretend repository is the one scratchpad
// every invocation can see — each of them is a separate process.
func countClaudeRun() (int, error) {
	path := os.Getenv(fakeGhEnv)
	st, err := readGhState(path)
	if err != nil {
		return 0, err
	}
	st.ClaudeRuns++
	return st.ClaudeRuns, writeGhState(path, st)
}

// plantPR is the same against the state file, for a fake CLI that is not
// already holding it open — it reads and writes rather than mutating in place.
func plantPR(state string) error {
	path := os.Getenv(fakeGhEnv)
	st, err := readGhState(path)
	if err != nil {
		return err
	}
	n := promptIssue()
	if st.Issues[n] == nil {
		return fmt.Errorf("no issue #%q to work on", n)
	}
	plantPRIn(st, n, state)
	return writeGhState(path, st)
}

// fakeCIFix stands in for a remediation run that pushed: the branch head moves
// and the re-run of CI comes back green, which is the whole of what such a run
// changes from `pr view`'s side. It fixes every red PR rather than the one this
// invocation was dispatched for, because the remediation prompt is prose and
// carries no issue number promptIssue could pick out — and the tests that use
// this mode have one PR.
func fakeCIFix() error {
	path := os.Getenv(fakeGhEnv)
	st, err := readGhState(path)
	if err != nil {
		return err
	}
	for _, pr := range st.PRs {
		if !slices.Contains(pr.Checks, "FAILURE") {
			continue
		}
		pr.Head += "+" // the push
		pr.Checks = []string{"SUCCESS"}
	}
	return writeGhState(path, st)
}

// fakeReviewFix stands in for a review remediation that pushed: the branch head
// moves and its newest commit is now younger than the review, which is the
// whole of what such a run changes from `pr view`'s side. Like fakeCIFix it
// fixes every PR with a review outstanding rather than the one this invocation
// was dispatched for, because the prompt is prose carrying no issue number
// promptIssue could pick out — and the tests using it have one PR.
func fakeReviewFix() error {
	path := os.Getenv(fakeGhEnv)
	st, err := readGhState(path)
	if err != nil {
		return err
	}
	for _, pr := range st.PRs {
		if !slices.ContainsFunc(pr.Reviews, func(r fakeReview) bool {
			return r.State == reviewChangesRequested
		}) {
			continue
		}
		pr.Head += "+"                          // the push
		pr.CommittedAt = "2026-08-20T12:00:00Z" // after every review the tests write
	}
	return writeGhState(path, st)
}

// issueFlagged reports whether the issue this invocation was dispatched for is
// already carrying awaitingAnswerLabel — the same reading fakeSkillEffect uses
// to tell the run that asks a question from the one dispatched once it was
// answered, for a mode that has to answer differently before the run rather
// than after it.
func issueFlagged() (bool, error) {
	st, err := readGhState(os.Getenv(fakeGhEnv))
	if err != nil {
		return false, err
	}
	is := st.Issues[promptIssue()]
	if is == nil {
		return false, fmt.Errorf("no issue #%q to work on", promptIssue())
	}
	return slices.Contains(is.Labels, awaitingAnswerLabel), nil
}

// promptIssue is the issue number this invocation was dispatched for, taken
// from the prompt the supervisor built. The last number rather than the last
// word, because a resume is dispatched as a paragraph of plain English with the
// "/implement-issue 1" buried in its first sentence — see resumePrompt — rather
// than as "/implement-issue 1".
func promptIssue() string {
	i := slices.Index(os.Args, "-p")
	if i < 0 || i+1 >= len(os.Args) {
		return ""
	}
	return lastNumber(os.Args[i+1])
}

// lastNumber is that reading on its own, so the test guarding the resume
// prompt's wording checks the same thing the fake CLI will do with it rather
// than a restatement of it.
func lastNumber(prompt string) string {
	n := ""
	for _, f := range strings.Fields(prompt) {
		if _, err := strconv.Atoi(f); err == nil {
			n = f
		}
	}
	return n
}

// captureLog redirects both narration loggers into one buffer and returns it.
// The union is the shift-log view: these tests pin what happened, not how the
// terminal chose to present it, so a line moving between channels breaks
// nothing here. Presentation has its own tests in ui_test.go.
//
// Wired through a ui rather than pointing both loggers at the buffer, because
// the buffer needs what production has: one mutex ordering the two loggers'
// writers — the stderr copier goroutine writes detail while the scan loop and
// the watchdogs write log, and two loggers' own locks do not know each other.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	wireSinks(t, &ui{terminal: io.Discard, file: &buf})
	return &buf
}

// Only SIGINT used to cancel the run. A SIGTERM — a machine shutting down, a
// service manager stopping the unit, a plain pkill — killed the supervisor
// outright, so the context never cancelled and exec.CommandContext never killed
// the child. That orphan keeps acceptEdits and the whole --allowedTools set: it
// can go on editing, commit, push the branch and open a PR, and a restarted
// drain sees no PR yet and starts a second run on the same issue.
func TestShutdownSignalsCoverMoreThanCtrlC(t *testing.T) {
	got := shutdownSignals()
	for _, want := range []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP} {
		if !slices.Contains(got, want) {
			t.Errorf("shutdownSignals() = %v, want it to carry %v", got, want)
		}
	}
}

// Nothing fails if the children go back to being os.Args[0] — they behave
// identically, they just cost a race-runtime startup each, and the suite starts
// some 300 of them. A silent regression from tens of seconds to five minutes of
// CI is exactly what nothing else here would catch.
//
// It is asserted on the seams rather than on fakeCLI's return value, because
// that is where the regression would arrive: a drain test written with
// os.Args[0], or one of these constructors reverted. fakeCLI hands back a path
// under os.MkdirTemp, which cannot equal the running binary, so asking it is
// asking a question that answers itself.
func TestFakeCLIIsBuiltRatherThanReExecuted(t *testing.T) {
	cfg, _ := drainConfig(t, "stream", &ghState{})
	notifyLog(t, &cfg)
	standalone := fakeClaudeConfig(t, "stream")

	for _, seam := range []struct{ name, bin string }{
		{"drainConfig claudeBin", cfg.claudeBin},
		{"drainConfig ghBin", cfg.ghBin},
		{"notifyLog notifyCmd", strings.Trim(cfg.notifyCmd, `"`)},
		{"fakeClaudeConfig claudeBin", standalone.claudeBin},
	} {
		if seam.bin == os.Args[0] {
			t.Errorf("%s is os.Args[0]; it has to be the non-race build fakeCLI returns. "+
				"Under `go test -race` re-executing this binary spends ~1s in runtime "+
				"startup per invocation, and the suite makes some 300 of them", seam.name)
		}
	}
}

func TestLogEventRendersProgressLines(t *testing.T) {
	buf := captureLog(t)

	events := []string{
		// No session_id: the line predates the field and still has to render,
		// since a CLI that reports none must not lose its progress line.
		`{"type":"system","subtype":"init","model":"claude-opus-5"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Gathering context on issue #48.\nStarting now."}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"gh issue view 48 --json body,comments"}}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{"file_path":"PLAN.md","content":"..."}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","content":"ignored"}]}}`,
		`not even json`,
		`{"type":"result","subtype":"success","duration_ms":1141000,"num_turns":74,"total_cost_usd":4.12,"is_error":false,` +
			`"result":"Unknown skill: polako:implement-issue"}`,
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
		"Unknown skill: polako:implement-issue",
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

// The session id is the one handle that reopens a run in full, and the stream
// is the only place it is ever announced. A run whose line does not carry it
// cannot be found again, however completely it was recorded.
func TestLogEventNamesTheSession(t *testing.T) {
	buf := captureLog(t)
	ev, ok := parseEvent([]byte(
		`{"type":"system","subtype":"init","model":"claude-opus-5","session_id":"0f8c1e22-6b4d-4a01-9c3e-2d5f77a1b0e9"}`))
	if !ok {
		t.Fatal("init event should parse")
	}
	logEvent(ev)
	if want := "session started (model claude-opus-5, session 0f8c1e22-6b4d-4a01-9c3e-2d5f77a1b0e9)"; !strings.Contains(buf.String(), want) {
		t.Errorf("output missing %q\ngot:\n%s", want, buf.String())
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
		"Bash(gh run:*)":   "gh run rerun, gh run cancel and gh run delete — a CI remediation only reads",
	} {
		if slices.Contains(have, entry) {
			t.Errorf("defaultTools grants %s, which permits %s; grant the subcommands the "+
				"skill needs and leave -add-tools as the escape hatch for projects that need more",
				entry, why)
		}
	}
}

// A CI remediation reads the failing job logs, and a gh call that raises a
// prompt hangs an unattended run silently.
func TestDefaultToolsCoverDiagnosingARedBuild(t *testing.T) {
	have := strings.Split(defaultTools, ",")
	for _, want := range []string{"Bash(gh pr checks:*)", "Bash(gh run list:*)", "Bash(gh run view:*)"} {
		if !slices.Contains(have, want) {
			t.Errorf("defaultTools is missing %q, so remediateChecks cannot read why CI failed", want)
		}
	}
}

// The one label command the skill needs is granted per run, pinned to the issue
// that run was dispatched for. In defaultTools it would have to be
// `Bash(gh issue edit:*)`, which reaches every other issue in the repository.
func TestIssueLabelToolsStayPinnedToOneIssue(t *testing.T) {
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

// The one read gh has no subcommand for, granted per run and pinned to the PR
// that run was dispatched for. `Bash(gh api:*)` in defaultTools would hand a
// prompt built out of attacker-supplied review text the whole GitHub API.
func TestPRReviewToolsStayPinnedToOnePR(t *testing.T) {
	got := prReviewTools("acme/widgets", 42)
	if want := "Bash(gh api repos/acme/widgets/pulls/42/comments:*)"; got != want {
		t.Errorf("prReviewTools = %q, want %q", got, want)
	}
	if strings.Contains(defaultTools, "gh api") {
		t.Error("defaultTools grants gh api; it belongs in prReviewTools, where it is " +
			"bounded to the comments of the PR the run is already working on")
	}
	// The pinned entry has to survive alongside an operator's own -add-tools,
	// since that is how it reaches the run.
	merged := resolveTools(defaultTools, resolveTools("Bash(bazel:*)", got))
	for _, want := range []string{"Bash(bazel:*)", got} {
		if !strings.Contains(merged, want) {
			t.Errorf("resolved allowlist is missing %q\ngot: %s", want, merged)
		}
	}
}

// Whether a review is still owed an answer is decided from one `pr view`
// payload alone, so that a drain restarted mid-flight reaches the same verdict
// as the one that dispatched the run.
func TestReviewOutstandingReadsOnePRView(t *testing.T) {
	const (
		old    = "2026-08-19T10:00:00Z"
		newer  = "2026-08-20T10:00:00Z"
		newest = "2026-08-21T10:00:00Z"
	)
	// payload renders the review half of `pr view` the way the real thing does:
	// every review on the PR, oldest first.
	payload := func(decision, reviews, commits string) []byte {
		return []byte(fmt.Sprintf(
			`{"state":"OPEN","mergeable":"MERGEABLE","headRefOid":"abc","statusCheckRollup":[],`+
				`"reviewDecision":%q,"reviews":[%s],"commits":[%s]}`, decision, reviews, commits))
	}
	review := func(author, state, at string) string {
		return fmt.Sprintf(`{"author":{"login":%q},"state":%q,"submittedAt":%q}`, author, state, at)
	}
	commit := func(at string) string { return fmt.Sprintf(`{"committedDate":%q}`, at) }

	cases := []struct {
		name string
		raw  []byte
		want bool
		note string
	}{{
		// The case the whole feature exists for, and the one this repository
		// itself produces: no branch protection, so reviewDecision is empty and
		// the review has to carry the verdict.
		name: "changes requested after the last commit",
		raw:  payload("", review("ann", reviewChangesRequested, newer), commit(old)),
		want: true,
	}, {
		name: "the branch moved after the review",
		raw:  payload("", review("ann", reviewChangesRequested, old), commit(newer)),
		want: false,
		note: ", changes requested and answered — waiting on a re-review",
	}, {
		// Only each reviewer's newest verdict stands, so a reviewer who asked
		// for changes and then approved is no longer in the way.
		name: "the reviewer came back and approved",
		raw: payload("APPROVED", review("ann", reviewChangesRequested, newer)+","+
			review("ann", reviewApproved, newest), commit(old)),
		want: false,
	}, {
		// The bug the `reviews` field exists to avoid: an ordinary comment is
		// not a verdict, so it cannot clear the request for changes under it.
		// gh's `latestReviews` would report this reviewer as COMMENTED and the
		// supervisor would go back to waiting for a merge nobody will perform.
		name: "a comment left after the request for changes",
		raw: payload("", review("ann", reviewChangesRequested, newer)+","+
			review("ann", "COMMENTED", newest), commit(old)),
		want: true,
	}, {
		// A dismissal is a verdict, and it is the one that stands.
		name: "the request for changes was dismissed",
		raw: payload("", review("ann", reviewChangesRequested, newer)+","+
			review("ann", reviewDismissed, newest), commit(old)),
		want: false,
	}, {
		name: "one approval does not cancel another reviewer's changes",
		raw: payload("", review("ann", reviewApproved, newer)+","+
			review("bob", reviewChangesRequested, newer), commit(old)),
		want: true,
	}, {
		// A repository that does require reviews says so here even when its
		// individual verdicts have been superseded. There is no date to hold the
		// branch against, so this waits on a person rather than guessing.
		name: "reviewDecision alone, with no review to date it",
		raw:  payload(reviewChangesRequested, "", commit(old)),
		want: false,
		note: ", changes requested",
	}, {
		// Nothing to chase and nothing to say: the ordinary open PR.
		name: "no reviews at all",
		raw:  payload("", "", commit(old)),
		want: false,
	}, {
		// An unreadable date must not be read as "reviewed at the epoch", which
		// would make every branch look newer than every review.
		name: "an unparseable review date",
		raw:  payload("", review("ann", reviewChangesRequested, "not a date"), commit(old)),
		want: false,
		note: ", changes requested",
	}, {
		// The mirror image: an unreadable commit date must not let a stale review
		// look outstanding forever, but it cannot show the branch moved either.
		name: "an unparseable commit date",
		raw:  payload("", review("ann", reviewChangesRequested, newer), commit("not a date")),
		want: true,
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pr, err := parsePRStatus(tc.raw)
			if err != nil {
				t.Fatalf("parsePRStatus: %v", err)
			}
			if got := pr.reviewOutstanding(); got != tc.want {
				t.Errorf("reviewOutstanding = %v, want %v", got, tc.want)
			}
			if got := pr.reviewNote(); got != tc.note {
				t.Errorf("reviewNote = %q, want %q", got, tc.note)
			}
		})
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

// The rollup is the one GitHub answer the supervisor reduces itself, and every
// verdict costs something to get wrong: a false "failing" spends a Claude run
// on healthy code, a false "passing" is the silent forever-wait issue #5 is
// about. Both node shapes GitHub can put in the array are exercised, because
// the CheckRun/StatusContext split is decoded by which fields came back empty.
func TestClassifyChecks(t *testing.T) {
	run := func(status, conclusion, name string) checkNode {
		return checkNode{Name: name, Status: status, Conclusion: conclusion}
	}
	ctxNode := func(state, context string) checkNode {
		return checkNode{Context: context, State: state}
	}
	cases := []struct {
		name    string
		nodes   []checkNode
		want    string
		failing []string
	}{
		{"nothing reported yet", nil, checksNone, nil},
		{"all green", []checkNode{run("COMPLETED", "SUCCESS", "test")}, checksPassing, nil},
		{"one red", []checkNode{
			run("COMPLETED", "SUCCESS", "lint"),
			run("COMPLETED", "FAILURE", "test"),
		}, checksFailing, []string{"test"}},
		{"a legacy status context", []checkNode{ctxNode("FAILURE", "ci/travis")}, checksFailing, []string{"ci/travis"}},
		{"a green status context", []checkNode{ctxNode("SUCCESS", "ci/travis")}, checksPassing, nil},
		{"a queued run", []checkNode{run("QUEUED", "", "test")}, checksPending, nil},
		// Pending outranks failing: the suite can only add to the list, and
		// diagnosing half of one wastes the run.
		{"red while another is still going", []checkNode{
			run("COMPLETED", "FAILURE", "test"),
			run("IN_PROGRESS", "", "build"),
		}, checksPending, nil},
		{"a pending status context", []checkNode{ctxNode("PENDING", "ci/travis")}, checksPending, nil},
		// Nothing a change to the branch can fix, so none of these is a failure
		// worth spending an attempt on — but a cancelled or unapproved check
		// still blocks the merge, so the verdict may not read "passing".
		{"cancelled, skipped and awaiting a human", []checkNode{
			run("COMPLETED", "CANCELLED", "test"),
			run("COMPLETED", "SKIPPED", "deploy"),
			run("COMPLETED", "NEUTRAL", "advisory"),
			run("COMPLETED", "ACTION_REQUIRED", "approve"),
		}, checksHuman, nil},
		{"skipped and neutral alone are green", []checkNode{
			run("COMPLETED", "SKIPPED", "deploy"),
			run("COMPLETED", "NEUTRAL", "advisory"),
		}, checksPassing, nil},
		// A deployment gate never finishes on its own, so it must not be read as
		// a suite still running: that would hide the real failure beside it for
		// as long as nobody approves.
		{"red behind a deployment gate", []checkNode{
			run("COMPLETED", "FAILURE", "test"),
			run("WAITING", "", "deploy"),
		}, checksFailing, []string{"test"}},
		{"only a deployment gate", []checkNode{run("WAITING", "", "deploy")}, checksHuman, nil},
		{"the other ways a build breaks", []checkNode{
			run("COMPLETED", "TIMED_OUT", "slow"),
			run("COMPLETED", "STARTUP_FAILURE", "broken-yaml"),
		}, checksFailing, []string{"slow", "broken-yaml"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, failing := classifyChecks(c.nodes)
			if got != c.want {
				t.Errorf("verdict = %q, want %q", got, c.want)
			}
			if !slices.Equal(failing, c.failing) {
				t.Errorf("failing = %v, want %v", failing, c.failing)
			}
		})
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

// -remote asks for something no CLI delivers in print mode, so the invocation
// must carry nothing for it either way round — issue #82. Both settings are
// pinned because only the pair says what the flag means now: on is not "sends
// the flag", it is "would, once a CLI takes it", and off has to stay identical
// to on for as long as that is true.
func TestBuildArgsNeverAsksForRemoteControl(t *testing.T) {
	off := config{permissionMode: "acceptEdits", tools: "Read", repo: "example/repo"}
	on := off
	on.remote = true

	for _, tc := range []struct {
		name string
		cfg  config
	}{{"-remote on", on}, {"-remote off", off}} {
		if got := buildArgs(tc.cfg, "/implement-issue 52", ""); slices.Contains(got, "--remote-control") {
			t.Errorf("%s: no CLI registers headless runs, so nothing should ask, got %v", tc.name, got)
		}
	}

	// The flag being inert is the whole claim, so pin it as one: -remote must
	// make no difference at all to what claude is invoked with.
	if !slices.Equal(buildArgs(on, "/implement-issue 52", ""), buildArgs(off, "/implement-issue 52", "")) {
		t.Error("-remote=false must invoke claude exactly as -remote does, and neither may register")
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

// The caps are off unless somebody sets one, which is the whole of what keeps
// every existing drain behaving as it did — and each reports in the words the
// park comment and the summary go on to carry.
func TestOverBudgetOnlySpeaksWhenACapIsSet(t *testing.T) {
	spent := issueTally{costUSD: 9, wallMS: (2 * time.Hour).Milliseconds()}

	if got := overBudget(config{}, spent); got != "" {
		t.Errorf("overBudget with no caps = %q, want silence — the default must not park anything", got)
	}
	if got := overBudget(config{maxCost: 20, maxIssueTime: 3 * time.Hour}, spent); got != "" {
		t.Errorf("overBudget under both caps = %q, want silence", got)
	}
	cost := overBudget(config{maxCost: 5}, spent)
	if !strings.Contains(cost, "$9.00") || !strings.Contains(cost, "-max-cost of $5.00") {
		t.Errorf("cost breach = %q, want it to quote what was spent and the cap it broke", cost)
	}
	clock := overBudget(config{maxIssueTime: time.Hour}, spent)
	if !strings.Contains(clock, "2h") || !strings.Contains(clock, "-max-issue-time of 1h") {
		t.Errorf("time breach = %q, want it to quote the run time and the cap it broke", clock)
	}
	// Exactly at the cap counts as spent: a cap of $5 that permits a run once
	// $5 is gone is a cap on nothing.
	if got := overBudget(config{maxCost: 9}, spent); got == "" {
		t.Error("spending the cap exactly must count as reaching it")
	}
}

// The limit handed to a run is what the cap has left, not the cap: an issue on
// its fourth run does not get the whole allowance over again.
func TestRunLimitLeavesOnlyWhatTheIssueHasLeft(t *testing.T) {
	spent := issueTally{wallMS: (20 * time.Minute).Milliseconds()}

	if got := runLimit(config{}, spent); got != 0 {
		t.Errorf("runLimit with the cap off = %v, want 0 for unbounded", got)
	}
	if got := runLimit(config{maxIssueTime: time.Hour}, spent); got != 40*time.Minute {
		t.Errorf("runLimit = %v, want 40m of a 1h cap after 20m of runs", got)
	}
	// Only reachable if a caller skipped overBudget; a limit of zero would read
	// as unbounded, which is the one answer an exhausted issue must not get.
	if got := runLimit(config{maxIssueTime: time.Minute}, spent); got <= 0 {
		t.Errorf("runLimit past the cap = %v, want a positive floor rather than unbounded", got)
	}
}

// A cap set in a shell profile is still a cap, so startup names the ones in
// force rather than leaving a park to quote a flag nobody typed.
func TestCapNotesNameEveryCapInForce(t *testing.T) {
	if got := capNotes(config{}); got != "" {
		t.Errorf("capNotes with no caps = %q, want nothing said", got)
	}
	got := capNotes(config{maxCost: 15, maxIssueTime: 90 * time.Minute, maxSessionCost: 200,
		maxSessionUsage: 80, maxWeekUsage: 90})
	for _, want := range []string{"-max-cost $15.00", "-max-issue-time 1h30m", "-max-session-cost $200.00",
		"-max-session-usage 80%", "-max-week-usage 90%"} {
		if !strings.Contains(got, want) {
			t.Errorf("capNotes = %q, missing %q", got, want)
		}
	}
}

// TestPreflightPairsSaysNothingUnasked pins the gate each row promises: unset
// (the zero-value config, no log path) produces exactly one row — the
// unconditional epics disclosure — and nothing else. Every other row earns its
// keep only when there is something to disclose.
func TestPreflightPairsSaysNothingUnasked(t *testing.T) {
	got := preflightPairs(config{}, "")
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
	cfg := config{
		label:       "ready",
		dryRun:      true,
		maxCost:     15,
		usage:       &usageSnapshot{pools: []usagePool{{name: "session", percent: 10}}},
		postSummary: true,
		notifyCmd:   "notify-send",
		remote:      true,
		rec:         &recorder{dir: "/tmp/metrics"},
		shiftID:     "abc123",
	}
	got := preflightPairs(cfg, "/tmp/logs/shift.log")
	wantLabels := []string{
		"epics", "queue", "dry-run", "caps", "plan", "post-summary", "notify", "remote", "run data", "shift", "shift log",
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
	if !strings.Contains(got[4][1], "session 10%") {
		t.Errorf("plan row = %q, want the usage snapshot rendered", got[4][1])
	}
	if !strings.Contains(got[8][1], "/tmp/metrics") || !strings.Contains(got[9][1], "abc123") {
		t.Errorf("run data/shift rows = %v, want the recorder dir and shift id named", got[8:10])
	}
}

// remediable is what the spend caps ask instead of repeating supervisePR's
// switch, so it has to answer yes to every condition that switch dispatches at
// and no to a PR that is merely waiting. A fourth kind of remediation added
// without a line here is a hole in the budget.
func TestRemediableCoversEveryDispatch(t *testing.T) {
	reviewed := prView{changesRequested: true, reviewedAt: time.Now()}
	for _, tc := range []struct {
		name string
		pr   prView
		want bool
	}{
		{"conflicting", prView{mergeable: "CONFLICTING"}, true},
		{"red checks", prView{checks: checksFailing, failing: []string{"build"}}, true},
		{"review outstanding", reviewed, true},
		{"green and mergeable", prView{mergeable: "MERGEABLE", checks: checksPassing}, false},
		{"checks still running", prView{mergeable: "MERGEABLE", checks: checksPending}, false},
		{"a check stopped on a person", prView{mergeable: "MERGEABLE", checks: checksHuman}, false},
		{"review already answered", prView{changesRequested: true,
			reviewedAt: reviewed.reviewedAt, branchAt: reviewed.reviewedAt.Add(time.Minute)}, false},
	} {
		if got := tc.pr.remediable(); got != tc.want {
			t.Errorf("%s: remediable = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// The cap -stall cannot stand in for: this run is not silent, it just never
// stops. The kill has to read as a deliberate stop rather than as the crash it
// looks like from the exit code, or the supervisor resumes it into the same
// wall.
func TestExecClaudeKillsARunPastItsBudget(t *testing.T) {
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

// The caps gate runs, never waiting: a PR that needs no fixing is free to
// merge however much its issue has already cost, and parking it would hand
// back an issue whose work is finished and sitting on GitHub.
func TestSupervisePRStillWaitsOutAnOverspentPRNobodyHasToFix(t *testing.T) {
	captureLog(t)
	cfg, _ := drainConfig(t, "stream", &ghState{
		Issues: map[string]*fakeIssue{"1": {Open: true}},
		PRs: map[string]*fakePR{"issue-1": {
			Number: 9, State: "OPEN", Mergeable: "MERGEABLE",
			Head: "abc123", Checks: []string{"SUCCESS"}, MergeOnRead: 2,
		}},
	})
	cfg.maxCost = 1
	tally := issueTally{runs: 1, costUSD: 9}

	state, err := supervisePR(context.Background(), cfg, 1, 9, &tally)
	if err != nil {
		t.Fatalf("an overspent issue whose PR is green must still be waited out: %v", err)
	}
	if state != "MERGED" {
		t.Errorf("state = %q, want MERGED", state)
	}
	if tally.runs != 1 {
		t.Errorf("tally.runs = %d, want no run dispatched while waiting", tally.runs)
	}
}

// The other half: a PR that does need fixing is a run this issue can no longer
// afford, so it parks instead of dispatching one.
func TestSupervisePRParksRatherThanRemediateOnAnOverspentIssue(t *testing.T) {
	captureLog(t)
	cfg, _ := drainConfig(t, "fixci", &ghState{
		Issues: map[string]*fakeIssue{"1": {Open: true}},
		PRs: map[string]*fakePR{"issue-1": {
			Number: 9, State: "OPEN", Mergeable: "MERGEABLE",
			Head: "abc123", Checks: []string{"FAILURE"},
		}},
	})
	cfg.maxIssueTime = time.Minute
	tally := issueTally{runs: 1, wallMS: (2 * time.Minute).Milliseconds()}

	_, err := supervisePR(context.Background(), cfg, 1, 9, &tally)
	reason, parked := parkReason(err)
	if !parked {
		t.Fatalf("a red PR on an issue past its cap should park, got %v", err)
	}
	if !strings.Contains(reason, "-max-issue-time") {
		t.Errorf("park reason = %q, want it to name the cap that stopped the remediation", reason)
	}
	if tally.runs != 1 {
		t.Errorf("tally.runs = %d, want the remediation never dispatched", tally.runs)
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
		claudeBin:      fakeCLI(t), // this test package, re-entered via TestMain
		skill:          defaultSkill,
		permissionMode: "acceptEdits",
		tools:          "Read",
		stall:          10 * time.Second,
	}
}

func TestExecClaudeStreamsEventsAndCapturesSession(t *testing.T) {
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

// buildArgs is asserted directly above, but the argv a child actually receives
// is the thing the promise was made about, so pin that end too: a real dispatch
// under -remote must reach the CLI carrying nothing to register with. Issue #82
// is what the pair guards against coming back — a flag reintroduced anywhere
// between buildArgs and exec would pass the unit test and still overpromise.
func TestDispatchNeverSendsRemoteControlToTheCLI(t *testing.T) {
	args := watchClaudeArgs(t)
	cfg := fakeClaudeConfig(t, "stream")
	cfg.remote, cfg.repo = true, "example/repo"

	if _, err := execClaude(context.Background(), cfg, "/implement-issue 7", "", "implement-issue", 0); err != nil {
		t.Fatalf("a healthy run under -remote: %v", err)
	}
	got := args()
	if len(got) != 1 {
		t.Fatalf("want exactly one dispatch — there is nothing left to re-dispatch for — got %v", got)
	}
	if strings.Contains(got[0], "--remote-control") {
		t.Errorf("no CLI registers headless runs, so none should be asked: %s", got[0])
	}
	// The session name went with the flag: no CLI reads it, and leaving it on
	// the command line would be the same false promise one argument along.
	if strings.Contains(got[0], "polako example/repo#") {
		t.Errorf("the session name has no reader left; it should not be passed: %s", got[0])
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

	out, err := capture(context.Background(), t.TempDir(), fakeCLI(t), "status")
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

// An event too large for the reader used to end the scan silently. The child
// then blocked writing into a pipe nobody was draining, cmd.Wait never
// returned, and the run died as a -stall kill a quarter of an hour later —
// reported as a stall, which is the one thing it was not. It has to fail
// straight away, and say what actually happened.
func TestExecClaudeReportsAnEventTooLargeToRead(t *testing.T) {
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
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
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

// The failure both of these guard against: -skill naming a slash command the
// installation does not have. CLIs before 2.1.85 answered with a clean exit
// at zero turns, and without this fallback the supervisor reports only "no PR
// and no questions".
func TestExecClaudeFlagsARunThatTookNoTurns(t *testing.T) {
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
	captureLog(t)
	cfg := fakeClaudeConfig(t, "unknownskill")

	if _, err := execClaude(context.Background(), cfg,
		"PR #3 has merge conflicts with the remote default branch.", "", "", 0); err != nil {
		t.Fatalf("a plain prompt must not trip the missing-skill check, got %v", err)
	}
}

// The near-match hint is what turns "no such command" into a fix, and the
// bare-vs-namespaced confusion is symmetrical, so both directions must hit.
func TestNearMatchesBridgesPluginNamespacing(t *testing.T) {
	inv := []string{"compact", "polako:implement-issue"}
	if got := nearMatches(inv, "implement-issue"); !slices.Equal(got, []string{"/polako:implement-issue"}) {
		t.Errorf("a bare -skill should surface the namespaced spelling, got %v", got)
	}
	if got := nearMatches([]string{"compact", "implement-issue"}, "polako:implement-issue"); !slices.Equal(got, []string{"/implement-issue"}) {
		t.Errorf("a namespaced -skill should surface the bare spelling, got %v", got)
	}
	if got := nearMatches(inv, "something-else"); got != nil {
		t.Errorf("no relative should mean no hint, got %v", got)
	}
}

// A wrong "missing" verdict kills a healthy run, so the check may only fire
// on positive evidence: an inventory that is present and lacks the command.
func TestLacksCommandNeedsPositiveEvidence(t *testing.T) {
	list := []string{"compact", "cost", "polako:implement-issue"}
	if lacksCommand(list, "polako:implement-issue") {
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
		"Unknown skill: polako:implement-issue",
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

// Issue #157: a clean exit used to have its result text read once — for
// authFailed/limitMsg, both gated on IsError — and otherwise dropped, so a
// park could assert "no questions" over a run whose final message was
// verbatim one. observe now classifies it on a success result too, since
// #138's run ended cleanly.
func TestObserveClassifiesACleanExitsFinalText(t *testing.T) {
	const asked = "This requires user confirmation to switch the session's " +
		"working directory into the worktree. Can you approve entering `/tmp/x`?"
	const ordinary = "Opened a PR for issue 7."

	cases := []struct {
		name           string
		result         string
		wantPermission bool
	}{
		{"a permission request", asked, true},
		{"an ordinary result", ordinary, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var rep runReport
			rep.observe(streamEvent{Type: "result", Subtype: "success", Result: c.result})
			if rep.permissionRefused != c.wantPermission {
				t.Errorf("permissionRefused = %v, want %v", rep.permissionRefused, c.wantPermission)
			}
		})
	}
}

// permissionRefusal matches the model's own wording, not a CLI wrapper
// string, so it has no single canonical form the way authFailure's does — but
// it still has to leave the same quoting risk closed: an issue about tool
// permissions that gets quoted back mid-message must not park the issue over
// a run that actually succeeded.
func TestPermissionRefusalMatchesTheWaysARunAsksApproval(t *testing.T) {
	asks := []string{
		"This requires user confirmation to switch the session's working " +
			"directory into the worktree. Can you approve entering `/tmp/x`?",
		"This requires confirmation before I can proceed.",
		"This requires approval to run the migration.",
		"This requires your approval before I continue.",
		"Can you approve granting Bash(cd:*) so I can finish?",
		"Could you approve this tool before I continue?",
		"I need permission to use the EnterWorktree tool.",
		"I don't have permission to run that command.",
		"I do not have permission to write outside the worktree.",
		// A markdown bullet ahead of the signature, the other wrapping
		// resultHead has to see through besides a heading or an asterisk.
		"- This requires approval to write outside the worktree. Can you approve?",
	}
	for _, r := range asks {
		if !permissionRefusal(r) {
			t.Errorf("should read as a permission request: %s", clip(r, 80))
		}
	}
	fine := []string{
		"Opened a PR for issue 7.",
		"Unknown skill: polako:implement-issue",
		// The word-boundary check: a signature is a raw byte prefix of this
		// sentence, but the run asked for nothing — a false match here would
		// park a possibly-salvageable run over sandbox tooling, not a refused
		// tool.
		"I need permission tooling wasn't available in this sandbox, so I " +
			"worked around it and left notes on the branch.",
		// The reason the match is anchored: a run whose issue is *about*
		// permission prompts can legitimately end by describing one without
		// asking for anything itself.
		"Fixed #156, which was about a run that says " +
			`"This requires user confirmation" when cd is not allowlisted. ` +
			"Opened a PR.",
	}
	for _, r := range fine {
		if permissionRefusal(r) {
			t.Errorf("should not read as a permission request: %s", clip(r, 80))
		}
	}
}

// Issue #182: on #169 the run asked for an ungranted tool in a turn partway
// through, then ended on a different sentence the head anchor could not catch,
// and parked as "no PR and no questions". observe now reads every assistant
// turn, not only the result text, so the ask is not lost.
func TestObserveReadsAPermissionAskFromAnEarlierTurn(t *testing.T) {
	turn := func(text string) streamEvent {
		ev, ok := parseEvent([]byte(`{"type":"assistant","message":{"content":[{"type":"text","text":` +
			strconv.Quote(text) + `}]}}`))
		if !ok {
			t.Fatalf("could not build assistant turn: %q", text)
		}
		return ev
	}

	// #169's actual final text, verbatim so the wording that slipped through
	// cannot regress: on its own it still does not read as a permission stop —
	// the ask is not at the head, and the head anchor is right to miss it.
	const finalText = "The sandbox restricts Bash to the launch directory, so I " +
		"need the `EnterWorktree` tool to move into `/Users/stef/code/polako-issue-169` " +
		"(already created via `git worktree add`) …"
	if permissionRefusal(finalText) {
		t.Fatal("#169's final text is not itself an approval request")
	}

	var rep runReport
	rep.observe(turn("Looking at the worktree layout now."))
	rep.observe(turn("This requires user confirmation to proceed. I'll wait for approval before continuing."))
	rep.observe(turn("Still blocked on that."))
	rep.observe(streamEvent{Type: "result", Subtype: "success", Result: finalText})

	if rep.permissionRefused {
		t.Error("permissionRefused should stay false — the final text was not the ask")
	}
	if !rep.permissionAsked {
		t.Error("permissionAsked should be true — an earlier turn asked for approval")
	}

	// A turn that only quotes the phrase mid-sentence must not latch it, the
	// same false positive the head anchor closes on the result text.
	var quoting runReport
	quoting.observe(turn("Earlier the run said \"This requires user confirmation\" and then stopped; fixing that."))
	quoting.observe(streamEvent{Type: "result", Subtype: "success", Result: "Opened a PR."})
	if quoting.permissionAsked {
		t.Error("permissionAsked should be false — the turn quoted the phrase, it did not ask")
	}

	// A mid-run turn that opens by reporting a missing permission and then
	// works around it is not a stop-to-ask: the "I lack permission" phrasings
	// are in permissionRefusal for a final-word match only, not the mid-run
	// scan, so this must not latch.
	var workaround runReport
	workaround.observe(turn("I don't have permission to run the full suite here, but I'll run the affected package."))
	workaround.observe(streamEvent{Type: "result", Subtype: "success", Result: "Opened a PR."})
	if workaround.permissionAsked {
		t.Error("permissionAsked should be false — the turn described a wall it then went around")
	}
}

// --- version skew between the two halves ---

func TestPluginVersionReadsTheInstalledPlugin(t *testing.T) {
	cfg := fakeClaudeConfig(t, "stream")
	t.Setenv(fakePluginEnv, "0.3.0")

	got, id := pluginVersion(context.Background(), cfg)
	if got != "0.3.0" {
		t.Errorf("pluginVersion = %q, want the installed plugin's version", got)
	}
	if id != "polako@scharissis" {
		t.Errorf("pluginVersion id = %q, want the installed copy's marketplace-qualified id", id)
	}
}

// A -skill with no plugin prefix names a skill copied into ~/.claude/skills.
// It carries no version, and asking the CLI about a plugin by that name would
// answer about something else.
func TestPluginVersionIsEmptyForAHandInstalledSkill(t *testing.T) {
	cfg := fakeClaudeConfig(t, "stream")
	cfg.skill = skillDir
	t.Setenv(fakePluginEnv, "0.3.0")

	if got, _ := pluginVersion(context.Background(), cfg); got != "" {
		t.Errorf("pluginVersion = %q, want empty: a hand-installed skill has no version", got)
	}
}

func TestPluginVersionIsEmptyWhenTheCLICannotAnswer(t *testing.T) {
	cfg := fakeClaudeConfig(t, "stream")

	if got, _ := pluginVersion(context.Background(), cfg); got != "" {
		t.Errorf("pluginVersion = %q, want empty rather than a guess", got)
	}
}

// The list can hold the same plugin twice, and the entry that drives the run is
// not always the first one. Fed straight to the selection so the shapes a real
// `plugin list --json` produces can be written out literally.
func TestPluginVersionPicksTheCopyThatWillRun(t *testing.T) {
	for _, tc := range []struct {
		name   string
		list   string
		want   string
		wantID string
		why    string
	}{{
		name: "sole match",
		list: `[{"id":"some-other-plugin@elsewhere","version":"9.9.9","scope":"user"},
		        {"id":"polako@scharissis","version":"0.3.0","scope":"user"}]`,
		want:   "0.3.0",
		wantID: "polako@scharissis",
		why:    "one copy installed, so there is nothing to choose between",
	}, {
		// The reason this issue exists: a --plugin-dir copy loaded alongside a
		// user-scope install of the same name, which is how a tip skill gets
		// tested against a tip binary. The session copy replaces the installed
		// one outright, and it is listed second.
		name: "session copy behind a user install",
		list: `[{"id":"polako@scharissis","version":"0.1.0","scope":"user"},
		        {"id":"polako@inline","version":"0.6.1","scope":"session"}]`,
		want:   "0.6.1",
		wantID: "polako@inline",
		why:    "the session copy is the one that drives the run",
	}, {
		name: "two copies with no scope to separate them",
		list: `[{"id":"polako@scharissis","version":"0.1.0","scope":"user"},
		        {"id":"polako@a-fork","version":"0.6.1","scope":"user"}]`,
		why: "no honest answer, and a wrong version is worse than none",
	}, {
		name: "two session copies",
		list: `[{"id":"polako@one","version":"0.1.0","scope":"session"},
		        {"id":"polako@two","version":"0.6.1","scope":"session"}]`,
		why: "narrowing to session scope did not get it down to one",
	}, {
		name: "duplicates that agree",
		list: `[{"id":"polako@scharissis","version":"0.6.1","scope":"user"},
		        {"id":"polako@a-mirror","version":"0.6.1","scope":"user"}]`,
		want:   "0.6.1",
		wantID: "",
		why:    "the version is unambiguous but the marketplace is not, so the id is withheld",
	}, {
		name: "a disabled duplicate beside an enabled one",
		list: `[{"id":"polako@a-fork","version":"0.1.0","scope":"user","enabled":false},
		        {"id":"polako@scharissis","version":"0.6.1","scope":"user","enabled":true}]`,
		want:   "0.6.1",
		wantID: "polako@scharissis",
		why:    "a disabled copy never loads, so it is not one of the copies to choose between",
	}, {
		name: "the only copy is disabled",
		list: `[{"id":"polako@scharissis","version":"0.6.1","scope":"user","enabled":false}]`,
		why:  "nothing will load it, so no version drove the run",
	}, {
		name:   "a CLI that does not report enabled",
		list:   `[{"id":"polako@scharissis","version":"0.6.1","scope":"user"}]`,
		want:   "0.6.1",
		wantID: "polako@scharissis",
		why:    "an absent field is not a disabled plugin",
	}, {
		name: "no match",
		list: `[{"id":"some-other-plugin@elsewhere","version":"9.9.9","scope":"user"}]`,
		why:  "the plugin is not installed at all",
	}, {
		name: "empty list",
		list: `[]`,
		why:  "nothing installed",
	}, {
		name: "output that is not the list",
		list: `not json`,
		why:  "a CLI answering with something else is not a version",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			got, gotID := installedVersion([]byte(tc.list), pluginName)
			if got != tc.want {
				t.Errorf("installedVersion = %q, want %q — %s", got, tc.want, tc.why)
			}
			if gotID != tc.wantID {
				t.Errorf("installedVersion id = %q, want %q — %s", gotID, tc.wantID, tc.why)
			}
		})
	}
}

func TestWarnOnVersionSkew(t *testing.T) {
	for _, tc := range []struct {
		name           string
		skill          string
		binary, plugin string
		id             string
		warn           bool
	}{
		{name: "matched release", binary: "0.4.0", plugin: "0.4.0"},
		{name: "matched despite the module's v prefix", binary: "v0.4.0", plugin: "0.4.0"},
		// A resolvable id: the remedy prints a `plugin update` command that names it.
		{name: "skewed with a resolvable id", binary: "0.4.0", plugin: "0.3.0", id: "polako@acme", warn: true},
		// No id — copies from more than one marketplace. The warning still fires
		// and still names both versions, but drops the exact command.
		{name: "skewed without an id", binary: "0.4.0", plugin: "0.3.0", warn: true},
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
			warnOnVersionSkew(tc.binary, config{skill: skill, pluginVersion: tc.plugin, pluginID: tc.id})

			out := buf.String()
			got := strings.Contains(out, "version skew")
			if got != tc.warn {
				t.Errorf("warned = %v, want %v\nlog: %s", got, tc.warn, out)
			}
			if !tc.warn {
				return
			}
			if !strings.Contains(out, tc.plugin) || !strings.Contains(out, tc.binary) {
				t.Errorf("the warning has to name both versions, got: %s", out)
			}
			if !strings.Contains(out, "docs/install.md") {
				t.Errorf("the warning has to point at docs/install.md, got: %s", out)
			}
			switch {
			case tc.id != "":
				// The remedy command has to carry the @-qualified id, or it is
				// the broken command this issue (#190) is about.
				if !strings.Contains(out, "claude plugin update "+tc.id) {
					t.Errorf("skewed with id %q: want a `claude plugin update %s` command, got: %s", tc.id, tc.id, out)
				}
			default:
				// No id to name, so no exact `plugin update` command — guessing
				// a marketplace is worse than sending the operator to the docs.
				if strings.Contains(out, "claude plugin update ") {
					t.Errorf("no id available: the warning must not print a `claude plugin update` command, got: %s", out)
				}
			}
		})
	}
}

// The skew warning's remedy and the update command in docs/install.md must not
// drift: install.md is the canonical wording of the `claude plugin update`
// trap (issue #190), so if the two can disagree, one is wrong the next time the
// CLI changes.
func TestVersionSkewRemedyAgreesWithInstallDocs(t *testing.T) {
	const wantCmd = "claude plugin marketplace update scharissis && claude plugin update polako@scharissis"
	if docs := readRepoFile(t, "docs", "install.md"); !strings.Contains(docs, wantCmd) {
		t.Fatalf("docs/install.md no longer shows %q — move this test and warnOnVersionSkew's remedy with it", wantCmd)
	}
	buf := captureLog(t)
	warnOnVersionSkew("0.4.0", config{
		skill:         defaultSkill,
		pluginVersion: "0.3.0",
		pluginID:      "polako@scharissis",
	})
	if !strings.Contains(buf.String(), wantCmd) {
		t.Errorf("skew warning does not print the docs' update command %q\nlog: %s", wantCmd, buf.String())
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

	rep, err := execClaude(context.Background(), cfg, "/implement-issue 7", "", "implement-issue", 0)
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
		"post-summary": "POLAKO_POST_SUMMARY",
		"metrics":      "POLAKO_METRICS",
		"retry-wait":   "POLAKO_RETRY_WAIT",
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
	t.Setenv("POLAKO_POST_SUMMARY", "1")
	t.Setenv("POLAKO_MODEL", "claude-opus-5")
	t.Setenv("POLAKO_POLL", "90s")

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
	t.Setenv("POLAKO_MODEL", "claude-opus-5")
	t.Setenv("POLAKO_POST_SUMMARY", "1")

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
	t.Setenv("POLAKO_POLL", "banana")

	fs, _, _, _ := envFlagSet()
	err := applyEnvDefaults(fs)
	if err == nil {
		t.Fatal("an unparseable value must stop the process, not be skipped")
	}
	// The message has to name both halves: the variable to fix, and the flag
	// it was trying to set.
	for _, want := range []string{"POLAKO_POLL", "banana", "-poll"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// The three flags that are actions rather than preferences. POLAKO_VERSION
// is what a Dockerfile or CI job pins an install with, POLAKO_DRY_RUN is what
// an operator exports to preview one repository and forgets, and POLAKO_APPLY
// is that same risk mirrored onto `tidy`. Honouring any of them would turn a
// later run into something other than what its operator typed that day: a
// drain that prints and exits 0 without touching the backlog, for the first
// two, or a `tidy` that quietly deletes worktrees and branches nobody meant
// to run live, for the third.
func TestEnvDefaultsIgnoreTheActionFlags(t *testing.T) {
	t.Setenv("POLAKO_VERSION", "0.6.0")
	t.Setenv("POLAKO_DRY_RUN", "1")
	t.Setenv("POLAKO_APPLY", "1")

	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	showVersion := fs.Bool("version", false, "")
	dry := fs.Bool("dry-run", false, "")
	apply := fs.Bool("apply", false, "")
	if err := applyEnvDefaults(fs); err != nil {
		t.Fatalf("applyEnvDefaults: %v", err)
	}
	if *showVersion {
		t.Error("-version must not be settable from the environment")
	}
	if *dry {
		t.Error("-dry-run must not be settable from the environment")
	}
	if *apply {
		t.Error("-apply must not be settable from the environment")
	}
}
