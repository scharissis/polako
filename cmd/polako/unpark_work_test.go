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
	items, err := readParkedIssues(context.Background(), cfg, 0)
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
	items, err := readParkedIssues(context.Background(), cfg, 0)
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
	items, err := readParkedIssues(context.Background(), cfg, 0)
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
