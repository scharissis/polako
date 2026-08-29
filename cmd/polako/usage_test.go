package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

// usageSample is the CLI 2.1.250 output quoted verbatim in issue #138's own
// body — the shape a subscription account gets from `claude -p "/usage"
// --output-format json`.
const usageSample = "You are currently using your subscription to power your Claude Code usage\n\n" +
	"Current session: 42% used · resets Aug 28 at 10:20pm (Europe/London)\n" +
	"Current week (all models): 52% used · resets Sep 2 at 6pm (Europe/London)\n" +
	"Current week (Fable): 62% used · resets Sep 2 at 6pm (Europe/London)\n\n" +
	"What's contributing to your limits usage?\n" +
	"Approximate, based on local sessions on this machine — does not include other\n" +
	"devices or claude.ai. Behaviors are independent characteristics, not a breakdown.\n\n" +
	"Last 24h · 3084 requests · 47 sessions\n" +
	"  86% of your usage came from subagent-heavy sessions\n" +
	"  Top skills: /polako:implement-issue 28%, /code-review 13%\n" +
	"  Top plugins: polako 29%\n"

// usageNow is the clock every parseUsage/usageReset case here is written
// against, chosen close to the sample's own dates so neither pool's reset
// clause needs the year-roll path to land in the future.
var usageNow = time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC)

func TestParseUsageReadsTheSubscriptionSample(t *testing.T) {
	snap, ok := parseUsage(usageSample, usageNow)
	if !ok {
		t.Fatal("parseUsage on the documented sample = false, want true")
	}
	if len(snap.pools) != 3 {
		t.Fatalf("pools = %+v, want 3", snap.pools)
	}
	session, ok := poolByLabel(snap.pools, "session")
	if !ok || session.percent != 42 {
		t.Errorf("session pool = %+v, %v; want 42%%", session, ok)
	}
	if !session.hasReset || !session.reset.Equal(time.Date(2026, 8, 28, 22, 20, 0, 0, session.reset.Location())) {
		t.Errorf("session reset = %v, %v; want Aug 28 22:20 (Europe/London)", session.reset, session.hasReset)
	}
	week, ok := poolByLabel(snap.pools, "week")
	if !ok || week.percent != 52 {
		t.Errorf("week pool = %+v, %v; want 52%% (the all-models figure, not the per-model one)", week, ok)
	}
	if !week.hasReset || !week.reset.Equal(time.Date(2026, 9, 2, 18, 0, 0, 0, week.reset.Location())) {
		t.Errorf("week reset = %v, %v; want Sep 2 18:00 (Europe/London)", week.reset, week.hasReset)
	}
	fable, ok := poolByLabel(snap.pools, "week (Fable)")
	if !ok || fable.percent != 62 {
		t.Errorf("week (Fable) pool = %+v, %v; want 62%%, proving N pools rather than a hardcoded three", fable, ok)
	}
	if !snap.hasAttribution || !snap.attribution.hasPluginPercent || snap.attribution.pluginPercent != 29 {
		t.Errorf("attribution = %+v, %v; want this plugin's 29%% share", snap.attribution, snap.hasAttribution)
	}
	if snap.attribution.windowLabel != "24h" {
		t.Errorf("window label = %q, want %q", snap.attribution.windowLabel, "24h")
	}

	want := "plan: session 42%, week 52% (resets Sep 2, 6pm) — polako was 29% of the last 24h"
	if got := usageLine(snap); got != want {
		t.Errorf("usageLine = %q, want %q", got, want)
	}
}

// An API-key account's answer carries no pool lines at all — the doctrine is
// "no plan to report", never a wrong zero.
func TestParseUsageAPIKeyAccountHasNoPlanToReport(t *testing.T) {
	text := "You are not currently using a Claude subscription — usage under an API key " +
		"is not tracked against a plan.\n"
	snap, ok := parseUsage(text, usageNow)
	if ok {
		t.Errorf("parseUsage on API-key prose = %+v, true; want false (no plan, not a zero one)", snap)
	}
	if len(snap.pools) != 0 {
		t.Errorf("pools = %+v, want none", snap.pools)
	}
}

// A wording change reads the same as the API-key case: nothing this parser
// recognises, so nothing is reported, rather than a guess.
func TestParseUsageWordingChangeParsesToNothing(t *testing.T) {
	text := "The usage reporting endpoint changed shape, and this text matches nothing this " +
		"binary knows how to read.\n"
	if _, ok := parseUsage(text, usageNow); ok {
		t.Error("parseUsage on unrecognised wording = true, want false")
	}
}

// A payload readable in part yields the pools it understood rather than
// nothing at all — the same doctrine limitRefusal/limitReset apply.
func TestParseUsagePartialPayloadKeepsWhatParsed(t *testing.T) {
	text := "Current session: 42% used · resets Aug 28 at 10:20pm (Europe/London)\n" +
		// A percent outside 0-100 cannot be trusted, so this line is dropped
		// rather than reported as a guess — the same "no guessed value"
		// doctrine as a reset clause that fails to parse.
		"Current week (all models): 150% used\n"
	snap, ok := parseUsage(text, usageNow)
	if !ok {
		t.Fatal("parseUsage on a partially-readable payload = false, want true")
	}
	if len(snap.pools) != 1 || snap.pools[0].name != "session" || snap.pools[0].percent != 42 {
		t.Errorf("pools = %+v, want only the session pool that actually parsed", snap.pools)
	}
}

func TestUsageResetReadsDatedClauses(t *testing.T) {
	london, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Fatalf("loading Europe/London (time/tzdata is imported, so this must work everywhere): %v", err)
	}
	now := time.Date(2026, 8, 27, 8, 0, 0, 0, london)
	at := func(y int, mo time.Month, d, h, min int) time.Time {
		return time.Date(y, mo, d, h, min, 0, 0, london)
	}
	cases := []struct {
		name   string
		clause string
		want   time.Time
		ok     bool
	}{
		{"short month, with minutes", "Aug 28 at 10:20pm (Europe/London)", at(2026, 8, 28, 22, 20), true},
		{"long month name", "September 2 at 6pm (Europe/London)", at(2026, 9, 2, 18, 0), true},
		{"no minutes", "Sep 2 at 6pm (Europe/London)", at(2026, 9, 2, 18, 0), true},
		{"no zone falls back to the caller's", "Sep 2 at 6pm", at(2026, 9, 2, 18, 0), true},
		{"12pm is noon", "Sep 2 at 12pm (Europe/London)", at(2026, 9, 2, 12, 0), true},
		{"12am is midnight", "Sep 2 at 12am (Europe/London)", at(2026, 9, 2, 0, 0), true},
		{"a zone this build cannot resolve", "Sep 2 at 6pm (Atlantis/Lost)", time.Time{}, false},
		{"a bare clock is beyond this parser", "6pm (Europe/London)", time.Time{}, false},
		{"no clause at all", "", time.Time{}, false},
		// Genuinely stale by a year, not a clock-skew blip: a date this far
		// behind now can only mean it already turned over into next year.
		{"a date past the stale grace rolls forward a year",
			"Jan 2 at 9am (Europe/London)", at(2027, 1, 2, 9, 0), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := usageReset(tc.clause, now)
			if ok != tc.ok {
				t.Fatalf("usageReset(%q) ok = %v, want %v", tc.clause, ok, tc.ok)
			}
			if ok && !got.Equal(tc.want) {
				t.Errorf("usageReset(%q) = %v, want %v", tc.clause, got, tc.want)
			}
		})
	}

	// Within the stale grace, a date that reads as slightly behind now is
	// left alone rather than bumped a whole year forward on a guess — the
	// far likelier explanation is this probe's own clock running a little
	// behind the CLI's.
	near := time.Date(2026, 8, 27, 9, 0, 0, 0, london)
	got, ok := usageReset("Aug 27 at 8am (Europe/London)", near)
	want := at(2026, 8, 27, 8, 0)
	if !ok || !got.Equal(want) {
		t.Errorf("usageReset just behind the clock = %v, %v; want %v left alone", got, ok, want)
	}
}

func TestUsageLineOmitsWhatItDoesNotHave(t *testing.T) {
	if got := usageLine(usageSnapshot{}); got != "" {
		t.Errorf("usageLine(empty) = %q, want empty", got)
	}
	weekOnly := usageSnapshot{pools: []usagePool{{name: "week (all models)", percent: 20}}}
	if got := usageLine(weekOnly); got != "plan: week 20%" {
		t.Errorf("usageLine(week only) = %q, want %q", got, "plan: week 20%")
	}
	sessionOnly := usageSnapshot{pools: []usagePool{{name: "session", percent: 5}}}
	if got := usageLine(sessionOnly); got != "plan: session 5%" {
		t.Errorf("usageLine(session only) = %q, want %q", got, "plan: session 5%")
	}
	// No attribution match for this binary's own plugin (a fork's usage, or
	// a session with no plugins in its top list) drops the clause silently
	// rather than printing a broken one.
	noPluginShare := usageSnapshot{
		pools:          []usagePool{{name: "session", percent: 5}},
		attribution:    usageAttribution{windowLabel: "24h"},
		hasAttribution: true,
	}
	if got := usageLine(noPluginShare); got != "plan: session 5%" {
		t.Errorf("usageLine(no plugin share) = %q, want the plain plan line", got)
	}
}

// --- probeUsage, end to end against the fake CLI ---

func fakeUsageConfig(t *testing.T, usageMode string) config {
	t.Helper()
	// fakeClaudeEnv is what makes the child impersonate claude at all (see
	// TestMain); the mode itself is never reached, because the /usage argv
	// check in fakeClaude dispatches before the mode switch does.
	t.Setenv(fakeClaudeEnv, "warmup")
	t.Setenv(fakeUsageEnv, usageMode)
	return config{
		dir:          t.TempDir(),
		claudeBin:    fakeCLI(t),
		usageTimeout: 5 * time.Second,
	}
}

func TestProbeUsageReadsTheFakeCLI(t *testing.T) {
	cfg := fakeUsageConfig(t, "sub")
	snap, ok := probeUsage(context.Background(), cfg)
	if !ok {
		t.Fatal("probeUsage on the sub fixture = false, want true")
	}
	if want := "plan: session 42%, week 52% (resets Sep 2, 6pm) — polako was 29% of the last 24h"; usageLine(snap) != want {
		t.Errorf("usageLine(probed snapshot) = %q, want %q", usageLine(snap), want)
	}
}

// An old CLI with no /usage command is the default fixture (fakeUsageEnv
// unset) — fails soft, and says so in exactly one log line.
func TestProbeUsageFailsSoftWithOneLogLineWhenTheCLIHasNoUsageCommand(t *testing.T) {
	buf := captureLog(t)
	cfg := fakeUsageConfig(t, "")
	if _, ok := probeUsage(context.Background(), cfg); ok {
		t.Error("probeUsage with no /usage command = true, want false")
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 1 {
		t.Errorf("log = %q, want exactly one line", buf.String())
	}
	if !strings.Contains(buf.String(), "usage probe") {
		t.Errorf("log = %q, want it to name what failed", buf.String())
	}
}

// A probe slower than its own bound is killed by that bound rather than
// hanging the caller — proved with a timeout short enough that the test
// itself stays fast, not the ~20s production default.
func TestProbeUsageTimesOut(t *testing.T) {
	captureLog(t)
	cfg := fakeUsageConfig(t, "timeout")
	cfg.usageTimeout = 200 * time.Millisecond

	start := time.Now()
	if _, ok := probeUsage(context.Background(), cfg); ok {
		t.Error("probeUsage on the timeout fixture = true, want false")
	}
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Errorf("probeUsage took %s, want it bounded by usageTimeout rather than the fixture's 5s sleep", elapsed)
	}
}
