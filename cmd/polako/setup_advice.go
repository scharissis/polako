package main

// docs/plans/setup.md ticket 5's three advice rows: never required, never
// touched by -apply (a repo setting, the owner's own call — see "Considered
// and not proposed" in that plan). Reported so an operator sees the gap
// without hunting for it, same spirit as the .gitignore/CLAUDE.md rows.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
)

// setupCIWorkflowRow is a plain tree read: any .yml/.yaml under
// .github/workflows counts, whatever it actually runs.
func setupCIWorkflowRow(cfg config) setupRow {
	const name = "CI workflow"
	dir := filepath.Join(cfg.dir, ".github", "workflows")
	entries, err := os.ReadDir(dir)
	switch {
	case err == nil:
		for _, e := range entries {
			ext := filepath.Ext(e.Name())
			if ext == ".yml" || ext == ".yaml" {
				return setupRow{name: name, status: setupOK}
			}
		}
		return setupRow{name: name, status: setupMissing, detail: "advice only — no .github/workflows/*.yml found"}
	case os.IsNotExist(err):
		return setupRow{name: name, status: setupMissing, detail: "advice only — no .github/workflows/*.yml found"}
	default:
		return setupRow{name: name, status: setupUnknown, detail: fmt.Sprintf("could not read %s (%v)", dir, err)}
	}
}

// branchProtectionState is readBranchProtection's answer: three states, not
// two, since "this token can't tell" is a real, common outcome (branch
// protection is an admin-only read) and must not be reported as "off".
type branchProtectionState int

const (
	branchProtectionUnknown branchProtectionState = iota
	branchProtectionOff
	branchProtectionOn
)

// readBranchProtection calls the one REST endpoint that answers this: 200
// means a rule exists, 404 means none does, and 403 means the token can't
// tell either way (it needs admin on the repository). Any other failure is a
// real error for retryRead to retry.
func readBranchProtection(ctx context.Context, cfg config, branch string) (branchProtectionState, error) {
	_, err := gh(ctx, cfg, "api", "repos/{owner}/{repo}/branches/"+url.PathEscape(branch)+"/protection")
	switch {
	case err == nil:
		return branchProtectionOn, nil
	case isNotFoundError(err):
		return branchProtectionOff, nil
	case isForbiddenError(err):
		return branchProtectionUnknown, nil
	default:
		return branchProtectionUnknown, err
	}
}

// setupBranchProtectionRow reads the default branch through
// originDefaultBranch (setup.go), the same resolution setupOriginHeadRow
// makes — no extra git subprocess just to name it.
func setupBranchProtectionRow(ctx context.Context, cfg config, reposOK, gitOK bool) setupRow {
	const name = "branch protection"
	if !reposOK {
		return setupRow{name: name, status: setupUnknown, detail: "the repository could not be read"}
	}
	if !gitOK {
		return setupRow{name: name, status: setupUnknown, detail: "git isn't on PATH"}
	}
	branch, notCheckout, err := originDefaultBranch(ctx, cfg)
	if err != nil {
		if notCheckout {
			return setupRow{name: name, status: setupUnknown, detail: fmt.Sprintf("-dir %s is not a git checkout", cfg.dir)}
		}
		return setupRow{name: name, status: setupUnknown, detail: "origin/HEAD does not resolve"}
	}
	state, err := retryRead(ctx, cfg, "branch protection", func() (branchProtectionState, error) {
		return readBranchProtection(ctx, cfg, branch)
	})
	if err != nil {
		return setupRow{name: name, status: setupUnknown, detail: fmt.Sprintf("could not read (%v)", err)}
	}
	switch state {
	case branchProtectionOn:
		return setupRow{name: name, status: setupOK}
	case branchProtectionOff:
		return setupRow{name: name, status: setupMissing, detail: "advice only — " + branch + " has no branch protection rule"}
	default:
		return setupRow{name: name, status: setupUnknown, detail: "this token can't check branch protection (needs admin)"}
	}
}

// deleteBranchOnMergeResult is readDeleteBranchOnMerge's answer: known is
// false on a gh too old to report the field at all — a terminal, not-erroring
// outcome, the same shape hasIssuesEnabled's own fallback takes.
type deleteBranchOnMergeResult struct {
	known bool
	value bool
}

func readDeleteBranchOnMerge(ctx context.Context, cfg config) (deleteBranchOnMergeResult, error) {
	out, err := gh(ctx, cfg, "repo", "view", "--json", "deleteBranchOnMerge")
	if unknownJSONField(err) {
		return deleteBranchOnMergeResult{}, nil
	}
	if err != nil {
		return deleteBranchOnMergeResult{}, err
	}
	var v struct {
		DeleteBranchOnMerge bool `json:"deleteBranchOnMerge"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return deleteBranchOnMergeResult{}, fmt.Errorf("parsing repository view: %w", err)
	}
	return deleteBranchOnMergeResult{known: true, value: v.DeleteBranchOnMerge}, nil
}

func setupDeleteBranchOnMergeRow(ctx context.Context, cfg config, reposOK bool) setupRow {
	const name = "delete branches on merge"
	if !reposOK {
		return setupRow{name: name, status: setupUnknown, detail: "the repository could not be read"}
	}
	result, err := retryRead(ctx, cfg, "repo view deleteBranchOnMerge", func() (deleteBranchOnMergeResult, error) {
		return readDeleteBranchOnMerge(ctx, cfg)
	})
	if err != nil {
		return setupRow{name: name, status: setupUnknown, detail: fmt.Sprintf("could not read (%v)", err)}
	}
	if !result.known {
		return setupRow{name: name, status: setupUnknown, detail: "this gh does not report deleteBranchOnMerge"}
	}
	if result.value {
		return setupRow{name: name, status: setupOK}
	}
	return setupRow{name: name, status: setupMissing,
		detail: "advice only — turn on \"automatically delete head branches\" in the repository's settings"}
}
