package main

// The reconciliation table, as text and as JSON. Like the rest of status it
// prints no issue or PR text: IDs and names from the doc, states, numbers,
// paths.

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// statusDesign is `status -design`: the reconciliation table for one plan
// doc (reconcile.go), in place of the snapshot. Reads only, like the rest of
// status. The gate label is found the way the snapshot finds it, since on a
// public repo it decides whose item markers count.
func statusDesign(ctx context.Context, cfg config, fs *flag.FlagSet, opt statusOptions, out io.Writer, rpt report) error {
	labelFromFlag := flagWasSet(fs, "label")
	labelFromEnv := !labelFromFlag && os.Getenv(envVarName("label")) != ""
	cfg, _, _ = resolveStatusScope(ctx, cfg, labelFromFlag, labelFromEnv)
	in, err := gatherRecon(ctx, cfg, opt.design)
	if err != nil {
		return err
	}
	res := reconcile(in)
	if opt.json {
		return renderReconJSON(out, cfg.repo, res)
	}
	renderRecon(out, rpt, cfg.repo, res)
	return nil
}

// reconNameWidth clips an item's name in the table. The full name is in the
// doc, in the milestone, and in -json.
const reconNameWidth = 40

// renderRecon prints the table: one row per item, then what no row can say.
func renderRecon(w io.Writer, rpt report, repo string, res reconResult) {
	fmt.Fprintf(w, "%s\n", rpt.bold(repo))
	if len(res.rows) == 0 {
		fmt.Fprintf(w, "\n%s has no items — number its entries under a \"## Milestones\" heading, or its tickets "+
			"under \"## Drafted tickets\", and plan files one issue per item\n", res.doc.path)
		return
	}
	rows := make([][]string, 0, len(res.rows))
	for _, r := range res.rows {
		rows = append(rows, []string{r.item.id, clipRunes(r.item.name, reconNameWidth), reconStateCell(r),
			reconIssueCell(r), issueRefs(r.prs), reconActionCell(r), reconNoteCell(r)})
	}
	header := []string{"id", "item", "state", "issue", "pr", "action", "note"}
	printTable(w, rpt, reconHeader(res), header, rows, len(header))
	for _, line := range reconFooter(res) {
		fmt.Fprintf(w, "%s\n", line)
	}
}

// reconHeader names the doc, the commit it was read at, and the count of
// each state present, in precedence order.
func reconHeader(res reconResult) string {
	var parts []string
	for _, s := range append(statePrecedence, stateMissing) {
		if n := res.count(s); n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, s))
		}
	}
	at := ""
	if res.sha != "" {
		at = " @ " + res.sha
	}
	return fmt.Sprintf("%s (%s)%s — %s: %s", res.doc.path, res.doc.slug, at,
		plural(len(res.rows), "item"), strings.Join(parts, ", "))
}

func reconStateCell(r reconRow) string {
	s := string(r.state)
	if r.stale {
		s += ", stale"
	}
	if r.state == stateDone && len(r.stubs) > 0 {
		s += ", stub left"
	}
	return s
}

// reconIssueCell is the row's issues, the deciding one first, each guess
// marked with a ?.
func reconIssueCell(r reconRow) string {
	var refs []string
	for _, m := range r.matches {
		ref := "#" + strconv.Itoa(m.issue)
		if !m.how.settled() {
			ref += "?"
		}
		refs = append(refs, ref)
	}
	return strings.Join(refs, ", ")
}

func reconActionCell(r reconRow) string {
	acts := make([]string, len(r.actions))
	for i, a := range r.actions {
		acts[i] = string(a)
	}
	return strings.Join(acts, ", ")
}

// reconNoteCell is what the row's other cells can't say.
func reconNoteCell(r reconRow) string {
	var notes []string
	if p, ok := r.primary(); ok {
		switch p.how {
		case matchRenumbered:
			notes = append(notes, "marked "+p.from+" before the doc was renumbered")
		case matchWeak:
			notes = append(notes, "by title")
		}
	}
	if len(r.shares) > 0 {
		notes = append(notes, "shares its issue with "+strings.Join(r.shares, ", "))
	}
	if len(r.dupes) > 0 {
		notes = append(notes, "also marked: "+issueRefs(r.dupes))
	}
	if r.closest > 0 {
		notes = append(notes, "closest #"+strconv.Itoa(r.closest))
	}
	for _, s := range r.stubs {
		notes = append(notes, "stub "+s.String())
	}
	return strings.Join(notes, "; ")
}

// reconFooter is the lines under the table: what a ? means, and what no row
// owns.
func reconFooter(res reconResult) []string {
	var lines []string
	if n := res.guesses(); n > 0 {
		lines = append(lines, fmt.Sprintf("? is a guess, %d in all — matched by the issue's own words, not a marker", n))
	}
	if len(res.loose) > 0 {
		var paths []string
		for _, s := range res.loose {
			paths = append(paths, s.String())
		}
		lines = append(lines, "stubs no one item claims: "+strings.Join(paths, ", "))
	}
	if len(res.orphans) > 0 {
		var refs []string
		for _, o := range res.orphans {
			refs = append(refs, fmt.Sprintf("#%d (%s)", o.issue, o.id))
		}
		lines = append(lines, "markers for items the doc no longer has: "+strings.Join(refs, ", "))
	}
	if res.truncated {
		lines = append(lines, fmt.Sprintf("only the first %d issues and PRs were read — rows past them may say missing when they aren't", reconListLimit))
	}
	return lines
}

// clipRunes cuts s to width runes, ending in "..." when it had to.
func clipRunes(s string, width int) string {
	r := []rune(s)
	if len(r) <= width {
		return s
	}
	return strings.TrimRight(string(r[:width-3]), " ,;:") + "..."
}

// reconDoc is the table as -json prints it.
type reconDoc struct {
	Repo      string           `json:"repo"`
	Doc       string           `json:"doc"`
	Slug      string           `json:"slug"`
	SHA       string           `json:"sha"`
	Items     []reconDocItem   `json:"items"`
	Orphans   []reconDocOrphan `json:"orphan_markers"`
	Stubs     []string         `json:"loose_stubs"`
	Truncated bool             `json:"truncated"`
}

type reconDocItem struct {
	ID         string          `json:"id"`
	FullID     string          `json:"full_id"`
	Name       string          `json:"name"`
	Kind       string          `json:"kind"`
	State      string          `json:"state"`
	Stale      bool            `json:"stale"`
	Issues     []reconDocMatch `json:"issues"`
	PRs        []int           `json:"prs"`
	Actions    []string        `json:"actions"`
	Milestone  string          `json:"milestone,omitempty"`
	Shares     []string        `json:"shares"`
	Duplicates []int           `json:"duplicates"`
	Closest    int             `json:"closest,omitempty"`
	Stubs      []string        `json:"stubs"`
}

type reconDocMatch struct {
	Issue     int    `json:"issue"`
	Match     string `json:"match"`
	Confirmed bool   `json:"confirmed"`
	Was       string `json:"renumbered_from,omitempty"`
}

type reconDocOrphan struct {
	Issue int    `json:"issue"`
	ID    string `json:"id"`
}

var matchNames = map[matchHow]string{matchMarker: "marker", matchRenumbered: "renumbered", matchGuess: "guess", matchWeak: "title", matchConfirmed: "confirmed"}

var kindNames = map[itemKind]string{itemMilestone: "milestone", itemTicket: "ticket", itemMarked: "marked"}

// renderReconJSON prints the table as one JSON document. Every list is
// present, empty rather than null, so a script can range over it unguarded.
func renderReconJSON(w io.Writer, repo string, res reconResult) error {
	doc := reconDoc{Repo: repo, Doc: res.doc.path, Slug: res.doc.slug, SHA: res.sha, Truncated: res.truncated,
		Items: []reconDocItem{}, Orphans: []reconDocOrphan{}, Stubs: []string{}}
	for _, r := range res.rows {
		it := reconDocItem{ID: r.item.id, FullID: r.fullID, Name: r.item.name, Kind: kindNames[r.item.kind],
			State: string(r.state), Stale: r.stale, Milestone: r.milestone, Closest: r.closest,
			Issues: []reconDocMatch{}, PRs: nonNilSlice(r.prs), Actions: []string{}, Shares: nonNilSlice(r.shares),
			Duplicates: nonNilSlice(r.dupes), Stubs: []string{}}
		for _, m := range r.matches {
			it.Issues = append(it.Issues, reconDocMatch{Issue: m.issue, Match: matchNames[m.how], Confirmed: m.how.settled(), Was: m.from})
		}
		for _, a := range r.actions {
			it.Actions = append(it.Actions, string(a))
		}
		for _, s := range r.stubs {
			it.Stubs = append(it.Stubs, s.String())
		}
		doc.Items = append(doc.Items, it)
	}
	for _, o := range res.orphans {
		doc.Orphans = append(doc.Orphans, reconDocOrphan{Issue: o.issue, ID: o.id})
	}
	for _, s := range res.loose {
		doc.Stubs = append(doc.Stubs, s.String())
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(doc)
}
