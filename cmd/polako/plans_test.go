package main

// The docs/designs/ plans section (#324), split out of status_test.go once
// that file crossed the accretion check's own file-length bound — verbatim
// movement, issue #510.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// planFooterFor builds the exact wording `polako plan` stamps — the same
// template footer_test.go proves parsePlanFooter reads back.
func planFooterFor(doc, sha string) string {
	return fmt.Sprintf(planFooterPrefix+"%s @ %s — edit freely; remove the `proposed` label to queue it.", doc, sha)
}

// writeDesignDoc creates an empty docs/designs/<name> file in dir — the local
// half of the derivation, localPlanDocs (plans.go).
func writeDesignDoc(t *testing.T, dir, name string) {
	t.Helper()
	designsDir := filepath.Join(dir, "docs", "designs")
	if err := os.MkdirAll(designsDir, 0o755); err != nil {
		t.Fatalf("mkdir docs/designs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(designsDir, name), []byte("# "+name+"\n"), 0o644); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
}

// The whole state table docs/designs/plan-conventions.md describes, one doc per
// case, plus a container and its open-children count.
func TestPlanDocsDerivesState(t *testing.T) {
	t.Parallel()
	cfg, _ := statusConfigFor(t, &ghState{
		Issues: map[string]*fakeIssue{
			// backlog-fill.md: one issue open past the gate makes it active,
			// whatever else names it.
			"1": {Open: true, Labels: []string{proposedLabel}, Body: planFooterFor("docs/designs/backlog-fill.md", "1a2b3c4")},
			"2": {Open: true, Body: planFooterFor("docs/designs/backlog-fill.md", "1a2b3c4")},
			// in-flight.md: every naming issue is either closed early or
			// still gated — proposed, neither active nor done.
			"3": {Open: false, Labels: []string{proposedLabel}, Body: planFooterFor("docs/designs/in-flight.md", "abc1234")},
			"4": {Open: true, Labels: []string{proposedLabel}, Body: planFooterFor("docs/designs/in-flight.md", "abc1234")},
			// shipped.md: every naming issue closed.
			"5": {Open: false, Body: planFooterFor("docs/designs/shipped.md", "def5678")},
			// epic.md: a container, open past the gate, with children left.
			"6": {Open: true, Body: planFooterFor("docs/designs/epic.md", "9999999"), SubIssues: 5, SubIssuesCompleted: 2},
		},
	})
	for _, name := range []string{"backlog-fill.md", "in-flight.md", "shipped.md", "epic.md", "empty.md"} {
		writeDesignDoc(t, cfg.dir, name)
	}

	snap, err := readPlanDocs(context.Background(), cfg)
	if err != nil {
		t.Fatalf("readPlanDocs: %v", err)
	}
	if snap.truncated {
		t.Error("truncated = true, want false — well under the limit")
	}
	got := map[string]planDocStatus{}
	for _, d := range snap.docs {
		got[d.path] = d
	}
	if len(got) != 5 {
		t.Fatalf("docs = %d, want 5: %+v", len(got), got)
	}
	for path, want := range map[string]planDocState{
		"docs/designs/backlog-fill.md": planActive,
		"docs/designs/in-flight.md":    planProposed,
		"docs/designs/shipped.md":      planDone,
		"docs/designs/epic.md":         planActive,
		"docs/designs/empty.md":        planDraft,
	} {
		if got[path].state != want {
			t.Errorf("%s state = %s, want %s", path, got[path].state, want)
		}
	}
	epic := got["docs/designs/epic.md"]
	if len(epic.containers) != 1 || epic.containers[0].number != 6 {
		t.Fatalf("epic.md containers = %+v, want just #6", epic.containers)
	}
	if epic.openChildren != 3 {
		t.Errorf("epic.md openChildren = %d, want 3 (5 total, 2 completed)", epic.openChildren)
	}
	if len(got["docs/designs/backlog-fill.md"].containers) != 0 {
		t.Errorf("backlog-fill.md names no sub-issues, want no containers, got %+v",
			got["docs/designs/backlog-fill.md"].containers)
	}
}

// A footer naming a document that no longer exists prints under gone, with
// the issue that names it — a deleted plan's leftovers, kept visible.
func TestPlanDocsGoneWhenFileMissing(t *testing.T) {
	t.Parallel()
	cfg, _ := statusConfigFor(t, &ghState{
		Issues: map[string]*fakeIssue{
			"7": {Open: true, Labels: []string{proposedLabel}, Body: planFooterFor("docs/designs/deleted.md", "0000000")},
		},
	})
	writeDesignDoc(t, cfg.dir, "kept.md")

	snap, err := readPlanDocs(context.Background(), cfg)
	if err != nil {
		t.Fatalf("readPlanDocs: %v", err)
	}
	if len(snap.docs) != 1 || snap.docs[0].path != "docs/designs/kept.md" || snap.docs[0].state != planDraft {
		t.Fatalf("docs = %+v, want just kept.md as draft", snap.docs)
	}
	if len(snap.gone) != 1 || snap.gone[0].path != "docs/designs/deleted.md" || !slices.Equal(snap.gone[0].issues, []int{7}) {
		t.Fatalf("gone = %+v, want deleted.md naming #7", snap.gone)
	}
}

// issue #554: a footer filed before the docs/plans → docs/designs rename
// still says docs/plans/<x>.md, and footers are never edited after the
// fact — so the derivation has to resolve that alias itself, against
// today's docs/designs/<x>.md.
func TestPlanDocsResolvesLegacyPlansFooterAgainstDesigns(t *testing.T) {
	t.Parallel()
	cfg, _ := statusConfigFor(t, &ghState{
		Issues: map[string]*fakeIssue{
			"1": {Open: true, Labels: []string{proposedLabel}, Body: planFooterFor("docs/plans/backlog-fill.md", "1a2b3c4")},
		},
	})
	writeDesignDoc(t, cfg.dir, "backlog-fill.md")

	snap, err := readPlanDocs(context.Background(), cfg)
	if err != nil {
		t.Fatalf("readPlanDocs: %v", err)
	}
	if len(snap.gone) != 0 {
		t.Errorf("gone = %+v, want none — the legacy footer resolves to a file that exists", snap.gone)
	}
	if len(snap.docs) != 1 || snap.docs[0].path != "docs/designs/backlog-fill.md" || snap.docs[0].state != planProposed {
		t.Fatalf("docs = %+v, want backlog-fill.md as proposed, grouped under its docs/designs/ path", snap.docs)
	}
}

// The done/ move itself is a separate, not-yet-built feature (see the issue
// body's "Out of scope"), but the alias already has to look there: a legacy
// footer whose document was archived under docs/designs/done/ must not be
// reported gone just because it is absent from the top-level directory.
func TestPlanDocsResolvesLegacyPlansFooterAgainstDesignsDone(t *testing.T) {
	t.Parallel()
	cfg, _ := statusConfigFor(t, &ghState{
		Issues: map[string]*fakeIssue{
			"1": {Open: false, Body: planFooterFor("docs/plans/shipped.md", "1a2b3c4")},
		},
	})
	// No file at the top level of docs/designs/ — writeDesignDoc always
	// writes there — so the archived copy is written by hand, under done/.
	doneDir := filepath.Join(cfg.dir, "docs", "designs", "done")
	if err := os.MkdirAll(doneDir, 0o755); err != nil {
		t.Fatalf("mkdir docs/designs/done: %v", err)
	}
	if err := os.WriteFile(filepath.Join(doneDir, "shipped.md"), []byte("# shipped.md\n"), 0o644); err != nil {
		t.Fatalf("writing shipped.md: %v", err)
	}

	snap, err := readPlanDocs(context.Background(), cfg)
	if err != nil {
		t.Fatalf("readPlanDocs: %v", err)
	}
	if len(snap.gone) != 0 {
		t.Errorf("gone = %+v, want none — the document was archived under docs/designs/done/, not deleted", snap.gone)
	}
}

// With no local docs/designs directory to check existence against — a -repo
// run reporting on a repository the checkout does not hold, most likely —
// every footer-named doc is reported plainly, never guessed to be gone.
func TestPlanDocsWithNoLocalCheckoutReportsNothingRatherThanGuessing(t *testing.T) {
	t.Parallel()
	cfg, _ := statusConfigFor(t, &ghState{
		Issues: map[string]*fakeIssue{
			"1": {Open: true, Body: planFooterFor("docs/designs/somewhere.md", "1111111")},
		},
	})

	snap, err := readPlanDocs(context.Background(), cfg)
	if err != nil {
		t.Fatalf("readPlanDocs: %v", err)
	}
	if len(snap.gone) != 0 {
		t.Errorf("gone = %+v, want none — a missing local directory must not be read as every footer being gone", snap.gone)
	}
	if len(snap.docs) != 1 || snap.docs[0].path != "docs/designs/somewhere.md" {
		t.Fatalf("docs = %+v, want the one issue's doc reported", snap.docs)
	}
}

// The search call is bounded, and a repo whose stamped issues outgrow it
// gets a warning naming the bound rather than a silently incomplete state.
func TestPlanDocsWarnsWhenTheSearchIsTruncated(t *testing.T) {
	t.Parallel()
	// One past the bound: readPlanDocs asks for planDocsLimit+1 rows
	// precisely so this case — more than the bound — is distinguishable
	// from landing exactly on it (see the next test).
	issues := make(map[string]*fakeIssue, planDocsLimit+1)
	for i := 1; i <= planDocsLimit+1; i++ {
		issues[strconv.Itoa(i)] = &fakeIssue{Open: true, Body: planFooterFor("docs/designs/flood.md", "abc0000")}
	}
	cfg, _ := statusConfigFor(t, &ghState{Issues: issues})
	writeDesignDoc(t, cfg.dir, "flood.md")

	snap, err := readPlanDocs(context.Background(), cfg)
	if err != nil {
		t.Fatalf("readPlanDocs: %v", err)
	}
	if !snap.truncated {
		t.Error("truncated = false, want true when more than the limit carries the footer")
	}

	var out strings.Builder
	printPlanDocs(&out, report{}, snap)
	if !strings.Contains(out.String(), fmt.Sprintf("past the first %d", planDocsLimit)) {
		t.Errorf("report missing the truncation warning:\n%s", out.String())
	}
}

// Landing exactly on the bound is a complete result, not a truncated one —
// the search asks for one row past planDocsLimit precisely so this case
// reads back false rather than a false positive.
func TestPlanDocsNotTruncatedExactlyAtTheLimit(t *testing.T) {
	t.Parallel()
	issues := make(map[string]*fakeIssue, planDocsLimit)
	for i := 1; i <= planDocsLimit; i++ {
		issues[strconv.Itoa(i)] = &fakeIssue{Open: true, Body: planFooterFor("docs/designs/flood.md", "abc0000")}
	}
	cfg, _ := statusConfigFor(t, &ghState{Issues: issues})
	writeDesignDoc(t, cfg.dir, "flood.md")

	snap, err := readPlanDocs(context.Background(), cfg)
	if err != nil {
		t.Fatalf("readPlanDocs: %v", err)
	}
	if snap.truncated {
		t.Error("truncated = true, want false — exactly planDocsLimit issues is a complete result")
	}
}

// Both renderers carry the plans section, derived once and rendered twice —
// the same rule statusDocFrom's own comment states for everything else in
// the snapshot.
func TestRenderStatusAndJSONIncludePlanDocuments(t *testing.T) {
	t.Parallel()
	cfg, _ := statusConfigFor(t, &ghState{
		Issues: map[string]*fakeIssue{
			"1": {Open: true, Body: planFooterFor("docs/designs/backlog-fill.md", "1a2b3c4")},
		},
	})
	writeDesignDoc(t, cfg.dir, "backlog-fill.md")

	snap, err := readStatus(context.Background(), cfg, statusNow)
	if err != nil {
		t.Fatalf("readStatus: %v", err)
	}

	var out strings.Builder
	renderStatus(&out, report{}, cfg, snap)
	for _, want := range []string{"plan documents", "docs/designs/backlog-fill.md", "active"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("report missing %q:\n%s", want, out.String())
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
	if len(doc.Plans.Docs) != 1 || doc.Plans.Docs[0].Path != "docs/designs/backlog-fill.md" || doc.Plans.Docs[0].State != "active" {
		t.Errorf("doc.Plans.Docs = %+v, want backlog-fill.md active", doc.Plans.Docs)
	}
}

// issue #510: a closed container is history, not a thing left for anyone to
// close — containerRefs has to say so plainly rather than promising "the
// next shift closes it" for work already done.
func TestContainerRefsRendersClosedContainer(t *testing.T) {
	t.Parallel()
	got := containerRefs([]containerInfo{{number: 321, total: 3, completed: 3, closed: true}})
	if want := "#321 (closed)"; got != want {
		t.Errorf("containerRefs = %q, want %q", got, want)
	}
	// A held-open, unclosed container is unaffected: closed defaults false,
	// so the queue's own containers row (which only ever sees open issues)
	// renders exactly as it did before this field existed.
	got = containerRefs([]containerInfo{{number: 12, total: 5, completed: 5}})
	if want := "#12 (5/5 closed — the next shift closes it)"; got != want {
		t.Errorf("containerRefs = %q, want %q", got, want)
	}
}

// issue #510: rows sort active, proposed, draft, done, then by path within a
// state — not the alphabetical-by-path order the local file listing hands
// back on its own.
func TestSortPlanDocsOrdersByStateThenPath(t *testing.T) {
	t.Parallel()
	docs := []planDocStatus{
		{path: "docs/designs/z-done.md", state: planDone},
		{path: "docs/designs/a-draft.md", state: planDraft},
		{path: "docs/designs/b-active.md", state: planActive},
		{path: "docs/designs/a-active.md", state: planActive},
		{path: "docs/designs/proposed.md", state: planProposed},
	}
	sortPlanDocs(docs)
	var got []string
	for _, d := range docs {
		got = append(got, d.path)
	}
	want := []string{
		"docs/designs/a-active.md",
		"docs/designs/b-active.md",
		"docs/designs/proposed.md",
		"docs/designs/a-draft.md",
		"docs/designs/z-done.md",
	}
	if !slices.Equal(got, want) {
		t.Errorf("sortPlanDocs order = %v, want %v", got, want)
	}
}

// issue #510: a done plan document is supposed to leave (move what's still
// true into docs/, delete the file) — the container route to a retire issue
// only fires when an epic closes, so a document whose naming issues were all
// plain gets no automatic nudge. needsYouParts is that nudge.
func TestNeedsYouNamesADoneDocumentStillOnDisk(t *testing.T) {
	t.Parallel()
	snap := statusSnapshot{plans: planDocsSnapshot{docs: []planDocStatus{
		{path: "docs/designs/shipped.md", state: planDone},
		{path: "docs/designs/backlog-fill.md", state: planActive},
	}}}
	got := needsYou(snap)
	if want := "needs you: retire docs/designs/shipped.md (done — move what's still true into docs/, delete the file)"; got != want {
		t.Errorf("needsYou = %q, want %q", got, want)
	}
}

// issue #510's own acceptance scenario: a fake repo with one closed
// container, one gone document whose every naming issue is closed, and one
// gone document with an open issue. The text report renders the closed
// container as "(closed)", names only the second gone document (by its open
// issue), and folds the first into the collapsed count — never listing a
// document with nothing left to act on by name.
func TestPlanDocsClosedContainerAndGoneCollapse(t *testing.T) {
	t.Parallel()
	cfg, _ := statusConfigFor(t, &ghState{
		Issues: map[string]*fakeIssue{
			// epic.md: its one naming issue is closed, with every child closed
			// too — done, and its container renders as history, not a promise.
			"1": {Open: false, Body: planFooterFor("docs/designs/epic.md", "aaa0000"), SubIssues: 3, SubIssuesCompleted: 3},
			// gone-all-closed.md: deleted from disk, every naming issue closed —
			// nothing left for anyone to act on, so it collapses into the count.
			"2": {Open: false, Body: planFooterFor("docs/designs/gone-all-closed.md", "bbb0000")},
			// gone-with-open.md: deleted from disk, but still named by an open
			// issue — named individually so the operator knows which one.
			"3": {Open: true, Body: planFooterFor("docs/designs/gone-with-open.md", "ccc0000")},
		},
	})
	writeDesignDoc(t, cfg.dir, "epic.md")

	snap, err := readPlanDocs(context.Background(), cfg)
	if err != nil {
		t.Fatalf("readPlanDocs: %v", err)
	}
	if len(snap.docs) != 1 || snap.docs[0].path != "docs/designs/epic.md" || snap.docs[0].state != planDone {
		t.Fatalf("docs = %+v, want just epic.md as done", snap.docs)
	}
	if len(snap.gone) != 2 {
		t.Fatalf("gone = %+v, want two deleted documents", snap.gone)
	}

	var out strings.Builder
	printPlanDocs(&out, report{}, snap)
	printed := out.String()
	if !strings.Contains(printed, "#1 (closed)") {
		t.Errorf("report missing the closed container as #1 (closed):\n%s", printed)
	}
	if strings.Contains(printed, "gone-all-closed.md") {
		t.Errorf("report names gone-all-closed.md by path — it should collapse into the count instead:\n%s", printed)
	}
	if !strings.Contains(printed, "docs/designs/gone-with-open.md (#3)") {
		t.Errorf("report missing gone-with-open.md named by its open issue #3:\n%s", printed)
	}
	if !strings.Contains(printed, "(1 deleted plan, every issue closed)") {
		t.Errorf("report missing the collapsed count for the all-closed document:\n%s", printed)
	}

	var jsonOut strings.Builder
	if err := renderStatusJSON(&jsonOut, cfg, statusSnapshot{plans: snap}); err != nil {
		t.Fatalf("renderStatusJSON: %v", err)
	}
	var doc statusDoc
	if err := json.Unmarshal([]byte(jsonOut.String()), &doc); err != nil {
		t.Fatalf("output did not parse as JSON: %v\n%s", err, jsonOut.String())
	}
	byPath := map[string]statusDocGone{}
	for _, g := range doc.Plans.Gone {
		byPath[g.Path] = g
	}
	if g := byPath["docs/designs/gone-all-closed.md"]; !slices.Equal(g.Issues, []int{2}) || g.Open != 0 {
		t.Errorf("gone-all-closed.md JSON = %+v, want issues [2], open 0", g)
	}
	if g := byPath["docs/designs/gone-with-open.md"]; !slices.Equal(g.Issues, []int{3}) || g.Open != 1 {
		t.Errorf("gone-with-open.md JSON = %+v, want issues [3], open 1", g)
	}
	if len(doc.Plans.Docs) != 1 || len(doc.Plans.Docs[0].Containers) != 1 || !doc.Plans.Docs[0].Containers[0].Closed {
		t.Errorf("epic.md JSON containers = %+v, want #1 marked closed", doc.Plans.Docs)
	}
}
