package main

// Consistency tests for the repository itself: the plugin manifest, the
// shipped skill, and the documented flags all have to agree with the code,
// and nothing but a test catches them drifting apart.

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// repoRoot is two levels up from cmd/polako.
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

// stringFlagDefault reads a flag's default out of its registration rather than
// out of a copy kept in the test, so a test comparing the skill against the
// binary's defaults cannot drift from what the binary actually ships. parseFlags
// and its registrations live in flags.go.
func stringFlagDefault(t *testing.T, name string) string {
	t.Helper()
	registration := regexp.MustCompile(`\.StringVar\(&\w+(?:\.\w+)?, "` + regexp.QuoteMeta(name) + `", "([^"]*)"`)
	m := registration.FindStringSubmatch(readRepoFile(t, "cmd", "polako", "flags.go"))
	if m == nil {
		t.Fatalf("flags.go registers no string flag named %q", name)
	}
	return m[1]
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
	// Both release tags derive from this field — polako--vX.Y.Z for the
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

// planSkillDir now lives in plan.go, beside the `plan` verb that runs it.

func planSkill(t *testing.T) string {
	t.Helper()
	return readRepoFile(t, "skills", planSkillDir, "SKILL.md")
}

// The plan skill is invoked as a slash command with a document to work from, so
// its frontmatter is the whole of its calling convention: the hint is what a
// human reads before typing, and every name under `arguments` is what the body
// interpolates. A body referring to an argument the frontmatter never declared
// gets the literal `$name` instead of the operator's path.
func TestPlanSkillDeclaresItsArguments(t *testing.T) {
	skill := planSkill(t)

	front, body, ok := strings.Cut(strings.TrimPrefix(skill, "---\n"), "\n---")
	if !ok {
		t.Fatalf("skills/%s/SKILL.md has no YAML frontmatter", planSkillDir)
	}
	for _, key := range []string{"description:", "argument-hint:", "arguments:", "disable-model-invocation: true"} {
		if !strings.Contains(front, key) {
			t.Errorf("SKILL.md frontmatter is missing %q\ngot:\n%s", key, front)
		}
	}

	declared := regexp.MustCompile(`(?m)^arguments:\s*\[([^\]]*)\]`).FindStringSubmatch(front)
	if declared == nil {
		t.Fatalf("frontmatter's `arguments:` is not the [name, name] list form:\n%s", front)
	}
	for _, name := range strings.Split(declared[1], ",") {
		if name = strings.TrimSpace(name); name == "" {
			continue
		}
		if !strings.Contains(body, "$"+name) {
			t.Errorf("frontmatter declares argument %q but the body never interpolates $%s,"+
				" so whatever the operator typed for it is silently dropped", name, name)
		}
	}
}

// Everything this skill creates is a proposal, and the label is the entire
// difference between a proposal and queued work: selectableIssues excludes it,
// so an issue filed without it is one an unattended run picks up before any
// human chose it. The spelling is checked against the binary's own constant
// because the two are one contract — the skill applies the label, the
// supervisor derives the queue by excluding it, and neither reads the other.
func TestPlanSkillLabelsEverythingItCreates(t *testing.T) {
	skill := planSkill(t)

	// The full invocation form is the one carrying --title; the other mentions
	// of `gh issue create` in the file are prose about it, not a command.
	invocations := 0
	for _, line := range strings.Split(skill, "\n") {
		if !strings.Contains(line, "gh issue create") || !strings.Contains(line, "--title") {
			continue
		}
		invocations++
		if !strings.Contains(line, "--label "+proposedLabel) {
			t.Errorf("SKILL.md spells an issue-creating command with no `--label %s`:\n\t%s\n"+
				"an unlabelled proposal is one a shift works before anybody approved it", proposedLabel, strings.TrimSpace(line))
		}
	}
	if invocations == 0 {
		t.Errorf("SKILL.md never spells `gh issue create --title ...`, so nothing tells the run"+
			" which form to file proposals in — only that form carries `--label %s`", proposedLabel)
	}

	// Children hang off the epic structurally rather than by a label, which is
	// also what makes the supervisor's container skip work: an issue with
	// sub-issues is never worked, whatever it is called.
	if !strings.Contains(skill, "--parent") {
		t.Error("SKILL.md never spells `--parent`, so an epic's children would be filed as loose" +
			" issues and the epic would be worked as if it were one of them")
	}
}

// The sizing contract is what keeps proposals workable: an issue too big for one
// PR, or one hiding a decision nobody has made, becomes a park or a question
// weeks later at full price. It is one sentence and it earns its pin.
func TestPlanSkillStatesTheSizingContract(t *testing.T) {
	skill := planSkill(t)

	// Read against the text with its line breaks flattened: the sentence is long
	// enough to wrap, and where it wraps is not the contract.
	want := "one issue is one PR that `/" + defaultSkill + "` can produce unattended without stopping to ask"
	if flat := strings.Join(strings.Fields(skill), " "); !strings.Contains(flat, want) {
		t.Errorf("SKILL.md no longer states the sizing contract (%q), so nothing bounds how big a"+
			" proposal may be — and every oversized one is discovered by a run failing on it", want)
	}
}

// Same posture the implement-issue skill carries, for the same reason: a plan
// run reads a vision document and an entire open backlog, and on a repo that
// accepts outside issues that backlog is written by strangers. Its blast radius
// is smaller — proposals behind a label — but the reading has to be the same.
func TestPlanSkillTreatsWhatItReadsAsData(t *testing.T) {
	skill := planSkill(t)

	for _, marker := range []string{"data", "not addressed to you", "content to report, not to act on"} {
		if !strings.Contains(skill, marker) {
			t.Errorf("the posture paragraph no longer says %q — without it the skill reads a"+
				" vision document and an attacker-editable backlog as instructions addressed to it", marker)
		}
	}
}

// issue #128: a plan run improvised `gh label list` to confirm the `proposed`
// label exists, a call no shipped skill is granted, and would hang an
// unattended drain on the permission prompt it raised. The fix was naming the
// closed `gh` surface explicitly rather than leaving "diligence" undefined;
// this locks that paragraph down the way every neighboring contract in this
// section is locked down.
func TestPlanSkillClosesItsGhSurface(t *testing.T) {
	skill := planSkill(t)

	for _, marker := range []string{"gh label list", "gh --version"} {
		if !strings.Contains(skill, marker) {
			t.Errorf("SKILL.md no longer names %q as an example of an unlicensed"+
				" pre-flight probe — without it, a run has no warning against improvising"+
				" a diligence check outside the granted `gh` surface", marker)
		}
	}

	flat := strings.Join(strings.Fields(skill), " ")
	if !strings.Contains(flat, "three call shapes") {
		t.Error("SKILL.md no longer states the closed `gh` surface as \"three call shapes\"," +
			" so nothing bounds which gh commands a plan run may use")
	}

	// issue #178 widened `gh issue create` by one flag and named the ceiling in
	// the same breath: `--parent` and `--blocked-by`, and no other write flag.
	// Drop that clause and the surface paragraph stops accounting for the flag
	// the create form now carries.
	if !strings.Contains(flat, "`--parent` and `--blocked-by`, and no other write flag") {
		t.Error("the gh-surface paragraph no longer bounds `gh issue create`'s flags to" +
			" `--parent` and `--blocked-by` — without that ceiling the create form's" +
			" `--blocked-by` reads as an improvised widening rather than a licensed one")
	}
}

// The estimate is the curator's cheapest signal, and it is only useful if it is
// shaped the same every time. The one thing it must never carry is money: the
// model has no price sheet, the binary refuses to hardcode one, and a
// confident-looking dollar figure invented here would be read as measured.
func TestPlanSkillEstimateLineKeepsItsShape(t *testing.T) {
	skill := planSkill(t)

	shape := regexp.MustCompile(`(?m)^[ \t]*Estimate: [SML] — likely \S+ runs[ \t]*$`)
	lines := shape.FindAllString(skill, -1)
	if lines == nil {
		t.Fatalf("SKILL.md spells no `Estimate: <S|M|L> — likely <n> runs` line, so proposals" +
			" carry no size and a curator cannot tell the cheap wins from the big bets")
	}
	for _, line := range lines {
		if strings.Contains(line, "$") {
			t.Errorf("the estimate line quotes money:\n\t%s\nsizes are the model's judgement;"+
				" costs come from run history via `%s stats` and nowhere else", strings.TrimSpace(line), moduleName(t))
		}
	}
}

// The one guarantee that makes proposals safe to file unattended: this run
// creates labelled issues and does nothing else. No commits, no pushes, no pull
// requests, and no touching threads that already exist — an edit could strip a
// `proposed` label as easily as apply one, which is self-approval. The skill
// text is not the enforcement (the allowlist is, once the plan verb ships), but
// a skill that talks about editing issues is one that will try.
func TestPlanSkillCreatesIssuesAndNothingElse(t *testing.T) {
	skill := planSkill(t)

	for _, forbidden := range []string{"gh issue edit", "gh issue comment", "gh pr", "git commit", "git push"} {
		if strings.Contains(skill, forbidden) {
			t.Errorf("SKILL.md spells %q; a plan run's entire write surface is `gh issue create`"+
				" plus the scratch body file it deletes", forbidden)
		}
	}
}

// issue #178: a plan run works out the child dependency order and then throws
// the answer away, expressing it only as creation order plus prose that names
// children by ordinal ("the first child of this epic") — a lookup a reader can
// get wrong in silence. The fix declares the order: `--blocked-by` on the child
// create call, a fixed-shape `Depends on:` line in the body, and prose that
// names issue numbers only. These three lines carry that contract.
func TestPlanSkillDeclaresDependencyOrder(t *testing.T) {
	skill := planSkill(t)

	if !strings.Contains(skill, "--blocked-by") {
		t.Error("SKILL.md never spells `--blocked-by`, so a child's blockers are declared" +
			" nowhere on GitHub and the dependency order is prose the drain cannot read")
	}

	// The body's dependency line has one shape so a curator can scan it and
	// repo_test.go can pin it: numbers first, then what each supplies.
	shape := regexp.MustCompile(`(?m)^[ \t]*Depends on: #\d+(, #\d+)* — .+$`)
	if !shape.MatchString(skill) {
		t.Error("SKILL.md spells no `Depends on: #N[, #N] — <what each supplies>` line, so the" +
			" body's dependency line has no fixed shape to hold proposals to")
	}

	// The ordinal form is retired by name — an ordinal is a lookup that can be
	// got wrong, a number cannot — so the skill has to say so, not just stop
	// using it.
	for _, marker := range []string{"never an ordinal", "the first child of this epic"} {
		if !strings.Contains(skill, marker) {
			t.Errorf("SKILL.md no longer retires the ordinal form by naming it (%q); without that"+
				" a run falls back to 'the first child of this epic', the silent lookup #178 was about", marker)
		}
	}
}

// The review gate runs as a forked agent that starts in the session's cwd, not
// in the worktree — nothing in the skill ever moves it there. Invoked with no
// target it reviews whatever that cwd holds instead, which on a clean default
// branch means reviewing an already-merged change and writing the fixes there.
// Naming the branch is the entire defence, and it is one word easy to drop.
func TestReviewGateNamesTheBranch(t *testing.T) {
	skill := readRepoFile(t, "skills", skillDir, "SKILL.md")

	invoked := false
	for _, line := range strings.Split(skill, "\n") {
		if !strings.Contains(line, "/code-review") {
			continue
		}
		invoked = true
		if !strings.Contains(line, "issue-$issue") {
			t.Errorf("the review gate names no branch, so it would review the main checkout"+
				" and apply its fixes there — add the issue-$issue target:\n\t%s", strings.TrimSpace(line))
		}
	}
	if !invoked {
		t.Error("SKILL.md no longer invokes /code-review; the mandatory review gate before a PR is gone")
	}
}

// Naming the branch aims what the review diffs. It does not aim where the
// review's own forked agent and the finder subagents under it read and write:
// they start in the session's cwd, the main checkout, and this skill never
// moves it. Left unnamed, they open the default-branch copy of every file the
// branch changed — findings judged against the wrong body, and a fix written
// there landing outside the branch (issue #219). The worktree has to be named
// in the same breath as the branch, and like the branch it is one token easy
// to drop.
func TestReviewGateNamesTheWorktree(t *testing.T) {
	skill := readRepoFile(t, "skills", skillDir, "SKILL.md")

	// Flattened, and checked over a window rather than one physical line: a
	// reflow that splits the branch token from the worktree token must not
	// fail a skill that is still correct — the same reason
	// TestReviewGateDoesNotAutoApplyFixes moved off a line window.
	flat := strings.Join(strings.Fields(skill), " ")
	// The level is no longer literal — issue #225 scales it to the diff size —
	// so match `/code-review <level> issue-$issue` for any single level token.
	loc := regexp.MustCompile(`/code-review \S+ issue-\$issue`).FindStringIndex(flat)
	if loc == nil {
		t.Fatal("SKILL.md no longer invokes `/code-review <level> issue-$issue`;" +
			" the mandatory review gate before a PR is gone")
	}
	window := flat[loc[0]:]
	if len(window) > 320 {
		window = window[:320]
	}
	if !strings.Contains(window, "<worktree>") {
		t.Errorf("the review gate invokes /code-review without naming <worktree> alongside the"+
			" branch, so its forked agent and the finder subagents under it stay in the session"+
			" cwd (the main checkout) and read the default-branch copy of the changed files"+
			" rather than the branch's (issue #219):\n\t%s", window)
	}
}

// Issue #225: the gate used to ask for `high` on every diff, so a one-line
// docs fix bought the same broad, full-fan-out review sweep as a large
// refactor. The level now scales to the change size, measured from the diff
// that step d already has in hand. A regression back to a hardcoded level is
// invisible except here — and it is the whole point of the issue.
func TestReviewGateScalesLevelToDiffSize(t *testing.T) {
	skill := readRepoFile(t, "skills", skillDir, "SKILL.md")

	if !strings.Contains(skill, "diff --stat") {
		t.Error("the review gate no longer sizes the change with `git diff --stat` before" +
			" choosing a review level — issue #225's fixed cost per PR is back")
	}
	for _, level := range []string{"`medium`", "`high`"} {
		if !strings.Contains(skill, level) {
			t.Errorf("the review gate no longer names %s as one of the levels it scales"+
				" between — the scaling rule from issue #225 is gone or hardcoded again", level)
		}
	}
}

// Naming the branch is only half of aiming the review. It resolves that
// branch's base from the *local* default branch, and a drain never pulls — it
// merges on GitHub — so that ref falls one commit behind per merged PR. Review
// against a stale one and somebody else's merged PR is inside the diff, where
// a finding "fixed" against it lands the fix inside this branch's own commits.
// The refresh has to come before the invocation, so check the order too.
func TestReviewGateRefreshesTheBaseBeforeReviewing(t *testing.T) {
	skill := readRepoFile(t, "skills", skillDir, "SKILL.md")

	refresh := strings.Index(skill, "merge --ff-only")
	if refresh < 0 {
		t.Fatal("the review gate never brings the local default branch up to date, so it will" +
			" review this branch against a base that is one commit stale per merged PR —" +
			" add a `git merge --ff-only` against the origin ref before the invocation")
	}
	if review := strings.Index(skill, "/code-review"); review >= 0 && refresh > review {
		t.Error("the base refresh comes after the review is invoked, which is too late to" +
			" affect what it diffs against — move it before the invocation")
	}
}

// The gate's resumability (issue #216) depends on the review returning before
// any fix is applied, so it can be checkpointed on its own — a death while
// --fix is still editing files is what left #216's gate with nothing to
// resume from. `--fix` coming back onto this line would silently regress
// that: the invocation would still name the branch (passing
// TestReviewGateNamesTheBranch above) while quietly losing the resumability
// this test exists to protect.
func TestReviewGateDoesNotAutoApplyFixes(t *testing.T) {
	skill := readRepoFile(t, "skills", skillDir, "SKILL.md")

	// Checked over the whole document rather than a window around the
	// invocation: a window sized to today's wording can miss a rewrap that
	// pushes `--fix` in from either direction, and `/code-review` names the
	// branch on exactly one line (TestReviewGateNamesTheBranch), so a
	// whole-document check can't accidentally cover a second, unrelated
	// invocation either. Every legitimate mention of `--fix` in this file
	// documents its absence — if that stops being true, name the new
	// phrasing here too, deliberately, rather than widen a line window.
	legitimate := []string{"no `--fix`", "Leaving `--fix` off"}
	flat := strings.Join(strings.Fields(skill), " ")
	for _, phrase := range legitimate {
		flat = strings.ReplaceAll(flat, strings.Join(strings.Fields(phrase), " "), "")
	}
	if strings.Contains(flat, "--fix") {
		t.Error("SKILL.md mentions --fix somewhere other than the phrasings this test knows" +
			" document its deliberate absence — if the review invocation now passes --fix, that" +
			" applies findings before this skill can checkpoint them, which is issue #216's" +
			" original incident; if it's a new legitimate mention, add its exact phrasing to" +
			" this test's exclusion list")
	}
}

// "Reviewed through" is the entire resumability contract issue #216 added —
// the one thing a resumed run reads to decide whether the expensive review
// sweep needs repeating. Losing it silently turns every resume back into a
// full re-review — the exact cost the issue was filed about — without any
// test failing to say so.
func TestReviewGateRecordsResumeMarkers(t *testing.T) {
	skill := readRepoFile(t, "skills", skillDir, "SKILL.md")

	// >= 2, not just present: step a reads it (the resume check) and step c
	// has to separately say to write it, or the marker step a looks for is
	// never actually produced by anything.
	if n := strings.Count(skill, "Reviewed through"); n < 2 {
		t.Errorf("SKILL.md mentions %q only %d time(s) — step a's resume check reads it and"+
			" step c has to separately write it, so fewer than two mentions means either the"+
			" read or the write side of this marker is gone, and issue #216's"+
			" full-re-review-on-every-death regresses silently", "Reviewed through", n)
	}
}

// The base refresh has to be unconditional — not nested under the resume
// shortcut, and not something the code-review-unavailable fallback could
// read as skippable — or a resumed run, or one using the substitute review
// pass, compares against a base that may be a merged PR stale. An earlier
// draft of this gate put the refresh after the resume check and had to patch
// the ambiguity with reminder sentences in two places; the fix was to make
// the refresh run first, always, before the resume decision exists to skip
// anything. Pin that ordering directly rather than the reminder prose, since
// the prose was the symptom, not the guarantee.
func TestReviewGateFallbackStillRefreshesTheBase(t *testing.T) {
	skill := readRepoFile(t, "skills", skillDir, "SKILL.md")

	fallback := strings.Index(skill, "not invocable in this session")
	if fallback < 0 {
		t.Fatal("SKILL.md no longer describes a fallback for when the code-review skill is" +
			" not invocable — Phase 3's gate needs one, since it is otherwise a hard stop")
	}
	refresh := strings.Index(skill, "merge --ff-only")
	resumeCheck := strings.Index(skill, "Check for a resume point")
	if refresh < 0 || resumeCheck < 0 || !(refresh < resumeCheck && resumeCheck < fallback) {
		t.Errorf("the base refresh (%q at %d), the resume-point check (%q at %d) and the"+
			" code-review-unavailable fallback (at %d) are no longer in that order — the refresh"+
			" has to run before the resume check exists to decide anything is skippable, or a"+
			" resumed run (or the fallback's substitute review) can compare against a stale base",
			"merge --ff-only", refresh, "Check for a resume point", resumeCheck, fallback)
	}
}

// Issue #154: diff-scoped review judges the change, never the file it lands in,
// so a file accretes without bound one passing PR at a time. The gate's
// accretion step is the one pass that looks. Its load-bearing part is the
// "repo median OR an absolute ceiling, whichever is lower" rule — a
// simplification to a relative-only check reads as harmless and silently
// restores the bootstrapping flaw (on a young repo the machine wrote, the
// median it measures against is the machine's own accreted norm). All three
// measures matter: comment density is the one that otherwise goes unwatched.
func TestReviewGateChecksForAccretion(t *testing.T) {
	skill := readRepoFile(t, "skills", skillDir, "SKILL.md")

	// Flattened: these markers read across line wraps in SKILL.md's prose, and
	// a reflow must not fail a check that is still correct.
	flat := strings.Join(strings.Fields(skill), " ")
	if !strings.Contains(flat, "Accretion check") {
		t.Fatal("SKILL.md's review gate no longer has an accretion-check step — a file the" +
			" branch touches can now grow past the repo's norm one passing PR at a time with" +
			" nothing looking (issue #154)")
	}
	for _, measure := range []string{"File length", "Function/unit length", "Comment density"} {
		if !strings.Contains(flat, measure) {
			t.Errorf("the accretion check no longer names %q as one of its three measures —"+
				" issue #154 asks for all three, and comment density is the one that otherwise"+
				" goes unwatched", measure)
		}
	}
	if !strings.Contains(flat, "absolute ceiling") || !strings.Contains(flat, "whichever is lower") {
		t.Error("the accretion check no longer bounds each measure by `an absolute ceiling," +
			" whichever is lower` — a relative-only check reads as a harmless simplification and" +
			" silently restores issue #154's bootstrapping flaw, so pin the ceiling here")
	}
}

// PLAN.md is the resume point: a run killed mid-implementation is restarted
// from it, and a plan written afterwards resumes nothing. So the ordering is
// the promise, not the file. Phase 3's own heading carries the gate because a
// resuming run reads that heading and decides from it whether it may start.
func TestPlanIsWrittenBeforeImplementation(t *testing.T) {
	skill := readRepoFile(t, "skills", skillDir, "SKILL.md")

	plan := strings.Index(skill, "## Phase 2 — Plan")
	implement := strings.Index(skill, "## Phase 3 — Implement")
	if plan < 0 || implement < 0 {
		t.Fatalf("SKILL.md needs both a `## Phase 2 — Plan` and a `## Phase 3 — Implement`"+
			" heading (found them at %d and %d)", plan, implement)
	}
	if plan > implement {
		t.Error("Phase 3 — Implement is written before Phase 2 — Plan, so a run reading the" +
			" skill in order implements before it has a plan to resume from")
	}
	// Spelled through the supervisor's own constant, which is the other half of
	// the contract: it discounts this file when it says what a parked run left
	// on disk, and a rename here that left that behind would have every park
	// claiming work was left behind.
	if !strings.Contains(skill, "Write "+planFile+" BEFORE implementing") {
		t.Errorf("Phase 2 no longer tells the run to write %s before implementing —"+
			" without it a clear-looking issue gets implemented with no resume point", planFile)
	}
	if heading, _, _ := strings.Cut(skill[implement:], "\n"); !strings.Contains(heading, planFile) {
		t.Errorf("Phase 3's heading no longer gates on PLAN.md existing, so a resumed run"+
			" cannot tell from it whether planning happened:\n\t%s", heading)
	}
}

// issue #275: a run testing whether PLAN.md existed reached for a bare `ls` —
// a Bash command outside the allowlist — because SKILL.md said "If PLAN.md
// doesn't exist" without ever saying how to test that, and the run had no way
// to recover from the resulting permission prompt. Read already answers the
// question (a missing file is a normal, handleable error) and needs no new
// grant, so this pins that the skill names the tool explicitly rather than
// leaving the model to improvise one.
func TestPlanExistenceCheckUsesReadNotBash(t *testing.T) {
	skill := readRepoFile(t, "skills", skillDir, "SKILL.md")

	if !strings.Contains(skill, "Read-ing `<worktree>/"+planFile+"`") {
		t.Errorf("SKILL.md no longer tells the run to test %s's existence by Read-ing it directly —"+
			" without that, \"if PLAN.md doesn't exist\" names no tool and a run can reach for an"+
			" unlisted Bash existence check instead (issue #275)", planFile)
	}
	for _, forbidden := range []string{"`ls`", "`test -f`", "`[ -f ]`"} {
		if !strings.Contains(skill, forbidden) {
			t.Errorf("SKILL.md no longer names %s as an example of the Bash existence check to avoid"+
				" when testing whether PLAN.md exists", forbidden)
		}
	}
}

// Under headless `claude -p` — the only way the supervisor invokes the skill —
// the model ending its turn is the process exiting. So a run that stops to wait
// on something does not pause, it terminates: exit 0, no error, work left
// uncommitted and no PR. That lands on the drain's default branch, which reads
// it as "Claude decided nothing" and parks the issue — correctly, on a premise
// it has no way to see through, since a run that paused and a run that decided
// nothing look identical from outside. No guard can tell them apart, which
// leaves the skill knowing what kind of process it is as the only defence.
func TestSkillSaysTheRunGetsOneTurn(t *testing.T) {
	skill := readRepoFile(t, "skills", skillDir, "SKILL.md")

	if !strings.Contains(skill, "one turn") {
		t.Error("SKILL.md never tells the run it gets one turn, so nothing stops it ending a" +
			" turn to wait on work it means to pick up later — under `claude -p` that exits" +
			" the process with the branch unpushed and no PR, and the issue gets parked")
	}
	if !strings.Contains(skill, "Never end a turn intending to resume") {
		t.Error("SKILL.md no longer forbids ending a turn intending to resume; stating that the" +
			" run is one-shot is not the same as saying what not to do about it")
	}

	// The failure is a shape, not a mechanism. A Monitor, a background job meant
	// to be polled on a later turn, a scheduled wake-up and an unawaited subagent
	// all end the same way, and a rule that named only the one that happened to
	// bite first would leave the rest wide open.
	var named []string
	for _, mechanism := range []string{"Monitor", "wake-up", "background", "subagent"} {
		if strings.Contains(skill, mechanism) {
			named = append(named, mechanism)
		}
	}
	if len(named) < 2 {
		t.Errorf("the one-turn rule names %v, which reads as a ban on one mechanism rather than"+
			" on ending a turn with work outstanding — every way of deferring to a later turn"+
			" fails identically, so name more than one", named)
	}
}

// issue #180: a run given an issue with an open, unmet blocker started
// implementing anyway, because nothing looked. The fix reads `blockedBy` in
// the same `gh issue view` call Phase 0 already makes — no new call, no wider
// grant — and stops before Phase 1 creates a worktree or branch for an issue
// an open blocker should have held back.
func TestPhase0ReadsBlockedBy(t *testing.T) {
	skill := readRepoFile(t, "skills", skillDir, "SKILL.md")

	if !strings.Contains(skill, "gh issue view $issue --json number,title,state,body,comments,blockedBy") {
		t.Error("Phase 0's issue read no longer asks for blockedBy, so an issue with an open" +
			" blocker looks identical to one with none")
	}
}

// The check has to run before anything is created, or a stop here is too
// late to matter: a worktree or branch already exists for an issue an open
// blocker should have held back, and now there is something to clean up.
func TestPhase0ChecksBlockedByBeforeAnythingIsCreated(t *testing.T) {
	skill := readRepoFile(t, "skills", skillDir, "SKILL.md")

	blockerCheck := strings.Index(skill, "Check `blockedBy` before Phase 1 creates anything")
	worktreeAdd := strings.Index(skill, "git worktree add")
	if blockerCheck < 0 {
		t.Fatal("SKILL.md no longer checks blockedBy before Phase 1 creates anything")
	}
	if worktreeAdd < 0 || blockerCheck > worktreeAdd {
		t.Error("the blockedBy check does not come before `git worktree add`, so a run could" +
			" create a worktree for an issue an open blocker should have stopped")
	}

	// An open blocker has to ask, not proceed — reusing the one stop shape the
	// skill already has, never inventing a fourth.
	for _, marker := range []string{"An open blocker not yet raised", "and stop. Do not create the worktree or the branch"} {
		if !strings.Contains(skill, marker) {
			t.Errorf("SKILL.md no longer says %q — without it an open blocker reads as something"+
				" to note rather than something that stops the run before it creates anything", marker)
		}
	}
}

// The label command is allowlisted per run by issueLabelTools, as a prefix with
// the issue number ahead of the flag. SKILL.md is where that command is
// actually spelled, so the grant and the spelling are one contract with two
// halves. Reordering the arguments does not fail loudly: the prefix stops
// matching, Claude raises a permission prompt, and an unattended run has nobody
// to answer it — so the run hangs with its question still unposted, which is
// the failure this whole label exists to prevent.
func TestLabelCommandsInTheSkillMatchTheGrantedPrefixes(t *testing.T) {
	const issue = 42
	skill := strings.ReplaceAll(readRepoFile(t, "skills", skillDir, "SKILL.md"), "$issue", strconv.Itoa(issue))

	var granted []string
	for _, tool := range strings.Split(issueLabelTools(issue), ",") {
		prefix := strings.TrimSuffix(strings.TrimPrefix(tool, "Bash("), ":*)")
		granted = append(granted, prefix)
	}

	var seen []string
	for _, cmd := range regexp.MustCompile("gh issue edit [^`\n]*").FindAllString(skill, -1) {
		cmd = strings.TrimRight(cmd, " .`")
		seen = append(seen, cmd)
		if !slices.ContainsFunc(granted, func(p string) bool { return strings.HasPrefix(cmd, p) }) {
			t.Errorf("SKILL.md spells a label command the run is not granted:\n\t%s\n"+
				"issueLabelTools grants only these prefixes: %v\n"+
				"any other form raises a permission prompt nobody is there to answer", cmd, granted)
		}
	}

	// Presence matters as much as spelling: raising the flag is the only thing
	// that tells the supervisor a question was asked rather than nothing
	// produced, and lowering it is the only thing that lets the drain carry on
	// once the question has an answer.
	for _, flag := range []string{"--add-label", "--remove-label"} {
		want := fmt.Sprintf("gh issue edit %d %s %s", issue, flag, awaitingAnswerLabel)
		if !slices.Contains(seen, want) {
			t.Errorf("SKILL.md never spells %q; found only %v", want, seen)
		}
	}
}

// The close command (#210's fourth ending) is allowlisted per run by
// issueCloseTool, the same shape issueLabelTools already is: a prefix pinned
// to the issue number. SKILL.md is where the command is actually spelled, so
// as with the label commands, the grant and the spelling are one contract
// with two halves — a reordering here raises a permission prompt nobody is
// there to answer, and the run hangs rather than closing or falling back to
// a park.
func TestCloseCommandInTheSkillMatchesTheGrantedPrefix(t *testing.T) {
	const issue = 42
	skill := strings.ReplaceAll(readRepoFile(t, "skills", skillDir, "SKILL.md"), "$issue", strconv.Itoa(issue))

	prefix := strings.TrimSuffix(strings.TrimPrefix(issueCloseTool(issue), "Bash("), ":*)")

	var seen []string
	for _, cmd := range regexp.MustCompile("gh issue close [^`\n]*").FindAllString(skill, -1) {
		cmd = strings.TrimRight(cmd, " .`")
		seen = append(seen, cmd)
		if !strings.HasPrefix(cmd, prefix) {
			t.Errorf("SKILL.md spells a close command the run is not granted:\n\t%s\n"+
				"issueCloseTool grants only this prefix: %q\n"+
				"any other form raises a permission prompt nobody is there to answer", cmd, prefix)
		}
	}
	if len(seen) == 0 {
		t.Errorf("SKILL.md never spells %q — without it the fourth ending has no way to act", prefix)
	}
}

// The skill names the branch and the supervisor finds the PR by that head
// branch, never asking the skill what it chose. Rename either half alone and
// every PR the other half goes looking for is simply absent — which reads as
// "the run produced nothing" and parks a perfectly good issue.
func TestSkillBranchNameMatchesTheBranchPrefixDefault(t *testing.T) {
	prefix := stringFlagDefault(t, "branch-prefix")
	skill := readRepoFile(t, "skills", skillDir, "SKILL.md")
	if want := prefix + "$issue"; !strings.Contains(skill, want) {
		t.Errorf("SKILL.md never names branch %q, but -branch-prefix defaults to %q and that"+
			" is the head branch the supervisor looks a PR up by", want, prefix)
	}
}

// The PR body is the only account of the run a human reads before merging, and
// its last line is what advances the queue: the merge auto-closes the issue,
// and a closed issue is how the next drain sees the work as done. Left off, the
// issue stays open and the backlog never drains past it.
func TestPRBodyKeepsItsSectionsAndClosingLine(t *testing.T) {
	skill := readRepoFile(t, "skills", skillDir, "SKILL.md")

	for _, heading := range []string{"## Summary", "## Evidence", "## Design decisions", "## Scope", "## Verification"} {
		if !strings.Contains(skill, heading) {
			t.Errorf("SKILL.md no longer asks the PR body for a %q section", heading)
		}
	}

	// Most PRs change nothing a human looks at, so Evidence has to stay
	// conditional — pinned separately so a later edit cannot quietly make it
	// mandatory and breed filler (a pasted `go test` tail dressed up as a demo).
	if !strings.Contains(skill, "Omit this section entirely when the change alters nothing a human sees") {
		t.Error("SKILL.md no longer tells the run to omit ## Evidence when the change has no visible artifact")
	}

	closes := func(line string) bool {
		return strings.Contains(line, "Closes #$issue") && strings.Contains(line, "End the body with")
	}
	if !slices.ContainsFunc(strings.Split(skill, "\n"), closes) {
		t.Error("SKILL.md no longer ends the PR body with `Closes #$issue`; without it a merge" +
			" leaves the issue open and the next drain picks the same issue up again")
	}
}

// issue #272: the skill writes two things a human reads before acting — the PR
// body at the merge gate, and a blocked run's question on the thread — and the
// house style for both is stated once in polako's CLAUDE.md, which is not
// loaded in the repos this skill runs in. So the skill carries its own copy,
// the same requirement CLAUDE.md now puts on every shipped skill. Lose it and
// the next edit drifts the tone back toward the memo voice with nothing to
// catch it.
func TestSkillCarriesTheHouseStyle(t *testing.T) {
	skill := readRepoFile(t, "skills", skillDir, "SKILL.md")

	flat := strings.Join(strings.Fields(skill), " ")
	for _, marker := range []string{
		"CLAUDE.md is not loaded",
		"terse, plain, informal English",
		"active voice, no rhetorical flourish",
		"reads in a minute",
	} {
		if !strings.Contains(flat, marker) {
			t.Errorf("SKILL.md's house-style copy no longer says %q — the skill runs where"+
				" polako's CLAUDE.md is not loaded, so this is the only copy of the rule", marker)
		}
	}
}

// issue #272: the PR body spec named five sections but bounded only ## Summary
// ("2–4 sentences"). The other four were unbounded, so the reviewer at the
// merge gate read whatever the run felt like writing. Every section now
// carries a length budget on or under its heading; drop one and that section
// is open-ended again.
func TestPRBodySectionsAllHaveBudgets(t *testing.T) {
	skill := readRepoFile(t, "skills", skillDir, "SKILL.md")

	start := strings.Index(skill, "## Summary — what changed and why")
	end := strings.Index(skill, "End the body with `Closes #$issue`")
	if start < 0 || end < 0 || end < start {
		t.Fatal("SKILL.md's PR body spec no longer runs from `## Summary` to the `Closes #$issue` line")
	}
	spec := skill[start:end]

	// A budget is a length word: a sentence/line count, "one screen", "a
	// minute". Each section needs one before the next heading starts.
	sections := []string{"## Summary", "## Evidence", "## Design decisions", "## Scope", "## Verification"}
	budget := regexp.MustCompile(`sentence|line each|one to|screen|minute|paragraph|at most|no more than`)
	for i, h := range sections {
		from := strings.Index(spec, h)
		if from < 0 {
			t.Errorf("the PR body spec no longer names the %q section", h)
			continue
		}
		to := len(spec)
		if i+1 < len(sections) {
			if n := strings.Index(spec[from:], sections[i+1]); n >= 0 {
				to = from + n
			}
		}
		if !budget.MatchString(spec[from:to]) {
			t.Errorf("the %q section of the PR body spec carries no length budget — it was"+
				" unbounded before issue #272 and the reviewer read whatever the run wrote:\n\t%s",
				h, strings.TrimSpace(spec[from:to]))
		}
	}
}

// issue #272: the question path said "terse, simple English" and stopped
// there. With no shape a blocked run could post several paragraphs when the
// human needs three things and a cap: what is blocked, what the run needs to
// know, and what each answer would change — capped at one screen. This pins
// that shape in the "Asking a question" section.
func TestQuestionPathHasAShape(t *testing.T) {
	skill := readRepoFile(t, "skills", skillDir, "SKILL.md")

	ask := strings.Index(skill, "## Asking a question")
	phase0 := strings.Index(skill, "## Phase 0")
	if ask < 0 || phase0 < 0 || phase0 < ask {
		t.Fatal("SKILL.md no longer has an `## Asking a question` section before Phase 0")
	}
	flat := strings.Join(strings.Fields(skill[ask:phase0]), " ")
	for _, marker := range []string{"what is blocked", "what you need to know", "what each answer would change", "one screen"} {
		if !strings.Contains(flat, marker) {
			t.Errorf("the question path no longer tells a blocked run to shape its question"+
				" around %q — without the shape it posts several paragraphs for a one-line answer", marker)
		}
	}
}

// The release pipeline is workflows coupled by filename: cut-release.yml
// watches plugin.json for the version changing and dispatches release.yml on
// the tag it pushes; release.yml dispatches smoke.yml and ci.yml;
// start-release.yml dispatches ci.yml on the release PR it opens. Every
// dispatch exists because GitHub suppresses workflow runs for events a
// workflow's own token caused, and every one resolves its target by filename
// at run time — so a renamed or restructured workflow does not fail anything,
// it just quietly never runs, and the merge that should have cut a release
// does nothing at all.
func TestReleaseWorkflowsStayCoupled(t *testing.T) {
	couplings := map[string][]string{
		// The path filter is how a version bump — and nothing else — starts a
		// release; workflow_dispatch is the documented recovery re-run.
		"cut-release.yml": {".claude-plugin/plugin.json", "release.yml", "workflow_dispatch"},
		// workflow_dispatch is what cut-release.yml calls, since its tag push
		// cannot trigger the push trigger.
		"release.yml": {"workflow_dispatch", "smoke.yml", "ci.yml"},
		"smoke.yml":   {"workflow_dispatch", "scripts/smoke.sh"},
		// The release commit convention is spelled here as well as in the
		// README; ci.yml is dispatched onto the PR branch it opens.
		"start-release.yml": {"ci.yml", "chore(release):"},
		"ci.yml":            {"workflow_dispatch"},
	}
	for file, wants := range couplings {
		content := readRepoFile(t, ".github", "workflows", file)
		for _, want := range wants {
			if !strings.Contains(content, want) {
				t.Errorf(".github/workflows/%s no longer mentions %q — the release pipeline is held together by that name and breaks silently without it", file, want)
			}
		}
	}
}

// A commit SHA is the only reference to a third-party action that says
// exactly what will run. A major tag is whatever its owner last force-moved
// it to, and here it would be moved onto workflows carrying `contents: write`
// and `actions: write`. The other half of the pin is .github/dependabot.yml:
// an exact reference with nothing to bump it goes stale in silence, so this
// checks both, since either alone is worse than the pair.
func TestWorkflowActionsArePinnedToSHAs(t *testing.T) {
	dir := filepath.Join(repoRoot(), ".github", "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	isSHA := regexp.MustCompile(`^[0-9a-f]{40}$`)
	pins := 0
	for _, e := range entries {
		if ext := filepath.Ext(e.Name()); e.IsDir() || (ext != ".yml" && ext != ".yaml") {
			continue
		}
		for line := range strings.SplitSeq(readRepoFile(t, ".github", "workflows", e.Name()), "\n") {
			ref, ok := strings.CutPrefix(strings.TrimPrefix(strings.TrimSpace(line), "- "), "uses: ")
			if !ok {
				continue
			}
			ref, _, _ = strings.Cut(ref, " ") // drop the trailing `# vX.Y.Z`
			if strings.HasPrefix(ref, "./") {
				continue // an action in this repository is not a supply chain
			}
			if _, version, found := strings.Cut(ref, "@"); !found || !isSHA.MatchString(version) {
				t.Errorf(".github/workflows/%s uses %q — a moving tag is whatever it points at on the day a release runs; pin the commit SHA, with the version as a trailing comment so Dependabot can bump it", e.Name(), ref)
				continue
			}
			pins++
		}
	}
	if pins == 0 {
		t.Error("no pinned actions found under .github/workflows — this test has stopped checking anything")
	}
	if !strings.Contains(readRepoFile(t, ".github", "dependabot.yml"), "github-actions") {
		t.Error(".github/dependabot.yml no longer updates the github-actions ecosystem; the SHA pins above have nothing to bump them and go stale silently")
	}
}

// Every flag is part of the interface, so every flag has to appear under
// docs/ — work's and status's in reference.md, stats's beside the report it
// describes in run-data.md. This is the check that catches a new flag shipped
// undocumented. The README is the landing page and deliberately carries no
// flag tables, so it is not what this reads.
func TestDocsDocumentEveryFlag(t *testing.T) {
	dir := filepath.Join(repoRoot(), "docs")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	var docs, names strings.Builder
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".md" {
			continue
		}
		docs.WriteString(readRepoFile(t, "docs", e.Name()))
		fmt.Fprintf(&names, " docs/%s", e.Name())
	}
	for _, name := range declaredFlags(t) {
		if !strings.Contains(docs.String(), "-"+name) {
			t.Errorf("the -%s flag is documented nowhere in%s", name, names.String())
		}
	}
}

// A queue is derived by excluding orchestration labels, and the -label gate is
// the one thing standing between "anyone can open an issue" and "anyone can
// queue work for an unattended agent". An issue form's `labels:` key is applied
// on creation whoever files it, so a template that handed one of these out
// would hand out the gate with it — the failure docs/security.md warns about,
// arriving through a file nobody thinks of as code.
func TestIssueTemplatesApplyNoOrchestrationLabel(t *testing.T) {
	dir := filepath.Join(repoRoot(), ".github", "ISSUE_TEMPLATE")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	// The gate label is the operator's choice, so it cannot be named here.
	// These are the three this repository's own queue rules turn on, plus the
	// label its README documents as the gate it runs with.
	forbidden := []string{needsHumanLabel, proposedLabel, awaitingAnswerLabel, "ready"}
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".yml" && filepath.Ext(e.Name()) != ".yaml" {
			continue
		}
		for _, line := range applied(readRepoFile(t, ".github", "ISSUE_TEMPLATE", e.Name())) {
			for _, label := range forbidden {
				if strings.Contains(line, `"`+label+`"`) || strings.Contains(line, "- "+label) {
					t.Errorf(".github/ISSUE_TEMPLATE/%s applies the %q label (%s):"+
						" a template's labels are applied on creation whoever files the issue,"+
						" so this lets an outsider queue work for an unattended run",
						e.Name(), label, strings.TrimSpace(line))
				}
			}
		}
	}
}

// releaseGrace is how long a fix on a shipping path may sit above the newest
// release tag before TestShippingFixesDoNotSitUnreleased fails. It is the whole
// escape hatch and it is deliberately short: work in progress between releases
// is normal, but a build that went red the instant a fix merged would only
// teach everyone to ignore it. A day is long enough to batch a morning's fixes
// and short enough that #169's false park — a run handed the released skill a
// day after the fix for it merged — goes red first.
const releaseGrace = 24 * time.Hour

// The plugin and the binary ship from one tagged commit, and an install
// resolves to that tag, never to `main` (docs/releasing.md, "Installs resolve
// to a tag"). So a fix merged to `main` reaches nobody until plugin.json is
// bumped and a new tag cut — and nothing else notices the gap: #169 parked
// because the released skill still said `cd`, a day after the commit that
// removed it. CLAUDE.md already states the rule ("a fix that lands without a
// bump reaches nobody"); this makes it fail loudly instead of being remembered.
//
// It trips when a change touching either shipping path — skills/ or cmd/, the
// two halves that ship together — has been on main, unreleased, for longer than
// releaseGrace. The bound is time, not a commit count: one fix sitting for a
// week is the same defect as ten in an afternoon, and a count lets the first
// slip by. Test-only changes are excluded — they never reach a `go install`
// user — and a release already in flight (plugin.json bumped, tag not yet
// pushed) is taken as the acknowledgement it is.
func TestShippingFixesDoNotSitUnreleased(t *testing.T) {
	root := repoRoot()
	// Real git against the actual checkout breaks none of the hermetic rules
	// (no network, no gh, no real claude): the facts are all local, and no fake
	// can tell you whether a fix has been released. Same reasoning as gitAt in
	// sync_test.go.
	git := func(args ...string) (string, bool) {
		out, err := exec.Command("git", append([]string{"-C", root}, args...)...).Output()
		return strings.TrimSpace(string(out)), err == nil
	}

	// In CI the check has to run — a silent skip is the same as not having it —
	// so a checkout that cannot answer (not a repo, shallow, no tags) is a
	// failure there, naming the fix. Everywhere else it is a skip: a shallow or
	// tagless clone is a normal thing to be working in.
	inCI := os.Getenv("GITHUB_ACTIONS") == "true"
	unavailable := func(why string) {
		if inCI {
			t.Fatalf("%s — CI must check out full history and tags (fetch-depth: 0) for this check", why)
		}
		t.Skipf("%s", why)
	}

	if _, ok := git("rev-parse", "--git-dir"); !ok {
		unavailable("not a git checkout")
		return
	}
	if shallow, _ := git("rev-parse", "--is-shallow-repository"); shallow == "true" {
		unavailable("shallow clone: the release tags are not present")
		return
	}

	// The newest release by semver, not by tag date: a re-tag could land out of
	// order, and v0.9.0 sorts after v0.10.0 lexically. The vX.Y.Z tags are the
	// ones `go install ...@vX.Y.Z` resolves; polako--vX.Y.Z moves with them.
	tagList, ok := git("tag", "--list", "v*")
	if !ok || tagList == "" {
		unavailable("no vX.Y.Z release tags found")
		return
	}
	var latestTag string
	var latest [3]int
	for _, tag := range strings.Fields(tagList) {
		v, err := parseSemver(strings.TrimPrefix(tag, "v"))
		if err != nil {
			continue
		}
		if latestTag == "" || slices.Compare(v[:], latest[:]) > 0 {
			latestTag, latest = tag, v
		}
	}
	if latestTag == "" {
		unavailable("no tag matching vX.Y.Z parsed as a version")
		return
	}

	// A release in flight: the chore(release) commit bumps plugin.json before
	// cut-release.yml pushes the tag, and the release PR's own CI runs in that
	// window. The bump is the acknowledgement, so nothing is owed.
	if v, err := parseSemver(pluginManifestVersion(t)); err == nil && slices.Compare(v[:], latest[:]) > 0 {
		return
	}

	// First-parent, so a date is when the change landed on main — a merge
	// commit's %ct — not when it was written on a branch that may have lived
	// for days before a same-day release made it current. The subject is then
	// the merge's ("Merge pull request #N …"), which points at the PR to
	// release. Test files are dropped: they do not ship to a `go install` user.
	log, ok := git("log", "--first-parent", "--pretty=format:%ct%x09%h%x09%s",
		latestTag+"..HEAD", "--", "skills", "cmd", ":(exclude)*_test.go")
	if !ok {
		unavailable("could not list commits since " + latestTag)
		return
	}
	if log == "" {
		return // nothing unreleased on a shipping path
	}

	var lines []string
	var oldest time.Time
	for _, line := range strings.Split(log, "\n") {
		when, rest, found := strings.Cut(line, "\t")
		if !found {
			continue
		}
		secs, err := strconv.ParseInt(when, 10, 64)
		if err != nil {
			continue
		}
		lines = append(lines, "\t"+strings.ReplaceAll(rest, "\t", "  "))
		if at := time.Unix(secs, 0); oldest.IsZero() || at.Before(oldest) {
			oldest = at
		}
	}
	if age := time.Since(oldest); age > releaseGrace {
		t.Errorf("%d commit(s) touching skills/ or cmd/ have been unreleased since %s,"+
			" the oldest for %s (grace is %s):\n%s\n\n"+
			"An install resolves to a release tag, not to main, so the released plugin"+
			" and binary do not contain these — an unattended run keeps getting the"+
			" released skill. Remedy: a `chore(release)` bump of"+
			" .claude-plugin/plugin.json and CHANGELOG.md (the \"Start a release\""+
			" workflow, or ./scripts/release.sh).",
			len(lines), latestTag, age.Round(time.Hour), releaseGrace, strings.Join(lines, "\n"))
	}
}

// applied returns the lines of an issue form that name labels: the `labels:`
// key itself and, when it is a block list, the items under it.
func applied(form string) []string {
	var out []string
	list := false
	for _, line := range strings.Split(form, "\n") {
		switch {
		case strings.HasPrefix(line, "labels:"):
			out, list = append(out, line), true
		case list && strings.HasPrefix(strings.TrimSpace(line), "- "):
			out = append(out, line)
		default:
			list = false
		}
	}
	return out
}
