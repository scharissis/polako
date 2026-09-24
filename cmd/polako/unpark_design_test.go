package main

// Issue #553: a parked design request names `polako design -issue N`, not a
// `work` shift, in both unpark and status. The ordinary issues beside it keep
// today's wording, which the other unpark and status tests pin byte for byte.

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestUnparkNamesDesignVerbForParkedDesignIssue(t *testing.T) {
	t.Parallel()
	st := &ghState{
		DefaultBranch: "main",
		Issues: map[string]*fakeIssue{
			"7":  {Open: true, Labels: []string{needsHumanLabel, designLabel}},
			"8":  {Open: true, Labels: []string{needsHumanLabel, designLabel}},
			"22": {Open: true, Labels: []string{needsHumanLabel}},
		},
		PRs: map[string]*fakePR{
			"issue-8": {Number: 81, State: "OPEN", Mergeable: "MERGEABLE", Head: "abc123"},
		},
		CompareAhead: map[string]int{"issue-7": 2, "issue-22": 4},
	}
	cfg := unparkCfg(t, st)
	items, err := readParkedIssues(context.Background(), cfg, 0, false)
	if err != nil {
		t.Fatalf("readParkedIssues: %v", err)
	}
	for _, tc := range []struct {
		issue  int
		design bool
		want   string
	}{
		{7, true, "polako design -issue 7 resumes issue-7 from its 2 commits"},
		{8, true, "polako design -issue 8 waits on PR #81"},
		{22, false, "resumes issue-22 from its 4 commits"},
	} {
		it := findParkListItem(t, items, tc.issue)
		if it.design != tc.design {
			t.Errorf("#%d design = %v, want %v", tc.issue, it.design, tc.design)
		}
		if got := parkNextShift(it); got != tc.want {
			t.Errorf("#%d parkNextShift = %q, want %q", tc.issue, got, tc.want)
		}
		var one strings.Builder
		renderUnpark(&one, report{}, cfg, []parkListItem{it}, true)
		if !strings.Contains(one.String(), "next shift  "+tc.want+"\n") {
			t.Errorf("#%d one-issue view missing %q:\n%s", tc.issue, tc.want, one.String())
		}
	}

	var out strings.Builder
	prompt := newSetupPrompt(strings.NewReader(""), &out, false)
	applyUnpark(context.Background(), prompt, true, &out, cfg, items)
	for _, want := range []string{
		"#7 next shift: polako design -issue 7 resumes issue-7 from its 2 commits\n",
		"#8 next shift: polako design -issue 8 waits on PR #81\n",
		"#22 next shift: resumes issue-22 from its 4 commits\n",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("-apply output missing %q:\n%s", want, out.String())
		}
	}
}

// A design item whose row couldn't be read says "not read" and nothing else:
// there's no action to attribute to the verb.
func TestParkNextShiftDesignNotRead(t *testing.T) {
	t.Parallel()
	it := parkListItem{issue: 7, design: true, work: parkWork{branch: "issue-7"}}
	if got := parkNextShift(it); got != "not read" {
		t.Errorf("parkNextShift = %q, want %q", got, "not read")
	}
}

func TestStatusNamesDesignVerbForParkedDesignIssue(t *testing.T) {
	t.Parallel()
	cfg, _ := statusConfigFor(t, &ghState{
		Issues: map[string]*fakeIssue{
			"7": {Open: true, Labels: []string{needsHumanLabel, designLabel}, Comments: 1,
				Bodies: map[int]string{1: parkCommentBody(7, "it hit -max-issue-time", nil, parkBudget)}},
			"9": {Open: true, Labels: []string{needsHumanLabel, designLabel}},
			"13": {Open: true, Labels: []string{needsHumanLabel}, Comments: 1,
				Bodies: map[int]string{1: parkCommentBody(13, "it hit -max-issue-time", nil, parkBudget)}},
			"22": {Open: true, Labels: []string{needsHumanLabel}},
		},
	})
	snap, err := readStatus(context.Background(), cfg, statusNow)
	if err != nil {
		t.Fatalf("readStatus: %v", err)
	}
	got := needsYou(snap)
	for _, want := range []string{
		fmt.Sprintf("#7 %s — polako unpark 7, then polako design -issue 7", parkNeedsYouClause[parkBudget]),
		"#9 is a parked design request — polako unpark 9, then polako design -issue 9",
		fmt.Sprintf("#13 %s — polako unpark 13;", parkNeedsYouClause[parkBudget]),
		"decide what to do about #22 (drop needs-human to requeue)",
	} {
		if !strings.Contains(got+";", want) {
			t.Errorf("needsYou = %q, want it to contain %q", got, want)
		}
	}
	if strings.Contains(got, "#9 (drop") || strings.Contains(got, "#9, ") {
		t.Errorf("needsYou = %q, #9 is a design park and must not join the requeue clause", got)
	}
}

func TestParkedDesignClauseWithEntries(t *testing.T) {
	t.Parallel()
	it := parkListItem{issue: 7, design: true, category: parkPermission, entries: []string{"Bash(echo:*)"}}
	want := "grant Bash(echo:*) or fix the skill, then polako unpark 7, then polako design -issue 7"
	if got := parkedDesignClause(it); got != want {
		t.Errorf("parkedDesignClause = %q, want %q", got, want)
	}
}
