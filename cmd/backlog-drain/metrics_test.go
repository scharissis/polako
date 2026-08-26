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
		drainID:        "d1d2d3d4",
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
		"drain":           "d1d2d3d4",
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
	cfg.rec.recordIssue(cfg, 12, 34, issueMerged, prFacts{})

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
		"drain": cfg.drainID,
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
	cfg.rec.recordIssue(cfg, 12, 34, issueMerged, prFacts{})

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
	cfg.rec.recordIssue(cfg, 1, 0, issueMerged, prFacts{})
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("removing the directory mid-drain: %v", err)
	}
	cfg.rec.recordIssue(cfg, 2, 0, issueMerged, prFacts{})

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
	cfg.rec.recordIssue(cfg, 1, 2, issueMerged, prFacts{})

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
	cfg.rec.recordIssue(cfg, 1, 2, issueMerged, prFacts{})
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
		cfg.rec.recordIssue(cfg, 1, 0, issueMerged, prFacts{})
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
	cfg.rec.recordIssue(cfg, 1, 2, issueMerged, prFacts{})

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

// The id's whole job is telling apart the drains a timestamp cannot: two
// started in the same second, and one running while another finishes.
func TestDrainIDsAreShortAndNeverRepeat(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		id := newDrainID()
		if len(id) != 8 || strings.Trim(id, "0123456789abcdef") != "" {
			t.Fatalf("drain id %q should be 8 hex digits — it goes in a log line to be retyped", id)
		}
		if seen[id] {
			t.Fatalf("drain id %q came back twice; two drains sharing one make -drain meaningless", id)
		}
		seen[id] = true
	}
}

// Every record one process writes carries the same id, whatever kind it is —
// that is what makes `stats -drain` a report on a drain rather than on some of
// its runs.
func TestEveryRecordOneDrainWritesCarriesItsID(t *testing.T) {
	dir := t.TempDir()
	cfg := metricsConfig(t, dir)
	rc := runContext{issue: 12, reason: reasonImplement, outcome: outcomeOpenedPR,
		started: time.Now(), ended: time.Now()}
	cfg.rec.recordRun(cfg, rc, sampleReport())
	cfg.rec.recordIssue(cfg, 12, 34, issueMerged, prFacts{})

	// A second process, same directory, same repository: the ordinary case of
	// a drain restarted after the first was killed.
	other := metricsConfig(t, dir)
	other.drainID = "0f0f0f0f"
	other.rec.recordRun(other, rc, sampleReport())

	lines := readRecords(t, dir, cfg.repo)
	if len(lines) != 3 {
		t.Fatalf("wrote %d lines, want 3", len(lines))
	}
	ids := map[string]int{}
	for _, line := range lines {
		var got struct {
			Drain string `json:"drain"`
		}
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Fatalf("record is not JSON: %v", err)
		}
		ids[got.Drain]++
	}
	if ids[cfg.drainID] != 2 || ids[other.drainID] != 1 {
		t.Errorf("ids across the file = %v, want 2 from %q and 1 from %q",
			ids, cfg.drainID, other.drainID)
	}
}

// The enrichment is the one thing an issue record holds that no run record
// could reconstruct: what GitHub says the PR turned out to be.
func TestIssueRecordCarriesTheGitHubEnrichment(t *testing.T) {
	dir := t.TempDir()
	cfg := metricsConfig(t, dir)
	cfg.rec.recordIssue(cfg, 12, 34, issueMerged, prFacts{
		Additions: 412, Deletions: 38, ChangedFiles: 7, Reviews: 2,
		Opened: "2026-08-24T10:34:02Z", Merged: "2026-08-24T14:02:00Z",
	})

	var got map[string]any
	if err := json.Unmarshal([]byte(readRecords(t, dir, cfg.repo)[0]), &got); err != nil {
		t.Fatalf("record is not JSON: %v", err)
	}
	for key, want := range map[string]any{
		"additions": float64(412), "deletions": float64(38), "changed_files": float64(7),
		"reviews":   float64(2),
		"pr_opened": "2026-08-24T10:34:02Z", "pr_merged": "2026-08-24T14:02:00Z",
	} {
		if got[key] != want {
			t.Errorf("record[%q] = %v, want %v", key, got[key], want)
		}
	}
	// The reviews are counted, never quoted: whatever a reviewer wrote is
	// exactly the text these files do not carry.
	if _, ok := got["review_bodies"]; ok {
		t.Error("a record must not carry review text")
	}
}

// A PR that never existed, a lookup that failed, a drain with -metrics off:
// the outcome is recorded either way, in the shape every reader written before
// the enrichment already knows.
func TestIssueRecordOmitsAnEnrichmentItNeverGot(t *testing.T) {
	dir := t.TempDir()
	cfg := metricsConfig(t, dir)
	cfg.rec.recordIssue(cfg, 12, 0, issueNeedsHuman, prFacts{})

	var got map[string]any
	if err := json.Unmarshal([]byte(readRecords(t, dir, cfg.repo)[0]), &got); err != nil {
		t.Fatalf("record is not JSON: %v", err)
	}
	if got["outcome"] != issueNeedsHuman {
		t.Errorf("outcome = %v, want the record written regardless", got["outcome"])
	}
	for _, key := range []string{"additions", "deletions", "changed_files", "reviews", "pr_opened", "pr_merged"} {
		if _, ok := got[key]; ok {
			t.Errorf("record carries %q with nothing to put in it", key)
		}
	}
}

func TestIssueTallySumsTheRunsThisDrainSaw(t *testing.T) {
	var tally issueTally
	tally.add(runRecord{Outcome: outcomeQuestions, CostUSD: 1.25, WallMS: 1200000,
		Tokens: tokenCounts{In: 2000, Out: 30000, CacheRead: 4000000, CacheWrite: 200000}})
	tally.add(runRecord{Outcome: outcomeOpenedPR, CostUSD: 2.50, WallMS: 1800000,
		Tokens: tokenCounts{In: 3000, Out: 50000, CacheRead: 6000000, CacheWrite: 300000}})

	if tally.runs != 2 || tally.questions != 1 {
		t.Errorf("tally = %d runs / %d question rounds, want 2 and 1", tally.runs, tally.questions)
	}
	if tally.costUSD != 3.75 || tally.wallMS != 3000000 {
		t.Errorf("tally = $%v over %dms, want $3.75 over 3000000ms", tally.costUSD, tally.wallMS)
	}
	if want := int64(10585000); tally.tokens.total() != want {
		t.Errorf("tokens = %d, want %d", tally.tokens.total(), want)
	}
}

// -post-summary works with -metrics off — the escape hatch for an operator who
// wants no local files at all — so the record has to be built whether or not
// anything is written.
func TestRecordRunReturnsTheRecordEvenWithMetricsOff(t *testing.T) {
	cfg := metricsConfig(t, metricsOff)
	rec := cfg.rec.recordRun(cfg, runContext{issue: 12, reason: reasonImplement,
		outcome: outcomeOpenedPR, started: time.Now(), ended: time.Now()}, sampleReport())
	if rec.CostUSD != 4.12 || rec.Outcome != outcomeOpenedPR {
		t.Errorf("record = $%v / %q, want the run's numbers back", rec.CostUSD, rec.Outcome)
	}
}

func TestSummaryCommentReportsTheNumbersAndSaysWhatTheyCover(t *testing.T) {
	got := summaryComment(issueTally{runs: 3, questions: 1, costUSD: 6.12, wallMS: 8040000,
		tokens: tokenCounts{In: 2000, Out: 40000, CacheRead: 12000000, CacheWrite: 400000}})
	for _, want := range []string{"3 runs", "1 question round", "12.4M tokens", "$6.12", "2h14m"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary is missing %q:\n%s", want, got)
		}
	}
	// It is posted where other people read it, so it says which runs it
	// covers and that the dollars are the CLI's pricing rather than a bill.
	for _, want := range []string{"this drain supervised", "API-equivalent pricing"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary does not say %q:\n%s", want, got)
		}
	}
	// Every run reported a result, so there is nothing to qualify.
	if strings.Contains(got, "undercount") {
		t.Errorf("summary hedges numbers that came from result events:\n%s", got)
	}
}

// The bias this guards against is the same one the recorder guards against,
// one audience further out: a run that crashed, stalled or was interrupted
// reports no cost at all, and an unqualified $0.00 on a PR reads as free work.
func TestSummaryCommentSaysWhenItsNumbersAreUndercounts(t *testing.T) {
	var tally issueTally
	tally.add(runRecord{Outcome: outcomeNothing, UsageSource: usageObserved,
		Tokens: tokenCounts{In: 500, Out: 6000}})
	tally.add(runRecord{Outcome: outcomeOpenedPR, UsageSource: usageResult, CostUSD: 3.00,
		Tokens: tokenCounts{In: 2500, Out: 42000}})

	got := summaryComment(tally)
	if !strings.Contains(got, "1 of them never reported a cost") {
		t.Errorf("summary does not own up to the crashed run:\n%s", got)
	}
	if !strings.Contains(got, "undercounts") {
		t.Errorf("summary does not say the numbers are low:\n%s", got)
	}
}
