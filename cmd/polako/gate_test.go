package main

import (
	"context"
	"log"
	"os"
	"strings"
	"testing"
)

// The gate itself: exactly one shape refuses — a public repository with no
// -label and no -ungated — because that is the one shape where "anyone can
// open an issue" and "an unattended agent implements open issues" meet.
func TestQueueGateRefusesOnlyThePublicUnlabelledQueue(t *testing.T) {
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
			err := queueGate(tc.visibility, tc.label, tc.ungated)
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

// The wiring: preflight reads visibility off the same `gh repo view` call that
// names the repository, and refuses before anything is written or run.
func TestPreflightRefusesAnUngatedPublicQueue(t *testing.T) {
	_, checkout := upstream(t)
	cfg, _ := drainConfig(t, "stream", &ghState{Visibility: "PUBLIC"})
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

// A dry run writes nothing and runs nothing, so it is allowed through to look —
// seeing the queue is how an operator decides what to label — but the gate
// still tells them a real run would refuse.
func TestPreflightLetsADryRunLookThroughTheGate(t *testing.T) {
	_, checkout := upstream(t)
	cfg, _ := drainConfig(t, "stream", &ghState{Visibility: "PUBLIC"})
	cfg.dir = checkout
	cfg.dryRun = true

	var logged strings.Builder
	log.SetOutput(&logged)
	defer log.SetOutput(os.Stderr)

	if err := preflight(context.Background(), &cfg); err != nil {
		t.Fatalf("the gate stopped a dry run, which changes nothing: %v", err)
	}
	if !strings.Contains(logged.String(), "-label") {
		t.Error("a dry run through the gate should still say a real run would refuse")
	}
}

// --- version skew between the two halves ---

func TestPluginVersionReadsTheInstalledPlugin(t *testing.T) {
	cfg := fakeClaudeConfig(t, "stream")
	t.Setenv(fakePluginEnv, "0.3.0")

	got, id := pluginVersion(context.Background(), cfg)
	if got != "0.3.0" {
		t.Errorf("pluginVersion = %q, want the installed plugin's version", got)
	}
	if id != "polako@scharissis" {
		t.Errorf("pluginVersion id = %q, want the installed copy's marketplace-qualified id", id)
	}
}

// A -skill with no plugin prefix names a skill copied into ~/.claude/skills.
// It carries no version, and asking the CLI about a plugin by that name would
// answer about something else.
func TestPluginVersionIsEmptyForAHandInstalledSkill(t *testing.T) {
	cfg := fakeClaudeConfig(t, "stream")
	cfg.skill = skillDir
	t.Setenv(fakePluginEnv, "0.3.0")

	if got, _ := pluginVersion(context.Background(), cfg); got != "" {
		t.Errorf("pluginVersion = %q, want empty: a hand-installed skill has no version", got)
	}
}

func TestPluginVersionIsEmptyWhenTheCLICannotAnswer(t *testing.T) {
	cfg := fakeClaudeConfig(t, "stream")

	if got, _ := pluginVersion(context.Background(), cfg); got != "" {
		t.Errorf("pluginVersion = %q, want empty rather than a guess", got)
	}
}

// The list can hold the same plugin twice, and the entry that drives the run is
// not always the first one. Fed straight to the selection so the shapes a real
// `plugin list --json` produces can be written out literally.
func TestPluginVersionPicksTheCopyThatWillRun(t *testing.T) {
	for _, tc := range []struct {
		name   string
		list   string
		want   string
		wantID string
		why    string
	}{{
		name: "sole match",
		list: `[{"id":"some-other-plugin@elsewhere","version":"9.9.9","scope":"user"},
		        {"id":"polako@scharissis","version":"0.3.0","scope":"user"}]`,
		want:   "0.3.0",
		wantID: "polako@scharissis",
		why:    "one copy installed, so there is nothing to choose between",
	}, {
		// The reason this issue exists: a --plugin-dir copy loaded alongside a
		// user-scope install of the same name, which is how a tip skill gets
		// tested against a tip binary. The session copy replaces the installed
		// one outright, and it is listed second.
		name: "session copy behind a user install",
		list: `[{"id":"polako@scharissis","version":"0.1.0","scope":"user"},
		        {"id":"polako@inline","version":"0.6.1","scope":"session"}]`,
		want:   "0.6.1",
		wantID: "polako@inline",
		why:    "the session copy is the one that drives the run",
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
		want:   "0.6.1",
		wantID: "polako@scharissis",
		why:    "a disabled copy never loads, so it is not one of the copies to choose between",
	}, {
		name: "the only copy is disabled",
		list: `[{"id":"polako@scharissis","version":"0.6.1","scope":"user","enabled":false}]`,
		why:  "nothing will load it, so no version drove the run",
	}, {
		name:   "a CLI that does not report enabled",
		list:   `[{"id":"polako@scharissis","version":"0.6.1","scope":"user"}]`,
		want:   "0.6.1",
		wantID: "polako@scharissis",
		why:    "an absent field is not a disabled plugin",
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
			got, gotID := installedVersion([]byte(tc.list), pluginName)
			if got != tc.want {
				t.Errorf("installedVersion = %q, want %q — %s", got, tc.want, tc.why)
			}
			if gotID != tc.wantID {
				t.Errorf("installedVersion id = %q, want %q — %s", gotID, tc.wantID, tc.why)
			}
		})
	}
}

func TestWarnOnVersionSkew(t *testing.T) {
	for _, tc := range []struct {
		name           string
		skill          string
		binary, plugin string
		id             string
		warn           bool
	}{
		{name: "matched release", binary: "0.4.0", plugin: "0.4.0"},
		{name: "matched despite the module's v prefix", binary: "v0.4.0", plugin: "0.4.0"},
		// A resolvable id: the remedy prints a `plugin update` command that names it.
		{name: "skewed with a resolvable id", binary: "0.4.0", plugin: "0.3.0", id: "polako@acme", warn: true},
		// No id — copies from more than one marketplace. The warning still fires
		// and still names both versions, but drops the exact command.
		{name: "skewed without an id", binary: "0.4.0", plugin: "0.3.0", warn: true},
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
			warnOnVersionSkew(tc.binary, config{skill: skill, pluginVersion: tc.plugin, pluginID: tc.id})

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
			if !strings.Contains(out, "docs/install.md") {
				t.Errorf("the warning has to point at docs/install.md, got: %s", out)
			}
			switch {
			case tc.id != "":
				// The remedy command has to carry the @-qualified id, or it is
				// the broken command this issue (#190) is about.
				if !strings.Contains(out, "claude plugin update "+tc.id) {
					t.Errorf("skewed with id %q: want a `claude plugin update %s` command, got: %s", tc.id, tc.id, out)
				}
			default:
				// No id to name, so no exact `plugin update` command — guessing
				// a marketplace is worse than sending the operator to the docs.
				if strings.Contains(out, "claude plugin update ") {
					t.Errorf("no id available: the warning must not print a `claude plugin update` command, got: %s", out)
				}
			}
		})
	}
}

// versionSkewGate is the escalation #254 adds: only the installed skill
// being strictly *behind* the binary refuses, because that is the direction
// #239 showed a real cost regression in — a newer or ambiguous mismatch stays
// warnOnVersionSkew's business alone, unchanged by this gate.
func TestVersionSkewGateRefusesOnlyWhenTheSkillIsBehind(t *testing.T) {
	for _, tc := range []struct {
		name           string
		skill          string
		binary, plugin string
		id             string
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
			err := versionSkewGate(tc.binary, config{skill: skill, pluginVersion: tc.plugin, pluginID: tc.id, ignoreSkew: tc.ignoreSkew})
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

// The skew warning's remedy and the update command in docs/install.md must not
// drift: install.md is the canonical wording of the `claude plugin update`
// trap (issue #190), so if the two can disagree, one is wrong the next time the
// CLI changes.
func TestVersionSkewRemedyAgreesWithInstallDocs(t *testing.T) {
	const wantCmd = "claude plugin marketplace update scharissis && claude plugin update polako@scharissis"
	if docs := readRepoFile(t, "docs", "install.md"); !strings.Contains(docs, wantCmd) {
		t.Fatalf("docs/install.md no longer shows %q — move this test and warnOnVersionSkew's remedy with it", wantCmd)
	}
	buf := captureLog(t)
	warnOnVersionSkew("0.4.0", config{
		skill:         defaultSkill,
		pluginVersion: "0.3.0",
		pluginID:      "polako@scharissis",
	})
	if !strings.Contains(buf.String(), wantCmd) {
		t.Errorf("skew warning does not print the docs' update command %q\nlog: %s", wantCmd, buf.String())
	}
}

// The manifests are the source of truth for the version, so a release binary
// and the plugin it drives compare equal only if this holds.
func TestParseSemverRejectsWhatIsNotARelease(t *testing.T) {
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
	if v, _, ok := releaseVersion("v0.0.0-20260825064232-a0aabd243c60"); ok {
		t.Errorf("releaseVersion accepted a pseudo-version as %q", v)
	}
	if v, parts, ok := releaseVersion("v0.4.0"); !ok || v != "0.4.0" {
		t.Errorf("releaseVersion(v0.4.0) = %q, %v; want the bare version", v, ok)
	} else if want := [3]int{0, 4, 0}; parts != want {
		t.Errorf("releaseVersion(v0.4.0) parts = %v, want %v", parts, want)
	}
}
