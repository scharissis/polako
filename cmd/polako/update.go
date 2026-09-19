package main

// `polako update` brings both halves — the plugin and the binary — to the
// release this project has actually published: the marketplace `ref` on the
// default branch, which is what merging the publish PR (docs/releasing.md)
// exposes to anyone installing or updating. Never `@latest`, which can land
// ahead of what the plugin side resolves to in the window between the two
// tags landing (docs/plans/update.md, "What exists today").
//
// It never runs inside a drain — `work` never updates itself, so a shift's
// binary and skill don't change under it by polako's hand. It is a verb a
// person or a script runs between shifts, same as `tidy`.

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
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
// that plan (-check) or run it.
func runUpdate(ctx context.Context, args []string, out io.Writer) error {
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
	out, err := capture(ctx, cfg.dir, cfg.claudeBin, "plugin", "list", "--json")
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
// about those. `--json` is passed on `plugin update` per docs/plans/update.md
// ticket 1's own spec, but its output is never parsed: comparing versions
// first and trusting the exit status is that ticket's own open question 2,
// resolved by not answering it yet.
func applyPlugin(ctx context.Context, cfg config, p pluginPlan) error {
	if _, err := capture(ctx, cfg.dir, cfg.claudeBin, "plugin", "marketplace", "update", p.marketplace); err != nil {
		return fmt.Errorf("updating the %s marketplace: %w", p.marketplace, err)
	}
	if _, err := capture(ctx, cfg.dir, cfg.claudeBin, "plugin", "update", p.id, "--scope", p.scope, "--json"); err != nil {
		return fmt.Errorf("updating the %s plugin: %w", p.id, err)
	}
	return nil
}

// --- the binary half ---

type binaryPlan struct {
	tier    buildTier
	current string
}

func resolveBinaryPlan() binaryPlan {
	tier, v := polakoBuildTier()
	return binaryPlan{tier: tier, current: normalizeBuildVersion(tier, v)}
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
// any other tier: a stamped release binary is a download (ticket 4, out of
// scope here) and a VCS build is the operator's own rebuild — neither has
// anything for `go install` to do.
func applyBinary(ctx context.Context, cfg config, published string) error {
	target := updateModulePath + "@v" + published
	if _, err := capture(ctx, cfg.dir, cfg.goBin, "install", target); err != nil {
		return fmt.Errorf("go install %s: %w", target, err)
	}
	return nil
}

// goInstallDir is where `go install` with no explicit output path would put
// this binary: GOBIN if set, else GOPATH's bin directory. Best-effort — a
// `go env` that fails leaves the warning goInstallWarning would add unsaid
// rather than blocking the install itself.
func goInstallDir(ctx context.Context, cfg config) (string, error) {
	if out, err := capture(ctx, cfg.dir, cfg.goBin, "env", "GOBIN"); err == nil {
		if dir := strings.TrimSpace(string(out)); dir != "" {
			return dir, nil
		}
	}
	out, err := capture(ctx, cfg.dir, cfg.goBin, "env", "GOPATH")
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
// copy rather than over it — os.Executable() isn't under the directory
// goInstallDir names. "" when there's nothing to warn about, including
// every case goInstallDir or os.Executable() couldn't answer: a warning
// built on an unknown is worse than none.
func goInstallWarning(ctx context.Context, cfg config) string {
	installDir, err := goInstallDir(ctx, cfg)
	if err != nil {
		return ""
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	running, err1 := filepath.EvalSymlinks(filepath.Dir(exe))
	want, err2 := filepath.EvalSymlinks(installDir)
	if err1 != nil || err2 != nil || running == want {
		return ""
	}
	return fmt.Sprintf("this binary is running from %s, but `go install` would put the new one in %s — "+
		"the running copy won't be replaced until you put that directory on PATH or run it from there",
		filepath.Dir(exe), installDir)
}

// releaseAssetName is the asset a stamped binary's release attaches for
// this GOOS/GOARCH — release.yml's own naming, `polako_v<version>_<goos>_
// <goarch>[.exe]` — named here rather than downloaded: replacing a stamped
// binary is ticket 4, out of scope for this verb yet.
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
			b.current, published, updateModulePath, published), true, goInstallWarning(ctx, cfg)
	case tierStamped:
		if b.current == published {
			return fmt.Sprintf("binary: release build, %s, already current", b.current), false, ""
		}
		return fmt.Sprintf("binary: release build, %s, published is %s — download %s from %s",
			b.current, published, releaseAssetName(published), releaseURL(published)), false, ""
	case tierVCS:
		return fmt.Sprintf("binary: built from source (%s) — rebuild it yourself", b.current), false, ""
	default: // tierUnknown
		return "binary: no version information at all (a `go run`, or a stripped build) — " +
			"reinstall or rebuild it yourself", false, ""
	}
}

// applyUpdate prints the plan — always, -check or not, since the two have to
// say the same thing — then, on a real run with something to do, runs it and
// prints the closing line. Nothing here mutates anything before this point:
// every read (the published-version fetch, `plugin list`, `go env`) has
// already happened in runUpdate and resolveBinaryPlan by the time this is
// called, so -check's "reads only" promise holds structurally, not by a flag
// this function has to remember to check before every call.
func applyUpdate(ctx context.Context, cfg config, check bool, published string, plugin pluginPlan, binary binaryPlan, out io.Writer) error {
	pLine, pAction := pluginSummary(plugin, published)
	bLine, bAction, warn := binarySummary(ctx, cfg, binary, published)

	fmt.Fprintf(out, "published: %s\n%s\n%s\n", published, pLine, bLine)
	if warn != "" {
		fmt.Fprintf(out, "warning: %s\n", warn)
	}

	if !pAction && !bAction {
		log.Println("nothing to run — both halves are already current, or neither can be updated by this verb")
		return nil
	}
	if check {
		log.Println("-check: the plan above, nothing run")
		return nil
	}

	if pAction {
		if err := applyPlugin(ctx, cfg, plugin); err != nil {
			return err
		}
	}
	if bAction {
		if err := applyBinary(ctx, cfg, published); err != nil {
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
	log.Printf("done — %s — restart Claude Code, or `/reload-plugins`", strings.Join(done, ", "))
	return nil
}
