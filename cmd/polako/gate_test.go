package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// The gate itself: exactly one shape refuses — a public repository with no
// -label and no -ungated — because that is the one shape where "anyone can
// open an issue" and "an unattended agent implements open issues" meet.
func TestQueueGateRefusesOnlyThePublicUnlabelledQueue(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		visibility string
		label      string
		ungated    bool
		refused    bool
	}{
		{"public and unfiltered", "PUBLIC", "", false, true},
		{"public, case from a different gh", "public", "", false, true},
		{"public but label-gated", "PUBLIC", "ready-for-claude", false, false},
		{"public and consented to", "PUBLIC", "", true, false},
		{"private", "PRIVATE", "", false, false},
		{"internal", "INTERNAL", "", false, false},
		{"visibility gh never named", "", "", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := queueGate(tc.visibility, tc.label, tc.ungated, "")
			if tc.refused && err == nil {
				t.Fatal("gate let an unfiltered public queue through")
			}
			if !tc.refused && err != nil {
				t.Fatalf("gate refused a queue it is not about: %v", err)
			}
			if err != nil {
				// The error is the operator's whole briefing, so it has to name
				// both ways forward.
				for _, want := range []string{"-label", "-ungated"} {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("refusal does not mention %s:\n%s", want, err)
					}
				}
			}
		})
	}
}

// markedLabel, when the caller knows it, replaces the generic "-label <name>"
// placeholder with the real name — so a refusal on a repo setup already
// marked a gate label on says "pass -label ready" rather than making the
// operator guess a name to type.
func TestQueueGateNamesTheMarkedLabelWhenKnown(t *testing.T) {
	t.Parallel()
	generic := queueGate("PUBLIC", "", false, "")
	if generic == nil || !strings.Contains(generic.Error(), "-label <name>") {
		t.Fatalf("queueGate with no marked label = %v, want the generic -label <name> placeholder", generic)
	}
	named := queueGate("PUBLIC", "", false, "ready")
	if named == nil || !strings.Contains(named.Error(), "-label ready") {
		t.Fatalf("queueGate with a marked label = %v, want it to say -label ready", named)
	}
	if strings.Contains(named.Error(), "<name>") {
		t.Errorf("refusal = %q, should not still carry the generic placeholder once a name is known", named.Error())
	}
}

// The wiring: preflight reads visibility off the same `gh repo view` call that
// names the repository, and refuses before anything is written or run.
func TestPreflightRefusesAnUngatedPublicQueue(t *testing.T) {
	t.Parallel()
	_, checkout := upstream(t)
	cfg, _ := drainConfig(t, "stream", &ghState{Visibility: "PUBLIC", Labels: []string{"ready-for-claude"}})
	cfg.dir = checkout

	if err := preflight(context.Background(), &cfg); err == nil {
		t.Fatal("preflight started an unfiltered drain on a public repository")
	} else if !strings.Contains(err.Error(), "-label") {
		t.Fatalf("refusal does not tell the operator about -label: %v", err)
	}

	gated := cfg
	gated.label = "ready-for-claude"
	if err := preflight(context.Background(), &gated); err != nil {
		t.Fatalf("a -label gate should satisfy preflight: %v", err)
	}

	consented := cfg
	consented.ungated = true
	if err := preflight(context.Background(), &consented); err != nil {
		t.Fatalf("-ungated should satisfy preflight: %v", err)
	}
}

// A public repository whose gate label is already marked gets a refusal
// naming it — "pass -label ready" — rather than the generic placeholder, and
// cfg.label itself stays empty: work still never scopes itself, only the
// message changes.
func TestPreflightNamesTheMarkedGateLabelInItsRefusal(t *testing.T) {
	t.Parallel()
	_, checkout := upstream(t)
	cfg, _ := drainConfig(t, "stream", &ghState{
		Visibility:        "PUBLIC",
		Labels:            []string{"ready"},
		LabelDescriptions: map[string]string{"ready": gateLabelDescription},
	})
	cfg.dir = checkout

	err := preflight(context.Background(), &cfg)
	if err == nil {
		t.Fatal("preflight started an unfiltered drain on a public repository")
	}
	if !strings.Contains(err.Error(), "-label ready") {
		t.Errorf("refusal = %v, want it to name the marked label", err)
	}
	if strings.Contains(err.Error(), "<name>") {
		t.Errorf("refusal = %v, should not still carry the generic placeholder once a marked label was found", err)
	}
	if cfg.label != "" {
		t.Errorf("cfg.label = %q, want it left empty — work never scopes itself from the marker", cfg.label)
	}
}

// A dry run writes nothing and runs nothing, so it is allowed through to look —
// seeing the queue is how an operator decides what to label — but the gate
// still tells them a real run would refuse.
func TestPreflightLetsADryRunLookThroughTheGate(t *testing.T) {
	t.Parallel()
	_, checkout := upstream(t)
	logged := captureLog(t)
	cfg, _ := drainConfig(t, "stream", &ghState{Visibility: "PUBLIC"})
	cfg.dir = checkout
	cfg.dryRun = true

	if err := preflight(context.Background(), &cfg); err != nil {
		t.Fatalf("the gate stopped a dry run, which changes nothing: %v", err)
	}
	if !strings.Contains(logged.String(), "-label") {
		t.Error("a dry run through the gate should still say a real run would refuse")
	}
}

// --- the -label gate: a label the repository has never defined ---

// The gate itself: only a missing label refuses, and the refusal names both
// the label and the fix.
func TestLabelGateRefusesOnlyAMissingLabel(t *testing.T) {
	t.Parallel()
	if err := labelGate("ready-for-claude", true); err != nil {
		t.Errorf("labelGate refused a label the repository has: %v", err)
	}
	err := labelGate("typo", false)
	if err == nil {
		t.Fatal("labelGate let a label the repository does not have through")
	}
	for _, want := range []string{"typo", "gh label create typo", "polako setup -apply -label typo"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not mention %q:\n%s", want, err)
		}
	}
}

// The wiring: preflight looks the label up and refuses before anything runs,
// naming the fix — and lets a run through once the label is one the
// repository actually has.
func TestPreflightRefusesALabelTheRepoDoesNotHave(t *testing.T) {
	t.Parallel()
	_, checkout := upstream(t)
	cfg, _ := drainConfig(t, "stream", &ghState{Labels: []string{"ready-for-claude"}})
	cfg.dir = checkout
	cfg.label = "typo"

	if err := preflight(context.Background(), &cfg); err == nil {
		t.Fatal("preflight started a drain scoped to a label the repository does not have")
	} else if !strings.Contains(err.Error(), "gh label create typo") {
		t.Fatalf("refusal does not name the fix: %v", err)
	}

	gated := cfg
	gated.label = "ready-for-claude"
	if err := preflight(context.Background(), &gated); err != nil {
		t.Fatalf("a label the repository has should satisfy preflight: %v", err)
	}
}

// A dry run still says a real run would refuse — the same carve-out
// queueGate gets, through the same refuseOrNote.
func TestPreflightLetsADryRunLookThroughTheLabelGate(t *testing.T) {
	t.Parallel()
	_, checkout := upstream(t)
	cfg, _ := drainConfig(t, "stream", &ghState{})
	cfg.dir = checkout
	cfg.label = "typo"
	cfg.dryRun = true

	logged := captureLog(t)

	if err := preflight(context.Background(), &cfg); err != nil {
		t.Fatalf("the label gate stopped a dry run, which changes nothing: %v", err)
	}
	if !strings.Contains(logged.String(), "gh label create typo") {
		t.Error("a dry run through the label gate should still say a real run would refuse")
	}
}

// A lookup that never gets a definitive answer is not a refusal — it must
// not be reported as a missing label, and it must fail preflight outright
// rather than being carved around by -dry-run the way a real refusal is.
func TestPreflightFailsOutrightWhenTheLabelLookupCannotAnswer(t *testing.T) {
	t.Parallel()
	_, checkout := upstream(t)
	cfg, _ := drainConfig(t, "stream", &ghState{
		Labels:    []string{"ready-for-claude"},
		FailReads: map[string]int{"api label": ghReads},
	})
	cfg.dir = checkout
	cfg.label = "ready-for-claude"
	cfg.dryRun = true

	err := preflight(context.Background(), &cfg)
	if err == nil {
		t.Fatal("preflight should fail when the label lookup cannot answer at all, dry run or not")
	}
	if strings.Contains(err.Error(), "gh label create") {
		t.Errorf("a lookup failure was reported as a missing label: %v", err)
	}
}

// --- version skew between the two halves ---

func TestPluginVersionReadsTheInstalledPlugin(t *testing.T) {
	t.Parallel()
	cfg := fakeClaudeConfig(t, "stream")
	setFakeEnv(&cfg, fakePluginEnv, "0.3.0")

	got, id, scope := pluginVersion(context.Background(), cfg)
	if got != "0.3.0" {
		t.Errorf("pluginVersion = %q, want the installed plugin's version", got)
	}
	if id != "polako@scharissis" {
		t.Errorf("pluginVersion id = %q, want the installed copy's marketplace-qualified id", id)
	}
	if scope != "user" {
		t.Errorf("pluginVersion scope = %q, want the installed copy's scope", scope)
	}
}

// installedPluginVersion is pluginVersion's own read, taking the plugin name
// directly — what status uses, since it carries no -skill to derive one
// from.
func TestInstalledPluginVersionReadsByExplicitName(t *testing.T) {
	t.Parallel()
	cfg := fakeClaudeConfig(t, "stream")
	setFakeEnv(&cfg, fakePluginEnv, "0.3.0")

	got, id, scope := installedPluginVersion(context.Background(), cfg, pluginName)
	if got != "0.3.0" || id != "polako@scharissis" || scope != "user" {
		t.Errorf("installedPluginVersion = %q, %q, %q, want the installed copy's version, id and scope",
			got, id, scope)
	}
}

func TestNamesThisPlugin(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, skill string
		want        bool
	}{
		{name: "this plugin", skill: defaultSkill, want: true},
		{name: "another plugin", skill: "my-fork:implement-issue"},
		{name: "hand-installed skill, no plugin prefix", skill: skillDir},
		{name: "empty", skill: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := namesThisPlugin(tc.skill); got != tc.want {
				t.Errorf("namesThisPlugin(%q) = %v, want %v", tc.skill, got, tc.want)
			}
		})
	}
}

// A -skill with no plugin prefix names a skill copied into ~/.claude/skills.
// It carries no version, and asking the CLI about a plugin by that name would
// answer about something else.
func TestPluginVersionIsEmptyForAHandInstalledSkill(t *testing.T) {
	t.Parallel()
	cfg := fakeClaudeConfig(t, "stream")
	cfg.skill = skillDir
	setFakeEnv(&cfg, fakePluginEnv, "0.3.0")

	if got, _, _ := pluginVersion(context.Background(), cfg); got != "" {
		t.Errorf("pluginVersion = %q, want empty: a hand-installed skill has no version", got)
	}
}

func TestPluginVersionIsEmptyWhenTheCLICannotAnswer(t *testing.T) {
	t.Parallel()
	cfg := fakeClaudeConfig(t, "stream")

	if got, _, _ := pluginVersion(context.Background(), cfg); got != "" {
		t.Errorf("pluginVersion = %q, want empty rather than a guess", got)
	}
}

// The list can hold the same plugin twice, and the entry that drives the run is
// not always the first one. Fed straight to the selection so the shapes a real
// `plugin list --json` produces can be written out literally.
func TestPluginVersionPicksTheCopyThatWillRun(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		list      string
		want      string
		wantID    string
		wantScope string
		why       string
	}{{
		name: "sole match",
		list: `[{"id":"some-other-plugin@elsewhere","version":"9.9.9","scope":"user"},
		        {"id":"polako@scharissis","version":"0.3.0","scope":"user"}]`,
		want:      "0.3.0",
		wantID:    "polako@scharissis",
		wantScope: "user",
		why:       "one copy installed, so there is nothing to choose between",
	}, {
		// The reason this issue exists: a --plugin-dir copy loaded alongside a
		// user-scope install of the same name, which is how a tip skill gets
		// tested against a tip binary. The session copy replaces the installed
		// one outright, and it is listed second.
		name: "session copy behind a user install",
		list: `[{"id":"polako@scharissis","version":"0.1.0","scope":"user"},
		        {"id":"polako@inline","version":"0.6.1","scope":"session"}]`,
		want:      "0.6.1",
		wantID:    "polako@inline",
		wantScope: "session",
		why:       "the session copy is the one that drives the run",
	}, {
		name: "two copies with no scope to separate them",
		list: `[{"id":"polako@scharissis","version":"0.1.0","scope":"user"},
		        {"id":"polako@a-fork","version":"0.6.1","scope":"user"}]`,
		why: "no honest answer, and a wrong version is worse than none",
	}, {
		name: "two session copies",
		list: `[{"id":"polako@one","version":"0.1.0","scope":"session"},
		        {"id":"polako@two","version":"0.6.1","scope":"session"}]`,
		why: "narrowing to session scope did not get it down to one",
	}, {
		name: "duplicates that agree",
		list: `[{"id":"polako@scharissis","version":"0.6.1","scope":"user"},
		        {"id":"polako@a-mirror","version":"0.6.1","scope":"user"}]`,
		want:   "0.6.1",
		wantID: "",
		why:    "the version is unambiguous but the marketplace is not, so the id is withheld",
	}, {
		name: "a disabled duplicate beside an enabled one",
		list: `[{"id":"polako@a-fork","version":"0.1.0","scope":"user","enabled":false},
		        {"id":"polako@scharissis","version":"0.6.1","scope":"user","enabled":true}]`,
		want:      "0.6.1",
		wantID:    "polako@scharissis",
		wantScope: "user",
		why:       "a disabled copy never loads, so it is not one of the copies to choose between",
	}, {
		name: "the only copy is disabled",
		list: `[{"id":"polako@scharissis","version":"0.6.1","scope":"user","enabled":false}]`,
		why:  "nothing will load it, so no version drove the run",
	}, {
		name:      "a CLI that does not report enabled",
		list:      `[{"id":"polako@scharissis","version":"0.6.1","scope":"user"}]`,
		want:      "0.6.1",
		wantID:    "polako@scharissis",
		wantScope: "user",
		why:       "an absent field is not a disabled plugin",
	}, {
		name: "no match",
		list: `[{"id":"some-other-plugin@elsewhere","version":"9.9.9","scope":"user"}]`,
		why:  "the plugin is not installed at all",
	}, {
		name: "empty list",
		list: `[]`,
		why:  "nothing installed",
	}, {
		name: "output that is not the list",
		list: `not json`,
		why:  "a CLI answering with something else is not a version",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			got, gotID, gotScope := installedVersion([]byte(tc.list), pluginName)
			if got != tc.want {
				t.Errorf("installedVersion = %q, want %q — %s", got, tc.want, tc.why)
			}
			if gotID != tc.wantID {
				t.Errorf("installedVersion id = %q, want %q — %s", gotID, tc.wantID, tc.why)
			}
			if gotScope != tc.wantScope {
				t.Errorf("installedVersion scope = %q, want %q — %s", gotScope, tc.wantScope, tc.why)
			}
		})
	}
}

func TestWarnOnVersionSkew(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name           string
		skill          string
		binary, plugin string
		warn           bool
	}{
		{name: "matched release", binary: "0.4.0", plugin: "0.4.0"},
		{name: "matched despite the module's v prefix", binary: "v0.4.0", plugin: "0.4.0"},
		{name: "skewed", binary: "0.4.0", plugin: "0.3.0", warn: true},
		// A build from a clone reports a revision. That is an unreleased
		// binary, not a skew, and warning every time would train the operator
		// to ignore the message that matters.
		{name: "unreleased binary", binary: "a1b2c3d4e5f6", plugin: "0.3.0"},
		{name: "dirty clone build", binary: "a1b2c3d4e5f6+dirty", plugin: "0.3.0"},
		{name: "nothing to compare", binary: "", plugin: "0.3.0"},
		{name: "no plugin installed", binary: "0.4.0", plugin: ""},
		// -skill may name another plugin entirely, which has its own version
		// line. Comparing it against this binary would warn on every run of a
		// deliberate configuration, and name the wrong plugin doing it.
		{name: "another plugin's skill", skill: "my-fork:implement-issue", binary: "0.4.0", plugin: "1.2.0"},
		{name: "hand-installed skill", skill: skillDir, binary: "0.4.0", plugin: "1.2.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			buf := captureLog(t)
			skill := tc.skill
			if skill == "" {
				skill = defaultSkill
			}
			warnOnVersionSkew(tc.binary, config{ui: testUI(t), skill: skill, pluginVersion: tc.plugin})

			out := buf.String()
			got := strings.Contains(out, "version skew")
			if got != tc.warn {
				t.Errorf("warned = %v, want %v\nlog: %s", got, tc.warn, out)
			}
			if !tc.warn {
				return
			}
			if !strings.Contains(out, tc.plugin) || !strings.Contains(out, tc.binary) {
				t.Errorf("the warning has to name both versions, got: %s", out)
			}
			if !strings.Contains(out, "polako update") {
				t.Errorf("the warning has to point at `polako update`, got: %s", out)
			}
		})
	}
}

// versionSkewGate is the escalation #254 adds: only the installed skill
// being strictly *behind* the binary refuses, because that is the direction
// #239 showed a real cost regression in — a newer or ambiguous mismatch stays
// warnOnVersionSkew's business alone, unchanged by this gate.
func TestVersionSkewGateRefusesOnlyWhenTheSkillIsBehind(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name           string
		skill          string
		binary, plugin string
		ignoreSkew     bool
		refused        bool
	}{
		{name: "matched release", binary: "0.4.0", plugin: "0.4.0"},
		{name: "plugin one patch behind", binary: "0.4.1", plugin: "0.4.0", refused: true},
		{name: "plugin a minor version behind", binary: "0.5.0", plugin: "0.4.0", refused: true},
		{name: "plugin a major version behind", binary: "1.0.0", plugin: "0.9.9", refused: true},
		{name: "plugin ahead of the binary — a deliberate dev setup", binary: "0.4.0", plugin: "0.5.0"},
		{name: "unreleased binary", binary: "a1b2c3d4e5f6", plugin: "0.3.0"},
		{name: "dirty clone build", binary: "a1b2c3d4e5f6+dirty", plugin: "0.3.0"},
		{name: "no plugin installed", binary: "0.4.0", plugin: ""},
		{name: "another plugin's skill", skill: "my-fork:implement-issue", binary: "0.4.0", plugin: "0.3.0"},
		{name: "hand-installed skill", skill: skillDir, binary: "0.4.0", plugin: "0.3.0"},
		// The override: the same "-ignore-skew consented to it" shape
		// -ungated has with queueGate, resolved inside the gate itself rather
		// than left for the call site to reconstruct.
		{name: "behind, but -ignore-skew consented to it", binary: "0.4.1", plugin: "0.4.0", ignoreSkew: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			skill := tc.skill
			if skill == "" {
				skill = defaultSkill
			}
			err := versionSkewGate(tc.binary, config{skill: skill, pluginVersion: tc.plugin, ignoreSkew: tc.ignoreSkew})
			if tc.refused && err == nil {
				t.Fatalf("gate let a behind-the-binary skill (%s behind %s) through", tc.plugin, tc.binary)
			}
			if !tc.refused && err != nil {
				t.Fatalf("gate refused a pair it is not about: %v", err)
			}
			if err == nil {
				return
			}
			for _, want := range []string{tc.binary, tc.plugin, "-ignore-skew", "#239", "#217", "#216", "#225"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("refusal does not mention %q, want the cost regression named alongside the versions: %v", want, err)
				}
			}
		})
	}
}

func TestSemverLess(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		a, b [3]int
		want bool
	}{
		{[3]int{0, 4, 0}, [3]int{0, 4, 1}, true},
		{[3]int{0, 4, 1}, [3]int{0, 4, 0}, false},
		{[3]int{0, 4, 0}, [3]int{0, 4, 0}, false},
		{[3]int{0, 4, 9}, [3]int{0, 5, 0}, true},
		{[3]int{0, 9, 9}, [3]int{1, 0, 0}, true},
		{[3]int{1, 0, 0}, [3]int{0, 9, 9}, false},
	} {
		if got := semverLess(tc.a, tc.b); got != tc.want {
			t.Errorf("semverLess(%v, %v) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

// The skew warning's remedy and docs/install.md must not drift: install.md
// is the canonical wording of how to fix a mismatch, so if the two can
// disagree, one is wrong the next time either changes.
func TestVersionSkewRemedyAgreesWithInstallDocs(t *testing.T) {
	t.Parallel()
	const wantCmd = "polako update"
	if docs := readRepoFile(t, "docs", "install.md"); !strings.Contains(docs, wantCmd) {
		t.Fatalf("docs/install.md no longer shows %q — move this test and warnOnVersionSkew's remedy with it", wantCmd)
	}
	buf := captureLog(t)
	warnOnVersionSkew("0.4.0", config{ui: testUI(t), skill: defaultSkill, pluginVersion: "0.3.0"})
	if !strings.Contains(buf.String(), wantCmd) {
		t.Errorf("skew warning does not print the docs' update command %q\nlog: %s", wantCmd, buf.String())
	}
}

// The manifests are the source of truth for the version, so a release binary
// and the plugin it drives compare equal only if this holds.
func TestParseSemverRejectsWhatIsNotARelease(t *testing.T) {
	t.Parallel()
	for _, ok := range []string{"0.0.0", "0.4.0", "10.20.30"} {
		if _, err := parseSemver(ok); err != nil {
			t.Errorf("parseSemver(%q) = %v, want it accepted", ok, err)
		}
	}
	for _, bad := range []string{"", "0.4", "0.4.0.1", "v0.4.0", "0.4.0-rc1", "01.4.0", "-1.4.0", "a.b.c"} {
		if _, err := parseSemver(bad); err == nil {
			t.Errorf("parseSemver(%q) succeeded, want it rejected", bad)
		}
	}
}

// The pseudo-version a plain `go build` inside a module records is the shape
// most likely to be mistaken for a release, and warning on it would fire on
// every developer build.
func TestReleaseVersionRejectsAPseudoVersion(t *testing.T) {
	t.Parallel()
	if v, _, ok := releaseVersion("v0.0.0-20260825064232-a0aabd243c60"); ok {
		t.Errorf("releaseVersion accepted a pseudo-version as %q", v)
	}
	if v, parts, ok := releaseVersion("v0.4.0"); !ok || v != "0.4.0" {
		t.Errorf("releaseVersion(v0.4.0) = %q, %v; want the bare version", v, ok)
	} else if want := [3]int{0, 4, 0}; parts != want {
		t.Errorf("releaseVersion(v0.4.0) parts = %v, want %v", parts, want)
	}
}

// --- CLI capability gating (-effort) ---

// effortFlagGate fails the run before it starts when -effort is set and the
// installed CLI has no --effort — otherwise the usage error lands an hour in,
// looks like a crash, and burns every resume. The message names the CLI
// version so the operator knows which install to update.
func TestEffortFlagGate(t *testing.T) {
	t.Setenv(fakeClaudeEnv, "stream") // any mode: --help is argv-dispatched ahead of it
	cfg := config{claudeBin: fakeCLI(t), dir: t.TempDir()}

	// Unset -effort: no probe, no error, whatever the CLI is.
	if err := effortFlagGate(context.Background(), cfg); err != nil {
		t.Errorf("effortFlagGate with no -effort = %v, want nil", err)
	}

	cfg.effort = "medium"
	if err := effortFlagGate(context.Background(), cfg); err != nil {
		t.Errorf("a CLI whose --help lists --effort should pass, got %v", err)
	}

	// -remediation-effort is the other flag that gates: it maps to the same
	// --effort, so a CLI that lists it passes with only that one set.
	remOnly := config{claudeBin: cfg.claudeBin, dir: cfg.dir, remediationEffort: "medium"}
	if err := effortFlagGate(context.Background(), remOnly); err != nil {
		t.Errorf("-remediation-effort alone should gate like -effort, got %v", err)
	}

	// -effort-by-size can put --effort on the argv too, so it gates like the
	// other two: alone it passes against a CLI that lists --effort.
	sizeOnly := config{claudeBin: cfg.claudeBin, dir: cfg.dir, effortBySize: "S=medium"}
	if err := effortFlagGate(context.Background(), sizeOnly); err != nil {
		t.Errorf("-effort-by-size alone should gate like -effort, got %v", err)
	}

	t.Setenv(fakeEffortHelpEnv, "0") // model an older CLI
	if err := effortFlagGate(context.Background(), sizeOnly); err == nil ||
		!strings.Contains(err.Error(), "-effort-by-size") {
		t.Errorf("the error should name -effort-by-size, got %v", err)
	}
	err := effortFlagGate(context.Background(), cfg)
	if err == nil {
		t.Fatal("effortFlagGate let -effort through against a CLI with no --effort")
	}
	if !strings.Contains(err.Error(), "2.1.99") {
		t.Errorf("the error should name the CLI version, got %v", err)
	}
	// A bad -remediation-effort against an old CLI names that flag, not -effort.
	if err := effortFlagGate(context.Background(), remOnly); err == nil ||
		!strings.Contains(err.Error(), "-remediation-effort") {
		t.Errorf("the error should name -remediation-effort, got %v", err)
	}
	// Both set: name both, so the operator does not fix one and hit the other.
	both := config{claudeBin: cfg.claudeBin, dir: cfg.dir, effort: "high", remediationEffort: "medium"}
	err = effortFlagGate(context.Background(), both)
	if err == nil || !strings.Contains(err.Error(), "-effort high") ||
		!strings.Contains(err.Error(), "-remediation-effort medium") {
		t.Errorf("with both effort flags set the error should name both, got %v", err)
	}

	// A probe that will not run at all is best-effort: warn, don't block —
	// a broken CLI has its own louder failure coming.
	broken := cfg
	broken.claudeBin = filepath.Join(t.TempDir(), "no-such-claude")
	if err := effortFlagGate(context.Background(), broken); err != nil {
		t.Errorf("a failed --help probe should not block the run, got %v", err)
	}
}
