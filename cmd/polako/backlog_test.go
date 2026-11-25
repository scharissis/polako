package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// A gh from before sub-issues rejects the whole --json set rather than the one
// field it does not know, so the listing asks again without it. Containers
// and blockedBy dependencies are both workable/invisible on such a host —
// that is the price, and the warning is what makes it visible — but the
// curation gate is labels alone and does not depend on either. Sub-issues and
// blockedBy share one budget: a gh old enough to reject one is assumed too
// old for both, so the drop and the warning happen once, not once per field.
func TestOldGhListsWithoutTheSubIssueRollup(t *testing.T) {
	t.Parallel()
	buf := captureLog(t)
	cfg, _ := drainConfig(t, "stream", &ghState{
		OldGh: true,
		Issues: map[string]*fakeIssue{
			"1": {Open: true},
			"2": {Open: true, SubIssues: 2},
			"3": {Open: true, Labels: []string{proposedLabel}},
			// Would be held back on a gh that could see blockedBy — 1 is open —
			// but an old gh cannot ask for it at all, so it reads as ordinary work.
			"4": {Open: true, BlockedBy: []int{1}},
		},
	})
	calls := filepath.Join(t.TempDir(), "gh-calls.log")
	setFakeEnv(&cfg, fakeGhLogEnv, calls)

	// Twice, because both the fallback and the warning have to be paid for once
	// a shift rather than once per issue — and the drain lists once per issue.
	for range 2 {
		q, err := openQueues(context.Background(), cfg)
		if err != nil {
			t.Fatalf("an old gh must still produce a queue: %v", err)
		}
		if want := []int{1, 2, 4}; !slices.Equal(q.ready, want) {
			t.Errorf("ready = %v, want %v — a container and a blocker it cannot see are both ordinary work",
				q.ready, want)
		}
		if want := []int{3}; !slices.Equal(q.proposed, want) {
			t.Errorf("proposed = %v, want %v — the gate is labels, and labels every gh has", q.proposed, want)
		}
		if len(q.heldBack) != 0 {
			t.Errorf("heldBack = %+v, want none — this gh cannot see blockedBy at all", q.heldBack)
		}
	}

	if got := strings.Count(buf.String(), "gh too old to see sub-issues"); got != 1 {
		t.Errorf("the warning should come once a shift, got %d\n%s", got, buf.String())
	}
	argv, err := os.ReadFile(calls)
	if err != nil {
		t.Fatalf("reading the gh call log: %v", err)
	}
	if got := strings.Count(string(argv), subIssuesField); got != 1 {
		t.Errorf("the rollup should be asked for once and then given up on, asked %d times:\n%s", got, argv)
	}
	if got := strings.Count(string(argv), blockedByField); got != 1 {
		t.Errorf("blockedBy should be asked for once and then given up on, asked %d times:\n%s", got, argv)
	}
	if got := strings.Count(string(argv), "issue list"); got != 3 {
		t.Errorf("issue list calls = %d, want 3: the rejected one, its fallback, "+
			"and one listing that no longer asks\n%s", got, argv)
	}
}

// -strict-order folds an awaiting-answer issue back into ready so it keeps
// its place in line, but a held-back issue is not an awaiting-answer issue:
// running it again this pass cannot reveal anything the same listing did not
// already know, so it stays out of ready either way.
func TestOpenIssuesKeepsAHeldBackIssueOutUnderStrictOrder(t *testing.T) {
	t.Parallel()
	cfg, _ := drainConfig(t, "stream", &ghState{
		Issues: map[string]*fakeIssue{
			"1": {Open: true, Labels: []string{awaitingAnswerLabel}},
			"2": {Open: true, BlockedBy: []int{3}},
			"3": {Open: true},
		},
	})
	cfg.strictOrder = true

	ready, blocked, _, heldBack, _, err := openIssues(context.Background(), cfg)
	if err != nil {
		t.Fatalf("openIssues: %v", err)
	}
	if blocked != nil {
		t.Errorf("blocked = %v, want nil — -strict-order folds it into ready", blocked)
	}
	// Ready then the folded-in blocked queue appended, unsorted against each
	// other — the existing -strict-order shape, unchanged by this issue.
	if want := []int{3, 1}; !slices.Equal(ready, want) {
		t.Errorf("ready = %v, want %v — 1 is folded in by -strict-order, 2 stays out held back", ready, want)
	}
	want := []heldBackInfo{{number: 2, blockers: []int{3}}}
	if !heldBackEqual(heldBack, want) {
		t.Errorf("heldBack = %+v, want %+v", heldBack, want)
	}
}

func TestSelectableIssuesDropsParkedOnes(t *testing.T) {
	t.Parallel()
	raw := []byte(`[{"number":4,"labels":[{"name":"bug"}]},
		{"number":5,"labels":[{"name":"Needs-Human"}]},
		{"number":6,"labels":[]},
		{"number":7,"labels":[{"name":"bug"},{"name":"needs-human"}]}]`)

	q, err := selectableIssues(raw)
	if err != nil {
		t.Fatalf("selectableIssues: %v", err)
	}
	// 5 proves the match ignores case, the way GitHub treats label names.
	if want := []int{4, 6}; !slices.Equal(q.ready, want) {
		t.Errorf("ready = %v, want %v", q.ready, want)
	}
	if len(q.blocked) != 0 {
		t.Errorf("blocked = %v, want nothing waiting on an answer", q.blocked)
	}
	// Out of the drain's way, but still findable: `status` reports what is
	// parked, and a queue that merely forgot them could not.
	if want := []int{5, 7}; !slices.Equal(q.parked, want) {
		t.Errorf("parked = %v, want %v", q.parked, want)
	}
}

// The two queues are separate: a flagged issue is not ready, but it is not
// parked either, so it has to come back as something the drain can revisit.
func TestSelectableIssuesSeparatesBlockedOnes(t *testing.T) {
	t.Parallel()
	raw := []byte(`[{"number":9,"labels":[{"name":"Awaiting-Answer"}]},
		{"number":4,"labels":[{"name":"awaiting-answer"}]},
		{"number":6,"labels":[]},
		{"number":7,"labels":[{"name":"awaiting-answer"},{"name":"needs-human"}]}]`)

	q, err := selectableIssues(raw)
	if err != nil {
		t.Fatalf("selectableIssues: %v", err)
	}
	if want := []int{6}; !slices.Equal(q.ready, want) {
		t.Errorf("ready = %v, want %v", q.ready, want)
	}
	// Ascending, because the drain revisits the lowest first and gh guarantees
	// no order. 7 is parked as well as flagged, and parked wins.
	if want := []int{4, 9}; !slices.Equal(q.blocked, want) {
		t.Errorf("blocked = %v, want %v", q.blocked, want)
	}
	if want := []int{7}; !slices.Equal(q.parked, want) {
		t.Errorf("parked = %v, want %v", q.parked, want)
	}
}

// The curation gate: an issue a machine proposed is nobody's to work until a
// human takes the label off, so it is in neither queue a drain reads.
func TestSelectableIssuesDropsProposals(t *testing.T) {
	t.Parallel()
	raw := []byte(`[{"number":4,"labels":[{"name":"Proposed"}]},
		{"number":5,"labels":[{"name":"bug"}]},
		{"number":6,"labels":[{"name":"proposed"},{"name":"awaiting-answer"}]},
		{"number":7,"labels":[{"name":"proposed"},{"name":"needs-human"}]}]`)

	q, err := selectableIssues(raw)
	if err != nil {
		t.Fatalf("selectableIssues: %v", err)
	}
	if want := []int{5}; !slices.Equal(q.ready, want) {
		t.Errorf("ready = %v, want %v", q.ready, want)
	}
	if len(q.blocked) != 0 {
		t.Errorf("blocked = %v, want a proposal to be out of that queue too", q.blocked)
	}
	// 4 proves the match ignores case, the way GitHub treats label names. 7 is
	// parked as well as proposed, and parked wins: it is a decision a human has
	// already made, and counting it as a proposal would have the startup line
	// promise that removing one label would queue it.
	if want := []int{4, 6}; !slices.Equal(q.proposed, want) {
		t.Errorf("proposed = %v, want %v", q.proposed, want)
	}
	if want := []int{7}; !slices.Equal(q.parked, want) {
		t.Errorf("parked = %v, want %v", q.parked, want)
	}
}

// A design request wants a plan, not code, so it is in neither queue a drain
// reads — even mid-question, where awaiting-answer alone would put it in
// blocked. Parked and proposed still outrank it.
func TestSelectableIssuesDropsDesignRequests(t *testing.T) {
	t.Parallel()
	raw := []byte(`[{"number":4,"labels":[{"name":"Design"}]},
		{"number":5,"labels":[{"name":"design"},{"name":"awaiting-answer"}]},
		{"number":6,"labels":[{"name":"design"},{"name":"needs-human"}]},
		{"number":7,"labels":[{"name":"design"},{"name":"proposed"}]},
		{"number":8}]`)

	q, err := selectableIssues(raw)
	if err != nil {
		t.Fatalf("selectableIssues: %v", err)
	}
	if want := []int{8}; !slices.Equal(q.ready, want) {
		t.Errorf("ready = %v, want %v", q.ready, want)
	}
	if len(q.blocked) != 0 {
		t.Errorf("blocked = %v, want a design request mid-question out of it", q.blocked)
	}
	if want := []designInfo{{number: 4}, {number: 5, awaiting: true}}; !slices.Equal(q.design, want) {
		t.Errorf("design = %+v, want %+v", q.design, want)
	}
	if want := []int{6}; !slices.Equal(q.parked, want) {
		t.Errorf("parked = %v, want %v", q.parked, want)
	}
	if want := []int{7}; !slices.Equal(q.proposed, want) {
		t.Errorf("proposed = %v, want %v", q.proposed, want)
	}
	if want := []int{8, 6, 7, 4, 5}; !slices.Equal(q.open(), want) {
		t.Errorf("open() = %v, want %v", q.open(), want)
	}
}

// An issue with sub-issues is a tracking container rather than a work item, and
// what makes it one is its shape rather than anything written on it — which is
// what also protects a parent somebody made by hand.
func TestSelectableIssuesDropsContainers(t *testing.T) {
	t.Parallel()
	raw := []byte(`[{"number":4,"subIssuesSummary":{"total":3,"completed":1}},
		{"number":5,"labels":[{"name":"bug"}],"subIssuesSummary":{"total":1}},
		{"number":6,"labels":[{"name":"needs-human"}],"subIssuesSummary":{"total":2}},
		{"number":7,"labels":[{"name":"proposed"}],"subIssuesSummary":{"total":2}},
		{"number":8,"labels":[],"subIssuesSummary":{"total":0}},
		{"number":9}]`)

	q, err := selectableIssues(raw)
	if err != nil {
		t.Fatalf("selectableIssues: %v", err)
	}
	// 8 and 9 are the controls: a rollup that says zero and a payload with no
	// rollup at all are both ordinary work.
	if want := []int{8, 9}; !slices.Equal(q.ready, want) {
		t.Errorf("ready = %v, want %v", q.ready, want)
	}
	if len(q.blocked)+len(q.parked)+len(q.proposed) != 0 {
		t.Errorf("blocked/parked/proposed = %v/%v/%v, want a container in no queue at all: "+
			"it is not held back by anything a human could release",
			q.blocked, q.parked, q.proposed)
	}
	want := []containerInfo{
		{number: 4, total: 3, completed: 1},
		{number: 5, total: 1, completed: 0},
		// needs-human and proposed still land a container here, not in parked or
		// proposed — but held records the label so the drain leaves a finished
		// one for a human rather than closing it.
		{number: 6, total: 2, completed: 0, held: true},
		{number: 7, total: 2, completed: 0, held: true},
	}
	if !slices.Equal(q.containers, want) {
		t.Errorf("containers = %+v, want %+v — the completed count travels with the total", q.containers, want)
	}
}

// The rollup's completed count is what tells a finished epic — every child
// closed, nothing left but for a human to close the container itself — from
// one still in progress. One predicate, finished(), is the only place that
// decides it.
func TestContainerInfoFinished(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		c    containerInfo
		want bool
	}{
		{"partly finished", containerInfo{number: 113, total: 6, completed: 3}, false},
		{"untouched", containerInfo{number: 147, total: 5, completed: 0}, false},
		{"finished", containerInfo{number: 113, total: 6, completed: 6}, true},
		// Cannot happen in a real payload — a container only exists because
		// total > 0 — but a defensive read must not call it finished.
		{"completed with no total", containerInfo{number: 1, total: 0, completed: 1}, false},
	}
	for _, tc := range cases {
		if got := tc.c.finished(); got != tc.want {
			t.Errorf("%s: finished() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// heldBackEqual compares two heldBackInfo slices field by field: the type
// carries a []int, so it is not comparable and slices.Equal cannot see inside
// it.
func heldBackEqual(a, b []heldBackInfo) bool {
	return slices.EqualFunc(a, b, func(x, y heldBackInfo) bool {
		return x.number == y.number && slices.Equal(x.blockers, y.blockers)
	})
}

// A ready issue with an open blockedBy dependency is put down for this pass
// rather than worked — the whole point of #179.
func TestSelectableIssuesHoldsBackAnOpenBlocker(t *testing.T) {
	t.Parallel()
	raw := []byte(`[
		{"number":4,"labels":[],"blockedBy":{"nodes":[{"number":100,"state":"OPEN"},{"number":101,"state":"CLOSED"}]}},
		{"number":5,"labels":[]}]`)

	q, err := selectableIssues(raw)
	if err != nil {
		t.Fatalf("selectableIssues: %v", err)
	}
	if want := []int{5}; !slices.Equal(q.ready, want) {
		t.Errorf("ready = %v, want %v — 4 has an open blocker", q.ready, want)
	}
	want := []heldBackInfo{{number: 4, blockers: []int{100}}}
	if !heldBackEqual(q.heldBack, want) {
		t.Errorf("heldBack = %+v, want %+v — only the still-open blocker is named, not the closed one",
			q.heldBack, want)
	}
}

// Nothing durable marks a held-back issue: the very next listing that finds
// the blocker closed hands it straight back to ready.
func TestSelectableIssuesUnblocksOnceTheBlockerCloses(t *testing.T) {
	t.Parallel()
	raw := []byte(`[{"number":4,"blockedBy":{"nodes":[{"number":100,"state":"CLOSED"}]}}]`)

	q, err := selectableIssues(raw)
	if err != nil {
		t.Fatalf("selectableIssues: %v", err)
	}
	if want := []int{4}; !slices.Equal(q.ready, want) {
		t.Errorf("ready = %v, want %v — a closed blocker must not hold it back", q.ready, want)
	}
	if len(q.heldBack) != 0 {
		t.Errorf("heldBack = %+v, want none", q.heldBack)
	}
}

// blockedBy's own state travels with the connection rather than with the
// top-level --label filter, so a blocker this call would never have listed on
// its own account — wrong label, or none at all — still blocks while it is
// open. The gate is whether the work landed, not whose business it was.
func TestSelectableIssuesBlockerOutsideLabelScopeStillBlocks(t *testing.T) {
	t.Parallel()
	raw := []byte(`[{"number":4,"blockedBy":{"nodes":[{"number":200,"state":"OPEN"}]}}]`)

	q, err := selectableIssues(raw)
	if err != nil {
		t.Fatalf("selectableIssues: %v", err)
	}
	if len(q.ready) != 0 {
		t.Errorf("ready = %v, want 4 held back even though 200 never appears as its own row", q.ready)
	}
	want := []heldBackInfo{{number: 4, blockers: []int{200}}}
	if !heldBackEqual(q.heldBack, want) {
		t.Errorf("heldBack = %+v, want %+v", q.heldBack, want)
	}
}

// A gh whose blockedBy support carries no state at all falls back to asking
// whether the blocker's number showed up anywhere in this same listing —
// numbers already in hand, no second request paid for the approximation.
func TestSelectableIssuesFallsBackToTheListingWhenStateIsAbsent(t *testing.T) {
	t.Parallel()
	// 9 is present in the payload as its own open row, so the fallback reads
	// it as open and 4 stays held back.
	raw := []byte(`[{"number":4,"blockedBy":{"nodes":[{"number":9}]}},{"number":9,"labels":[]}]`)
	q, err := selectableIssues(raw)
	if err != nil {
		t.Fatalf("selectableIssues: %v", err)
	}
	if want := []int{9}; !slices.Equal(q.ready, want) {
		t.Errorf("ready = %v, want %v — 4 is held back by 9, still present in this listing", q.ready, want)
	}
	want := []heldBackInfo{{number: 4, blockers: []int{9}}}
	if !heldBackEqual(q.heldBack, want) {
		t.Errorf("heldBack = %+v, want %+v", q.heldBack, want)
	}

	// 999 never shows up anywhere in the payload, so the fallback reads it as
	// closed and 4 is ordinary work.
	raw = []byte(`[{"number":4,"blockedBy":{"nodes":[{"number":999}]}}]`)
	q, err = selectableIssues(raw)
	if err != nil {
		t.Fatalf("selectableIssues: %v", err)
	}
	if want := []int{4}; !slices.Equal(q.ready, want) {
		t.Errorf("ready = %v, want %v — a blocker absent from the listing reads as closed", q.ready, want)
	}
	if len(q.heldBack) != 0 {
		t.Errorf("heldBack = %+v, want none", q.heldBack)
	}
}

// Two issues blocking each other resolve independently in the same pass: both
// are held back, both are named, and neither loops or hangs the rest of the
// queue.
func TestSelectableIssuesResolvesADependencyCycle(t *testing.T) {
	t.Parallel()
	raw := []byte(`[
		{"number":4,"blockedBy":{"nodes":[{"number":5,"state":"OPEN"}]}},
		{"number":5,"blockedBy":{"nodes":[{"number":4,"state":"OPEN"}]}},
		{"number":6,"labels":[]}]`)

	q, err := selectableIssues(raw)
	if err != nil {
		t.Fatalf("selectableIssues: %v", err)
	}
	if want := []int{6}; !slices.Equal(q.ready, want) {
		t.Errorf("ready = %v, want %v — the rest of the queue keeps working", q.ready, want)
	}
	want := []heldBackInfo{{number: 4, blockers: []int{5}}, {number: 5, blockers: []int{4}}}
	if !heldBackEqual(q.heldBack, want) {
		t.Errorf("heldBack = %+v, want %+v", q.heldBack, want)
	}
}

// The precedence decisions: a container, needs-human, proposed or
// awaiting-answer classification wins outright over an open blocker — each is
// either structural or a judgement already made, and only the awaiting-answer
// case is a close call. It stays above held-back on purpose: awaitAnswer polls
// that issue's thread for a reply whether or not some unrelated dependency has
// merged, and demoting it to held-back would silently stop that poll.
func TestSelectableIssuesLabelsAndContainersOutrankAnOpenBlocker(t *testing.T) {
	t.Parallel()
	blocker := `"blockedBy":{"nodes":[{"number":100,"state":"OPEN"}]}`
	raw := []byte(`[
		{"number":4,"labels":[{"name":"needs-human"}],` + blocker + `},
		{"number":5,"labels":[{"name":"proposed"}],` + blocker + `},
		{"number":6,"labels":[{"name":"awaiting-answer"}],` + blocker + `},
		{"number":7,"subIssuesSummary":{"total":2},` + blocker + `},
		{"number":8,"labels":[],` + blocker + `}]`)

	q, err := selectableIssues(raw)
	if err != nil {
		t.Fatalf("selectableIssues: %v", err)
	}
	if len(q.ready) != 0 {
		t.Errorf("ready = %v, want none — every non-held-back issue here is claimed by a higher case", q.ready)
	}
	if want := []int{4}; !slices.Equal(q.parked, want) {
		t.Errorf("parked = %v, want %v", q.parked, want)
	}
	if want := []int{5}; !slices.Equal(q.proposed, want) {
		t.Errorf("proposed = %v, want %v", q.proposed, want)
	}
	if want := []int{6}; !slices.Equal(q.blocked, want) {
		t.Errorf("blocked = %v, want %v — awaiting-answer outranks a blocker too", q.blocked, want)
	}
	if len(q.containers) != 1 || q.containers[0].number != 7 {
		t.Errorf("containers = %+v, want just #7", q.containers)
	}
	want := []heldBackInfo{{number: 8, blockers: []int{100}}}
	if !heldBackEqual(q.heldBack, want) {
		t.Errorf("heldBack = %+v, want %+v — only the plain ready candidate is held back", q.heldBack, want)
	}
}

func TestSelectableIssuesRejectsJunk(t *testing.T) {
	t.Parallel()
	if _, err := selectableIssues([]byte("not json")); err == nil {
		t.Fatal("a payload that is not an issue list must be an error, not an empty queue")
	}
}
