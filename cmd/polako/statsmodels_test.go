package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// writeRecords is sessionFixtureDir over a block of JSONL, which reads
// better here than one argument per record.
func writeRecords(t *testing.T, body string) string {
	t.Helper()
	return sessionFixtureDir(t, strings.Split(strings.TrimSuffix(body, "\n"), "\n")...)
}

func statsEpochs(t *testing.T, dir string) []statsDocEpoch {
	t.Helper()
	var doc struct {
		Epochs []statsDocEpoch `json:"epochs"`
	}
	out := stats(t, "-metrics", dir, "-json")
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("output did not parse as JSON: %v\n%s", err, out)
	}
	if doc.Epochs == nil {
		t.Fatalf("epochs is absent or null, want an array always:\n%s", out)
	}
	return doc.Epochs
}

// Two inherited models: the line names both, with the CLI version each first
// appeared on. A run that asked for a model says nothing about what inherit
// resolves to, so its model stays out even though it's a third one.
func TestStatsListsTheModelsInheritedRunsRanOn(t *testing.T) {
	t.Parallel()
	dir := writeRecords(t, `{"v":1,"kind":"run","ts":"2026-08-28T09:00:00Z","repo":"r/r","issue":1,"reason":"implement","status":"ok","model":"claude-sonnet-5","claude_version":"2.1.245"}
{"v":1,"kind":"run","ts":"2026-09-10T09:00:00Z","repo":"r/r","issue":2,"reason":"implement","status":"ok","model":"claude-sonnet-5","claude_version":"2.1.260"}
{"v":1,"kind":"run","ts":"2026-09-12T09:00:00Z","repo":"r/r","issue":3,"reason":"implement","status":"ok","model":"claude-haiku-4-5","requested_model":"haiku"}
{"v":1,"kind":"run","ts":"2026-09-23T09:00:00Z","repo":"r/r","issue":4,"reason":"implement","status":"ok","model":"claude-opus-5-5[1m]","claude_version":"2.1.280"}
`)
	want := "models claude-sonnet-5 2026-08-28 → 2026-09-10, 2 runs, from CLI 2.1.245; " +
		"claude-opus-5-5[1m] 2026-09-23 → 2026-09-23, 1 run, from CLI 2.1.280"
	if out := stats(t, "-metrics", dir); !hasLine(out, want) {
		t.Errorf("models line differs, want %q in:\n%s", want, out)
	}

	epochs := statsEpochs(t, dir)
	if len(epochs) != 2 {
		t.Fatalf("epochs = %+v, want two", epochs)
	}
	if e := epochs[0]; e.Model != "claude-sonnet-5" || e.First != "2026-08-28T09:00:00Z" ||
		e.Last != "2026-09-10T09:00:00Z" || e.Runs != 2 || e.FirstClaudeVersion != "2.1.245" {
		t.Errorf("epochs[0] = %+v", e)
	}
	if e := epochs[1]; e.Model != "claude-opus-5-5[1m]" || e.Runs != 1 || e.FirstClaudeVersion != "2.1.280" {
		t.Errorf("epochs[1] = %+v", e)
	}
}

// A model whose first run recorded no CLI version says nothing about one,
// rather than borrowing a later run's.
func TestStatsModelsNeverBackfillTheFirstVersion(t *testing.T) {
	t.Parallel()
	dir := writeRecords(t, `{"v":1,"kind":"run","ts":"2026-08-28T09:00:00Z","repo":"r/r","issue":1,"reason":"implement","status":"ok","model":"claude-sonnet-5"}
{"v":1,"kind":"run","ts":"2026-09-10T09:00:00Z","repo":"r/r","issue":2,"reason":"implement","status":"ok","model":"claude-sonnet-5","claude_version":"2.1.260"}
{"v":1,"kind":"run","ts":"2026-09-23T09:00:00Z","repo":"r/r","issue":3,"reason":"implement","status":"ok","model":"claude-opus-5-5[1m]","claude_version":"2.1.280"}
`)
	if e := statsEpochs(t, dir)[0]; e.FirstClaudeVersion != "" {
		t.Errorf("first_claude_version = %q, want none — the first run recorded none", e.FirstClaudeVersion)
	}
}

// An unparsable first ts drops first, never last, which the text line still
// prints.
func TestStatsModelsJSONKeepsLastWithoutFirst(t *testing.T) {
	t.Parallel()
	dir := writeRecords(t, `{"v":1,"kind":"run","ts":"not a time","repo":"r/r","issue":1,"reason":"implement","status":"ok","model":"claude-sonnet-5"}
{"v":1,"kind":"run","ts":"2026-09-10T09:00:00Z","repo":"r/r","issue":2,"reason":"implement","status":"ok","model":"claude-sonnet-5"}
{"v":1,"kind":"run","ts":"2026-09-23T09:00:00Z","repo":"r/r","issue":3,"reason":"implement","status":"ok","model":"claude-opus-5-5[1m]"}
`)
	for _, e := range statsEpochs(t, dir) {
		if e.Model == "claude-sonnet-5" && (e.First != "" || e.Last != "2026-09-10T09:00:00Z") {
			t.Errorf("epoch = %+v, want no first and last 2026-09-10T09:00:00Z", e)
		}
	}
}

// A resume can report the bare id where the fresh run reported [1m]. Counting
// it would print one model as two.
func TestStatsModelsIgnoresResumes(t *testing.T) {
	t.Parallel()
	dir := writeRecords(t, `{"v":1,"kind":"run","ts":"2026-09-23T09:00:00Z","repo":"r/r","issue":1,"reason":"implement","status":"crash","model":"claude-opus-5-5[1m]"}
{"v":1,"kind":"run","ts":"2026-09-23T09:10:00Z","repo":"r/r","issue":1,"reason":"resume","status":"ok","model":"claude-opus-5-5"}
{"v":1,"kind":"run","ts":"2026-09-23T10:00:00Z","repo":"r/r","issue":1,"reason":"unfinished","status":"ok","model":"claude-opus-5-5"}
{"v":1,"kind":"run","ts":"2026-09-23T11:00:00Z","repo":"r/r","issue":1,"reason":"checks","resumed_from":"s1","status":"ok","model":"claude-opus-5-5"}
`)
	if out := stats(t, "-metrics", dir); hasLine(out, "models claude-") {
		t.Errorf("a resume's bare id printed a second model:\n%s", out)
	}
	if epochs := statsEpochs(t, dir); len(epochs) != 0 {
		t.Errorf("epochs = %+v, want []", epochs)
	}
}

// One model is nothing to report.
func TestStatsModelsSilentOnOneModel(t *testing.T) {
	t.Parallel()
	dir := writeRecords(t, `{"v":1,"kind":"run","ts":"2026-09-23T09:00:00Z","repo":"r/r","issue":1,"reason":"implement","status":"ok","model":"claude-opus-5-5[1m]"}
{"v":1,"kind":"run","ts":"2026-09-24T09:00:00Z","repo":"r/r","issue":2,"reason":"implement","status":"ok","model":"claude-opus-5-5[1m]"}
`)
	if out := stats(t, "-metrics", dir); hasLine(out, "models claude-") {
		t.Errorf("one model printed a models line:\n%s", out)
	}
}

// The init event's version is the CLI that ran; the preflight snapshot is
// only the fallback, since a mid-shift update outdates it.
func TestRecordTakesClaudeVersionFromTheInitEvent(t *testing.T) {
	t.Parallel()
	cfg := config{claudeVersion: "2.1.200"}
	rc := runContext{started: time.Now(), ended: time.Now()}

	var rep runReport
	rep.observe(streamEvent{Type: "system", Subtype: "init", Model: "m", ClaudeCodeVersion: "2.1.280"})
	if got := newRunRecord(cfg, rc, rep).ClaudeVersion; got != "2.1.280" {
		t.Errorf("claude_version = %q, want the init event's 2.1.280", got)
	}

	var old runReport
	old.observe(streamEvent{Type: "system", Subtype: "init", Model: "m"})
	if got := newRunRecord(cfg, rc, old).ClaudeVersion; got != "2.1.200" {
		t.Errorf("claude_version = %q, want the preflight snapshot 2.1.200", got)
	}
}
