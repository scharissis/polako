package main

// labelExists's own tests, alongside labels.go. Reuses drain_test.go's fake
// gh harness (drainConfig, ghState) — no network, no real gh.

import (
	"context"
	"strings"
	"testing"
)

func TestLabelExists(t *testing.T) {
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
