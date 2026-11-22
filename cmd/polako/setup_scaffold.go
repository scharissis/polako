package main

// The scaffold half of issue #417: docs/VISION.md and
// docs/designs/README.md, the pages plan-backlog's own layout convention
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

//go:embed assets/designs-readme.md
var designsReadmeTemplate string

const (
	visionMdPath      = "docs/VISION.md"
	designsReadmePath = "docs/designs/README.md"
	visionAdviceOnly  = "optional — `polako setup -apply` can scaffold them (advice only)"
)

// setupVisionRow is advisory only: the scaffold not existing never fails the
// report, and nothing here judges whether a repo without one is doing
// anything wrong. plan-backlog is opt-in. The row is named for the vision
// doc, so it reads `ok` whenever that doc exists — docs/designs/README.md
// alone missing is a note in the detail, not a `missing` row (issue #738).
func setupVisionRow(cfg config) setupRow {
	if !scaffoldNeedsWrite(cfg.dir) {
		return setupRow{name: visionMdPath, status: setupOK}
	}
	if fileExistsIn(cfg.dir, filepath.FromSlash(visionMdPath)) {
		return setupRow{name: visionMdPath, status: setupOK,
			detail: "no " + designsReadmePath + " — optional — `polako setup -apply` can scaffold it (advice only)"}
	}
	// A root VISION.md is the doc this row exists to find, just kept
	// elsewhere. scaffoldOffered keeps -apply from offering a second one.
	if fileExistsIn(cfg.dir, rootVisionPath) {
		return setupRow{name: visionMdPath, status: setupOK,
			detail: "found VISION.md at the root — `polako plan -design VISION.md` reads it there"}
	}
	var missing []string
	for _, rel := range []string{visionMdPath, designsReadmePath} {
		if !fileExistsIn(cfg.dir, filepath.FromSlash(rel)) {
			missing = append(missing, rel)
		}
	}
	return setupRow{name: visionMdPath, status: setupMissing,
		detail: "missing " + strings.Join(missing, ", ") + " — " + visionAdviceOnly}
}

// scaffoldOffered is -apply's gate for the scaffold question, kept apart
// from the row's status: a repo with its own docs/VISION.md but no
// docs/designs/README.md reads `ok` yet still has a page writeScaffold would
// add. A root VISION.md with no docs/VISION.md is the one exception: that
// repo already chose where its vision lives, so it gets no offer at all.
func scaffoldOffered(dir string) bool {
	if !scaffoldNeedsWrite(dir) {
		return false
	}
	return fileExistsIn(dir, filepath.FromSlash(visionMdPath)) || !fileExistsIn(dir, rootVisionPath)
}

// scaffoldNeedsWrite reports whether either scaffold page is missing from
// dir — the gate proposeSetupFiles checks on the freshly fetched worktree
// before staging anything, the same way claudeMdNeedsUpdate gates the block.
func scaffoldNeedsWrite(dir string) bool {
	return !fileExistsIn(dir, filepath.FromSlash(visionMdPath)) || !fileExistsIn(dir, filepath.FromSlash(designsReadmePath))
}

// writeScaffold writes whichever of the two pages dir is missing, leaving
// an existing one — a human's own — untouched.
func writeScaffold(dir string) error {
	if err := writeIfMissing(filepath.Join(dir, filepath.FromSlash(visionMdPath)), visionMdTemplate); err != nil {
		return err
	}
	return writeIfMissing(filepath.Join(dir, filepath.FromSlash(designsReadmePath)), designsReadmeTemplate)
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
