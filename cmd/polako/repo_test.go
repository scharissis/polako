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
// binary's defaults cannot drift from what the binary actually ships.
func stringFlagDefault(t *testing.T, name string) string {
	t.Helper()
	registration := regexp.MustCompile(`\.StringVar\(&\w+(?:\.\w+)?, "` + regexp.QuoteMeta(name) + `", "([^"]*)"`)
	m := registration.FindStringSubmatch(readRepoFile(t, "cmd", "polako", "main.go"))
	if m == nil {
		t.Fatalf("main.go registers no string flag named %q", name)
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

// planSkillDir is the intake-side skill this repo ships under skills/. It lives
// here rather than beside skillDir in main.go because the binary has no plan
// verb yet — that is phase 3 — and a constant no shipped code path reads is
// dead weight in the binary. It moves to main.go when the verb that runs it
// does.
const planSkillDir = "plan-backlog"

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

// Naming the branch is only half of aiming the review. It resolves that
// branch's base from the *local* default branch, and a drain never pulls — it
// merges on GitHub — so that ref falls one commit behind per merged PR. Review
// against a stale one and somebody else's merged PR is inside the diff, where
// `--fix` will happily rewrite it into this branch. The refresh has to come
// before the invocation, so check the order too.
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
// It trips when a non-merge commit touching either shipping path — skills/ or
// cmd/, the two halves that ship together — has been unreleased for longer than
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

	if _, ok := git("rev-parse", "--git-dir"); !ok {
		t.Skip("not a git checkout: nothing to check release freshness against")
	}

	// In CI the check has to run — a silent skip is the same as not having it —
	// so a checkout without the history or tags this needs is a failure there,
	// naming the fix. Everywhere else it is a skip: a shallow or tagless clone
	// is a normal thing to be working in.
	inCI := os.Getenv("GITHUB_ACTIONS") == "true"
	unavailable := func(why string) {
		if inCI {
			t.Fatalf("%s — CI must check out full history and tags (fetch-depth: 0) for this check", why)
		}
		t.Skipf("%s", why)
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

	// Non-merge, so the subjects name the fixes rather than the PRs and the
	// dates are the commits' own; %ct is the committer time, when the change
	// joined a history heading for a release. Test files are dropped: they do
	// not ship to a `go install` user.
	log, ok := git("log", "--no-merges", "--pretty=format:%ct%x09%h%x09%s",
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
