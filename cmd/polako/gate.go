package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"slices"
	"strconv"
	"strings"
)

// queueGate refuses to work a public repository's backlog unfiltered. On a
// public repo anyone can open an issue, an open issue is exactly what a drain
// picks up, and issue text is attacker-controllable input to an unattended
// agent. Applying a label takes triage permission or better, so a -label gate
// turns "anyone can queue work" into "a maintainer chose this one" — the
// docs/security.md has always advised it, and on the one repository
// shape where the risk is structural, advice is not enough. -ungated is the
// operator overruling this on purpose, out loud.
//
// Anything but PUBLIC passes: on a private or internal repo, everyone who can
// open an issue was let in by name, and an unknown visibility from a future gh
// should not strand an operator whose repo the gate was never about.
func queueGate(visibility, label string, ungated bool) error {
	if !strings.EqualFold(visibility, "PUBLIC") || label != "" || ungated {
		return nil
	}
	return errors.New("this repository is public, so anyone who can open an issue can queue work for an unattended agent — " +
		"pass -label <name> to work only issues a maintainer labelled (see docs/security.md), " +
		"or -ungated to work every open issue anyway")
}

// refuseOrNote is the dry-run carve-out every preflight gate shares: nil
// passes straight through, and anything else refuses a real run but only
// narrates what a real run would have refused on -dry-run, which runs
// nothing and writes nothing. queueGate and versionSkewGate both go through
// this so a real refusal and its dry-run preview can never drift into saying
// it two different ways.
func refuseOrNote(err error, dryRun bool) error {
	if err == nil {
		return nil
	}
	if !dryRun {
		return err
	}
	log.Printf("note: a real run would refuse to start here — %v", err)
	return nil
}

// effortFlagGate fails preflight when an effort flag — -effort,
// -remediation-effort or -effort-by-size — is set and `claude --help` runs but
// has no --effort: that usage error would otherwise surface an hour in, look
// like a crash, burn -retries resumes, and park the issue for nothing. The
// message names the CLI version so the operator knows which install to update.
//
// A no-op when none is set — the common path, and the one that keeps this
// from adding a `claude --help` call to every preflight. A probe that will not
// run at all (a transient exec error, a wrapper shim mid-setup) only warns and
// lets the run proceed, the same best-effort stance claudeVersion takes beside
// it: a broken CLI has its own louder failure coming, and "your CLI is too
// old" would be the wrong diagnosis for it.
func effortFlagGate(ctx context.Context, cfg config) error {
	// All three map to the same --effort, so all are named — an operator who
	// set two and gets told to "drop -effort" hits the identical wall on the
	// other one next.
	var setFlags []string
	if cfg.effort != "" {
		setFlags = append(setFlags, "-effort "+cfg.effort)
	}
	if cfg.remediationEffort != "" {
		setFlags = append(setFlags, "-remediation-effort "+cfg.remediationEffort)
	}
	// -effort-by-size resolves to a --effort on any implementation run whose
	// size hits a cell, so it gates like the other two.
	if cfg.effortBySize != "" {
		setFlags = append(setFlags, "-effort-by-size "+cfg.effortBySize)
	}
	if len(setFlags) == 0 {
		return nil
	}
	set, drop := strings.Join(setFlags, " and "), "drop it"
	if len(setFlags) > 1 {
		drop = "drop them"
	}
	out, err := capture(ctx, cfg.dir, cfg.claudeBin, "--help")
	if err != nil {
		log.Printf("could not check whether claude takes --effort (%v) — running anyway; "+
			"a run that then rejects %s needs a newer CLI", err, set)
		return nil
	}
	if strings.Contains(string(out), "--effort") {
		return nil
	}
	v := cfg.claudeVersion
	if v == "" {
		v = claudeVersion(ctx, cfg)
	}
	if v == "" {
		v = "unknown version"
	}
	return fmt.Errorf("%s is set, but claude (%s) does not list --effort in `claude --help` — "+
		"update the CLI, or %s", set, v, drop)
}

// warnClaudeModelEnv says out loud when the operator's environment carries a
// model or effort override. The CLI's own precedence puts ANTHROPIC_MODEL
// above --model, and the child inherits this process's environment by design
// (TestDispatchGivesTheChildTheOperatorsEnvironment pins cmd.Env nil so the
// egress proxy keeps working), so an exported variable silently beats -model
// and -effort both — worth a line before an operator wonders why their flag
// did nothing.
func warnClaudeModelEnv() {
	for _, name := range []string{"ANTHROPIC_MODEL", "CLAUDE_CODE_EFFORT_LEVEL"} {
		if v := os.Getenv(name); v != "" {
			log.Printf("%s=%s is exported — the CLI reads it, and it can override -model/-effort for every run", name, v)
		}
	}
}

// claudeVersion pins which CLI produced a run's numbers. Best-effort: a
// version it cannot read leaves the field empty rather than stopping a drain
// over telemetry.
func claudeVersion(ctx context.Context, cfg config) string {
	out, err := capture(ctx, cfg.dir, cfg.claudeBin, "--version")
	if err != nil {
		return ""
	}
	if fields := strings.Fields(string(out)); len(fields) > 0 {
		return fields[0]
	}
	return ""
}

// pluginVersion reports which release of the skill this run will drive, by
// asking the CLI what it has installed, along with that copy's
// `<plugin>@<marketplace>` id where there is an unambiguous one. Best-effort in
// the same way as claudeVersion, and empty rather than wrong in every case
// where there is no honest answer: a -skill with no plugin prefix names a
// hand-installed skill, which carries no version at all, a CLI too old for
// `plugin list --json` fails the call, and a list that holds the plugin more
// than once may not say which copy wins.
func pluginVersion(ctx context.Context, cfg config) (version, id string) {
	plugin, _, ok := strings.Cut(cfg.skill, ":")
	if !ok || plugin == "" {
		return "", ""
	}
	out, err := capture(ctx, cfg.dir, cfg.claudeBin, "plugin", "list", "--json")
	if err != nil {
		return "", ""
	}
	return installedVersion(out, plugin)
}

// installedPlugin is the part of a `plugin list --json` entry this reads.
// Enabled is a pointer because the list holds disabled plugins too, and a CLI
// that omits the field must not be read as "everything is off" — absent means
// enabled, which is what every CLI without the field meant.
type installedPlugin struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Scope   string `json:"scope"`
	Enabled *bool  `json:"enabled"`
}

// loadable reports whether a session would pick this copy up at all.
func (p installedPlugin) loadable() bool { return p.Enabled == nil || *p.Enabled }

// installedVersion picks the copy of plugin a session started now would load,
// out of `plugin list --json` output. The list can hold the same plugin twice,
// and the first entry is not the one that drives the run. It returns that
// copy's version and its `<plugin>@<marketplace>` id — the id only when one
// copy is unambiguously in the running, because the marketplace half is
// operator-chosen and the skew warning builds a `plugin update` command out of
// it (see warnOnVersionSkew).
func installedVersion(list []byte, plugin string) (version, id string) {
	var installed []installedPlugin
	if err := json.Unmarshal(list, &installed); err != nil {
		return "", ""
	}
	// The id is <plugin>@<marketplace>; the marketplace is whatever the
	// operator named it when they added it, so only the plugin half is ours to
	// match on. A disabled copy is listed but never loaded, so it is not a
	// candidate — counting it would both report a version no session ran and
	// let a stale disabled duplicate wash out an otherwise unambiguous answer.
	var matches []installedPlugin
	for _, p := range installed {
		if name, _, _ := strings.Cut(p.ID, "@"); name == plugin && p.loadable() {
			matches = append(matches, p)
		}
	}
	// A --plugin-dir copy is loaded for that session alone and replaces the
	// installed one outright — the way anyone testing a tip skill against a tip
	// binary runs. Nothing else has a precedence this can be sure of, so a tie
	// between any other pair of scopes stays a tie.
	if len(matches) > 1 {
		var session []installedPlugin
		for _, p := range matches {
			if p.Scope == "session" {
				session = append(session, p)
			}
		}
		if len(session) > 0 {
			matches = session
		}
	}
	if len(matches) == 0 {
		return "", ""
	}
	// Several copies still in the running. Report a version only if they agree
	// on one, because picking between them would be a guess, and a wrong
	// identifier in the run data is worse than an absent one: nothing reading it
	// later can tell that it is wrong.
	for _, p := range matches[1:] {
		if p.Version != matches[0].Version {
			return "", ""
		}
	}
	// The id goes back only when a single copy is left: two marketplaces that
	// happen to agree on a version still have no one right `plugin update`
	// target, so the warning drops the command rather than guess between them —
	// the same "wrong identifier is worse than none" rule the version follows.
	if len(matches) == 1 {
		return matches[0].Version, matches[0].ID
	}
	return matches[0].Version, ""
}

// warnOnVersionSkew reports a binary and a skill that did not ship together.
// The two halves share one version number by design — the supervisor finds a
// PR by the head branch the skill names, so a mismatched pair fails later and
// far less legibly than this. It stays a warning: an operator testing a new
// binary against an installed release, or running a skill newer than the
// binary, is doing something deliberate, and nothing here is safe to guess
// about. The one direction that is not a deliberate developer setup — the
// skill *behind* the binary, the #239 shape — is escalated separately by
// versionSkewGate, called ahead of this at the one call site preflight has;
// this still fires for every other skew shape exactly as before that gate
// existed.
func warnOnVersionSkew(binary string, cfg config) {
	self, plugin, _, ok := skewComparison(binary, cfg)
	if !ok {
		return
	}
	log.Printf("version skew: this binary is %s but the installed %s plugin is %s — "+
		"they are meant to ship together, and the supervisor finds a PR by the "+
		"branch name the skill chooses. To fix, %s", self, pluginName, plugin, skewRemedy(cfg))
}

// skewComparison is the one place that decides whether a binary and an
// installed skill are a comparable, differing pair of releases — shared by
// warnOnVersionSkew (any direction) and versionSkewGate (behind only), so the
// two can never disagree about what counts as skew. ok is false whenever
// there is nothing safe to compare: another plugin's skill, a build that
// carries no release version on either side, or two releases that agree.
func skewComparison(binary string, cfg config) (self, plugin string, behind, ok bool) {
	// Only this repo's own plugin shares a version line with this binary.
	// -skill is documented as pointing anywhere, and another plugin's versions
	// mean nothing here — comparing them would warn on every run of a
	// deliberate configuration, and name the wrong plugin while doing it.
	if name, _, _ := strings.Cut(cfg.skill, ":"); name != pluginName {
		return "", "", false, false
	}
	self, selfParts, selfIsRelease := releaseVersion(binary)
	plugin, pluginParts, pluginIsRelease := releaseVersion(cfg.pluginVersion)
	// A binary built from a clone reports a revision, not a release. That is
	// not skew, it is an unreleased build, and warning about it every time
	// would train an operator to ignore the one message that matters.
	if !selfIsRelease || !pluginIsRelease || self == plugin {
		return self, plugin, false, false
	}
	return self, plugin, semverLess(pluginParts, selfParts), true
}

// semverLess reports whether a names an earlier release than b — a named
// wrapper over slices.Compare, the same primitive TestShippingFixesDoNotSitUnreleased
// (repo_test.go) already uses to compare two [3]int release triples, rather
// than a second hand-rolled loop over the same shape.
func semverLess(a, b [3]int) bool {
	return slices.Compare(a[:], b[:]) < 0
}

// skewRemedy is the command (or commands) an operator runs to bring the
// binary and the plugin back to the same release, shared by
// warnOnVersionSkew and versionSkewGate so the two never drift apart in what
// they tell an operator to do about it.
func skewRemedy(cfg config) string {
	// `claude plugin update` wants the full `<plugin>@<marketplace>` id and
	// reports the bare name as not found even when it is installed
	// (docs/install.md) — so the remedy prints the id preflight carried from
	// the `plugin list` read, never one rebuilt from pluginName here, because
	// the marketplace half is operator-chosen and unguessable. When there was
	// no unambiguous id — copies from more than one marketplace — the skew is
	// still worth saying, so the message fires without the exact command and
	// sends the operator to the docs instead.
	if cfg.pluginID == "" {
		return "bring both to the current release — update the plugin (its update " +
			"command needs the full `plugin@marketplace` id, and more than one copy is " +
			"installed here) and run " +
			"`go install github.com/scharissis/polako/cmd/polako@latest`; see docs/install.md"
	}
	// `claude plugin marketplace update` wants the marketplace name, which is
	// the `@` half of the id — the same one docs/install.md names. Deriving it
	// keeps the two commands in step and matches the canonical wording there.
	_, marketplace, _ := strings.Cut(cfg.pluginID, "@")
	return fmt.Sprintf("bring both to the current release: "+
		"`claude plugin marketplace update %s && claude plugin update %s`, then "+
		"`go install github.com/scharissis/polako/cmd/polako@latest` (see docs/install.md)",
		marketplace, cfg.pluginID)
}

// versionSkewGate refuses to start a drain whose installed skill is a
// strictly older release than this binary — not merely different, which a
// newer or hand-installed skill can be on purpose (see warnOnVersionSkew).
// #239 is what that direction costs in practice: a shift ran a plugin three
// releases stale and paid for the pre-#225 review gate on every issue, with
// neither #216's resume point nor #217's polling floor, so the branch-name
// contract alone understates the risk. cfg.ignoreSkew is the operator
// overruling this, exactly the shape queueGate already has with
// cfg.ungated: the override is a parameter the gate itself resolves, not a
// second thing the call site has to reconstruct — preflight only adds its
// own "said out loud" line on top when the override actually fired.
func versionSkewGate(binary string, cfg config) error {
	self, plugin, behind, ok := skewComparison(binary, cfg)
	if !ok || !behind || cfg.ignoreSkew {
		return nil
	}
	return fmt.Errorf("the installed %s plugin (%s) is behind this binary (%s) — they are meant to ship "+
		"together, and a shift on a stale skill is not only a branch-naming risk: it is the shift #239 ran, "+
		"missing the polling floor (#217), the review-gate resume point (#216) and the diff-scaled review "+
		"level (#225), and spending well more per issue for it. Pass -ignore-skew to run anyway, or %s",
		pluginName, plugin, self, skewRemedy(cfg))
}

// releaseVersion normalizes a version that names a release, and reports false
// for anything that does not — an empty string, or the revision a build from a
// clone carries. The `v` prefix is optional because the binary picks one up
// from a module version and none from an -ldflags stamp. The parsed parts
// come back alongside the string so a caller comparing two releases
// (skewComparison) never has to parseSemver the same string twice.
func releaseVersion(s string) (string, [3]int, bool) {
	s = strings.TrimPrefix(s, "v")
	parts, err := parseSemver(s)
	if err != nil {
		return "", [3]int{}, false
	}
	return s, parts, true
}

// parseSemver reads the plain major.minor.patch this project releases under —
// no pre-release or build metadata, which is what the manifest test already
// holds plugin.json to. The parts come back in order so two versions can be
// compared without pulling in a module to do it.
func parseSemver(s string) ([3]int, error) {
	var out [3]int
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return out, fmt.Errorf("%q is not major.minor.patch", s)
	}
	for i, p := range parts {
		// Digits only: Atoi alone would accept the sign in "-1" and the "+1"
		// that a build-metadata suffix leaves behind, and a leading zero is
		// not a version this project ever tags.
		if p == "" || (len(p) > 1 && p[0] == '0') || strings.ContainsFunc(p, func(r rune) bool {
			return r < '0' || r > '9'
		}) {
			return out, fmt.Errorf("%q is not major.minor.patch", s)
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return out, fmt.Errorf("%q is not major.minor.patch", s)
		}
		out[i] = n
	}
	return out, nil
}

// describeVersion answers -version: which release this binary is, or an honest
// account of why it is not one.
func describeVersion() string {
	v := polakoVersion()
	if v == "" {
		return pluginName + " (unknown version: built without module or VCS information)"
	}
	return pluginName + " " + v
}
