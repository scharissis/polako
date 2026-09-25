package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

// fakeStdinLogEnv names a file the fake claude run writes whatever it read
// from stdin to — the -remote invocation's own control_request and user
// message, which never reach argv at all (see remoteStdin). Read once,
// before mode dispatch, the same way recordFakeArgs is: a mode that delegates
// to another by recursing must not read stdin twice.
const fakeStdinLogEnv = "POLAKO_FAKE_STDIN_LOG"

// fakeEnv turns alternating key, value pairs into KEY=value entries for
// config.env, which hands them to a child without t.Setenv on the parent —
// the thing that would otherwise bar the test from t.Parallel(). A pair whose
// value is "" is dropped: with config.env an absent entry is the "unset" the
// child sees, so no placeholder is needed.
func fakeEnv(kv ...string) []string {
	var env []string
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] != "" {
			env = append(env, kv[i]+"="+kv[i+1])
		}
	}
	return env
}

// blankModelEnv blanks the CLI's model and effort overrides for a config's
// children and for lookupEnv, so a developer's own export can't trip the effort
// gate or the env warnings in a test that never asked for them. A test that
// wants one set layers it on with setFakeEnv.
func blankModelEnv() []string {
	return []string{"ANTHROPIC_MODEL=", "ANTHROPIC_DEFAULT_OPUS_MODEL=", "ANTHROPIC_DEFAULT_SONNET_MODEL=",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL=", effortEnv + "="}
}

// setFakeEnv replaces (or adds, or with an empty value removes) KEY=value
// entries on cfg.env, for a test that layers one more handshake variable on a
// config a builder already populated.
func setFakeEnv(cfg *config, kv ...string) {
	for i := 0; i+1 < len(kv); i += 2 {
		cfg.env = slices.DeleteFunc(cfg.env, func(e string) bool {
			return strings.HasPrefix(e, kv[i]+"=")
		})
		if kv[i+1] != "" {
			cfg.env = append(cfg.env, kv[i]+"="+kv[i+1])
		}
	}
}

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
	// Gated on argv the same way, and for the same reason: `polako update`'s
	// process inherits every POLAKO_FAKE_* a test set for gh and claude too.
	if os.Getenv(fakeGoEnv) != "" && len(os.Args) > 1 && (os.Args[1] == "env" || os.Args[1] == "install") {
		recordFakeArgs()
		os.Exit(fakeGo(os.Args[1:]))
	}
	if mode := os.Getenv(fakeClaudeEnv); mode != "" {
		// Here rather than inside fakeClaude, which recurses: a mode that
		// delegates to another must still count as the one invocation it is.
		recordFakeArgs()
		recordFakeStdin()
		os.Exit(fakeClaude(mode))
	}
	clearEnvDefaults()
	code := m.Run()
	if fakeCLIDir != "" {
		os.RemoveAll(fakeCLIDir)
	}
	if gitFixtureDir != "" {
		os.RemoveAll(gitFixtureDir)
	}
	if fakeSSHDenyDir != "" {
		os.RemoveAll(fakeSSHDenyDir)
	}
	os.Exit(code)
}

// clearEnvDefaults keeps the suite hermetic against the shell it runs in.
// Flags take their defaults from POLAKO_*, so a maintainer who set one in
// their profile would otherwise be running a different suite from CI. Once,
// for the whole process, rather than per test through t.Setenv: that panics
// in a test that has called t.Parallel(), and nearly all of them have. A test
// that wants one set still uses t.Setenv, stays serial, and restores it to
// unset. Called after TestMain's fake-CLI checks — a child picks its role off
// POLAKO_FAKE_*, which carry the same prefix.
func clearEnvDefaults() {
	for _, kv := range os.Environ() {
		if name, _, _ := strings.Cut(kv, "="); strings.HasPrefix(name, envPrefix) {
			os.Unsetenv(name)
		}
	}
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

// fakeGoEnv makes the test binary impersonate `go`, for `polako update`'s
// own two calls — `go env GOBIN`/`go env GOPATH`, and `go install
// ...@vX.Y.Z`. Gated on argv (TestMain) like fakeGhEnv, since a test that
// sets this alongside the claude/gh fixtures has all three inherited by
// every child update spawns.
const fakeGoEnv = "POLAKO_FAKE_GO"

// fakeGoGOBINEnv and fakeGoGOPATHEnv are what the fake answers `go env
// GOBIN`/`go env GOPATH` with. Both empty (unset) is the common case — an
// operator who never set GOBIN — under which goInstallDir falls back to
// GOPATH's bin directory.
const (
	fakeGoGOBINEnv  = "POLAKO_FAKE_GO_GOBIN"
	fakeGoGOPATHEnv = "POLAKO_FAKE_GO_GOPATH"
)

// fakeGo stands in for `go env GOBIN`/`go env GOPATH` and `go install`.
// Trusts exit status for `install` the same way update.go's own caller
// does — no output to fake, since nothing reads `go install`'s stdout.
func fakeGo(args []string) int {
	if len(args) == 2 && args[0] == "env" {
		switch args[1] {
		case "GOBIN":
			fmt.Println(os.Getenv(fakeGoGOBINEnv))
		case "GOPATH":
			fmt.Println(os.Getenv(fakeGoGOPATHEnv))
		default:
			fmt.Fprintf(os.Stderr, "fake go: unknown env var %q\n", args[1])
			return 1
		}
		return 0
	}
	if len(args) >= 1 && args[0] == "install" {
		return 0
	}
	fmt.Fprintf(os.Stderr, "fake go: unrecognized argv %v\n", args)
	return 1
}

// fakeUsageEnv picks which `/usage` fixture fakeClaude answers with. Unset
// means "no such command" — an old CLI without /usage — which is also why
// every existing stream-mode test is unaffected by this dispatch existing
// at all.
const fakeUsageEnv = "POLAKO_FAKE_USAGE"

// fakeEffortHelpEnv toggles whether fakeClaude's `--help` lists `--effort`.
// Unset means it does — today's CLI — so effortFlagGate's probe passes wherever
// a test sets -effort without opting out. Set to "0" to model an older CLI that
// predates the flag.
const fakeEffortHelpEnv = "POLAKO_FAKE_EFFORT_HELP"

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
	// `polako update`'s own two writes — argv-dispatched like plugin list,
	// since neither goes through execClaude. Success is the only fixture:
	// what update.go does with a failure is capture()'s own error wrapping,
	// already covered where every other exec caller in this package is.
	if len(os.Args) > 2 && os.Args[1] == "plugin" && os.Args[2] == "marketplace" {
		return 0
	}
	if len(os.Args) > 2 && os.Args[1] == "plugin" && os.Args[2] == "update" {
		return 0
	}
	// `claude --version` is another argv-dispatched call any run's preflight can
	// make — claudeVersion reads the first field of its output. Dispatched here
	// for the same reason plugin list is.
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		emit("2.1.99 (Claude Code)")
		return 0
	}
	// `claude --help` is effortFlagGate's capability probe — argv-dispatched
	// like --version, and only ever run when a test sets -effort.
	if len(os.Args) == 2 && os.Args[1] == "--help" {
		emit("Usage: claude [options] [command]")
		emit("  --model <model>   an alias for the latest model")
		if os.Getenv(fakeEffortHelpEnv) != "0" {
			emit("  --effort <level>  low, medium, high, xhigh, max")
		}
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
		emit(`{"type":"system","subtype":"init","session_id":"sess-xyz","model":"claude-opus-5","claude_code_version":"2.1.280",` +
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
	case "remoteok":
		// A control_response success, before the init event — the earlier of
		// the two orderings issue #471 probed by hand ("the reply lands in
		// 0.5-2s, before or after init"). Delegates to "stream" for the rest,
		// so this only has to add the one line the -remote path cares about.
		emit(`{"type":"control_response","response":{"subtype":"success","request_id":"rc",` +
			`"response":{"session_url":"https://claude.ai/code/session/abc123"}}}`)
		return fakeClaude("stream")
	case "remoteerror":
		// A control_response error, after the result event — the later of
		// the two orderings. Its own small stream rather than delegating,
		// since "stream" returns before this mode gets to add anything after
		// its own result event.
		emit(`{"type":"system","subtype":"init","session_id":"sess-remote-err","model":"claude-opus-5",` +
			`"slash_commands":["implement-issue"]}`)
		emit(`{"type":"result","subtype":"success","session_id":"sess-remote-err","duration_ms":10,` +
			`"num_turns":1,"total_cost_usd":0.01,"result":"done",` +
			`"usage":{"input_tokens":1,"output_tokens":1}}`)
		emit(`{"type":"control_response","response":{"subtype":"error","request_id":"rc",` +
			`"error":"Remote Control initialization failed"}}`)
		return 0
	case "remotewrongid":
		// A control_response success, but for a request this run never
		// sent — request_id "other" rather than remoteControlRequestID's
		// "rc". Must be read as no reply at all, not as this run's own
		// registration.
		emit(`{"type":"control_response","response":{"subtype":"success","request_id":"other",` +
			`"response":{"session_url":"https://claude.ai/code/session/unrelated"}}}`)
		return fakeClaude("stream")
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
	case "toolrefused":
		// Issue #209: #126's actual shape — a refused tool_result mid-run
		// (the CLI's own fact) followed by a clean exit whose final text is
		// ordinary prose with no ask of its own, so the prose route
		// (permissionRefusal) stays blind to it and only the structural
		// signal catches it. Two refusals, issue #432 (ticket 3): one
		// addToolsEntry can turn into a grantable entry, one path-bearing —
		// addToolsEntryThreadSafe has to keep the second off the thread while
		// the first still reaches it.
		emit(`{"type":"system","subtype":"init","session_id":"sess-refused","model":"claude-opus-5"}`)
		emit(`{"type":"assistant","session_id":"sess-refused","message":{"content":[` +
			`{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"curl -s https://example.com/status"}}]}}`)
		emit(`{"type":"user","session_id":"sess-refused","message":{"content":[` +
			`{"type":"tool_result","tool_use_id":"toolu_1","is_error":true,"content":"This command requires approval"}]}}`)
		emit(`{"type":"assistant","session_id":"sess-refused","message":{"content":[` +
			`{"type":"tool_use","id":"toolu_2","name":"Bash","input":{"command":"/Users/x/bin/tool --flag"}}]}}`)
		emit(`{"type":"user","session_id":"sess-refused","message":{"content":[` +
			`{"type":"tool_result","tool_use_id":"toolu_2","is_error":true,"content":"This command requires approval"}]}}`)
		emit(`{"type":"result","subtype":"success","session_id":"sess-refused","duration_ms":100,` +
			`"num_turns":3,"total_cost_usd":0.1,"result":"Issue #1 is resolved: nothing left to do."}`)
		return 0
	case "toolrefusedrecovered":
		// Issue #461's #402/#318 shape: the same refused tool_result as
		// "toolrefused", but this run kept going afterward — one more tool
		// call, this one successful, then a calm final word that is not
		// itself an ask. Identical on every dispatch, so a drain that resumes
		// into this fake still has the same refusal to name once the shared
		// clean-exit ceiling finally parks it.
		emit(`{"type":"system","subtype":"init","session_id":"sess-recovered","model":"claude-opus-5"}`)
		emit(`{"type":"assistant","session_id":"sess-recovered","message":{"content":[` +
			`{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"cd /w && gofmt -l ."}}]}}`)
		emit(`{"type":"user","session_id":"sess-recovered","message":{"content":[` +
			`{"type":"tool_result","tool_use_id":"toolu_1","is_error":true,"content":"This command requires approval"}]}}`)
		emit(`{"type":"assistant","session_id":"sess-recovered","message":{"content":[` +
			`{"type":"tool_use","id":"toolu_2","name":"Bash","input":{"command":"gofmt -l /w"}}]}}`)
		emit(`{"type":"user","session_id":"sess-recovered","message":{"content":[` +
			`{"type":"tool_result","tool_use_id":"toolu_2","is_error":false,"content":""}]}}`)
		emit(`{"type":"result","subtype":"success","session_id":"sess-recovered","duration_ms":100,` +
			`"num_turns":4,"total_cost_usd":0.1,"result":"Committed the fix; ending here."}`)
		return 0
	case "toolrefused390":
		// Issue #390's actual shape, for ticket 3 (#432): two refusals — one
		// addToolsEntry can turn into `Bash(ssh:*)`, one `Contains
		// simple_expansion` (a `$VAR`, ungrantable) — a successful call
		// after the last one, and a calm final word that is itself the real
		// diagnosis (an unreachable SSH agent), not an ask. Granting the
		// tool named in the first refusal would have fixed nothing; #390's
		// own park still said "grant a tool" because only the first refusal
		// was ever kept. Identical on every dispatch, so a drain that
		// resumes into this fake still has the same two refusals and the
		// same final words once the shared clean-exit ceiling finally parks
		// it.
		emit(`{"type":"system","subtype":"init","session_id":"sess-390","model":"claude-opus-5"}`)
		emit(`{"type":"assistant","session_id":"sess-390","message":{"content":[` +
			`{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"ssh -T git@github.com"}}]}}`)
		emit(`{"type":"user","session_id":"sess-390","message":{"content":[` +
			`{"type":"tool_result","tool_use_id":"toolu_1","is_error":true,"content":"This command requires approval"}]}}`)
		emit(`{"type":"assistant","session_id":"sess-390","message":{"content":[` +
			`{"type":"tool_use","id":"toolu_2","name":"Bash",` +
			`"input":{"command":"git fetch origin 2>&1; echo RC=$?"}}]}}`)
		emit(`{"type":"user","session_id":"sess-390","message":{"content":[` +
			`{"type":"tool_result","tool_use_id":"toolu_2","is_error":true,"content":"Contains simple_expansion"}]}}`)
		emit(`{"type":"assistant","session_id":"sess-390","message":{"content":[` +
			`{"type":"tool_use","id":"toolu_3","name":"Bash","input":{"command":"git status"}}]}}`)
		emit(`{"type":"user","session_id":"sess-390","message":{"content":[` +
			`{"type":"tool_result","tool_use_id":"toolu_3","is_error":false,"content":""}]}}`)
		emit(`{"type":"result","subtype":"success","session_id":"sess-390","duration_ms":100,` +
			`"num_turns":6,"total_cost_usd":0.1,` +
			`"result":"git fetch origin keeps failing here — looks like the SSH agent isn't reachable from this session."}`)
		return 0
	case "toolrefusedrecoveredthenrefusedagain":
		// Issue #432's own review: a worked-around refusal (run 1) defers to
		// a resume, and that resume hits a *different* refusal it does not
		// recover from — #126's shape, not #461's. The immediate park this
		// triggers must still name run 1's deferred refusal alongside this
		// one, not just its own.
		if !slices.Contains(os.Args, "--resume") {
			return fakeClaude("toolrefusedrecovered")
		}
		emit(`{"type":"system","subtype":"init","session_id":"sess-recovered","model":"claude-opus-5"}`)
		emit(`{"type":"assistant","session_id":"sess-recovered","message":{"content":[` +
			`{"type":"tool_use","id":"toolu_3","name":"Bash","input":{"command":"rm -rf /tmp/x"}}]}}`)
		emit(`{"type":"user","session_id":"sess-recovered","message":{"content":[` +
			`{"type":"tool_result","tool_use_id":"toolu_3","is_error":true,"content":"This command requires approval"}]}}`)
		emit(`{"type":"result","subtype":"success","session_id":"sess-recovered","duration_ms":100,` +
			`"num_turns":2,"total_cost_usd":0.1,"result":"Cleanup failed; stopping here."}`)
		return 0
	case "toolrefusedrecoveredthencrash":
		// Issue #461: the deferred refusal from a worked-around clean exit
		// has to survive the *resume* itself dying instead of ending cleanly
		// again — giveUpAfterCrash's own share of the fix, not just
		// afterCleanExit's. Which run this is comes off argv, since it is
		// the crash arm this proves, not the clean-exit one.
		if !slices.Contains(os.Args, "--resume") {
			return fakeClaude("toolrefusedrecovered")
		}
		return fakeClaude("crash")
	case "toolrefusedrecoveredthenquestionthenclean":
		// Issue #461's ledger-clearing fix: a worked-around refusal (run 1)
		// defers and resumes; the resumed session (run 2) asks an unrelated
		// question instead, which -strict-order waits out and folds in; the
		// fresh run that follows and its own resume (runs 3 and 4) both end
		// cleanly with no refusal of their own. Once the shared ceiling
		// finally parks it, the reason must be generic — clearRetries has to
		// have dropped the question round's now-irrelevant deferred refusal.
		n, err := countClaudeRun()
		if err != nil {
			fmt.Fprintf(os.Stderr, "fake claude: %v\n", err)
			return 1
		}
		if n == 1 {
			return fakeClaude("toolrefusedrecovered")
		}
		if n == 2 {
			if err := fakeSkillEffect(mode); err != nil {
				fmt.Fprintf(os.Stderr, "fake claude: %v\n", err)
				return 1
			}
		} else {
			// Runs 3 and 4: the label is still up from run 2's question —
			// real skill behaviour once an answer is folded in is to clear
			// it, same as fakeSkillEffect's own "already labelled" branch,
			// but without also planting a PR: this run still ends with
			// nothing to show, which is the shape the ceiling park needs.
			path := os.Getenv(fakeGhEnv)
			st, err := readGhState(path)
			if err != nil {
				fmt.Fprintf(os.Stderr, "fake claude: %v\n", err)
				return 1
			}
			if is := st.Issues[promptIssue()]; is != nil && slices.Contains(is.Labels, awaitingAnswerLabel) {
				if _, _, code := answerGh(st, []string{"issue", "edit", promptIssue(),
					"--remove-label", awaitingAnswerLabel}); code != 0 {
					fmt.Fprintln(os.Stderr, "fake claude: could not remove the answered label")
					return 1
				}
				if err := writeGhState(path, st); err != nil {
					fmt.Fprintf(os.Stderr, "fake claude: %v\n", err)
					return 1
				}
			}
		}
		emit(`{"type":"system","subtype":"init","session_id":"sess-clean","model":"claude-opus-5"}`)
		emit(`{"type":"result","subtype":"success","session_id":"sess-clean","duration_ms":100,` +
			`"num_turns":2,"total_cost_usd":0.1,"result":"Nothing more to do here."}`)
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
	case "heartbeat":
		// A stream that is busy the whole way through — a tool call every
		// 100ms — but whose calls are all detail the default terminal filters
		// out. So the terminal is quiet for ~600ms while the stream is not,
		// which is exactly the gap the heartbeat exists to fill and -stall
		// (watching the stream) never sees. The first Read parks the stage
		// recognizer at "reading the code…". Under -verbose every one of these
		// calls reaches the terminal, so the heartbeat clock never expires.
		emit(`{"type":"system","subtype":"init","session_id":"sess-hb","model":"claude-opus-5"}`)
		for i := 0; i < 10; i++ {
			emit(`{"type":"assistant","session_id":"sess-hb","message":{"content":[` +
				`{"type":"tool_use","name":"Read","input":{"file_path":"main.go"}}]}}`)
			time.Sleep(100 * time.Millisecond)
		}
		emit(`{"type":"result","subtype":"success","session_id":"sess-hb","duration_ms":600,` +
			`"num_turns":2,"total_cost_usd":0.1,"result":"Opened a PR."}`)
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
	case "plan", "plancap":
		// A `polako plan` run: file some proposals through the fake gh — a mix
		// of labelled and not — pairing each `gh issue create` tool_use with a
		// real tool_result once the fake gh has actually created it, since
		// the -max-issues counter now counts the result, not the dispatch
		// (issue #340). "plancap" files one more than any cap a test sets, so
		// the kill path runs; the pause after each result gives the parent's
		// kill a real window to land between issues rather than after every
		// one already exists — the same pattern "heartbeat" uses.
		path := os.Getenv(fakeGhEnv)
		st, err := readGhState(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "fake claude: %v\n", err)
			return 1
		}
		emit(`{"type":"system","subtype":"init","session_id":"sess-plan","model":"claude-opus-5"}`)
		n := 3
		if mode == "plancap" {
			n = 4
		}
		for i := 0; i < n; i++ {
			id := fmt.Sprintf("toolu_create_%d", i+1)
			emit(fmt.Sprintf(`{"type":"assistant","session_id":"sess-plan","message":{"content":[`+
				`{"type":"tool_use","id":%q,"name":"Bash","input":{"command":"gh issue create --title T --label proposed --body-file ISSUE_BODY.md"}}]}}`, id))
			args := []string{"issue", "create", "--title", fmt.Sprintf("Proposal %d", i+1), "--body-file", "ISSUE_BODY.md"}
			if i == 0 {
				args = append(args, "--label", proposedLabel)
			}
			if _, _, code := answerGh(st, args); code != 0 {
				fmt.Fprintf(os.Stderr, "fake claude: gh refused to create proposal %d\n", i+1)
				return 1
			}
			if err := writeGhState(path, st); err != nil {
				fmt.Fprintf(os.Stderr, "fake claude: %v\n", err)
				return 1
			}
			emit(fmt.Sprintf(`{"type":"user","session_id":"sess-plan","message":{"content":[`+
				`{"type":"tool_result","tool_use_id":%q,"is_error":false,"content":"created"}]}}`, id))
			if mode == "plancap" {
				time.Sleep(100 * time.Millisecond)
			}
		}
		emit(`{"type":"result","subtype":"success","session_id":"sess-plan","duration_ms":100,` +
			`"num_turns":4,"total_cost_usd":0.3,"result":"Filed the proposals."}`)
		return 0
	case "planempty":
		// A `polako plan` run that proposed nothing — the document held no
		// one-PR work the backlog was missing, or the proposal gate cut every
		// candidate. It files no issues and creates nothing for the label
		// pass to normalise or the `proposed` hook to announce.
		emit(`{"type":"system","subtype":"init","session_id":"sess-plan","model":"claude-opus-5"}`)
		emit(`{"type":"result","subtype":"success","session_id":"sess-plan","duration_ms":100,` +
			`"num_turns":2,"total_cost_usd":0.1,"result":"Nothing worth proposing."}`)
		return 0
	case "asks", "noisy", "askscrash", "asksbot":
		// A run that leaves something behind on the thread. Both then stream
		// like a healthy one, because the supervisor's whole reading of what
		// happened comes from GitHub afterwards, not from the events.
		if err := fakeSkillEffect(mode); err != nil {
			fmt.Fprintf(os.Stderr, "fake claude: %v\n", err)
			return 1
		}
		return fakeClaude("stream")
	case "design", "designasks":
		// A `polako design` run. The prompt has to be the bare slash command —
		// no `no-evidence`, which design-plan doesn't declare — so anything else
		// fails the run loudly. "design" ships straight to a merged PR;
		// "designasks" asks first and ships on the rerun, the "asks" shape.
		want := "/" + defaultDesignSkill + " " + promptIssue()
		if got := promptText(); got != want {
			fmt.Fprintf(os.Stderr, "fake claude: prompt %q, want exactly %q\n", got, want)
			return 1
		}
		var err error
		if mode == "design" {
			err = plantPR("MERGED")
		} else {
			err = fakeSkillEffect("asks")
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "fake claude: %v\n", err)
			return 1
		}
		// The session has to list design-plan, or the missing-skill tripwire
		// kills the run before it starts.
		emit(`{"type":"system","subtype":"init","session_id":"sess-design","model":"claude-opus-5",` +
			`"slash_commands":["polako:design-plan","design-plan"]}`)
		emit(`{"type":"result","subtype":"success","session_id":"sess-design","duration_ms":1000,` +
			`"num_turns":3,"total_cost_usd":0.5,"result":"Opened a PR for the design.",` +
			`"usage":{"input_tokens":100,"output_tokens":200}}`)
		return 0
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
	case "shootreview":
		// A review remediation that answered with screenshots alone: shots
		// published and linked in a PR comment, nothing pushed to the branch.
		if err := fakeReviewShots(); err != nil {
			fmt.Fprintf(os.Stderr, "fake claude: %v\n", err)
			return 1
		}
		return fakeClaude("stream")
	case "implementmerged":
		// A fresh implement run whose PR is already merged by the time the
		// supervisor looks — the shortest path from pickup to a closed issue,
		// for a test that only cares what the implement run was dispatched with.
		if err := plantPR("MERGED"); err != nil {
			fmt.Fprintf(os.Stderr, "fake claude: %v\n", err)
			return 1
		}
		return fakeClaude("stream")
	case "implementthenrebase":
		// A fresh implement run that opens a CONFLICTING PR, then — dispatched
		// again with a prose prompt — clears the conflict. Branches on whether
		// the -p prompt opens with "/" (a skill invocation) or not (the
		// remediation), the same tell promptIssue's comment describes.
		var err error
		if strings.HasPrefix(promptText(), "/") {
			err = plantPR("OPEN")
			if err == nil {
				err = markPRsConflicting()
			}
		} else {
			err = fakeConflictFix()
		}
		if err != nil {
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
// that ends the call rather than this. "over-then-under" answers over any
// ceiling on the first probe and well under on every one after it, for the
// usage gate's wait-then-carry-on path. Unset exits 1 — a CLI too old to have
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
	case "over-then-under":
		// The gate's wait-and-resume path. The first probe reports both pools
		// over any ceiling a test would set and names no reset clause — so the
		// drain re-checks every -poll rather than sleeping the suite into real
		// wall time, the same limitation TestDrainWaitsOutASessionLimitThenShips
		// works around. Every probe after it reports the pools well under, as
		// if the block had reset while the gate waited.
		if n, err := countUsageProbe(); err != nil {
			fmt.Fprintf(os.Stderr, "fake claude: %v\n", err)
			return 1
		} else if n <= 1 {
			result = "Current session: 95% used\nCurrent week (all models): 95% used\n"
		} else {
			result = "Current session: 3% used\nCurrent week (all models): 4% used\n"
		}
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
// it: one string per invocation the supervisor made, in order. It records the
// log path on cfg.env, so the config must be built first.
func watchClaudeArgs(t *testing.T, cfg *config) func() []string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "claude-args.log")
	setFakeEnv(cfg, fakeArgsLogEnv, path)
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

// recordFakeStdin copies whatever this invocation read from stdin into the
// file fakeStdinLogEnv names, if a test asked for one. Reading an unset
// cmd.Stdin hits the null device and returns immediately, so this is safe to
// call unconditionally rather than gating it on -remote — the same
// best-effort, silent shape recordFakeArgs has.
func recordFakeStdin() {
	path := os.Getenv(fakeStdinLogEnv)
	if path == "" {
		return
	}
	b, err := io.ReadAll(os.Stdin)
	if err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	f.Write(b)
}

// watchClaudeStdin points the fake CLI at a fresh log and returns the reader
// for it: everything the invocation wrote to stdin. It records the log path
// on cfg.env, so the config must be built first.
func watchClaudeStdin(t *testing.T, cfg *config) func() string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "claude-stdin.log")
	setFakeEnv(cfg, fakeStdinLogEnv, path)
	return func() string {
		b, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			return ""
		}
		if err != nil {
			t.Fatalf("reading the fake CLI's stdin log: %v", err)
		}
		return string(b)
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

// countUsageProbe records another `claude -p /usage` call and reports which
// one this is — the same scratchpad and the same reason as countClaudeRun,
// for the fixture whose pool drops below the ceiling once the gate has waited
// one reset out.
func countUsageProbe() (int, error) {
	path := os.Getenv(fakeGhEnv)
	st, err := readGhState(path)
	if err != nil {
		return 0, err
	}
	st.UsageProbes++
	return st.UsageProbes, writeGhState(path, st)
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

// promptText is the whole -p prompt this invocation was dispatched with, for
// a fake that branches on its shape rather than on the issue number in it.
func promptText() string {
	i := slices.Index(os.Args, "-p")
	if i < 0 || i+1 >= len(os.Args) {
		return ""
	}
	return os.Args[i+1]
}

// markPRsConflicting turns every open PR's mergeability CONFLICTING, standing
// in for the moment a merge onto the default branch made this branch stop
// applying cleanly. Like fakeCIFix it touches every matching PR rather than
// the one this prose-prompted invocation was dispatched for, and the tests
// using it have one.
func markPRsConflicting() error {
	path := os.Getenv(fakeGhEnv)
	st, err := readGhState(path)
	if err != nil {
		return err
	}
	for _, pr := range st.PRs {
		if pr.State == "OPEN" {
			pr.Mergeable = "CONFLICTING"
		}
	}
	return writeGhState(path, st)
}

// fakeConflictFix stands in for a conflict remediation that rebased and
// force-pushed: the branch applies cleanly again, and a human merges it two
// polls later. The CONFLICTING counterpart of fakeCIFix.
func fakeConflictFix() error {
	path := os.Getenv(fakeGhEnv)
	st, err := readGhState(path)
	if err != nil {
		return err
	}
	for _, pr := range st.PRs {
		if pr.Mergeable == "CONFLICTING" {
			pr.Mergeable, pr.Head, pr.MergeOnRead = "MERGEABLE", pr.Head+"+", 2
		}
	}
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

// fakeReviewShots stands in for a review remediation whose answer was
// screenshots alone: it leaves the head where it was and adds one comment,
// posted as the viewer, linking a before/after pair of the current head on the
// evidence ref — the table reviewShotsHow asks for. Every PR with a review
// outstanding, for the same reason fakeReviewFix gives.
func fakeReviewShots() error {
	path := os.Getenv(fakeGhEnv)
	st, err := readGhState(path)
	if err != nil {
		return err
	}
	for _, pr := range st.PRs {
		if !slices.ContainsFunc(pr.Reviews, func(r fakeReview) bool {
			return r.State == reviewChangesRequested
		}) || len(pr.Head) < 7 {
			continue
		}
		dir := fmt.Sprintf("https://github.com/%s/blob/%s/issue-1/%s/", st.Repo,
			"0123456789abcdef0123456789abcdef01234567", pr.Head[:7])
		pr.Comments = append(pr.Comments, fakeComment{
			Author: viewerLogin(st),
			Body: "Shots of `/` at 1280x800.\n\n| Before | After |\n| --- | --- |\n" +
				"| ![before /](" + dir + "before-home.png?raw=true) | ![after /](" + dir +
				"after-home.png?raw=true) |\n",
			CreatedAt: "2026-08-20T12:00:00Z", // after every review the tests write
		})
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

// testCaptureUI maps a running test to its one ui. Keyed by *testing.T — which
// every test and subtest owns uniquely — so parallel tests never see each
// other's entry; this is the per-test replacement for the global logger
// redirect that used to make narration tests serial. Every config builder
// sets cfg.ui to testUI(t), so all of a test's production narration lands in
// one buffer it can assert on, whatever order the test builds things in.
var testCaptureUI sync.Map // *testing.T -> *ui

// testUI is this test's ui, created on first use: an io.Discard terminal and
// a buffer for everything (captureLog hands that buffer back). Presentation
// tests that need the terminal and file sinks kept apart, or -verbose, call
// captureUI first with a ui of their own.
func testUI(t *testing.T) *ui {
	if u, ok := testCaptureUI.Load(t); ok {
		return u.(*ui)
	}
	u := &ui{terminal: io.Discard, file: &bytes.Buffer{}}
	actual, loaded := testCaptureUI.LoadOrStore(t, u)
	if !loaded {
		t.Cleanup(func() { testCaptureUI.Delete(t) })
	}
	return actual.(*ui)
}

// captureLog returns the buffer this test's narration is captured into — the
// shift-log view these tests assert on, terminal presentation and the
// milestone/detail split alike collapsed into one stream (ui_test.go covers
// presentation separately). No global to redirect, so the test is free to
// run t.Parallel(). Order-independent with the config builders: both resolve
// the same ui through testUI.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	return testUI(t).file.(*bytes.Buffer)
}

// captureUI registers a caller-built ui for this test, for the presentation
// tests that need the terminal and file sinks kept apart or -verbose set —
// what testUI's io.Discard terminal collapses. Config builders route
// production narration into it the same way. Call it before anything that
// reaches testUI.
func captureUI(t *testing.T, u *ui) {
	t.Helper()
	if _, loaded := testCaptureUI.LoadOrStore(t, u); loaded {
		t.Fatal("captureUI: this test already has a ui — call it before captureLog or any config builder")
	}
	t.Cleanup(func() { testCaptureUI.Delete(t) })
}

// fakeClaudeConfig is the config builder shared by every test that dispatches
// the fake CLI through execClaude — claude_test.go's own tests, and
// gate_test.go's, metrics_test.go's and status_test.go's fixtures too — which
// is why it lives here with the rest of the harness rather than in one of
// those topic files.
func fakeClaudeConfig(t *testing.T, mode string) config {
	t.Helper()
	return config{
		env:            append(fakeEnv(fakeClaudeEnv, mode), blankModelEnv()...), // handed to the child, not set on the parent
		ui:             testUI(t),
		dir:            t.TempDir(),
		claudeBin:      fakeCLI(t), // this test package, re-entered via TestMain
		skill:          defaultSkill,
		permissionMode: "acceptEdits",
		tools:          "Read",
		stall:          10 * time.Second,
		visualEvidence: true,
	}
}

// Only SIGINT used to cancel the run. A SIGTERM — a machine shutting down, a
// service manager stopping the unit, a plain pkill — killed the supervisor
// outright, so the context never cancelled and exec.CommandContext never killed
// the child. That orphan keeps acceptEdits and the whole --allowedTools set: it
// can go on editing, commit, push the branch and open a PR, and a restarted
// drain sees no PR yet and starts a second run on the same issue.
func TestShutdownSignalsCoverMoreThanCtrlC(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
