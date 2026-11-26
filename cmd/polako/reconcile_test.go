package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The fixtures in testdata/reconcile/ are one repo at two moments. The doc
// is at HEAD (2222222); vision-old.md is how it read at 1111111, when most
// issues were filed. Since then M3 was reworded and `explain` was inserted
// as M9, pushing the nightly workflow to M10 and pay parsing to M11.
// issues.json is before plan reconciled, issues-after.json after it applied
// every action and filed M9.

func readReconFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "reconcile", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func textsByID(doc planDoc) map[string]string {
	m := map[string]string{}
	for _, it := range doc.items {
		m[strings.ToUpper(it.id)] = it.text
	}
	return m
}

func reconFixture(t *testing.T, issuesFile string) reconInput {
	t.Helper()
	doc, err := parseDocItems("docs/VISION.md", string(readReconFixture(t, "vision.md")))
	if err != nil {
		t.Fatal(err)
	}
	old, err := parseDocItems("docs/VISION.md", string(readReconFixture(t, "vision-old.md")))
	if err != nil {
		t.Fatal(err)
	}
	in := reconInput{doc: doc, sha: "2222222", trustAll: true, branchPrefix: "issue-",
		oldTexts: map[string]map[string]string{"1111111": textsByID(old), "2222222": textsByID(doc)},
		stubs: []stubHit{
			{path: "cmd/app/main.go", line: 42, text: `return errors.New("explain: not implemented")`},
			{path: "cmd/app/report.go", line: 7, text: "// not implemented yet"},
			{path: "internal/util/x.go", line: 3, text: `panic("unimplemented")`},
		}}
	if err := json.Unmarshal(readReconFixture(t, issuesFile), &in.issues); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(readReconFixture(t, "prs.json"), &in.prs); err != nil {
		t.Fatal(err)
	}
	return in
}

// rowLine is a row as one comparable line: id, state, issues, PRs, actions.
func rowLine(r reconRow) string {
	return r.item.id + " " + reconStateCell(r) + "|" + reconIssueCell(r) + "|" + issueRefs(r.prs) + "|" + reconActionCell(r)
}

func rowByID(t *testing.T, res reconResult, id string) reconRow {
	t.Helper()
	for _, r := range res.rows {
		if r.item.id == id {
			return r
		}
	}
	t.Fatalf("no row %s", id)
	return reconRow{}
}

func TestReconcileClassifiesEveryItem(t *testing.T) {
	t.Parallel()
	res := reconcile(reconFixture(t, "issues.json"))
	want := []string{
		"M1 done|#3|#11|",                 // marked and merged: settled, nothing to do
		"M2 done|#4?|#12|mark, move",      // "Vision milestone 2" under this doc's footer
		"M3 queued, stale|#5||edit, move", // reworded since #5 was filed
		"M4 parked|#6||",                  // needs-human
		"M5 in progress|#7|#20|",          // an open PR on issue-7
		"M6 dropped|#8?||mark",            // closed as not planned: never re-filed
		"M7 done|#9?|#13|mark, move",      // one issue for two milestones...
		"M8 done, stub left|#9?|#13|mark", // ...in the first one's milestone; report.go's stub
		"M9 missing|||create",             // inserted since: the one item to file
		"M10 done|#10|#14|mark, move",     // marked M9 before the insert: renumbered, not missing
		"M11 queued|#17, #12||move",       // marked twice, an open issue each
		"M12 queued|#13?||mark, move",     // a hand-made issue, matched by title
	}
	var got []string
	for _, r := range res.rows {
		got = append(got, rowLine(r))
	}
	if !slices.Equal(got, want) {
		t.Errorf("rows:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}

	if m3 := rowByID(t, res, "M3"); m3.matches[0].was != "Widget importer." {
		t.Errorf("M3's old text = %q, want the text #5 was filed against", m3.matches[0].was)
	}
	if m10 := rowByID(t, res, "M10"); m10.matches[0].how != matchRenumbered || m10.matches[0].from != "M9" {
		t.Errorf("M10's match = %+v, want #10 renumbered from M9", m10.matches[0])
	}
	if m9 := rowByID(t, res, "M9"); len(m9.stubs) != 1 || m9.stubs[0].String() != "cmd/app/main.go:42" {
		t.Errorf("M9's stubs = %v, want the explain stub — its line names the item's code span", m9.stubs)
	}
	if got := rowByID(t, res, "M11").dupes; !slices.Equal(got, []int{12}) {
		t.Errorf("M11 dupes = %v, want [12]", got)
	}
	if got := rowByID(t, res, "M7").shares; !slices.Equal(got, []string{"M8"}) {
		t.Errorf("M7 shares = %v, want [M8]", got)
	}
	if got := rowByID(t, res, "M8").milestone; got != "" {
		t.Errorf("M8's milestone = %q — #9 sits in M7's, since one issue has one milestone", got)
	}
	if got := rowByID(t, res, "M2").milestone; got != "[VISION] M2: HTTP fetcher with retries" {
		t.Errorf("M2's milestone = %q", got)
	}
	if len(res.orphans) != 1 || res.orphans[0] != (orphanMarker{issue: 15, id: "M30"}) {
		t.Errorf("orphans = %+v, want #15's M30", res.orphans)
	}
	if len(res.loose) != 1 || res.loose[0].path != "internal/util/x.go" {
		t.Errorf("loose stubs = %v, want only the one no item names", res.loose)
	}
	if res.guesses() != 5 {
		t.Errorf("guesses = %d, want 5 (M2, M6, M7, M8, M12)", res.guesses())
	}
}

// The point of the whole thing: once plan has applied what it proposed and
// filed what was missing, the next run proposes nothing at all.
func TestReconcileAfterApplyingProposesNothing(t *testing.T) {
	t.Parallel()
	res := reconcile(reconFixture(t, "issues-after.json"))
	for _, r := range res.rows {
		if r.state == stateMissing || len(r.actions) > 0 || r.guessed() {
			t.Errorf("%s after applying: %s — want it settled with nothing left to do", r.item.id, rowLine(r))
		}
	}
}

// On a public repo a marker counts only on the operator's own issue or one
// carrying the gate label. Anybody else's is a guess, so a stranger can't
// mark an item done by typing an HTML comment.
func TestReconcileTrustsMarkersOnlyFromCollaboratorsOnAPublicRepo(t *testing.T) {
	t.Parallel()
	forged := reconIssue{Number: 16, Title: "fake", State: "OPEN", Body: "<!-- polako:item=VISION/M9 -->"}
	forged.Author.Login = "mallory"

	in := reconFixture(t, "issues.json")
	in.trustAll, in.viewer, in.gate = false, "op", "ready"
	in.issues = append(in.issues, forged)
	if got := rowLine(rowByID(t, reconcile(in), "M9")); got != "M9 queued|#16?||mark, move" {
		t.Errorf("a stranger's marker: %s, want a guess waiting on the operator", got)
	}
	if got := rowLine(rowByID(t, reconcile(in), "M1")); got != "M1 done|#3|#11|" {
		t.Errorf("the operator's own marker: %s, want it settled", got)
	}

	forged.Labels = []ghLabel{{Name: "ready"}}
	in.issues[len(in.issues)-1] = forged
	if got := rowLine(rowByID(t, reconcile(in), "M9")); got != "M9 queued|#16||move" {
		t.Errorf("a marker on a gate-labelled issue: %s, want it settled", got)
	}
}

// Every doc numbers from 1. Reconciling one never reads another's markers.
func TestReconcileScopesMarkersByDoc(t *testing.T) {
	t.Parallel()
	in := reconFixture(t, "issues.json")
	other, err := parseDocItems("docs/designs/other.md", "## Milestones\n\n1. Other thing.\n")
	if err != nil {
		t.Fatal(err)
	}
	in.doc, in.oldTexts = other, nil
	res := reconcile(in)
	if got := reconIssueCell(res.rows[0]); got != "#14" {
		t.Errorf("other/M1 matched %q, want only #14, which carries other's marker — not VISION's #3", got)
	}
}

func TestItemRefs(t *testing.T) {
	t.Parallel()
	cases := []struct {
		body string
		want []string
	}{
		{"Vision milestone 4. The first reason.", []string{"M4"}},
		{"Vision milestones 12 and 13.", []string{"M12", "M13"}},
		{"Vision milestone 2 and the size cap.", []string{"M2"}},
		{"Milestones 1–3, 6 and 21 from the vision.", []string{"M1", "M2", "M3", "M6", "M21"}},
		{"Ticket 2 of docs/plans/unpark.md, then ticket 4.", []string{"T2", "T4"}},
		{"milestones 1-100", []string{"M1", "M100"}}, // a range past the cap isn't expanded
		{"No mention here.", nil},
	}
	for _, tc := range cases {
		if got := itemRefs(tc.body); !slices.Equal(got, tc.want) {
			t.Errorf("itemRefs(%q) = %v, want %v", tc.body, got, tc.want)
		}
	}
}

func TestContainsTerm(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		s, term string
		want    bool
	}{
		{`errors.new("explain: not implemented")`, "explain", true},
		{"explainer is fine", "explain", false},
		{"run check-sources now", "check-sources", true},
		{".github/workflows/crawl.yml", "crawl.yml", true},
	} {
		if got := containsTerm(tc.s, tc.term); got != tc.want {
			t.Errorf("containsTerm(%q, %q) = %v, want %v", tc.s, tc.term, got, tc.want)
		}
	}
}

// gatherRecon end to end: the fake gh answers with the fixture listings, a
// real git repository holds the doc's two versions and a stub, and the
// rendered table says what reconcile found.
func TestStatusDesignReadsGitHubAndTheCheckout(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	gitAt(t, repo, "init", "-q")
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(repo, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("docs/VISION.md", string(readReconFixture(t, "vision-old.md")))
	gitAt(t, repo, "add", "-A")
	gitAt(t, repo, "commit", "-q", "-m", "vision")
	oldSHA := gitAt(t, repo, "rev-parse", "--short", "HEAD")
	write("docs/VISION.md", string(readReconFixture(t, "vision.md")))
	write("cmd/app/main.go", "package main\n\nfunc explain() error { return errors.New(\"explain: not implemented\") }\n")
	write("cmd/app/main_test.go", "package main\n\n// want \"explain: not implemented\"\n")
	gitAt(t, repo, "add", "-A")
	gitAt(t, repo, "commit", "-q", "-m", "insert explain")
	headSHA := gitAt(t, repo, "rev-parse", "--short", "HEAD")

	fixtures := t.TempDir()
	issues := strings.NewReplacer("1111111", oldSHA, "2222222", headSHA).Replace(string(readReconFixture(t, "issues.json")))
	if err := os.WriteFile(filepath.Join(fixtures, "issues.json"), []byte(issues), 0o644); err != nil {
		t.Fatal(err)
	}
	prs, _ := filepath.Abs(filepath.Join("testdata", "reconcile", "prs.json"))
	cfg, _ := statusConfigFor(t, &ghState{Canned: map[string]string{
		"issue list --state all": filepath.Join(fixtures, "issues.json"),
		"pr list --state all":    prs,
	}})
	cfg.dir = repo

	in, err := gatherRecon(context.Background(), cfg, "docs/VISION.md")
	if err != nil {
		t.Fatal(err)
	}
	if in.sha != headSHA || !in.trustAll {
		t.Errorf("sha = %q, trustAll = %v — want HEAD and a private repo's every marker", in.sha, in.trustAll)
	}
	if got := in.oldTexts[oldSHA]["M9"]; got != "Nightly workflow that commits data." {
		t.Errorf("M9 at the old commit = %q, want the nightly workflow it was before the insert", got)
	}
	if len(in.stubs) != 1 || in.stubs[0].String() != "cmd/app/main.go:3" {
		t.Errorf("stubs = %v, want main.go's alone — a test asserting a stub isn't one", in.stubs)
	}

	res := reconcile(in)
	var out strings.Builder
	renderRecon(&out, report{}, cfg.repo, res)
	printed := out.String()
	for _, want := range []string{
		"docs/VISION.md (VISION) @ " + headSHA + " — 12 items: 1 in progress, 1 parked, 3 queued, 5 done, 1 dropped, 1 missing",
		"M9   explain command",
		"stub cmd/app/main.go:3",
		"? is a guess, 5 in all",
		"markers for items the doc no longer has: #15 (M30)",
	} {
		if !strings.Contains(printed, want) {
			t.Errorf("table missing %q:\n%s", want, printed)
		}
	}
	for _, leak := range []string{"widget importer", "Hand-made", "A change."} {
		if strings.Contains(printed, leak) {
			t.Errorf("the table printed issue text %q — it prints numbers and the doc's own words only", leak)
		}
	}

	var doc reconDoc
	var js strings.Builder
	if err := renderReconJSON(&js, cfg.repo, res); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(js.String()), &doc); err != nil {
		t.Fatalf("-json isn't JSON: %v\n%s", err, js.String())
	}
	if len(doc.Items) != 12 || doc.Items[8].State != "missing" || doc.Items[9].Issues[0].Match != "renumbered" {
		t.Errorf("-json items = %+v", doc.Items)
	}
}

func TestStatusDesignRefusesARepoWithoutACheckout(t *testing.T) {
	t.Parallel()
	err := runStatus(context.Background(), []string{"-design", "docs/VISION.md", "-repo", "o/r"}, &strings.Builder{}, statusNow, report{})
	if err == nil || !strings.Contains(err.Error(), "pass -dir, not -repo") {
		t.Errorf("err = %v, want a refusal naming -dir", err)
	}
}
