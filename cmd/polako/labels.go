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
	"encoding/json"
	"fmt"
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
	// isGateLabel marks the one def built from -label (setupLabelDefs,
	// applySetup's public-repo prompt) — the only one checkLabelDef compares
	// against gateLabelDescription rather than merely checking existence.
	// The labelTable entries and the policy-label set never carry
	// this: their own descriptions are fixed and unrelated to the marker.
	isGateLabel bool
}

// gateLabelDescription is the description `setup` stamps on a gate label —
// on creation (setupLabelDefs, applySetup's public-repo prompt) or, with
// -apply, on an existing label found to be missing it (ensureLabelMarked).
// `status` reads it back (markedGateLabel) to scope itself with no -label
// given: the marker, not the name, is what makes a label "the" gate label.
const gateLabelDescription = "gate label for `polako work -label`"

// labelTable is every label polako manages on an issue, each orchestration
// state the queue logic reads (see the consts in main.go), not decoration.
// The first three are required. designLabel is not: a repo with no design
// requests loses nothing by lacking it.
var labelTable = []labelDef{
	{name: needsHumanLabel, color: "D93F0B", description: "polako parked this issue for a human", required: true},
	{name: proposedLabel, color: "1D76DB", description: "proposed by polako — a human removes this label to queue it", required: true},
	{name: awaitingAnswerLabel, color: "FBCA04", description: "polako is waiting for an answer on this issue", required: true},
	{name: designLabel, color: "C5DEF5", description: "a design request — polako work skips it; polako design works it into a plan"},
}

// labelTableNames is every name labelTable holds, in table order — the one
// place setup_templates.go and its self-test (repo_test.go) each add a
// gate label to, so a future addition to labelTable can't leave either
// stale.
func labelTableNames() []string {
	names := make([]string, len(labelTable))
	for i, l := range labelTable {
		names[i] = l.name
	}
	return names
}

// labelByName looks up one label's definition. Every call site names one of
// the consts above, so a miss means this table fell out of sync with
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

// ghLabelDesc is the one shape labelDescription and markedGateLabel each
// read back off a `gh api .../labels...` call: the name and the
// description — the field the gate-label marker lives in. Named apart from
// backlog.go's own ghLabel (name only, off an issue's own label list) since
// the two read different endpoints for different reasons.
type ghLabelDesc struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// labelDescription reads one label's own description over the same
// `/labels/<name>` endpoint labelExists always has; exists is false, with no
// error, for a label the repository has never defined — the same 404
// handling labelExists held on its own before this. Wrapped in retryRead:
// only a real transient failure — a network not yet reassociated, a token
// GitHub refuses — reaches retryRead as an error, the same as labelExists's
// own comment already explains.
func labelDescription(ctx context.Context, cfg config, name string) (desc string, exists bool, err error) {
	type result struct {
		label  ghLabelDesc
		exists bool
	}
	r, err := retryRead(ctx, cfg, "label "+name, func() (result, error) {
		out, apiErr := gh(ctx, cfg, "api", "repos/{owner}/{repo}/labels/"+url.PathEscape(name))
		if apiErr == nil {
			var l ghLabelDesc
			if uerr := json.Unmarshal(out, &l); uerr != nil {
				return result{}, fmt.Errorf("parsing label %s: %w", name, uerr)
			}
			return result{label: l, exists: true}, nil
		}
		if isNotFoundError(apiErr) {
			return result{}, nil
		}
		return result{}, apiErr
	})
	return r.label.Description, r.exists, err
}

// labelExists reports whether the repository has defined name. `gh label
// list` stays out of the binary (issue #412); this is the
// one-name check the setup report and work's preflight need instead. A thin
// wrapper over labelDescription now, which reads the same endpoint and
// parses one more field — every existing caller here only ever wanted the
// bool.
func labelExists(ctx context.Context, cfg config, name string) (bool, error) {
	_, exists, err := labelDescription(ctx, cfg, name)
	return exists, err
}

// markedGateLabel finds the one label carrying setup's own gate-label
// marker (gateLabelDescription) — one `gh api repos/{owner}/{repo}/labels`
// read, so `status` can scope itself with no -label given. Absent (no label
// carries it) and ambiguous (more than one does) both come back with name
// == "": scoping to a guess would be worse than not scoping at all, the
// same "wrong is worse than none" rule installedVersion already holds to
// for a plugin id it can't disambiguate. ambiguous distinguishes the two so
// a caller can say which happened.
func markedGateLabel(ctx context.Context, cfg config) (name string, ambiguous bool, err error) {
	out, err := retryRead(ctx, cfg, "listing labels", func() ([]byte, error) {
		return gh(ctx, cfg, "api", "repos/{owner}/{repo}/labels")
	})
	if err != nil {
		return "", false, err
	}
	var labels []ghLabelDesc
	if err := json.Unmarshal(out, &labels); err != nil {
		return "", false, fmt.Errorf("parsing labels: %w", err)
	}
	var matches []string
	for _, l := range labels {
		if l.Description == gateLabelDescription {
			matches = append(matches, l.Name)
		}
	}
	switch len(matches) {
	case 0:
		return "", false, nil
	case 1:
		return matches[0], false, nil
	default:
		return "", true, nil
	}
}

// ensureLabelMarked stamps gateLabelDescription onto an existing label —
// -apply's remedy for checkLabelDef's "exists, not marked as the gate
// label" row, so a hand-made gate label predating `setup` becomes one
// `status` can discover on its own via markedGateLabel.
func ensureLabelMarked(ctx context.Context, cfg config, name string) error {
	_, err := gh(ctx, cfg, "label", "edit", name, "--description", gateLabelDescription)
	return err
}

// isNotFoundError reports whether a gh api call failed because the resource
// does not exist, matched on gh's own wording the way unknownJSONField
// matches its own rejection — gh gives no separate exit code for "does not
// exist" versus every other way an api call can fail.
func isNotFoundError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "HTTP 404")
}

// isForbiddenError reports whether a gh api call failed because the token
// can't reach the resource — branch protection is admin-only, so a token with
// less than that gets a 403 rather than an answer. Matched the same way
// isNotFoundError is: gh gives no separate exit code for it.
func isForbiddenError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "HTTP 403")
}

// isAlreadyExistsError reports whether ensureLabel's own create failed
// because the label was already there — gh's own wording for it, matched
// case-insensitively since exact casing isn't documented. Not a write
// failure to report: the label exists, which is exactly what the create
// wanted.
func isAlreadyExistsError(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "already exists")
}
