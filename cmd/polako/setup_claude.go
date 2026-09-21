package main

// The CLAUDE.md half of ticket 4 (docs/plans/setup.md), the second half of
// what setup_files.go's own .gitignore half started: a marked block between
// <!-- polako:begin --> and <!-- polako:end -->, replaced in place on a
// rerun, appended when the file has no such markers, and the file created
// when there is none at all. Text comes from an embedded template, sized to
// the one check command this function detects for the target repo.

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	claudeMdBeginMarker = "<!-- polako:begin -->"
	claudeMdEndMarker   = "<!-- polako:end -->"
)

//go:embed assets/claude-block.md
var claudeMdBlockTemplate string

// claudeMdCheckCommandUnknown is what the block asks for when none of
// claudeMdCheckCommand's probes find anything — a fresh repo whose build
// tool this list doesn't know, left for a human to fill in rather than
// guessed.
const claudeMdCheckCommandUnknown = "(fill this in — no test command detected)"

// claudeMdCheckCommand detects the one command the block tells a human (and
// a run reading CLAUDE.md) to check this repo's work with, tried in this
// fixed order: the more specific a signal, the earlier it's tried, so a repo
// with both a Makefile wrapping `go test` and a bare go.mod gets the
// Makefile's own entry point.
func claudeMdCheckCommand(dir string) string {
	switch {
	case fileExistsIn(dir, "scripts/check.sh"):
		return "./scripts/check.sh"
	case makefileHasTestTarget(dir):
		return "make test"
	case fileExistsIn(dir, "go.mod"):
		return "go test ./..."
	case packageJSONHasTestScript(dir):
		return "npm test"
	case fileExistsIn(dir, "Cargo.toml"):
		return "cargo test"
	case fileExistsIn(dir, "pyproject.toml"):
		return "pytest"
	default:
		return claudeMdCheckCommandUnknown
	}
}

func fileExistsIn(dir, rel string) bool {
	_, err := os.Stat(filepath.Join(dir, rel))
	return err == nil
}

// makefileTestTargetRe matches a target named test at the start of a line —
// never a tab-indented recipe line, which is how a real Makefile spells the
// body of some other target.
var makefileTestTargetRe = regexp.MustCompile(`(?m)^test\s*:`)

// make itself tries these three names in this order (GNU make's own
// manual); a repo can use any of them.
var makefileNames = []string{"Makefile", "makefile", "GNUmakefile"}

func makefileHasTestTarget(dir string) bool {
	for _, name := range makefileNames {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		if makefileTestTargetRe.Match(b) {
			return true
		}
	}
	return false
}

func packageJSONHasTestScript(dir string) bool {
	b, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return false
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(b, &pkg); err != nil {
		return false
	}
	script := strings.TrimSpace(pkg.Scripts["test"])
	if script == "" {
		return false
	}
	// npm init -y's own placeholder: `echo "Error: no test specified" && exit
	// 1`, non-empty but not a real test command — recommending `npm test`
	// for it would just always fail.
	return !strings.Contains(strings.ToLower(script), "no test specified")
}

// claudeMdBlock is the block this run would write today, sized to dir's own
// detected check command. Never ends in a newline — extractClaudeMdBlock
// doesn't either, and the two have to compare equal for a rerun on an
// already-correct CLAUDE.md to come out byte-identical.
func claudeMdBlock(dir string) string {
	return strings.TrimRight(fmt.Sprintf(claudeMdBlockTemplate, claudeMdCheckCommand(dir)), "\n")
}

// extractClaudeMdBlock finds the first marked region in content, markers
// included. The first occurrence, matching mergeClaudeMd's own choice of
// which region to replace when a file somehow carries the markers twice.
func extractClaudeMdBlock(content string) (string, bool) {
	begin := strings.Index(content, claudeMdBeginMarker)
	if begin < 0 {
		return "", false
	}
	rel := strings.Index(content[begin:], claudeMdEndMarker)
	if rel < 0 {
		return "", false
	}
	end := begin + rel + len(claudeMdEndMarker)
	return content[begin:end], true
}

// claudeMdNeedsUpdate reports whether dir's CLAUDE.md is missing the block
// entirely or carries one that differs from what this run would write today
// — a stale check command, most likely, or no CLAUDE.md at all.
func claudeMdNeedsUpdate(dir string) bool {
	existing, err := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	if err != nil {
		return true
	}
	got, ok := extractClaudeMdBlock(string(existing))
	return !ok || got != claudeMdBlock(dir)
}

// mergeClaudeMd inserts block into existing: replaced in place when the
// markers are already there, appended after a blank line when they aren't,
// and returned as the whole file when existing is empty (no CLAUDE.md yet).
// Everything outside the markers is untouched, which is what makes a
// same-for-same replace byte-identical to what was already there.
func mergeClaudeMd(existing, block string) string {
	if begin := strings.Index(existing, claudeMdBeginMarker); begin >= 0 {
		if rel := strings.Index(existing[begin:], claudeMdEndMarker); rel >= 0 {
			end := begin + rel + len(claudeMdEndMarker)
			return existing[:begin] + block + existing[end:]
		}
	}
	if strings.TrimSpace(existing) == "" {
		return block + "\n"
	}
	return strings.TrimRight(existing, "\n") + "\n\n" + block + "\n"
}

// writeClaudeMdBlock merges today's block into dir/CLAUDE.md, creating the
// file when there is none.
func writeClaudeMdBlock(dir string) error {
	path := filepath.Join(dir, "CLAUDE.md")
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	merged := mergeClaudeMd(string(existing), claudeMdBlock(dir))
	return os.WriteFile(path, []byte(merged), 0o644)
}

// setupClaudeMdRow is the read-only report's row for the block. Not
// required, the same as .gitignore: a repo without it still runs `polako
// work` fine, it only means a run has to guess the check command and the
// rest of what the block would have told it.
func setupClaudeMdRow(cfg config) setupRow {
	const name = "CLAUDE.md"
	if !claudeMdNeedsUpdate(cfg.dir) {
		return setupRow{name: name, status: setupOK}
	}
	return setupRow{name: name, status: setupMissing,
		detail: "missing the polako block — `polako setup -apply` proposes it through a PR; " +
			"run `/init` for the rest of CLAUDE.md"}
}
