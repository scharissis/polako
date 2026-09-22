package main

// Tests for parkNextStep (unpark_work.go) — docs/plans/unpark.md ticket 4:
// the fixed table mapping each park category to one short next-step
// sentence, printed in the one-issue view and beside each -apply question.

import (
	"context"
	"strings"
	"testing"
)

// Every category in parkReasonOrder (metrics.go) must have a real sentence —
// not the unlabeled fallback, which is what a forgotten table entry would
// silently return instead of failing loudly. parkPermission is handled
// separately below: its sentence depends on whether entries were named, not
// on a table lookup.
func TestParkNextStepCoversEveryCategory(t *testing.T) {
	t.Parallel()
	for _, category := range parkReasonOrder {
		if category == parkPermission {
			continue
		}
		got := parkNextStep(category, false)
		if got == "" || got == parkNextStepUnlabeled {
			t.Errorf("parkNextStep(%q, false) = %q, want a real sentence for this category", category, got)
		}
	}
}

// permission_refused reads its second argument rather than the table: named
// entries point at the rerun line unpark already prints, no entries falls
// back to reading the run's own log or the thread.
func TestParkNextStepPermissionRefused(t *testing.T) {
	t.Parallel()
	if got := parkNextStep(parkPermission, true); !strings.Contains(got, "rerun line") {
		t.Errorf("parkNextStep(permission, entries) = %q, want it to point at the rerun line", got)
	}
	if got := parkNextStep(parkPermission, false); got != parkNextStepNoEntries {
		t.Errorf("parkNextStep(permission, no entries) = %q, want %q", got, parkNextStepNoEntries)
	}
}

// No category at all — a hand-applied label, or a comment readParkListItem
// couldn't parse as polako's own — gets the short fallback, not the
// per-category "these end a shift" framing.
func TestParkNextStepNoCategory(t *testing.T) {
	t.Parallel()
	if got := parkNextStep("", false); got != parkNextStepUnlabeled {
		t.Errorf("parkNextStep(\"\", false) = %q, want %q", got, parkNextStepUnlabeled)
	}
}

// The acceptance criteria's own example: a budget park's one-issue view
// prints the budget sentence.
func TestUnparkOneIssueViewPrintsBudgetNextStep(t *testing.T) {
	t.Parallel()
	st := &ghState{
		Issues: map[string]*fakeIssue{
			"413": {Open: true, Labels: []string{needsHumanLabel}, Comments: 1,
				Bodies: map[int]string{1: parkCommentBody(413, "hit the -max-issue-time cap", nil, parkBudget)}},
		},
	}
	cfg := unparkCfg(t, st)
	items, err := readParkedIssues(context.Background(), cfg, 413)
	if err != nil {
		t.Fatalf("readParkedIssues: %v", err)
	}
	it := findParkListItem(t, items, 413)
	if it.category != parkBudget {
		t.Fatalf("category = %q, want %q", it.category, parkBudget)
	}

	var one strings.Builder
	renderUnpark(&one, report{}, cfg, []parkListItem{it}, true)
	if !strings.Contains(one.String(), parkNextStepTable[parkBudget]) {
		t.Errorf("one-issue view missing the budget next step:\n%s", one.String())
	}

	var footer strings.Builder
	printUnparkNextStep(&footer, []parkListItem{it}, true)
	if !strings.Contains(footer.String(), parkNextStepTable[parkBudget]) {
		t.Errorf("footer missing the budget next step:\n%s", footer.String())
	}
}

// A permission park with named entries still points at the rerun line, in
// both the one-issue view and the -apply confirmation question — not the
// no-entries fallback that would apply if hasEntries were computed wrong.
func TestUnparkPermissionWithEntriesPointsAtRerunLine(t *testing.T) {
	t.Parallel()
	st := &ghState{
		Issues: map[string]*fakeIssue{
			"16": {Open: true, Labels: []string{needsHumanLabel}, Comments: 1,
				Bodies: map[int]string{1: parkCommentBody(16, "the run was refused Bash(echo:*)",
					[]string{"Bash(echo:*)"}, parkPermission)}},
		},
	}
	cfg := unparkCfg(t, st)
	items, err := readParkedIssues(context.Background(), cfg, 16)
	if err != nil {
		t.Fatalf("readParkedIssues: %v", err)
	}
	it := findParkListItem(t, items, 16)
	if it.category != parkPermission || len(it.entries) == 0 {
		t.Fatalf("item = %+v, want a permission park with a named entry", it)
	}

	var one strings.Builder
	renderUnpark(&one, report{}, cfg, []parkListItem{it}, true)
	if !strings.Contains(one.String(), "rerun line") {
		t.Errorf("one-issue view missing the rerun-line next step:\n%s", one.String())
	}

	var out strings.Builder
	prompt := newSetupPrompt(strings.NewReader("n\n"), &out, false)
	applyUnpark(context.Background(), prompt, false, &out, cfg, []parkListItem{it})
	if !strings.Contains(out.String(), "rerun line") {
		t.Errorf("-apply question missing the rerun-line next step:\n%s", out.String())
	}
}
