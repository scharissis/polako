package main

// The CLAUDE.md half of issue #417, the second half of
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
	return claudeMdBlockFor(dir, "")
}

// claudeMdCheckLineRe matches the block's one substituted line, capturing
// whatever command currently sits there.
var claudeMdCheckLineRe = regexp.MustCompile("(?m)^- Check your work with `(.+)`\\.$")

// claudeMdExistingCheckCommand reads the check command already recorded in
// existing's marked block — "" when there is no block, or its check line is
// still the unknown placeholder, either of which means there is nothing
// worth keeping.
func claudeMdExistingCheckCommand(existing string) string {
	block, ok := extractClaudeMdBlock(existing)
	if !ok {
		return ""
	}
	m := claudeMdCheckLineRe.FindStringSubmatch(block)
	if m == nil || m[1] == claudeMdCheckCommandUnknown {
		return ""
	}
	return m[1]
}

// claudeMdBlockFor is the block this run would write into dir's CLAUDE.md
// given its current content. Detection wins when it finds something; when it
// comes back unknown, a human's own fill-in already sitting in existing's
// block is kept rather than reverted to the placeholder — claudeMdCheckCommand
// has no way to see that fill-in, so treating unknown as "nothing to keep"
// would make every rerun undo it.
func claudeMdBlockFor(dir, existing string) string {
	command := claudeMdCheckCommand(dir)
	if command == claudeMdCheckCommandUnknown {
		if kept := claudeMdExistingCheckCommand(existing); kept != "" {
			command = kept
		}
	}
	return strings.TrimRight(fmt.Sprintf(claudeMdBlockTemplate, command), "\n")
}

// findClaudeMdMarkers locates a clean marked region in content: the first
// begin marker, paired with the next end marker after it, but only when no
// second begin marker sits between them. An unpaired begin (no end at all,
// or another begin arriving first) doesn't count as a region — pairing it
// with a later, unrelated end is what let an orphan begin swallow everything
// a rerun had put between them; see mergeClaudeMd's own doc comment.
func findClaudeMdMarkers(content string) (begin, end int, ok bool) {
	begin = strings.Index(content, claudeMdBeginMarker)
	if begin < 0 {
		return 0, 0, false
	}
	rest := content[begin+len(claudeMdBeginMarker):]
	relEnd := strings.Index(rest, claudeMdEndMarker)
	if relEnd < 0 {
		return 0, 0, false
	}
	if relBegin := strings.Index(rest, claudeMdBeginMarker); relBegin >= 0 && relBegin < relEnd {
		return 0, 0, false
	}
	end = begin + len(claudeMdBeginMarker) + relEnd + len(claudeMdEndMarker)
	return begin, end, true
}

// extractClaudeMdBlock finds the first clean marked region in content,
// markers included.
func extractClaudeMdBlock(content string) (string, bool) {
	begin, end, ok := findClaudeMdMarkers(content)
	if !ok {
		return "", false
	}
	return content[begin:end], true
}

// claudeMdNeedsUpdate reports whether dir's CLAUDE.md is missing the block
// entirely or carries one that differs from what this run would write today
// — a stale check command, most likely, or no CLAUDE.md at all.
//
// The comparison normalizes CRLF to LF first: a checkout with git's
// core.autocrlf on (Windows' common default, on a repo with no .gitattributes
// pinning line endings) hands this an existing block with \r\n even though
// claudeMdBlockFor always generates \n — a false "needs update" that then
// writes back exactly what was already there, autocrlf normalizes the stage
// right back to the original bytes, and the commit that follows has nothing
// to commit.
func claudeMdNeedsUpdate(dir string) bool {
	existing, err := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	if err != nil {
		return true
	}
	got, ok := extractClaudeMdBlock(string(existing))
	if !ok {
		return true
	}
	want := claudeMdBlockFor(dir, string(existing))
	return normalizeCRLF(got) != normalizeCRLF(want)
}

func normalizeCRLF(s string) string {
	return strings.ReplaceAll(s, "\r\n", "\n")
}

// mergeClaudeMd inserts block into existing: replaced in place when the
// markers are already there, appended after a blank line when they aren't,
// and returned as the whole file when existing is empty (no CLAUDE.md yet).
// Everything outside the markers is untouched, which is what makes a
// same-for-same replace byte-identical to what was already there.
func mergeClaudeMd(existing, block string) string {
	if begin, end, ok := findClaudeMdMarkers(existing); ok {
		return existing[:begin] + block + existing[end:]
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
	merged := mergeClaudeMd(string(existing), claudeMdBlockFor(dir, string(existing)))
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
		detail: "the polako block is missing or out of date — `polako setup -apply` proposes it through a PR; " +
			"run `/init` for the rest of CLAUDE.md"}
}
