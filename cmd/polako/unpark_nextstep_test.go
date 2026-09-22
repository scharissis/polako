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
// separately below: its sentence depends on what its comment named, not on
// a table lookup.
func TestParkNextStepCoversEveryCategory(t *testing.T) {
	t.Parallel()
	for _, category := range parkReasonOrder {
		if category == parkPermission {
			continue
		}
		got := parkNextStep(parkListItem{category: category})
		if got == "" || got == parkNextStepUnlabeled {
			t.Errorf("parkNextStep(%q) = %q, want a real sentence for this category", category, got)
		}
	}
}

// permission_refused reads what its comment named rather than the table: a
// valid, grantable entry points at rerunning with -add-tools; an entry
// validParkEntry rejected (it.ignored, never grantable) says so instead of
// claiming nothing was named; no entry at all falls back to reading the log
// or the thread.
func TestParkNextStepPermissionRefused(t *testing.T) {
	t.Parallel()
	withEntry := parkListItem{category: parkPermission, entries: []string{"Bash(echo:*)"}}
	if got := parkNextStep(withEntry); !strings.Contains(got, "-add-tools") {
		t.Errorf("parkNextStep(permission, entry) = %q, want it to point at rerunning with -add-tools", got)
	}
	withIgnored := parkListItem{category: parkPermission, ignored: []string{"rm -rf /"}}
	if got := parkNextStep(withIgnored); got != parkNextStepUngrantable {
		t.Errorf("parkNextStep(permission, ignored only) = %q, want %q", got, parkNextStepUngrantable)
	}
	withNothing := parkListItem{category: parkPermission}
	if got := parkNextStep(withNothing); got != parkNextStepNoEntries {
		t.Errorf("parkNextStep(permission, nothing named) = %q, want %q", got, parkNextStepNoEntries)
	}
}

// No category at all — a hand-applied label, or a comment readParkListItem
// couldn't parse as polako's own — gets the short fallback, not the
// per-category "these end a shift" framing.
func TestParkNextStepNoCategory(t *testing.T) {
	t.Parallel()
	if got := parkNextStep(parkListItem{}); got != parkNextStepUnlabeled {
		t.Errorf("parkNextStep({}) = %q, want %q", got, parkNextStepUnlabeled)
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
	items, err := readParkedIssues(context.Background(), cfg, 413, false)
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

// A permission park with a named, grantable entry says to rerun with
// -add-tools, in both the one-issue view and the -apply confirmation
// question — not the no-entries fallback that would apply if the entry were
// missed.
func TestUnparkPermissionWithEntriesPointsAtAddTools(t *testing.T) {
	t.Parallel()
	st := &ghState{
		Issues: map[string]*fakeIssue{
			"16": {Open: true, Labels: []string{needsHumanLabel}, Comments: 1,
				Bodies: map[int]string{1: parkCommentBody(16, "the run was refused Bash(echo:*)",
					[]string{"Bash(echo:*)"}, parkPermission)}},
		},
	}
	cfg := unparkCfg(t, st)
	items, err := readParkedIssues(context.Background(), cfg, 16, false)
	if err != nil {
		t.Fatalf("readParkedIssues: %v", err)
	}
	it := findParkListItem(t, items, 16)
	if it.category != parkPermission || len(it.entries) == 0 {
		t.Fatalf("item = %+v, want a permission park with a named entry", it)
	}

	var one strings.Builder
	renderUnpark(&one, report{}, cfg, []parkListItem{it}, true)
	if !strings.Contains(one.String(), "-add-tools") {
		t.Errorf("one-issue view missing the -add-tools next step:\n%s", one.String())
	}

	var out strings.Builder
	prompt := newSetupPrompt(strings.NewReader("n\n"), &out, false)
	applyUnpark(context.Background(), prompt, false, &out, cfg, []parkListItem{it})
	if !strings.Contains(out.String(), "-add-tools") {
		t.Errorf("-apply question missing the -add-tools next step:\n%s", out.String())
	}
}

// A permission park whose comment named an entry validParkEntry rejects —
// rendered elsewhere as "(ignored)" — must not claim nothing was named: it
// gets its own sentence, distinct from both the grantable-entry case and
// the nothing-named one.
func TestUnparkPermissionWithOnlyIgnoredEntry(t *testing.T) {
	t.Parallel()
	// "Bash(gh pr merge:*)" matches the footer's own shape but
	// hasNeverGrantPrefix refuses it (refusals.go), so it lands in
	// it.ignored rather than it.entries.
	st := &ghState{
		Issues: map[string]*fakeIssue{
			"16": {Open: true, Labels: []string{needsHumanLabel}, Comments: 1,
				Bodies: map[int]string{1: parkCommentBody(16, "the run was refused something",
					[]string{"Bash(gh pr merge:*)"}, parkPermission)}},
		},
	}
	cfg := unparkCfg(t, st)
	items, err := readParkedIssues(context.Background(), cfg, 16, false)
	if err != nil {
		t.Fatalf("readParkedIssues: %v", err)
	}
	it := findParkListItem(t, items, 16)
	if len(it.entries) != 0 || len(it.ignored) == 0 {
		t.Fatalf("item = %+v, want an ignored entry and no grantable one", it)
	}
	if got := parkNextStep(it); got != parkNextStepUngrantable {
		t.Errorf("parkNextStep(ignored-only) = %q, want %q", got, parkNextStepUngrantable)
	}
}
