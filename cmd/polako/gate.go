package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"
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
//
// markedLabel names the label setup's own marker already identifies as the
// gate label (markedGateLabel), when the caller happens to know it — preflight
// looks it up only in the one branch that is about to refuse, so a real run
// can say "pass -label ready" instead of the generic "-label <name>" placeholder.
// Empty is always safe: every other call site (setup's own advisory row,
// applySetup's boolean check, every existing test) passes "" and gets that
// placeholder back, unchanged from before this parameter existed.
func queueGate(visibility, label string, ungated bool, markedLabel string) error {
	if !strings.EqualFold(visibility, "PUBLIC") || label != "" || ungated {
		return nil
	}
	suggestion := "-label <name>"
	if markedLabel != "" {
		suggestion = "-label " + markedLabel
	}
	return fmt.Errorf("this repository is public, so anyone who can open an issue can queue work for an unattended agent — "+
		"pass %s to work only issues a maintainer labelled (see docs/security.md), "+
		"or -ungated to work every open issue anyway", suggestion)
}

// labelGate refuses a -label the repository has never defined. Without it,
// `-label typo` passes queueGate (it is, after all, a label) and then drains
// an empty queue all shift, reported as success — the same silent-nothing
// failure queueGate exists to catch, one flag over. exists is the answer
// #412's labelExists lookup already gave; a lookup failure that is not a
// definitive "no" is the call site's problem, not this gate's — it must
// never be misread as a missing label, which would send an operator to
// create one gh already has.
func labelGate(label string, exists bool) error {
	if exists {
		return nil
	}
	return fmt.Errorf("this repository has no %q label — -label only scopes the queue to issues that carry it, "+
		"and one the repository doesn't have scopes it to nothing. Create it (`gh label create %s`, or "+
		"`polako setup -apply -label %s`) or point -label at a label that exists", label, label, label)
}

// refuseOrNote is the dry-run carve-out every preflight gate shares: nil
// passes straight through, and anything else refuses a real run but only
// narrates what a real run would have refused on -dry-run, which runs
// nothing and writes nothing. queueGate and versionSkewGate both go through
// this so a real refusal and its dry-run preview can never drift into saying
// it two different ways.
func refuseOrNote(cfg config, err error, dryRun bool) error {
	if err == nil {
		return nil
	}
	if !dryRun {
		return err
	}
	cfg.logf("note: a real run would refuse to start here — %v", err)
	return nil
}

// effortFlagGate fails preflight when an effort flag — -effort,
// -remediation-effort or -effort-by-size — is set and `claude --help` runs but
// has no --effort: that usage error would otherwise surface an hour in, look
// like a crash, burn -retries resumes, and park the issue for nothing. The
// message names the CLI version so the operator knows which install to update.
// It also fails when CLAUDE_CODE_EFFORT_LEVEL is exported with a value one of
// those flags disagrees with, since the flag would silently do nothing.
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
	// Probed on 2.1.280: an exported CLAUDE_CODE_EFFORT_LEVEL beats --effort,
	// so a flag that disagrees with it does nothing. Refused rather than
	// unset for the child — polako never edits a child's environment
	// (docs/hardening.md). Ahead of the --help probe: it costs no process.
	if env := lookupEnv(cfg, effortEnv); env != "" && !effortFlagsMatch(cfg, env) {
		return fmt.Errorf("%s is set, but %s=%s is exported and the CLI lets it win over --effort — "+
			"unset %s, or %s", set, effortEnv, env, effortEnv, drop)
	}
	out, err := capture(ctx, cfg.dir, cfg.env, cfg.claudeBin, "--help")
	if err != nil {
		cfg.logf("could not check whether claude takes --effort (%v) — running anyway; "+
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

// claudeVersion pins which CLI produced a run's numbers. Best-effort: a
// version it cannot read leaves the field empty rather than stopping a drain
// over telemetry.
func claudeVersion(ctx context.Context, cfg config) string {
	out, err := capture(ctx, cfg.dir, cfg.env, cfg.claudeBin, "--version")
	if err != nil {
		return ""
	}
	if fields := strings.Fields(string(out)); len(fields) > 0 {
		return fields[0]
	}
	return ""
}

// providerProbeTimeout bounds `claude auth status`. Probed on 2.1.280 it makes
// no API call, so anything slower than this is a CLI stuck on something else.
const providerProbeTimeout = 5 * time.Second

// claudeProvider names the API provider a shift's runs bill through: firstParty
// reads as anthropic, anything else (bedrock, vertex, foundry — none probed)
// passes through. Best-effort like claudeVersion: a failure, or a CLI without
// `auth status`, leaves it empty. The reply also carries the account's email
// and org; decoding into a one-field struct is what keeps both out of every
// record, log line and terminal.
//
// Not capture: `auth status` exits 1 when no claude.ai login is present, still
// printing the JSON — the usual state on Bedrock, Vertex or an API key, which
// are the providers this field exists to tell apart. So the exit code is
// ignored and the reply alone decides.
func claudeProvider(ctx context.Context, cfg config) string {
	ctx, cancel := context.WithTimeout(ctx, providerProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, cfg.claudeBin, "auth", "status", "--json")
	cmd.Dir = cfg.dir
	cmd.Env = childEnv(cfg.env)
	out, _ := cmd.Output()
	var status struct {
		APIProvider string `json:"apiProvider"`
	}
	if json.Unmarshal(out, &status) != nil {
		return ""
	}
	if status.APIProvider == "firstParty" {
		return "anthropic"
	}
	return status.APIProvider
}

// pluginVersion reports which release of the skill this run will drive, by
// asking the CLI what it has installed, along with that copy's
// `<plugin>@<marketplace>` id and `--scope` where there is an unambiguous
// one. Best-effort in the same way as claudeVersion, and empty rather than
// wrong in every case where there is no honest answer: a -skill with no
// plugin prefix names a hand-installed skill, which carries no version at
// all, a CLI too old for `plugin list --json` fails the call, and a list
// that holds the plugin more than once may not say which copy wins.
func pluginVersion(ctx context.Context, cfg config) (version, id, scope string) {
	plugin, _, ok := strings.Cut(cfg.skill, ":")
	if !ok || plugin == "" {
		return "", "", ""
	}
	return installedPluginVersion(ctx, cfg, plugin)
}

// installedPluginVersion is pluginVersion's own read, taking the plugin name
// directly rather than deriving it from cfg.skill — for a caller asking
// about a specific plugin by name rather than by an operator's -skill.
// status is exactly this: it carries no -skill of its own, and wants this
// repo's own plugin (pluginName) rather than a value manufactured to look
// like one.
func installedPluginVersion(ctx context.Context, cfg config, plugin string) (version, id, scope string) {
	out, err := capture(ctx, cfg.dir, cfg.env, cfg.claudeBin, "plugin", "list", "--json")
	if err != nil {
		return "", "", ""
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
// copy's version and its `<plugin>@<marketplace>` id and scope — id and scope
// only when one copy is unambiguously in the running, because the
// marketplace half is operator-chosen and `update` needs both to build a
// `claude plugin update <id> --scope <scope>` command that names the right
// copy.
func installedVersion(list []byte, plugin string) (version, id, scope string) {
	var installed []installedPlugin
	if err := json.Unmarshal(list, &installed); err != nil {
		return "", "", ""
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
		return "", "", ""
	}
	// Several copies still in the running. Report a version only if they agree
	// on one, because picking between them would be a guess, and a wrong
	// identifier in the run data is worse than an absent one: nothing reading it
	// later can tell that it is wrong.
	for _, p := range matches[1:] {
		if p.Version != matches[0].Version {
			return "", "", ""
		}
	}
	// The id and scope go back only when a single copy is left: two
	// marketplaces that happen to agree on a version still have no one right
	// `plugin update` target, so a caller building that command drops it
	// rather than guess between them — the same "wrong identifier is worse
	// than none" rule the version follows.
	if len(matches) == 1 {
		return matches[0].Version, matches[0].ID, matches[0].Scope
	}
	return matches[0].Version, "", ""
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
	cfg.logf("version skew: this binary is %s but the installed %s plugin is %s — "+
		"they are meant to ship together, and the supervisor finds a PR by the "+
		"branch name the skill chooses. To fix, %s", self, pluginName, plugin, skewRemedy())
}

// namesThisPlugin reports whether -skill names this repo's own plugin —
// the gate skewComparison and the published-version notice (update.go) both
// need, and for the same reason: -skill is documented as pointing anywhere,
// and another plugin's version means nothing to compare against this
// binary's own release. Comparing it anyway would warn (or notice) on every
// run of a deliberate configuration, and name the wrong plugin while doing
// it.
func namesThisPlugin(skill string) bool {
	name, _, _ := strings.Cut(skill, ":")
	return name == pluginName
}

// skewComparison is the one place that decides whether a binary and an
// installed skill are a comparable, differing pair of releases — shared by
// warnOnVersionSkew (any direction) and versionSkewGate (behind only), so the
// two can never disagree about what counts as skew. ok is false whenever
// there is nothing safe to compare: another plugin's skill, a build that
// carries no release version on either side, or two releases that agree.
func skewComparison(binary string, cfg config) (self, plugin string, behind, ok bool) {
	if !namesThisPlugin(cfg.skill) {
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

// skewRemedy is the one line every skew message and refusal points to,
// shared by warnOnVersionSkew and versionSkewGate so the two never drift
// apart in what they tell an operator to do about it. `polako update`
// resolves both halves to the published release on its own — including the
// case this used to have to special-case, a plugin installed from more than
// one marketplace with no one unambiguous `plugin update` target — so there
// is nothing left here to branch on.
func skewRemedy() string {
	return "run `polako update`"
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
		pluginName, plugin, self, skewRemedy())
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
