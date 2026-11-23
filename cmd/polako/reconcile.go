package main

// Reconciliation: what each of a plan document's items already has on
// GitHub, worked out before plan proposes anything, so a re-plan files only
// what's missing. See docs/designs/reconcile.md.
//
// Three layers, so the middle one can be tested on fixtures alone:
// gatherRecon (reconcile_gather.go) makes every read, reconcile here is a
// pure function of what was read, and reconcile_render.go prints the result.
//
// Issue text is read for markers, "milestone N" mentions and word overlap,
// and nothing else. None of it is printed by status or handed to a model: on
// a repo that takes outside issues, it's anyone's to write.

import (
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// reconIssue is one issue as reconciliation reads it, from `gh issue list
// --state all` (reconIssueFields).
type reconIssue struct {
	Number      int       `json:"number"`
	Title       string    `json:"title"`
	Body        string    `json:"body"`
	State       string    `json:"state"`
	StateReason string    `json:"stateReason"`
	Labels      []ghLabel `json:"labels"`
	Milestone   *struct {
		Title string `json:"title"`
	} `json:"milestone"`
	Author struct {
		Login string `json:"login"`
	} `json:"author"`
	ClosedBy []struct {
		Number int `json:"number"`
	} `json:"closedByPullRequestsReferences"`
	SubIssues struct {
		Total int `json:"total"`
	} `json:"subIssuesSummary"`
}

func (is reconIssue) open() bool { return strings.EqualFold(is.State, "OPEN") }

func (is reconIssue) milestone() string {
	if is.Milestone == nil {
		return ""
	}
	return strings.TrimSpace(is.Milestone.Title)
}

// reconPR is one pull request, from `gh pr list --state all` (reconPRFields).
type reconPR struct {
	Number int    `json:"number"`
	State  string `json:"state"` // OPEN, MERGED or CLOSED
	Head   string `json:"headRefName"`
	Closes []struct {
		Number int `json:"number"`
	} `json:"closingIssuesReferences"`
}

// stubHit is one "not implemented" in the code. text is the line itself,
// read to match it to an item and never printed.
type stubHit struct {
	path string
	line int
	text string
}

func (s stubHit) String() string { return s.path + ":" + strconv.Itoa(s.line) }

// reconInput is everything reconciliation reads.
type reconInput struct {
	doc    planDoc
	sha    string // the checkout's HEAD, short
	issues []reconIssue
	prs    []reconPR
	// oldTexts is the doc's items as they stood at each commit an issue was
	// filed or last synced at: sha → upper-case ID → text. A sha that isn't
	// here is one the checkout couldn't show, and nothing filed at it can be
	// called stale.
	oldTexts map[string]map[string]string
	stubs    []stubHit
	// Whose markers count without confirming. On a private repo, everyone's.
	// On a public one, the operator's own and any on an issue carrying the
	// gate label: anyone can type an HTML comment, only collaborators label.
	trustAll     bool
	viewer       string
	gate         string
	branchPrefix string
	truncated    bool // a listing hit its limit, so some issues or PRs went unread
}

// matchHow is how an issue was matched to an item. It decides whether the
// match stands on its own or waits for the operator to confirm it.
type matchHow int

const (
	matchMarker     matchHow = iota // its marker names the item
	matchRenumbered                 // its marker names the ID the item's text had before the doc was renumbered
	matchGuess                      // its footer names the doc and its body the item's number, or a marker nobody vouches for names it
	matchWeak                       // its title shares most of the item's name
)

// settled reports whether a match stands without the operator confirming it.
func (h matchHow) settled() bool { return h == matchMarker || h == matchRenumbered }

// itemMatch is one issue matched to an item.
type itemMatch struct {
	issue int
	how   matchHow
	from  string // matchRenumbered: the ID its marker names, to be rewritten
	stale bool   // the item's text changed since the issue was filed or synced
	was   string // the item's text back then, when stale
}

// itemState is where an item stands, read off its issues.
type itemState string

const (
	stateInProgress itemState = "in progress"
	stateParked     itemState = "parked"
	stateQueued     itemState = "queued"
	stateProposed   itemState = "proposed"
	stateDone       itemState = "done"
	stateDropped    itemState = "dropped"
	stateMissing    itemState = "missing"
)

// statePrecedence orders the states an item's issues can be in; the first
// one present is the item's. Open work outranks finished work, and finished
// outranks dropped.
var statePrecedence = []itemState{stateInProgress, stateParked, stateQueued, stateProposed, stateDone, stateDropped}

func stateRank(s itemState) int {
	if i := slices.Index(statePrecedence, s); i >= 0 {
		return i
	}
	return len(statePrecedence)
}

// reconAction is one change plan would make for an item.
type reconAction string

const (
	actCreate  reconAction = "create"  // file an issue for it
	actMark    reconAction = "mark"    // write its marker into the issue matched to it
	actMove    reconAction = "move"    // put that issue in the item's milestone
	actEdit    reconAction = "edit"    // append the doc's new text to the issue's body
	actComment reconAction = "comment" // say on the thread that the doc moved on
)

// reconRow is one item and what it has.
type reconRow struct {
	item    docItem
	fullID  string
	matches []itemMatch // the issue deciding the item's state first
	state   itemState
	stale   bool
	prs     []int // open PRs for work in progress, merged ones for done work
	actions []reconAction
	stubs   []stubHit
	shares  []string // other items the deciding issue serves too
	closest int      // a title close to the item's name, under the guess bar; 0 for none
	dupes   []int    // open issues past the first whose marker names the item
	// milestone is the one the deciding issue belongs in: the item's own for
	// a milestone item, the doc's batch one otherwise, "" when the issue sits
	// with another item it serves.
	milestone string
}

// primary is the issue that decides the row's state.
func (r reconRow) primary() (itemMatch, bool) {
	if len(r.matches) == 0 {
		return itemMatch{}, false
	}
	return r.matches[0], true
}

// guessed reports whether the row's deciding issue still needs confirming.
func (r reconRow) guessed() bool {
	p, ok := r.primary()
	return ok && !p.how.settled()
}

func (r *reconRow) addMatch(m itemMatch) {
	for _, have := range r.matches {
		if have.issue == m.issue {
			return
		}
	}
	r.matches = append(r.matches, m)
}

// orphanMarker is a marker naming an item the doc no longer has.
type orphanMarker struct {
	issue int
	id    string
}

// reconResult is the whole reconciliation.
type reconResult struct {
	doc       planDoc
	sha       string
	rows      []reconRow
	orphans   []orphanMarker
	loose     []stubHit // stubs no single item claims
	truncated bool
}

func (r reconResult) count(s itemState) int {
	n := 0
	for _, row := range r.rows {
		if row.state == s {
			n++
		}
	}
	return n
}

// guesses is how many items are matched only by a guess.
func (r reconResult) guesses() int {
	n := 0
	for _, row := range r.rows {
		if row.guessed() {
			n++
		}
	}
	return n
}

// Weak guesses: a title sharing at least weakBar of an item's name words is
// a guess; one sharing at least weakFloor is only named as the closest.
const (
	weakBar   = 0.6
	weakFloor = 0.3
)

// reconcile works out, for every item, which issues serve it, where it
// stands, and what plan would change. Pure: everything it knows is in in.
func reconcile(in reconInput) reconResult {
	res := reconResult{doc: in.doc, sha: in.sha, truncated: in.truncated}
	rowOf := make(map[string]int, len(in.doc.items))
	rowWithKey := make(map[string]int, len(in.doc.items)) // -1 when two items share a text
	for i, it := range in.doc.items {
		res.rows = append(res.rows, reconRow{item: it, fullID: in.doc.fullID(it)})
		rowOf[strings.ToUpper(it.id)] = i
		k := itemKey(it.text)
		if _, dup := rowWithKey[k]; dup {
			rowWithKey[k] = -1
		} else {
			rowWithKey[k] = i
		}
	}
	byNumber := make(map[int]reconIssue, len(in.issues))
	for _, is := range in.issues {
		byNumber[is.Number] = is
	}

	// resolve finds the row an ID filed at sha means now, and whether the
	// item's text has moved on since. Usually the row holding that ID. But
	// when the text the ID had then now belongs to another item, the doc was
	// renumbered — an item inserted above it — and the text wins. Otherwise
	// one insert would make every later issue stale and re-file the last item.
	resolve := func(id, sha string) (row int, renumbered bool, was string, stale bool) {
		row, has := rowOf[strings.ToUpper(id)]
		old, known := in.oldTexts[sha][strings.ToUpper(id)]
		switch {
		case !known && has:
			return row, false, "", false
		case !known:
			return -1, false, "", false
		case has && itemKey(res.rows[row].item.text) == itemKey(old):
			return row, false, "", false
		}
		if j, ok := rowWithKey[itemKey(old)]; ok && j >= 0 {
			return j, true, "", false
		}
		if has {
			return row, false, old, true
		}
		return -1, false, "", false
	}

	// Markers first: an issue carrying one for this doc is spoken for, so no
	// guess below can give it to another item.
	claimed := map[int]bool{}
	for _, is := range in.issues {
		markers := parseItemMarkers(is.Body)
		if len(markers) == 0 {
			continue
		}
		footerSHA := footerSHAFor(is.Body, in.doc.slug)
		trusted := in.trustAll ||
			(in.viewer != "" && strings.EqualFold(is.Author.Login, in.viewer)) ||
			(in.gate != "" && hasLabel(is.Labels, in.gate))
		for _, mk := range markers {
			if !strings.EqualFold(mk.slug, in.doc.slug) {
				continue
			}
			claimed[is.Number] = true
			sha := mk.at
			if sha == "" {
				sha = footerSHA
			}
			row, renumbered, was, stale := resolve(mk.id, sha)
			if row < 0 {
				if trusted {
					res.orphans = append(res.orphans, orphanMarker{issue: is.Number, id: mk.id})
				}
				continue
			}
			m := itemMatch{issue: is.Number, how: matchMarker, stale: stale, was: was}
			if renumbered {
				m.how, m.from = matchRenumbered, mk.id
			}
			if !trusted {
				m.how, m.from = matchGuess, ""
			}
			res.rows[row].addMatch(m)
		}
	}

	// Then what plan runs before markers left: a footer naming this doc, and
	// the item's number in the body's own words. Never a container — an epic
	// groups items, it isn't one.
	guessedIssue := map[int]bool{}
	for _, is := range in.issues {
		if claimed[is.Number] || is.SubIssues.Total > 0 {
			continue
		}
		footer, ok := parsePlanFooter(is.Body)
		if !ok || !strings.EqualFold(slugFromPath(footer.doc), in.doc.slug) {
			continue
		}
		for _, id := range itemRefs(is.Body) {
			row, _, was, stale := resolve(id, footer.sha)
			if row < 0 {
				continue
			}
			res.rows[row].addMatch(itemMatch{issue: is.Number, how: matchGuess, stale: stale, was: was})
			guessedIssue[is.Number] = true
		}
	}

	weakGuesses(&res, in.issues, claimed, guessedIssue)
	classify(&res, in, byNumber)
	claimStubs(&res, in.stubs)
	return res
}

// weakGuesses matches what's still unmatched by title alone, best score
// first and one issue per item, so two items never guess the same issue.
func weakGuesses(res *reconResult, issues []reconIssue, claimed, guessed map[int]bool) {
	type pair struct {
		row, issue int
		score      float64
	}
	var pairs []pair
	for i := range res.rows {
		if len(res.rows[i].matches) > 0 {
			continue
		}
		words := nameWords(res.rows[i].item.name)
		if len(words) == 0 {
			continue
		}
		for _, is := range issues {
			if claimed[is.Number] || guessed[is.Number] || is.SubIssues.Total > 0 {
				continue
			}
			if s := wordOverlap(words, is.Title); s >= weakFloor {
				pairs = append(pairs, pair{i, is.Number, s})
			}
		}
	}
	sort.SliceStable(pairs, func(a, b int) bool {
		if pairs[a].score != pairs[b].score {
			return pairs[a].score > pairs[b].score
		}
		if pairs[a].row != pairs[b].row {
			return pairs[a].row < pairs[b].row
		}
		return pairs[a].issue < pairs[b].issue
	})
	used := map[int]bool{}
	for _, p := range pairs {
		r := &res.rows[p.row]
		if len(r.matches) > 0 || used[p.issue] {
			continue
		}
		if p.score >= weakBar {
			r.addMatch(itemMatch{issue: p.issue, how: matchWeak})
			used[p.issue] = true
		} else if r.closest == 0 {
			r.closest = p.issue
		}
	}
}

// classify gives every row its state, evidence and actions.
func classify(res *reconResult, in reconInput, byNumber map[int]reconIssue) {
	openPRs, mergedPRs := prsByIssue(in.prs, in.issues, in.branchPrefix)
	stateOf := func(n int) itemState { return reconIssueState(byNumber[n], openPRs[n]) }

	servedBy := map[int][]int{} // issue → rows it serves, in doc order
	for i, r := range res.rows {
		for _, m := range r.matches {
			servedBy[m.issue] = append(servedBy[m.issue], i)
		}
	}

	for i := range res.rows {
		r := &res.rows[i]
		if len(r.matches) == 0 {
			r.state = stateMissing
			r.actions = []reconAction{actCreate}
			continue
		}
		sort.SliceStable(r.matches, func(a, b int) bool {
			return stateRank(stateOf(r.matches[a].issue)) < stateRank(stateOf(r.matches[b].issue))
		})
		p := r.matches[0]
		r.state, r.stale = stateOf(p.issue), p.stale
		switch r.state {
		case stateInProgress:
			r.prs = openPRs[p.issue]
		case stateDone:
			r.prs = mergedPRs[p.issue]
		}
		var open []int
		for _, m := range r.matches {
			if m.how.settled() && byNumber[m.issue].open() {
				open = append(open, m.issue)
			}
		}
		if len(open) > 1 {
			r.dupes = open[1:]
		}
		for _, j := range servedBy[p.issue] {
			if j != i {
				r.shares = append(r.shares, res.rows[j].item.id)
			}
		}
		// An issue serving several items sits with the first of them: one
		// issue has one milestone.
		if servedBy[p.issue][0] == i {
			r.milestone = rowMilestone(in.doc, r.item)
		}

		if !p.how.settled() || p.how == matchRenumbered {
			r.actions = append(r.actions, actMark)
		}
		if r.stale {
			switch r.state {
			case stateQueued, stateProposed:
				r.actions = append(r.actions, actEdit)
			case stateDropped:
				// Rejected: what the doc says now doesn't matter to it.
			default:
				r.actions = append(r.actions, actComment)
			}
		}
		if r.state != stateDropped && needsMove(byNumber[p.issue].milestone(), r.milestone, in.doc.slug, r.item) {
			r.actions = append(r.actions, actMove)
		}
	}
}

// reconIssueState is where one issue stands.
func reconIssueState(is reconIssue, openPRs []int) itemState {
	if !is.open() {
		switch strings.ToUpper(is.StateReason) {
		case "NOT_PLANNED", "DUPLICATE":
			return stateDropped
		}
		return stateDone
	}
	switch {
	case hasLabel(is.Labels, needsHumanLabel):
		return stateParked
	case len(openPRs) > 0 || hasLabel(is.Labels, awaitingAnswerLabel):
		return stateInProgress
	case hasLabel(is.Labels, proposedLabel):
		return stateProposed
	}
	return stateQueued
}

// prsByIssue maps each issue to its open PRs and its merged ones. A PR
// counts for an issue it closes or whose branch it's on; a closed issue's own
// closing references count too, unless the PR listing says that PR was
// closed unmerged.
func prsByIssue(prs []reconPR, issues []reconIssue, prefix string) (open, merged map[int][]int) {
	open, merged = map[int][]int{}, map[int][]int{}
	stateOf := make(map[int]string, len(prs))
	for _, pr := range prs {
		stateOf[pr.Number] = strings.ToUpper(pr.State)
		var targets []int
		for _, c := range pr.Closes {
			targets = append(targets, c.Number)
		}
		if n, ok := issueForBranch(pr.Head, prefix); ok {
			targets = append(targets, n)
		}
		for _, n := range targets {
			switch stateOf[pr.Number] {
			case "OPEN":
				open[n] = appendUnique(open[n], pr.Number)
			case "MERGED":
				merged[n] = appendUnique(merged[n], pr.Number)
			}
		}
	}
	for _, is := range issues {
		if is.open() {
			continue
		}
		for _, c := range is.ClosedBy {
			if s, known := stateOf[c.Number]; !known || s == "MERGED" {
				merged[is.Number] = appendUnique(merged[is.Number], c.Number)
			}
		}
	}
	for _, m := range []map[int][]int{open, merged} {
		for n := range m {
			slices.Sort(m[n])
		}
	}
	return open, merged
}

func appendUnique(s []int, n int) []int {
	if slices.Contains(s, n) {
		return s
	}
	return append(s, n)
}

// rowMilestone is the milestone an item's issue belongs in: the item's own
// for a milestone item, which is milestone-sized, and the doc's one batch
// milestone, named for its slug, for anything smaller.
func rowMilestone(doc planDoc, it docItem) string {
	if it.kind == itemMilestone {
		return itemMilestoneTitle(doc.slug, it)
	}
	return doc.slug
}

// itemMilestonePrefix is how a milestone item's GitHub milestone is found
// again: "[VISION] M4:". The title after it follows the item's name, so a
// renamed item renames its milestone instead of leaving one behind.
func itemMilestonePrefix(slug, id string) string { return "[" + slug + "] " + id + ":" }

// itemMilestoneTitle is a milestone item's GitHub milestone:
// "[VISION] M4: CSV importer with fixtures". The slug keeps two docs' M1
// apart, which GitHub needs anyway: milestone titles are unique per repo.
func itemMilestoneTitle(slug string, it docItem) string {
	return itemMilestonePrefix(slug, it.id) + " " + it.name
}

// needsMove reports whether an issue in milestone cur should move to want.
// Only off no milestone, the doc's batch one, or another of the doc's item
// milestones — where a renumbered item's issue is left — never off one
// somebody picked by hand.
func needsMove(cur, want, slug string, it docItem) bool {
	if want == "" {
		return false
	}
	lower := strings.ToLower(cur)
	if it.kind == itemMilestone {
		if strings.HasPrefix(lower, strings.ToLower(itemMilestonePrefix(slug, it.id))) {
			return false
		}
	} else if strings.EqualFold(cur, want) {
		return false
	}
	return cur == "" || strings.EqualFold(cur, slug) || strings.HasPrefix(lower, "["+strings.ToLower(slug)+"] ")
}

// footerSHAFor is the sha an issue's plan footer names, when the footer names
// the doc slug scopes; "" otherwise.
func footerSHAFor(body, slug string) string {
	if f, ok := parsePlanFooter(body); ok && strings.EqualFold(slugFromPath(f.doc), slug) {
		return f.sha
	}
	return ""
}

// itemRefRe finds an older issue's own words for the item it serves —
// "Vision milestone 4", "milestones 12 and 13", "ticket 2" — the way plan
// runs before markers wrote their "Why now".
var itemRefRe = regexp.MustCompile(`(?i)\b(milestone|ticket)s?\s+(\d{1,4}(?:\s*(?:,|&|and|to|–|-)\s*\d{1,4})*)`)

var digitsRe = regexp.MustCompile(`\d+`)

// itemRefs reads the item IDs a body names in prose, in order, each once.
func itemRefs(body string) []string {
	var ids []string
	seen := map[string]bool{}
	for _, m := range itemRefRe.FindAllStringSubmatch(body, -1) {
		letter := "M"
		if strings.EqualFold(m[1], "ticket") {
			letter = "T"
		}
		for _, n := range expandNumberList(m[2]) {
			if id := letter + strconv.Itoa(n); !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	return ids
}

// maxRefRange caps "14–19"-style ranges, so a typo can't claim the doc.
const maxRefRange = 20

// expandNumberList reads "12 and 13", "1, 2, 5" or "14–19".
func expandNumberList(s string) []int {
	var out []int
	locs := digitsRe.FindAllStringIndex(s, -1)
	prev := 0
	for k, loc := range locs {
		n, _ := strconv.Atoi(s[loc[0]:loc[1]])
		if k > 0 {
			sep := strings.ToLower(strings.TrimSpace(s[locs[k-1][1]:loc[0]]))
			if (sep == "–" || sep == "-" || sep == "to") && n > prev && n-prev <= maxRefRange {
				for x := prev + 1; x < n; x++ {
					out = append(out, x)
				}
			}
		}
		out = append(out, n)
		prev = n
	}
	return out
}

// reconStopwords never count towards a match: they'd tie every item to
// every title.
var reconStopwords = map[string]bool{
	"the": true, "and": true, "for": true, "with": true, "from": true, "into": true,
	"that": true, "this": true, "its": true, "add": true, "new": true, "use": true,
	"against": true, "each": true, "every": true, "other": true, "only": true, "also": true,
	"feat": true, "fix": true, "docs": true, "chore": true, "test": true, "refactor": true,
}

// nameWords is s's words as matching sees them: lower case, three letters or
// more, no stopwords, a plural's s dropped, each once.
func nameWords(s string) []string {
	var out []string
	seen := map[string]bool{}
	split := func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }
	for _, f := range strings.FieldsFunc(strings.ToLower(plainItemText(s)), split) {
		if len([]rune(f)) < 3 || reconStopwords[f] {
			continue
		}
		if len(f) > 4 {
			f = strings.TrimSuffix(f, "s")
		}
		if !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	return out
}

// wordOverlap is the share of words found in title.
func wordOverlap(words []string, title string) float64 {
	have := map[string]bool{}
	for _, w := range nameWords(title) {
		have[w] = true
	}
	n := 0
	for _, w := range words {
		if have[w] {
			n++
		}
	}
	return float64(n) / float64(len(words))
}

// stubWordMin is the shortest name word that can tie a stub to an item by
// its path. Shorter ones — "file", "cmd" — name no item.
const stubWordMin = 5

// claimStubs gives each stub to the one item it names. Two kinds of words
// count: an item's code spans (`explain`, `crawl.yml`), which are what code
// spells, found in the stub's line or path; and its name's longer words,
// found in the path alone. Never the prose of a comment: "reports cmd as not
// implemented" would tie a stub to "Health report". A stub two items could
// claim, or none, is listed apart, since a wrong guess puts a false "stub
// left" on finished work.
func claimStubs(res *reconResult, stubs []stubHit) {
	terms := make([][]string, len(res.rows)) // code spans
	words := make([][]string, len(res.rows)) // longer name words
	count := map[string]int{}
	for i, r := range res.rows {
		seen := map[string]bool{}
		for _, span := range codeSpanRe.FindAllString(r.item.text, -1) {
			if t := strings.ToLower(strings.Trim(span, "` ")); len(t) >= 3 && !seen[t] {
				seen[t] = true
				terms[i] = append(terms[i], t)
				count["`"+t]++
			}
		}
		for _, w := range nameWords(r.item.name) {
			if len(w) >= stubWordMin {
				words[i] = append(words[i], w)
				count[w]++
			}
		}
	}
	for _, s := range stubs {
		line, path := strings.ToLower(s.text), strings.ToLower(s.path)
		inPath := map[string]bool{}
		for _, w := range nameWords(s.path) {
			inPath[w] = true
		}
		var hits []int
		for i := range res.rows {
			named := false
			for _, t := range terms[i] {
				if count["`"+t] == 1 && (containsTerm(line, t) || containsTerm(path, t)) {
					named = true
				}
			}
			for _, w := range words[i] {
				if count[w] == 1 && inPath[w] {
					named = true
				}
			}
			if named {
				hits = append(hits, i)
			}
		}
		if len(hits) == 1 {
			res.rows[hits[0]].stubs = append(res.rows[hits[0]].stubs, s)
		} else {
			res.loose = append(res.loose, s)
		}
	}
}

// containsTerm reports whether s holds term as a whole word: "explain" is in
// `cmd explain:` but not in "explainer".
func containsTerm(s, term string) bool {
	word := func(b byte) bool { return b == '_' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' }
	for from := 0; ; {
		i := strings.Index(s[from:], term)
		if i < 0 {
			return false
		}
		i += from
		end := i + len(term)
		if (i == 0 || !word(s[i-1])) && (end == len(s) || !word(s[end])) {
			return true
		}
		from = i + 1
	}
}
