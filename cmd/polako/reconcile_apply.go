package main

// The changes `polako plan` makes once the operator approves a
// reconciliation (reconcile.go): GitHub milestones for the doc's milestones,
// an item marker into each issue matched to an item, each issue into its
// item's milestone, and the doc's new text onto an issue whose item changed.
// Then one plan-backlog run files the missing items (plan_reconcile.go).
//
// Every write here is the binary's, built from the doc and fixed wording,
// never from issue text, and none touches a label or an issue's state: the
// model's half of plan still only creates issues, so nothing a run reads can
// approve its own work. Bodies are re-read just before each edit and only
// ever added to, so a curator's edits survive. The order lets a run stop
// anywhere and be rerun: milestones first, so a move always has somewhere to
// go.

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// reconChange is one write to one issue.
type reconChange struct {
	row       reconRow
	issue     int
	milestone string // a move's destination
}

// reconChanges is everything an approved reconciliation writes.
type reconChanges struct {
	creates  []reconRow
	marks    []reconChange
	moves    []reconChange
	edits    []reconChange
	comments []reconChange
	// milestones are the doc's milestone items whose GitHub milestone has to
	// exist, created or renamed to the item's name, before anything moves.
	milestones []docItem
	batch      string     // the batch milestone new issues go in, when per-item ones are off
	held       []reconRow // matched only by a guess nobody confirmed: neither marked nor filed
}

func (c reconChanges) empty() bool {
	return len(c.creates)+len(c.marks)+len(c.moves)+len(c.edits)+len(c.comments)+len(c.milestones) == 0
}

// milestonesOff and the empty string are planChanges's two special
// -milestone values: off means no milestones at all, empty means one per
// milestone item. Anything else is a batch title, today's single milestone.
const milestonesOff = "off"

// planChanges turns a reconciliation into the writes plan would make.
// milestones is -milestone: "" for one GitHub milestone per milestone item,
// "off" for none, or a batch title for new issues alone, existing ones left
// where they are.
func planChanges(res reconResult, milestones string) reconChanges {
	var c reconChanges
	need := map[string]bool{}
	for _, r := range res.rows {
		if r.guessed() {
			c.held = append(c.held, r)
			continue
		}
		p, _ := r.primary()
		for _, a := range r.actions {
			switch a {
			case actCreate:
				c.creates = append(c.creates, r)
				if milestones == "" && r.milestone != "" {
					need[r.item.id] = true
				}
			case actMark:
				c.marks = append(c.marks, reconChange{row: r, issue: p.issue})
			case actEdit:
				c.edits = append(c.edits, reconChange{row: r, issue: p.issue})
			case actComment:
				c.comments = append(c.comments, reconChange{row: r, issue: p.issue})
			case actMove:
				if milestones == "" {
					c.moves = append(c.moves, reconChange{row: r, issue: p.issue, milestone: r.milestone})
					need[r.item.id] = true
				}
			}
		}
		// An issue already in its item's milestone under an older name: the
		// item was renamed, and the milestone follows it.
		if milestones == "" && r.item.kind == itemMilestone && r.milestone != "" && r.inMilestone != r.milestone &&
			strings.HasPrefix(strings.ToLower(r.inMilestone), strings.ToLower(itemMilestonePrefix(res.doc.slug, r.item.id))) {
			need[r.item.id] = true
		}
	}
	for _, r := range res.rows {
		if need[r.item.id] {
			if r.item.kind == itemMilestone {
				c.milestones = append(c.milestones, r.item)
			} else {
				c.batch = res.doc.slug
			}
		}
	}
	if milestones != "" && milestones != milestonesOff && len(c.creates) > 0 {
		c.batch = milestones
	}
	return c
}

// summary is the changes as the lines plan's approval prompt shows.
func (c reconChanges) summary(res reconResult, maxIssues int) []string {
	ids := func(rows []reconRow) string {
		var s []string
		for _, r := range rows {
			s = append(s, r.item.id)
		}
		return strings.Join(s, ", ")
	}
	refs := func(ch []reconChange) string {
		var s []string
		for _, x := range ch {
			s = append(s, fmt.Sprintf("#%d (%s)", x.issue, x.row.item.id))
		}
		return strings.Join(s, ", ")
	}
	var lines []string
	if n := len(c.milestones); n > 0 {
		lines = append(lines, fmt.Sprintf("milestones  make sure %s exist, named as the doc names them now", plural(n, "item milestone")))
	}
	if c.batch != "" {
		lines = append(lines, fmt.Sprintf("milestones  make sure %q exists", c.batch))
	}
	if len(c.marks) > 0 {
		issues := map[int]bool{}
		for _, m := range c.marks {
			issues[m.issue] = true
		}
		lines = append(lines, fmt.Sprintf("mark        %s with their items: %s", plural(len(issues), "issue"), refs(c.marks)))
	}
	if len(c.moves) > 0 {
		lines = append(lines, fmt.Sprintf("move        %s to their item's milestone", plural(len(c.moves), "issue")))
	}
	if len(c.edits) > 0 {
		lines = append(lines, fmt.Sprintf("edit        %s whose item changed, adding the doc's new text: %s", plural(len(c.edits), "issue"), refs(c.edits)))
	}
	if len(c.comments) > 0 {
		lines = append(lines, fmt.Sprintf("comment on  %s whose item changed: %s", plural(len(c.comments), "issue"), refs(c.comments)))
	}
	if n := len(c.creates); n > 0 {
		line := fmt.Sprintf("create      %s, one plan-backlog run capped at %d", plural(n, "issue"), maxIssues)
		if maxIssues <= n {
			// An explicit -max-issues with no headroom: the skill files items
			// in order until the cap, and an item that becomes an epic takes
			// a slot per child, so even a cap equal to the count can stop
			// short. Whether anything is left, and how many reruns it takes,
			// turns on how the run cuts the items, so the line says what to
			// do rather than promising a rest or a single rerun.
			line += " — it files items in order up to the cap, epics included; rerun plan for any left over"
		}
		lines = append(lines, line+": "+ids(c.creates))
	}
	if len(c.held) > 0 {
		lines = append(lines, fmt.Sprintf("leave       %s matched only by a guess you didn't confirm: %s", plural(len(c.held), "item"), ids(c.held)))
	}
	return lines
}

// reconApplied is what the writes did, and what didn't take.
type reconApplied struct {
	milestones, marks, moves, edits, comments int
	failures                                  []string
}

// applyChanges makes the binary's share of the writes. Failures are
// collected rather than fatal: one issue that won't take an edit doesn't
// stop the rest, and rerunning plan finds whatever is left.
func applyChanges(ctx context.Context, cfg config, res reconResult, c reconChanges) reconApplied {
	var out reconApplied
	fail := func(format string, args ...any) { out.failures = append(out.failures, fmt.Sprintf(format, args...)) }

	if len(c.milestones) > 0 {
		existing, err := listMilestones(ctx, cfg)
		if err != nil {
			fail("could not list milestones (%v) — no milestone was made or renamed, and no issue moved", err)
			c.moves = nil
		}
		for _, it := range c.milestones {
			if err != nil {
				break
			}
			changed, merr := ensureItemMilestone(ctx, cfg, existing, res.doc.slug, it)
			if merr != nil {
				fail("could not make the %q milestone: %v", itemMilestoneTitle(res.doc.slug, it), merr)
				continue
			}
			if changed {
				out.milestones++
			}
		}
	}
	if c.batch != "" {
		if err := ensureMilestone(ctx, cfg, c.batch); err != nil {
			fail("could not make the %q milestone: %v", c.batch, err)
		}
	}

	// One body edit per issue, whatever it carries: a marker, a doc update,
	// or both.
	type bodyEdit struct {
		markers []itemMarker
		from    map[string]string // new ID → old ID, for a renumbered marker
		updates []string
		marks   int // of markers, the ones that mark rather than re-sync
	}
	edits := map[int]*bodyEdit{}
	var order []int
	editFor := func(n int) *bodyEdit {
		if edits[n] == nil {
			edits[n] = &bodyEdit{from: map[string]string{}}
			order = append(order, n)
		}
		return edits[n]
	}
	marker := func(r reconRow) itemMarker { return itemMarker{slug: res.doc.slug, id: r.item.id, at: res.sha} }
	for _, ch := range c.marks {
		e := editFor(ch.issue)
		e.markers = append(e.markers, marker(ch.row))
		e.marks++
		if p, _ := ch.row.primary(); p.how == matchRenumbered {
			e.from[ch.row.item.id] = p.from
		}
	}
	for _, ch := range c.edits {
		e := editFor(ch.issue)
		e.markers = append(e.markers, marker(ch.row))
		e.updates = append(e.updates, docUpdate(res.doc, ch.row))
	}
	// A comment is the thread's notice; the marker's at moves too, or the
	// next run would say the same thing again.
	for _, ch := range c.comments {
		editFor(ch.issue).markers = append(editFor(ch.issue).markers, marker(ch.row))
	}
	for _, n := range order {
		e := edits[n]
		body, err := issueBody(ctx, cfg, n)
		if err != nil {
			fail("could not read #%d to edit it: %v", n, err)
			continue
		}
		next := body
		for _, mk := range e.markers {
			next = withItemMarker(next, mk, e.from[mk.id])
		}
		for _, u := range e.updates {
			next = withDocUpdate(next, u)
		}
		if next == body {
			continue
		}
		if _, err := ghStdin(ctx, cfg, next, "issue", "edit", strconv.Itoa(n), "--body-file", "-"); err != nil {
			fail("could not edit #%d: %v", n, err)
			continue
		}
		cfg.detailf("plan: edited #%d's body", n)
		out.marks += e.marks
		out.edits += len(e.updates)
	}
	for _, ch := range c.comments {
		if _, err := ghStdin(ctx, cfg, docUpdate(res.doc, ch.row), "issue", "comment", strconv.Itoa(ch.issue), "--body-file", "-"); err != nil {
			fail("could not comment on #%d: %v", ch.issue, err)
			continue
		}
		out.comments++
	}
	for _, ch := range c.moves {
		if _, err := gh(ctx, cfg, "issue", "edit", strconv.Itoa(ch.issue), "--milestone", ch.milestone); err != nil {
			fail("could not move #%d to %q: %v", ch.issue, ch.milestone, err)
			continue
		}
		cfg.detailf("plan: moved #%d to %q", ch.issue, ch.milestone)
		out.moves++
	}
	return out
}

// issueBody reads one issue's body as it is now: an edit builds on what's
// there at the moment of writing, not on the listing read minutes earlier.
func issueBody(ctx context.Context, cfg config, n int) (string, error) {
	raw, err := gh(ctx, cfg, "issue", "view", strconv.Itoa(n), "--json", "body")
	if err != nil {
		return "", err
	}
	var v struct {
		Body string `json:"body"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", fmt.Errorf("unreadable issue: %w", err)
	}
	return v.Body, nil
}

// withItemMarker puts mk into body. It replaces the marker for the same
// item, or for from when the doc was renumbered, so an at moves rather than
// doubling up; otherwise it goes on its own line directly above the plan
// footer, or at the end of a body that has none.
func withItemMarker(body string, mk itemMarker, from string) string {
	lines := strings.Split(body, "\n")
	for i, l := range lines {
		for _, have := range parseItemMarkers(l) {
			if strings.EqualFold(have.slug, mk.slug) && (strings.EqualFold(have.id, mk.id) || (from != "" && strings.EqualFold(have.id, from))) {
				lines[i] = mk.String()
				return strings.Join(lines, "\n")
			}
		}
	}
	return insertAboveFooter(body, mk.String())
}

// insertAboveFooter adds block above the plan footer, or at the end of a
// body with no footer. A marker goes directly over the footer, after any
// markers there; anything else goes above the markers too.
func insertAboveFooter(body, block string) string {
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	at := -1
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimLeft(l, "> \t"), planFooterPrefix) {
			at = i
		}
	}
	if at < 0 {
		return strings.TrimRight(body, "\n") + "\n\n" + block + "\n"
	}
	if strings.HasPrefix(block, "<!--") {
		// A marker joins the markers over the footer, last, no blank line.
		return strings.Join(append(append(slices.Clone(lines[:at]), block), lines[at:]...), "\n") + "\n"
	}
	for at > 0 && len(parseItemMarkers(lines[at-1])) > 0 {
		at--
	}
	head := strings.TrimRight(strings.Join(lines[:at], "\n"), "\n")
	return head + "\n\n" + block + "\n\n" + strings.Join(lines[at:], "\n") + "\n"
}

// withDocUpdate adds a doc update to body, above its markers and footer.
func withDocUpdate(body, update string) string {
	return insertAboveFooter(body, "## Doc update\n\n"+update)
}

// docUpdate says what changed in an item since its issue was filed: the
// doc's words then and now. Both are the operator's own doc.
func docUpdate(doc planDoc, r reconRow) string {
	p, _ := r.primary()
	quote := func(s string) string {
		var b strings.Builder
		for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
			b.WriteString(strings.TrimRight("> "+l, " ") + "\n")
		}
		return b.String()
	}
	return fmt.Sprintf("%s in %s changed since this issue was filed. It now reads:\n\n%s\nIt read:\n\n%s",
		r.item.id, doc.path, quote(r.item.text), quote(p.was))
}

// ghMilestone is one row of the repo's milestone listing.
type ghMilestone struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
}

func listMilestones(ctx context.Context, cfg config) ([]ghMilestone, error) {
	raw, err := gh(ctx, cfg, "api", "--paginate", "repos/{owner}/{repo}/milestones?state=all")
	if err != nil {
		return nil, err
	}
	var ms []ghMilestone
	if err := json.Unmarshal(raw, &ms); err != nil {
		return nil, fmt.Errorf("unreadable milestones list: %w", err)
	}
	return ms, nil
}

// ensureItemMilestone makes the item's milestone exist under the name the
// doc gives the item now: found by its prefix, renamed when the item was,
// created when absent. changed is false when it was already right.
func ensureItemMilestone(ctx context.Context, cfg config, existing []ghMilestone, slug string, it docItem) (changed bool, err error) {
	want := itemMilestoneTitle(slug, it)
	prefix := strings.ToLower(itemMilestonePrefix(slug, it.id))
	for _, m := range existing {
		if !strings.HasPrefix(strings.ToLower(m.Title), prefix) {
			continue
		}
		if m.Title == want {
			return false, nil
		}
		_, err := gh(ctx, cfg, "api", "-X", "PATCH", "repos/{owner}/{repo}/milestones/"+strconv.Itoa(m.Number), "-f", "title="+want)
		return err == nil, err
	}
	_, err = gh(ctx, cfg, "api", "repos/{owner}/{repo}/milestones", "-f", "title="+want)
	return err == nil, err
}

// placeNewIssues is the post-run pass over what the skill filed from an
// items file: each new issue should carry exactly one listed item's marker,
// and goes in that item's milestone. A missing marker is added when the
// title points at exactly one listed item; anything else is reported, never
// guessed. An epic's children ride with the epic's item.
func placeNewIssues(ctx context.Context, cfg config, numbers []int, res reconResult, create []reconRow, titles map[string]string, batch string) []string {
	var failures []string
	listed := map[string]reconRow{}
	for _, r := range create {
		listed[strings.ToUpper(r.item.id)] = r
	}
	filed := map[string]int{}
	itemOf := map[int]string{}
	type viewed struct {
		Title  string `json:"title"`
		Body   string `json:"body"`
		Parent *struct {
			Number int `json:"number"`
		} `json:"parent"`
	}
	var children []int
	parentOf := map[int]int{}
	for _, n := range numbers {
		raw, err := gh(ctx, cfg, "issue", "view", strconv.Itoa(n), "--json", "title,body,parent")
		if err != nil {
			failures = append(failures, fmt.Sprintf("could not read new issue #%d to check its item marker: %v", n, err))
			continue
		}
		var v viewed
		if err := json.Unmarshal(raw, &v); err != nil {
			failures = append(failures, fmt.Sprintf("unreadable new issue #%d: %v", n, err))
			continue
		}
		if v.Parent != nil && slices.Contains(numbers, v.Parent.Number) {
			children = append(children, n)
			parentOf[n] = v.Parent.Number
			continue
		}
		var ids []string
		for _, mk := range parseItemMarkers(v.Body) {
			if strings.EqualFold(mk.slug, res.doc.slug) {
				ids = append(ids, strings.ToUpper(mk.id))
			}
		}
		if len(ids) == 0 {
			id, ok := soleItemFor(v.Title, create)
			if !ok {
				failures = append(failures, fmt.Sprintf("#%d carries no item marker and its title points at no single listed item — "+
					"add its marker by hand, or the next plan asks you to match it", n))
				continue
			}
			next := withItemMarker(v.Body, itemMarker{slug: res.doc.slug, id: listed[id].item.id}, "")
			if _, err := ghStdin(ctx, cfg, next, "issue", "edit", strconv.Itoa(n), "--body-file", "-"); err != nil {
				failures = append(failures, fmt.Sprintf("could not add the %s marker to #%d: %v", listed[id].fullID, n, err))
				continue
			}
			cfg.logf("plan: #%d had no item marker; added %s's, its title's match", n, listed[id].fullID)
			ids = []string{id}
		}
		for _, id := range ids {
			if first, dup := filed[id]; dup {
				failures = append(failures, fmt.Sprintf("#%d and #%d both carry %s/%s — close one; plan never closes an issue itself",
					first, n, res.doc.slug, id))
				continue
			}
			if _, ok := listed[id]; !ok {
				failures = append(failures, fmt.Sprintf("#%d carries %s/%s, which the items file didn't list — check it isn't a duplicate",
					n, res.doc.slug, id))
			}
			filed[id] = n
		}
		itemOf[n] = ids[0]
	}
	for _, n := range children {
		itemOf[n] = itemOf[parentOf[n]]
	}
	for _, n := range numbers {
		id, ok := itemOf[n]
		if !ok {
			continue
		}
		title := batch
		if t := titles[id]; t != "" {
			title = t
		}
		if title == "" {
			continue
		}
		if _, err := gh(ctx, cfg, "issue", "edit", strconv.Itoa(n), "--milestone", title); err != nil {
			failures = append(failures, fmt.Sprintf("could not put #%d in %q: %v", n, title, err))
			continue
		}
		cfg.detailf("plan: put #%d in %q", n, title)
	}
	return failures
}

// soleItemFor is the one listed item a title clearly names, by the same
// word overlap a weak guess uses.
func soleItemFor(title string, create []reconRow) (string, bool) {
	var hit string
	for _, r := range create {
		if words := nameWords(r.item.name); len(words) > 0 && wordOverlap(words, title) >= weakBar {
			if hit != "" {
				return "", false
			}
			hit = strings.ToUpper(r.item.id)
		}
	}
	return hit, hit != ""
}
