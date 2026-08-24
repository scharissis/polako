package main

// Consistency tests for the repository itself: the plugin manifest, the
// shipped skill, and the documented flags all have to agree with the code,
// and nothing but a test catches them drifting apart.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// repoRoot is two levels up from cmd/backlog-drain.
func repoRoot() string { return filepath.Join("..", "..") }

func readRepoFile(t *testing.T, parts ...string) string {
	t.Helper()
	path := filepath.Join(append([]string{repoRoot()}, parts...)...)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(b)
}

// moduleName is the last path element of the go.mod module directive; the
// plugin, the binary and the repository all take their name from it.
func moduleName(t *testing.T) string {
	t.Helper()
	for line := range strings.SplitSeq(readRepoFile(t, "go.mod"), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return filepath.Base(strings.TrimSpace(rest))
		}
	}
	t.Fatal("go.mod has no module directive")
	return ""
}

func TestPluginManifestMatchesTheModule(t *testing.T) {
	var manifest struct {
		Name        string `json:"name"`
		Version     string `json:"version"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal([]byte(readRepoFile(t, ".claude-plugin", "plugin.json")), &manifest); err != nil {
		t.Fatalf("plugin.json is not valid JSON: %v", err)
	}
	if want := moduleName(t); manifest.Name != want {
		t.Errorf("plugin name = %q, want %q to match the module", manifest.Name, want)
	}
	if manifest.Description == "" {
		t.Error("plugin.json needs a description: it is what the marketplace lists")
	}
	// Both release tags derive from this field — backlog-drain--vX.Y.Z for the
	// plugin tooling, vX.Y.Z for `go install` — so it has to be plain semver.
	if !regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(manifest.Version) {
		t.Errorf("plugin version %q is not semver; the release tags derive from it", manifest.Version)
	}
}

// The repo doubles as its own marketplace, so `/plugin marketplace add` works
// straight from the clone.
func TestMarketplaceManifestListsThisPlugin(t *testing.T) {
	var market struct {
		Name    string `json:"name"`
		Plugins []struct {
			Name   string `json:"name"`
			Source string `json:"source"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal([]byte(readRepoFile(t, ".claude-plugin", "marketplace.json")), &market); err != nil {
		t.Fatalf("marketplace.json is not valid JSON: %v", err)
	}
	want := moduleName(t)
	for _, p := range market.Plugins {
		if p.Name == want {
			if p.Source == "" {
				t.Errorf("plugin %q needs a source", want)
			}
			return
		}
	}
	t.Errorf("marketplace.json does not list %q (got %+v)", want, market.Plugins)
}

// Claude namespaces plugin skills as <plugin>:<skill>, so the -skill default
// has to track the plugin name. Getting this wrong is invisible until a run
// exits at 0 turns with "Unknown command".
func TestDefaultSkillIsNamespacedForThePlugin(t *testing.T) {
	if want := moduleName(t) + ":" + skillDir; defaultSkill != want {
		t.Errorf("defaultSkill = %q, want %q", defaultSkill, want)
	}
}

func TestShippedSkillMatchesTheDefaultFlag(t *testing.T) {
	skill := readRepoFile(t, "skills", skillDir, "SKILL.md")

	front, _, ok := strings.Cut(strings.TrimPrefix(skill, "---\n"), "\n---")
	if !ok {
		t.Fatalf("skills/%s/SKILL.md has no YAML frontmatter", skillDir)
	}
	for _, key := range []string{"description:", "argument-hint:", "arguments:"} {
		if !strings.Contains(front, key) {
			t.Errorf("SKILL.md frontmatter is missing %q\ngot:\n%s", key, front)
		}
	}
	// The supervisor passes the issue number as the sole argument, and relies
	// on the PR closing the issue to advance the queue.
	for _, marker := range []string{"$issue", "Closes #$issue"} {
		if !strings.Contains(skill, marker) {
			t.Errorf("SKILL.md must reference %q for the pipeline to work", marker)
		}
	}
}

// Every flag is part of the interface, so every flag has to appear in the
// README. This is the check that catches a new flag shipped undocumented.
func TestReadmeDocumentsEveryFlag(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("reading main.go: %v", err)
	}
	flagName := regexp.MustCompile(`flag\.\w+Var\(&\w+(?:\.\w+)?, "([a-z-]+)"`)
	matches := flagName.FindAllStringSubmatch(string(src), -1)
	if len(matches) < 10 {
		t.Fatalf("only found %d flags in main.go — the regexp has gone stale", len(matches))
	}

	readme := readRepoFile(t, "README.md")
	for _, m := range matches {
		if !strings.Contains(readme, "-"+m[1]) {
			t.Errorf("README.md does not document the -%s flag", m[1])
		}
	}
}
