package main

// The report's last line (issue #734): the one command to run next. Setup is
// the first thing a new user runs, so its last line is where they look —
// and a `polako work` line is the wrong answer on a repo that isn't ready
// yet, or on one with nothing in its queue, where the answer is `plan`.

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
)

// rootVisionPath is the other place a vision doc lives: plenty of repos keep
// it at the root rather than under docs/. `plan -design` reads either.
const rootVisionPath = "VISION.md"

// setupNextStep picks the last line from the rows already read. rerun is the
// setup command itself, rebuilt from the operator's own flags, so a "fix
// this, then rerun" line stays true for a -repo run on a non-checkout too.
func setupNextStep(ctx context.Context, cfg config, rows []setupRow, rerun string) string {
	for _, r := range rows {
		if r.status == setupMissing && r.required {
			return fmt.Sprintf("next: fix the %s row, then rerun `%s`", r.name, rerun)
		}
	}
	open, err := hasOpenIssues(ctx, cfg)
	if err != nil || open {
		// A listing gh couldn't answer says nothing about the queue, so the
		// line stays what it always was rather than guessing an empty one.
		return suggestedWorkLine(cfg)
	}
	if doc := setupPlanDoc(cfg.dir); doc != "" {
		return fmt.Sprintf("polako plan -dir %s -design %s -dry-run", cfg.dir, doc)
	}
	return fmt.Sprintf("no issues and no design doc yet — start with `polako design -dir %s -brief \"<idea>\"` "+
		"or `polako health -dir %s`", cfg.dir, cfg.dir)
}

// hasOpenIssues asks for one open issue — any at all, held or not. Holds
// (proposed, needs-human, design) all name their own next step in `status`;
// this line only has to tell an empty repo from one with something in it.
func hasOpenIssues(ctx context.Context, cfg config) (bool, error) {
	out, err := gh(ctx, cfg, "issue", "list", "--state", "open", "--limit", "1", "--json", "number")
	if err != nil {
		return false, err
	}
	var issues []struct{}
	if err := json.Unmarshal(out, &issues); err != nil {
		return false, fmt.Errorf("parsing issue list: %w", err)
	}
	return len(issues) > 0, nil
}

// setupPlanDoc finds the doc `plan -design` would start from: docs/VISION.md,
// then a root VISION.md, then the first draft under docs/designs/. The
// designs README is the scaffold's index of that folder, not a plan.
func setupPlanDoc(dir string) string {
	for _, rel := range []string{visionMdPath, rootVisionPath} {
		if fileExistsIn(dir, filepath.FromSlash(rel)) {
			return rel
		}
	}
	docs, _ := localPlanDocs(dir)
	for _, d := range docs {
		if d != designsReadmePath {
			return d
		}
	}
	return ""
}

// fillBacklogHint names the commands that fill an empty backlog, for `work`
// and `status` (issue #736) — the same doc search setup's last line uses, but
// dir-free, like every other command those two print.
func fillBacklogHint(dir string) string {
	if doc := setupPlanDoc(dir); doc != "" {
		return "fill it with `polako plan -design " + doc + "`"
	}
	return "fill it with `polako design -brief \"<idea>\"` or `polako health`"
}

// setupRerunLine is `polako setup` with the flags that pick what it checks.
func setupRerunLine(opt setupOptions, cfg config) string {
	cmd := "polako setup -dir " + cfg.dir
	if opt.repo != "" {
		cmd += " -repo " + opt.repo
	}
	if cfg.label != "" {
		cmd += " -label " + cfg.label
	}
	if opt.policyLabels {
		cmd += " -policy-labels"
	}
	return cmd
}
