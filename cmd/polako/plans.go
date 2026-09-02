package main

// `status` gains a plans section: what state each document under
// docs/plans/ is in, derived from the issues whose footer names it — never
// written down, so it cannot go stale. See docs/plans/plan-conventions.md
// for the convention this reads back, and footer.go for the parser it reads
// bodies with.
//
// One gh call for the whole report: every issue, open or closed, whose body
// carries the footer phrase. The search is a pre-filter — parsePlanFooter's
// own strictness is what actually decides whether a hit is a real footer, so
// a body that merely mentions the phrase in prose is dropped here the same
// way its own tests already prove.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// planDocsLimit bounds the one search call this derivation makes. A repo
// whose stamped issues outgrow it gets a warning naming the bound, rather
// than a report that quietly shows less than the truth. Same size as the
// queue listing's own bound (backlog.go) — no reasoning here favors a
// different number.
const planDocsLimit = 200

// planFooterSearchPhrase is what the gh search asks for: planFooterPrefix,
// trimmed of the trailing space GitHub's phrase search does not want. Tied to
// the contract constant rather than a second literal, so the two cannot
// drift apart.
var planFooterSearchPhrase = strings.TrimSpace(planFooterPrefix)

// planDocState is a plan document's derived state — never written down,
// always read back from the issues whose footer names it. See
// docs/plans/plan-conventions.md's table.
type planDocState string

const (
	planDraft    planDocState = "draft"
	planProposed planDocState = "proposed"
	planActive   planDocState = "active"
	planDone     planDocState = "done"
)

// planDocStatus is one line of the plans section.
type planDocStatus struct {
	path  string
	state planDocState
	// containers is the subset of the doc's naming issues that are
	// themselves containers (a non-zero sub-issue rollup), ascending by
	// number — reusing containerInfo so containerRefs renders them exactly
	// as the queue's own containers row does.
	containers []containerInfo
	// openChildren is the sum of total-completed across those containers:
	// how much of the work those epics still track is unfinished.
	openChildren int
}

// goneDoc is a footer naming a document that no longer exists on disk, with
// the issue numbers that name it — a deleted plan's leftovers, kept visible
// rather than silently dropped.
type goneDoc struct {
	path   string
	issues []int
}

// planDocsSnapshot is the whole plans section.
type planDocsSnapshot struct {
	docs []planDocStatus
	gone []goneDoc
	// truncated is set when the search call hit planDocsLimit — the states
	// above may be derived from an incomplete set of issues, and the render
	// says so rather than presenting them as certain.
	truncated bool
}

// ghPlanIssue is one row of the plans search:
// `gh issue list --state all --search '"<phrase>" in:body' --json
// number,state,labels,body,subIssuesSummary`.
type ghPlanIssue struct {
	Number    int       `json:"number"`
	State     string    `json:"state"`
	Labels    []ghLabel `json:"labels"`
	Body      string    `json:"body"`
	SubIssues struct {
		Total     int `json:"total"`
		Completed int `json:"completed"`
	} `json:"subIssuesSummary"`
}

// readPlanDocs derives the plans section. Best-effort from the caller's
// point of view (see readStatus): a failure here is not reason enough to
// fail the whole snapshot, the same tolerance the usage probe and the quiet
// spans already get.
func readPlanDocs(ctx context.Context, cfg config) (planDocsSnapshot, error) {
	local, haveLocal := localPlanDocs(cfg.dir)
	localSet := make(map[string]bool, len(local))
	for _, d := range local {
		localSet[d] = true
	}

	// Asks for one row past the bound: that's the only way to tell "exactly
	// planDocsLimit issues, nothing missed" from "more than planDocsLimit,
	// truncated" — both would otherwise come back as exactly planDocsLimit
	// rows and be indistinguishable.
	raw, err := retryRead(ctx, cfg, "listing plan-stamped issues", func() ([]byte, error) {
		return gh(ctx, cfg, "issue", "list", "--state", "all",
			"--search", `"`+planFooterSearchPhrase+`" in:body`,
			"--json", "number,state,labels,body,subIssuesSummary",
			"--limit", strconv.Itoa(planDocsLimit+1))
	})
	if err != nil {
		return planDocsSnapshot{}, err
	}

	var issues []ghPlanIssue
	if err := json.Unmarshal(raw, &issues); err != nil {
		return planDocsSnapshot{}, fmt.Errorf("parsing plan-stamped issue list: %w", err)
	}

	snap := planDocsSnapshot{}
	if len(issues) > planDocsLimit {
		snap.truncated = true
		issues = issues[:planDocsLimit]
	}

	byDoc := map[string][]ghPlanIssue{}
	for _, is := range issues {
		footer, ok := parsePlanFooter(is.Body)
		if !ok {
			continue
		}
		byDoc[footer.doc] = append(byDoc[footer.doc], is)
	}

	if haveLocal {
		for _, path := range local {
			snap.docs = append(snap.docs, planDocStatusFrom(path, byDoc[path]))
		}
		for _, doc := range sortedKeys(byDoc) {
			if localSet[doc] {
				continue
			}
			var numbers []int
			for _, is := range byDoc[doc] {
				numbers = append(numbers, is.Number)
			}
			slices.Sort(numbers)
			snap.gone = append(snap.gone, goneDoc{path: doc, issues: numbers})
		}
	} else {
		// No local docs/plans to check existence against — a -repo run with
		// no matching checkout, most likely. Report what GitHub says without
		// claiming to know which of these are gone.
		for _, doc := range sortedKeys(byDoc) {
			snap.docs = append(snap.docs, planDocStatusFrom(doc, byDoc[doc]))
		}
	}
	return snap, nil
}

// sortedKeys returns a map's keys, ascending, so a derivation built from map
// grouping still reports in a stable order.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// localPlanDocs lists the *.md files under docs/plans in the given checkout,
// logical forward-slash paths (docs/plans/foo.md) matching how a footer
// names them regardless of host OS. ok is false when the directory could not
// be read at all — distinct from a real, empty directory — so the caller
// never mistakes "couldn't check" for "confirmed gone".
func localPlanDocs(dir string) (docs []string, ok bool) {
	entries, err := os.ReadDir(filepath.Join(dir, "docs", "plans"))
	if err != nil {
		return nil, false
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		docs = append(docs, "docs/plans/"+e.Name())
	}
	slices.Sort(docs)
	return docs, true
}

// planDocStatusFrom derives one document's line from the issues whose
// footer names it. See docs/plans/plan-conventions.md's table: no issues is
// draft; all closed is done; any issue still open and past the `proposed`
// gate is active; otherwise every naming issue is still open-and-proposed
// or closed-early with nothing past the gate yet, which reads as proposed.
func planDocStatusFrom(path string, issues []ghPlanIssue) planDocStatus {
	if len(issues) == 0 {
		return planDocStatus{path: path, state: planDraft}
	}

	allClosed := true
	anyPastGate := false
	for _, is := range issues {
		if !strings.EqualFold(is.State, "open") {
			continue
		}
		allClosed = false
		if !hasLabel(is.Labels, proposedLabel) {
			anyPastGate = true
		}
	}
	state := planProposed
	switch {
	case allClosed:
		state = planDone
	case anyPastGate:
		state = planActive
	}

	var containers []containerInfo
	openChildren := 0
	for _, is := range issues {
		if is.SubIssues.Total == 0 {
			continue
		}
		containers = append(containers, containerInfo{
			number:    is.Number,
			total:     is.SubIssues.Total,
			completed: is.SubIssues.Completed,
			held:      hasLabel(is.Labels, needsHumanLabel) || hasLabel(is.Labels, proposedLabel),
		})
		openChildren += is.SubIssues.Total - is.SubIssues.Completed
	}
	slices.SortFunc(containers, func(a, b containerInfo) int { return a.number - b.number })

	return planDocStatus{path: path, state: state, containers: containers, openChildren: openChildren}
}

// --- rendering ---

// printPlanDocs prints the docs/plans/ section built by readPlanDocs: one
// row per document, its derived state, its container issues, and how many
// of those containers' children are still open. Absent when there is
// nothing to say — no local docs/plans and no gone footer either — the same
// "no line on a healthy backlog" rule needsYou (status.go) follows.
func printPlanDocs(w io.Writer, rpt report, plans planDocsSnapshot) {
	if len(plans.docs) == 0 && len(plans.gone) == 0 {
		return
	}
	if len(plans.docs) > 0 {
		rows := make([][]string, 0, len(plans.docs))
		for _, d := range plans.docs {
			rows = append(rows, []string{d.path, string(d.state), planContainersCell(d), planOpenChildrenCell(d)})
		}
		header := []string{"doc", "state", "containers", "open children"}
		printTable(w, rpt, "plan documents", header, rows, len(header))
	} else {
		fmt.Fprintf(w, "\n%s\n", rpt.bold("plan documents"))
	}
	if len(plans.gone) > 0 {
		refs := make([]string, len(plans.gone))
		for i, g := range plans.gone {
			refs[i] = fmt.Sprintf("%s (%s)", g.path, issueRefs(g.issues))
		}
		fmt.Fprintf(w, "  (gone — footer names a document no longer on disk: %s)\n", strings.Join(refs, ", "))
	}
	if plans.truncated {
		fmt.Fprintf(w, "  (past the first %d issues carrying the plan footer — state above may be incomplete)\n",
			planDocsLimit)
	}
}

func planContainersCell(d planDocStatus) string {
	if len(d.containers) == 0 {
		return "—"
	}
	return containerRefs(d.containers)
}

func planOpenChildrenCell(d planDocStatus) string {
	if len(d.containers) == 0 {
		return "—"
	}
	return strconv.Itoa(d.openChildren)
}

// --- JSON rendering ---

// statusDocPlans mirrors planDocsSnapshot field for field: Docs is the text
// report's table rows, Gone the same footers-with-no-file the "gone" note
// lists, Truncated the same warning the note prints.
type statusDocPlans struct {
	Docs      []statusDocPlan `json:"docs"`
	Gone      []statusDocGone `json:"gone"`
	Truncated bool            `json:"truncated"`
}

// statusDocPlan is one document's line: State is planDocState's string form
// ("draft", "proposed", "active", "done"); Containers reuses
// statusDocContainer (status.go), the same shape the queue's own containers
// carry, so a caller reads a container issue's rollup the same way in both
// places.
type statusDocPlan struct {
	Path         string               `json:"path"`
	State        string               `json:"state"`
	Containers   []statusDocContainer `json:"containers"`
	OpenChildren int                  `json:"open_children"`
}

// statusDocGone is a footer naming a document with no matching file on disk,
// and the issue numbers whose footer names it.
type statusDocGone struct {
	Path   string `json:"path"`
	Issues []int  `json:"issues"`
}
