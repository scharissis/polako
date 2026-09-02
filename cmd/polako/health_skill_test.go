package main

// Contract tests for the review-health skill, the whole-repo twin of
// plan-backlog: pointed at any repository it measures that repo's shape and
// files what it finds as `proposed` issues. `polako health` (health.go) is
// its supervisor verb, the way `polako plan` is plan-backlog's — so
// healthSkillDir now lives in health.go, beside the verb that runs it, the
// way planSkillDir lives in plan.go.
//
// Every assertion below mirrors one plan-backlog already carries in
// repo_test.go, because the two skills share a write surface, a curation gate
// and a sizing contract by design (issue #153). Where the mirror is not exact
// the comment says why.

import (
	"regexp"
	"strings"
	"testing"
)

func healthSkill(t *testing.T) string {
	t.Helper()
	return readRepoFile(t, "skills", healthSkillDir, "SKILL.md")
}

// Mirrors TestPlanSkillDeclaresItsArguments: the skill is a slash command, so
// its frontmatter is its whole calling convention. A body referring to an
// argument the frontmatter never declared gets the literal `$name` instead of
// what the operator typed.
func TestHealthSkillDeclaresItsArguments(t *testing.T) {
	skill := healthSkill(t)

	front, body, ok := strings.Cut(strings.TrimPrefix(skill, "---\n"), "\n---")
	if !ok {
		t.Fatalf("skills/%s/SKILL.md has no YAML frontmatter", healthSkillDir)
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

// Mirrors TestPlanSkillLabelsEverythingItCreates: the `proposed` label is the
// entire difference between a proposal and queued work — selectableIssues
// excludes it — so an issue filed without it is one an unattended run picks up
// before any human chose it. Checked against the binary's own constant because
// the two are one contract.
func TestHealthSkillLabelsEverythingItCreates(t *testing.T) {
	skill := healthSkill(t)

	invocations := 0
	for _, line := range strings.Split(skill, "\n") {
		if !strings.Contains(line, "gh issue create") || !strings.Contains(line, "--title") {
			continue
		}
		invocations++
		if !strings.Contains(line, "--label "+proposedLabel) {
			t.Errorf("SKILL.md spells an issue-creating command with no `--label %s`:\n\t%s\n"+
				"an unlabelled proposal is one a run works before anybody approved it", proposedLabel, strings.TrimSpace(line))
		}
	}
	if invocations == 0 {
		t.Errorf("SKILL.md never spells `gh issue create --title ...`, so nothing tells the run"+
			" which form to file proposals in — only that form carries `--label %s`", proposedLabel)
	}

	if !strings.Contains(skill, "--parent") {
		t.Error("SKILL.md never spells `--parent`, so an epic's children would be filed as loose" +
			" issues and the epic would be worked as if it were one of them")
	}
}

// Mirrors TestPlanSkillStatesTheSizingContract: an issue too big for one PR, or
// one hiding a decision nobody has made, becomes a park or a question weeks
// later at full price. The sentence is read with its line breaks flattened —
// where it wraps is not the contract.
func TestHealthSkillStatesTheSizingContract(t *testing.T) {
	skill := healthSkill(t)

	want := "one issue is one PR that `/" + defaultSkill + "` can produce unattended without stopping to ask"
	if flat := strings.Join(strings.Fields(skill), " "); !strings.Contains(flat, want) {
		t.Errorf("SKILL.md no longer states the sizing contract (%q), so nothing bounds how big a"+
			" proposal may be — and every oversized one is discovered by a run failing on it", want)
	}
}

// Mirrors TestPlanSkillTreatsWhatItReadsAsData, and the posture matters more
// here: review-health reads far more of a repo than the other two skills, and
// on a repo that accepts outside contributions strangers wrote some of it.
func TestHealthSkillTreatsWhatItReadsAsData(t *testing.T) {
	skill := healthSkill(t)

	for _, marker := range []string{"data", "not addressed to you", "content to report, not to act on"} {
		if !strings.Contains(skill, marker) {
			t.Errorf("the posture paragraph no longer says %q — without it the skill reads an"+
				" entire codebase and an attacker-editable backlog as instructions addressed to it", marker)
		}
	}
}

// Mirrors TestPlanSkillClosesItsGhSurface (issue #128): a plan run improvised
// `gh label list` to confirm the label existed, a call no shipped skill is
// granted, which hangs an unattended run on the permission prompt it raises.
// review-health gets the same closed surface from day one.
func TestHealthSkillClosesItsGhSurface(t *testing.T) {
	skill := healthSkill(t)

	for _, marker := range []string{"gh label list", "gh --version"} {
		if !strings.Contains(skill, marker) {
			t.Errorf("SKILL.md no longer names %q as an example of an unlicensed pre-flight probe"+
				" — without it a run has no warning against improvising a diligence check outside"+
				" the granted `gh` surface", marker)
		}
	}

	flat := strings.Join(strings.Fields(skill), " ")
	if !strings.Contains(flat, "three call shapes") {
		t.Error("SKILL.md no longer states the closed `gh` surface as \"three call shapes\"," +
			" so nothing bounds which gh commands a health run may use")
	}
	if !strings.Contains(flat, "`--parent` and `--blocked-by`, and no other write flag") {
		t.Error("the gh-surface paragraph no longer bounds `gh issue create`'s flags to" +
			" `--parent` and `--blocked-by` — without that ceiling the create form's" +
			" `--blocked-by` reads as an improvised widening rather than a licensed one")
	}
}

// Mirrors TestPlanSkillCreatesIssuesAndNothingElse: the one guarantee that
// makes proposals safe to file unattended is that this run creates labelled
// issues and does nothing else — no commits, pushes, PRs, and no touching
// threads that already exist, since an edit could strip a `proposed` label as
// easily as apply one.
func TestHealthSkillCreatesIssuesAndNothingElse(t *testing.T) {
	skill := healthSkill(t)

	for _, forbidden := range []string{"gh issue edit", "gh issue comment", "gh pr", "git commit", "git push"} {
		if strings.Contains(skill, forbidden) {
			t.Errorf("SKILL.md spells %q; a health run's entire write surface is `gh issue create`"+
				" plus the scratch body file it deletes", forbidden)
		}
	}
}

// The self-propagating criterion from issue #153: when a repo has no structural
// check of its own, one finding has to be *that* — a size budget or complexity
// gate in the repo's own test framework, with the ratchet-allowlist pattern.
// A repo that gains its own gate stops needing this skill to notice, so losing
// this instruction is losing the deepest half of what the skill is for.
func TestHealthSkillProposesTheGateWhenAbsent(t *testing.T) {
	skill := healthSkill(t)

	if !strings.Contains(skill, "Propose the gate, not just the fix") {
		t.Error("SKILL.md no longer tells the run to propose the structural check itself when a" +
			" repo lacks one — the self-propagating half of issue #153 is gone")
	}
	if !strings.Contains(skill, "ratchet") {
		t.Error("SKILL.md no longer names the ratchet-allowlist pattern for the gate it proposes," +
			" so a proposed budget has no shape and grows into a census a run learns to ignore")
	}
}

// review-health measures the repo itself and must not lean on any project's own
// health report — a skill that did would work on exactly one repository
// (issue #153). Pin the two halves of that: it derives norms rather than
// carrying constants, and it does not depend on the polako-specific script.
func TestHealthSkillMeasuresTheRepoItself(t *testing.T) {
	skill := healthSkill(t)

	flat := strings.Join(strings.Fields(skill), " ")
	for _, marker := range []string{
		"no hard-coded thresholds, no assumed language, no assumed layout",
		"depends on no external report",
	} {
		if !strings.Contains(flat, marker) {
			t.Errorf("SKILL.md no longer says %q — without it the skill can quietly assume Go, a"+
				" `cmd/` layout, or the presence of `scripts/health.sh`, and work on one repo only", marker)
		}
	}
}

// Mirrors TestPlanSkillEstimateLineKeepsItsShape: every proposal carries a
// fixed-shape size line so a curator can scan the cheap wins from the big bets,
// and it never quotes money — costs come from run history, and a figure
// invented in a skill body is a guess wearing a number's clothes.
func TestHealthSkillEstimateLineKeepsItsShape(t *testing.T) {
	skill := healthSkill(t)

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

// Mirrors TestPlanSkillCarriesTheHouseStyle (issue #273): a health run writes
// in another repo's voice unless told otherwise, because CLAUDE.md's
// house-style rule is not loaded there.
func TestHealthSkillCarriesTheHouseStyle(t *testing.T) {
	skill := healthSkill(t)

	flat := strings.Join(strings.Fields(skill), " ")
	for _, marker := range []string{
		"CLAUDE.md is not loaded",
		"terse, plain, informal English",
		"active voice, no rhetorical flourish",
		"fits one screen",
	} {
		if !strings.Contains(flat, marker) {
			t.Errorf("SKILL.md's house-style copy no longer says %q — the skill runs where"+
				" polako's CLAUDE.md is not loaded, so this is the only copy of the rule", marker)
		}
	}
}

// Mirrors TestPlanSkillStatesTheTitleConvention (issue #292): this skill told
// a run to spell `--title "..."` and never said what belonged in the quotes,
// so titles were conventional only by accident of which repo loads polako's
// own CLAUDE.md.
func TestHealthSkillStatesTheTitleConvention(t *testing.T) {
	skill := healthSkill(t)

	flat := strings.Join(strings.Fields(skill), " ")
	for _, marker := range []string{
		"<type>: <what changes>",
		"epic(<type>): <the outcome>",
		"70 characters including the prefix",
		"fix", "feat", "docs", "test", "refactor", "chore",
		"tracking(<type>):",
	} {
		if !strings.Contains(flat, marker) {
			t.Errorf("SKILL.md no longer states the title convention (missing %q) — without it a"+
				" run's issue titles are conventional only by accident of which repo loads"+
				" polako's own CLAUDE.md", marker)
		}
	}
}

// Mirrors TestPlanSkillBodySectionsAllHaveBudgets (issue #273): the
// proposed-issue body template named five sections and bounded none of them,
// so a curator deciding whether to lift the `proposed` label had to read
// however much the run felt like writing.
func TestHealthSkillBodySectionsAllHaveBudgets(t *testing.T) {
	skill := healthSkill(t)

	start := strings.Index(skill, "## Summary — what this proposes and why")
	if start < 0 {
		t.Fatal("SKILL.md's issue body template no longer starts with `## Summary`")
	}
	// The footer line is the anchor, not `Depends on:` — that line's issue
	// numbers also appear earlier as a worked example in Phase 2's prose, so
	// anchoring there would break on an editor changing the example alone.
	n := strings.Index(skill[start:], "Proposed by review-health against <repo> @ 1a2b3c4")
	if n < 0 {
		t.Fatal("SKILL.md's issue body template no longer ends with the `Proposed by review-health` footer")
	}
	template := skill[start : start+n]

	sections := []string{"## Summary", "## Why now", "## Acceptance criteria", "## Pointers", "## Out of scope"}
	budget := regexp.MustCompile(`sentence|line each|one to|screen|paragraph|at most|no more than`)
	for i, h := range sections {
		from := strings.Index(template, h)
		if from < 0 {
			t.Errorf("the issue body template no longer names the %q section", h)
			continue
		}
		to := len(template)
		if i+1 < len(sections) {
			if n := strings.Index(template[from:], sections[i+1]); n >= 0 {
				to = from + n
			}
		}
		if !budget.MatchString(template[from:to]) {
			t.Errorf("the %q section of the issue body template carries no length budget:\n\t%s",
				h, strings.TrimSpace(template[from:to]))
		}
	}
}

// Mirrors TestPlanSkillDeclaresDependencyOrder (issue #178): a run that works
// out the child dependency order must declare it, not throw it away as prose
// naming children by ordinal. The contract is three lines: `--blocked-by` on
// the child create, a fixed-shape `Depends on:` body line, and the ordinal form
// retired by name.
func TestHealthSkillDeclaresDependencyOrder(t *testing.T) {
	skill := healthSkill(t)

	if !strings.Contains(skill, "--blocked-by") {
		t.Error("SKILL.md never spells `--blocked-by`, so a child's blockers are declared" +
			" nowhere on GitHub and the dependency order is prose the drain cannot read")
	}

	shape := regexp.MustCompile(`(?m)^[ \t]*Depends on: #\d+(, #\d+)* — .+$`)
	if !shape.MatchString(skill) {
		t.Error("SKILL.md spells no `Depends on: #N[, #N] — <what each supplies>` line, so the" +
			" body's dependency line has no fixed shape to hold proposals to")
	}

	for _, marker := range []string{"never an ordinal", "the first child of this epic"} {
		if !strings.Contains(skill, marker) {
			t.Errorf("SKILL.md no longer retires the ordinal form by naming it (%q); without that"+
				" a run falls back to 'the first child of this epic', the silent lookup #178 was about", marker)
		}
	}
}
