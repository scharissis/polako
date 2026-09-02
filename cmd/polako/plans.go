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

	raw, err := retryRead(ctx, cfg, "listing plan-stamped issues", func() ([]byte, error) {
		return gh(ctx, cfg, "issue", "list", "--state", "all",
			"--search", `"`+planFooterSearchPhrase+`" in:body`,
			"--json", "number,state,labels,body,subIssuesSummary",
			"--limit", strconv.Itoa(planDocsLimit))
	})
	if err != nil {
		return planDocsSnapshot{}, err
	}

	var issues []ghPlanIssue
	if err := json.Unmarshal(raw, &issues); err != nil {
		return planDocsSnapshot{}, fmt.Errorf("parsing plan-stamped issue list: %w", err)
	}

	byDoc := map[string][]ghPlanIssue{}
	for _, is := range issues {
		footer, ok := parsePlanFooter(is.Body)
		if !ok {
			continue
		}
		byDoc[footer.doc] = append(byDoc[footer.doc], is)
	}

	snap := planDocsSnapshot{truncated: len(issues) >= planDocsLimit}
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
