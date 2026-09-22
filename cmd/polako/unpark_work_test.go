package main

// Tests for readParkWork/parkWorkSummary/renderParkWorkDetail
// (unpark_work.go) — split out of unpark_test.go alongside that file
// (issue #531). unparkCfg and findParkListItem are shared with
// unpark_test.go, which still declares them.

import (
	"context"
	"strings"
	"testing"
)

// The three shapes the acceptance criteria name: a PR with a failing check,
// a pushed branch with no PR, and nothing at all. Each row says so, and the
// one-issue view names the failing check for the first.
func TestUnparkReadsEachParkedIssuesWork(t *testing.T) {
	t.Parallel()
	st := &ghState{
		DefaultBranch: "main",
		Issues: map[string]*fakeIssue{
			"16": {Open: true, Labels: []string{needsHumanLabel}},
			"22": {Open: true, Labels: []string{needsHumanLabel}},
			"30": {Open: true, Labels: []string{needsHumanLabel}},
			"40": {Open: true, Labels: []string{needsHumanLabel}},
		},
		PRs: map[string]*fakePR{
			"issue-16": {Number: 84, State: "OPEN", Mergeable: "MERGEABLE", Head: "abc123", Checks: []string{"FAILURE"}},
			// Closed without merging — the parkPRClosed case (issue.go): the
			// PR still exists, but its checks are not what a human is
			// deciding on, and it must never render like a live PR.
			"issue-40": {Number: 90, State: "CLOSED", Mergeable: "MERGEABLE"},
		},
		CompareAhead: map[string]int{"issue-22": 4},
	}
	cfg := unparkCfg(t, st)
	items, err := readParkedIssues(context.Background(), cfg, 0, false)
	if err != nil {
		t.Fatalf("readParkedIssues: %v", err)
	}
	if len(items) != 4 {
		t.Fatalf("items = %+v, want #16, #22, #30 and #40", items)
	}

	got16 := findParkListItem(t, items, 16)
	if !got16.work.read || got16.work.prNumber != 84 || got16.work.checks != checksFailing {
		t.Errorf("#16 work = %+v, want a read of PR #84 with checks failing", got16.work)
	}
	if got := parkWorkSummary(got16.work); got != "PR #84, CI red" {
		t.Errorf("#16 work summary = %q, want %q", got, "PR #84, CI red")
	}

	got22 := findParkListItem(t, items, 22)
	if !got22.work.read || got22.work.prNumber != 0 || !got22.work.onOrigin || got22.work.ahead != 4 {
		t.Errorf("#22 work = %+v, want a read of a pushed branch, 4 commits ahead, no PR", got22.work)
	}
	if got := parkWorkSummary(got22.work); got != "issue-22, 4 commits, no PR" {
		t.Errorf("#22 work summary = %q, want %q", got, "issue-22, 4 commits, no PR")
	}

	got30 := findParkListItem(t, items, 30)
	if !got30.work.read || got30.work.prNumber != 0 || got30.work.onOrigin {
		t.Errorf("#30 work = %+v, want a read of nothing pushed", got30.work)
	}
	if got := parkWorkSummary(got30.work); got != "nothing pushed" {
		t.Errorf("#30 work summary = %q, want %q", got, "nothing pushed")
	}

	got40 := findParkListItem(t, items, 40)
	if !got40.work.read || got40.work.prNumber != 90 || got40.work.prState != "CLOSED" {
		t.Errorf("#40 work = %+v, want a read of closed PR #90", got40.work)
	}
	if got := parkWorkSummary(got40.work); got != "PR #90 (closed)" {
		t.Errorf("#40 work summary = %q, want %q — a closed PR must never read like a live one", got, "PR #90 (closed)")
	}

	var table strings.Builder
	renderUnpark(&table, report{}, cfg, items, false)
	for _, want := range []string{"PR #84, CI red", "issue-22, 4 commits, no PR", "nothing pushed", "PR #90 (closed)"} {
		if !strings.Contains(table.String(), want) {
			t.Errorf("table missing %q:\n%s", want, table.String())
		}
	}

	var one strings.Builder
	renderUnpark(&one, report{}, cfg, []parkListItem{got16}, true)
	for _, want := range []string{"https://example.invalid/pr/84", "check-1"} {
		if !strings.Contains(one.String(), want) {
			t.Errorf("one-issue view for #16 missing %q:\n%s", want, one.String())
		}
	}

	var closedOne strings.Builder
	renderUnpark(&closedOne, report{}, cfg, []parkListItem{got40}, true)
	if !strings.Contains(closedOne.String(), "https://example.invalid/pr/90 (closed)") {
		t.Errorf("one-issue view for #40 missing the closed marker:\n%s", closedOne.String())
	}
}

// A PR exists, but reading its checks keeps failing (a transient network
// blip that outlasts every retry) — this must render "not read", not a
// plain "PR #N" indistinguishable from a genuinely green one.
func TestUnparkWorkNotReadWhenPRChecksFail(t *testing.T) {
	t.Parallel()
	st := &ghState{
		Issues: map[string]*fakeIssue{
			"16": {Open: true, Labels: []string{needsHumanLabel}},
		},
		PRs: map[string]*fakePR{
			"issue-16": {Number: 84, State: "OPEN", Mergeable: "MERGEABLE"},
		},
		FailReads: map[string]int{"pr view": 10},
	}
	cfg := unparkCfg(t, st)
	items, err := readParkedIssues(context.Background(), cfg, 0, false)
	if err != nil {
		t.Fatalf("readParkedIssues: %v", err)
	}
	got := findParkListItem(t, items, 16)
	if got.work.read {
		t.Errorf("#16 work = %+v, want an unread row — checks never answered", got.work)
	}
	if summary := parkWorkSummary(got.work); summary != "not read" {
		t.Errorf("work summary = %q, want %q", summary, "not read")
	}
}

// nextShiftLine's shapes, off parkWork (and, for the local-worktree case,
// leftWork) directly rather than a full gh fixture — readParkWork's own read
// is TestUnparkReadsEachParkedIssuesWork's job, this is the wording that
// comes out of what it read. local is the zero value throughout except the
// one case naming it: that's what every caller passes when -repo was given,
// or -dir wasn't a checkout, and it must fall through to today's wording
// exactly as before readParkedIssues could ever read local disk state.
func TestNextShiftLine(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		work  parkWork
		local leftWork
		want  string
	}{
		{"open PR, red CI", parkWork{read: true, prNumber: 84, prState: "OPEN", checks: checksFailing},
			leftWork{}, "waits on PR #84 and remediates its red CI"},
		{"open PR, green", parkWork{read: true, prNumber: 84, prState: "OPEN", checks: checksPassing},
			leftWork{}, "waits on PR #84"},
		{"closed PR", parkWork{read: true, prNumber: 90, prState: "CLOSED"}, leftWork{}, "waits on PR #90"},
		{"pushed branch, no PR", parkWork{read: true, branch: "issue-22", onOrigin: true, ahead: 4},
			leftWork{}, "resumes issue-22 from its 4 commits"},
		{"nothing pushed", parkWork{read: true, branch: "issue-30"}, leftWork{}, "starts over — nothing was pushed"},
		{"not read", parkWork{read: false, branch: "issue-40"}, leftWork{}, "not read"},
		{"nothing pushed to origin, but local commits", parkWork{read: true, branch: "issue-50"},
			leftWork{branch: "issue-50", commits: 2}, "resumes from the local worktree"},
	} {
		if got := nextShiftLine(tc.work, tc.local); got != tc.want {
			t.Errorf("%s: nextShiftLine = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// staleRedCIWarning fires only for a checks-remediation park still showing
// red CI right now — not a park of a different category, and not once the
// branch has actually moved past red.
func TestStaleRedCIWarning(t *testing.T) {
	t.Parallel()
	red := parkWork{prNumber: 84, prState: "OPEN", checks: checksFailing}
	green := parkWork{prNumber: 84, prState: "OPEN", checks: checksPassing}
	if got := staleRedCIWarning(parkChecks, red); got == "" {
		t.Error("want a warning for a checks_remediation park still showing red CI")
	}
	if got := staleRedCIWarning(parkChecks, green); got != "" {
		t.Errorf("staleRedCIWarning = %q, want none once CI is no longer red", got)
	}
	if got := staleRedCIWarning(parkBudget, red); got != "" {
		t.Errorf("staleRedCIWarning = %q, want none for a non-checks category", got)
	}
}

// The three next-shift lines the acceptance criteria name, rendered through
// the real one-issue view and through -apply's own per-issue print, plus the
// stale-red-CI warning on a fourth, checks_remediation fixture.
func TestUnparkPrintsNextShiftLine(t *testing.T) {
	t.Parallel()
	st := &ghState{
		DefaultBranch: "main",
		Issues: map[string]*fakeIssue{
			"16": {Open: true, Labels: []string{needsHumanLabel}},
			"22": {Open: true, Labels: []string{needsHumanLabel}},
			"30": {Open: true, Labels: []string{needsHumanLabel}},
			"55": {Open: true, Labels: []string{needsHumanLabel}, Comments: 1,
				Bodies: map[int]string{1: parkCommentBody(55, "CI on PR #99 is still red", nil, parkChecks)}},
		},
		PRs: map[string]*fakePR{
			"issue-16": {Number: 84, State: "OPEN", Mergeable: "MERGEABLE", Head: "abc123", Checks: []string{"FAILURE"}},
			"issue-55": {Number: 99, State: "OPEN", Mergeable: "MERGEABLE", Head: "def456", Checks: []string{"FAILURE"}},
		},
		CompareAhead: map[string]int{"issue-22": 4},
	}
	cfg := unparkCfg(t, st)
	items, err := readParkedIssues(context.Background(), cfg, 0, false)
	if err != nil {
		t.Fatalf("readParkedIssues: %v", err)
	}

	for _, tc := range []struct {
		issue int
		want  string
	}{
		{16, "waits on PR #84 and remediates its red CI"},
		{22, "resumes issue-22 from its 4 commits"},
		{30, "starts over — nothing was pushed"},
	} {
		it := findParkListItem(t, items, tc.issue)
		var one strings.Builder
		renderUnpark(&one, report{}, cfg, []parkListItem{it}, true)
		if !strings.Contains(one.String(), tc.want) {
			t.Errorf("#%d one-issue view missing next-shift line %q:\n%s", tc.issue, tc.want, one.String())
		}
	}

	got55 := findParkListItem(t, items, 55)
	if got55.category != parkChecks {
		t.Fatalf("#55 category = %q, want %q", got55.category, parkChecks)
	}
	var one55 strings.Builder
	renderUnpark(&one55, report{}, cfg, []parkListItem{got55}, true)
	if !strings.Contains(one55.String(), "the next shift will remediate the same red and likely park again") {
		t.Errorf("#55 one-issue view missing the stale-red-CI warning:\n%s", one55.String())
	}

	// -apply prints the same lines right after removing each label.
	var out strings.Builder
	prompt := newSetupPrompt(strings.NewReader(""), &out, false)
	applyUnpark(context.Background(), prompt, true, &out, cfg, items)
	for _, want := range []string{
		"waits on PR #84 and remediates its red CI",
		"resumes issue-22 from its 4 commits",
		"starts over — nothing was pushed",
		"the next shift will remediate the same red and likely park again",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("-apply output missing %q:\n%s", want, out.String())
		}
	}
}

// A failed read anywhere in the chain — here, a repository whose default
// branch this fake never learned, so the compare has nothing to compare
// against — renders "not read" and still leaves the row in the listing.
func TestUnparkWorkNotReadWhenDefaultBranchFails(t *testing.T) {
	t.Parallel()
	st := &ghState{
		Issues: map[string]*fakeIssue{
			"16": {Open: true, Labels: []string{needsHumanLabel}},
		},
		FailReads: map[string]int{"api repo": 10},
	}
	cfg := unparkCfg(t, st)
	items, err := readParkedIssues(context.Background(), cfg, 0, false)
	if err != nil {
		t.Fatalf("readParkedIssues: %v", err)
	}
	got := findParkListItem(t, items, 16)
	if got.work.read {
		t.Errorf("#16 work = %+v, want an unread row", got.work)
	}
	if summary := parkWorkSummary(got.work); summary != "not read" {
		t.Errorf("work summary = %q, want %q", summary, "not read")
	}
}
