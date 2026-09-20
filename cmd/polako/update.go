package main

// `polako update` brings both halves — the plugin and the binary — to the
// release this project has actually published: the marketplace `ref` on the
// default branch, which is what merging the publish PR (docs/releasing.md)
// exposes to anyone installing or updating. Never `@latest`, which can land
// ahead of what the plugin side resolves to in the window between the two
// tags landing.
//
// It never runs inside a drain — `work` never updates itself, so a shift's
// binary and skill don't change under it by polako's hand. It is a verb a
// person or a script runs between shifts, same as `tidy`.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// updateRepo and updateMarketplacePath name this project's own repository
// and the file inside it that carries the published version — hardcoded,
// unlike every gh call elsewhere in this binary, because there is only ever
// one polako to check against, whatever -dir a drain elsewhere is pointed
// at. update takes no -dir at all: it touches the operator's Claude Code
// install and Go bin, not a repository checkout.
const (
	updateRepo            = "scharissis/polako"
	updateMarketplacePath = "repos/" + updateRepo + "/contents/.claude-plugin/marketplace.json"
)

// publishedVersionTimeout bounds the marketplace-file read, the same shape
// probeUsage's own timeout has (usage.go) — one bounded gh api call for a
// public file.
const publishedVersionTimeout = 10 * time.Second

type updateOptions struct {
	check     bool
	claudeBin string
	ghBin     string
	skill     string
}

// runUpdate is the `update` subcommand: parse its own flags, read what's
// published, resolve what each half would need to change, then either print
// that plan (-check) or run it. seed is zero in production; the suite uses it
// to hand the verb a ui to narrate into and fake-CLI handshake vars for its
// children (see config.env), since this entry point builds its own config.
func runUpdate(ctx context.Context, seed config, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	fs.SetOutput(out)
	var opt updateOptions
	fs.BoolVar(&opt.check, "check", false, "print the plan and change nothing")
	fs.StringVar(&opt.claudeBin, "claude", "claude", "claude binary to invoke")
	fs.StringVar(&opt.ghBin, "gh", "gh", "gh binary to invoke")
	fs.StringVar(&opt.skill, "skill", defaultSkill,
		"skill `polako work` would run, so update knows whether that's the plugin or a hand-copied skill")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "Usage: polako update [flags]\n\n"+
			"Brings the installed plugin and the binary to the release this project has\n"+
			"actually published — never `go install ...@latest`, which can land ahead of\n"+
			"what the plugin side resolves to. -check prints the plan and changes nothing.\n\n"+envUsage+"\nFlags:\n")
		fs.PrintDefaults()
	}
	if err := applyEnvDefaults(fs); err != nil {
		return err
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return errFlagsReported
	}
	if rest := fs.Args(); len(rest) > 0 {
		return fmt.Errorf("unexpected argument %q — update takes flags only", rest[0])
	}

	cfg, err := updateConfig(opt)
	if err != nil {
		return err
	}
	cfg.env, cfg.ui = seed.env, seed.ui

	published, err := publishedVersion(ctx, cfg)
	if err != nil {
		return fmt.Errorf("could not read the published version: %w", err)
	}
	plugin := resolvePluginPlan(ctx, cfg)
	binary := resolveBinaryPlan()

	return applyUpdate(ctx, cfg, opt.check, published, plugin, binary, out)
}

// updateConfig builds the lightweight config the shared gh/claude readers
// take, the same way tidyConfig and statusConfig do. No -dir: nothing here
// reads or writes a repository checkout.
func updateConfig(opt updateOptions) (config, error) {
	cfg := config{
		claudeBin:   opt.claudeBin,
		ghBin:       opt.ghBin,
		goBin:       "go",
		skill:       opt.skill,
		ghRetryWait: ghRetryDelay,
	}
	for _, bin := range []string{cfg.claudeBin, cfg.ghBin} {
		if _, err := exec.LookPath(bin); err != nil {
			return cfg, fmt.Errorf("%q not found on PATH: %w", bin, err)
		}
	}
	abs, err := filepath.Abs(".")
	if err != nil {
		return cfg, fmt.Errorf("resolving the working directory: %w", err)
	}
	cfg.dir = abs
	return cfg, nil
}

// publishedVersion reads the release this project has actually shipped: the
// `polako` entry's `ref` in the marketplace file on the default branch,
// which is what an install or an auto-update resolves through — not what
// the latest `vX.Y.Z` tag alone would say, since that tag lands before the
// publish PR merges (docs/releasing.md).
//
// Unlike probeUsage, a failure here is a real, reported error rather than a
// silent no-op: `update` is an explicit command, and running it to find out
// nothing happened would defeat the point. (The passive "update available"
// notice ticket 2 adds to `work`/`status` is the one that gets to stay
// silent on failure — a different call site, out of scope here.)
func publishedVersion(ctx context.Context, cfg config) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, publishedVersionTimeout)
	defer cancel()
	out, err := gh(ctx, cfg, "api", updateMarketplacePath, "-H", "Accept: application/vnd.github.raw")
	if err != nil {
		return "", fmt.Errorf("reading %s from %s (is gh authenticated?): %w", updateMarketplacePath, updateRepo, err)
	}
	return parseMarketplaceVersion(out)
}

// parseMarketplaceVersion is publishedVersion's parsing half, split out so
// it's testable against a literal marketplace.json without a fake gh.
func parseMarketplaceVersion(raw []byte) (string, error) {
	var mkt struct {
		Plugins []struct {
			Name   string `json:"name"`
			Source struct {
				Ref string `json:"ref"`
			} `json:"source"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(raw, &mkt); err != nil {
		return "", fmt.Errorf("parsing the marketplace file: %w", err)
	}
	for _, p := range mkt.Plugins {
		if p.Name != pluginName {
			continue
		}
		refVersion, ok := strings.CutPrefix(p.Source.Ref, pluginName+"--v")
		if !ok {
			return "", fmt.Errorf("the %s entry's ref %q is not %s--v<version>", pluginName, p.Source.Ref, pluginName)
		}
		v, _, ok := releaseVersion(refVersion)
		if !ok {
			return "", fmt.Errorf("the %s entry's ref %q does not name a release", pluginName, p.Source.Ref)
		}
		return v, nil
	}
	return "", fmt.Errorf("no %q entry in the marketplace file", pluginName)
}

// --- the plugin half ---

// pluginPlanState is what update learned about the installed plugin, ahead
// of deciding what (if anything) to run for it.
type pluginPlanState int

const (
	// pluginNotConfigured is a -skill with no plugin prefix: a hand-installed
	// skill, which has no plugin to update at all.
	pluginNotConfigured pluginPlanState = iota
	// pluginNotInstalled covers both "no matching copy" and "the CLI could
	// not answer at all" — pluginVersion already collapses those, and there
	// is no more honest answer to give than "nothing to update here".
	pluginNotInstalled
	// pluginAmbiguous is a version but no id: more than one installed copy
	// agreeing on a version, with no single `plugin update` target.
	pluginAmbiguous
	// pluginSessionLoad is a --plugin-dir copy: a session-scope dev load
	// that replaces the installed one for this Claude Code session alone
	// (installedVersion's own doc comment — "the way anyone testing a tip
	// skill against a tip binary runs"). "session" isn't one of the three
	// scopes `claude plugin install`/`update` accept (docs/install.md's own
	// table: user/project/local), so there is no real marketplace behind it
	// to run `plugin marketplace update` against — a dev load, not
	// something this verb manages.
	pluginSessionLoad
	pluginFound
)

type pluginPlan struct {
	state              pluginPlanState
	skill              string // cfg.skill verbatim, for pluginNotConfigured's message
	version, id, scope string
	marketplace        string
}

// resolvePluginPlan is the exec-calling half, mirroring pluginVersion:
// asking the CLI what it has installed and turning the answer into one of
// the four states above.
func resolvePluginPlan(ctx context.Context, cfg config) pluginPlan {
	plugin, _, ok := strings.Cut(cfg.skill, ":")
	if !ok || plugin == "" {
		return pluginPlan{state: pluginNotConfigured, skill: cfg.skill}
	}
	out, err := capture(ctx, cfg.dir, cfg.env, cfg.claudeBin, "plugin", "list", "--json")
	if err != nil {
		return pluginPlan{state: pluginNotInstalled}
	}
	return resolvePluginPlanFrom(out, plugin)
}

// resolvePluginPlanFrom is the pure half, taking installedVersion's own
// output shape directly so it's unit testable with literal `plugin list
// --json` output the same way TestPluginVersionPicksTheCopyThatWillRun is.
func resolvePluginPlanFrom(list []byte, plugin string) pluginPlan {
	version, id, scope := installedVersion(list, plugin)
	switch {
	case version == "":
		return pluginPlan{state: pluginNotInstalled}
	case id == "":
		return pluginPlan{state: pluginAmbiguous, version: version}
	case scope == "session":
		return pluginPlan{state: pluginSessionLoad, version: version, id: id, scope: scope}
	default:
		_, marketplace, _ := strings.Cut(id, "@")
		return pluginPlan{state: pluginFound, version: version, id: id, scope: scope, marketplace: marketplace}
	}
}

// applyPlugin runs the two `claude plugin` commands a ready plan calls for.
// Never called for any other state — the caller already decided what to say
// about those. `--json` is passed on `plugin update`, but its output is
// never parsed: this compares versions first and trusts the exit status
// instead, unresolved on what a no-op update prints.
func applyPlugin(ctx context.Context, cfg config, p pluginPlan) error {
	if _, err := capture(ctx, cfg.dir, cfg.env, cfg.claudeBin, "plugin", "marketplace", "update", p.marketplace); err != nil {
		return fmt.Errorf("updating the %s marketplace: %w", p.marketplace, err)
	}
	if _, err := capture(ctx, cfg.dir, cfg.env, cfg.claudeBin, "plugin", "update", p.id, "--scope", p.scope, "--json"); err != nil {
		return fmt.Errorf("updating the %s plugin: %w", p.id, err)
	}
	return nil
}

// --- the binary half ---

type binaryPlan struct {
	tier    buildTier
	current string
	// exe is the running binary's own resolved path, read once here rather
	// than by applyStampedBinary itself — the same "every read happens before
	// applyUpdate is called" shape every other field in this file already
	// has, and the seam that lets a test hand applyStampedBinary a temp file
	// instead of the real os.Executable(). Empty when it can't be read; a
	// stamped-tier update then refuses rather than guessing a path.
	exe string
}

func resolveBinaryPlan() binaryPlan {
	tier, v := polakoBuildTier()
	exe, _ := os.Executable()
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return binaryPlan{tier: tier, current: normalizeBuildVersion(tier, v), exe: exe}
}

// normalizeBuildVersion strips a release tag's "v" prefix for the two tiers
// whose version is release-shaped — a go-installed module version and a
// release's -ldflags stamp are both "vX.Y.Z" (a Go module version always
// carries the v, and release.yml stamps the `vX.Y.Z` tag verbatim), while
// publishedVersion's own answer is already stripped (releaseVersion, inside
// parseMarketplaceVersion). Comparing the two unnormalized never matches
// even when they name the same release. releaseVersion is the same
// normalization skewComparison already applies to both sides of its own
// comparison; a version that isn't release-shaped (a pseudo-version, an
// unparseable stamp) is left exactly as it was, which then correctly
// compares as "behind" rather than silently matching nothing. A VCS
// revision or an unknown build is never release-shaped, so it passes
// through untouched.
func normalizeBuildVersion(tier buildTier, v string) string {
	if tier != tierModule && tier != tierStamped {
		return v
	}
	if norm, _, ok := releaseVersion(v); ok {
		return norm
	}
	return v
}

// updateModulePath is the binary's own module, the same one docs/install.md
// and skewRemedy's predecessor pointed `go install` at.
const updateModulePath = "github.com/scharissis/polako/cmd/polako"

// applyBinary runs `go install` for a module-tier build. Never called for
// any other tier: a stamped release binary goes through applyStampedBinary
// instead, and a VCS build is the operator's own rebuild — neither has
// anything for `go install` to do.
func applyBinary(ctx context.Context, cfg config, published string) error {
	target := updateModulePath + "@v" + published
	if _, err := capture(ctx, cfg.dir, cfg.env, cfg.goBin, "install", target); err != nil {
		return fmt.Errorf("go install %s: %w", target, err)
	}
	return nil
}

// goInstallDir is where `go install` with no explicit output path would put
// this binary: GOBIN if set, else GOPATH's bin directory. Best-effort — a
// `go env` that fails leaves the warning goInstallWarning would add unsaid
// rather than blocking the install itself.
func goInstallDir(ctx context.Context, cfg config) (string, error) {
	if out, err := capture(ctx, cfg.dir, cfg.env, cfg.goBin, "env", "GOBIN"); err == nil {
		if dir := strings.TrimSpace(string(out)); dir != "" {
			return dir, nil
		}
	}
	out, err := capture(ctx, cfg.dir, cfg.env, cfg.goBin, "env", "GOPATH")
	if err != nil {
		return "", err
	}
	gopath := strings.TrimSpace(string(out))
	if gopath == "" {
		return "", errors.New("go env GOPATH is empty")
	}
	return filepath.Join(gopath, "bin"), nil
}

// goInstallWarning says so when `go install` would land beside the running
// copy rather than over it — exe isn't under the directory goInstallDir
// names. "" when there's nothing to warn about, including an empty exe or
// every case goInstallDir couldn't answer: a warning built on an unknown is
// worse than none. exe is resolveBinaryPlan's own resolved path, threaded
// through rather than read again here — the second of two places in this
// file that would otherwise each call os.Executable() + EvalSymlinks within
// one `update` run.
func goInstallWarning(ctx context.Context, cfg config, exe string) string {
	if exe == "" {
		return ""
	}
	installDir, err := goInstallDir(ctx, cfg)
	if err != nil {
		return ""
	}
	want, err := filepath.EvalSymlinks(installDir)
	if err != nil || filepath.Dir(exe) == want {
		return ""
	}
	return fmt.Sprintf("this binary is running from %s, but `go install` would put the new one in %s — "+
		"the running copy won't be replaced until you put that directory on PATH or run it from there",
		filepath.Dir(exe), installDir)
}

// applyStampedBinary downloads the release asset for this GOOS/GOARCH,
// verifies it against checksums.txt, and swaps it in over the running
// binary — the download ticket 1 could only print a link for. Everything
// happens in a temp dir made beside exe, so the one rename that actually
// touches the running binary never crosses a filesystem boundary; a
// checksum mismatch, a missing checksums.txt, or an unwritable directory
// all refuse before that rename, leaving exe untouched.
func applyStampedBinary(ctx context.Context, cfg config, published, exe string) error {
	asset := releaseAssetName(published)
	if exe == "" {
		return fmt.Errorf("could not find this binary's own path — download %s from %s yourself",
			asset, releaseURL(published))
	}
	dir := filepath.Dir(exe)
	tmp, err := os.MkdirTemp(dir, ".polako-update-*")
	if err != nil {
		return fmt.Errorf("%s is not writable (%w) — download %s from %s yourself",
			dir, err, asset, releaseURL(published))
	}
	defer func() {
		if err := os.RemoveAll(tmp); err != nil {
			cfg.logf("could not remove the temp dir %s: %v — safe to delete by hand", tmp, err)
		}
	}()

	if _, err := gh(ctx, cfg, "release", "download", "v"+published, "--repo", updateRepo,
		"--pattern", asset, "--pattern", "checksums.txt", "--dir", tmp); err != nil {
		return fmt.Errorf("downloading %s: %w", asset, err)
	}

	sums, err := os.ReadFile(filepath.Join(tmp, "checksums.txt"))
	if err != nil {
		return fmt.Errorf("release v%s has no checksums.txt to verify %s against — leaving the running binary in place", published, asset)
	}
	want, err := checksumFor(sums, asset)
	if err != nil {
		return err
	}
	assetPath := filepath.Join(tmp, asset)
	got, err := sha256File(assetPath)
	if err != nil {
		return fmt.Errorf("checksumming %s: %w", asset, err)
	}
	if got != want {
		return fmt.Errorf("checksum mismatch for %s: downloaded %s, checksums.txt says %s — leaving the running binary in place",
			asset, got, want)
	}

	info, err := os.Stat(exe)
	if err != nil {
		return fmt.Errorf("reading %s's mode: %w", exe, err)
	}
	if err := os.Chmod(assetPath, info.Mode()); err != nil {
		return fmt.Errorf("setting %s's mode: %w", assetPath, err)
	}
	return swapBinary(assetPath, exe)
}

// checksumFor finds asset's line in a checksums.txt-shaped file — sha256sum's
// own format, "<hex>  name" (or "<hex> *name" in binary mode), which is what
// release.yml's own `sha256sum * > checksums.txt` step writes.
func checksumFor(raw []byte, asset string) (string, error) {
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		if strings.TrimPrefix(fields[1], "*") == asset {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("checksums.txt has no entry for %s — leaving the running binary in place", asset)
}

// sha256File is the download's half of the checksum comparison.
func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// swapBinary replaces exe with newBinary in the one rename that touches the
// running binary — assumed already on the same filesystem, which
// applyStampedBinary's own temp dir (made beside exe) guarantees. Every OS
// but Windows can rename straight over a running executable; Windows' loader
// holds it open in a way that blocks that, so the running exe is renamed
// aside to ".old" first, freeing its name for the new one. removeStaleOldBinary
// cleans that file up, not this — the loader may still hold it open right
// after the swap.
func swapBinary(newBinary, exe string) error {
	if runtime.GOOS != "windows" {
		if err := os.Rename(newBinary, exe); err != nil {
			return fmt.Errorf("replacing %s: %w", exe, err)
		}
		return nil
	}

	old := exe + ".old"
	if err := os.Rename(exe, old); err != nil {
		return fmt.Errorf("moving the running binary aside to %s: %w", old, err)
	}
	if err := os.Rename(newBinary, exe); err != nil {
		// The rename-aside above already succeeded, so exe is gone — put the
		// running binary straight back rather than leave the operator with
		// neither a working exe nor an obvious way back to one.
		if restoreErr := os.Rename(old, exe); restoreErr != nil {
			return fmt.Errorf("replacing %s: %w (restoring the original from %s also failed: %v — "+
				"it's still there, move it back to %s by hand)", exe, err, old, restoreErr, exe)
		}
		return fmt.Errorf("replacing %s: %w (the original binary was put back)", exe, err)
	}
	return nil
}

// removeStaleOldBinary clears a ".old" a previous swapBinary left next to exe
// on Windows. Best-effort and silent: exe not existing yet, or nothing to
// remove, are both the common case, not an error.
func removeStaleOldBinary(exe string) {
	if runtime.GOOS != "windows" || exe == "" {
		return
	}
	os.Remove(exe + ".old")
}

// releaseAssetName is the asset a stamped binary's release attaches for
// this GOOS/GOARCH — release.yml's own naming, `polako_v<version>_<goos>_
// <goarch>[.exe]` — both what applyStampedBinary downloads and what a human
// downloads by hand when it can't.
func releaseAssetName(published string) string {
	name := fmt.Sprintf("polako_v%s_%s_%s", published, runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

// releaseURL is where that asset (and the release notes) live.
func releaseURL(published string) string {
	return fmt.Sprintf("https://github.com/%s/releases/tag/v%s", updateRepo, published)
}

// updateMarketplaceName is this project's own marketplace name — the
// `name` field in marketplace.json, not the GitHub owner, though here they
// happen to match (docs/install.md says the same).
const updateMarketplaceName = "scharissis"

// --- putting it together ---

// pluginSummary renders one plugin state as the line update prints — under
// -check and a real run alike, since the plan has to read the same either
// way — and whether a real run has anything to do for it.
func pluginSummary(p pluginPlan, published string) (line string, action bool) {
	switch p.state {
	case pluginNotConfigured:
		return fmt.Sprintf("plugin: -skill %q has no plugin prefix — a hand-installed skill copy, "+
			"which is yours to keep up to date", p.skill), false
	case pluginNotInstalled:
		return fmt.Sprintf("plugin: not installed — `claude plugin marketplace add %s` then "+
			"`claude plugin install %s@%s`", updateRepo, pluginName, updateMarketplaceName), false
	case pluginAmbiguous:
		return fmt.Sprintf("plugin: installed more than once (version %s), from more than one "+
			"marketplace — no single copy to update; resolve the duplicate install by hand", p.version), false
	case pluginSessionLoad:
		return fmt.Sprintf("plugin: %s (%s) is a --plugin-dir session load — not something update "+
			"manages; stop overriding it to get the marketplace copy back", p.version, p.id), false
	default: // pluginFound
		if p.version == published {
			return fmt.Sprintf("plugin: %s, already current", p.version), false
		}
		return fmt.Sprintf("plugin: %s -> %s — `claude plugin marketplace update %s` then "+
			"`claude plugin update %s --scope %s`", p.version, published, p.marketplace, p.id, p.scope), true
	}
}

// binarySummary is pluginSummary's twin for the binary half. warn is
// goInstallWarning's line, checked (a read) whether or not a real run
// follows through on it, since -check's whole point is to preview it.
func binarySummary(ctx context.Context, cfg config, b binaryPlan, published string) (line string, action bool, warn string) {
	switch b.tier {
	case tierModule:
		if b.current == published {
			return fmt.Sprintf("binary: go-installed, %s, already current", b.current), false, ""
		}
		return fmt.Sprintf("binary: go-installed, %s -> %s — `go install %s@v%s`",
			b.current, published, updateModulePath, published), true, goInstallWarning(ctx, cfg, b.exe)
	case tierStamped:
		if b.current == published {
			return fmt.Sprintf("binary: release build, %s, already current", b.current), false, ""
		}
		return fmt.Sprintf("binary: release build, %s -> %s — download %s, verify its checksum, and replace this binary",
			b.current, published, releaseAssetName(published)), true, ""
	case tierVCS:
		return fmt.Sprintf("binary: built from source (%s) — rebuild it yourself", b.current), false, ""
	default: // tierUnknown
		return "binary: no version information at all (a `go run`, or a stripped build) — " +
			"reinstall or rebuild it yourself", false, ""
	}
}

// --- the passive notice ---
//
// work's preflight and status make the same published-version read
// `update -check` does, and say one line when it is ahead of either half —
// the only way a release reaches an operator who never runs `update` by
// hand.

// publishedVersionQuiet is publishedVersion best-effort: "" on a failed or
// timed-out read rather than an error, the same tolerance probeUsage has —
// what a passive notice needs, never a refusal. Ungated: status calls this
// directly, since it carries no -skill of its own and always means this
// repo's own plugin (readStatus, status.go).
func publishedVersionQuiet(ctx context.Context, cfg config) (string, bool) {
	published, err := publishedVersion(ctx, cfg)
	if err != nil {
		return "", false
	}
	return published, true
}

// readPublishedVersion is the notice's read as work's preflight makes it:
// gated by namesThisPlugin, since -skill may point anywhere and another
// plugin's installed version has nothing here to compare against — the same
// gate skewComparison applies, for the same reason.
func readPublishedVersion(ctx context.Context, cfg config) (string, bool) {
	if !namesThisPlugin(cfg.skill) {
		return "", false
	}
	return publishedVersionQuiet(ctx, cfg)
}

// versionBehind reports whether current names a release strictly behind
// target, the releaseVersion + semverLess pair skewComparison already
// applies once, used here for both halves of the notice's own comparison.
// isRelease false (current carries no release version at all) always comes
// back with behind false: an unreleased or absent version is not "behind",
// it is nothing to compare.
func versionBehind(current string, target [3]int) (norm string, isRelease, behind bool) {
	norm, parts, isRelease := releaseVersion(current)
	if !isRelease {
		return "", false, false
	}
	return norm, true, semverLess(parts, target)
}

// updateAvailableLine is the notice's comparison, pure so work's preflight
// and status can both call it over whatever they already read rather than
// repeating it. "" unless published is ahead of the binary or the
// installed plugin. Silent (not merely unmatched) when the binary carries
// no release version — a clone build or a stripped one, releaseVersion's
// own rule, the same one skewComparison applies for the same reason: an
// unreleased binary is not skew, and warning about it every time would
// train an operator to ignore the notice that matters. plugin missing or
// not release-shaped just drops out of the comparison rather than silencing
// the whole line — the binary alone may still be worth a notice.
func updateAvailableLine(binary, plugin, published string) string {
	publishedParts, err := parseSemver(published)
	if err != nil {
		return ""
	}
	self, selfIsRelease, behindSelf := versionBehind(binary, publishedParts)
	if !selfIsRelease {
		return ""
	}
	pluginNorm, pluginIsRelease, behindPlugin := versionBehind(plugin, publishedParts)
	if !behindSelf && !behindPlugin {
		return ""
	}
	pluginDisplay := plugin
	if pluginIsRelease {
		pluginDisplay = pluginNorm
	}
	if pluginDisplay == "" {
		pluginDisplay = "not installed"
	}
	return fmt.Sprintf("update available: polako %s is out (binary %s, plugin %s) — run `polako update`",
		published, self, pluginDisplay)
}

// updateNoticeLine is the notice as work's preflight calls it: the read and
// the comparison together, both best-effort — nothing here refuses or
// blocks a shift over a release notice, the same tolerance probeUsage has.
func updateNoticeLine(ctx context.Context, binary string, cfg config) string {
	published, ok := readPublishedVersion(ctx, cfg)
	if !ok {
		return ""
	}
	return updateAvailableLine(binary, cfg.pluginVersion, published)
}

// applyUpdate prints the plan — always, -check or not, since the two have to
// say the same thing — then, on a real run with something to do, runs it and
// prints the closing line. Nothing here mutates anything before the -check
// return below except removeStaleOldBinary, which is not part of the plan —
// it clears a Windows ".old" a previous stamped-tier swap left behind,
// unconditionally on every real run for a stamped-tier binary, whether or
// not this one has anything else to do — gated to that tier because a
// ".old" is only ever swapBinary's own leftover, and only that tier ever
// calls it. Everything else is a read (the published-version fetch, `plugin
// list`, `go env`) that has already happened in runUpdate and
// resolveBinaryPlan by the time this is called, so -check's "reads only"
// promise for the plan itself holds structurally, not by a flag this
// function has to remember to check before every call.
func applyUpdate(ctx context.Context, cfg config, check bool, published string, plugin pluginPlan, binary binaryPlan, out io.Writer) error {
	if !check && binary.tier == tierStamped {
		removeStaleOldBinary(binary.exe)
	}

	pLine, pAction := pluginSummary(plugin, published)
	bLine, bAction, warn := binarySummary(ctx, cfg, binary, published)

	fmt.Fprintf(out, "published: %s\n%s\n%s\n", published, pLine, bLine)
	if warn != "" {
		fmt.Fprintf(out, "warning: %s\n", warn)
	}

	if !pAction && !bAction {
		cfg.logf("nothing to run — both halves are already current, or neither can be updated by this verb")
		return nil
	}
	if check {
		cfg.logf("-check: the plan above, nothing run")
		return nil
	}

	if pAction {
		if err := applyPlugin(ctx, cfg, plugin); err != nil {
			return err
		}
	}
	if bAction {
		var err error
		switch binary.tier {
		case tierStamped:
			err = applyStampedBinary(ctx, cfg, published, binary.exe)
		default: // tierModule — the only other tier binarySummary ever sets action for
			err = applyBinary(ctx, cfg, published)
		}
		if err != nil {
			return err
		}
	}
	var done []string
	if pAction {
		done = append(done, "plugin "+published)
	}
	if bAction {
		done = append(done, "polako "+published)
	}
	cfg.logf("done — %s — restart Claude Code, or `/reload-plugins`", strings.Join(done, ", "))
	return nil
}
