package main

// Consistency tests for the repository itself: the plugin manifest, the
// shipped skill, and the documented flags all have to agree with the code,
// and nothing but a test catches them drifting apart.

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
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

// pluginManifest decodes plugin.json, the single source of truth for the
// version both release tags and the marketplace ref derive from.
func pluginManifest(t *testing.T) struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`
} {
	t.Helper()
	var manifest struct {
		Name        string `json:"name"`
		Version     string `json:"version"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal([]byte(readRepoFile(t, ".claude-plugin", "plugin.json")), &manifest); err != nil {
		t.Fatalf("plugin.json is not valid JSON: %v", err)
	}
	return manifest
}

func pluginManifestVersion(t *testing.T) string {
	t.Helper()
	return pluginManifest(t).Version
}

func TestPluginManifestMatchesTheModule(t *testing.T) {
	manifest := pluginManifest(t)
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

// marketplaceEntry is this repo's own plugin entry. The source is left raw
// because it is either a string or an object depending on how the plugin is
// resolved, and only TestMarketplaceRefIsNotAheadOfTheVersion cares which.
type marketplaceEntry struct {
	Name   string          `json:"name"`
	Source json.RawMessage `json:"source"`
}

func thisPluginEntry(t *testing.T) marketplaceEntry {
	t.Helper()
	var market struct {
		Name    string             `json:"name"`
		Plugins []marketplaceEntry `json:"plugins"`
	}
	if err := json.Unmarshal([]byte(readRepoFile(t, ".claude-plugin", "marketplace.json")), &market); err != nil {
		t.Fatalf("marketplace.json is not valid JSON: %v", err)
	}
	want := moduleName(t)
	for _, p := range market.Plugins {
		if p.Name == want {
			return p
		}
	}
	t.Fatalf("marketplace.json does not list %q (got %+v)", want, market.Plugins)
	return marketplaceEntry{}
}

// The repo doubles as its own marketplace, so `/plugin marketplace add` works
// straight from the clone.
func TestMarketplaceManifestListsThisPlugin(t *testing.T) {
	if entry := thisPluginEntry(t); len(entry.Source) == 0 {
		t.Errorf("plugin %q needs a source", entry.Name)
	}
}

// Installs resolve to the tag this ref names, so the ref is what publishing
// moves. It may lag plugin.json — between the release commit and the publish
// commit it points at the previous release, which is the whole point of
// keeping those two steps apart — but it must never lead it. A ref naming a
// tag that does not exist yet is an install that fails for everyone.
func TestMarketplaceRefIsNotAheadOfTheVersion(t *testing.T) {
	entry := thisPluginEntry(t)
	var source struct {
		Source string `json:"source"`
		Repo   string `json:"repo"`
		Ref    string `json:"ref"`
	}
	if err := json.Unmarshal(entry.Source, &source); err != nil {
		t.Fatalf("plugin source is not the pinned object form: %v\ngot: %s", err, entry.Source)
	}
	if source.Source != "github" || source.Repo == "" {
		t.Errorf("plugin source = %+v, want a github repo so the ref can pin a release", source)
	}
	prefix := entry.Name + "--v"
	rest, ok := strings.CutPrefix(source.Ref, prefix)
	if !ok {
		t.Fatalf("ref %q does not start with %q; that is the tag name `claude plugin tag` creates", source.Ref, prefix)
	}
	pinned, err := parseSemver(rest)
	if err != nil {
		t.Fatalf("ref %q does not name a release: %v", source.Ref, err)
	}
	current, err := parseSemver(pluginManifestVersion(t))
	if err != nil {
		t.Fatalf("plugin.json version: %v", err)
	}
	if slices.Compare(pinned[:], current[:]) > 0 {
		t.Errorf("marketplace ref pins %s but plugin.json is %s: the ref names a tag that does not exist yet",
			rest, pluginManifestVersion(t))
	}
}

// Every release publishes its changelog section as the GitHub release body, so
// a version with no section ships with an empty one.
func TestChangelogHasASectionForThisVersion(t *testing.T) {
	version := pluginManifestVersion(t)
	heading := regexp.MustCompile(`(?m)^## \[?` + regexp.QuoteMeta(version) + `\]?`)
	if !heading.MatchString(readRepoFile(t, "CHANGELOG.md")) {
		t.Errorf("CHANGELOG.md has no `## %s` section; the release workflow publishes it as the release body", version)
	}
}

// `claude plugin validate` rejects any root key it does not know, and that
// validator is what catches plugin.json and the marketplace entry disagreeing
// — the thing `claude plugin tag` refuses to release on. A stray root key
// takes the whole check out, so guard the key set here where the suite can see
// it without shelling out to `claude`.
func TestMarketplaceManifestHasNoUnrecognizedRootKeys(t *testing.T) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal([]byte(readRepoFile(t, ".claude-plugin", "marketplace.json")), &root); err != nil {
		t.Fatalf("marketplace.json is not valid JSON: %v", err)
	}
	accepted := map[string]bool{"name": true, "owner": true, "metadata": true, "plugins": true}
	for key := range root {
		if !accepted[key] {
			t.Errorf("marketplace.json has root key %q; `claude plugin validate` accepts only %v", key, slices.Sorted(maps.Keys(accepted)))
		}
	}

	// Omitting it validates, but only with a warning, and the description is
	// what the marketplace listing shows.
	var meta struct {
		Description string `json:"description"`
	}
	if raw, ok := root["metadata"]; ok {
		if err := json.Unmarshal(raw, &meta); err != nil {
			t.Fatalf("marketplace.json metadata is not an object: %v", err)
		}
	}
	if meta.Description == "" {
		t.Error("marketplace.json needs metadata.description: it is what the marketplace listing shows")
	}
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
	sources, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("listing sources: %v", err)
	}
	// Any receiver, not just the flag package: a subcommand declares its flags
	// on its own FlagSet (fs.StringVar), and those are just as much interface.
	flagName := regexp.MustCompile(`\.\w+Var\(&\w+(?:\.\w+)?, "([a-z-]+)"`)
	var names []string
	for _, src := range sources {
		if strings.HasSuffix(src, "_test.go") {
			continue
		}
		b, err := os.ReadFile(src)
		if err != nil {
			t.Fatalf("reading %s: %v", src, err)
		}
		for _, m := range flagName.FindAllStringSubmatch(string(b), -1) {
			names = append(names, m[1])
		}
	}
	if len(names) < 10 {
		t.Fatalf("only found %d flags in %v — the regexp has gone stale", len(names), sources)
	}

	readme := readRepoFile(t, "README.md")
	for _, name := range names {
		if !strings.Contains(readme, "-"+name) {
			t.Errorf("README.md does not document the -%s flag", name)
		}
	}
}
