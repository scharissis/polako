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
		{Issue: 9, QuietSeconds: secs(12 * 24 * time.Hour)},
		{Issue: 12, QuietSeconds: secs(3 * 24 * time.Hour)},
		{Issue: 13, Effort: "high"},
	}
	got := doc.Queue.Details
	if len(got) != len(want) {
		t.Fatalf("queue.details = %+v, want %+v", got, want)
	}
	for i := range want {
		g, w := got[i], want[i]
		sameQuiet := (g.QuietSeconds == nil) == (w.QuietSeconds == nil) &&
			(g.QuietSeconds == nil || *g.QuietSeconds == *w.QuietSeconds)
		if g.Issue != w.Issue || g.Model != w.Model || g.Effort != w.Effort || !sameQuiet {
			t.Errorf("queue.details[%d] = %+v, want %+v", i, g, w)
		}
	}
}

// model:default is a real choice — the account default — so it shows as one.
func TestPolicyNoteNamesTheDefaultModel(t *testing.T) {
	t.Parallel()
	q := sortIssueQueues([]ghIssue{{Number: 1, Labels: labelsFrom("model:default", "effort:low")}})
	if got, want := q.policyNote(1), "default model, low"; got != want {
		t.Errorf("policyNote = %q, want %q", got, want)
	}
}
