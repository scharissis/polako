package main

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// One repository with something in every bucket on both sides of the gate.
func gatedBacklog() *ghState {
	gate := "ready"
	return &ghState{
		Issues: map[string]*fakeIssue{
			"1":  {Open: true, Labels: []string{gate}},
			"2":  {Open: true, Labels: []string{gate}, BlockedBy: []int{3}}, // behind an outside issue
			"3":  {Open: true},
			"4":  {Open: true, Labels: []string{gate, awaitingAnswerLabel}},
			"5":  {Open: true, Labels: []string{gate, needsHumanLabel}},
			"6":  {Open: true, Labels: []string{gate, proposedLabel}},
			"7":  {Open: true, Labels: []string{proposedLabel}},
			"8":  {Open: true, SubIssues: 2},
			"9":  {Open: true, Labels: []string{gate}, SubIssues: 3, SubIssuesCompleted: 1},
			"10": {Open: true, Labels: []string{needsHumanLabel}},
			"11": {Open: true, Labels: []string{awaitingAnswerLabel}},
			"12": {Open: false, Labels: []string{gate}},
			"13": {Open: true},
			// proposed outranks awaiting-answer, outside the gate as inside it
			"14": {Open: true, Labels: []string{proposedLabel, awaitingAnswerLabel}},
		},
	}
}

// The issue's own "done when": one listing read both ways, and the in-gate
// queues a gated status reports are the ones `work -label` would drain.
func TestGatedStatusQueuesMatchALabelledListing(t *testing.T) {
	t.Parallel()
	cfg, _ := statusConfigFor(t, gatedBacklog())
	cfg.label = "ready"

	labelled, err := openQueues(context.Background(), cfg)
	if err != nil {
		t.Fatalf("openQueues: %v", err)
	}
	gated, split, err := statusQueues(context.Background(), cfg)
	if err != nil {
		t.Fatalf("statusQueues: %v", err)
	}

	if !slices.Equal(gated.ready, labelled.ready) || !slices.Equal(gated.blocked, labelled.blocked) ||
		!slices.Equal(gated.parked, labelled.parked) || !slices.Equal(gated.containers, labelled.containers) ||
		!heldBackEqual(gated.heldBack, labelled.heldBack) {
		t.Errorf("in-gate queues differ:\ngated    %+v\nlabelled %+v", gated, labelled)
	}
	// Proposed is the one queue that widens, and only by the ungated ones.
	if want := []int{6}; !slices.Equal(labelled.proposed, want) {
		t.Errorf("labelled proposed = %v, want %v", labelled.proposed, want)
	}
	if want := []int{6, 7, 14}; !slices.Equal(gated.proposed, want) {
		t.Errorf("gated proposed = %v, want %v", gated.proposed, want)
	}
	if want := []int{7, 14}; !slices.Equal(split.ungatedProposed, want) {
		t.Errorf("ungatedProposed = %v, want %v", split.ungatedProposed, want)
	}
	// #8 is a container, #10 and #11 are held: none of them is triage.
	if want := []int{3, 13}; !slices.Equal(split.outside, want) {
		t.Errorf("outside = %v, want %v", split.outside, want)
	}
}

func TestUnscopedStatusHasNoGateSplit(t *testing.T) {
	t.Parallel()
	cfg, _ := statusConfigFor(t, gatedBacklog())

	_, split, err := statusQueues(context.Background(), cfg)
	if err != nil {
		t.Fatalf("statusQueues: %v", err)
	}
	if len(split.outside)+len(split.ungatedProposed) != 0 {
		t.Errorf("unscoped split = %+v, want empty", split)
	}
}

// A proposal lacking the gate label shows in `proposed` and gets its own
// curate clause naming both moves; outside issues get their own row.
func TestGatedStatusKeepsProposalsAndOutsideIssues(t *testing.T) {
	t.Parallel()
	cfg, _ := statusConfigFor(t, gatedBacklog())
	cfg.label = "ready"

	snap, err := readStatus(context.Background(), cfg, statusNow)
	if err != nil {
		t.Fatalf("readStatus: %v", err)
	}
	var out strings.Builder
	renderStatus(&out, report{}, cfg, snap)
	printed := out.String()
	for _, want := range []string{
		"proposed          3 issues — #6, #7, #14, labelled proposed",
		"outside the gate  2 issues — #3, #13",
		"curate #6 (drop proposed to queue them); curate #7, #14 (drop proposed, add ready)",
	} {
		if !strings.Contains(printed, want) {
			t.Errorf("report is missing %q\ngot:\n%s", want, printed)
		}
	}
	for _, unwanted := range []string{"#8", "#10", "#11"} {
		if strings.Contains(printed, unwanted) {
			t.Errorf("report names %s, an out-of-gate container or hold\ngot:\n%s", unwanted, printed)
		}
	}

	var jsonOut strings.Builder
	if err := renderStatusJSON(&jsonOut, cfg, snap); err != nil {
		t.Fatalf("renderStatusJSON: %v", err)
	}
	var doc statusDoc
	if err := json.Unmarshal([]byte(jsonOut.String()), &doc); err != nil {
		t.Fatalf("output did not parse as JSON: %v\n%s", err, jsonOut.String())
	}
	if want := []int{3, 13}; !slices.Equal(doc.Queue.OutsideGate, want) {
		t.Errorf("queue.outside_gate = %v, want %v", doc.Queue.OutsideGate, want)
	}
	if want := []int{6, 7, 14}; !slices.Equal(doc.Queue.Proposed, want) {
		t.Errorf("queue.proposed = %v, want %v", doc.Queue.Proposed, want)
	}
}

// A gate with nothing behind it, on a repository that isn't empty, is not a
// cleared backlog.
func TestGatedStatusDoesNotCallOutsideIssuesCleared(t *testing.T) {
	t.Parallel()
	cfg, _ := statusConfigFor(t, &ghState{Issues: map[string]*fakeIssue{"1": {Open: true}}})
	cfg.label = "ready"

	snap, err := readStatus(context.Background(), cfg, statusNow)
	if err != nil {
		t.Fatalf("readStatus: %v", err)
	}
	var out strings.Builder
	renderStatus(&out, report{}, cfg, snap)
	printed := out.String()
	if strings.Contains(printed, "backlog cleared") {
		t.Errorf("an open issue outside the gate is not a cleared backlog:\n%s", printed)
	}
	if want := "next              nothing — every open issue is outside the gate"; !strings.Contains(printed, want) {
		t.Errorf("report is missing %q\ngot:\n%s", want, printed)
	}
}

func TestOutsideGateLineStopsAtTen(t *testing.T) {
	t.Parallel()
	var nums []int
	for n := 1; n <= 12; n++ {
		nums = append(nums, n)
	}
	want := "12 issues — #1, #2, #3, #4, #5, #6, #7, #8, #9, #10 and 2 more"
	if got := outsideGateLine(nums); got != want {
		t.Errorf("outsideGateLine = %q, want %q", got, want)
	}
	if got, want := outsideGateLine([]int{4}), "1 issue — #4"; got != want {
		t.Errorf("outsideGateLine = %q, want %q", got, want)
	}
}
