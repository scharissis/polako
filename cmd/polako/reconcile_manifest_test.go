package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// The items file carries the doc's words, numbers and paths, and the marker
// each new issue must copy — never issue text, and never a guess as though it
// were settled.
func TestItemsManifest(t *testing.T) {
	t.Parallel()
	res := reconcile(reconFixture(t, "issues.json"))
	var create []reconRow
	for _, r := range res.rows {
		if r.state == stateMissing {
			create = append(create, r)
		}
	}
	got := itemsManifest(res, create)
	for _, want := range []string{
		"From docs/VISION.md (slug VISION) at 2222222.",
		"## VISION/M9: explain command",
		"    <!-- polako:item=VISION/M9 -->",
		"Stubs in the code that name it: cmd/app/main.go:42",
		"> `explain` command.",
		"- VISION/M1: #3 (done)",
		"- VISION/M10: #10 (done)", // renumbered, but settled
		"## Stubs no item claims\n\ninternal/util/x.go:3",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("items file missing %q:\n%s", want, got)
		}
	}
	for _, leak := range []string{"scaffold the module", "A change.", "VISION/M2: #4"} {
		if strings.Contains(got, leak) {
			t.Errorf("items file holds %q — issue text, or a guess nobody confirmed:\n%s", leak, got)
		}
	}
	// Indented, so the file itself never reads as an issue claiming M9 if
	// someone pastes it into one.
	if m := parseItemMarkers(got); len(m) != 0 {
		t.Errorf("the items file's own marker lines parse as placed markers: %+v", m)
	}
}

// The plan-reconcile eval hands the skill an items file written by hand in
// seed.sh. This holds it to what itemsManifest renders for the same repo, so
// the eval grades the skill on the file the binary really writes.
func TestEvalItemsFileIsWhatTheBinaryWrites(t *testing.T) {
	t.Parallel()
	vision := readRepoFile(t, "evals", "plan-reconcile", "VISION.md")
	doc, err := parseDocItems("docs/VISION.md", vision)
	if err != nil {
		t.Fatal(err)
	}
	in := reconInput{doc: doc, sha: "SHA", trustAll: true, branchPrefix: "issue-"}
	for _, f := range []string{"issues.json", "issues-closed.json"} {
		var issues []reconIssue
		if err := json.Unmarshal([]byte(readRepoFile(t, "evals", "plan-reconcile", f)), &issues); err != nil {
			t.Fatal(err)
		}
		in.issues = append(in.issues, issues...)
	}
	res := reconcile(in)
	var create []reconRow
	for _, r := range res.rows {
		if r.state == stateMissing {
			create = append(create, r)
		}
	}
	seed := readRepoFile(t, "evals", "plan-reconcile", "seed.sh")
	start := strings.Index(seed, "<<ITEMS\n")
	end := strings.Index(seed, "\nITEMS\n")
	if start < 0 || end < start {
		t.Fatal("seed.sh no longer writes the items file between <<ITEMS and ITEMS")
	}
	written := strings.NewReplacer("$sha", "SHA", "\\`", "`").Replace(seed[start+len("<<ITEMS\n") : end+1])
	if want := itemsManifest(res, create); written != want {
		t.Errorf("seed.sh's items file drifted from itemsManifest.\nseed.sh:\n%s\nbinary:\n%s", written, want)
	}
}
