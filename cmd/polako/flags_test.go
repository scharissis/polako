package main

import (
	"flag"
	"io"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestParseSkipIgnoresJunk(t *testing.T) {
	t.Parallel()
	got := parseSkip(" 12, 34 ,,notanumber,56,")
	want := map[int]bool{12: true, 34: true, 56: true}
	if len(got) != len(want) {
		t.Fatalf("parseSkip = %v, want %v", got, want)
	}
	for n := range want {
		if !got[n] {
			t.Errorf("parseSkip missing %d (got %v)", n, got)
		}
	}
	if len(parseSkip("")) != 0 {
		t.Errorf("empty -skip should produce an empty set")
	}
}

func TestParseEffortBySize(t *testing.T) {
	t.Parallel()
	got, err := parseEffortBySize(" S=medium , L=max ")
	if err != nil {
		t.Fatalf("parseEffortBySize: %v", err)
	}
	if want := map[string]string{"S": "medium", "L": "max"}; len(got) != len(want) ||
		got["S"] != want["S"] || got["L"] != want["L"] {
		t.Errorf("parseEffortBySize = %v, want %v", got, want)
	}

	if m, err := parseEffortBySize(""); m != nil || err != nil {
		t.Errorf("empty -effort-by-size = (%v, %v), want (nil, nil)", m, err)
	}

	// Unlike -skip, a bad entry stops the run rather than being ignored.
	for _, bad := range []string{"S", "S=", "=medium", "XL=high", "S=medim", "S=medium,S=max"} {
		if _, err := parseEffortBySize(bad); err == nil {
			t.Errorf("parseEffortBySize(%q) = nil error, want a rejection", bad)
		}
	}
}

func TestParseModelBySize(t *testing.T) {
	t.Parallel()
	got, err := parseModelBySize(" S=sonnet , L=opus ")
	if err != nil {
		t.Fatalf("parseModelBySize: %v", err)
	}
	if want := map[string]string{"S": "sonnet", "L": "opus"}; len(got) != len(want) ||
		got["S"] != want["S"] || got["L"] != want["L"] {
		t.Errorf("parseModelBySize = %v, want %v", got, want)
	}

	if m, err := parseModelBySize(""); m != nil || err != nil {
		t.Errorf("empty -model-by-size = (%v, %v), want (nil, nil)", m, err)
	}

	// A full model id passes, the same shape a model: label accepts.
	if got, err := parseModelBySize("S=claude-opus-4-1"); err != nil || got["S"] != "claude-opus-4-1" {
		t.Errorf("parseModelBySize(full id) = (%v, %v), want claude-opus-4-1, nil", got, err)
	}

	for _, bad := range []string{"S", "S=", "=sonnet", "XL=opus", "S=opus!", "S=sonnet,S=opus"} {
		if _, err := parseModelBySize(bad); err == nil {
			t.Errorf("parseModelBySize(%q) = nil error, want a rejection", bad)
		}
	}
}

func TestResolveToolsAppendsWithoutDuplicating(t *testing.T) {
	t.Parallel()
	got := resolveTools("Read,Write,", " Bash(cargo:*) ,Read")
	if want := "Read,Write,Bash(cargo:*)"; got != want {
		t.Errorf("resolveTools = %q, want %q", got, want)
	}
	if got := resolveTools(defaultTools, ""); got != defaultTools {
		t.Errorf("an empty -add-tools must leave -tools untouched\ngot:  %q\nwant: %q", got, defaultTools)
	}
}

// The skill reads code, writes files, and invokes /code-review as a mandatory
// gate. Unattended runs die silently if any of those tools needs a prompt.
// Bash(evals/run.sh:*) is the same story for a run that changes a shipped
// SKILL.md: the eval step in Phase 3 hangs without it.
func TestDefaultToolsCoverWhatTheSkillNeeds(t *testing.T) {
	t.Parallel()
	have := strings.Split(defaultTools, ",")
	for _, want := range []string{
		"Bash(git:*)", "Bash(gh issue view:*)", "Bash(gh issue comment:*)", "Bash(gh pr create:*)",
		"Read", "Write", "Edit", "Glob", "Grep", "Skill", "Bash(evals/run.sh:*)",
	} {
		if !slices.Contains(have, want) {
			t.Errorf("defaultTools is missing %q", want)
		}
	}
}

// The gh grant is per subcommand on purpose, and the positive test above still
// passes with a broader one present — so the narrowing needs its own check.
// Each of these would hand attacker-supplied issue text something the skill
// never needs and the design forbids.
func TestDefaultToolsDoNotGrantGhWholesale(t *testing.T) {
	t.Parallel()
	have := strings.Split(defaultTools, ",")
	for entry, why := range map[string]string{
		"Bash(gh:*)":       "gh api, gh secret set and gh repo delete",
		"Bash(gh pr:*)":    "gh pr merge — nothing may merge itself",
		"Bash(gh issue:*)": "gh issue edit --add-label — that reopens a -label-gated queue",
		"Bash(gh run:*)":   "gh run rerun, gh run cancel and gh run delete — a CI remediation only reads",
	} {
		if slices.Contains(have, entry) {
			t.Errorf("defaultTools grants %s, which permits %s; grant the subcommands the "+
				"skill needs and leave -add-tools as the escape hatch for projects that need more",
				entry, why)
		}
	}
}

// A CI remediation reads the failing job logs, and a gh call that raises a
// prompt hangs an unattended run silently.
func TestDefaultToolsCoverDiagnosingARedBuild(t *testing.T) {
	t.Parallel()
	have := strings.Split(defaultTools, ",")
	for _, want := range []string{"Bash(gh pr checks:*)", "Bash(gh run list:*)", "Bash(gh run view:*)"} {
		if !slices.Contains(have, want) {
			t.Errorf("defaultTools is missing %q, so remediateChecks cannot read why CI failed", want)
		}
	}
}

// --- environment defaults ---

func TestEnvVarNameMapsAFlagToItsVariable(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"post-summary": "POLAKO_POST_SUMMARY",
		"metrics":      "POLAKO_METRICS",
		"retry-wait":   "POLAKO_RETRY_WAIT",
	}
	for flagName, want := range cases {
		if got := envVarName(flagName); got != want {
			t.Errorf("envVarName(%q) = %q, want %q", flagName, got, want)
		}
	}
}

// A flag set shaped like the drain's: one of each kind the environment has to
// be able to carry.
func envFlagSet() (*flag.FlagSet, *bool, *string, *time.Duration) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	post := fs.Bool("post-summary", false, "")
	model := fs.String("model", "", "")
	poll := fs.Duration("poll", 5*time.Minute, "")
	return fs, post, model, poll
}

func TestEnvDefaultsSetWhatWasNotPassed(t *testing.T) {
	t.Setenv("POLAKO_POST_SUMMARY", "1")
	t.Setenv("POLAKO_MODEL", "claude-opus-5")
	t.Setenv("POLAKO_POLL", "90s")

	fs, post, model, poll := envFlagSet()
	if err := applyEnvDefaults(fs); err != nil {
		t.Fatalf("applyEnvDefaults: %v", err)
	}
	if err := fs.Parse(nil); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !*post || *model != "claude-opus-5" || *poll != 90*time.Second {
		t.Errorf("flags = %v / %q / %s, want the environment's values", *post, *model, *poll)
	}
	// -h prints DefValue, so the default it reports has to be the one in force.
	if got := fs.Lookup("poll").DefValue; got != "1m30s" {
		t.Errorf("printed default = %q, want the environment's 1m30s", got)
	}
}

// The environment is a preference; an argument is a decision about this run.
func TestCommandLineBeatsTheEnvironment(t *testing.T) {
	t.Setenv("POLAKO_MODEL", "claude-opus-5")
	t.Setenv("POLAKO_POST_SUMMARY", "1")

	fs, post, model, _ := envFlagSet()
	if err := applyEnvDefaults(fs); err != nil {
		t.Fatalf("applyEnvDefaults: %v", err)
	}
	if err := fs.Parse([]string{"-model", "claude-haiku-4-5", "-post-summary=false"}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if *model != "claude-haiku-4-5" || *post {
		t.Errorf("flags = %q / %v, want the arguments to win", *model, *post)
	}
}

// A preference that was set, looks set, and silently does nothing is the worst
// outcome for a run nobody is watching.
func TestEnvDefaultsRejectAValueTheFlagCannotParse(t *testing.T) {
	t.Setenv("POLAKO_POLL", "banana")

	fs, _, _, _ := envFlagSet()
	err := applyEnvDefaults(fs)
	if err == nil {
		t.Fatal("an unparseable value must stop the process, not be skipped")
	}
	// The message has to name both halves: the variable to fix, and the flag
	// it was trying to set.
	for _, want := range []string{"POLAKO_POLL", "banana", "-poll"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// The four flags that are actions rather than preferences. POLAKO_VERSION
// is what a Dockerfile or CI job pins an install with, POLAKO_DRY_RUN is what
// an operator exports to preview one repository and forgets, POLAKO_APPLY
// is that same risk mirrored onto `tidy`, and POLAKO_YES mirrors it again
// onto `setup -apply`. Honouring any of them would turn a later run into
// something other than what its operator typed that day: a drain that prints
// and exits 0 without touching the backlog, for the first two, a `tidy` that
// quietly deletes worktrees and branches nobody meant to run live, for the
// third, or a `setup -apply` that writes every label without asking, for the
// fourth.
func TestEnvDefaultsIgnoreTheActionFlags(t *testing.T) {
	t.Setenv("POLAKO_VERSION", "0.6.0")
	t.Setenv("POLAKO_DRY_RUN", "1")
	t.Setenv("POLAKO_APPLY", "1")
	t.Setenv("POLAKO_YES", "1")

	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	showVersion := fs.Bool("version", false, "")
	dry := fs.Bool("dry-run", false, "")
	apply := fs.Bool("apply", false, "")
	yes := fs.Bool("yes", false, "")
	if err := applyEnvDefaults(fs); err != nil {
		t.Fatalf("applyEnvDefaults: %v", err)
	}
	if *showVersion {
		t.Error("-version must not be settable from the environment")
	}
	if *dry {
		t.Error("-dry-run must not be settable from the environment")
	}
	if *apply {
		t.Error("-apply must not be settable from the environment")
	}
	if *yes {
		t.Error("-yes must not be settable from the environment")
	}
}
