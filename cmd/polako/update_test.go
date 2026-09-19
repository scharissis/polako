package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestVerbUsageListsUpdate(t *testing.T) {
	var b strings.Builder
	verbUsage(&b)
	if !strings.Contains(b.String(), "\n  update ") {
		t.Errorf("verbUsage does not list `update`:\n%s", b.String())
	}
}

// The same flags-only contract every other verb's entry point holds to; see
// TestRunStatusRejectsAnArgument in main_test.go for the sibling this mirrors.
func TestRunUpdateRejectsAnArgument(t *testing.T) {
	err := runUpdate(context.Background(), []string{"12"}, &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "update takes flags only") {
		t.Errorf("err = %v, want a complaint about the argument", err)
	}
}

// --- publishedVersion / parseMarketplaceVersion ---

func TestParseMarketplaceVersion(t *testing.T) {
	for _, tc := range []struct {
		name    string
		raw     string
		want    string
		wantErr string
	}{{
		name: "the real shape",
		raw:  `{"name":"scharissis","plugins":[{"name":"polako","source":{"source":"github","repo":"scharissis/polako","ref":"polako--v0.23.0"}}]}`,
		want: "0.23.0",
	}, {
		name: "another plugin listed first is ignored",
		raw: `{"plugins":[{"name":"other","source":{"ref":"other--v9.9.9"}},` +
			`{"name":"polako","source":{"ref":"polako--v1.2.3"}}]}`,
		want: "1.2.3",
	}, {
		name:    "no polako entry at all",
		raw:     `{"plugins":[{"name":"other","source":{"ref":"other--v9.9.9"}}]}`,
		wantErr: `no "polako" entry`,
	}, {
		name:    "ref carries no polako--v prefix",
		raw:     `{"plugins":[{"name":"polako","source":{"ref":"v0.23.0"}}]}`,
		wantErr: "is not polako--v<version>",
	}, {
		name:    "ref prefix but not a release",
		raw:     `{"plugins":[{"name":"polako","source":{"ref":"polako--vnope"}}]}`,
		wantErr: "does not name a release",
	}, {
		name:    "not json at all",
		raw:     "not json",
		wantErr: "parsing the marketplace file",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseMarketplaceVersion([]byte(tc.raw))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want it to mention %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("parseMarketplaceVersion = %q, want %q", got, tc.want)
			}
		})
	}
}

func updateGhCfg(t *testing.T, st *ghState) config {
	t.Helper()
	if st.Repo == "" {
		st.Repo = "example/repo"
	}
	path := filepath.Join(t.TempDir(), "gh-state.json")
	if err := writeGhState(path, st); err != nil {
		t.Fatalf("writing fake gh state: %v", err)
	}
	t.Setenv(fakeGhEnv, path)
	return config{
		dir:         t.TempDir(),
		ghBin:       fakeCLI(t),
		ghRetryWait: time.Millisecond,
	}
}

func TestPublishedVersionReadsTheMarketplaceFile(t *testing.T) {
	cfg := updateGhCfg(t, &ghState{PublishedRef: "polako--v0.24.0"})
	got, err := publishedVersion(context.Background(), cfg)
	if err != nil {
		t.Fatalf("publishedVersion: %v", err)
	}
	if got != "0.24.0" {
		t.Errorf("publishedVersion = %q, want 0.24.0", got)
	}
}

func TestPublishedVersionIsARealErrorOnFailure(t *testing.T) {
	// Empty PublishedRef is the fake's "could not read this" fixture — unlike
	// probeUsage, update's own explicit command must not swallow this.
	cfg := updateGhCfg(t, &ghState{})
	if _, err := publishedVersion(context.Background(), cfg); err == nil {
		t.Fatal("publishedVersion should report a real error, not go silent")
	}
}

func TestPublishedVersionQuietSilentOnFailure(t *testing.T) {
	cfg := updateGhCfg(t, &ghState{})
	if _, ok := publishedVersionQuiet(context.Background(), cfg); ok {
		t.Error("publishedVersionQuiet should stay silent, not error, on a failed read")
	}
}

func TestPublishedVersionQuietReadsTheMarketplaceFile(t *testing.T) {
	cfg := updateGhCfg(t, &ghState{PublishedRef: "polako--v0.24.0"})
	got, ok := publishedVersionQuiet(context.Background(), cfg)
	if !ok || got != "0.24.0" {
		t.Errorf("publishedVersionQuiet = %q, %v, want 0.24.0, true", got, ok)
	}
}

// --- the passive notice (docs/plans/update.md ticket 2) ---

func TestUpdateAvailableLine(t *testing.T) {
	for _, tc := range []struct {
		name                string
		binary, plugin, pub string
		wantLine            bool
	}{
		{name: "both current", binary: "0.23.0", plugin: "0.23.0", pub: "0.23.0"},
		{name: "binary behind", binary: "0.23.0", plugin: "0.24.0", pub: "0.24.0", wantLine: true},
		{name: "plugin behind", binary: "0.24.0", plugin: "0.23.0", pub: "0.24.0", wantLine: true},
		{name: "both behind", binary: "0.23.0", plugin: "0.23.0", pub: "0.24.0", wantLine: true},
		{name: "module v prefix normalizes", binary: "v0.23.0", plugin: "0.23.0", pub: "0.23.0"},
		// A clone build reports a revision, not a release — not staleness,
		// an unreleased binary, the same rule skewComparison applies.
		{name: "unreleased binary", binary: "a1b2c3d4e5f6", plugin: "0.23.0", pub: "0.24.0"},
		{name: "no binary version at all", binary: "", plugin: "0.23.0", pub: "0.24.0"},
		// The plugin not being installed doesn't silence a binary that is
		// itself behind.
		{name: "plugin not installed, binary behind", binary: "0.23.0", plugin: "", pub: "0.24.0", wantLine: true},
		{name: "plugin not installed, binary current", binary: "0.24.0", plugin: "", pub: "0.24.0"},
		{name: "no published version read", binary: "0.23.0", plugin: "0.23.0", pub: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := updateAvailableLine(tc.binary, tc.plugin, tc.pub)
			if (got != "") != tc.wantLine {
				t.Fatalf("updateAvailableLine(%q, %q, %q) = %q, want a line: %v",
					tc.binary, tc.plugin, tc.pub, got, tc.wantLine)
			}
			if !tc.wantLine {
				return
			}
			if !strings.Contains(got, tc.pub) || !strings.Contains(got, tc.binary) {
				t.Errorf("line does not name the published and binary versions: %s", got)
			}
			if !strings.Contains(got, "polako update") {
				t.Errorf("line does not point at `polako update`: %s", got)
			}
		})
	}
}

func TestUpdateAvailableLineNamesPluginNotInstalled(t *testing.T) {
	got := updateAvailableLine("0.23.0", "", "0.24.0")
	if !strings.Contains(got, "plugin not installed") {
		t.Errorf("line = %q, want it to say the plugin is not installed", got)
	}
}

func TestReadPublishedVersionSilentWhenSkillNamesAnotherPlugin(t *testing.T) {
	cfg := updateGhCfg(t, &ghState{PublishedRef: "polako--v0.24.0"})
	cfg.skill = "my-fork:implement-issue"
	if _, ok := readPublishedVersion(context.Background(), cfg); ok {
		t.Error("readPublishedVersion should stay silent when -skill names another plugin")
	}
}

func TestReadPublishedVersionSilentOnAFailedRead(t *testing.T) {
	cfg := updateGhCfg(t, &ghState{})
	cfg.skill = defaultSkill
	if _, ok := readPublishedVersion(context.Background(), cfg); ok {
		t.Error("readPublishedVersion should stay silent, not error, on a failed read")
	}
}

func TestReadPublishedVersionReadsTheSameFilePublishedVersionDoes(t *testing.T) {
	cfg := updateGhCfg(t, &ghState{PublishedRef: "polako--v0.24.0"})
	cfg.skill = defaultSkill
	got, ok := readPublishedVersion(context.Background(), cfg)
	if !ok || got != "0.24.0" {
		t.Errorf("readPublishedVersion = %q, %v, want 0.24.0, true", got, ok)
	}
}

func TestUpdateNoticeLineEndToEnd(t *testing.T) {
	cfg := updateGhCfg(t, &ghState{PublishedRef: "polako--v0.24.0"})
	cfg.skill = defaultSkill
	cfg.pluginVersion = "0.23.0"

	got := updateNoticeLine(context.Background(), "0.23.0", cfg)
	if !strings.Contains(got, "update available: polako 0.24.0 is out (binary 0.23.0, plugin 0.23.0)") {
		t.Errorf("updateNoticeLine = %q, want it to name all three versions", got)
	}
}

func TestUpdateNoticeLineSilentWhenCurrent(t *testing.T) {
	cfg := updateGhCfg(t, &ghState{PublishedRef: "polako--v0.23.0"})
	cfg.skill = defaultSkill
	cfg.pluginVersion = "0.23.0"

	if got := updateNoticeLine(context.Background(), "0.23.0", cfg); got != "" {
		t.Errorf("updateNoticeLine = %q, want silence when both halves are current", got)
	}
}

// --- the plugin half ---

func TestResolvePluginPlanFrom(t *testing.T) {
	for _, tc := range []struct {
		name          string
		list          string
		wantState     pluginPlanState
		wantVersion   string
		wantID        string
		wantScope     string
		wantMarketkey string
	}{{
		name:          "found, one unambiguous copy",
		list:          `[{"id":"polako@scharissis","version":"0.3.0","scope":"user"}]`,
		wantState:     pluginFound,
		wantVersion:   "0.3.0",
		wantID:        "polako@scharissis",
		wantScope:     "user",
		wantMarketkey: "scharissis",
	}, {
		name:      "not installed",
		list:      `[]`,
		wantState: pluginNotInstalled,
	}, {
		name: "ambiguous — two marketplaces agreeing on a version",
		list: `[{"id":"polako@scharissis","version":"0.6.1","scope":"user"},
		        {"id":"polako@a-mirror","version":"0.6.1","scope":"user"}]`,
		wantState:   pluginAmbiguous,
		wantVersion: "0.6.1",
	}, {
		// A --plugin-dir load: session-scope, replacing the installed copy for
		// this session alone. "session" isn't a --scope `plugin update` takes,
		// and its marketplace half ("inline" here) isn't a real marketplace —
		// nothing for update to run `plugin marketplace update` against.
		name:        "a --plugin-dir session load",
		list:        `[{"id":"polako@inline","version":"0.6.1","scope":"session"}]`,
		wantState:   pluginSessionLoad,
		wantVersion: "0.6.1",
		wantID:      "polako@inline",
		wantScope:   "session",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			got := resolvePluginPlanFrom([]byte(tc.list), pluginName)
			if got.state != tc.wantState {
				t.Errorf("state = %v, want %v", got.state, tc.wantState)
			}
			if got.version != tc.wantVersion || got.id != tc.wantID || got.scope != tc.wantScope {
				t.Errorf("got %+v, want version=%q id=%q scope=%q", got, tc.wantVersion, tc.wantID, tc.wantScope)
			}
			if got.marketplace != tc.wantMarketkey {
				t.Errorf("marketplace = %q, want %q", got.marketplace, tc.wantMarketkey)
			}
		})
	}
}

func TestPluginSummary(t *testing.T) {
	for _, tc := range []struct {
		name       string
		plan       pluginPlan
		published  string
		wantAction bool
		want       []string
	}{{
		name:      "hand-installed skill",
		plan:      pluginPlan{state: pluginNotConfigured, skill: skillDir},
		published: "0.24.0",
		want:      []string{skillDir, "hand-installed"},
	}, {
		name:      "not installed",
		plan:      pluginPlan{state: pluginNotInstalled},
		published: "0.24.0",
		want:      []string{"marketplace add", "plugin install"},
	}, {
		name:      "ambiguous",
		plan:      pluginPlan{state: pluginAmbiguous, version: "0.23.0"},
		published: "0.24.0",
		want:      []string{"0.23.0", "more than one"},
	}, {
		name:      "a --plugin-dir session load",
		plan:      pluginPlan{state: pluginSessionLoad, version: "0.6.1", id: "polako@inline", scope: "session"},
		published: "0.24.0",
		want:      []string{"polako@inline", "--plugin-dir", "session load"},
	}, {
		name:      "found and current",
		plan:      pluginPlan{state: pluginFound, version: "0.24.0"},
		published: "0.24.0",
		want:      []string{"0.24.0", "already current"},
	}, {
		name:       "found and behind",
		plan:       pluginPlan{state: pluginFound, version: "0.23.0", id: "polako@scharissis", scope: "user", marketplace: "scharissis"},
		published:  "0.24.0",
		wantAction: true,
		want: []string{"0.23.0", "0.24.0", "claude plugin marketplace update scharissis",
			"claude plugin update polako@scharissis --scope user"},
	}} {
		t.Run(tc.name, func(t *testing.T) {
			line, action := pluginSummary(tc.plan, tc.published)
			if action != tc.wantAction {
				t.Errorf("action = %v, want %v (line: %s)", action, tc.wantAction, line)
			}
			for _, want := range tc.want {
				if !strings.Contains(line, want) {
					t.Errorf("line %q does not contain %q", line, want)
				}
			}
		})
	}
}

// --- the binary half ---

// A go-installed binary's module version, and a release's -ldflags stamp,
// both carry the "v" prefix (a Go module version always does; release.yml
// stamps the `vX.Y.Z` tag verbatim) — but publishedVersion's own answer is
// already stripped (releaseVersion, inside parseMarketplaceVersion). Without
// this normalization binarySummary's `current == published` never matches
// even when they name the same release: exactly the bug the high-level
// review of this branch (PLAN.md) caught, since every other test here
// hand-builds binaryPlan.current without the prefix.
func TestNormalizeBuildVersion(t *testing.T) {
	for _, tc := range []struct {
		name string
		tier buildTier
		v    string
		want string
	}{
		{name: "module version keeps the v stripped", tier: tierModule, v: "v0.23.0", want: "0.23.0"},
		{name: "stamped release keeps the v stripped", tier: tierStamped, v: "v0.23.0", want: "0.23.0"},
		{name: "vcs revision is untouched", tier: tierVCS, v: "a1b2c3d4e5f6", want: "a1b2c3d4e5f6"},
		{name: "unknown tier is untouched", tier: tierUnknown, v: "", want: ""},
		{name: "a pseudo-version doesn't parse as a release, so it passes through",
			tier: tierModule, v: "v0.23.1-0.20240101120000-abcdef123456", want: "v0.23.1-0.20240101120000-abcdef123456"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeBuildVersion(tc.tier, tc.v); got != tc.want {
				t.Errorf("normalizeBuildVersion(%v, %q) = %q, want %q", tc.tier, tc.v, got, tc.want)
			}
		})
	}
}

func TestBinarySummary(t *testing.T) {
	cfg := config{dir: t.TempDir(), goBin: fakeCLI(t)}
	t.Setenv(fakeGoEnv, "1")

	for _, tc := range []struct {
		name       string
		plan       binaryPlan
		published  string
		wantAction bool
		want       []string
	}{{
		name:      "module, current",
		plan:      binaryPlan{tier: tierModule, current: "0.24.0"},
		published: "0.24.0",
		want:      []string{"0.24.0", "already current"},
	}, {
		name:       "module, behind",
		plan:       binaryPlan{tier: tierModule, current: "0.23.0"},
		published:  "0.24.0",
		wantAction: true,
		want:       []string{"0.23.0", "0.24.0", "go install " + updateModulePath + "@v0.24.0"},
	}, {
		name:      "stamped, current",
		plan:      binaryPlan{tier: tierStamped, current: "0.24.0"},
		published: "0.24.0",
		want:      []string{"0.24.0", "already current"},
	}, {
		name:      "stamped, behind",
		plan:      binaryPlan{tier: tierStamped, current: "0.23.0"},
		published: "0.24.0",
		want:      []string{releaseAssetName("0.24.0"), releaseURL("0.24.0")},
	}, {
		name:      "vcs build",
		plan:      binaryPlan{tier: tierVCS, current: "a1b2c3d4e5f6"},
		published: "0.24.0",
		want:      []string{"a1b2c3d4e5f6", "rebuild it yourself"},
	}, {
		name:      "unknown build",
		plan:      binaryPlan{tier: tierUnknown},
		published: "0.24.0",
		want:      []string{"reinstall or rebuild it yourself"},
	}} {
		t.Run(tc.name, func(t *testing.T) {
			line, action, _ := binarySummary(context.Background(), cfg, tc.plan, tc.published)
			if action != tc.wantAction {
				t.Errorf("action = %v, want %v (line: %s)", action, tc.wantAction, line)
			}
			for _, want := range tc.want {
				if !strings.Contains(line, want) {
					t.Errorf("line %q does not contain %q", line, want)
				}
			}
		})
	}
}

func TestGoInstallDirPrefersGOBINOverGOPATH(t *testing.T) {
	cfg := config{dir: t.TempDir(), goBin: fakeCLI(t)}
	t.Setenv(fakeGoEnv, "1")
	t.Setenv(fakeGoGOBINEnv, "/gobin")
	t.Setenv(fakeGoGOPATHEnv, "/gopath")

	got, err := goInstallDir(context.Background(), cfg)
	if err != nil {
		t.Fatalf("goInstallDir: %v", err)
	}
	if got != "/gobin" {
		t.Errorf("goInstallDir = %q, want GOBIN to win", got)
	}
}

func TestGoInstallDirFallsBackToGOPATHBin(t *testing.T) {
	cfg := config{dir: t.TempDir(), goBin: fakeCLI(t)}
	t.Setenv(fakeGoEnv, "1")
	t.Setenv(fakeGoGOPATHEnv, "/gopath")

	got, err := goInstallDir(context.Background(), cfg)
	if err != nil {
		t.Fatalf("goInstallDir: %v", err)
	}
	if want := filepath.Join("/gopath", "bin"); got != want {
		t.Errorf("goInstallDir = %q, want %q", got, want)
	}
}

// --- applyUpdate orchestration ---

func TestApplyUpdateChecksWithoutWriting(t *testing.T) {
	cfg := config{dir: t.TempDir(), claudeBin: fakeCLI(t), goBin: fakeCLI(t)}
	t.Setenv(fakeClaudeEnv, "stream")
	t.Setenv(fakeGoEnv, "1")
	argsOf := watchClaudeArgs(t)
	buf := captureLog(t)

	plugin := pluginPlan{state: pluginFound, version: "0.23.0", id: "polako@scharissis", scope: "user", marketplace: "scharissis"}
	binary := binaryPlan{tier: tierModule, current: "0.23.0"}

	var out strings.Builder
	if err := applyUpdate(context.Background(), cfg, true, "0.24.0", plugin, binary, &out); err != nil {
		t.Fatalf("applyUpdate: %v", err)
	}
	if !strings.Contains(buf.String(), "-check") {
		t.Errorf("log does not say this was a check: %s", buf.String())
	}
	for _, args := range argsOf() {
		if strings.HasPrefix(args, "plugin marketplace update") || strings.HasPrefix(args, "plugin update") ||
			strings.HasPrefix(args, "install ") {
			t.Errorf("-check ran a write: %q", args)
		}
	}
}

func TestApplyUpdateRunsBothHalvesWhenBehind(t *testing.T) {
	cfg := config{dir: t.TempDir(), claudeBin: fakeCLI(t), goBin: fakeCLI(t)}
	t.Setenv(fakeClaudeEnv, "stream")
	t.Setenv(fakeGoEnv, "1")
	argsOf := watchClaudeArgs(t)

	plugin := pluginPlan{state: pluginFound, version: "0.23.0", id: "polako@scharissis", scope: "user", marketplace: "scharissis"}
	binary := binaryPlan{tier: tierModule, current: "0.23.0"}

	var out strings.Builder
	if err := applyUpdate(context.Background(), cfg, false, "0.24.0", plugin, binary, &out); err != nil {
		t.Fatalf("applyUpdate: %v", err)
	}

	var sawMarketplaceUpdate, sawPluginUpdate, sawGoInstall bool
	for _, args := range argsOf() {
		switch {
		case strings.HasPrefix(args, "plugin marketplace update scharissis"):
			sawMarketplaceUpdate = true
		case strings.HasPrefix(args, "plugin update polako@scharissis --scope user"):
			sawPluginUpdate = true
		case strings.HasPrefix(args, "install "+updateModulePath+"@v0.24.0"):
			sawGoInstall = true
		}
	}
	if !sawMarketplaceUpdate {
		t.Error("did not run `claude plugin marketplace update`")
	}
	if !sawPluginUpdate {
		t.Error("did not run `claude plugin update`")
	}
	if !sawGoInstall {
		t.Error("did not run `go install`")
	}
}

// --- runUpdate end to end ---
//
// Exercises the full wiring — flags, updateConfig, publishedVersion,
// resolvePluginPlan — through the fake gh and claude CLIs. The binary half
// is left unasserted here: resolveBinaryPlan reads this test binary's own
// real build info (go test -c never produces a module- or stamped-tier
// build), so what it reports depends on the environment the suite runs in,
// not on anything this test controls — the module-tier branch itself is
// covered above, directly, by TestApplyUpdateRunsBothHalvesWhenBehind.
func runUpdateCfg(t *testing.T, published, installedPluginVersion string) (args []string, argsOf func() []string) {
	t.Helper()
	bin := fakeCLI(t)
	t.Setenv(fakeClaudeEnv, "stream")
	t.Setenv(fakePluginEnv, installedPluginVersion)
	t.Setenv(fakeGoEnv, "1")
	argsOf = watchClaudeArgs(t)

	ghPath := filepath.Join(t.TempDir(), "gh-state.json")
	if err := writeGhState(ghPath, &ghState{Repo: "example/repo", PublishedRef: "polako--v" + published}); err != nil {
		t.Fatalf("writing fake gh state: %v", err)
	}
	t.Setenv(fakeGhEnv, ghPath)

	return []string{"-claude", bin, "-gh", bin}, argsOf
}

func TestRunUpdateEndToEndRunsThePlanWhenBehind(t *testing.T) {
	args, argsOf := runUpdateCfg(t, "0.24.0", "0.23.0")
	buf := captureLog(t)

	var out strings.Builder
	if err := runUpdate(context.Background(), args, &out); err != nil {
		t.Fatalf("runUpdate: %v", err)
	}
	if !strings.Contains(out.String(), "plugin: 0.23.0 -> 0.24.0") {
		t.Errorf("plan does not show the plugin move: %s", out.String())
	}
	if !strings.Contains(buf.String(), "plugin 0.24.0") {
		t.Errorf("closing line does not name the plugin's new version: %s", buf.String())
	}
	var sawMarketplaceUpdate, sawPluginUpdate bool
	for _, a := range argsOf() {
		if strings.HasPrefix(a, "plugin marketplace update scharissis") {
			sawMarketplaceUpdate = true
		}
		if strings.HasPrefix(a, "plugin update polako@scharissis --scope user") {
			sawPluginUpdate = true
		}
	}
	if !sawMarketplaceUpdate || !sawPluginUpdate {
		t.Errorf("did not run both plugin commands: %v", argsOf())
	}
}

func TestRunUpdateEndToEndCheckRunsNothing(t *testing.T) {
	args, argsOf := runUpdateCfg(t, "0.24.0", "0.23.0")
	args = append(args, "-check")
	buf := captureLog(t)

	if err := runUpdate(context.Background(), args, &strings.Builder{}); err != nil {
		t.Fatalf("runUpdate: %v", err)
	}
	if !strings.Contains(buf.String(), "-check") {
		t.Errorf("log does not say this was a check: %s", buf.String())
	}
	for _, a := range argsOf() {
		if strings.HasPrefix(a, "plugin marketplace update") || strings.HasPrefix(a, "plugin update") {
			t.Errorf("-check ran a write: %q", a)
		}
	}
}

func TestRunUpdateEndToEndBothCurrentRunsNothing(t *testing.T) {
	args, argsOf := runUpdateCfg(t, "0.23.0", "0.23.0")

	var out strings.Builder
	if err := runUpdate(context.Background(), args, &out); err != nil {
		t.Fatalf("runUpdate: %v", err)
	}
	if !strings.Contains(out.String(), "plugin: 0.23.0, already current") {
		t.Errorf("plan does not say the plugin is current: %s", out.String())
	}
	for _, a := range argsOf() {
		if strings.HasPrefix(a, "plugin marketplace update") || strings.HasPrefix(a, "plugin update") {
			t.Errorf("already current: ran a write: %q", a)
		}
	}
}

func TestApplyUpdateRunsNothingWhenAlreadyCurrent(t *testing.T) {
	cfg := config{dir: t.TempDir(), claudeBin: fakeCLI(t), goBin: fakeCLI(t)}
	t.Setenv(fakeClaudeEnv, "stream")
	t.Setenv(fakeGoEnv, "1")
	argsOf := watchClaudeArgs(t)

	plugin := pluginPlan{state: pluginFound, version: "0.24.0", id: "polako@scharissis", scope: "user", marketplace: "scharissis"}
	binary := binaryPlan{tier: tierModule, current: "0.24.0"}

	var out strings.Builder
	if err := applyUpdate(context.Background(), cfg, false, "0.24.0", plugin, binary, &out); err != nil {
		t.Fatalf("applyUpdate: %v", err)
	}
	if got := argsOf(); len(got) != 0 {
		t.Errorf("already current: expected no calls, got %v", got)
	}
}
