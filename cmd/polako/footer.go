package main

import "strings"

// planFooterPrefix is the fixed leading phrase every issue `polako plan` files
// ends its body with. It is a contract, like issue-N branch naming: the skill
// writes it, parsePlanFooter reads it back, and repo_test.go asserts the two
// still agree on the wording.
const planFooterPrefix = "Proposed by polako plan from "

// planFooter is what a proposed issue's footer points at: the plan document the
// issue was filed from, and the short commit SHA the repository was at when
// `plan` filed it. sha is "" when the footer has been edited to drop it — the
// document path is the half that matters, and the parse still succeeds.
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
	// The last matching line, not the first: a body that quotes an earlier
	// proposal's footer in its prose still ends with its own.
	var line string
	for _, l := range strings.Split(body, "\n") {
		if t := strings.TrimLeft(l, "> \t"); strings.HasPrefix(t, planFooterPrefix) {
			line = t
		}
	}
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
	return planFooter{doc: doc, sha: sha}, true
}

func firstField(s string) string {
	if f := strings.Fields(s); len(f) > 0 {
		return f[0]
	}
	return ""
}
