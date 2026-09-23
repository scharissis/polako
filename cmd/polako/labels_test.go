package main

// labelExists's own tests, alongside labels.go. Reuses drain_test.go's fake
// gh harness (drainConfig, ghState) — no network, no real gh.

import (
	"context"
	"strings"
	"testing"
)

func TestLabelExists(t *testing.T) {
	t.Parallel()
	cfg, _ := drainConfig(t, "stream", &ghState{Labels: []string{"needs-human", "model:opus"}})

	ok, err := labelExists(context.Background(), cfg, "needs-human")
	if err != nil {
		t.Fatalf("labelExists(needs-human): %v", err)
	}
	if !ok {
		t.Error("labelExists(needs-human) = false, want true — the fake repo has it")
	}
}

func TestLabelExistsMissingIsFalseNotError(t *testing.T) {
	t.Parallel()
	cfg, _ := drainConfig(t, "stream", &ghState{})

	ok, err := labelExists(context.Background(), cfg, "does-not-exist")
	if err != nil {
		t.Fatalf("labelExists(does-not-exist): %v", err)
	}
	if ok {
		t.Error("labelExists(does-not-exist) = true, want false")
	}
}

// A name gh's own path needs url.PathEscape'd — the colon in "model:opus" —
// round-trips through the fake's own PathUnescape the same way a real
// repository's REST endpoint would see it.
func TestLabelExistsEscapesTheName(t *testing.T) {
	t.Parallel()
	cfg, _ := drainConfig(t, "stream", &ghState{Labels: []string{"model:opus"}})

	ok, err := labelExists(context.Background(), cfg, "model:opus")
	if err != nil {
		t.Fatalf("labelExists(model:opus): %v", err)
	}
	if !ok {
		t.Error("labelExists(model:opus) = false, want true")
	}
}

// A non-404 failure — a network that has not reassociated yet, say — must
// never be reported as "missing": that would let a caller create a label
// gh already has under a different name it just couldn't answer for.
func TestLabelExistsNonNotFoundFailureIsAnError(t *testing.T) {
	t.Parallel()
	cfg, _ := drainConfig(t, "stream", &ghState{
		Labels:    []string{"needs-human"},
		FailReads: map[string]int{"api label": ghReads},
	})

	_, err := labelExists(context.Background(), cfg, "needs-human")
	if err == nil {
		t.Fatal("labelExists returned no error for a call that failed every attempt")
	}
	if strings.Contains(err.Error(), "HTTP 404") {
		t.Errorf("labelExists reported a transient failure as a 404: %v", err)
	}
}

// labelDescription reads the description alongside existence — the field
// checkLabelDef's gate-label row needs to tell a plain label from a marked
// gate label.
func TestLabelDescription(t *testing.T) {
	t.Parallel()
	cfg, _ := drainConfig(t, "stream", &ghState{
		Labels:            []string{"ready"},
		LabelDescriptions: map[string]string{"ready": gateLabelDescription},
	})

	desc, exists, err := labelDescription(context.Background(), cfg, "ready")
	if err != nil {
		t.Fatalf("labelDescription(ready): %v", err)
	}
	if !exists || desc != gateLabelDescription {
		t.Errorf("labelDescription(ready) = (%q, %v), want (%q, true)", desc, exists, gateLabelDescription)
	}

	_, exists, err = labelDescription(context.Background(), cfg, "missing")
	if err != nil {
		t.Fatalf("labelDescription(missing): %v", err)
	}
	if exists {
		t.Error("labelDescription(missing) = exists true, want false")
	}
}

// markedGateLabel finds the one label carrying the marker, out of every
// label the repository has defined — the one `gh api .../labels` read
// status makes with no -label given.
func TestMarkedGateLabel(t *testing.T) {
	t.Parallel()
	cfg, _ := drainConfig(t, "stream", &ghState{
		Labels: []string{"ready", "bug", needsHumanLabel},
		LabelDescriptions: map[string]string{
			"ready":         gateLabelDescription,
			needsHumanLabel: "polako parked this issue for a human",
		},
	})

	name, ambiguous, err := markedGateLabel(context.Background(), cfg)
	if err != nil {
		t.Fatalf("markedGateLabel: %v", err)
	}
	if ambiguous || name != "ready" {
		t.Errorf("markedGateLabel() = (%q, %v), want (\"ready\", false)", name, ambiguous)
	}
}

// No label carrying the marker is not an error — it's the common case on a
// repository `setup -apply` has never touched.
func TestMarkedGateLabelNoneFound(t *testing.T) {
	t.Parallel()
	cfg, _ := drainConfig(t, "stream", &ghState{Labels: []string{"bug"}})

	name, ambiguous, err := markedGateLabel(context.Background(), cfg)
	if err != nil {
		t.Fatalf("markedGateLabel: %v", err)
	}
	if ambiguous || name != "" {
		t.Errorf("markedGateLabel() = (%q, %v), want (\"\", false)", name, ambiguous)
	}
}

// Two labels both carrying the marker is ambiguous, not a coin flip — a
// caller must not scope to a guess.
func TestMarkedGateLabelAmbiguousWithTwo(t *testing.T) {
	t.Parallel()
	cfg, _ := drainConfig(t, "stream", &ghState{
		Labels: []string{"ready", "ready-2"},
		LabelDescriptions: map[string]string{
			"ready":   gateLabelDescription,
			"ready-2": gateLabelDescription,
		},
	})

	name, ambiguous, err := markedGateLabel(context.Background(), cfg)
	if err != nil {
		t.Fatalf("markedGateLabel: %v", err)
	}
	if !ambiguous || name != "" {
		t.Errorf("markedGateLabel() = (%q, %v), want (\"\", true)", name, ambiguous)
	}
}

// ensureLabelMarked is -apply's remedy for an unmarked existing gate label —
// proved here at the gh-call level, checkLabelDef's own row is proved in
// setup_test.go.
func TestEnsureLabelMarked(t *testing.T) {
	t.Parallel()
	cfg, path := drainConfig(t, "stream", &ghState{Labels: []string{"ready"}})

	if err := ensureLabelMarked(context.Background(), cfg, "ready"); err != nil {
		t.Fatalf("ensureLabelMarked: %v", err)
	}

	st, err := readGhState(path)
	if err != nil {
		t.Fatalf("reading fake gh state: %v", err)
	}
	if got := st.LabelDescriptions["ready"]; got != gateLabelDescription {
		t.Errorf("label edit set description %q, want %q", got, gateLabelDescription)
	}
}
