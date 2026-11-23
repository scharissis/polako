package main

// Plan-document items: the entries `polako plan` files one issue for, each
// with an ID stable enough to find its issue again on the next run, so a
// re-plan files only what's missing. See docs/designs/reconcile.md.
//
// Two shapes count, each scoped to the heading it sits under: a numbered
// entry under a heading that starts with "Milestones", and a `### N. title`
// heading under `## Drafted tickets` — the section every design-plan doc
// writes. Anything else a doc wants counted, it marks by hand. A doc with
// none of these has no items, and plan works it the way it did before items
// existed.
//
// The section headings are matched at the start, not anywhere: setup.md has
// "Experiments — rows, not tickets" and backlog-fill.md "Epics and
// milestones: both, at different altitudes", and a numbered list under
// either is not a backlog.
//
// An ID is the number the author wrote, never a list position. Markdown
// renders `1. 1. 1.` as 1, 2, 3, but only the source survives a re-plan, and
// an item added at the end must not shift anyone else's ID.

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// itemKind is the shape an item came from. It picks the ID's letter, and
// whether the item is milestone-sized: only those get a GitHub milestone of
// their own.
type itemKind int

const (
	itemMilestone itemKind = iota // M<n>: a numbered entry under a milestones heading
	itemTicket                    // T<n>: a numbered heading under a tickets heading
	itemMarked                    // an ID the doc gives by hand
)

// docItem is one item of a plan document.
type docItem struct {
	id   string // unique within its doc: "M23", "T3", or a hand-given ID
	kind itemKind
	name string // a few words: table rows and milestone titles
	text string // the entry as written, number and markers stripped
	line int    // 1-based line the entry starts on, for errors
}

// planDoc is a parsed plan document.
type planDoc struct {
	path  string    // as given, relative to the checkout
	slug  string    // scopes every ID: the file name, unless the doc pins one
	items []docItem // in document order
}

// fullID is an item's ID as an issue's marker carries it. Every doc numbers
// from 1, so the slug is what keeps one doc's M1 apart from another's.
func (d planDoc) fullID(it docItem) string { return d.slug + "/" + it.id }

var (
	// mdHeadingRe is an ATX heading, its indent already stripped. The space
	// after the hashes is required, so "#hashtag" is prose.
	mdHeadingRe = regexp.MustCompile(`^(#{1,6})(?:\s+(.*?))?(?:\s+#+)?\s*$`)
	// mdListStartRe opens any list entry, bulleted or numbered.
	mdListStartRe = regexp.MustCompile(`^\s*(?:[-*+]|\d{1,9}[.)])\s`)
	// numberedEntryRe is a numbered entry at the top of a list: up to three
	// spaces in, the number, then its text.
	numberedEntryRe = regexp.MustCompile(`^( {0,3})(\d{1,4})[.)]\s+(.*)$`)
	// numberedHeadingRe is a heading's text that opens with a number.
	numberedHeadingRe = regexp.MustCompile(`^(\d{1,4})[.)]\s+(.+)$`)
	// milestoneHeadingRe is a heading naming its own milestone number,
	// "Milestone 3: Filters", wherever it sits.
	milestoneHeadingRe  = regexp.MustCompile(`(?i)^milestone\s+(\d{1,4})\s*(?:[:.)]|[-–—])\s*(.+)$`)
	milestonesSectionRe = regexp.MustCompile(`(?i)^milestones?\b`)
	ticketsSectionRe    = regexp.MustCompile(`(?i)^(?:drafted\s+)?tickets\b`)
	// docItemMarkerRe is a hand-given ID on an entry's line.
	docItemMarkerRe = regexp.MustCompile(`<!--\s*polako:item=(\S*?)\s*-->`)
	// docSlugPinRe pins a doc's slug, on a line of its own.
	docSlugPinRe = regexp.MustCompile(`^<!--\s*polako:doc=(\S*?)\s*-->$`)
	itemIDRe     = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9._-]{0,31}$`)
	slugRe       = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	slugUnsafeRe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)
	codeSpanRe   = regexp.MustCompile("`[^`]*`")
	mdCommentRe  = regexp.MustCompile(`<!--.*?-->`)
	mdLinkRe     = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
)

// mdLine is one line of a markdown document, classified once so the item
// walk never has to work out again whether a line is code.
type mdLine struct {
	text   string
	fenced bool   // inside a code fence, or a fence line itself
	level  int    // ATX heading level; 0 for anything else
	head   string // the heading's text
}

// scanMarkdown splits content into classified lines. Fenced code is flagged
// rather than dropped, so indexes still match the file's line numbers.
func scanMarkdown(content string) []mdLine {
	raw := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	lines := make([]mdLine, len(raw))
	fence := ""
	for i, l := range raw {
		lines[i].text = l
		t := strings.TrimLeft(l, " ")
		indented := indentOf(l) > 3
		if fence != "" {
			lines[i].fenced = true
			if !indented && strings.HasPrefix(t, fence) && strings.TrimSpace(strings.TrimLeft(t, fence[:1])) == "" {
				fence = ""
			}
			continue
		}
		if !indented && (strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~")) {
			fence = t[:len(t)-len(strings.TrimLeft(t, t[:1]))]
			lines[i].fenced = true
			continue
		}
		if !indented {
			if m := mdHeadingRe.FindStringSubmatch(t); m != nil {
				lines[i].level = len(m[1])
				lines[i].head = m[2]
			}
		}
	}
	return lines
}

// indentOf is a line's leading whitespace in columns, a tab counting four.
func indentOf(s string) int {
	n := 0
	for _, r := range s {
		switch r {
		case ' ':
			n++
		case '\t':
			n += 4
		default:
			return n
		}
	}
	return n
}

// parseDocItems reads a plan document's items. A doc with none is no error:
// plan then works the way it did before items existed. Two items with one ID
// are, since either could be the one an issue's marker means.
func parseDocItems(path, content string) (planDoc, error) {
	lines := scanMarkdown(content)
	slug, err := docSlug(path, lines)
	if err != nil {
		return planDoc{}, err
	}
	doc := planDoc{path: path, slug: slug}

	sectionKind, sectionLevel := itemMilestone, 0 // sectionLevel 0: in no section
	inItem := 0                                   // level of the heading item whose body this is; 0 for none
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		if l.fenced {
			continue
		}
		if l.level > 0 {
			if inItem > 0 && l.level <= inItem {
				inItem = 0
			}
			if sectionLevel > 0 && l.level <= sectionLevel {
				sectionLevel = 0
			}
			if inItem > 0 {
				continue // a sub-heading of an item: part of its text
			}
			it, ok, err := headingItem(lines, i, sectionKind, sectionLevel)
			if err != nil {
				return planDoc{}, fmt.Errorf("%s:%d: %w", path, i+1, err)
			}
			if ok {
				doc.items = append(doc.items, it)
				inItem = l.level
				continue
			}
			switch head := plainItemText(l.head); {
			case milestonesSectionRe.MatchString(head):
				sectionKind, sectionLevel = itemMilestone, l.level
			case ticketsSectionRe.MatchString(head):
				sectionKind, sectionLevel = itemTicket, l.level
			}
			continue
		}
		if inItem > 0 {
			continue // a heading item's body is its text, not items of its own
		}

		if sectionLevel > 0 && sectionKind == itemMilestone {
			if m := numberedEntryRe.FindStringSubmatch(l.text); m != nil {
				end := entryEnd(lines, i, len(m[1]))
				n, _ := strconv.Atoi(m[2])
				items, err := milestoneEntries(lines, i, end, n, m[3])
				if err != nil {
					return planDoc{}, fmt.Errorf("%s:%d: %w", path, i+1, err)
				}
				doc.items = append(doc.items, items...)
				i = end - 1
				continue
			}
		}
		// A hand-given ID on any other line: an indented one is a code block,
		// and a code span only talks about a marker.
		if indentOf(l.text) < 4 && docItemMarkerRe.MatchString(codeSpanRe.ReplaceAllString(l.text, "")) {
			end := entryEnd(lines, i, indentOf(l.text))
			text, id, err := takeDocMarker(entryText(lines, i, end, stripListMarker(l.text)))
			if err != nil {
				return planDoc{}, fmt.Errorf("%s:%d: %w", path, i+1, err)
			}
			doc.items = append(doc.items, docItem{id: id, kind: itemMarked, name: itemName(text), text: text, line: i + 1})
			i = end - 1
		}
	}

	seen := make(map[string]int, len(doc.items))
	for _, it := range doc.items {
		key := strings.ToUpper(it.id)
		if first, dup := seen[key]; dup {
			return planDoc{}, fmt.Errorf("%s has two items with ID %s, at lines %d and %d — number them apart, "+
				"or give one its own ID with <!-- polako:item=ID -->", path, it.id, first, it.line)
		}
		seen[key] = it.line
	}
	return doc, nil
}

// headingItem reports whether the heading at lines[i] opens an item: a
// "Milestone N: name" heading anywhere, a numbered heading inside a
// milestones or tickets section, or any heading carrying a hand-given ID. Its
// text is the heading and everything under it, down to the next heading at
// its level or above.
func headingItem(lines []mdLine, i int, sectionKind itemKind, sectionLevel int) (docItem, bool, error) {
	l := lines[i]
	head, id, err := takeDocMarker(l.head)
	if err != nil {
		return docItem{}, false, err
	}
	head = strings.TrimSpace(head)
	it := docItem{line: i + 1}
	var title string
	if m := milestoneHeadingRe.FindStringSubmatch(head); m != nil {
		n, _ := strconv.Atoi(m[1])
		it.kind, it.id, title = itemMilestone, "M"+strconv.Itoa(n), m[2]
	} else if m := numberedHeadingRe.FindStringSubmatch(head); m != nil && sectionLevel > 0 && l.level > sectionLevel {
		n, _ := strconv.Atoi(m[1])
		it.kind, it.id, title = sectionKind, itemLetter(sectionKind)+strconv.Itoa(n), m[2]
	} else if id != "" {
		it.kind, title = itemMarked, head
	} else {
		return docItem{}, false, nil
	}
	if id != "" {
		it.id = id
	}

	end := i + 1
	for end < len(lines) && (lines[end].fenced || lines[end].level == 0 || lines[end].level > l.level) {
		end++
	}
	body, _, err := takeDocMarker(joinLines(lines[i+1 : end]))
	if err != nil {
		return docItem{}, false, err
	}
	it.text = strings.TrimSpace(title + "\n" + body)
	it.name = itemName(title)
	return it, true, nil
}

// itemLetter is the ID prefix an item of kind gets from its number.
func itemLetter(kind itemKind) string {
	if kind == itemTicket {
		return "T"
	}
	return "M"
}

// milestoneEntries turns one numbered entry into its items. Usually one;
// several when its first line carries a run of them, `8. Foo. 9. Bar.`, each
// number the previous plus one. Continuation lines belong to the last.
func milestoneEntries(lines []mdLine, start, end, n int, first string) ([]docItem, error) {
	pieces := splitNumberedRun(n, first)
	cont := joinLines(lines[start+1 : end])
	items := make([]docItem, 0, len(pieces))
	for k, p := range pieces {
		raw := p.text
		if k == len(pieces)-1 && strings.TrimSpace(cont) != "" {
			raw += "\n" + cont
		}
		text, id, err := takeDocMarker(raw)
		if err != nil {
			return nil, err
		}
		it := docItem{id: "M" + strconv.Itoa(p.n), kind: itemMilestone, name: itemName(text), text: text, line: start + 1}
		if id != "" {
			it.id = id
		}
		items = append(items, it)
	}
	return items, nil
}

// numberedPiece is one entry of a run that shares a line.
type numberedPiece struct {
	n    int
	text string
}

// splitNumberedRun splits "Foo. 9. Bar. 10. Baz." (n being 8) into 8, 9 and
// 10. A number only starts a new entry when it is exactly the next one and
// follows a sentence's end, so "retry 3 times. 4. Next" splits but "between
// 2 and 3. Then" and "v1. 7. x" don't.
func splitNumberedRun(n int, s string) []numberedPiece {
	var out []numberedPiece
	for {
		next := strconv.Itoa(n + 1)
		at := -1
		for from := 0; ; {
			k := strings.Index(s[from:], " "+next+". ")
			if k < 0 {
				break
			}
			k += from
			if before := strings.TrimRight(s[:k], " "); before != "" && strings.ContainsRune(".;!?)", rune(before[len(before)-1])) {
				at = k
				break
			}
			from = k + 1
		}
		if at < 0 {
			return append(out, numberedPiece{n, strings.TrimSpace(s)})
		}
		out = append(out, numberedPiece{n, strings.TrimSpace(s[:at])})
		s = s[at+len(next)+3:]
		n++
	}
}

// entryEnd is the index just past the entry — list entry or paragraph — that
// starts at lines[i], indent being the column its marker sits at. It runs to
// a heading, the next entry at its own depth or shallower, or a blank line
// the entry doesn't continue past: one followed by something indented deeper
// than the entry itself.
func entryEnd(lines []mdLine, i, indent int) int {
	j := i + 1
	for j < len(lines) {
		l := lines[j]
		if l.level > 0 {
			break
		}
		if l.fenced {
			if !lines[j-1].fenced && indentOf(l.text) <= indent {
				break // a fence at the entry's own depth ends it
			}
			j++
			continue
		}
		if strings.TrimSpace(l.text) == "" {
			k := j
			for k < len(lines) && !lines[k].fenced && lines[k].level == 0 && strings.TrimSpace(lines[k].text) == "" {
				k++
			}
			if k < len(lines) && lines[k].level == 0 && indentOf(lines[k].text) > indent {
				j = k
				continue
			}
			break
		}
		if indentOf(l.text) <= indent && mdListStartRe.MatchString(l.text) {
			break
		}
		j++
	}
	return j
}

// entryText is an entry's text: its first line as given, then every line
// up to end, trailing space trimmed.
func entryText(lines []mdLine, i, end int, first string) string {
	rest := joinLines(lines[i+1 : end])
	if strings.TrimSpace(rest) == "" {
		return strings.TrimSpace(first)
	}
	return strings.TrimSpace(first) + "\n" + rest
}

// joinLines is lines' text, one per line, trailing space trimmed and the
// whole thing trimmed of blank lines at either end.
func joinLines(lines []mdLine) string {
	parts := make([]string, len(lines))
	for i, l := range lines {
		parts[i] = strings.TrimRight(l.text, " \t")
	}
	return strings.Trim(strings.Join(parts, "\n"), "\n")
}

// stripListMarker drops a list entry's bullet or number from its first line.
func stripListMarker(s string) string {
	if loc := mdListStartRe.FindStringIndex(s); loc != nil {
		return s[loc[1]:]
	}
	return s
}

// takeDocMarker strips every hand-given ID out of s and returns the first.
// A doc's own marker carries no slug: the doc is its scope, and a slug
// written into one would be a second, disagreeing source for it. Markers in
// code spans are talk about markers, and stay put.
func takeDocMarker(s string) (string, string, error) {
	var id string
	var bad error
	spans := codeSpanRe.FindAllStringIndex(s, -1)
	inSpan := func(at int) bool {
		for _, sp := range spans {
			if at >= sp[0] && at < sp[1] {
				return true
			}
		}
		return false
	}
	var b strings.Builder
	last := 0
	for _, loc := range docItemMarkerRe.FindAllStringSubmatchIndex(s, -1) {
		if inSpan(loc[0]) {
			continue
		}
		got := s[loc[2]:loc[3]]
		switch {
		case strings.Contains(got, "/"):
			if bad == nil {
				bad = fmt.Errorf("item marker %q names a slug — a doc's own markers carry the ID alone; "+
					"pin the doc's slug with <!-- polako:doc=SLUG --> instead", got)
			}
		case !itemIDRe.MatchString(got):
			if bad == nil {
				bad = fmt.Errorf("%q can't be an item ID — start with a letter, then up to 31 letters, digits, dots, dashes or underscores", got)
			}
		case id == "":
			id = got
		}
		b.WriteString(s[last:loc[0]])
		last = loc[1]
	}
	b.WriteString(s[last:])
	if bad != nil {
		return "", "", bad
	}
	lines := strings.Split(b.String(), "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " \t")
	}
	return strings.TrimSpace(strings.Join(lines, "\n")), id, nil
}

// docSlug is the scope a doc's IDs live in: its pin if it has one, else its
// file name without the extension. The file name rather than the path, so a
// design moved to docs/designs/done/, or still named under docs/plans/ by an
// old footer, keeps every issue it ever filed.
func docSlug(path string, lines []mdLine) (string, error) {
	for i, l := range lines {
		if l.fenced || indentOf(l.text) > 3 {
			continue
		}
		if m := docSlugPinRe.FindStringSubmatch(strings.TrimSpace(l.text)); m != nil {
			if !slugRe.MatchString(m[1]) {
				return "", fmt.Errorf("%s:%d: %q can't be a slug — use up to 64 letters, digits, dots, dashes or underscores", path, i+1, m[1])
			}
			return m[1], nil
		}
	}
	if s := slugFromPath(path); s != "" {
		return s, nil
	}
	return "", fmt.Errorf("%s has no file name to scope its items with — pin a slug with <!-- polako:doc=SLUG -->", path)
}

// slugFromPath is a path's default slug: the file name without its
// extension, any run of characters a marker can't carry turned into a dash.
// Footers write forward slashes; a hand-typed path might not.
func slugFromPath(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		p = p[i+1:]
	}
	if i := strings.LastIndexByte(p, '.'); i > 0 {
		p = p[:i]
	}
	p = strings.Trim(slugUnsafeRe.ReplaceAllString(p, "-"), "-")
	if len(p) > 64 {
		p = strings.TrimRight(p[:64], "-")
	}
	return p
}

// itemNameMax caps an item's name. A name ends a milestone title and fills a
// table cell; past this it's a paragraph, and the cut lands on a word.
const itemNameMax = 80

// itemName is an item's name: the text before the first colon, when that
// reads like a label ("Scaffold: Go module, …"), or else its first sentence,
// capped at itemNameMax.
func itemName(text string) string {
	first, _, _ := strings.Cut(text, "\n")
	s := plainItemText(first)
	if i := strings.Index(s+" ", ": "); i > 0 && i <= 40 {
		s = s[:i]
	} else if j := sentenceEnd(s); j > 0 {
		s = s[:j]
	}
	return capTitle(strings.TrimSpace(s), itemNameMax)
}

// sentenceEnd is the index of the punctuation ending s's first sentence, or
// -1. Punctuation only ends one when a space or the end follows it, so
// `sources.yaml` and "v1.2" don't.
func sentenceEnd(s string) int {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '.', ';', '!', '?':
			if i+1 == len(s) || s[i+1] == ' ' {
				return i
			}
		}
	}
	return -1
}

// plainItemText is s as a reader sees it: no comments, links reduced to
// their text, no backticks, bold markers or double quotes, one space between
// words. Quotes go because a milestone title holding one can't be searched
// for (proposalsURL).
func plainItemText(s string) string {
	s = mdCommentRe.ReplaceAllString(s, "")
	s = mdLinkRe.ReplaceAllString(s, "$1")
	s = strings.NewReplacer("`", "", "**", "", "__", "", `"`, "").Replace(s)
	return strings.Join(strings.Fields(s), " ")
}

// itemKey is an item's text reduced to what a reader would call a change:
// case, markdown, spacing and a closing full stop don't count. Two versions
// of a doc agree on an item exactly when their keys do.
func itemKey(text string) string {
	return strings.TrimRight(strings.ToLower(plainItemText(text)), ".;:!? ")
}
