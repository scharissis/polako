package main

// `status` is a read-only derivation of what a drain sees, so it is held to two
// things: it says what the drain would do, and it does none of it. Both are
// tested against the same fake `gh` the drain loop runs on.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// statusNow is the clock every case here is written against, so a "quiet for"
// span is a literal rather than something that drifts with the calendar.
var statusNow = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

// statusConfigFor builds the config the readers take, pointed at a fake gh and
// at a pretend repository. ghRepo is set the way runStatus sets it, so every
// call goes through the --repo and {owner}/{repo} rewriting as well.
func statusConfigFor(t *testing.T, st *ghState) (config, string) {
	t.Helper()
	drainCfg, path := drainConfig(t, "stream", st)
	return config{
		dir:          drainCfg.dir,
		ghBin:        drainCfg.ghBin,
		repo:         drainCfg.repo,
		ghRepo:       drainCfg.repo,
		branchPrefix: "issue-",
		ghRetryWait:  time.Millisecond,
		queue:        new(queueMemo),
	}, path
}

// A whole backlog in one snapshot: what is workable, what is waiting on the
// operator, what is parked, which issue a drain would pick up, and what state
// the open PR is in.
func TestStatusReportsWhereTheBacklogStands(t *testing.T) {
	cfg, _ := statusConfigFor(t, &ghState{
		Issues: map[string]*fakeIssue{
			"3":  {Open: true},
			"5":  {Open: true},
			"7":  {Open: true, Labels: []string{awaitingAnswerLabel}, Comments: 2, CommentedAt: statusNow.Add(-26 * time.Hour).Format(time.RFC3339)},
			"9":  {Open: true, Labels: []string{needsHumanLabel}},
			"11": {Open: false},
		},
		PRs: map[string]*fakePR{
			"issue-3": {Number: 40, State: "OPEN", Mergeable: "MERGEABLE", Checks: []string{"SUCCESS"}},
			// On an issue branch, but for an issue nobody has open: finished
			// business, and not part of where the backlog stands.
			"issue-11": {Number: 41, State: "OPEN", Mergeable: "MERGEABLE"},
			// Not an issue branch at all — somebody's own work in the same repo.
			"hotfix": {Number: 42, State: "OPEN", Mergeable: "MERGEABLE"},
		},
	})

	snap, err := readStatus(context.Background(), cfg, statusNow)
	if err != nil {
		t.Fatalf("readStatus: %v", err)
	}
	if want := []int{3, 5}; !slices.Equal(snap.queues.ready, want) {
		t.Errorf("ready = %v, want %v", snap.queues.ready, want)
	}
	if want := []int{7}; !slices.Equal(snap.queues.blocked, want) {
		t.Errorf("blocked = %v, want %v", snap.queues.blocked, want)
	}
	if want := []int{9}; !slices.Equal(snap.queues.parked, want) {
		t.Errorf("parked = %v, want %v", snap.queues.parked, want)
	}
	if snap.next != 3 {
		t.Errorf("next = %d, want the lowest ready issue #3", snap.next)
	}
	if len(snap.prs) != 1 || snap.prs[0].number != 40 {
		t.Fatalf("prs = %+v, want only PR #40 on issue-3", snap.prs)
	}

	var out strings.Builder
	renderStatus(&out, report{}, cfg, snap)
	printed := out.String()
	for _, want := range []string{
		"example/repo",
		"ready         2 issues — #3, #5",
		"awaiting you  1 issue — #7 (quiet 26h)",
		"parked        1 issue — #9, labelled needs-human",
		"next          #3 — its branch already has PR #40",
		"#40  issue-3  #3     mergeable  passing  clear",
		"needs you: reply on #7; review and merge PR #40; " +
			"decide what to do about #9 (drop needs-human to requeue)",
	} {
		if !strings.Contains(printed, want) {
			t.Errorf("report is missing %q\ngot:\n%s", want, printed)
		}
	}
	// No issue, PR or comment text reaches the terminal: only numbers,
	// branches, labels and states.
	for _, unwanted := range []string{"#41", "#42", "hotfix", "issue-11"} {
		if strings.Contains(printed, unwanted) {
			t.Errorf("report names %q, which is not part of where the backlog stands\ngot:\n%s",
				unwanted, printed)
		}
	}

	// The whole report, byte for byte, with report{} — the zero value, and
	// what every pipe gets: no call site decided to colour anything, so
	// today's plain bytes must be untouched by the renderer that now sits in
	// front of them. Captured from this exact scenario, not re-derived from
	// the new renderer — the same rule stats.go's TestStatsReport already
	// follows for its own report.
	want := `example/repo
  ready         2 issues — #3, #5
  awaiting you  1 issue — #7 (quiet 26h)
  parked        1 issue — #9, labelled needs-human
  next          #3 — its branch already has PR #40, so it would wait on that rather than run the skill again

open prs on issue branches
  pr   branch   issue  mergeable  checks   review  url
  #40  issue-3  #3     mergeable  passing  clear   https://example.invalid/pr/40

needs you: reply on #7; review and merge PR #40; decide what to do about #9 (drop needs-human to requeue)
`
	if printed != want {
		t.Errorf("report differs\n--- got ---\n%s\n--- want ---\n%s", printed, want)
	}
}

// The same backlog as TestStatusReportsWhereTheBacklogStands, this time
// through `-json`. Same snapshot, same facts — the assertions below are the
// text test's own assertions translated field by field, so the two reports
// cannot silently disagree about the same backlog.
func TestStatusJSONMatchesTheTextReport(t *testing.T) {
	cfg, _ := statusConfigFor(t, &ghState{
		Issues: map[string]*fakeIssue{
			"3":  {Open: true},
			"5":  {Open: true},
			"7":  {Open: true, Labels: []string{awaitingAnswerLabel}, Comments: 2, CommentedAt: statusNow.Add(-26 * time.Hour).Format(time.RFC3339)},
			"9":  {Open: true, Labels: []string{needsHumanLabel}},
			"11": {Open: false},
		},
		PRs: map[string]*fakePR{
			"issue-3":  {Number: 40, State: "OPEN", Mergeable: "MERGEABLE", Checks: []string{"SUCCESS"}},
			"issue-11": {Number: 41, State: "OPEN", Mergeable: "MERGEABLE"},
			"hotfix":   {Number: 42, State: "OPEN", Mergeable: "MERGEABLE"},
		},
	})

	snap, err := readStatus(context.Background(), cfg, statusNow)
	if err != nil {
		t.Fatalf("readStatus: %v", err)
	}

	var out strings.Builder
	if err := renderStatusJSON(&out, cfg, snap); err != nil {
		t.Fatalf("renderStatusJSON: %v", err)
	}
	var doc statusDoc
	if err := json.Unmarshal([]byte(out.String()), &doc); err != nil {
		t.Fatalf("output did not parse as JSON: %v\n%s", err, out.String())
	}

	if doc.Repo != "example/repo" {
		t.Errorf("repo = %q, want %q", doc.Repo, "example/repo")
	}
	want := statusDocQueue{
		Ready:      []int{3, 5},
		Blocked:    []statusDocBlocked{{Issue: 7, QuietSeconds: ptrInt64(26 * 3600)}},
		Parked:     []int{9},
		Proposed:   []int{},
		Containers: []statusDocContainer{},
	}
	if !slices.Equal(doc.Queue.Ready, want.Ready) ||
		!slices.Equal(doc.Queue.Parked, want.Parked) ||
		!slices.Equal(doc.Queue.Proposed, want.Proposed) ||
		!slices.Equal(doc.Queue.Containers, want.Containers) ||
		!slices.EqualFunc(doc.Queue.Blocked, want.Blocked, func(a, b statusDocBlocked) bool {
			return a.Issue == b.Issue && a.QuietSeconds != nil && b.QuietSeconds != nil && *a.QuietSeconds == *b.QuietSeconds
		}) {
		t.Errorf("queue = %+v, want %+v", doc.Queue, want)
	}
	if doc.Next.Issue != 3 || !strings.Contains(doc.Next.Reason, "its branch already has PR #40") {
		t.Errorf("next = %+v, want issue 3 and the restart-safety reason", doc.Next)
	}
	if len(doc.PRs) != 1 {
		t.Fatalf("prs = %+v, want only PR #40 on issue-3", doc.PRs)
	}
	if pr := doc.PRs[0]; pr.Number != 40 || pr.Branch != "issue-3" || pr.Issue != 3 ||
		pr.Mergeable != "mergeable" || pr.Checks != "passing" || pr.Review != "clear" {
		t.Errorf("pr = %+v, want #40 on issue-3, mergeable/passing/clear", pr)
	}
	if want := "needs you: reply on #7; review and merge PR #40; " +
		"decide what to do about #9 (drop needs-human to requeue)"; "needs you: "+strings.Join(doc.NeedsYou, "; ") != want {
		t.Errorf("needs_you = %v, want it to join into %q", doc.NeedsYou, want)
	}
	// The two PRs on issue-11 and the hotfix branch never reach the report at
	// all — same exclusion the text renderer applies.
	for _, unwanted := range []int{41, 42} {
		for _, pr := range doc.PRs {
			if pr.Number == unwanted {
				t.Errorf("JSON named PR #%d, which is not part of where the backlog stands", unwanted)
			}
		}
	}
}

func ptrInt64(n int64) *int64 { return &n }

// An empty backlog still comes back with every array field as `[]`, never
// `null` — a script doing `.queue.ready[]` must not have to special-case the
// quiet case.
func TestStatusJSONKeepsArraysEmptyNotNull(t *testing.T) {
	cfg, _ := statusConfigFor(t, &ghState{Issues: map[string]*fakeIssue{"1": {Open: false}}})

	snap, err := readStatus(context.Background(), cfg, statusNow)
	if err != nil {
		t.Fatalf("readStatus: %v", err)
	}
	var out strings.Builder
	if err := renderStatusJSON(&out, cfg, snap); err != nil {
		t.Fatalf("renderStatusJSON: %v", err)
	}
	for _, unwanted := range []string{
		`"ready":null`, `"parked":null`, `"proposed":null`, `"containers":null`,
		`"blocked":null`, `"prs":null`, `"undetailed_prs":null`, `"needs_you":null`,
	} {
		if strings.Contains(out.String(), unwanted) {
			t.Errorf("output has %q, want an empty array instead\n%s", unwanted, out.String())
		}
	}
	for _, want := range []string{
		`"ready": []`, `"blocked": []`, `"prs": []`, `"needs_you": []`,
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output is missing %q\n%s", want, out.String())
		}
	}
}

// A thread whose newest comment could not be dated is a real state — the same
// one the text report shows without a "(quiet ...)" suffix — and it must not
// collapse into quiet_seconds: 0, which would claim the thread just spoke.
func TestStatusJSONLeavesQuietSecondsAbsentWhenUnreadable(t *testing.T) {
	cfg, _ := statusConfigFor(t, &ghState{
		Issues: map[string]*fakeIssue{
			"4": {Open: true, Labels: []string{awaitingAnswerLabel}, Comments: 1, CommentedAt: "not a date"},
		},
	})
	snap, err := readStatus(context.Background(), cfg, statusNow)
	if err != nil {
		t.Fatalf("readStatus: %v", err)
	}
	var out strings.Builder
	if err := renderStatusJSON(&out, cfg, snap); err != nil {
		t.Fatalf("renderStatusJSON: %v", err)
	}
	var doc statusDoc
	if err := json.Unmarshal([]byte(out.String()), &doc); err != nil {
		t.Fatalf("output did not parse as JSON: %v\n%s", err, out.String())
	}
	if len(doc.Queue.Blocked) != 1 || doc.Queue.Blocked[0].Issue != 4 {
		t.Fatalf("blocked = %+v, want issue 4", doc.Queue.Blocked)
	}
	if doc.Queue.Blocked[0].QuietSeconds != nil {
		t.Errorf("quiet_seconds = %v, want absent for an unreadable span", *doc.Queue.Blocked[0].QuietSeconds)
	}
	if strings.Contains(out.String(), "quiet_seconds") {
		t.Errorf("output names quiet_seconds for an issue whose span could not be read\n%s", out.String())
	}
}

// The same snapshot again, this time through a report whose styler is on —
// the palette applied where it matters (the repo line, the table title, the
// column headers, a parked row, a failing check, the needs-you line) and left
// alone everywhere else (a plain "mergeable" cell, "clear" review), with the
// columns still aligned once the ANSI is stripped back out.
func TestRenderStatusAppliesThePaletteOnAColourTTY(t *testing.T) {
	cfg, _ := statusConfigFor(t, &ghState{
		Issues: map[string]*fakeIssue{
			"3": {Open: true},
			"5": {Open: true},
			"7": {Open: true, Labels: []string{awaitingAnswerLabel}, Comments: 2, CommentedAt: statusNow.Add(-26 * time.Hour).Format(time.RFC3339)},
			"9": {Open: true, Labels: []string{needsHumanLabel}},
		},
		PRs: map[string]*fakePR{
			"issue-3": {Number: 40, State: "OPEN", Mergeable: "MERGEABLE", Checks: []string{"FAILURE"}},
		},
	})
	snap, err := readStatus(context.Background(), cfg, statusNow)
	if err != nil {
		t.Fatalf("readStatus: %v", err)
	}

	var out strings.Builder
	renderStatus(&out, report{style: styler{on: true}}, cfg, snap)
	printed := out.String()

	for _, want := range []string{
		"\x1b[1mexample/repo\x1b[0m\n",               // the repo line, bold
		"\x1b[1mopen prs on issue branches\x1b[0m\n", // the table title, bold
		"\x1b[2mmergeable\x1b[0m",                    // a column header, dim
		"\x1b[33mparked      \x1b[0m  1 issue",       // the parked row's label, yellow
		"\x1b[33mfailing (check-1)\x1b[0m",           // the failing check, yellow
		"\x1b[1mneeds you: reply on #7; " +
			"decide what to do about #9 (drop needs-human to requeue)\x1b[0m", // the closing line, bold
	} {
		if !strings.Contains(printed, want) {
			t.Errorf("coloured report is missing %q\ngot:\n%q", want, printed)
		}
	}
	for _, plain := range []string{"mergeable", "clear"} {
		if strings.Contains(printed, "\x1b[33m"+plain) {
			t.Errorf("%q has no attention marker and must not be highlighted\ngot:\n%q", plain, printed)
		}
	}

	// Strip every ANSI wrap and the plain report comes back byte for byte:
	// the palette is decoration painted over the same bytes, and the plain
	// renderer's own render of this scenario is the reference.
	var plain strings.Builder
	renderStatus(&plain, report{}, cfg, snap)
	if got, want := stripANSI(printed), plain.String(); got != want {
		t.Errorf("colour changed what the report says\n--- stripped ---\n%s\n--- plain ---\n%s", got, want)
	}
}

// stripANSI removes every "\x1b[...m" escape this package's styler ever
// writes, so a coloured report can be compared against its plain shape.
func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == '\x1b' {
			if j := strings.IndexByte(s[i:], 'm'); j >= 0 {
				i += j + 1
				continue
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// `status` answers "what would a drain work here?", so it has to exclude
// exactly what a drain excludes — which is why it derives the queue through the
// drain's own call rather than a second copy of it.
func TestStatusInheritsTheCurationGate(t *testing.T) {
	cfg, _ := statusConfigFor(t, &ghState{
		Issues: map[string]*fakeIssue{
			"1": {Open: true, Labels: []string{proposedLabel}},
			"2": {Open: true, SubIssues: 3, SubIssuesCompleted: 1},
			"3": {Open: true},
		},
		// #1 was worked before somebody labelled it: excluded from the queue is
		// not excluded from the report, because that PR still wants merging.
		PRs: map[string]*fakePR{
			"issue-1": {Number: 40, State: "OPEN", Mergeable: "MERGEABLE", Checks: []string{"SUCCESS"}},
		},
	})

	snap, err := readStatus(context.Background(), cfg, statusNow)
	if err != nil {
		t.Fatalf("readStatus: %v", err)
	}
	if want := []int{3}; !slices.Equal(snap.queues.ready, want) {
		t.Errorf("ready = %v, want %v", snap.queues.ready, want)
	}
	if want := []int{1}; !slices.Equal(snap.queues.proposed, want) {
		t.Errorf("proposed = %v, want %v", snap.queues.proposed, want)
	}
	if want := []containerInfo{{number: 2, total: 3, completed: 1}}; !slices.Equal(snap.queues.containers, want) {
		t.Errorf("containers = %v, want %v", snap.queues.containers, want)
	}
	if len(snap.queues.blocked)+len(snap.queues.parked) != 0 {
		t.Errorf("blocked/parked = %v/%v, want a container in neither",
			snap.queues.blocked, snap.queues.parked)
	}
	if snap.next != 3 {
		t.Errorf("next = %d, want the lowest issue a drain would actually pick up", snap.next)
	}

	var out strings.Builder
	renderStatus(&out, report{}, cfg, snap)
	printed := out.String()
	// What is held back is named, not merely absent: an operator reading a
	// snapshot that showed neither would have no way to find the batch at all.
	for _, want := range []string{
		"ready       1 issue — #3",
		"proposed    1 issue — #1, labelled proposed",
		"containers  1 issue — #2 (1/3 closed)",
		"#40  issue-1  #1     mergeable  passing  clear",
		"curate #1 (drop proposed to queue them)",
	} {
		if !strings.Contains(printed, want) {
			t.Errorf("report is missing %q\ngot:\n%s", want, printed)
		}
	}
}

// The containers row tells three states apart, in both the text report and the
// JSON document: a live epic still in progress, a finished one the next shift
// will close on its own, and a finished one a human has held open.
func TestStatusReportsFinishedAndLiveContainers(t *testing.T) {
	cfg, _ := statusConfigFor(t, &ghState{
		Issues: map[string]*fakeIssue{
			"113": {Open: true, SubIssues: 6, SubIssuesCompleted: 6},
			"147": {Open: true, SubIssues: 5, SubIssuesCompleted: 1},
			"150": {Open: true, SubIssues: 4, SubIssuesCompleted: 4, Labels: []string{needsHumanLabel}},
		},
	})

	snap, err := readStatus(context.Background(), cfg, statusNow)
	if err != nil {
		t.Fatalf("readStatus: %v", err)
	}
	want := []containerInfo{
		{number: 113, total: 6, completed: 6},
		{number: 147, total: 5, completed: 1},
		{number: 150, total: 4, completed: 4, held: true},
	}
	if !slices.Equal(snap.queues.containers, want) {
		t.Fatalf("containers = %+v, want %+v", snap.queues.containers, want)
	}

	var out strings.Builder
	renderStatus(&out, report{}, cfg, snap)
	printed := out.String()
	if want := "containers  3 issues — #113 (6/6 closed — the next shift closes it), " +
		"#147 (1/5 closed), #150 (4/4 closed — yours to close)"; !strings.Contains(printed, want) {
		t.Errorf("report is missing %q\ngot:\n%s", want, printed)
	}
	if want := "close #150 (every sub-issue closed; held open by needs-human or proposed)"; !strings.Contains(printed, want) {
		t.Errorf("needs-you line is missing %q\ngot:\n%s", want, printed)
	}

	var jsonOut strings.Builder
	if err := renderStatusJSON(&jsonOut, cfg, snap); err != nil {
		t.Fatalf("renderStatusJSON: %v", err)
	}
	var doc statusDoc
	if err := json.Unmarshal([]byte(jsonOut.String()), &doc); err != nil {
		t.Fatalf("output did not parse as JSON: %v\n%s", err, jsonOut.String())
	}
	wantDoc := []statusDocContainer{
		{Issue: 113, Total: 6, Completed: 6, Finished: true},
		{Issue: 147, Total: 5, Completed: 1, Finished: false},
		{Issue: 150, Total: 4, Completed: 4, Finished: true, Held: true},
	}
	if !slices.Equal(doc.Queue.Containers, wantDoc) {
		t.Errorf("queue.containers = %+v, want %+v", doc.Queue.Containers, wantDoc)
	}
}

// A backlog of nothing but proposals and containers is not a cleared one, and
// the two are released differently — so the report names which is holding it
// rather than sending an operator to drop a label nothing carries.
func TestStatusDoesNotCallAGatedBacklogCleared(t *testing.T) {
	cfg, _ := statusConfigFor(t, &ghState{
		Issues: map[string]*fakeIssue{
			"1": {Open: true, Labels: []string{proposedLabel}},
			"2": {Open: true, SubIssues: 3},
		},
	})

	snap, err := readStatus(context.Background(), cfg, statusNow)
	if err != nil {
		t.Fatalf("readStatus: %v", err)
	}
	var out strings.Builder
	renderStatus(&out, report{}, cfg, snap)
	printed := out.String()
	if strings.Contains(printed, "backlog cleared") {
		t.Errorf("two open issues is not a cleared backlog:\n%s", printed)
	}
	if want := "next        nothing — every open issue is awaiting curation or a tracking container"; !strings.Contains(printed, want) {
		t.Errorf("report is missing %q\ngot:\n%s", want, printed)
	}
}

// The one promise that makes it safe to run against a repository somebody else
// is draining: every call is a read, and nothing on GitHub moves.
func TestStatusMakesOnlyReadCalls(t *testing.T) {
	cfg, path := statusConfigFor(t, &ghState{
		Issues: map[string]*fakeIssue{
			"1": {Open: true},
			"2": {Open: true, Labels: []string{awaitingAnswerLabel}, Comments: 1},
			"3": {Open: true, Labels: []string{needsHumanLabel}},
		},
		PRs: map[string]*fakePR{"issue-1": {Number: 8, State: "OPEN", Mergeable: "MERGEABLE"}},
	})
	calls := filepath.Join(t.TempDir(), "gh-calls.log")
	t.Setenv(fakeGhLogEnv, calls)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading fake gh state: %v", err)
	}

	snap, err := readStatus(context.Background(), cfg, statusNow)
	if err != nil {
		t.Fatalf("readStatus: %v", err)
	}
	renderStatus(&strings.Builder{}, report{}, cfg, snap)

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading fake gh state: %v", err)
	}
	if string(after) != string(before) {
		t.Errorf("status changed the repository:\nbefore %s\nafter  %s", before, after)
	}

	// Stronger than the state comparison, which a refused write would also
	// pass: this is the list of subcommands that were reached for at all.
	reads := []string{"issue list", "pr list", "pr view", "repo view"}
	f, err := os.Open(calls)
	if err != nil {
		t.Fatalf("reading the gh call log: %v", err)
	}
	defer f.Close()
	seen := 0
	for sc := bufio.NewScanner(f); sc.Scan(); {
		line := sc.Text()
		seen++
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == "api" {
			if !strings.HasPrefix(fields[1], "repos/") || !strings.HasSuffix(fields[1], "/comments?per_page=100") {
				t.Errorf("status called `gh %s`, which is not the comments read", line)
			}
			continue
		}
		if len(fields) < 2 || !slices.Contains(reads, fields[0]+" "+fields[1]) {
			t.Errorf("status called `gh %s`, which is not one of the reads %v", line, reads)
		}
	}
	if seen == 0 {
		t.Fatal("no gh calls were logged — the fake is not being exercised")
	}
}

// With nothing ready, a drain runs the lowest issue waiting on an answer, to
// find out whether the reply is already on the thread. Saying "next: nothing"
// there would send an operator looking for a drain that had stopped.
func TestStatusNamesTheAnswerItWouldChase(t *testing.T) {
	cfg, _ := statusConfigFor(t, &ghState{
		Issues: map[string]*fakeIssue{
			"4": {Open: true, Labels: []string{awaitingAnswerLabel}, Comments: 1},
			"6": {Open: true, Labels: []string{needsHumanLabel}},
		},
	})

	snap, err := readStatus(context.Background(), cfg, statusNow)
	if err != nil {
		t.Fatalf("readStatus: %v", err)
	}
	if snap.next != 4 {
		t.Fatalf("next = %d, want #4", snap.next)
	}
	var out strings.Builder
	renderStatus(&out, report{}, cfg, snap)
	for _, want := range []string{
		"ready         no issue is workable right now",
		"next          #4 — nothing else is workable",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("report is missing %q\ngot:\n%s", want, out.String())
		}
	}
}

// -strict-order is the one flag that changes which issue is next without
// changing what is in the queues: a flagged issue keeps its place, and the
// ready issues behind it wait. A report that ignored it would name an issue
// the drain it describes is not going to touch.
func TestStatusHonoursStrictOrder(t *testing.T) {
	state := func() *ghState {
		return &ghState{Issues: map[string]*fakeIssue{
			"4": {Open: true, Labels: []string{awaitingAnswerLabel}, Comments: 1},
			"8": {Open: true},
		}}
	}
	cfg, _ := statusConfigFor(t, state())
	snap, err := readStatus(context.Background(), cfg, statusNow)
	if err != nil {
		t.Fatalf("readStatus: %v", err)
	}
	if snap.next != 8 {
		t.Errorf("next = %d, want #8: by default a drain works past the issue awaiting an answer", snap.next)
	}

	strict, _ := statusConfigFor(t, state())
	strict.strictOrder = true
	snap, err = readStatus(context.Background(), strict, statusNow)
	if err != nil {
		t.Fatalf("readStatus: %v", err)
	}
	if snap.next != 4 {
		t.Fatalf("next = %d, want #4: -strict-order waits in place on it", snap.next)
	}
	var out strings.Builder
	renderStatus(&out, report{}, strict, snap)
	for _, want := range []string{
		"example/repo — -strict-order", // said out loud: the flag can come from the environment
		"next          #4 — -strict-order holds the queue behind it",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("report is missing %q\ngot:\n%s", want, out.String())
		}
	}
}

// An empty backlog is a real answer, and the one an operator most wants said
// plainly rather than inferred from three absent sections.
func TestStatusSaysWhenTheBacklogIsDrained(t *testing.T) {
	cfg, _ := statusConfigFor(t, &ghState{Issues: map[string]*fakeIssue{"1": {Open: false}}})

	snap, err := readStatus(context.Background(), cfg, statusNow)
	if err != nil {
		t.Fatalf("readStatus: %v", err)
	}
	var out strings.Builder
	renderStatus(&out, report{}, cfg, snap)
	if want := "nothing open — a shift starting now would find the backlog cleared"; !strings.Contains(out.String(), want) {
		t.Errorf("report is missing %q\ngot:\n%s", want, out.String())
	}
	if strings.Contains(out.String(), "needs you") {
		t.Errorf("a drained backlog needs nobody:\n%s", out.String())
	}
}

// Past the cap the PRs are still reported, by number, and the report says so.
// A silently shorter table would read as a repository with fewer open PRs than
// it has.
func TestStatusSaysWhichPRsItLeftUndetailed(t *testing.T) {
	issues := map[string]*fakeIssue{}
	prs := map[string]*fakePR{}
	for n := 1; n <= statusPRs+2; n++ {
		issues[fmt.Sprint(n)] = &fakeIssue{Open: true}
		prs[fmt.Sprintf("issue-%d", n)] = &fakePR{Number: 100 + n, State: "OPEN", Mergeable: "MERGEABLE"}
	}
	cfg, _ := statusConfigFor(t, &ghState{Issues: issues, PRs: prs})

	snap, err := readStatus(context.Background(), cfg, statusNow)
	if err != nil {
		t.Fatalf("readStatus: %v", err)
	}
	if want := []int{100 + statusPRs + 1, 100 + statusPRs + 2}; !slices.Equal(snap.undetailed, want) {
		t.Errorf("undetailed = %v, want %v", snap.undetailed, want)
	}
	var out strings.Builder
	renderStatus(&out, report{}, cfg, snap)
	for _, want := range []string{
		fmt.Sprintf("(2 PRs past the first %d, listed without state: #%d, #%d)",
			statusPRs, 100+statusPRs+1, 100+statusPRs+2),
		unknownCell,
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("report is missing %q\ngot:\n%s", want, out.String())
		}
	}
}

// The same cap, this time through -json: undetailed_prs names the PRs past
// the eighth by number, and each of those PRs still gets a row in prs — with
// "not read" in the fields nobody queried, the same distinction unknownCell
// preserves in the text table.
func TestStatusJSONSaysWhichPRsItLeftUndetailed(t *testing.T) {
	issues := map[string]*fakeIssue{}
	prs := map[string]*fakePR{}
	for n := 1; n <= statusPRs+2; n++ {
		issues[fmt.Sprint(n)] = &fakeIssue{Open: true}
		prs[fmt.Sprintf("issue-%d", n)] = &fakePR{Number: 100 + n, State: "OPEN", Mergeable: "MERGEABLE"}
	}
	cfg, _ := statusConfigFor(t, &ghState{Issues: issues, PRs: prs})

	snap, err := readStatus(context.Background(), cfg, statusNow)
	if err != nil {
		t.Fatalf("readStatus: %v", err)
	}
	var out strings.Builder
	if err := renderStatusJSON(&out, cfg, snap); err != nil {
		t.Fatalf("renderStatusJSON: %v", err)
	}
	var doc statusDoc
	if err := json.Unmarshal([]byte(out.String()), &doc); err != nil {
		t.Fatalf("output did not parse as JSON: %v\n%s", err, out.String())
	}

	wantUndetailed := []int{100 + statusPRs + 1, 100 + statusPRs + 2}
	if !slices.Equal(doc.UndetailedPRs, wantUndetailed) {
		t.Errorf("undetailed_prs = %v, want %v", doc.UndetailedPRs, wantUndetailed)
	}
	if len(doc.PRs) != statusPRs+2 {
		t.Fatalf("prs = %d entries, want %d", len(doc.PRs), statusPRs+2)
	}
	for _, n := range wantUndetailed {
		i := slices.IndexFunc(doc.PRs, func(p statusDocPR) bool { return p.Number == n })
		if i < 0 {
			t.Fatalf("prs is missing #%d entirely", n)
		}
		if pr := doc.PRs[i]; pr.Mergeable != unknownCell || pr.Checks != unknownCell || pr.Review != unknownCell {
			t.Errorf("PR #%d = %+v, want mergeable/checks/review all %q", n, pr, unknownCell)
		}
	}
	// A PR inside the cap was queried and does not carry the "not read" cells.
	if i := slices.IndexFunc(doc.PRs, func(p statusDocPR) bool { return p.Number == 101 }); i < 0 ||
		doc.PRs[i].Mergeable == unknownCell {
		t.Errorf("PR #101 should have been detailed, got %+v", doc.PRs[i])
	}
}

// --- the pieces, on their own ---

// -repo is what lets the command run from a directory that is no checkout, and
// gh spells "this repository" two ways: a flag on every subcommand here, and
// nothing at all on `gh api`, whose path placeholders have to be filled in.
func TestGhArgsNamesTheRepository(t *testing.T) {
	list := []string{"issue", "list", "--state", "open"}
	if got := ghArgs("", list); !slices.Equal(got, list) {
		t.Errorf("with no repo, args = %v, want them untouched", got)
	}
	if got, want := ghArgs("o/n", list),
		[]string{"issue", "list", "--state", "open", "--repo", "o/n"}; !slices.Equal(got, want) {
		t.Errorf("args = %v, want %v", got, want)
	}
	if !slices.Equal(list, []string{"issue", "list", "--state", "open"}) {
		t.Errorf("ghArgs wrote through to its caller's slice: %v", list)
	}

	api := []string{"api", "repos/{owner}/{repo}/issues/9/comments?per_page=100", "--paginate"}
	want := []string{"api", "repos/o/n/issues/9/comments?per_page=100", "--paginate"}
	if got := ghArgs("o/n", api); !slices.Equal(got, want) {
		t.Errorf("api args = %v, want %v", got, want)
	}
}

// The other end of the naming contract: the supervisor finds a PR by the branch
// the skill named, and this finds the issue by the same rule.
func TestIssueForBranch(t *testing.T) {
	for _, tc := range []struct {
		branch, prefix string
		want           int
	}{
		{"issue-12", "issue-", 12},
		{"claude/issue-12", "issue-", 0}, // a prefix, not a substring
		{"issue-", "issue-", 0},
		{"issue-0", "issue-", 0},
		{"issue-12a", "issue-", 0},
		{"main", "issue-", 0},
		{"bd/7", "bd/", 7}, // -branch-prefix keeps working
		{"anything", "", 0},
	} {
		got, ok := issueForBranch(tc.branch, tc.prefix)
		if (tc.want != 0) != ok || got != tc.want {
			t.Errorf("issueForBranch(%q, %q) = %d, %v; want %d", tc.branch, tc.prefix, got, ok, tc.want)
		}
	}
}

func TestBranchPRsKeepsOnlyOpenIssuesInQueueOrder(t *testing.T) {
	raw := []byte(`[{"number":7,"headRefName":"issue-9","url":"u9"},
		{"number":5,"headRefName":"issue-2","url":"u2"},
		{"number":4,"headRefName":"issue-88","url":"u88"},
		{"number":3,"headRefName":"release","url":"ur"}]`)
	q := issueQueues{ready: []int{2}, blocked: []int{9}, parked: []int{4}}

	prs, err := branchPRs(raw, "issue-", q)
	if err != nil {
		t.Fatalf("branchPRs: %v", err)
	}
	// #88 has no open issue and `release` is nobody's issue branch; the rest
	// come back lowest issue first, the order the queue is read in.
	if len(prs) != 2 || prs[0].issue != 2 || prs[1].issue != 9 {
		t.Fatalf("prs = %+v, want the PRs for issues 2 then 9", prs)
	}
	if prs[0].number != 5 || prs[0].branch != "issue-2" || prs[0].url != "u2" {
		t.Errorf("first PR = %+v, want #5 on issue-2", prs[0])
	}
}

func TestQuietForReadsTheNewestComment(t *testing.T) {
	stamp := func(d time.Duration) issueComment {
		var c issueComment
		c.CreatedAt = statusNow.Add(d).Format(time.RFC3339)
		return c
	}
	if _, ok := quietFor(nil, statusNow); ok {
		t.Error("an empty thread has no span to report")
	}
	if _, ok := quietFor([]issueComment{{CreatedAt: "not a date"}}, statusNow); ok {
		t.Error("a date that will not parse must leave the span absent, not zero")
	}
	got, ok := quietFor([]issueComment{stamp(-72 * time.Hour), stamp(-3 * time.Hour)}, statusNow)
	if !ok || got != 3*time.Hour {
		t.Errorf("quietFor = %v, %v; want 3h from the newest comment", got, ok)
	}
	// Clocks disagree; a comment from the future is not a negative age.
	if got, ok := quietFor([]issueComment{stamp(time.Hour)}, statusNow); !ok || got != 0 {
		t.Errorf("quietFor = %v, %v; want a fresh thread", got, ok)
	}
}

// Every review state a PR can be in gets its own words, including the one
// prView.reviewNote deliberately leaves blank.
func TestReviewCell(t *testing.T) {
	earlier := statusNow.Add(-time.Hour)
	for _, tc := range []struct {
		name string
		pr   statusPR
		want string
	}{
		{"unread", statusPR{}, unknownCell},
		{"clear", statusPR{detailed: true}, "clear"},
		{"outstanding", statusPR{detailed: true, view: prView{
			changesRequested: true, reviewedAt: statusNow, branchAt: earlier}}, "changes requested"},
		{"undated", statusPR{detailed: true, view: prView{changesRequested: true}}, "changes requested"},
		{"answered", statusPR{detailed: true, view: prView{
			changesRequested: true, reviewedAt: earlier, branchAt: statusNow}}, "answered, awaiting re-review"},
	} {
		if got := reviewCell(tc.pr); got != tc.want {
			t.Errorf("%s: reviewCell = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// A red build is a fact about named checks; "failing" alone sends an operator
// to the PR to find out which.
func TestChecksCellNamesWhatFailed(t *testing.T) {
	pr := statusPR{detailed: true, view: prView{checks: checksFailing, failing: []string{"build", "lint"}}}
	if want := "failing (build, lint)"; checksCell(pr) != want {
		t.Errorf("checksCell = %q, want %q", checksCell(pr), want)
	}
	if got := checksCell(statusPR{detailed: true, view: prView{checks: checksPassing}}); got != checksPassing {
		t.Errorf("checksCell = %q, want %q", got, checksPassing)
	}
}

// A PR a drain would remediate itself is not yours yet, and one whose checks
// are still running is nobody's.
func TestNeedsYouOnlyNamesWhatAPersonMustMove(t *testing.T) {
	detailed := func(n int, v prView) statusPR { return statusPR{number: n, detailed: true, view: v} }
	snap := statusSnapshot{prs: []statusPR{
		detailed(1, prView{mergeable: "MERGEABLE", checks: checksPassing}),
		detailed(2, prView{mergeable: "CONFLICTING", checks: checksPassing}),
		detailed(3, prView{mergeable: "MERGEABLE", checks: checksFailing, failing: []string{"build"}}),
		detailed(4, prView{mergeable: "MERGEABLE", checks: checksPending}),
		detailed(5, prView{mergeable: "MERGEABLE", checks: checksHuman}),
	}}
	got := needsYou(snap)
	if want := "needs you: review and merge PR #1; approve the checks waiting on you on PR #5"; got != want {
		t.Errorf("needsYou = %q, want %q", got, want)
	}
	if needsYou(statusSnapshot{}) != "" {
		t.Errorf("needsYou = %q on an idle backlog, want nothing", needsYou(statusSnapshot{}))
	}
}

// Restart safety outranks the awaiting-answer wording, because processIssue
// puts it first: a flagged issue whose branch already carries a PR is
// supervised, never re-run. Saying otherwise sends an operator looking for a
// Claude run that will not happen.
func TestStatusNextRespectsRestartSafetyOnAFlaggedIssue(t *testing.T) {
	cfg, _ := statusConfigFor(t, &ghState{
		Issues: map[string]*fakeIssue{
			"4": {Open: true, Labels: []string{awaitingAnswerLabel}, Comments: 1},
		},
		PRs: map[string]*fakePR{"issue-4": {Number: 20, State: "OPEN", Mergeable: "MERGEABLE"}},
	})

	snap, err := readStatus(context.Background(), cfg, statusNow)
	if err != nil {
		t.Fatalf("readStatus: %v", err)
	}
	if snap.next != 4 {
		t.Fatalf("next = %d, want #4", snap.next)
	}
	if got, want := nextLine(snap), "#4 — its branch already has PR #20"; !strings.Contains(got, want) {
		t.Errorf("nextLine = %q, want it to start %q", got, want)
	}
}

func TestRunStatusRejectsAnArgument(t *testing.T) {
	err := runStatus(context.Background(), []string{"12"}, &strings.Builder{}, statusNow, report{})
	if err == nil || !strings.Contains(err.Error(), "status takes flags only") {
		t.Errorf("err = %v, want a complaint about the argument", err)
	}
}
