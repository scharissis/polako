package main

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"
)

// The detector has to catch the CLI's refusal and must not catch a run that
// merely quotes one — a live risk, not a hypothetical: issue #67 on this very
// repository carries the refusal verbatim in its body, and a run implementing
// it can end with a final message that repeats the text mid-sentence.
func TestLimitRefusalMatchesTheRefusalNotAQuote(t *testing.T) {
	refusals := []string{
		"You've hit your session limit · resets 10:50am (Europe/London)",
		"You've hit your usage limit · resets 9pm (America/New_York)",
		"Session limit reached ∙ resets 6pm",
		"5-hour limit reached ∙ resets 3am",
		"Weekly limit reached ∙ resets Oct 14, 10am",
		"  **You've hit your session limit** · resets 10:50am", // markdown wrapping
	}
	for _, msg := range refusals {
		if !limitRefusal(msg) {
			t.Errorf("limitRefusal(%q) = false, want true", msg)
		}
	}
	quotes := []string{
		"The run failed after the CLI printed: You've hit your session limit",
		"Implemented the wait for \"session limit reached\" messages and opened a PR.",
		"",
		"exit status 1",
	}
	for _, msg := range quotes {
		if limitRefusal(msg) {
			t.Errorf("limitRefusal(%q) = true: a quote must not put the drain to sleep", msg)
		}
	}
}

func TestLimitResetReadsTheRefusalsClock(t *testing.T) {
	london, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Fatalf("loading Europe/London (time/tzdata is imported, so this must work everywhere): %v", err)
	}
	// The morning issue #67 was reproduced on: 08:56 in London.
	now := time.Date(2026, 8, 27, 8, 56, 0, 0, london)
	at := func(y int, mo time.Month, d, h, min int) time.Time {
		return time.Date(y, mo, d, h, min, 0, 0, london)
	}
	cases := []struct {
		name string
		msg  string
		want time.Time
		ok   bool
	}{
		{"the observed refusal", "You've hit your session limit · resets 10:50am (Europe/London)",
			at(2026, 8, 27, 10, 50), true},
		{"pm converts", "You've hit your session limit · resets 9:10pm (Europe/London)",
			at(2026, 8, 27, 21, 10), true},
		{"no minutes", "Session limit reached ∙ resets 9pm (Europe/London)",
			at(2026, 8, 27, 21, 0), true},
		{"a time already behind the clock means tomorrow", "resets 8am (Europe/London)",
			at(2026, 8, 28, 8, 0), true},
		{"12pm is noon", "resets 12pm (Europe/London)", at(2026, 8, 27, 12, 0), true},
		{"12am is midnight", "resets 12am (Europe/London)", at(2026, 8, 28, 0, 0), true},
		{"no zone falls back to the caller's", "resets 9:10pm", at(2026, 8, 27, 21, 10), true},
		{"a zone this build cannot resolve", "resets 9pm (Atlantis/Lost)", time.Time{}, false},
		{"a dated reset is beyond this parser", "Weekly limit reached ∙ resets Oct 14, 10am",
			time.Time{}, false},
		{"no clause at all", "You've hit your session limit", time.Time{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := limitReset(tc.msg, now)
			if ok != tc.ok {
				t.Fatalf("limitReset(%q) ok = %v, want %v", tc.msg, ok, tc.ok)
			}
			if ok && !got.Equal(tc.want) {
				t.Errorf("limitReset(%q) = %v, want %v", tc.msg, got, tc.want)
			}
		})
	}

	// The next occurrence is computed in the refusal's zone, not the caller's:
	// 23:00 UTC is 18:00 in New York, so "resets 11:30pm" there is still
	// today — half past four in the morning, UTC.
	utcNow := time.Date(2026, 1, 15, 23, 0, 0, 0, time.UTC)
	got, ok := limitReset("resets 11:30pm (America/New_York)", utcNow)
	want := time.Date(2026, 1, 16, 4, 30, 0, 0, time.UTC)
	if !ok || !got.Equal(want) {
		t.Errorf("limitReset across zones = %v, %v; want %v", got, ok, want)
	}
}

// Issue #67's shape, fixed: the fresh run is refused over the session limit,
// and instead of burning resumes into a park the drain waits and then resumes
// the refused session, which finishes the job.
func TestDrainWaitsOutASessionLimitThenShips(t *testing.T) {
	buf := captureLog(t)
	cfg, path := drainConfig(t, "limitedthenships", &ghState{
		Issues: map[string]*fakeIssue{"1": {Open: true}},
	})

	if err := drain(context.Background(), cfg); err != nil {
		t.Fatalf("a session limit must not end the drain: %v", err)
	}

	st := finalGhState(t, path)
	if got := st.Issues["1"].Labels; slices.Contains(got, needsHumanLabel) {
		t.Errorf("issue 1 labels = %v: a limit is not the issue's fault, so it must not park", got)
	}
	if st.Issues["1"].Open {
		t.Error("issue 1 should have been closed after the resumed run's PR merged")
	}

	out := buf.String()
	// The fake's refusal deliberately names no reset time, so the drain has to
	// say it is polling — the parsed-clock wait cannot be exercised here
	// without sleeping the suite into real wall time, and limitReset's own
	// tests cover the parsing.
	if !strings.Contains(out, "over its usage limit") {
		t.Errorf("log never explains the wait:\n%s", out)
	}
	if !strings.Contains(out, "retrying every") {
		t.Errorf("log should say it is polling for the reset it could not read:\n%s", out)
	}
	if !strings.Contains(out, "--resume sess-limited") {
		t.Errorf("the refused session is the one that must be resumed:\n%s", out)
	}
}

// Five refusals in a row exceed -retries, and would have exhausted it long
// before the reset on the old path. They are not crashes, so they spend
// neither that budget nor the resume ceiling: the sixth attempt ships.
func TestDrainDoesNotSpendRetriesOnASessionLimit(t *testing.T) {
	captureLog(t)
	cfg, path := drainConfig(t, "limitedrepeatthenships", &ghState{
		Issues: map[string]*fakeIssue{"1": {Open: true}},
	})
	cfg.retries = 3

	if err := drain(context.Background(), cfg); err != nil {
		t.Fatalf("repeated limit refusals must not end the drain: %v", err)
	}

	st := finalGhState(t, path)
	if got := st.Issues["1"].Labels; slices.Contains(got, needsHumanLabel) {
		t.Errorf("issue 1 labels = %v: limit waits must not add up to a park", got)
	}
	if st.Issues["1"].Open {
		t.Error("issue 1 should have been closed after the sixth run's PR merged")
	}
	if st.ClaudeRuns != 6 {
		t.Errorf("claude ran %d times, want 6: five refusals waited out, one run that ships", st.ClaudeRuns)
	}
}
