package main

import (
	"slices"
	"strings"
)

// planFooterPrefix is the fixed leading phrase every issue `polako plan` files
// ends its body with. It is a contract, like issue-N branch naming: the skill
// writes it, parsePlanFooter reads it back, and repo_test.go asserts the two
// still agree on the wording.
const planFooterPrefix = "Proposed by polako plan from "

// planFooter is what a proposed issue's footer points at: the plan document the
// issue was filed from, and the short commit SHA the repository was at when
// `plan` filed it. sha is "" when the footer has been edited to drop it — the
// parse still succeeds as long as doc ends in .md; without a SHA to fall back
// on, that suffix is the only thing left telling doc apart from stray prose.
type planFooter struct {
	doc string
	sha string
}

// parsePlanFooter reads the footer out of an issue body. It reports false, with
// no error, for a body that carries no footer — a hand-filed issue is the
// common case, not a fault.
//
// Strict about the leading phrase and the ` @ ` between path and SHA; tolerant
// of everything after the SHA, which is an editable tail (the "edit freely;
// ..." sentence ships there and a curator is invited to rewrite it). So the
// parse survives an edited tail, a missing SHA, extra lines below the footer,
// and a footer that is not the last line — it holds the line only to the
// phrase and the path.
func parsePlanFooter(body string) (planFooter, bool) {
	line := lastFooterLine(body, planFooterPrefix)
	if line == "" {
		return planFooter{}, false
	}

	rest := line[len(planFooterPrefix):]

	// Drop the editable tail. The template joins it with a spaced em dash; a
	// plain hyphen is tolerated for a tail edited in by hand.
	for _, sep := range []string{" — ", " -- ", " - "} {
		if i := strings.Index(rest, sep); i >= 0 {
			rest = rest[:i]
			break
		}
	}

	doc, sha, _ := strings.Cut(rest, " @ ")
	// First field of each: a path carries no spaces, so this keeps the path and
	// drops any trailing words left when the tail separator was removed or when
	// there is no ` @ ` at all.
	doc = firstField(doc)
	sha = firstField(sha)
	if doc == "" {
		return planFooter{}, false
	}
	// A prose sentence that happens to start with the phrase ("... from an
	// earlier version of the plan...") still yields a non-empty first field.
	// Require it to actually look like a plan document: either a path ending
	// in .md, or a SHA that looks like one follows it.
	if !strings.HasSuffix(doc, ".md") && !isHexSHA(sha) {
		return planFooter{}, false
	}
	return planFooter{doc: doc, sha: sha}, true
}

// isHexSHA reports whether s looks like a git commit SHA (short or full):
// 4 to 40 lowercase hex digits.
func isHexSHA(s string) bool {
	if len(s) < 4 || len(s) > 40 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func firstField(s string) string {
	if f := strings.Fields(s); len(f) > 0 {
		return f[0]
	}
	return ""
}

// lastFooterLine finds the last line of body starting with prefix, tolerant
// of a leading quote marker or indent — shared by parsePlanFooter and
// parseParkFooter, both of which read the *last* matching line rather than
// the first: a body that quotes an earlier footer in its prose still ends
// with its own. "" when no line matches.
func lastFooterLine(body, prefix string) string {
	var line string
	for _, l := range strings.Split(body, "\n") {
		if t := strings.TrimLeft(l, "> \t"); strings.HasPrefix(t, prefix) {
			line = t
		}
	}
	return line
}

// parkFooterPrefix is the fixed leading phrase a permission park's comment
// ends with when it has -add-tools entries to name. A contract like
// planFooterPrefix: parkIssue writes it, parseParkFooter reads it back, and
// a test holds both to the same wording. Issue #432.
const parkFooterPrefix = "Refused: "

// parkFooter renders entries as the footer parkIssue appends to a permission
// park's comment, or "" for no entries — no footer at all rather than an
// empty one.
func parkFooter(entries []string) string {
	if len(entries) == 0 {
		return ""
	}
	return parkFooterPrefix + strings.Join(entries, ", ")
}

// parseParkFooter reads the entries back out of a park comment — the last
// matching line, tolerant of a leading quote or indent, the same rules
// parsePlanFooter follows and for the same reason: a body that quotes an
// earlier park's footer in its prose still ends with its own, and this
// comment's own is never anything but the last line polako itself wrote.
// False for a body with no such line, or one naming no entries at all.
func parseParkFooter(body string) ([]string, bool) {
	line := lastFooterLine(body, parkFooterPrefix)
	if line == "" {
		return nil, false
	}
	rest := strings.TrimSpace(line[len(parkFooterPrefix):])
	if rest == "" {
		return nil, false
	}
	fields := strings.Split(rest, ", ")
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

// parkCategoryFooterPrefix is the fixed leading phrase every park comment
// ends with, naming which park.go category produced it — nothing else on
// the thread says a budget park apart from a CI park. A contract like
// planFooterPrefix and parkFooterPrefix: parkIssue writes it,
// parseParkCategory reads it back, and a test holds both sides to the same
// wording. Issue #530.
const parkCategoryFooterPrefix = "Park: "

// parkCategoryFooter renders category as the footer parkIssue appends to
// every park comment, after the Refused: footer when there is one.
func parkCategoryFooter(category string) string {
	return parkCategoryFooterPrefix + category
}

// parseParkCategory reads the category back out of a park comment — the
// last matching line, the same rules parseParkFooter and parsePlanFooter
// follow. "" for a body with no such line, or one naming something
// parkReasonOrder (metrics.go) doesn't recognize as a category: a forged or
// hand-edited line is not trusted to reclassify the park.
func parseParkCategory(body string) string {
	line := lastFooterLine(body, parkCategoryFooterPrefix)
	if line == "" {
		return ""
	}
	category := strings.TrimSpace(line[len(parkCategoryFooterPrefix):])
	if !slices.Contains(parkReasonOrder, category) {
		return ""
	}
	return category
}

// milestoneLinePrefix is the fixed leading phrase plan-backlog's Phase 5
// report ends with, naming the batch in a few words. A contract like
// planFooterPrefix: the skill writes it, parseMilestoneLine reads it back
// out of the run's final text, and repo_test.go holds both to the same
// wording. Issue #674.
const milestoneLinePrefix = "Milestone: "

// parseMilestoneLine reads the title off the last `Milestone:` line of a
// run's final text, raw — the caller cleans and caps it. "" for text with no
// such line, or a blank one.
func parseMilestoneLine(text string) string {
	line := lastFooterLine(text, milestoneLinePrefix)
	if line == "" {
		return ""
	}
	return strings.TrimSpace(line[len(milestoneLinePrefix):])
}
