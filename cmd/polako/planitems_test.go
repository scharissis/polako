package main

import (
	"strconv"
	"strings"
	"testing"
)

// visionDoc is a vision shaped like the ones plan meets in the wild: a
// milestones list with a run of entries on one line, a numbered list under
// another heading, and a fence that only looks like entries.
const visionDoc = `# widget: stage 1

## Goals

1. Read every board in full.
2. Never fail the whole run.

## Milestones (each one a PR)

1. Scaffold: Go module, CLI skeleton, config loading and validation for ` + "`sources.yaml`" + `, README.
2. HTTP fetcher with timeouts, retries, backoff, per-host limits and the size cap.
3. ` + "`Posting`" + ` model, ` + "`Source`" + ` interface and adapter registry.
   - one registry per binary
   - adapters register themselves
4. Foo adapter. 5. Bar adapter. 6. Baz adapter.
7. ` + "`export`" + ` command, run against the seed file, with bad slugs fixed or removed.

` + "```" + `
8. not a milestone, just a code sample
` + "```" + `

## Open questions (mine to decide)

1. Daily, or twice daily.
`

func TestParseDocItemsMilestonesList(t *testing.T) {
	t.Parallel()
	doc, err := parseDocItems("docs/VISION.md", visionDoc)
	if err != nil {
		t.Fatal(err)
	}
	if doc.slug != "VISION" {
		t.Errorf("slug = %q, want VISION", doc.slug)
	}
	want := []struct{ id, name string }{
		{"M1", "Scaffold"},
		{"M2", "HTTP fetcher with timeouts, retries, backoff, per-host limits and the size cap"},
		{"M3", "Posting model, Source interface and adapter registry"},
		{"M4", "Foo adapter"},
		{"M5", "Bar adapter"},
		{"M6", "Baz adapter"},
		{"M7", "export command, run against the seed file, with bad slugs fixed or removed"},
	}
	if len(doc.items) != len(want) {
		t.Fatalf("got %d items %+v, want %d — the goals, the open questions and the fenced sample are not milestones",
			len(doc.items), doc.items, len(want))
	}
	for i, w := range want {
		it := doc.items[i]
		if it.id != w.id || it.name != w.name || it.kind != itemMilestone {
			t.Errorf("item %d = %s %q (kind %d), want %s %q (milestone)", i, it.id, it.name, it.kind, w.id, w.name)
		}
	}
	if got := doc.fullID(doc.items[0]); got != "VISION/M1" {
		t.Errorf("fullID = %q, want VISION/M1", got)
	}
	// Continuation lines belong to their entry, so a change to a sub-bullet
	// is a change to the item.
	if m3 := doc.items[2].text; !strings.Contains(m3, "adapters register themselves") {
		t.Errorf("M3's text lost its sub-bullets: %q", m3)
	}
	// Each entry of a run is its own, its siblings' text not included.
	if m5 := doc.items[4].text; m5 != "Bar adapter." {
		t.Errorf("M5's text = %q, want just its own entry", m5)
	}
	if doc.items[3].line != doc.items[5].line {
		t.Errorf("a run shares its line: M4 at %d, M6 at %d", doc.items[3].line, doc.items[5].line)
	}
}

// ticketDoc is a design-plan doc: tickets are headings, their bodies are
// their text, and a later heading that merely mentions tickets isn't a
// tickets section.
const ticketDoc = `# Unpark for every park

## Drafted tickets

Ordered by dependency.

### 1. The park comment names its category

**Problem.** Nothing on the thread says which park it was.

1. a numbered list in a body is the body's

Estimate: S

### 2. ` + "`unpark`" + ` shows where the work stands

**Problem.** Clearing one took five lookups.

Depends on 1.

## Considered and not proposed

### 3. A local database

## Experiments — rows, not tickets

### 4. Not a ticket either
`

func TestParseDocItemsDraftedTickets(t *testing.T) {
	t.Parallel()
	doc, err := parseDocItems("docs/designs/unpark.md", ticketDoc)
	if err != nil {
		t.Fatal(err)
	}
	if doc.slug != "unpark" {
		t.Errorf("slug = %q, want unpark", doc.slug)
	}
	if len(doc.items) != 2 {
		t.Fatalf("got %d items %+v, want T1 and T2 only", len(doc.items), doc.items)
	}
	t1, t2 := doc.items[0], doc.items[1]
	if t1.id != "T1" || t1.kind != itemTicket || t1.name != "The park comment names its category" {
		t.Errorf("T1 = %+v", t1)
	}
	if !strings.Contains(t1.text, "Estimate: S") || !strings.Contains(t1.text, "a numbered list in a body") {
		t.Errorf("T1's text is its heading and its whole body: %q", t1.text)
	}
	if t2.id != "T2" || t2.name != "unpark shows where the work stands" {
		t.Errorf("T2 = %+v", t2)
	}
	if strings.Contains(t2.text, "local database") {
		t.Errorf("T2's text ran past the next section: %q", t2.text)
	}
}

func TestParseDocItemsMilestoneHeadingsAndMarkers(t *testing.T) {
	t.Parallel()
	doc, err := parseDocItems("docs/roadmap.md", `# Roadmap

## Milestone 3: Filters

Rules with reason codes.

## Later

A paragraph that opts in. <!-- polako:item=Search -->

- a bullet too <!-- polako:item=X-1 -->

Talk about `+"`<!-- polako:item=Nope -->`"+` in a code span doesn't count.

    <!-- polako:item=Indented -->

## Milestones

01. Padded number. <!-- polako:item=M9 -->
`)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, it := range doc.items {
		got = append(got, it.id+"="+it.name)
	}
	want := []string{"M3=Filters", "Search=A paragraph that opts in", "X-1=a bullet too", "M9=Padded number"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("items = %q, want %q", got, want)
	}
	if doc.items[0].text != "Filters\nRules with reason codes." {
		t.Errorf("a heading item's text is its title and body: %q", doc.items[0].text)
	}
	if strings.Contains(doc.items[1].text, "polako:item") {
		t.Errorf("the marker stays out of the item's text: %q", doc.items[1].text)
	}
}

func TestParseDocItemsRefusesAmbiguity(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, doc, want string }{
		{"a number used twice", "## Milestones\n\n1. One.\n2. Two.\n2. Two again.\n", "two items with ID M2, at lines 4 and 5"},
		{"lazy numbering", "## Milestones\n\n1. One.\n1. Two.\n", "two items with ID M1"},
		{"a hand-given ID that clashes", "## Milestones\n\n1. One.\n\nElse. <!-- polako:item=m1 -->\n", "two items with ID"},
		{"a slug in a doc's own marker", "Text <!-- polako:item=VISION/M1 -->\n", "names a slug"},
		{"an ID that can't be one", "Text <!-- polako:item=1st -->\n", "can't be an item ID"},
		{"a pin that can't be a slug", "<!-- polako:doc=a/b -->\n", "can't be a slug"},
	}
	for _, tc := range cases {
		_, err := parseDocItems("docs/VISION.md", tc.doc)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want one containing %q", tc.name, err, tc.want)
		}
	}
}

func TestParseDocItemsWithoutItems(t *testing.T) {
	t.Parallel()
	// The shape of polako's own VISION.md: headings, prose, bullets, no
	// milestones list. No items, and no error — plan works it as before.
	doc, err := parseDocItems("docs/VISION.md", "# Where it's going\n\n## What stays true\n\n- one\n- two\n\n## Next\n\n1. Maybe this.\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.items) != 0 {
		t.Errorf("items = %+v, want none", doc.items)
	}
}

func TestDocSlug(t *testing.T) {
	t.Parallel()
	cases := []struct{ path, doc, want string }{
		{"docs/VISION.md", "", "VISION"},
		{"docs/designs/db-migration.md", "", "db-migration"},
		{"docs/designs/done/db-migration.md", "", "db-migration"},
		{"docs/plans/unpark.md", "", "unpark"},
		{`docs\designs\Road Map (v2).md`, "", "Road-Map-v2"},
		{"docs/designs/renamed.md", "# x\n\n<!-- polako:doc=db-migration -->\n", "db-migration"},
		// A pin quoted in a fence is talk, not a pin.
		{"docs/x.md", "```\n<!-- polako:doc=other -->\n```\n", "x"},
	}
	for _, tc := range cases {
		doc, err := parseDocItems(tc.path, tc.doc)
		if err != nil {
			t.Errorf("%s: %v", tc.path, err)
			continue
		}
		if doc.slug != tc.want {
			t.Errorf("slug(%s) = %q, want %q", tc.path, doc.slug, tc.want)
		}
	}
}

func TestSplitNumberedRun(t *testing.T) {
	t.Parallel()
	cases := []struct {
		n    int
		in   string
		want []string
	}{
		{8, "Foo adapter. 9. Bar adapter. 10. Baz adapter.", []string{"8 Foo adapter.", "9 Bar adapter.", "10 Baz adapter."}},
		{2, "Retry 3 times. 4. Next", []string{"2 Retry 3 times. 4. Next"}}, // 4 isn't the next number
		{2, "Between 2 and 3. Then more", []string{"2 Between 2 and 3. Then more"}},
		{6, "Ship v1 7. later", []string{"6 Ship v1 7. later"}}, // no sentence ended before 7
		{1, "Scaffold: module, CLI.", []string{"1 Scaffold: module, CLI."}},
	}
	for _, tc := range cases {
		var got []string
		for _, p := range splitNumberedRun(tc.n, tc.in) {
			got = append(got, strconv.Itoa(p.n)+" "+p.text)
		}
		if strings.Join(got, "|") != strings.Join(tc.want, "|") {
			t.Errorf("splitNumberedRun(%d, %q) = %q, want %q", tc.n, tc.in, got, tc.want)
		}
	}
}

func TestItemName(t *testing.T) {
	t.Parallel()
	cases := []struct{ in, want string }{
		{"Scaffold: Go module, CLI skeleton.", "Scaffold"},
		{"Fetch from https://example.com with retries.", "Fetch from https://example.com with retries"},
		{"Load `sources.yaml` and validate it. Then more.", "Load sources.yaml and validate it"},
		{`A "quoted" name`, "A quoted name"},
		{"[Linked](https://x.y) thing.", "Linked thing"},
		{"A label far too long to be a label before its colon arrives: rest", "A label far too long to be a label before its colon arrives: rest"},
		{strings.Repeat("word ", 30), strings.TrimSpace(strings.Repeat("word ", 16))},
	}
	for _, tc := range cases {
		if got := itemName(tc.in); got != tc.want {
			t.Errorf("itemName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestItemKeyIgnoresFormatting(t *testing.T) {
	t.Parallel()
	same := []string{
		"`Posting` model and **adapter** registry.",
		"Posting model   and adapter\nregistry",
		"posting model and adapter registry",
	}
	for _, s := range same[1:] {
		if itemKey(s) != itemKey(same[0]) {
			t.Errorf("itemKey(%q) = %q, want it equal to %q", s, itemKey(s), itemKey(same[0]))
		}
	}
	if itemKey("Posting model and adapter registry") == itemKey("Posting model and source registry") {
		t.Error("a changed word must change the key")
	}
}
