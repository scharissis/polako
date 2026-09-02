package main

// Garbage-collecting a finished plan: closeFinishedContainers calls into this
// once a container's own close has actually succeeded. If the container's
// body carries a plan footer (footer.go) and no other open issue names the
// same document, file one `proposed` issue asking a human to retire it — the
// "Retire on close" step in docs/plans/plan-conventions.md.
//
// Best-effort like the close's own comment: nothing here can turn a
// container close into a drain failure. A read, a search or a create that
// fails is a warning, and the shift carries on — worst case nobody sees a
// stale plan document flagged, which is exactly the status quo before this
// feature existed.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
)

// retiredDoc is one retire issue this shift filed, for the exit summary.
type retiredDoc struct {
	container int
	doc       string
	issue     int
}

// retireOrphanedDoc is the whole step. ok is true only when a retire issue
// was actually filed. err is non-nil only for a real context cancellation —
// every other failure is narrated here and swallowed, matching how the
// comment and close steps around it in closeFinishedContainers behave.
//
// filedThisCall is the set of documents this same closeFinishedContainers
// call has already retired, moments ago, for a different container. It has
// to be checked ahead of the GitHub search below: two containers naming the
// same document can both be finished in the same pass, and the search alone
// cannot be trusted to see the first container's create in time for the
// second — GitHub's search index lags a write by seconds to a minute (the
// same lag docs/plans/plan-conventions.md already accepts across shifts),
// which is far longer than the gap between two loop iterations in one call.
// This local set closes that gap for free — it costs no extra call, since
// closeFinishedContainers already holds the equivalent information in the
// `retired` slice it is accumulating.
func retireOrphanedDoc(ctx context.Context, cfg config, c containerInfo, filedThisCall map[string]bool) (rd retiredDoc, ok bool, err error) {
	body, err := containerBody(ctx, cfg, c.number)
	if err != nil {
		if ctx.Err() != nil {
			return retiredDoc{}, false, ctx.Err()
		}
		narrate(sevWarning, "could not read #%d's body to check for a plan footer (%v) — no retire issue filed",
			c.number, err)
		return retiredDoc{}, false, nil
	}
	footer, ok := parsePlanFooter(body)
	if !ok {
		return retiredDoc{}, false, nil
	}
	if filedThisCall[footer.doc] {
		return retiredDoc{}, false, nil
	}

	other, err := anyOtherOpenIssueNamesDoc(ctx, cfg, footer.doc)
	if err != nil {
		if ctx.Err() != nil {
			return retiredDoc{}, false, ctx.Err()
		}
		narrate(sevWarning, "could not search for other open issues naming %s (%v) — no retire issue filed",
			footer.doc, err)
		return retiredDoc{}, false, nil
	}
	if other {
		return retiredDoc{}, false, nil
	}

	issue, err := fileRetireIssue(ctx, cfg, c, footer)
	if err != nil {
		if ctx.Err() != nil {
			return retiredDoc{}, false, ctx.Err()
		}
		narrate(sevWarning, "could not file a retire issue for %s (%v) — file one by hand, or wait for "+
			"the next epic close on this document", footer.doc, err)
		return retiredDoc{}, false, nil
	}
	log.Printf("epic #%d: filed #%d to retire %s", c.number, issue, footer.doc)
	return retiredDoc{container: c.number, doc: footer.doc, issue: issue}, true, nil
}

// containerBody reads back the one field this step needs off a container
// that just closed — nothing upstream of here ever had reason to fetch it.
// Through retryRead like every other read-only gh lookup on this same code
// path (issueComments, called moments earlier in closeFinishedContainers) —
// a transient failure here should not read as "no footer" any more than one
// there should read as "no marker".
func containerBody(ctx context.Context, cfg config, issue int) (string, error) {
	raw, err := retryRead(ctx, cfg, fmt.Sprintf("reading #%d's body", issue), func() ([]byte, error) {
		return gh(ctx, cfg, "issue", "view", strconv.Itoa(issue), "--json", "body")
	})
	if err != nil {
		return "", err
	}
	var v struct {
		Body string `json:"body"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", fmt.Errorf("parsing #%d's body: %w", issue, err)
	}
	return v.Body, nil
}

// anyOtherOpenIssueNamesDoc runs the same search readPlanDocs (plans.go)
// makes, scoped to open issues: whether any of them, once parsed, names doc.
// This is the whole backstop against filing a second retire issue for the
// same document — the retire issue's own body carries the same footer
// (fileRetireIssue), so once it exists this search finds it and a later
// container closing for the same document files nothing.
//
// --state open is requested, but state is also read back and checked
// per-row rather than trusted: gh's --search results are not guaranteed to
// respect --state server-side on every version, and relying on that alone
// would silently reintroduce the container's own now-closed issue as a false
// positive on a lagging index.
//
// Through retryRead, the same as containerBody above and every other
// read-only gh lookup on this code path — a transient failure here should
// not read as "nothing else names this document" any more than one there
// should read as "no footer".
func anyOtherOpenIssueNamesDoc(ctx context.Context, cfg config, doc string) (bool, error) {
	raw, err := retryRead(ctx, cfg, "searching for other open issues naming "+doc, func() ([]byte, error) {
		return gh(ctx, cfg, "issue", "list", "--state", "open",
			"--search", `"`+planFooterSearchPhrase+`" in:body`,
			"--json", "number,state,body", "--limit", strconv.Itoa(planDocsLimit+1))
	})
	if err != nil {
		return false, err
	}
	var issues []struct {
		Number int    `json:"number"`
		State  string `json:"state"`
		Body   string `json:"body"`
	}
	if err := json.Unmarshal(raw, &issues); err != nil {
		return false, fmt.Errorf("parsing plan-stamped issue list: %w", err)
	}
	for _, is := range issues {
		if !strings.EqualFold(is.State, "open") {
			continue
		}
		if footer, ok := parsePlanFooter(is.Body); ok && footer.doc == doc {
			return true, nil
		}
	}
	return false, nil
}

// fileRetireIssue creates the retire issue and returns its number, parsed
// off the URL a successful `gh issue create` prints.
func fileRetireIssue(ctx context.Context, cfg config, c containerInfo, footer planFooter) (int, error) {
	title := fmt.Sprintf("docs: retire %s — every issue it proposed is closed", footer.doc)
	body := retireIssueBody(c, footer)
	raw, err := gh(ctx, cfg, "issue", "create", "--title", title, "--body", body, "--label", proposedLabel)
	if err != nil {
		// Far the likeliest cause is a repository that has never used the
		// `proposed` label — GitHub refuses to attach one that doesn't exist
		// yet. Same recovery parkIssue makes for needs-human: create it, then
		// retry the one call that actually failed.
		if cerr := ensureLabel(ctx, cfg, proposedLabel, proposedLabelColor, proposedLabelDesc); cerr == nil {
			raw, err = gh(ctx, cfg, "issue", "create", "--title", title, "--body", body, "--label", proposedLabel)
		}
	}
	if err != nil {
		return 0, err
	}
	return issueNumberFromCreateOutput(raw)
}

// issueNumberFromCreateOutput reads the issue number off the end of the URL
// `gh issue create` prints on success.
func issueNumberFromCreateOutput(raw []byte) (int, error) {
	url := strings.TrimSpace(string(raw))
	i := strings.LastIndexByte(url, '/')
	if i < 0 {
		return 0, fmt.Errorf("unrecognised `gh issue create` output: %q", url)
	}
	n, err := strconv.Atoi(url[i+1:])
	if err != nil {
		return 0, fmt.Errorf("unrecognised `gh issue create` output: %q", url)
	}
	return n, nil
}

// retireIssueBody names the container that closed, the document, and the
// rule from docs/plans/plan-conventions.md's "Retire on close" section — then
// ends with the same footer the document's own proposals carry, so a later
// container closing for this document finds this issue in the search above
// and files nothing.
func retireIssueBody(c containerInfo, footer planFooter) string {
	line := planFooterPrefix + footer.doc
	if footer.sha != "" {
		line += " @ " + footer.sha
	}
	return fmt.Sprintf("Every issue proposed from %s is closed — #%d was the last. Retire the document: "+
		"move what is still true into `docs/`, delete the file, and fix any inbound links.\n\n%s — "+
		"filed after #%d closed; edit freely.",
		footer.doc, c.number, line, c.number)
}
