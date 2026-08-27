package main

import (
	"context"
	"log"
	"os"
	"strings"
	"testing"
)

// The gate itself: exactly one shape refuses — a public repository with no
// -label and no -ungated — because that is the one shape where "anyone can
// open an issue" and "an unattended agent implements open issues" meet.
func TestQueueGateRefusesOnlyThePublicUnlabelledQueue(t *testing.T) {
	cases := []struct {
		name       string
		visibility string
		label      string
		ungated    bool
		refused    bool
	}{
		{"public and unfiltered", "PUBLIC", "", false, true},
		{"public, case from a different gh", "public", "", false, true},
		{"public but label-gated", "PUBLIC", "ready-for-claude", false, false},
		{"public and consented to", "PUBLIC", "", true, false},
		{"private", "PRIVATE", "", false, false},
		{"internal", "INTERNAL", "", false, false},
		{"visibility gh never named", "", "", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := queueGate(tc.visibility, tc.label, tc.ungated)
			if tc.refused && err == nil {
				t.Fatal("gate let an unfiltered public queue through")
			}
			if !tc.refused && err != nil {
				t.Fatalf("gate refused a queue it is not about: %v", err)
			}
			if err != nil {
				// The error is the operator's whole briefing, so it has to name
				// both ways forward.
				for _, want := range []string{"-label", "-ungated"} {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("refusal does not mention %s:\n%s", want, err)
					}
				}
			}
		})
	}
}

// The wiring: preflight reads visibility off the same `gh repo view` call that
// names the repository, and refuses before anything is written or run.
func TestPreflightRefusesAnUngatedPublicQueue(t *testing.T) {
	_, checkout := upstream(t)
	cfg, _ := drainConfig(t, "stream", &ghState{Visibility: "PUBLIC"})
	cfg.dir = checkout

	if err := preflight(context.Background(), &cfg); err == nil {
		t.Fatal("preflight started an unfiltered drain on a public repository")
	} else if !strings.Contains(err.Error(), "-label") {
		t.Fatalf("refusal does not tell the operator about -label: %v", err)
	}

	gated := cfg
	gated.label = "ready-for-claude"
	if err := preflight(context.Background(), &gated); err != nil {
		t.Fatalf("a -label gate should satisfy preflight: %v", err)
	}

	consented := cfg
	consented.ungated = true
	if err := preflight(context.Background(), &consented); err != nil {
		t.Fatalf("-ungated should satisfy preflight: %v", err)
	}
}

// A dry run writes nothing and runs nothing, so it is allowed through to look —
// seeing the queue is how an operator decides what to label — but the gate
// still tells them a real run would refuse.
func TestPreflightLetsADryRunLookThroughTheGate(t *testing.T) {
	_, checkout := upstream(t)
	cfg, _ := drainConfig(t, "stream", &ghState{Visibility: "PUBLIC"})
	cfg.dir = checkout
	cfg.dryRun = true

	var logged strings.Builder
	log.SetOutput(&logged)
	defer log.SetOutput(os.Stderr)

	if err := preflight(context.Background(), &cfg); err != nil {
		t.Fatalf("the gate stopped a dry run, which changes nothing: %v", err)
	}
	if !strings.Contains(logged.String(), "-label") {
		t.Error("a dry run through the gate should still say a real run would refuse")
	}
}
