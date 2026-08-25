// Command backlog-drain drives Claude Code through a repository's GitHub
// issues, strictly in ascending order, one at a time.
//
// For each open issue it runs `claude -p "/implement-issue N"`, then waits on
// GitHub until the resulting PR is merged before advancing — so every run
// branches from a default branch that already contains the previous merge, and
// sequential runs can't conflict with each other. An issue that cannot be
// finished is parked for a human instead of merged, which advances the queue
// without ever putting two issues in flight.
//
// All state lives in GitHub (issues, comments, PRs, branches). This process
// is stateless and restart-safe: kill it any time, rerun it later, and it
// re-derives where things stand. The only human touchpoints are answering
// clarification comments and merging PRs — both on GitHub.
//
// The one thing it writes locally is run data: a line of numbers per run,
// under ~/.backlog-drain, which nothing here ever reads back. See metrics.go.
//
// Nothing here is tied to one repository or language: point -dir at any GitHub
// checkout, and use -tools/-add-tools to match that project's ecosystem.
//
// Dependencies: the `claude`, `gh` and `git` CLIs on PATH, authenticated.
// Stdlib-only Go, so it cross-compiles to a single binary for any platform.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"maps"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// skillDir is the per-issue skill this repo ships under skills/.
const skillDir = "implement-issue"

// defaultSkill is how that skill is invoked once installed as a plugin: Claude
// namespaces plugin skills as <plugin>:<skill>. A skill hand-copied into
// ~/.claude/skills is invoked bare instead, so that install path needs
// -skill implement-issue. Point -skill anywhere else to drive a different
// workflow with the same supervisor.
const defaultSkill = "backlog-drain:" + skillDir

// pluginName is the half of defaultSkill that names the plugin: the id
// `claude plugin list` reports an installed copy under, and the prefix of the
// release tag the plugin tooling creates.
var pluginName, _, _ = strings.Cut(defaultSkill, ":")

// version is stamped at build time with -ldflags "-X main.version=$tag", which
// is how a prebuilt release binary knows which release it is. A `go install`
// learns the same thing from the module version, and a build from a clone falls
// back to the revision — see drainVersion in metrics.go.
var version string

// errNoWork marks a run whose prompt never resolved to a skill — almost
// always a -skill naming a slash command this installation does not have.
// Two detections funnel here: the init event listing the session's commands
// without the one the prompt invokes (CLIs from 2.1.85 answer an unknown
// command with an ordinary-looking success result, so the list is the only
// early tell), and a clean exit at zero turns (how older CLIs reported it).
var errNoWork = errors.New("the prompt never resolved to a skill")

// errAuth marks a run whose credentials the API refused. It is a dead end,
// not a crash: nothing this process does changes the token, so resuming the
// session thirty seconds later buys another 401 and another few minutes of
// wall clock. Runs carrying it stop the drain rather than retry it.
var errAuth = errors.New("claude could not authenticate")

// authAdvice attaches the fix to an authentication failure. This process runs
// unattended, so its last log line is usually the whole diagnosis a human
// gets, and "needs a human" without the remedy wastes the trip.
func authAdvice(err error) error {
	return fmt.Errorf("%w — check `claude auth status`, then `claude auth login` "+
		"(or `claude setup-token` for an unattended host), and start the drain again", err)
}

// parkedError ends work on one issue without ending the drain. The distinction
// is the whole point: an unimplementable issue is a fact about that issue, and
// stopping the session over it strands every later issue too — typically hours
// before anyone looks at the terminal. Fatal is reserved for conditions where
// no further progress is possible at all: a bad -dir, a gh that cannot answer,
// a -skill this installation does not have, a token the API refuses.
type parkedError struct{ reason string }

func (e *parkedError) Error() string { return e.reason }

// park stops an issue and states why in terms a person can act on — the text
// goes on the issue thread and into the exit summary, so it is written for a
// reader who was not watching.
func park(format string, a ...any) error {
	return &parkedError{reason: fmt.Sprintf(format, a...)}
}

// parkReason reports whether an error parks its issue, and why.
func parkReason(err error) (string, bool) {
	var pe *parkedError
	if errors.As(err, &pe) {
		return pe.reason, true
	}
	return "", false
}

// needsHumanLabel takes a parked issue out of the queue. It is deliberately the
// only durable trace of a park: the next drain re-derives its queue from
// GitHub, and without the label it would pick the same unimplementable issue
// straight back up. Removing the label is how an operator puts it back in.
const needsHumanLabel = "needs-human"

// defaultTools is the --allowedTools set for unattended runs: everything the
// implement-issue skill needs, plus the build/test entry points of the common
// ecosystems. Replace it with -tools, or extend it with -add-tools.
//
// gh is granted per subcommand rather than as Bash(gh:*). The run's input —
// issue bodies and comments — is attacker-controllable on any repository that
// accepts issues from outside the team, and a blanket grant hands it `gh api`,
// `gh secret set` and `gh repo delete`. Verb-level grants are not enough
// either: `gh pr:*` includes `gh pr merge`, which would let a run merge its
// own PR past the human check, and `gh issue:*` includes
// `gh issue edit --add-label`, which would let one labelled issue pull an
// unlabelled one into a -label-gated queue.
//
// The skill itself only needs issue view/comment and pr create. The read-only
// pr lookups are here because a resumed run orients itself before deciding
// what to do, and a gh call that raises a prompt hangs an unattended run
// silently — the one failure mode worse than being too narrow. Nothing else
// that writes is granted; that is what -add-tools is for.
const defaultTools = "Bash(git:*)," +
	"Bash(gh issue view:*),Bash(gh issue comment:*)," +
	"Bash(gh pr create:*),Bash(gh pr view:*),Bash(gh pr list:*),Bash(gh pr diff:*)," +
	"Read,Write,Edit,Glob,Grep,TodoWrite,Skill," +
	"Bash(npm:*),Bash(npx:*),Bash(pnpm:*),Bash(yarn:*)," +
	"Bash(go:*),Bash(cargo:*),Bash(make:*)," +
	"Bash(python:*),Bash(python3:*),Bash(pytest:*),Bash(uv:*)," +
	"Bash(dotnet:*),Bash(mvn:*),Bash(gradle:*)"

type config struct {
	dir       string
	claudeBin string
	// ghBin is a test seam, not an interface: the suite is hermetic and never
	// calls a real gh, but the drain loop is mostly GitHub bookkeeping and is
	// worth covering end to end. No flag sets it — parseFlags pins it to "gh".
	ghBin          string
	skill          string
	branchPrefix   string
	label          string
	tools          string
	addTools       string
	permissionMode string
	model          string
	poll           time.Duration
	retries        int
	retryWait      time.Duration
	stall          time.Duration
	skip           map[int]bool
	once           bool

	// Run-data capture. tag labels a batch of runs so configurations can be
	// compared later; rec is the sink, and writes nothing when -metrics is off.
	// postSummary is the one opt-in that shows those numbers to anybody else:
	// a numbers-only comment on each merged PR.
	tag         string
	rec         *recorder
	postSummary bool

	// Filled in by preflight, recorded with every run: which repository this
	// is, which CLI produced its numbers, and which release of the skill it
	// drove. pluginVersion is empty when -skill names a hand-installed skill,
	// which has no plugin to report a version.
	repo          string
	claudeVersion string
	pluginVersion string
}

func main() {
	// One subcommand, dispatched before any flag is parsed so that a bare
	// invocation still drains and nothing existing changes. `stats` only
	// reads the run data; it never touches GitHub or starts a run.
	if len(os.Args) > 1 && os.Args[1] == "stats" {
		log.SetFlags(0) // a report, not a log
		if err := runStats(os.Args[2:], os.Stdout, time.Now()); err != nil {
			if errors.Is(err, errFlagsReported) {
				os.Exit(2) // the usage is already on screen
			}
			log.Fatalf("stats: %v", err)
		}
		return
	}

	cfg := parseFlags()

	// Ctrl+C cancels the context: in-flight waits end promptly, and a running
	// claude process receives the interrupt through CommandContext.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	log.SetFlags(log.Ldate | log.Ltime)
	if err := run(ctx, cfg); err != nil {
		if errors.Is(err, context.Canceled) {
			log.Println("interrupted — state is on GitHub; rerun to resume")
			os.Exit(130)
		}
		log.Fatalf("stopping: %v", err)
	}
}

func parseFlags() config {
	var cfg config
	var skip, metrics string
	var showVersion bool
	flag.BoolVar(&showVersion, "version", false, "print the version of this binary and exit")
	flag.StringVar(&cfg.dir, "dir", ".", "path to the repository's main checkout")
	flag.StringVar(&cfg.claudeBin, "claude", "claude", "claude binary to invoke")
	flag.StringVar(&cfg.skill, "skill", defaultSkill, "skill to run per issue")
	flag.StringVar(&cfg.branchPrefix, "branch-prefix", "issue-", "branch name prefix the skill uses")
	flag.StringVar(&cfg.label, "label", "", "only process issues carrying this label (empty = all)")
	flag.StringVar(&cfg.tools, "tools", defaultTools,
		"comma-separated --allowedTools for unattended runs")
	flag.StringVar(&cfg.addTools, "add-tools", "",
		"extra --allowedTools entries, appended to -tools instead of replacing it")
	flag.StringVar(&cfg.permissionMode, "permission-mode", "acceptEdits", "claude --permission-mode")
	flag.StringVar(&cfg.model, "model", "", "claude --model for every run (empty = whatever the CLI defaults to)")
	flag.DurationVar(&cfg.poll, "poll", 5*time.Minute, "interval between GitHub checks while waiting")
	flag.IntVar(&cfg.retries, "retries", 3, "resume attempts after a crashed claude run (nonzero exit)")
	flag.DurationVar(&cfg.retryWait, "retry-wait", 30*time.Second, "wait before each resume attempt")
	flag.DurationVar(&cfg.stall, "stall", 15*time.Minute, "kill and resume a run with no output events for this long (0 disables)")
	flag.StringVar(&skip, "skip", "", "comma-separated issue numbers to skip (head-of-line escape hatch)")
	flag.BoolVar(&cfg.once, "once", false, "process a single issue to a merge or a park, then exit")
	flag.StringVar(&cfg.tag, "run-tag", "", "label recorded with every run, for comparing one batch against another")
	flag.BoolVar(&cfg.postSummary, "post-summary", false,
		"comment one line of run numbers on each merged PR (runs, tokens, dollars, wall time)")
	flag.StringVar(&metrics, "metrics", "",
		`directory for run-data records, or "off" (default ~/.backlog-drain/metrics)`)
	flag.Usage = func() {
		fmt.Fprint(flag.CommandLine.Output(),
			"Usage: backlog-drain [flags]        drain the backlog, one issue at a time\n"+
				"       backlog-drain stats [flags]  report on the run data already recorded\n\n"+
				envUsage+"\nFlags:\n")
		flag.PrintDefaults()
	}
	// Before Parse, so an argument on the command line always wins over a
	// preference set in the environment.
	if err := applyEnvDefaults(flag.CommandLine); err != nil {
		log.Fatalf("%v", err)
	}
	flag.Parse()

	// Answered before anything else a flag implies, so it stays usable on a
	// machine where -dir points nowhere: this is the flag an operator reaches
	// for when the startup warning says the two halves disagree.
	if showVersion {
		fmt.Println(describeVersion())
		os.Exit(0)
	}

	cfg.rec = newRecorder(metrics)
	cfg.ghBin = "gh"
	cfg.skip = parseSkip(skip)
	abs, err := filepath.Abs(cfg.dir)
	if err != nil {
		log.Fatalf("resolving -dir: %v", err)
	}
	cfg.dir = abs
	return cfg
}

// envPrefix namespaces the variables that set flag defaults: -post-summary
// reads BACKLOG_DRAIN_POST_SUMMARY.
const envPrefix = "BACKLOG_DRAIN_"

// envUsage is the one line of help that makes the mechanism discoverable. A
// default nobody knows how to set is a default nobody sets.
const envUsage = "Any flag below can take its default from the environment:\n" +
	"BACKLOG_DRAIN_<FLAG>, e.g. BACKLOG_DRAIN_POST_SUMMARY=1. Arguments win.\n"

// envExempt are the flags the environment may not set. -version is an action
// rather than a preference, and BACKLOG_DRAIN_VERSION is exactly the variable
// a Dockerfile or CI job pins an install with — where it would quietly turn
// every drain into a version print that exits before doing any work.
var envExempt = map[string]bool{"version": true}

// applyEnvDefaults lets an operator set a per-machine default for any flag, so
// a preference they always want lives in a shell profile instead of being
// typed on every invocation. Call it before Parse: the command line is read
// afterwards, so an argument always wins.
//
// A value the flag cannot parse stops the process rather than being skipped.
// Ignoring it would leave a preference that was set, looks set, and does
// nothing — which for an unattended run is the worst of the three outcomes.
func applyEnvDefaults(fs *flag.FlagSet) error {
	var err error
	fs.VisitAll(func(f *flag.Flag) {
		if err != nil || envExempt[f.Name] {
			return
		}
		name := envVarName(f.Name)
		v, ok := os.LookupEnv(name)
		if !ok {
			return
		}
		if serr := f.Value.Set(v); serr != nil {
			// flag's own parse errors are terse ("parse error"), and this one
			// stops a drain before it starts, so say what to do about it.
			err = fmt.Errorf("%s=%q is not a valid -%s (%w) — fix the value or unset the variable",
				name, v, f.Name, serr)
			return
		}
		// Keep -h honest: PrintDefaults reports DefValue, and the default in
		// force is now the one the environment set.
		f.DefValue = f.Value.String()
	})
	return err
}

// envVarName is the variable that sets one flag's default.
func envVarName(flagName string) string {
	return envPrefix + strings.ToUpper(strings.ReplaceAll(flagName, "-", "_"))
}

// parseSkip reads a comma-separated issue list. Unparseable entries are
// ignored, so a stray comma or space can't stop a run.
func parseSkip(s string) map[int]bool {
	skip := map[int]bool{}
	for _, f := range strings.Split(s, ",") {
		if n, err := strconv.Atoi(strings.TrimSpace(f)); err == nil {
			skip[n] = true
		}
	}
	return skip
}

// resolveTools joins the base list with any -add-tools additions, dropping
// blanks and duplicates so trailing commas in either flag are harmless.
func resolveTools(tools, add string) string {
	var out []string
	for _, part := range strings.Split(tools+","+add, ",") {
		if p := strings.TrimSpace(part); p != "" && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return strings.Join(out, ",")
}

func run(ctx context.Context, cfg config) error {
	if err := preflight(ctx, &cfg); err != nil {
		return err
	}
	return drain(ctx, cfg)
}

// issueResult is one issue's fate, kept only long enough to print the summary
// this process ends with. Nothing reads it back afterwards — the durable record
// of a park is the label on GitHub.
type issueResult struct {
	issue  int
	parked bool
	reason string // why it parked; empty when it merged
}

// drain works the queue until it empties, an issue proves fatal, or -once says
// stop. An issue that cannot be finished is parked rather than fatal, so a
// backlog with one bad issue in it still drains overnight.
func drain(ctx context.Context, cfg config) error {
	started := time.Now()

	// Parked issues leave the queue by their label, but labelling is a network
	// call that can fail, and a park the label did not take would put this loop
	// straight back on the same issue. Remembering them here is what guarantees
	// it terminates. It is not state: nothing reads it after the process ends,
	// and a restart re-derives the queue from GitHub alone.
	skip := maps.Clone(cfg.skip)
	if skip == nil {
		skip = map[int]bool{}
	}
	cfg.skip = skip

	var results []issueResult
	// Every exit goes through finish, fatal ones included: a session that died
	// on issue nine should still account for the eight before it. The issue it
	// died on is not among them — unfinished is not an outcome — so a run that
	// dies on its first issue has nothing to summarize and says nothing.
	finish := func(err error) error {
		for _, line := range drainSummary(results, time.Since(started)) {
			log.Print(line)
		}
		return err
	}

	for {
		issue, err := lowestOpenIssue(ctx, cfg)
		if err != nil {
			return finish(err)
		}
		if issue == 0 {
			log.Println("no open issues — backlog drained")
			return finish(nil)
		}
		log.Printf("=== issue #%d ===", issue)

		err = processIssue(ctx, cfg, issue)
		switch reason, parked := parkReason(err); {
		case parked:
			skip[issue] = true
			results = append(results, issueResult{issue: issue, parked: true, reason: reason})
			log.Printf("issue #%d needs a human: %s — parking it and moving on", issue, reason)
			parkIssue(ctx, cfg, issue, reason)
		case err != nil:
			return finish(fmt.Errorf("issue #%d: %w", issue, err))
		default:
			results = append(results, issueResult{issue: issue})
		}
		if cfg.once {
			log.Println("-once set — exiting after one issue")
			return finish(nil)
		}
	}
}

// parkIssue hands one issue back to a person: the label that takes it out of
// the queue, and a comment saying what happened. Both are best-effort — a
// GitHub call that fails must not end a drain that is otherwise healthy — but
// a label that did not take means the next drain retries this issue, so that
// one says so out loud rather than failing quietly.
func parkIssue(ctx context.Context, cfg config, issue int, reason string) {
	n := strconv.Itoa(issue)
	_, err := gh(ctx, cfg, "issue", "edit", n, "--add-label", needsHumanLabel)
	if err != nil {
		// Far the likeliest cause is a repository that has never used the
		// label: GitHub refuses to add one that does not exist yet.
		if _, cerr := gh(ctx, cfg, "label", "create", needsHumanLabel, "--color", "D93F0B",
			"--description", "backlog-drain parked this issue for a human"); cerr == nil {
			_, err = gh(ctx, cfg, "issue", "edit", n, "--add-label", needsHumanLabel)
		}
	}
	if err != nil {
		log.Printf("could not label issue #%d %q (%v) — the next drain will pick it up again "+
			"unless you label it yourself or close it", issue, needsHumanLabel, err)
	}
	body := fmt.Sprintf("**backlog-drain parked this issue.** %s\n\n"+
		"Nothing will run on it again until the `%s` label is removed.", reason, needsHumanLabel)
	if _, cerr := gh(ctx, cfg, "issue", "comment", n, "--body", body); cerr != nil {
		log.Printf("could not comment on issue #%d (%v) — the reason is in this log and in the exit summary",
			issue, cerr)
	}
}

// drainSummary is what the process says on its way out: what merged, what was
// parked and why, and how long it all took. Returned as lines rather than one
// blob so each carries the log's own timestamp.
func drainSummary(results []issueResult, elapsed time.Duration) []string {
	if len(results) == 0 {
		return nil
	}
	var merged []string
	var parked []string
	for _, r := range results {
		if r.parked {
			parked = append(parked, fmt.Sprintf("  parked  #%d — %s", r.issue, r.reason))
			continue
		}
		merged = append(merged, "#"+strconv.Itoa(r.issue))
	}
	lines := []string{fmt.Sprintf("summary: %s merged, %s parked, %s of wall clock",
		plural(len(merged), "issue"), plural(len(parked), "issue"), dur(elapsed))}
	if len(merged) > 0 {
		lines = append(lines, "  merged  "+strings.Join(merged, ", "))
	}
	return append(lines, parked...)
}

// preflight fails fast on a misconfigured environment, so an unattended run
// can't die on its first gh call an hour after being started.
func preflight(ctx context.Context, cfg *config) error {
	for _, bin := range []string{cfg.claudeBin, cfg.ghBin, "git"} {
		if _, err := exec.LookPath(bin); err != nil {
			return fmt.Errorf("%q not found on PATH: %w", bin, err)
		}
	}
	if _, err := git(ctx, *cfg, "rev-parse", "--git-dir"); err != nil {
		return fmt.Errorf("-dir %s is not a git checkout: %w", cfg.dir, err)
	}
	out, err := gh(ctx, *cfg, "repo", "view", "--json", "nameWithOwner", "--jq", ".nameWithOwner")
	if err != nil {
		return fmt.Errorf("no GitHub repository reachable from %s (is gh authenticated?): %w", cfg.dir, err)
	}
	cfg.repo = strings.TrimSpace(string(out))
	cfg.claudeVersion = claudeVersion(ctx, *cfg)
	cfg.pluginVersion = pluginVersion(ctx, *cfg)
	log.Printf("%s — running /%s per issue, polling every %s", cfg.repo, cfg.skill, cfg.poll)
	warnOnVersionSkew(drainVersion(), *cfg)
	if cfg.postSummary {
		// The environment can set this, so say it out loud: an operator who
		// forgot the variable is in their profile should not have to work out
		// where the PR comments are coming from.
		log.Printf("-post-summary is on — each merged PR gets one comment of run numbers")
	}
	if cfg.rec.enabled() {
		// Say where the data goes, every time, unprompted: it is the whole of
		// the answer to "what does this tool record".
		log.Printf("recording run data in %s — numbers only, never leaves this machine (-metrics off to disable)",
			cfg.rec.dir)
	}
	return nil
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
// asking the CLI what it has installed. Best-effort in the same way as
// claudeVersion, and empty rather than wrong in the two cases where there is no
// honest answer: a -skill with no plugin prefix names a hand-installed skill,
// which carries no version at all, and a CLI too old for `plugin list --json`
// fails the call.
func pluginVersion(ctx context.Context, cfg config) string {
	plugin, _, ok := strings.Cut(cfg.skill, ":")
	if !ok || plugin == "" {
		return ""
	}
	out, err := capture(ctx, cfg.dir, cfg.claudeBin, "plugin", "list", "--json")
	if err != nil {
		return ""
	}
	var installed []struct {
		ID      string `json:"id"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(out, &installed); err != nil {
		return ""
	}
	// The id is <plugin>@<marketplace>; the marketplace is whatever the
	// operator named it when they added it, so only the plugin half is ours to
	// match on.
	for _, p := range installed {
		if name, _, _ := strings.Cut(p.ID, "@"); name == plugin {
			return p.Version
		}
	}
	return ""
}

// warnOnVersionSkew reports a binary and a skill that did not ship together.
// The two halves share one version number by design — the supervisor finds a
// PR by the head branch the skill names, so a mismatched pair fails later and
// far less legibly than this. It stays a warning: an operator testing a new
// binary against an installed release is doing something deliberate, and
// nothing here is safe to guess about.
func warnOnVersionSkew(binary string, cfg config) {
	// Only this repo's own plugin shares a version line with this binary.
	// -skill is documented as pointing anywhere, and another plugin's versions
	// mean nothing here — comparing them would warn on every run of a
	// deliberate configuration, and name the wrong plugin while doing it.
	if name, _, _ := strings.Cut(cfg.skill, ":"); name != pluginName {
		return
	}
	self, selfIsRelease := releaseVersion(binary)
	plugin, pluginIsRelease := releaseVersion(cfg.pluginVersion)
	// A binary built from a clone reports a revision, not a release. That is
	// not skew, it is an unreleased build, and warning about it every time
	// would train an operator to ignore the one message that matters.
	if !selfIsRelease || !pluginIsRelease || self == plugin {
		return
	}
	log.Printf("version skew: this binary is %s but the installed %s plugin is %s — "+
		"they are meant to ship together, and the supervisor finds a PR by the "+
		"branch name the skill chooses. Bring both to the current release: "+
		"`claude plugin marketplace update && claude plugin update %s`, then "+
		"`go install github.com/scharissis/backlog-drain/cmd/backlog-drain@latest`",
		self, pluginName, plugin, pluginName)
}

// releaseVersion normalizes a version that names a release, and reports false
// for anything that does not — an empty string, or the revision a build from a
// clone carries. The `v` prefix is optional because the binary picks one up
// from a module version and none from an -ldflags stamp.
func releaseVersion(s string) (string, bool) {
	s = strings.TrimPrefix(s, "v")
	if _, err := parseSemver(s); err != nil {
		return "", false
	}
	return s, true
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
	v := drainVersion()
	if v == "" {
		return pluginName + " (unknown version: built without module or VCS information)"
	}
	return pluginName + " " + v
}

// processIssue advances one issue all the way to merged, however many
// implement/wait cycles that takes.
func processIssue(ctx context.Context, cfg config, issue int) error {
	branch := fmt.Sprintf("%s%d", cfg.branchPrefix, issue)
	attempt := 0      // 0 = fresh skill run; >0 = retry after a crash
	sessionID := ""   // from the run's init event; resume target for retries
	answered := false // a human replied, so the next run folds the answers in

	// tally is what -post-summary reports, and the only thing this function
	// carries across runs besides the resume target. Nothing reads it back
	// once the process ends, so it stays telemetry rather than state: a
	// supervisor restarted mid-issue starts a fresh one, and the comment it
	// feeds says it covers this drain.
	tally := &issueTally{}

	// terminal marks how the issue ended, failures included — they are the
	// most informative rows in the dataset, and every one of them ends this
	// issue: merged, or parked for a human and left behind. Transient GitHub
	// errors and Ctrl+C are deliberately not outcomes: the issue is still open
	// and unparked, and the next drain resumes it.
	terminal := func(prNumber int, outcome string) {
		cfg.rec.recordIssue(cfg, issue, prNumber, outcome, lookupPRFacts(ctx, cfg, prNumber))
	}

	for {
		// Restart safety: if a PR already exists for this branch, never
		// re-run Claude — go straight to waiting on it.
		pr, err := prForBranch(ctx, cfg, branch)
		if err != nil {
			return err
		}

		if pr == nil {
			before, err := commentCount(ctx, cfg, issue)
			if err != nil {
				return err
			}

			resumeTarget, reason := "", reasonImplement
			switch {
			case attempt > 0:
				resumeTarget = sessionID // empty if the crashed run never got a session
				reason = reasonResume
			case answered:
				reason = reasonAnswers
			}
			answered = false

			started := time.Now()
			rep, runErr := runClaude(ctx, cfg, issue, resumeTarget)
			rc := runContext{
				issue: issue, reason: reason, attempt: attempt,
				resumedFrom: resumeTarget, started: started, ended: time.Now(),
			}
			if rep.sessionID != "" {
				sessionID = rep.sessionID
			}
			// record closes over the run; the outcome is whatever the checks
			// below turn out to find, so every exit from here on passes one.
			record := func(prNumber int, outcome string) {
				rc.pr, rc.outcome = prNumber, outcome
				tally.add(cfg.rec.recordRun(cfg, rc, rep))
			}

			if runErr != nil {
				if ctx.Err() != nil {
					record(0, outcomeUnknown)
					return ctx.Err()
				}
				// A prompt that never resolved will never resolve on a retry,
				// and the generic "no PR and no questions" report buries the
				// cause. Say what is actually wrong and stop.
				if errors.Is(runErr, errNoWork) {
					record(0, outcomeNothing)
					terminal(0, issueNeedsHuman)
					return fmt.Errorf("%w — check that -skill %q names a skill this "+
						"installation has; plugin skills are namespaced <plugin>:<skill>, "+
						"a skill copied into ~/.claude/skills is not",
						runErr, cfg.skill)
				}
				log.Printf("claude run ended with error (%v) — checking what it left behind", runErr)
			}

			pr, err = prForBranch(ctx, cfg, branch)
			if err != nil {
				record(0, outcomeUnknown)
				return err
			}
			if pr == nil {
				after, err := commentCount(ctx, cfg, issue)
				if err != nil {
					record(0, outcomeUnknown)
					return err
				}
				switch {
				case errors.Is(runErr, errAuth):
					// The token does not come back on its own, so every
					// resume spends minutes reaching the same 401 and buries
					// the cause under a report about crashes. Stop the drain:
					// every later issue would hit the same wall.
					//
					// Checked here rather than beside errNoWork, which stops
					// before the PR lookup: a token can die after the run
					// opened its PR, and that case belongs on the waiting
					// path above, which needs no token at all.
					//
					// Always outcomeNothing, even if the comment count grew:
					// a run refused at the door cannot have commented, so a
					// new comment is a human's, and crediting it to the run
					// would bias every questions rate stats computes.
					record(0, outcomeNothing)
					terminal(0, issueNeedsHuman)
					return authAdvice(runErr)
				case after > before:
					// Questions were posted (even if the run then crashed):
					// wait for a human reply, then fold it in with a fresh run.
					record(0, outcomeQuestions)
					log.Printf("blocked on questions — waiting for a reply on the issue thread")
					if err := waitForComments(ctx, cfg, issue, after); err != nil {
						return err
					}
					log.Printf("new activity on #%d — re-running to fold the answers in", issue)
					attempt, answered = 0, true
					continue
				case runErr != nil && attempt < cfg.retries:
					// Crash (API drop, stall, tool failure): resume the exact
					// session by ID, keeping its research context. If no
					// session was ever created, retry as a fresh run instead.
					record(0, outcomeNothing)
					attempt++
					mode := "restarting fresh"
					if sessionID != "" {
						mode = "resuming session " + sessionID
					}
					log.Printf("%s (attempt %d/%d) in %s",
						mode, attempt, cfg.retries, cfg.retryWait)
					if err := sleep(ctx, cfg.retryWait); err != nil {
						return err
					}
					continue
				case runErr != nil:
					record(0, outcomeNothing)
					terminal(0, issueNeedsHuman)
					return park("claude crashed and %d resume attempts failed", cfg.retries)
				default:
					// Clean exit, yet no PR and no questions: Claude decided
					// nothing, which a machine shouldn't paper over.
					record(0, outcomeNothing)
					terminal(0, issueNeedsHuman)
					return park("the run completed but produced no PR and no questions")
				}
			}
			record(pr.Number, outcomeOpenedPR)
			attempt = 0
		}

		switch pr.State {
		case "OPEN":
			log.Printf("PR #%d open — waiting for merge (%s)", pr.Number, pr.URL)
			state, err := supervisePR(ctx, cfg, issue, pr.Number, tally)
			if err != nil {
				if ctx.Err() == nil { // not Ctrl+C: remediation ran out of attempts
					terminal(pr.Number, issueNeedsHuman)
				}
				return err
			}
			pr.State = state
			fallthrough
		case "MERGED", "CLOSED":
			if pr.State == "MERGED" {
				log.Printf("PR #%d merged — cleaning up and advancing", pr.Number)
				cleanupWorktree(ctx, cfg, issue)
				terminal(pr.Number, issueMerged)
				postSummary(ctx, cfg, pr.Number, *tally)
				return ensureIssueClosed(ctx, cfg, issue, pr.Number)
			}
			terminal(pr.Number, issueClosed)
			return park("PR #%d was closed without merging, which is a decision only a human can make",
				pr.Number)
		default:
			terminal(pr.Number, issueNeedsHuman)
			return park("PR #%d is in the unexpected state %q", pr.Number, pr.State)
		}
	}
}

// --- Claude ---

// runClaude executes one headless skill run. A non-empty resumeID continues
// that exact session instead of starting the skill fresh.
func runClaude(ctx context.Context, cfg config, issue int, resumeID string) (runReport, error) {
	prompt, invokes := fmt.Sprintf("/%s %d", cfg.skill, issue), cfg.skill
	if resumeID != "" {
		// Plain English, so invokes is empty: the resumed session already
		// holds the skill's context and never re-resolves the command.
		prompt, invokes = fmt.Sprintf("Continue the /%s %d workflow exactly where it stopped.", cfg.skill, issue), ""
	}
	return execClaude(ctx, cfg, prompt, resumeID, invokes)
}

// buildArgs assembles one headless claude invocation.
func buildArgs(cfg config, prompt, resumeID string) []string {
	var args []string
	if resumeID != "" {
		args = append(args, "--resume", resumeID)
	}
	args = append(args, "-p", prompt, "--permission-mode", cfg.permissionMode)
	if cfg.model != "" {
		args = append(args, "--model", cfg.model)
	}
	return append(args,
		"--allowedTools", resolveTools(cfg.tools, cfg.addTools),
		"--output-format", "stream-json", // one JSON event per message, in real time
		"--verbose", // required for stream-json in print mode
	)
}

// watchTick is how often the stall watchdog samples: often enough to honour a
// short -stall promptly, capped so long production runs stay quiet.
func watchTick(stall time.Duration) time.Duration {
	tick := stall / 4
	if tick > 30*time.Second {
		tick = 30 * time.Second
	}
	if tick < 50*time.Millisecond {
		tick = 50 * time.Millisecond
	}
	return tick
}

// streamEvent is one stream-json line. Every consumer — the progress log and
// the run report — reads this single parse: the stream carries whole file
// contents, so unmarshalling each line once per consumer was never free.
type streamEvent struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	Model     string `json:"model"`
	SessionID string `json:"session_id"`
	// SlashCommands is the init event's inventory of every command the
	// session can invoke — the only early sign of a -skill this installation
	// does not have. CLIs before 2.1.85 do not send it.
	SlashCommands []string `json:"slash_commands"`
	Message       struct {
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
		Usage streamUsage `json:"usage"`
	} `json:"message"`
	DurationMS    int64                       `json:"duration_ms"`
	DurationAPIMS int64                       `json:"duration_api_ms"`
	NumTurns      int                         `json:"num_turns"`
	TotalCost     float64                     `json:"total_cost_usd"`
	IsError       bool                        `json:"is_error"`
	Result        string                      `json:"result"` // the result event's final text
	Usage         streamUsage                 `json:"usage"`
	ModelUsage    map[string]streamModelUsage `json:"modelUsage"`
}

// streamUsage is the token block the CLI hangs off both assistant messages and
// the final result event.
type streamUsage struct {
	Input      int64 `json:"input_tokens"`
	Output     int64 `json:"output_tokens"`
	CacheRead  int64 `json:"cache_read_input_tokens"`
	CacheWrite int64 `json:"cache_creation_input_tokens"`
}

// streamModelUsage is the per-model breakdown on the result event. Two things
// to honour: camelCase keys, unlike the snake_case block above, and older CLI
// versions omit it entirely.
type streamModelUsage struct {
	Input      int64   `json:"inputTokens"`
	Output     int64   `json:"outputTokens"`
	CacheRead  int64   `json:"cacheReadInputTokens"`
	CacheWrite int64   `json:"cacheCreationInputTokens"`
	CostUSD    float64 `json:"costUSD"`
}

// salvageSessionID reads the one field worth recovering from a line the full
// schema rejected.
func salvageSessionID(line []byte) string {
	var v struct {
		SessionID string `json:"session_id"`
	}
	if json.Unmarshal(line, &v) != nil {
		return ""
	}
	return v.SessionID
}

func parseEvent(line []byte) (streamEvent, bool) {
	var ev streamEvent
	if json.Unmarshal(line, &ev) != nil {
		return streamEvent{}, false // not an event we understand
	}
	return ev, true
}

// runReport is what one claude invocation yielded: the session to resume, the
// numbers it reported, and how it ended. It is filled in as the stream
// arrives, so it stays valid — and worth recording — for a run that crashed,
// stalled or was interrupted before it could report anything itself.
type runReport struct {
	sessionID  string
	model      string // what actually ran, which -model only requests
	subtype    string
	isError    bool
	hasResult  bool
	turns      int // -1 until a result event says otherwise
	toolUses   int
	wallMS     int64
	apiMS      int64
	costUSD    float64
	usage      tokenCounts
	modelUsage map[string]modelTokens

	// Observed as the run streamed: the only numbers a crash, a stall or an
	// interrupt leaves behind, since none of the three emits a result event.
	// Approximate by construction — turns counts assistant messages, and cost
	// stays zero because pricing belongs to the CLI, never to this binary.
	observed      tokenCounts
	observedTurns int

	exitCode     int
	stalled      bool
	interrupted  bool
	skillMissing bool // the session's command list lacks the skill the prompt invokes
	authFailed   bool // the result text is the CLI reporting refused credentials
}

// status maps a run to exactly one value, most specific first: a run stopped
// over a missing skill was killed deliberately, an interrupted run is a
// nonzero exit too, and so is a stalled one — and so is a run the API refused
// to authenticate, which is the one worth telling apart from a crash.
func (r runReport) status() string {
	switch {
	case r.skillMissing:
		return "no-skill"
	case r.interrupted:
		return "interrupted"
	case r.stalled:
		return "stalled"
	case r.authFailed:
		return "auth"
	case r.exitCode != 0:
		return "crash"
	case r.isError:
		return "error"
	case r.hasResult && r.turns == 0:
		return "no-turns"
	}
	return "ok"
}

// observe folds one event into the report.
func (r *runReport) observe(ev streamEvent) {
	if ev.SessionID != "" {
		r.sessionID = ev.SessionID
	}
	switch ev.Type {
	case "system":
		if ev.Subtype == "init" && ev.Model != "" {
			r.model = ev.Model
		}
	case "assistant":
		r.observedTurns++
		r.observed.add(ev.Message.Usage)
		for _, c := range ev.Message.Content {
			if c.Type == "tool_use" {
				r.toolUses++
			}
		}
	case "result":
		r.hasResult = true
		r.subtype, r.isError = ev.Subtype, ev.IsError
		r.authFailed = ev.IsError && authFailure(ev.Result)
		r.turns = ev.NumTurns
		r.wallMS, r.apiMS = ev.DurationMS, ev.DurationAPIMS
		r.costUSD = ev.TotalCost
		r.usage = tokenCounts{}
		r.usage.add(ev.Usage)
		for name, u := range ev.ModelUsage {
			if r.modelUsage == nil {
				r.modelUsage = make(map[string]modelTokens, len(ev.ModelUsage))
			}
			r.modelUsage[name] = modelTokens{
				tokenCounts: tokenCounts{In: u.Input, Out: u.Output,
					CacheRead: u.CacheRead, CacheWrite: u.CacheWrite},
				CostUSD: u.CostUSD,
			}
		}
	}
}

// lacksCommand reports whether an init event's command inventory is present
// and cmd is missing from it. An absent inventory (CLIs before 2.1.85) is no
// evidence either way, and entries are compared with any leading slash
// stripped: a wrong "missing" verdict kills a healthy run, so every
// uncertainty has to resolve toward "found".
func lacksCommand(commands []string, cmd string) bool {
	if cmd == "" || len(commands) == 0 {
		return false
	}
	return !slices.ContainsFunc(commands, func(c string) bool {
		return strings.TrimPrefix(c, "/") == cmd
	})
}

// nearMatches returns inventory entries that differ from cmd only by plugin
// namespacing — the exact confusion the missing-skill error warns about, so
// naming the spelling the session does have turns that warning into a fix.
func nearMatches(commands []string, cmd string) []string {
	tail := func(s string) string {
		if i := strings.LastIndexByte(s, ':'); i >= 0 {
			return s[i+1:]
		}
		return s
	}
	var near []string
	for _, c := range commands {
		if c = strings.TrimPrefix(c, "/"); tail(c) == tail(cmd) {
			near = append(near, "/"+c)
		}
	}
	return near
}

// authFailure reports whether a run's final text is the CLI saying the API
// refused its credentials.
//
// The match is anchored to the head of the message, not merely contained in
// it, because a run that *quotes* a 401 is the likelier sight on this repo's
// own backlog: an issue about OAuth, a run that then hits max turns, and a
// final message repeating the error out of the issue body. Refusing that run
// a retry and stopping the drain over a healthy token is a worse failure than
// missing an unrecognised wrapper — which costs only the retries this code
// already spent before. So, as with lacksCommand, every uncertainty resolves
// toward "keep going".
func authFailure(result string) bool {
	head := strings.ToLower(strings.TrimLeft(strings.TrimSpace(result), "*# "))
	return slices.ContainsFunc([]string{
		"failed to authenticate",        // the CLI's own wrapper, and the one observed
		"oauth token has expired",       // a credential it could not refresh
		"oauth access token is invalid", // a revoked or corrupt stored one
		"invalid api key",               // the ANTHROPIC_API_KEY spellings
		"invalid x-api-key",
		"api error: 401",       // the bare status, when no wrapper survives
		"authentication_error", // the raw API envelope, unwrapped
	}, func(sig string) bool { return strings.HasPrefix(head, sig) })
}

// execClaude runs one headless claude invocation with the shared streaming,
// logging, and stall-watchdog machinery. invokes names the slash command the
// prompt starts with — "" for a plain-text prompt — so a session whose init
// inventory lacks it can be stopped instead of run; the caller states it
// outright because re-deriving it from the prompt would make "never start a
// plain prompt with /" a load-bearing rule enforced nowhere. The report it
// returns is valid on every path, error included: the retry logic needs the
// session ID to resume, and the run burned real tokens whether or not it
// lived to report them.
func execClaude(ctx context.Context, cfg config, prompt, resumeID, invokes string) (runReport, error) {
	rep := runReport{sessionID: resumeID, turns: -1, exitCode: -1}
	args := buildArgs(cfg, prompt, resumeID)
	log.Printf("running: %s %s", cfg.claudeBin, strings.Join(args, " "))
	cmd := exec.CommandContext(ctx, cfg.claudeBin, args...)
	cmd.Dir = cfg.dir
	cmd.Stderr = os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return rep, err
	}
	if err := cmd.Start(); err != nil {
		return rep, err
	}

	// Stall watchdog: if no events arrive for cfg.stall, kill the run.
	// The caller's retry logic then resumes the session, context intact.
	var lastEvent atomic.Int64
	lastEvent.Store(time.Now().UnixNano())
	var stalled atomic.Bool
	watchDone := make(chan struct{})
	defer close(watchDone)
	if cfg.stall > 0 {
		go func() {
			t := time.NewTicker(watchTick(cfg.stall))
			defer t.Stop()
			for {
				select {
				case <-watchDone:
					return
				case <-t.C:
					idle := time.Since(time.Unix(0, lastEvent.Load()))
					if idle > cfg.stall {
						log.Printf("no activity for %s — killing the run to resume it",
							idle.Round(time.Millisecond))
						stalled.Store(true)
						_ = cmd.Process.Kill()
						return
					}
				}
			}
		}()
	}

	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64*1024), 32*1024*1024) // events carrying file contents can be large
	missing := ""                                  // the diagnosis, once the inventory rules the prompt's command out
	for sc.Scan() {
		lastEvent.Store(time.Now().UnixNano())
		ev, ok := parseEvent(sc.Bytes())
		if !ok {
			// Junk on stdout, or an event whose JSON does not fit the schema
			// — a content field typed as a string rather than an array, say.
			// Stay quiet about it, but never at the price of the session ID:
			// that one field is what a retry resumes, and it is worth a
			// second, far laxer parse on the rare line that fails the first.
			if id := salvageSessionID(sc.Bytes()); id != "" {
				rep.sessionID = id
			}
			continue
		}
		rep.observe(ev)
		logEvent(ev)
		// An unknown slash command no longer shows up as a zero-turn exit:
		// the CLI (2.1.85) answers it with an ordinary-looking success
		// result. The init event's command inventory is the early tell, and
		// a prompt invoking a command that is not there can only fail — so
		// stop the run now instead of paying for whatever it does next.
		if ev.Type == "system" && ev.Subtype == "init" && lacksCommand(ev.SlashCommands, invokes) {
			missing = fmt.Sprintf("the session has no /%s command", invokes)
			if near := nearMatches(ev.SlashCommands, invokes); len(near) > 0 {
				missing += " (it does list " + strings.Join(near, ", ") + ")"
			}
			rep.skillMissing = true
			log.Printf("%s — stopping the run", missing)
			_ = cmd.Process.Kill()
		}
	}

	err = cmd.Wait()
	if cmd.ProcessState != nil {
		rep.exitCode = cmd.ProcessState.ExitCode()
	}
	rep.stalled = stalled.Load()
	rep.interrupted = ctx.Err() != nil
	if rep.skillMissing {
		return rep, fmt.Errorf("%w: %s", errNoWork, missing)
	}
	if rep.stalled {
		return rep, fmt.Errorf("run stalled: no output events for %s", cfg.stall)
	}
	// Deliberately not gated on the exit code: the CLI reports this as a
	// nonzero exit today, which is exactly what made it look like a crash
	// worth resuming, but a clean exit carrying the same result would be no
	// less of a dead end.
	if rep.authFailed {
		return rep, errAuth
	}
	if err == nil && rep.turns == 0 {
		return rep, fmt.Errorf("%w: %q produced no work", errNoWork, prompt)
	}
	return rep, err
}

// logEvent renders one stream-json event as a single progress line.
func logEvent(ev streamEvent) {
	switch ev.Type {
	case "system":
		if ev.Subtype == "init" {
			log.Printf("[claude] session started (model %s)", ev.Model)
		}
	case "assistant":
		for _, c := range ev.Message.Content {
			switch c.Type {
			case "text":
				if t := strings.TrimSpace(c.Text); t != "" {
					log.Printf("[claude] %s", clip(t, 160))
				}
			case "tool_use":
				log.Printf("[claude] → %s%s", c.Name, toolDetail(c.Input))
			}
		}
	case "result":
		// The final text first: for a healthy run it restates the last
		// assistant message, but a result the CLI synthesized itself —
		// "Unknown skill: x" — appears nowhere else in the stream, and is
		// usually the whole diagnosis.
		if t := strings.TrimSpace(ev.Result); t != "" {
			log.Printf("[claude] %s", clip(t, 160))
		}
		status := "ok"
		if ev.IsError {
			// is_error is the authority, not the subtype: the CLI reports an
			// authentication failure as is_error with subtype "success",
			// which rendered as the self-contradicting "ERROR: success".
			status = "ERROR"
			if ev.Subtype != "" && ev.Subtype != "success" {
				status += ": " + ev.Subtype
			}
		}
		log.Printf("[claude] finished (%s) — %d turns, %s, $%.2f", status, ev.NumTurns,
			(time.Duration(ev.DurationMS) * time.Millisecond).Round(time.Second), ev.TotalCost)
	}
}

// toolDetail extracts the most human-useful field from a tool's input.
func toolDetail(raw json.RawMessage) string {
	var in map[string]any
	if json.Unmarshal(raw, &in) != nil {
		return ""
	}
	for _, k := range []string{"command", "file_path", "pattern", "query", "description"} {
		if v, ok := in[k].(string); ok && v != "" {
			return ": " + clip(v, 120)
		}
	}
	return ""
}

// clip flattens text to one line and truncates it for log output.
func clip(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

// --- GitHub state, via the gh CLI ---

type pullRequest struct {
	Number int    `json:"number"`
	State  string `json:"state"`
	URL    string `json:"url"`
}

// pickLowest returns the smallest number not in skip, or 0 if none remain.
func pickLowest(numbers []int, skip map[int]bool) int {
	lowest := 0
	for _, n := range numbers {
		if skip[n] {
			continue
		}
		if lowest == 0 || n < lowest {
			lowest = n
		}
	}
	return lowest
}

func lowestOpenIssue(ctx context.Context, cfg config) (int, error) {
	args := []string{"issue", "list", "--state", "open", "--limit", "200", "--json", "number,labels"}
	if cfg.label != "" {
		args = append(args, "--label", cfg.label)
	}
	out, err := gh(ctx, cfg, args...)
	if err != nil {
		return 0, err
	}
	numbers, err := selectableIssues(out)
	if err != nil {
		return 0, err
	}
	return pickLowest(numbers, cfg.skip), nil
}

// selectableIssues reads a `gh issue list --json number,labels` payload and
// keeps the issues still worth working: anything a previous drain parked is
// dropped here, which is what stops the queue handing back the same
// unimplementable issue on every pass. Labels are matched case-insensitively,
// the way GitHub itself treats them.
func selectableIssues(raw []byte) ([]int, error) {
	var issues []ghIssue
	if err := json.Unmarshal(raw, &issues); err != nil {
		return nil, fmt.Errorf("parsing issue list: %w", err)
	}
	numbers := make([]int, 0, len(issues))
	for _, is := range issues {
		if !is.hasLabel(needsHumanLabel) {
			numbers = append(numbers, is.Number)
		}
	}
	return numbers, nil
}

// ghIssue is one row of `gh issue list --json number,labels`.
type ghIssue struct {
	Number int       `json:"number"`
	Labels []ghLabel `json:"labels"`
}

type ghLabel struct {
	Name string `json:"name"`
}

// hasLabel matches case-insensitively, the way GitHub treats label names.
func (i ghIssue) hasLabel(name string) bool {
	return slices.ContainsFunc(i.Labels, func(l ghLabel) bool {
		return strings.EqualFold(l.Name, name)
	})
}

func commentCount(ctx context.Context, cfg config, issue int) (int, error) {
	out, err := gh(ctx, cfg, "issue", "view", strconv.Itoa(issue), "--json", "comments")
	if err != nil {
		return 0, err
	}
	var v struct {
		Comments []json.RawMessage `json:"comments"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return 0, fmt.Errorf("parsing comments: %w", err)
	}
	return len(v.Comments), nil
}

// pickPR chooses the PR that decides a branch's fate: an open one if any,
// else a merged one, else whatever GitHub listed first.
func pickPR(prs []pullRequest) *pullRequest {
	if len(prs) == 0 {
		return nil
	}
	for _, want := range []string{"OPEN", "MERGED"} {
		if i := slices.IndexFunc(prs, func(p pullRequest) bool { return p.State == want }); i >= 0 {
			return &prs[i]
		}
	}
	return &prs[0]
}

func prForBranch(ctx context.Context, cfg config, branch string) (*pullRequest, error) {
	out, err := gh(ctx, cfg, "pr", "list", "--head", branch, "--state", "all",
		"--json", "number,state,url")
	if err != nil {
		return nil, err
	}
	var prs []pullRequest
	if err := json.Unmarshal(out, &prs); err != nil {
		return nil, fmt.Errorf("parsing PR list: %w", err)
	}
	return pickPR(prs), nil
}

// supervisePR waits on an open PR until it leaves the OPEN state, dispatching
// a conflict-remediation run whenever GitHub reports the branch CONFLICTING.
// Status is checked immediately on entry, then once per poll interval.
func supervisePR(ctx context.Context, cfg config, issue, prNumber int, tally *issueTally) (string, error) {
	failures := 0
	for {
		state, mergeable, err := prStatus(ctx, cfg, prNumber)
		switch {
		case err != nil:
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			log.Printf("transient: checking PR #%d failed (%v) — will retry", prNumber, err)
		case state != "OPEN":
			return state, nil
		case mergeable == "CONFLICTING":
			log.Printf("PR #%d has merge conflicts — dispatching remediation", prNumber)
			if rerr := remediateConflicts(ctx, cfg, issue, prNumber, tally); rerr != nil {
				if ctx.Err() != nil {
					return "", ctx.Err()
				}
				if errors.Is(rerr, errAuth) {
					return "", authAdvice(rerr)
				}
				failures++
				log.Printf("remediation attempt %d/%d failed (%v)", failures, cfg.retries, rerr)
				if failures >= cfg.retries {
					return "", park("conflict remediation for PR #%d failed %d times", prNumber, failures)
				}
			} else {
				failures = 0
				log.Printf("remediation pushed — GitHub will recompute mergeability")
			}
		default:
			log.Printf("PR #%d still open (mergeable: %s) — next check in %s",
				prNumber, mergeable, cfg.poll)
		}
		if serr := sleep(ctx, cfg.poll); serr != nil {
			return "", serr
		}
	}
}

// remediateConflicts dispatches a self-contained Claude run that rebases the
// PR branch onto the current default branch and force-pushes the result.
func remediateConflicts(ctx context.Context, cfg config, issue, prNumber int, tally *issueTally) error {
	branch := fmt.Sprintf("%s%d", cfg.branchPrefix, issue)
	prompt := fmt.Sprintf(
		"PR #%d (branch %s) has merge conflicts with the remote default branch. "+
			"Locate the worktree for that branch via `git worktree list`; if none exists, "+
			"create one as a sibling folder from origin/%s. Working in that worktree: fetch, "+
			"then rebase the branch onto the remote default branch. Resolve every conflict "+
			"faithfully to the intent of BOTH sides — read the conflicting commits on the "+
			"default branch to understand what they changed and why, and preserve their "+
			"behavior alongside this branch's. Then run the test suite, typecheck, and lint, "+
			"fix anything the rebase broke, and push with --force-with-lease. "+
			"Do not open a new PR, do not merge anything, and do not commit to the default branch.",
		prNumber, branch, branch)
	started := time.Now()
	rep, err := execClaude(ctx, cfg, prompt, "", "")
	// A remediation run pushes to a PR that already exists, so it leaves
	// behind neither a new PR nor questions.
	tally.add(cfg.rec.recordRun(cfg, runContext{
		issue: issue, pr: prNumber, reason: reasonRemediate, outcome: outcomeNothing,
		started: started, ended: time.Now(),
	}, rep))
	return err
}

func prStatus(ctx context.Context, cfg config, prNumber int) (state, mergeable string, err error) {
	out, err := gh(ctx, cfg, "pr", "view", strconv.Itoa(prNumber), "--json", "state,mergeable")
	if err != nil {
		return "", "", err
	}
	var v struct {
		State     string `json:"state"`
		Mergeable string `json:"mergeable"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return "", "", fmt.Errorf("parsing PR status: %w", err)
	}
	return v.State, v.Mergeable, nil
}

// lookupPRFacts asks GitHub what a PR turned out to be: how large the change
// was, how much review it drew, and when it opened and merged. Those are the
// numbers no event stream can know, and the two timestamps are authoritative
// where a record's own are inferred.
//
// It exists for the issue record alone, so a drain with -metrics off makes no
// call at all, and a lookup that fails records the outcome without it — losing
// the record over an enrichment would drop exactly the terminal rows the
// dataset is for.
func lookupPRFacts(ctx context.Context, cfg config, prNumber int) prFacts {
	if prNumber == 0 || !cfg.rec.enabled() || ctx.Err() != nil {
		return prFacts{}
	}
	out, err := gh(ctx, cfg, "pr", "view", strconv.Itoa(prNumber),
		"--json", "additions,deletions,changedFiles,createdAt,mergedAt,reviews")
	var facts prFacts
	if err == nil {
		facts, err = parsePRFacts(out)
	}
	if err != nil {
		log.Printf("run data: GitHub could not say what PR #%d changed (%v) — recording the outcome without it",
			prNumber, err)
		return prFacts{}
	}
	return facts
}

// parsePRFacts keeps the numbers and drops the rest: the reviews gh returns
// carry their authors and their bodies, and a record counts them rather than
// quoting them.
func parsePRFacts(raw []byte) (prFacts, error) {
	var v struct {
		Additions    int               `json:"additions"`
		Deletions    int               `json:"deletions"`
		ChangedFiles int               `json:"changedFiles"`
		CreatedAt    string            `json:"createdAt"`
		MergedAt     string            `json:"mergedAt"`
		Reviews      []json.RawMessage `json:"reviews"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return prFacts{}, fmt.Errorf("parsing PR facts: %w", err)
	}
	return prFacts{
		Additions:    v.Additions,
		Deletions:    v.Deletions,
		ChangedFiles: v.ChangedFiles,
		Reviews:      len(v.Reviews),
		Opened:       v.CreatedAt,
		Merged:       v.MergedAt,
	}, nil
}

// postSummary puts one line of numbers on a merged PR, if -post-summary asked
// for it. This is the only thing in the program that shows run data to anybody
// but the operator, which is why it is off by default, carries numbers only,
// and goes no further than the PR it describes — to exactly the people who
// could already see that PR.
//
// Best-effort like the rest of run data: a comment that could not be posted is
// a log line, never a failed drain.
func postSummary(ctx context.Context, cfg config, prNumber int, tally issueTally) {
	if !cfg.postSummary || prNumber == 0 {
		return
	}
	if tally.runs == 0 {
		// This drain only waited on a PR an earlier one opened, so it has
		// nothing to report — and "0 runs, $0.00" would read as a free PR.
		log.Printf("-post-summary: no runs for PR #%d in this drain — leaving it uncommented", prNumber)
		return
	}
	if _, err := gh(ctx, cfg, "pr", "comment", strconv.Itoa(prNumber), "--body", summaryComment(tally)); err != nil {
		log.Printf("could not comment the run summary on PR #%d (%v) — the drain continues", prNumber, err)
		return
	}
	log.Printf("commented the run summary on PR #%d", prNumber)
}

func waitForComments(ctx context.Context, cfg config, issue, baseline int) error {
	for {
		if err := sleep(ctx, cfg.poll); err != nil {
			return err
		}
		n, err := commentCount(ctx, cfg, issue)
		if err != nil {
			log.Printf("transient: checking #%d comments failed (%v) — will retry", issue, err)
			continue
		}
		if n > baseline {
			return nil
		}
		log.Printf("issue #%d still awaiting a reply — next check in %s", issue, cfg.poll)
	}
}

func ensureIssueClosed(ctx context.Context, cfg config, issue, prNumber int) error {
	out, err := gh(ctx, cfg, "issue", "view", strconv.Itoa(issue), "--json", "state")
	if err != nil {
		return err
	}
	var v struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return fmt.Errorf("parsing issue state: %w", err)
	}
	if v.State == "OPEN" { // "Closes #N" normally handles this; belt and braces
		_, err = gh(ctx, cfg, "issue", "close", strconv.Itoa(issue),
			"--comment", fmt.Sprintf("Shipped in #%d.", prNumber))
		return err
	}
	return nil
}

// cleanupWorktree removes the sibling worktree the skill creates. Best-effort:
// a desktop-app session may have used its own worktree path instead.
func cleanupWorktree(ctx context.Context, cfg config, issue int) {
	repo := filepath.Base(cfg.dir)
	path := filepath.Join(filepath.Dir(cfg.dir), fmt.Sprintf("%s-issue-%d", repo, issue))
	if _, err := git(ctx, cfg, "worktree", "remove", path, "--force"); err == nil {
		log.Printf("removed worktree %s", path)
	}
	_, _ = git(ctx, cfg, "worktree", "prune")
}

// --- plumbing ---

func gh(ctx context.Context, cfg config, args ...string) ([]byte, error) {
	return capture(ctx, cfg.dir, cfg.ghBin, args...)
}

func git(ctx context.Context, cfg config, args ...string) ([]byte, error) {
	return capture(ctx, cfg.dir, "git", args...)
}

func capture(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	var errBuf strings.Builder
	cmd.Stderr = &errBuf
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err,
			strings.TrimSpace(errBuf.String()))
	}
	return out, nil
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
