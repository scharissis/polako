package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// homeDir points os.UserHomeDir at a temporary directory for one test, on
// every platform, so nothing can touch the real ~/.backlog-drain.
func homeDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)        // unix
	t.Setenv("USERPROFILE", dir) // windows
	return dir
}

func metricsConfig(t *testing.T, spec string) config {
	t.Helper()
	return config{
		repo:           "scharissis/backlog-drain",
		skill:          defaultSkill,
		permissionMode: "acceptEdits",
		model:          "claude-opus-5",
		tag:            "baseline",
		tools:          "Read,Write",
		poll:           5 * time.Minute,
		retries:        3,
		retryWait:      30 * time.Second,
		stall:          15 * time.Minute,
		claudeVersion:  "2.1.34",
		pluginVersion:  "0.3.0",
		rec:            newRecorder(spec),
	}
}

func sampleReport() runReport {
	return runReport{
		sessionID: "sess-xyz",
		model:     "claude-opus-5",
		subtype:   "success",
		hasResult: true,
		turns:     74,
		toolUses:  63,
		wallMS:    1141000,
		apiMS:     812000,
		costUSD:   4.12,
		usage:     tokenCounts{In: 2143, Out: 48210, CacheRead: 8123400, CacheWrite: 401200},
		modelUsage: map[string]modelTokens{
			"claude-opus-5": {tokenCounts: tokenCounts{In: 2143, Out: 47000}, CostUSD: 4.01},
		},
	}
}

// decode marshals a record the way the recorder does and reads it back, so the
// assertions are about the JSON that actually lands on disk.
func decode(t *testing.T, rec any) map[string]any {
	t.Helper()
	b, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshalling %T: %v", rec, err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("record is not an object: %v", err)
	}
	return got
}

func TestRunRecordIsSelfDescribing(t *testing.T) {
	cfg := metricsConfig(t, metricsOff)
	rc := runContext{
		issue: 12, pr: 34, reason: reasonImplement, outcome: outcomeOpenedPR,
		started: time.Date(2026, 8, 24, 10, 15, 0, 0, time.UTC),
		ended:   time.Date(2026, 8, 24, 10, 34, 2, 0, time.UTC),
	}
	got := decode(t, newRunRecord(cfg, rc, sampleReport()))

	for key, want := range map[string]any{
		"v":               float64(recordVersion),
		"kind":            "run",
		"ts":              "2026-08-24T10:15:00Z",
		"ended":           "2026-08-24T10:34:02Z",
		"repo":            "scharissis/backlog-drain",
		"issue":           float64(12),
		"pr":              float64(34),
		"reason":          reasonImplement,
		"outcome":         outcomeOpenedPR,
		"status":          "ok",
		"subtype":         "success",
		"session":         "sess-xyz",
		"turns":           float64(74),
		"tool_uses":       float64(63),
		"api_ms":          float64(812000),
		"cost_usd":        4.12,
		"usage_source":    usageResult,
		"model":           "claude-opus-5",
		"requested_model": "claude-opus-5",
		"permission_mode": "acceptEdits",
		"tag":             "baseline",
		"claude_version":  "2.1.34",
		"plugin_version":  "0.3.0",
		// The configuration under test, snapshotted so one line explains itself.
		"poll_s":       float64(300),
		"retries":      float64(3),
		"retry_wait_s": float64(30),
		"stall_s":      float64(900),
	} {
		if got[key] != want {
			t.Errorf("record[%q] = %v, want %v", key, got[key], want)
		}
	}
	if got["tools_hash"] != toolsHash("Read,Write") {
		t.Errorf("tools_hash = %v, want the hash of the resolved list", got["tools_hash"])
	}
	tokens, ok := got["tokens"].(map[string]any)
	if !ok || tokens["cache_read"] != float64(8123400) {
		t.Errorf("tokens = %v, want the four-way split", got["tokens"])
	}
	models, ok := got["model_usage"].(map[string]any)
	if !ok || len(models) != 1 {
		t.Fatalf("model_usage = %v, want one entry", got["model_usage"])
	}
	if opus := models["claude-opus-5"].(map[string]any); opus["cost_usd"] != 4.01 || opus["in"] != float64(2143) {
		t.Errorf("per-model entry = %v, want flattened tokens plus cost", opus)
	}
}

// Crash, stall and interrupt never emit a result event, yet burned real
// tokens. Dropping those runs would make exactly the configurations we need to
// price look cheap, so they record what was seen going past instead.
func TestRunRecordFallsBackToObservedUsage(t *testing.T) {
	cfg := metricsConfig(t, metricsOff)
	rc := runContext{
		issue: 12, reason: reasonImplement, outcome: outcomeNothing,
		started: time.Date(2026, 8, 24, 10, 15, 0, 0, time.UTC),
		ended:   time.Date(2026, 8, 24, 10, 15, 30, 0, time.UTC),
	}
	rep := runReport{
		sessionID: "sess-crash", exitCode: 7, turns: -1,
		observed:      tokenCounts{In: 8, Out: 9, CacheRead: 10, CacheWrite: 11},
		observedTurns: 2,
	}
	got := decode(t, newRunRecord(cfg, rc, rep))

	if got["usage_source"] != usageObserved {
		t.Errorf("usage_source = %v, want %q so analysis treats it as an approximation",
			got["usage_source"], usageObserved)
	}
	if got["status"] != "crash" || got["exit_code"] != float64(7) {
		t.Errorf("a crash should keep its status and exit code, got %v / %v", got["status"], got["exit_code"])
	}
	if tokens := got["tokens"].(map[string]any); tokens["out"] != float64(9) {
		t.Errorf("tokens = %v, want the observed tally", tokens)
	}
	// A negative turn count would poison every sum downstream.
	if got["turns"] != float64(2) {
		t.Errorf("turns = %v, want the observed count", got["turns"])
	}
	if got["wall_ms"] != float64(30000) {
		t.Errorf("wall_ms = %v, want it timed from the clock when no result reported one", got["wall_ms"])
	}
	if _, ok := got["model_usage"]; ok {
		t.Error("model_usage should be omitted when the run never reported one")
	}
}

// A result that reports no tokens at all is not evidence that none were spent;
// stamping that zero "result" would price every failing run at nothing.
func TestRunRecordDistrustsAResultWithNoTokens(t *testing.T) {
	cfg := metricsConfig(t, metricsOff)
	rc := runContext{issue: 12, started: time.Now(), ended: time.Now()}
	rep := runReport{
		hasResult: true, isError: true, subtype: "error_during_execution", turns: 4,
		observed: tokenCounts{In: 8, Out: 9},
	}
	got := decode(t, newRunRecord(cfg, rc, rep))

	if got["usage_source"] != usageObserved {
		t.Errorf("usage_source = %v, want %q when the result carried no usage block",
			got["usage_source"], usageObserved)
	}
	if tokens := got["tokens"].(map[string]any); tokens["out"] != float64(9) {
		t.Errorf("tokens = %v, want the observed tally rather than an authoritative zero", tokens)
	}
	// The result did report these, so they stand.
	if got["turns"] != float64(4) || got["status"] != "error" {
		t.Errorf("the rest of the result should survive, got %v / %v", got["turns"], got["status"])
	}
}

func TestRunStatusPrecedence(t *testing.T) {
	// Precedence matters because these overlap: an interrupted run is also a
	// nonzero exit, and a stalled one was killed.
	cases := []struct {
		name string
		rep  runReport
		want string
	}{
		{"no-skill beats an interrupt", runReport{skillMissing: true, interrupted: true, stalled: true, exitCode: -1}, "no-skill"},
		{"interrupt beats a stall and the exit code", runReport{interrupted: true, stalled: true, exitCode: -1}, "interrupted"},
		{"stall beats the exit code", runReport{stalled: true, exitCode: -1}, "stalled"},
		{"crash", runReport{exitCode: 7}, "crash"},
		{"result error", runReport{hasResult: true, isError: true, subtype: "error_max_turns"}, "error"},
		{"no turns", runReport{hasResult: true, turns: 0}, "no-turns"},
		{"ok", runReport{hasResult: true, turns: 3}, "ok"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.rep.status(); got != c.want {
				t.Errorf("status = %q, want %q", got, c.want)
			}
		})
	}
}

func TestIssueRecordHoldsOnlyTheTerminalOutcome(t *testing.T) {
	dir := t.TempDir()
	cfg := metricsConfig(t, dir)
	cfg.rec.recordIssue(cfg, 12, 34, issueMerged)

	lines := readRecords(t, dir, cfg.repo)
	if len(lines) != 1 {
		t.Fatalf("wrote %d records, want 1", len(lines))
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatalf("record is not JSON: %v", err)
	}
	for key, want := range map[string]any{
		"v": float64(recordVersion), "kind": "issue", "repo": cfg.repo,
		"issue": float64(12), "pr": float64(34), "outcome": issueMerged, "tag": "baseline",
	} {
		if got[key] != want {
			t.Errorf("record[%q] = %v, want %v", key, got[key], want)
		}
	}
	// Everything summable is derived from run records at read time; a rollup
	// here would have to survive restarts, and would be wrong when it didn't.
	for _, forbidden := range []string{"cost_usd", "runs", "turns", "tokens"} {
		if _, ok := got[forbidden]; ok {
			t.Errorf("issue records must not carry %q — it is derived from run records", forbidden)
		}
	}
}

func readRecords(t *testing.T, dir, repo string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, recordFile(repo)))
	if err != nil {
		t.Fatalf("reading records: %v", err)
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

func TestRecorderAppendsOneLinePerRecord(t *testing.T) {
	dir := t.TempDir()
	cfg := metricsConfig(t, dir)
	rc := runContext{issue: 12, reason: reasonImplement, outcome: outcomeOpenedPR,
		started: time.Now(), ended: time.Now()}

	cfg.rec.recordRun(cfg, rc, sampleReport())
	cfg.rec.recordRun(cfg, rc, sampleReport())
	cfg.rec.recordIssue(cfg, 12, 34, issueMerged)

	lines := readRecords(t, dir, cfg.repo)
	if len(lines) != 3 {
		t.Fatalf("wrote %d lines, want 3 (append-only, one object per line)", len(lines))
	}
	kinds := map[string]int{}
	for _, line := range lines {
		var v struct {
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			t.Fatalf("line is not self-contained JSON: %v\n%s", err, line)
		}
		kinds[v.Kind]++
	}
	if kinds["run"] != 2 || kinds["issue"] != 1 {
		t.Errorf("kinds = %v, want 2 runs and 1 issue", kinds)
	}
}

// The one discipline that lets telemetry coexist with "no state files": the
// drain loop never reads these, so a directory deleted mid-run is simply
// recreated on the next write.
func TestRecorderRecreatesADeletedDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "metrics")
	cfg := metricsConfig(t, dir)
	cfg.rec.recordIssue(cfg, 1, 0, issueMerged)
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("removing the directory mid-drain: %v", err)
	}
	cfg.rec.recordIssue(cfg, 2, 0, issueMerged)

	if lines := readRecords(t, dir, cfg.repo); len(lines) != 1 {
		t.Errorf("wrote %d lines after the delete, want 1", len(lines))
	}
}

func TestRecorderOffWritesNothing(t *testing.T) {
	home := homeDir(t)
	cfg := metricsConfig(t, metricsOff)
	if cfg.rec.enabled() {
		t.Fatal("-metrics off must disable the recorder")
	}
	cfg.rec.recordRun(cfg, runContext{issue: 1, started: time.Now(), ended: time.Now()}, sampleReport())
	cfg.rec.recordIssue(cfg, 1, 2, issueMerged)

	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatalf("reading home: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("-metrics off wrote %v — it must write nothing, anywhere", entries)
	}
}

// A config built without a recorder — every caller that does not care — must
// be safe to hand to any code path.
func TestNilRecorderIsSafe(t *testing.T) {
	var cfg config
	cfg.rec.recordRun(cfg, runContext{started: time.Now(), ended: time.Now()}, runReport{})
	cfg.rec.recordIssue(cfg, 1, 2, issueMerged)
	if cfg.rec.enabled() {
		t.Error("a nil recorder is disabled")
	}
}

func TestNewRecorderDefaultsUnderTheHomeDirectory(t *testing.T) {
	home := homeDir(t)
	rec := newRecorder("")
	if want := filepath.Join(home, ".backlog-drain", "metrics"); rec.dir != want {
		t.Errorf("default -metrics dir = %q, want %q", rec.dir, want)
	}
	// Resolving must not create anything: an -metrics off run and a
	// --help both go through here.
	if entries, _ := os.ReadDir(home); len(entries) != 0 {
		t.Errorf("resolving the default location created %v", entries)
	}
	if got := newRecorder("OFF"); got.enabled() {
		t.Error(`-metrics OFF should be as off as "off"`)
	}
}

// Losing a metric must never fail a run, and an unattended log is a cost too —
// so the warning comes once, not once per record.
func TestRecorderFailsQuietlyAndOnlyOnce(t *testing.T) {
	buf := captureLog(t)
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("in the way"), 0o644); err != nil {
		t.Fatalf("setting up: %v", err)
	}
	cfg := metricsConfig(t, blocked)
	for range 3 {
		cfg.rec.recordIssue(cfg, 1, 0, issueMerged)
	}
	if n := strings.Count(buf.String(), "run data not recorded"); n != 1 {
		t.Errorf("warned %d times, want exactly 1\ngot:\n%s", n, buf.String())
	}
}

// The files name private repositories and what they cost, so they are the
// operator's to read and nobody else's on a shared machine.
func TestRecordsAreNotWorldReadable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits are not how Windows decides this")
	}
	dir := filepath.Join(t.TempDir(), "metrics")
	cfg := metricsConfig(t, dir)
	cfg.rec.recordIssue(cfg, 1, 2, issueMerged)

	for _, path := range []string{dir, filepath.Join(dir, recordFile(cfg.repo))} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if mode := info.Mode().Perm(); mode&0o077 != 0 {
			t.Errorf("%s is mode %04o — group and other must have no access", path, mode)
		}
	}
}

func TestRecordFilePartitionsPerRepository(t *testing.T) {
	cases := map[string]string{
		"scharissis/backlog-drain": "scharissis--backlog-drain.jsonl",
		"Owner/Repo.js":            "Owner--Repo.js.jsonl",
		// Every separator is folded away, so a slug is always one filename
		// component however odd the repo name is.
		"weird name/../repo": "weird_name--..--repo.jsonl",
		"back\\slash":        "back_slash.jsonl",
		"":                   "unknown.jsonl",
	}
	for repo, want := range cases {
		if got := recordFile(repo); got != want {
			t.Errorf("recordFile(%q) = %q, want %q", repo, got, want)
		}
	}
}

func TestToolsHashDistinguishesAllowlists(t *testing.T) {
	a := toolsHash(resolveTools(defaultTools, ""))
	if a != toolsHash(defaultTools) {
		t.Error("the same list must hash the same, or comparisons across runs mean nothing")
	}
	if a == toolsHash(resolveTools(defaultTools, "Bash(bazel:*)")) {
		t.Error("a widened allowlist must hash differently — it changes what a run may do")
	}
	if len(a) != 8 {
		t.Errorf("tools hash %q should be 8 hex digits", a)
	}
}
