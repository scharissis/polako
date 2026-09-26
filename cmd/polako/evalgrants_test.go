package main

// The eval suite's tool grant (evals/lib/grants.sh) and the refusal grader it
// exists for. A case run under plain Bash never meets the refusals a real run
// meets, so the grant copies production's, and these tests keep the copy from
// drifting off the Go it came from.

import (
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// evalGrants reads the eval_grants array out of evals/lib/grants.sh: one
// single-quoted entry per line, with comments and blank lines between.
func evalGrants(t *testing.T) []string {
	t.Helper()
	src := readRepoFile(t, "evals", "lib", "grants.sh")
	_, body, ok := strings.Cut(src, "\neval_grants=(\n")
	if !ok {
		t.Fatal("evals/lib/grants.sh has no `eval_grants=(` line opening a multi-line array")
	}
	body, _, ok = strings.Cut(body, "\n)")
	if !ok {
		t.Fatal("evals/lib/grants.sh's eval_grants array has no closing `)` on a line of its own")
	}
	var out []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if len(line) < 2 || line[0] != '\'' || line[len(line)-1] != '\'' {
			t.Fatalf("evals/lib/grants.sh entry %s is not one single-quoted entry on a line of its own", line)
		}
		out = append(out, line[1:len(line)-1])
	}
	return out
}

// evalUngated is grade.py's UNGATED: tools a case names in its own
// allowed_tools, which reach the run without the operator granting them.
var evalUngated = []string{"Read", "Glob", "Grep", "NotebookRead", "Skill", "AskUserQuestion",
	"TaskCreate", "TaskGet", "TaskList", "TaskUpdate", "TaskStop", "Agent", "TodoWrite"}

// acceptEditsCommands is the CLI's own list of the Bash commands acceptEdits
// lets through, read from 2.1.283. It's polako's -permission-mode default, and
// the CLI runs every eval case under dontAsk instead, so grants.sh names them.
var acceptEditsCommands = []string{"mkdir", "touch", "rm", "rmdir", "mv", "cp", "sed"}

// evalFixtureScripts are the scratch repo's own scripts that a case's run
// executes, relative to evals/. No defaultTools prefix covers a repo's own
// script, so an operator grants one with -add-tools, and grants.sh does too.
var evalFixtureScripts = []string{"lib/fixture/greet.sh", "lib/fixture/test.sh", "one-turn/bench.sh"}

// bashPrefix is the command a `Bash(<command>:*)` entry grants, or "" for any
// other shape.
func bashPrefix(entry string) string {
	if inner, ok := strings.CutPrefix(entry, "Bash("); ok {
		if cmd, ok := strings.CutSuffix(inner, ":*)"); ok {
			return cmd
		}
	}
	return ""
}

// grantCovers reports whether entry adds nothing to have: it is already there,
// or a Bash prefix in have is a word-wise prefix of it, the way `Bash(git:*)`
// covers `Bash(git log:*)`.
func grantCovers(have []string, entry string) bool {
	cmd := bashPrefix(entry)
	return slices.ContainsFunc(have, func(h string) bool {
		if h == entry {
			return true
		}
		hc := bashPrefix(h)
		return cmd != "" && hc != "" && strings.HasPrefix(cmd, hc+" ")
	})
}

// wantEvalGrants is what grants.sh must hold: every verb's grant at once,
// since the CLI takes one grant per invocation, minus the ungated tools a case
// names itself and anything a wider entry already covers — plus the three
// groups grants.sh documents as standing in for what a production run has
// without a grant. defaultTools goes first, so its wide `Bash(git:*)` covers
// the narrower git entries planTools and healthTools spell.
func wantEvalGrants(t *testing.T) []string {
	t.Helper()
	var want []string
	add := func(entry string) {
		if entry == "" || slices.Contains(evalUngated, entry) || grantCovers(want, entry) {
			return
		}
		want = append(want, entry)
	}
	for _, grant := range []string{
		defaultTools, issueLabelTools(1), issueCloseTool(1), designTools, planTools, healthTools,
	} {
		for _, entry := range strings.Split(grant, ",") {
			add(strings.TrimSpace(entry))
		}
	}
	for _, cmd := range acceptEditsCommands {
		add("Bash(" + cmd + ":*)")
	}
	for _, script := range evalFixtureScripts {
		if _, err := os.Stat(filepath.Join(repoRoot(), "evals", filepath.FromSlash(script))); err != nil {
			t.Errorf("evals/%s is gone (%v): drop or rename its entry here and in evals/lib/grants.sh", script, err)
		}
		add("Bash(*/" + filepath.Base(filepath.FromSlash(script)) + "*)")
	}
	add("ListAgents")
	return want
}

// The eval grant is production's, entry for entry, so a case meets the
// refusals a real run meets. It used to be plain Bash, which let every
// command through. This holds it to the Go constants it copies.
func TestEvalGrantsMatchTheVerbsGrants(t *testing.T) {
	t.Parallel()
	got, want := evalGrants(t), wantEvalGrants(t)
	for i, entry := range got {
		if slices.Contains(got[:i], entry) {
			t.Errorf("evals/lib/grants.sh lists %s twice", entry)
		}
	}
	for _, entry := range want {
		if !slices.Contains(got, entry) {
			t.Errorf("evals/lib/grants.sh is missing '%s', which the Go grants now hold — add it", entry)
		}
	}
	for _, entry := range got {
		if entry == "Bash" {
			t.Error("evals/lib/grants.sh grants plain Bash: every command gets through, so no case " +
				"can meet a refusal a real run would. Grant production's prefixes instead")
			continue
		}
		if !slices.Contains(want, entry) {
			t.Errorf("evals/lib/grants.sh grants '%s', which no Go grant holds and no stand-in group "+
				"here names — drop it, or add it to the group it stands in for", entry)
		}
	}
}

// grants.sh names acceptEdits' file commands because production runs under
// acceptEdits and the CLI runs cases under dontAsk. If polako's default mode
// changes, that group stops standing in for anything.
func TestEvalGrantsStandInForTheDefaultPermissionMode(t *testing.T) {
	t.Parallel()
	var cfg config
	var local localFlags
	issue := flag.NewFlagSet("work", flag.ContinueOnError)
	registerIssueFlags(issue, &cfg, defaultSkill, defaultTools, &local)
	var opt intakeOptions
	intake := flag.NewFlagSet("plan", flag.ContinueOnError)
	registerIntakeFlags(intake, &opt, planVerb)
	for _, fs := range []*flag.FlagSet{issue, intake} {
		if got := fs.Lookup("permission-mode").DefValue; got != "acceptEdits" {
			t.Errorf("%s's -permission-mode now defaults to %q, not acceptEdits: evals/lib/grants.sh's "+
				"acceptEdits group (and acceptEditsCommands here) no longer mirrors production — rework it",
				fs.Name(), got)
		}
	}
}

// evalRefusalPattern is the no_call_was_refused grader's regex, as the case
// files spell it in single quotes. The result event's permission_denials list
// is every refusal of the run; the permission_denied system event is the same
// refusal as it happens, a subagent's included.
const evalRefusalPattern = `'"permission_denials":\[\{|"subtype":"permission_denied"'`

// Every implement-issue case fails a run that met a refusal, so a new case
// can't quietly go back to grading only what a run produced.
func TestImplementIssueEvalCasesFailARefusedCall(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob(filepath.Join(repoRoot(), "evals", "*", "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	prompt := regexp.MustCompile(`(?m)^\s+prompt: /polako:implement-issue\b`)
	checked := 0
	for _, path := range files {
		if base := filepath.Base(path); base != "case.yaml" && base != "by-hand.yaml" {
			continue
		}
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		if !prompt.MatchString(src) {
			continue
		}
		checked++
		rel, _ := filepath.Rel(repoRoot(), path)
		_, grader, ok := strings.Cut(src, "name: no_call_was_refused\n")
		if !ok {
			t.Errorf("%s has no no_call_was_refused grader: a run that met a refusal would still grade green", rel)
			continue
		}
		grader, _, _ = strings.Cut(grader, "- type:")
		for _, want := range []string{"target: trace", "pattern: " + evalRefusalPattern, "match: not_contains"} {
			if !strings.Contains(grader, want) {
				t.Errorf("%s's no_call_was_refused grader lacks %q", rel, want)
			}
		}
	}
	if checked == 0 {
		t.Fatal("found no implement-issue case under evals/ — the glob or the prompt pattern is wrong")
	}
}
