package main

// Tests for -window (today/week/month/session) and the plan-cost-per-issue
// line (#140). Split from stats_test.go because most of this exercises pure
// functions (mondayStart, weekAnchorFromProbe, resolveWindowBounds,
// sessionAnchor) directly rather than the full report, and because the
// probe-integration cases need statsReport's lower-level entry point with a
// fake claudeBin — a different rig from the rest of that file.

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- mondayStart ---

func TestMondayStart(t *testing.T) {
	cases := map[string]time.Time{
		// A Wednesday rolls back to that week's Monday.
		"2026-08-26T15:00:00Z": time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC),
		// Monday itself is its own anchor, whatever the time of day.
		"2026-08-24T23:59:00Z": time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC),
		// Sunday rolls back to the *previous* Monday, not forward.
		"2026-08-30T09:00:00Z": time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC),
	}
	for in, want := range cases {
		now, err := time.Parse(time.RFC3339, in)
		if err != nil {
			t.Fatalf("parsing %q: %v", in, err)
		}
		if got := mondayStart(now, time.UTC); !got.Equal(want) {
			t.Errorf("mondayStart(%s) = %s, want %s", in, got, want)
		}
	}
}

// --- resolveWindowBounds: today/month, DST and month-end ---

func TestResolveWindowBoundsTodaySurvivesSpringForwardDST(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("loading America/New_York (time/tzdata is imported, so this must work everywhere): %v", err)
	}
	// 2026-03-08 is the US spring-forward date: clocks jump 2am to 3am, so
	// the calendar day from local midnight to the next is only 23h long in
	// absolute time. AddDate lands on that 23h, where a fixed 24h add would
	// have put periodEnd an hour past the next midnight.
	now := time.Date(2026, 3, 8, 12, 0, 0, 0, ny)
	bounds, probe, err := resolveWindowBounds(context.Background(), config{}, statsOptions{window: windowToday}, "", now)
	if err != nil {
		t.Fatalf("resolveWindowBounds: %v", err)
	}
	if probe != nil {
		t.Errorf("today needs no probe, got %+v", probe)
	}
	wantFrom := time.Date(2026, 3, 8, 0, 0, 0, 0, ny)
	if !bounds.from.Equal(wantFrom) {
		t.Errorf("from = %s, want %s", bounds.from, wantFrom)
	}
	if got := bounds.periodEnd.Sub(bounds.from); got != 23*time.Hour {
		t.Errorf("today spans %s across the spring-forward transition, want exactly 23h", got)
	}
	wantPeriodEnd := time.Date(2026, 3, 9, 0, 0, 0, 0, ny)
	if !bounds.periodEnd.Equal(wantPeriodEnd) {
		t.Errorf("periodEnd = %s, want local midnight %s", bounds.periodEnd, wantPeriodEnd)
	}
}

func TestResolveWindowBoundsMonthSurvivesAYearBoundary(t *testing.T) {
	now := time.Date(2026, 12, 15, 9, 0, 0, 0, time.UTC)
	bounds, _, err := resolveWindowBounds(context.Background(), config{}, statsOptions{window: windowMonth}, "", now)
	if err != nil {
		t.Fatalf("resolveWindowBounds: %v", err)
	}
	wantFrom := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	wantTo := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC) // rolls the year, not just the month
	if !bounds.from.Equal(wantFrom) || !bounds.periodEnd.Equal(wantTo) {
		t.Errorf("bounds = %s -> %s, want %s -> %s", bounds.from, bounds.periodEnd, wantFrom, wantTo)
	}
}

func TestResolveWindowBoundsMonthSurvivesA31DayMonth(t *testing.T) {
	// Jan has 31 days; a fixed 30-day add would leave periodEnd a day short
	// of Feb 1. now is Jan 31 itself, the sharpest edge of the month.
	now := time.Date(2026, 1, 31, 23, 0, 0, 0, time.UTC)
	bounds, _, err := resolveWindowBounds(context.Background(), config{}, statsOptions{window: windowMonth}, "", now)
	if err != nil {
		t.Fatalf("resolveWindowBounds: %v", err)
	}
	wantTo := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	if !bounds.periodEnd.Equal(wantTo) {
		t.Errorf("periodEnd = %s, want %s", bounds.periodEnd, wantTo)
	}
}

// --- week: the pure anchor math, independent of the live probe's own clock ---
//
// probeUsage calls time.Now() itself (usage.go), so nothing that exercises
// it end to end can be pinned to a test's chosen instant. weekAnchorFromProbe
// is resolveWeekWindow's pure half precisely so this rollback logic can be
// tested against a hand-built snapshot instead.

func TestWeekAnchorFromProbeRollsBackToTheMostRecentOccurrence(t *testing.T) {
	reset := time.Date(2026, 9, 2, 18, 0, 0, 0, time.UTC) // the *next* reset, named by the probe
	now := time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC)   // three days before it
	snap := usageSnapshot{pools: []usagePool{{name: "week (all models)", percent: 52, reset: reset, hasReset: true}}}

	bounds, ok := weekAnchorFromProbe(snap, now)
	if !ok {
		t.Fatal("weekAnchorFromProbe = false, want true — the snapshot carries a week pool with a reset")
	}
	wantFrom := reset.AddDate(0, 0, -7) // one 7-day step back, and no further: it is already <= now
	if !bounds.from.Equal(wantFrom) {
		t.Errorf("from = %s, want %s (one week before the named reset)", bounds.from, wantFrom)
	}
	if !bounds.periodEnd.Equal(reset) {
		t.Errorf("periodEnd = %s, want the reset itself %s", bounds.periodEnd, reset)
	}
	if bounds.anchor != "the plan's reset" {
		t.Errorf("anchor = %q, want %q", bounds.anchor, "the plan's reset")
	}
}

// Two resets ago rather than one: the loop has to keep stepping back until
// it lands at or before now, not stop after a single 7-day step.
func TestWeekAnchorFromProbeRollsBackMultipleSteps(t *testing.T) {
	reset := time.Date(2026, 9, 2, 18, 0, 0, 0, time.UTC)
	now := time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC) // more than two weeks before it
	snap := usageSnapshot{pools: []usagePool{{name: "week", percent: 10, reset: reset, hasReset: true}}}

	bounds, ok := weekAnchorFromProbe(snap, now)
	if !ok {
		t.Fatal("weekAnchorFromProbe = false, want true")
	}
	if bounds.from.After(now) {
		t.Fatalf("from = %s is after now = %s", bounds.from, now)
	}
	if got := now.Sub(bounds.from); got >= 7*24*time.Hour {
		t.Errorf("from is %s before now, want the most recent occurrence (<7d back)", got)
	}
}

func TestWeekAnchorFromProbeFalseWithoutAReadableReset(t *testing.T) {
	now := time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC)
	cases := map[string]usageSnapshot{
		"no week pool at all":     {pools: []usagePool{{name: "session", percent: 10, hasReset: true}}},
		"week pool with no reset": {pools: []usagePool{{name: "week", percent: 10, hasReset: false}}},
	}
	for name, snap := range cases {
		if _, ok := weekAnchorFromProbe(snap, now); ok {
			t.Errorf("%s: weekAnchorFromProbe = true, want false", name)
		}
	}
}

// --- week: the fallback path, exercised through the live (fake) probe ---

func TestResolveWeekWindowFallsBackToMondayWhenTheProbeCannotAnswer(t *testing.T) {
	t.Setenv(fakeClaudeEnv, "stream")
	t.Setenv(fakeUsageEnv, "") // unset: an old CLI with no /usage, per fakeUsageProbe
	cfg := config{claudeBin: fakeCLI(t), usageTimeout: 5 * time.Second}
	now := time.Date(2026, 8, 26, 15, 0, 0, 0, time.UTC) // a Wednesday

	bounds, probe, err := resolveWindowBounds(context.Background(), cfg, statsOptions{window: windowWeek}, "", now)
	if err != nil {
		t.Fatalf("resolveWindowBounds: %v", err)
	}
	if probe != nil {
		t.Errorf("probe = %+v, want nil — the fake CLI reported no /usage", probe)
	}
	if bounds.anchor != "monday" {
		t.Errorf("anchor = %q, want %q", bounds.anchor, "monday")
	}
	wantFrom := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	if !bounds.from.Equal(wantFrom) {
		t.Errorf("from = %s, want Monday %s", bounds.from, wantFrom)
	}
}

// A smoke test for the exec path itself: the fake CLI answers /usage, so the
// probe is attempted and its result carried back. It does not assert an
// absolute anchor instant — probeUsage parses the reset against the real
// wall clock (see the pure-math tests above for that) — only that the probe
// fired and the bounds it returned are internally consistent.
func TestResolveWeekWindowUsesTheProbeWhenItAnswers(t *testing.T) {
	t.Setenv(fakeClaudeEnv, "stream")
	t.Setenv(fakeUsageEnv, "sub")
	cfg := config{claudeBin: fakeCLI(t), usageTimeout: 5 * time.Second}

	bounds, probe, err := resolveWindowBounds(context.Background(), cfg, statsOptions{window: windowWeek}, "", time.Now())
	if err != nil {
		t.Fatalf("resolveWindowBounds: %v", err)
	}
	if probe == nil {
		t.Fatal("probe = nil, want the fake CLI's answer carried back")
	}
	if bounds.anchor != "the plan's reset" && bounds.anchor != "monday" {
		t.Errorf("anchor = %q, want one of the two named anchors", bounds.anchor)
	}
	if got := bounds.periodEnd.Sub(bounds.from); got != 7*24*time.Hour {
		t.Errorf("week window spans %s, want exactly 7d", got)
	}
}

// --- session ---

func sessionFixtureDir(t *testing.T, lines ...string) string {
	t.Helper()
	dir := t.TempDir()
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "r--r.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	return dir
}

func TestSessionAnchorFindsTheEarliestRunInTheLast5h(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	dir := sessionFixtureDir(t,
		// Outside the 5h lookback: not a candidate.
		`{"v":1,"kind":"run","ts":"2026-08-25T06:00:00Z","repo":"r/r","issue":1,"cost_usd":1}`,
		// The earliest run inside the last 5h — this is the anchor.
		`{"v":1,"kind":"run","ts":"2026-08-25T08:30:00Z","repo":"r/r","issue":2,"cost_usd":1}`,
		`{"v":1,"kind":"run","ts":"2026-08-25T10:00:00Z","repo":"r/r","issue":3,"cost_usd":1}`,
	)
	from, ok := sessionAnchor(dir, statsOptions{}, now)
	if !ok {
		t.Fatal("sessionAnchor = false, want true — a run sits inside the last 5h")
	}
	want := time.Date(2026, 8, 25, 8, 30, 0, 0, time.UTC)
	if !from.Equal(want) {
		t.Errorf("anchor = %s, want the earliest in-window run %s", from, want)
	}
}

func TestSessionAnchorFalseWithNoRecentRun(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	dir := sessionFixtureDir(t,
		`{"v":1,"kind":"run","ts":"2026-08-25T06:00:00Z","repo":"r/r","issue":1,"cost_usd":1}`,
	)
	if _, ok := sessionAnchor(dir, statsOptions{}, now); ok {
		t.Error("sessionAnchor = true, want false — nothing in the last 5h")
	}
}

func TestResolveWindowBoundsSessionFallsBackToAPlain5hWhenNothingRecent(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir() // empty: no runs at all
	bounds, probe, err := resolveWindowBounds(context.Background(), config{}, statsOptions{window: windowSession}, dir, now)
	if err != nil {
		t.Fatalf("resolveWindowBounds: %v", err)
	}
	if probe != nil {
		t.Errorf("session needs no probe, got %+v", probe)
	}
	if bounds.anchor != "approximate" {
		t.Errorf("anchor = %q, want %q", bounds.anchor, "approximate")
	}
	want := now.Add(-sessionWindow)
	if !bounds.from.Equal(want) {
		t.Errorf("from = %s, want now-5h %s", bounds.from, want)
	}
	if got := bounds.periodEnd.Sub(bounds.from); got != sessionWindow {
		t.Errorf("session window spans %s, want exactly 5h", got)
	}
}

// --- -window end to end, through the text report ---

func TestStatsWindowTodayFiltersToLocalMidnight(t *testing.T) {
	now := time.Date(2026, 8, 25, 15, 0, 0, 0, time.UTC)
	dir := sessionFixtureDir(t,
		// Yesterday: outside today's window.
		`{"v":1,"kind":"run","ts":"2026-08-24T23:00:00Z","repo":"r/r","issue":1,"status":"ok","cost_usd":1,"outcome":"opened_pr"}`,
		// Today: inside it.
		`{"v":1,"kind":"run","ts":"2026-08-25T09:00:00Z","repo":"r/r","issue":2,"status":"ok","cost_usd":2,"outcome":"opened_pr"}`,
	)
	var out strings.Builder
	if err := runStats([]string{"-metrics", dir, "-window", "today"}, &out, io.Discard, now, report{}); err != nil {
		t.Fatalf("stats -window today: %v", err)
	}
	got := out.String()
	if !hasLine(got, "total 1 — ok 1") {
		t.Errorf("-window today did not filter to the one run today:\n%s", got)
	}
	if !strings.Contains(got, "today") {
		t.Errorf("header does not name the window kind:\n%s", got)
	}
}

// --- plan cost per issue ---

const fixturePlanCost = `
{"v":1,"kind":"run","ts":"2026-08-20T09:00:00Z","ended":"2026-08-20T09:20:00Z","repo":"r/r","issue":1,"reason":"implement","status":"ok","outcome":"opened_pr","cost_usd":1}
{"v":1,"kind":"issue","ts":"2026-08-20T10:00:00Z","repo":"r/r","issue":1,"outcome":"merged","week_usage_at_pickup_pct":10,"week_usage_at_terminal_pct":12}
{"v":1,"kind":"run","ts":"2026-08-21T09:00:00Z","ended":"2026-08-21T09:20:00Z","repo":"r/r","issue":2,"reason":"implement","status":"ok","outcome":"opened_pr","cost_usd":1}
{"v":1,"kind":"issue","ts":"2026-08-21T10:00:00Z","repo":"r/r","issue":2,"outcome":"merged","week_usage_at_pickup_pct":20,"week_usage_at_terminal_pct":24}
{"v":1,"kind":"run","ts":"2026-08-22T09:00:00Z","ended":"2026-08-22T09:20:00Z","repo":"r/r","issue":3,"reason":"implement","status":"ok","outcome":"opened_pr","cost_usd":1}
{"v":1,"kind":"issue","ts":"2026-08-22T10:00:00Z","repo":"r/r","issue":3,"outcome":"merged"}
{"v":1,"kind":"run","ts":"2026-08-23T09:00:00Z","ended":"2026-08-23T09:20:00Z","repo":"r/r","issue":4,"reason":"implement","status":"ok","outcome":"opened_pr","cost_usd":1}
{"v":1,"kind":"issue","ts":"2026-08-23T10:00:00Z","repo":"r/r","issue":4,"outcome":"merged","week_usage_at_pickup_pct":50,"week_usage_at_terminal_pct":59}
{"v":1,"kind":"run","ts":"2026-08-24T09:00:00Z","ended":"2026-08-24T09:20:00Z","repo":"r/r","issue":5,"reason":"implement","status":"ok","outcome":"opened_pr","cost_usd":1}
{"v":1,"kind":"issue","ts":"2026-08-24T10:00:00Z","repo":"r/r","issue":5,"outcome":"merged","week_usage_at_pickup_pct":90,"week_usage_at_terminal_pct":85}
`

func planCostDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "r--r.jsonl"), []byte(strings.TrimPrefix(fixturePlanCost, "\n")), 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	return dir
}

// Deltas: #1 = 2, #2 = 4, #3 unsampled, #4 = 9, #5 = -5 (a reset crossed
// mid-issue, excluded rather than averaged in as a false near-zero). So the
// three usable deltas are 2, 4, 9 — mean 5, median 4 — over 3 sampled issues
// with 2 left out.
//
// Goes through statsReport with a fake claudeBin rather than the stats()
// helper's public runStats: this fixture has samples, so
// issuesHaveUsageSamples is true and a real "claude" resolved off PATH would
// otherwise be probed for the cross-check — exactly the trap status_test.go
// avoids by never exercising a live probe through anything but a fake CLI.
func TestPlanCostPerIssueLine(t *testing.T) {
	t.Setenv(fakeClaudeEnv, "stream")
	t.Setenv(fakeUsageEnv, "") // unset: no cross-check figure, so the line is pinned in full
	cfg := config{claudeBin: fakeCLI(t), usageTimeout: 5 * time.Second}

	_, _, summary, err := statsReport(context.Background(), cfg, statsOptions{}, planCostDir(t), fixtureNow)
	if err != nil {
		t.Fatalf("statsReport: %v", err)
	}
	got := planCostPairs(summary.plan)
	want := "5% mean, 4% median of a week — about 25 issues to a full week " +
		"(upper bound: counts everything the account did meanwhile, not just this issue) " +
		"(2 issues of 5 issues had no usable reading)"
	if len(got) != 1 || got[0][1] != want {
		t.Errorf("plan cost pairs = %v, want [[plan cost per issue %q]]", got, want)
	}
}

// The same "absent, never a line of zeroes" treatment the change-per-issue
// line already gets: no terminal issue anywhere carries a usable sample, so
// the line does not appear at all.
func TestPlanCostOmittedWithoutAnySamples(t *testing.T) {
	out := stats(t, "-metrics", fixtureDir(t))
	if strings.Contains(out, "plan cost per issue") {
		t.Errorf("no samples in this fixture, want no plan-cost line:\n%s", out)
	}
}

// The cross-check figure — the probe's own attribution over a different,
// self-reported window — sits beside the mean/median and is fetched once,
// through statsReport directly since it needs a cfg pointed at the fake CLI.
func TestPlanCostCrossCheckFromTheLiveProbe(t *testing.T) {
	t.Setenv(fakeClaudeEnv, "stream")
	t.Setenv(fakeUsageEnv, "sub") // usageSample: "Top plugins: polako 29%" over "Last 24h"
	cfg := config{claudeBin: fakeCLI(t), usageTimeout: 5 * time.Second}

	_, _, summary, err := statsReport(context.Background(), cfg, statsOptions{}, planCostDir(t), fixtureNow)
	if err != nil {
		t.Fatalf("statsReport: %v", err)
	}
	got := planCostPairs(summary.plan)
	if len(got) != 1 {
		t.Fatalf("plan cost pairs = %v, want exactly one line", got)
	}
	if !strings.Contains(got[0][1], "polako's own share was 29% of the last 24h") {
		t.Errorf("no cross-check figure in %q", got[0][1])
	}
}

// No probe call at all when there is nothing for it to cross-check — the
// point of gating it on issuesHaveUsageSamples rather than probing on every
// invocation regardless.
func TestPlanCostNoProbeCallWithoutSamples(t *testing.T) {
	t.Setenv(fakeClaudeEnv, "stream")
	t.Setenv(fakeUsageEnv, "sub")
	args := watchClaudeArgs(t)
	cfg := config{claudeBin: fakeCLI(t), usageTimeout: 5 * time.Second}

	_, _, _, err := statsReport(context.Background(), cfg, statsOptions{}, fixtureDir(t), fixtureNow)
	if err != nil {
		t.Fatalf("statsReport: %v", err)
	}
	for _, a := range args() {
		if strings.Contains(a, "/usage") {
			t.Errorf("probed /usage with nothing to cross-check: argv %q", a)
		}
	}
}

// --- -since / -window vs. an env default ---

// applyEnvDefaults's "arguments win" promise is per-flag, so it says nothing
// about -since and -window overriding each other — this is the pair's own
// check, so it needs its own regression: an explicit -since must still beat
// a POLAKO_WINDOW default sitting in the environment, not be silently
// overridden by it once resolveWindowBounds runs.
func TestExplicitSinceBeatsAWindowEnvDefault(t *testing.T) {
	clearEnvDefaults(t)
	t.Setenv(envVarName("window"), "week")
	var out bytes.Buffer
	if err := runStats([]string{"-since", "1h", "-metrics", fixtureDir(t)}, &out, io.Discard, fixtureNow, report{}); err != nil {
		t.Fatalf("runStats: %v", err)
	}
	if strings.Contains(out.String(), "for week") || strings.Contains(out.String(), "anchor:") {
		t.Errorf("explicit -since did not beat POLAKO_WINDOW=week:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "in the last 1h") {
		t.Errorf("-since 1h did not take effect:\n%s", out.String())
	}
}

// The mirror image: an explicit -window must beat a POLAKO_SINCE default.
func TestExplicitWindowBeatsASinceEnvDefault(t *testing.T) {
	clearEnvDefaults(t)
	t.Setenv(envVarName("since"), "1h")
	var out bytes.Buffer
	if err := runStats([]string{"-window", "today", "-metrics", fixtureDir(t)}, &out, io.Discard, fixtureNow, report{}); err != nil {
		t.Fatalf("runStats: %v", err)
	}
	if strings.Contains(out.String(), "in the last") {
		t.Errorf("POLAKO_SINCE=1h leaked through an explicit -window:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "for today") {
		t.Errorf("-window today did not take effect:\n%s", out.String())
	}
}
