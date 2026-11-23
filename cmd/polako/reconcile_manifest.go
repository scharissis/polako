package main

// The items file `polako plan` hands plan-backlog: the doc's items that have
// no issue yet, each with the marker line its issue must carry, and the items
// that already have one, by number. The skill files exactly what it lists
// (skills/plan-backlog/SKILL.md, "When you're handed items").
//
// Built from the doc, issue numbers, states and file paths — never issue text,
// which on a public repo is anyone's to write and would reach the run as
// something to read.

import (
	"fmt"
	"strings"
)

// itemsManifestPath is where plan writes the items file, under the
// checkout's scratch directory, which setup's .gitignore already covers.
const itemsManifestPath = ".polako-scratch/plan-items.md"

// itemsManifest renders the items file for the rows to create.
func itemsManifest(res reconResult, create []reconRow) string {
	var b strings.Builder
	at := ""
	if res.sha != "" {
		at = " at " + res.sha
	}
	fmt.Fprintf(&b, "# Items to file\n\nFrom %s (slug %s)%s. File one issue per item below, its marker line\n"+
		"directly above the provenance footer.\n", res.doc.path, res.doc.slug, at)
	for _, r := range create {
		fmt.Fprintf(&b, "\n## %s: %s\n\nIts marker line:\n\n    %s\n", r.fullID, r.item.name,
			itemMarker{slug: res.doc.slug, id: r.item.id})
		if len(r.stubs) > 0 {
			var paths []string
			for _, s := range r.stubs {
				paths = append(paths, s.String())
			}
			fmt.Fprintf(&b, "\nStubs in the code that name it: %s\n", strings.Join(paths, ", "))
		}
		b.WriteString("\nThe doc's entry:\n\n")
		for _, line := range strings.Split(r.item.text, "\n") {
			b.WriteString(strings.TrimRight("> "+line, " ") + "\n")
		}
	}
	var served []string
	for _, r := range res.rows {
		if p, ok := r.primary(); ok && p.how.settled() {
			served = append(served, fmt.Sprintf("- %s: #%d (%s)", r.fullID, p.issue, r.state))
		}
	}
	if len(served) > 0 {
		b.WriteString("\n## Items that already have an issue\n\nFor `Depends on:` and `--blocked-by`. Don't file these.\n\n")
		b.WriteString(strings.Join(served, "\n") + "\n")
	}
	if len(res.loose) > 0 {
		var paths []string
		for _, s := range res.loose {
			paths = append(paths, s.String())
		}
		fmt.Fprintf(&b, "\n## Stubs no item claims\n\n%s\n", strings.Join(paths, ", "))
	}
	return b.String()
}
