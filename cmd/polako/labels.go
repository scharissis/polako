package main

// The one table of every label polako itself manages, and the one lookup
// that answers "does this label exist on the repo". Before this, the three
// ensureLabel call sites (parkIssue, work's preflight, the proposed-label
// ensure in intake.go and retire.go) each carried their own colour and
// description literal — this file is where those live now, so the three
// descriptions can't drift apart.
//
// Each is declared (gh's "label create") the first time its call site needs
// it, never earlier: GitHub refuses to apply a label the repository has not
// defined, and a headless run holds no grant that could create one ahead of
// time some other way, so ensureLabel is a find-or-create at the point of use.

import (
	"context"
	"net/url"
	"strings"
)

// labelDef is one entry of labelTable: what ensureLabel needs to declare it,
// and whether the supervisor treats its absence as something to gate on.
type labelDef struct {
	name        string
	color       string
	description string
	required    bool
}

// labelTable is every label polako applies to an issue. All three are
// required: each is orchestration state the queue logic reads (see
// needsHumanLabel, proposedLabel, awaitingAnswerLabel in main.go), not
// decoration a repo could do without.
var labelTable = []labelDef{
	{name: needsHumanLabel, color: "D93F0B", description: "polako parked this issue for a human", required: true},
	{name: proposedLabel, color: "1D76DB", description: "proposed by polako — a human removes this label to queue it", required: true},
	{name: awaitingAnswerLabel, color: "FBCA04", description: "polako is waiting for an answer on this issue", required: true},
}

// labelByName looks up one label's definition. Every call site names one of
// the three consts above, so a miss means this table fell out of sync with
// main.go, not bad input — hence the panic rather than a second error path
// every caller would have to handle for a case that can't happen.
func labelByName(name string) labelDef {
	for _, l := range labelTable {
		if l.name == name {
			return l
		}
	}
	panic("label: unknown label " + name)
}

// labelExists reports whether the repository has defined name. `gh label
// list` stays out of the binary (docs/plans/setup.md ticket 1); this is the
// one-name check the setup report and work's preflight need instead.
// Wrapped in retryRead: a 404 answers definitively, so the closure turns it
// into (false, nil) before retryRead ever sees it as an error to retry: only
// a real transient failure — a network not yet reassociated, a token
// GitHub refuses — reaches retryRead as an error.
func labelExists(ctx context.Context, cfg config, name string) (bool, error) {
	return retryRead(ctx, cfg, "label "+name, func() (bool, error) {
		_, err := gh(ctx, cfg, "api", "repos/{owner}/{repo}/labels/"+url.PathEscape(name))
		if err == nil {
			return true, nil
		}
		if isNotFoundError(err) {
			return false, nil
		}
		return false, err
	})
}

// isNotFoundError reports whether a gh api call failed because the resource
// does not exist, matched on gh's own wording the way unknownJSONField
// matches its own rejection — gh gives no separate exit code for "does not
// exist" versus every other way an api call can fail.
func isNotFoundError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "HTTP 404")
}

// isAlreadyExistsError reports whether ensureLabel's own create failed
// because the label was already there — gh's own wording for it, matched
// case-insensitively since exact casing isn't documented. Not a write
// failure to report: the label exists, which is exactly what the create
// wanted.
func isAlreadyExistsError(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "already exists")
}
