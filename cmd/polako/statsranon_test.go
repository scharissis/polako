package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// firstParty reads as anthropic, an unprobed provider passes through verbatim,
// and a CLI without `auth status` leaves the field empty rather than failing.
func TestClaudeProviderReadsOnlyTheProvider(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ reply, want string }{
		{"", "anthropic"}, // the fake's default, firstParty
		{"bedrock", "bedrock"},
		{"fail", ""},
	} {
		cfg := fakeClaudeConfig(t, "stream")
		if tc.reply != "" {
			setFakeEnv(&cfg, fakeAuthEnv, tc.reply)
		}
		if got := claudeProvider(context.Background(), cfg); got != tc.want {
			t.Errorf("auth reply %q: provider = %q, want %q", tc.reply, got, tc.want)
		}
	}
}

// The same reply carries the account's email and org. Neither may reach a
// record: run and plan records both, marshalled the way the recorder writes.
func TestProviderRecordCarriesNoAccountDetails(t *testing.T) {
	t.Parallel()
	cfg := fakeClaudeConfig(t, "stream")
	cfg.provider = claudeProvider(context.Background(), cfg)
	for _, rec := range []any{
		newRunRecord(cfg, runContext{}, sampleReport()),
		newPlanRecord(cfg, sampleReport(), samplePlanFacts()),
	} {
		b, err := json.Marshal(rec)
		if err != nil {
			t.Fatalf("marshalling %T: %v", rec, err)
		}
		if !strings.Contains(string(b), `"provider":"anthropic"`) {
			t.Errorf("%T lost the provider: %s", rec, b)
		}
		for _, leak := range []string{fakeAuthEmail, "org-123", "Fake Org"} {
			if strings.Contains(string(b), leak) {
				t.Errorf("%T carries %q from auth status: %s", rec, leak, b)
			}
		}
	}
}

// Most runs first; a tie keeps the order each first appeared. Every run
// counts — asked-for models, resumes and old records without a provider too.
func TestRanOnGroupsEveryRun(t *testing.T) {
	t.Parallel()
	dir := writeRecords(t, `{"v":1,"kind":"run","ts":"2026-09-20T09:00:00Z","repo":"r/r","issue":1,"reason":"implement","status":"ok","model":"claude-sonnet-5"}
{"v":1,"kind":"run","ts":"2026-09-21T09:00:00Z","repo":"r/r","issue":2,"reason":"implement","status":"ok","provider":"anthropic","model":"claude-opus-5","requested_model":"opus","requested_effort":"high"}
{"v":1,"kind":"run","ts":"2026-09-22T09:00:00Z","repo":"r/r","issue":3,"reason":"implement","status":"ok","provider":"anthropic","model":"claude-opus-5","requested_model":"opus","requested_effort":"high"}
{"v":1,"kind":"run","ts":"2026-09-22T10:00:00Z","repo":"r/r","issue":3,"reason":"resume","status":"ok","provider":"bedrock","resumed_from":"s"}
`)
	out := stats(t, "-metrics", dir)
	want := "ran on anthropic · claude-opus-5 · high, 2 runs; unrecorded · claude-sonnet-5 · inherited, 1 run; bedrock · unrecorded · inherited, 1 run"
	if !hasLine(out, want) {
		t.Errorf("ran on line differs, want %q:\n%s", want, out)
	}

	var doc statsDoc
	if err := json.Unmarshal([]byte(stats(t, "-metrics", dir, "-json", "-runs")), &doc); err != nil {
		t.Fatalf("stats -json: %v", err)
	}
	if len(doc.Runs.RanOn) != 3 || doc.Runs.RanOn[0] != (statsDocRanOn{Provider: "anthropic", Model: "claude-opus-5", Effort: "high", Runs: 2}) {
		t.Errorf("runs.ran_on = %+v, want the text line's three, most runs first", doc.Runs.RanOn)
	}
	if r := doc.RunLog[3]; r.Provider != "bedrock" || r.Model != "unrecorded" || r.Effort != "inherited" {
		t.Errorf("run_log[3] = %+v, want bedrock · unrecorded · inherited", r)
	}
}
