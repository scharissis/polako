package main

import (
	"strings"
	"testing"
	"time"
)

func TestLogEventRendersProgressLines(t *testing.T) {
	t.Parallel()
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
	el := eventLog{u: testUI(t)}
	for _, e := range events {
		if ev, ok := parseEvent([]byte(e)); ok {
			el.event(ev)
		}
	}

	out := buf.String()
	for _, want := range []string{
		"session started (model claude-opus-5)",
		"Gathering context on issue #48. Starting now.",
		"→ Bash: gh issue view 48",
		"→ Write: PLAN.md",
		// The result text must reach the log: for a run the CLI answered
		// itself, it is the only place the diagnosis ever appears. The
		// "finished" line is dispatchClaude's to emit, not event's — see
		// TestFinishLineRendersFromTheReport.
		"Unknown skill: polako:implement-issue",
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
	t.Parallel()
	buf := captureLog(t)
	ev, ok := parseEvent([]byte(
		`{"type":"system","subtype":"init","model":"claude-opus-5","session_id":"0f8c1e22-6b4d-4a01-9c3e-2d5f77a1b0e9"}`))
	if !ok {
		t.Fatal("init event should parse")
	}
	(&eventLog{u: testUI(t)}).event(ev)
	if want := "session started (model claude-opus-5, session 0f8c1e22-6b4d-4a01-9c3e-2d5f77a1b0e9)"; !strings.Contains(buf.String(), want) {
		t.Errorf("output missing %q\ngot:\n%s", want, buf.String())
	}
}

// finishLine renders the run's single finish milestone from the report, so a
// run whose review gate woke it five times still finishes once — with the
// per-turn fields summed across every result event, not just the last one's
// (issue #227).
func TestFinishLineRendersFromTheReport(t *testing.T) {
	t.Parallel()
	feed := func(lines ...string) *runReport {
		rep := &runReport{turns: -1}
		for _, l := range lines {
			ev, ok := parseEvent([]byte(l))
			if !ok {
				t.Fatalf("parseEvent rejected %s", l)
			}
			rep.observe(ev)
		}
		return rep
	}

	// The whole run's standing: two results streamed, turns and wall are the
	// sum of both prompt turns (16+74, 61s+1141s) while total_cost_usd —
	// process-cumulative and whole on every event — stays last-wins, not
	// summed to $41.57.
	sev, line := finishLine(feed(
		`{"type":"result","subtype":"success","num_turns":16,"duration_ms":61000,"total_cost_usd":12.00}`,
		`{"type":"result","subtype":"success","num_turns":74,"duration_ms":1141000,"total_cost_usd":29.57}`,
	))
	if sev != sevSuccess {
		t.Errorf("sev = %v, want sevSuccess", sev)
	}
	if want := "[claude] finished (ok) — 90 turns, 20m2s, $29.57"; line != want {
		t.Errorf("line = %q, want %q", line, want)
	}

	// An error subtype is named; is_error is the authority even when the
	// subtype still says "success".
	sev, line = finishLine(feed(`{"type":"result","subtype":"error_max_turns","num_turns":9,"is_error":true}`))
	if sev != sevError {
		t.Errorf("sev = %v, want sevError", sev)
	}
	if !strings.Contains(line, "finished (ERROR: error_max_turns)") {
		t.Errorf("line = %q, want it to flag the error subtype", line)
	}

	sev, line = finishLine(feed(`{"type":"result","subtype":"success","is_error":true}`))
	if sev != sevError || strings.Contains(line, "success") {
		t.Errorf("line = %q (sev %v), want a bare ERROR with no self-contradicting subtype", line, sev)
	}
}

// Issue #227: a run with background subagents streams one result event per
// dequeued prompt, all at exit. The per-prompt-turn fields — num_turns, the
// two durations, the top-level usage block — accumulate across them; the
// process-cumulative fields — total_cost_usd, modelUsage — stay last-wins,
// since every event already carries the whole process's figure. Cumulative
// over the process, not the session: see stream.go's result case, and
// TestIssueTallySumsBothHalvesOfAResumedSession for the other half.
func TestObserveSumsPerTurnFieldsAcrossResultEvents(t *testing.T) {
	t.Parallel()
	rep := runReport{turns: -1}
	rep.observe(streamEvent{Type: "result", Subtype: "success",
		DurationMS: 61000, DurationAPIMS: 40000, NumTurns: 16, TotalCost: 29.57,
		Usage:      streamUsage{Input: 10, Output: 100, CacheRead: 1000, CacheWrite: 5},
		ModelUsage: map[string]streamModelUsage{"claude-opus-5": {Output: 1000, CostUSD: 29.57}}})
	rep.observe(streamEvent{Type: "result", Subtype: "success",
		DurationMS: 1141000, DurationAPIMS: 800000, NumTurns: 74, TotalCost: 29.57,
		Usage:      streamUsage{Input: 20, Output: 200, CacheRead: 2000, CacheWrite: 10},
		ModelUsage: map[string]streamModelUsage{"claude-opus-5": {Output: 1000, CostUSD: 29.57}}})

	if rep.turns != 90 {
		t.Errorf("turns = %d, want 16+74=90", rep.turns)
	}
	if rep.wallMS != 1202000 || rep.apiMS != 840000 {
		t.Errorf("wall/api = %d/%d ms, want 1202000/840000 summed", rep.wallMS, rep.apiMS)
	}
	if want := (tokenCounts{In: 30, Out: 300, CacheRead: 3000, CacheWrite: 15}); rep.usage != want {
		t.Errorf("usage = %+v, want the sum %+v", rep.usage, want)
	}
	if rep.costUSD != 29.57 {
		t.Errorf("costUSD = %v, want 29.57 last-wins, not doubled", rep.costUSD)
	}
	if got := rep.modelUsage["claude-opus-5"]; got.Out != 1000 || got.CostUSD != 29.57 {
		t.Errorf("modelUsage entry = %+v, want last-wins (out 1000, $29.57), not summed", got)
	}
}

// One parse per line feeds both the progress log and the run report, so the
// session ID every consumer depends on now comes out of parseEvent.
func TestParseEventReadsTheSessionAndRejectsJunk(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	cases := []struct {
		in, want string
	}{
		{`{"command":"go test ./..."}`, ": go test ./..."},
		{`{"file_path":"PLAN.md","content":"long body"}`, ": PLAN.md"},
		{`{"pattern":"func main"}`, ": func main"},
		{`{"content":"nothing addressable"}`, ""},
		{`not json`, ""},
		{`{"command":""}`, ""},
		{`{"skill":"code-review","args":"high --fix issue-77"}`, ": code-review high --fix issue-77"},
		{`{"skill":"code-review"}`, ": code-review"},
		{`{"skill":"code-review","args":""}`, ": code-review"},
		{`{"skill":42}`, ""},
	}
	for _, c := range cases {
		if got := toolDetail([]byte(c.in)); got != c.want {
			t.Errorf("toolDetail(%s) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestClipFlattensAndTruncates(t *testing.T) {
	t.Parallel()
	if got := clip("  one\n two\tthree  ", 40); got != "one two three" {
		t.Errorf("clip should collapse whitespace, got %q", got)
	}
	got := clip(strings.Repeat("x", 50), 10)
	if want := strings.Repeat("x", 10) + "…"; got != want {
		t.Errorf("clip = %q, want %q", got, want)
	}
}

func TestHeartbeatLine(t *testing.T) {
	t.Parallel()
	cases := []struct {
		elapsed  time.Duration
		toolUses int
		phase    stage
		want     string
	}{
		{6 * time.Minute, 59, stageStudy, "still working — 6m in, 59 tool calls, reading the code"},
		{12*time.Minute + 20*time.Second, 118, stageImplement, "still working — 12m in, 118 tool calls, implementing"},
		{5 * time.Minute, 1, stageReview, "still working — 5m in, 1 tool call, running the review gate"},
		{2 * time.Minute, 14, stageFiling, "still working — 2m in, 14 tool calls, filing proposals"},
		// No stage recognised yet: no clause, not an empty one.
		{5 * time.Minute, 3, stageNone, "still working — 5m in, 3 tool calls"},
	}
	for _, c := range cases {
		if got := heartbeatLine(c.elapsed, c.toolUses, c.phase); got != c.want {
			t.Errorf("heartbeatLine(%s, %d, %v) = %q, want %q", c.elapsed, c.toolUses, c.phase, got, c.want)
		}
	}
}
