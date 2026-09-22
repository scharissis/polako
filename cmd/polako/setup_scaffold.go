package main

// The scaffold half of issue #417: docs/VISION.md and
// docs/plans/README.md, the pages plan-backlog's own layout convention
// assumes exist (skills/plan-backlog/SKILL.md, "The layout convention").
// Offered by -apply as its own prompt, default no — unlike the .gitignore
// fix and the CLAUDE.md block, a repo that hasn't opted into plan-backlog
// yet has no use for either page.

import (
	_ "embed"
	"os"
	"path/filepath"
	"strings"
)

//go:embed assets/vision.md
var visionMdTemplate string

//go:embed assets/plans-readme.md
var plansReadmeTemplate string

const (
	visionMdPath     = "docs/VISION.md"
	plansReadmePath  = "docs/plans/README.md"
	visionAdviceOnly = "optional — `polako setup -apply` can scaffold them (advice only)"
)

// setupVisionRow is advisory only: the scaffold not existing never fails the
// report, and nothing here judges whether a repo without one is doing
// anything wrong. plan-backlog is opt-in. Driven by scaffoldNeedsWrite — the
// same gate proposeSetupFiles checks — rather than stat'ing docs/VISION.md
// alone, so a repo with its own VISION.md but no docs/plans/README.md still
// gets offered the write writeScaffold would actually make.
func setupVisionRow(cfg config) setupRow {
	if !scaffoldNeedsWrite(cfg.dir) {
		return setupRow{name: visionMdPath, status: setupOK}
	}
	var missing []string
	for _, rel := range []string{visionMdPath, plansReadmePath} {
		if !fileExistsIn(cfg.dir, filepath.FromSlash(rel)) {
			missing = append(missing, rel)
		}
	}
	return setupRow{name: visionMdPath, status: setupMissing,
		detail: "missing " + strings.Join(missing, ", ") + " — " + visionAdviceOnly}
}

// scaffoldNeedsWrite reports whether either scaffold page is missing from
// dir — the gate proposeSetupFiles checks on the freshly fetched worktree
// before staging anything, the same way claudeMdNeedsUpdate gates the block.
func scaffoldNeedsWrite(dir string) bool {
	return !fileExistsIn(dir, filepath.FromSlash(visionMdPath)) || !fileExistsIn(dir, filepath.FromSlash(plansReadmePath))
}

// writeScaffold writes whichever of the two pages dir is missing, leaving
// an existing one — a human's own — untouched.
func writeScaffold(dir string) error {
	if err := writeIfMissing(filepath.Join(dir, filepath.FromSlash(visionMdPath)), visionMdTemplate); err != nil {
		return err
	}
	return writeIfMissing(filepath.Join(dir, filepath.FromSlash(plansReadmePath)), plansReadmeTemplate)
}

func writeIfMissing(path, content string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}
