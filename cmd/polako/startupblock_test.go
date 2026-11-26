package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStartupHeader(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		version, scope string
		want           string
	}{
		"labelled": {"0.41.0", queueScope("ready"),
			`polako 0.41.0 · o/r — issues labelled "ready", /polako:implement-issue, polling every 5m`},
		"no label": {"0.41.0", queueScope(""),
			`polako 0.41.0 · o/r — all open issues, /polako:implement-issue, polling every 5m`},
		"no version": {"", "issue #7",
			`o/r — issue #7, /polako:implement-issue, polling every 5m`},
	} {
		if got := startupHeader(tc.version, "o/r", tc.scope, "polako:implement-issue", 5*time.Minute); got != tc.want {
			t.Errorf("%s: startupHeader = %q, want %q", name, got, tc.want)
		}
	}
}

// Each pool's reading sits beside its threshold; a threshold with no reading
// says so rather than reading as zero; neither prints nothing.
func TestUsageRow(t *testing.T) {
	t.Parallel()
	reset := time.Date(2026, 11, 25, 18, 0, 0, 0, time.UTC)
	both := &usageSnapshot{pools: []usagePool{
		{name: "session", percent: 60},
		{name: "week", percent: 71, hasReset: true, reset: reset},
	}}
	sessionOnly := &usageSnapshot{pools: []usagePool{{name: "session", percent: 5}}}
	for name, tc := range map[string]struct {
		snap          *usageSnapshot
		session, week int
		want          string
	}{
		"readings and thresholds": {both, 97, 99,
			"session 60% (pauses at 97%) · week 71% (pauses at 99%, resets Nov 25, 6pm)"},
		"readings only":    {both, 0, 0, "session 60% · week 71% (resets Nov 25, 6pm)"},
		"thresholds only":  {nil, 97, 99, "pauses at session 97%, week 99% (current usage unknown)"},
		"one pool missing": {sessionOnly, 0, 99, "session 5% · pauses at week 99% (current usage unknown)"},
		"neither":          {nil, 0, 0, ""},
		"empty snapshot":   {&usageSnapshot{}, 0, 0, ""},
	} {
		if got := usageRow(tc.snap, tc.session, tc.week); got != tc.want {
			t.Errorf("%s: usageRow = %q, want %q", name, got, tc.want)
		}
	}
}

func TestHomeTilde(t *testing.T) {
	t.Parallel()
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory")
	}
	sep := string(filepath.Separator)
	in := filepath.Join(home, ".polako", "metrics")
	if got, want := homeTilde(in), "~"+sep+".polako"+sep+"metrics"; got != want {
		t.Errorf("homeTilde(%q) = %q, want %q", in, got, want)
	}
	// A sibling that merely shares the prefix is not under home.
	if got := homeTilde(home + "x" + sep + "f"); got != home+"x"+sep+"f" {
		t.Errorf("homeTilde shortened a sibling of home: %q", got)
	}
	if got := homeTilde("/elsewhere/f"); got != "/elsewhere/f" {
		t.Errorf("homeTilde(/elsewhere/f) = %q", got)
	}
}
