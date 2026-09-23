package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// What the listing already carries reaches the report: a ready or held-back
// issue's own model:/effort: labels, and how long a parked or proposed issue
// has sat. A malformed label shows nothing, the way a pickup falls through it.
func TestStatusShowsListingDetails(t *testing.T) {
	t.Parallel()
	ago := func(d time.Duration) string { return statusNow.Add(-d).Format(time.RFC3339) }
	cfg, _ := statusConfigFor(t, &ghState{
		Issues: map[string]*fakeIssue{
			"3":  {Open: true, Labels: []string{"model:opus", "effort:max"}, UpdatedAt: ago(time.Hour)},
			"5":  {Open: true, Labels: []string{"effort:turbo"}},
			"9":  {Open: true, Labels: []string{needsHumanLabel}, UpdatedAt: ago(12 * 24 * time.Hour)},
			"12": {Open: true, Labels: []string{proposedLabel}, UpdatedAt: ago(3 * 24 * time.Hour)},
			"13": {Open: true, Labels: []string{"effort:high"}, BlockedBy: []int{3, 5}},
			// Parked with no date: shown bare rather than as fresh.
			"15": {Open: true, Labels: []string{needsHumanLabel}},
		},
	})

	snap, err := readStatus(context.Background(), cfg, statusNow)
	if err != nil {
		t.Fatalf("readStatus: %v", err)
	}

	var out strings.Builder
	renderStatus(&out, report{}, cfg, snap)
	printed := out.String()
	for _, want := range []string{
		"ready      2 issues — #3 (opus, max), #5\n",
		"held back  1 issue — #13 (behind #3, #5; high)\n",
		"parked     2 issues — #9 (quiet 12d), #15, labelled needs-human\n",
		"proposed   1 issue — #12 (quiet 3d), labelled proposed\n",
	} {
		if !strings.Contains(printed, want) {
			t.Errorf("report is missing %q\ngot:\n%s", want, printed)
		}
	}
	if strings.Contains(printed, "turbo") {
		t.Errorf("a malformed effort: label reached the report\ngot:\n%s", printed)
	}

	out.Reset()
	if err := renderStatusJSON(&out, cfg, snap); err != nil {
		t.Fatalf("renderStatusJSON: %v", err)
	}
	var doc statusDoc
	if err := json.Unmarshal([]byte(out.String()), &doc); err != nil {
		t.Fatalf("output did not parse as JSON: %v\n%s", err, out.String())
	}
	secs := func(d time.Duration) *int64 { s := int64(d.Seconds()); return &s }
	want := []statusDocDetail{
		{Issue: 3, Model: "opus", Effort: "max"},
		{Issue: 9, IdleSeconds: secs(12 * 24 * time.Hour)},
		{Issue: 12, IdleSeconds: secs(3 * 24 * time.Hour)},
		{Issue: 13, Effort: "high"},
	}
	got := doc.Queue.Details
	if len(got) != len(want) {
		t.Fatalf("queue.details = %+v, want %+v", got, want)
	}
	for i := range want {
		g, w := got[i], want[i]
		sameIdle := (g.IdleSeconds == nil) == (w.IdleSeconds == nil) &&
			(g.IdleSeconds == nil || *g.IdleSeconds == *w.IdleSeconds)
		if g.Issue != w.Issue || g.Model != w.Model || g.Effort != w.Effort || !sameIdle {
			t.Errorf("queue.details[%d] = %+v, want %+v", i, g, w)
		}
	}
}

// Gated, a proposal without the gate label comes from the second, unscoped
// listing — so its age has to come from there too.
func TestStatusGatedProposalShowsItsAge(t *testing.T) {
	t.Parallel()
	cfg, _ := statusConfigFor(t, &ghState{
		Issues: map[string]*fakeIssue{
			"4": {Open: true, Labels: []string{"gate"}},
			"8": {Open: true, Labels: []string{proposedLabel}, UpdatedAt: statusNow.Add(-5 * 24 * time.Hour).Format(time.RFC3339)},
		},
	})
	cfg.label = "gate"
	snap, err := readStatus(context.Background(), cfg, statusNow)
	if err != nil {
		t.Fatalf("readStatus: %v", err)
	}
	var out strings.Builder
	renderStatus(&out, report{}, cfg, snap)
	if want := "1 issue — #8 (quiet 5d), labelled proposed"; !strings.Contains(out.String(), want) {
		t.Errorf("report is missing %q\ngot:\n%s", want, out.String())
	}
}

// A parked issue's recorded reason comes first, its age after.
func TestParkedRowPutsTheReasonBeforeTheAge(t *testing.T) {
	t.Parallel()
	snap := statusSnapshot{
		queues:  issueQueues{parked: []int{9}},
		idle:    map[int]time.Duration{9: 12 * 24 * time.Hour},
		runData: statusRunData{parkReasons: map[int]string{9: "budget"}},
	}
	for _, p := range queuePairs(snap) {
		if p[0] == "parked" {
			if want := "1 issue — #9 (budget, quiet 12d), labelled needs-human"; p[1] != want {
				t.Errorf("parked row = %q, want %q", p[1], want)
			}
			return
		}
	}
	t.Fatal("no parked row")
}

// model:default is a real choice — the account default — so it shows as one.
func TestPolicyNoteNamesTheDefaultModel(t *testing.T) {
	t.Parallel()
	q := sortIssueQueues([]ghIssue{{Number: 1, Labels: labelsFrom("model:default", "effort:low")}})
	if got, want := q.policyNote(1), "default model, low"; got != want {
		t.Errorf("policyNote = %q, want %q", got, want)
	}
}
