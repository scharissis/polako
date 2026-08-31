// Command polako drives Claude Code through a repository's GitHub
// issues, in ascending order, one at a time.
//
// For each open issue it runs `claude -p "/implement-issue N"`, then waits on
// GitHub until the resulting PR is merged before advancing — so every run
// branches from a default branch that already contains the previous merge, and
// sequential runs can't conflict with each other. An issue that cannot be
// finished is parked for a human instead of merged, and an issue whose run
// stopped to ask something is put down until somebody replies; both advance the
// queue without ever putting two issues in flight. -strict-order turns the
// second one off, at the price of one blocked issue holding up every issue
// behind it.
//
// All state lives in GitHub (issues, comments, PRs, branches). This process
// is stateless and restart-safe: kill it any time, rerun it later, and it
// re-derives where things stand. The only human touchpoints are answering
// clarification comments and merging PRs — both on GitHub.
//
// What it writes locally is two write-only artifacts under ~/.polako, neither
// of which the drain ever reads back: run data — a line of numbers per run,
// see metrics.go — and the per-shift log, the full claude event stream, see
// ui.go.
//
// Nothing here is tied to one repository or language: point -dir at any GitHub
// checkout, and use -tools/-add-tools to match that project's ecosystem.
//
// Dependencies: the `claude`, `gh` and `git` CLIs on PATH, authenticated.
// Stdlib-only Go, so it cross-compiles to a single binary for any platform.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	// Embedded zone data, because limitReset resolves zone names like
	// "Europe/London" out of the CLI's limit refusals, and Windows keeps no
	// zoneinfo on disk for LoadLocation to find. Half a megabyte so the wait
	// is computed the same on all five targets instead of degrading to the
	// poll fallback on one of them.
	_ "time/tzdata"
)

// skillDir is the per-issue skill this repo ships under skills/.
const skillDir = "implement-issue"

// defaultSkill is how that skill is invoked once installed as a plugin: Claude
// namespaces plugin skills as <plugin>:<skill>. A skill hand-copied into
// ~/.claude/skills is invoked bare instead, so that install path needs
// -skill implement-issue. Point -skill anywhere else to drive a different
// workflow with the same supervisor.
const defaultSkill = "polako:" + skillDir

// pluginName is the half of defaultSkill that names the plugin: the id
// `claude plugin list` reports an installed copy under, and the prefix of the
// release tag the plugin tooling creates.
var pluginName, _, _ = strings.Cut(defaultSkill, ":")

// version is stamped at build time with -ldflags "-X main.version=$tag", which
// is how a prebuilt release binary knows which release it is. A `go install`
// learns the same thing from the module version, and a build from a clone falls
// back to the revision — see polakoVersion in metrics.go.
var version string

// needsHumanLabel takes a parked issue out of the queue. It is deliberately the
// only durable trace of a park: the next drain re-derives its queue from
// GitHub, and without the label it would pick the same unimplementable issue
// straight back up. Removing the label is how an operator puts it back in.
const needsHumanLabel = "needs-human"

// proposedLabel is the curation gate: an issue a machine proposed and no human
// has approved yet. The intake-side twin of needsHumanLabel — the queue is
// derived by excluding both — and the reason the exclusion ships before
// anything that applies the label exists: at no commit may a shift work an
// issue nobody chose. Only a human takes it off, which is the whole of what
// approving a proposal means.
const proposedLabel = "proposed"

// awaitingAnswerLabel says a run stopped to ask something. It is the only
// evidence the supervisor has that "no PR" means "blocked on a human" rather
// than "the run produced nothing at all": the count of comments on the thread
// cannot tell the skill's question apart from CI, a bot, a linked-PR notice or
// a passer-by, and reading any of those as a question left the drain waiting
// on a reply nobody knew was expected. The skill raises it, the skill lowers
// it, and it is also the only sign on GitHub that an issue is waiting on you.
const awaitingAnswerLabel = "awaiting-answer"

func main() {
	// Verbs, dispatched before any flag is parsed. `work` is the only one
	// that starts runs; `stats` reads the run data and never touches GitHub,
	// `status` reads GitHub and never touches the run data. A bare invocation
	// prints the verb table rather than defaulting to the most consequential
	// verb: starting an unattended agent loop should take a word that says so.
	if len(os.Args) < 2 {
		verbUsage(os.Stdout)
		return
	}
	runReport := func(name string, run func() error) {
		// Narration (transient retries, gh warnings, the proposed-issues
		// notice) goes through the same sinks and rendering rules work's
		// does, rather than the bare stdlib logger this used to leave it
		// on — colour on a capable stderr TTY, plain otherwise. Stamps stay
		// off unconditionally: unlike work, status and stats never open a
		// shift log, so there's no stamped copy elsewhere to justify a
		// terminal that drops them, and turning stamps on here would put a
		// timestamp on piped output that never had one.
		log.SetFlags(0) // a report, not a log
		log.SetOutput(milestoneWriter{u: sinks})
		sinks.stamp = stampOff
		if isTerminal(os.Stderr) {
			sinks.style = styleFor(true)
		}
		if err := run(); err != nil {
			if errors.Is(err, errFlagsReported) {
				os.Exit(2) // the usage is already on screen
			}
			log.Fatalf("%s: %v", name, err)
		}
	}
	switch os.Args[1] {
	case "work":
		// Drop the verb so the flag package parses what follows it.
		os.Args = append(os.Args[:1], os.Args[2:]...)
	case "plan":
		// Its own context, cancelled by the same signals work honours: the
		// preflight probes make a handful of gh calls, and Ctrl+C partway
		// through should end them rather than be ignored.
		ctx, stop := signal.NotifyContext(context.Background(), shutdownSignals()...)
		defer stop()
		runReport("plan", func() error { return runPlan(ctx, os.Args[2:], os.Stdout) })
		return
	case "health":
		// Its own context for the same reason plan's is: preflight makes a
		// handful of gh calls, and Ctrl+C partway through should end them
		// rather than be ignored.
		ctx, stop := signal.NotifyContext(context.Background(), shutdownSignals()...)
		defer stop()
		runReport("health", func() error { return runHealth(ctx, os.Args[2:], os.Stdout) })
		return
	case "stats":
		rpt := newReport(isTerminal(os.Stdout))
		runReport("stats", func() error { return runStats(os.Args[2:], os.Stdout, os.Stderr, time.Now(), rpt) })
		return
	case "status":
		// Its own context, cancelled by the same signals work honours: a
		// snapshot makes a handful of gh calls, and Ctrl+C partway through
		// should end them rather than be ignored.
		ctx, stop := signal.NotifyContext(context.Background(), shutdownSignals()...)
		defer stop()
		rpt := newReport(isTerminal(os.Stdout))
		runReport("status", func() error { return runStatus(ctx, os.Args[2:], os.Stdout, time.Now(), rpt) })
		return
	case "tidy":
		// Its own context for the same reason status gets one: this makes gh
		// and git calls, and some of them mutate, so Ctrl+C partway through
		// should end them rather than be ignored.
		ctx, stop := signal.NotifyContext(context.Background(), shutdownSignals()...)
		defer stop()
		rpt := newReport(isTerminal(os.Stdout))
		runReport("tidy", func() error { return runTidy(ctx, os.Args[2:], os.Stdout, rpt) })
		return
	case "version", "-version", "--version":
		// Reachable without a verb, because it is what an operator asks
		// exactly when they are unsure what they are running.
		fmt.Println(describeVersion())
		return
	case "help", "-h", "-help", "--help":
		verbUsage(os.Stdout)
		return
	default:
		// Old muscle memory lands here: the bare invocation used to work the
		// backlog, so flags arriving without a verb get pointed at the verb
		// they almost certainly meant.
		if strings.HasPrefix(os.Args[1], "-") {
			fmt.Fprintf(os.Stderr, "polako needs a verb before any flags — did you mean `polako work %s`?\n\n",
				strings.Join(os.Args[1:], " "))
		} else {
			fmt.Fprintf(os.Stderr, "unknown verb %q\n\n", os.Args[1])
		}
		verbUsage(os.Stderr)
		os.Exit(2)
	}

	cfg := parseFlags()

	// A shutdown signal cancels the context: in-flight waits end promptly, and a
	// running claude process is killed through CommandContext.
	ctx, stop := signal.NotifyContext(context.Background(), shutdownSignals()...)
	defer stop()

	// Timestamps are the sinks' job, and every line carries one: the shift log
	// is always stamped in full, a terminal that is not a TTY keeps the same
	// full stamp the default flags used to add so redirected transcripts look
	// like they always did, and a TTY gets a dim, time-only stamp instead of
	// dropping it — worn quietly rather than shown or hidden outright, and
	// deliberately not conditioned on whether a shift log exists this run —
	// plus colour when the platform and NO_COLOR allow it.
	log.SetFlags(0)
	log.SetOutput(milestoneWriter{u: sinks})
	sinks.verbose = cfg.verbose
	if isTerminal(os.Stderr) {
		sinks.stamp = stampTTYDim
		sinks.style = styleFor(true)
	}
	if err := run(ctx, cfg); err != nil {
		if errors.Is(err, context.Canceled) {
			// 130 for every shutdown signal, not only SIGINT. Telling them apart
			// would mean hand-rolling NotifyContext to record which one arrived,
			// and what an operator does about it — rerun, everything is on
			// GitHub — is the same in all three cases.
			log.Println("interrupted — state is on GitHub; rerun to resume")
			os.Exit(130)
		}
		fatal("stopping: %v", err)
	}
}

// shutdownSignals are every way a host says "stop now". All of them have to
// cancel the context, because the claude child is killed through it and nothing
// else kills it: a supervisor that dies without cancelling leaves that child
// running with acceptEdits and the whole --allowedTools set, free to go on
// editing, commit, push the branch and open a PR nobody is supervising. The
// next drain then finds no PR yet, starts a second run on the same issue and the
// same worktree, and one issue in flight at a time is no longer true.
//
// SIGINT is the operator at the keyboard; SIGTERM is a service manager stopping
// the unit, a shutdown, or a plain `pkill`; SIGHUP is the terminal going away.
// Both POSIX names are declared in syscall on Windows as well, so naming them
// costs the cross-compile nothing — they are simply never delivered there.
func shutdownSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP}
}

func run(ctx context.Context, cfg config) error {
	if err := preflight(ctx, &cfg); err != nil {
		return err
	}
	if cfg.dryRun {
		return dryRun(ctx, cfg, os.Stdout)
	}
	return drain(ctx, cfg)
}

// preflight fails fast on a misconfigured environment, so an unattended run
// can't die on its first gh call an hour after being started.
func preflight(ctx context.Context, cfg *config) error {
	for _, bin := range []string{cfg.claudeBin, cfg.ghBin, "git"} {
		if _, err := exec.LookPath(bin); err != nil {
			return fmt.Errorf("%q not found on PATH: %w", bin, err)
		}
	}
	if err := checkNotifyCommand(cfg.notifyCmd); err != nil {
		return err
	}
	if _, err := git(ctx, *cfg, "rev-parse", "--git-dir"); err != nil {
		return fmt.Errorf("-dir %s is not a git checkout: %w", cfg.dir, err)
	}
	out, err := gh(ctx, *cfg, "repo", "view", "--json", "nameWithOwner,visibility")
	if err != nil {
		return fmt.Errorf("no GitHub repository reachable from %s (is gh authenticated?): %w", cfg.dir, err)
	}
	var repoView struct {
		NameWithOwner string `json:"nameWithOwner"`
		Visibility    string `json:"visibility"`
	}
	if err := json.Unmarshal(out, &repoView); err != nil {
		return fmt.Errorf("unreadable `gh repo view` reply (is gh current?): %w", err)
	}
	cfg.repo = repoView.NameWithOwner
	// As soon as the repository is known, because the file is named after it.
	// Everything logged from here on lands in the shift log too — including a
	// preflight refusal below, which is often the diagnosis an operator wants.
	logPath := ""
	if cfg.logDir != "" {
		path, err := sinks.openShiftLog(cfg.logDir, cfg.repo, cfg.shiftID)
		if err != nil {
			narrate(sevWarning, logLostFmt, err)
		} else {
			logPath = path
		}
	}
	if err := queueGate(repoView.Visibility, cfg.label, cfg.ungated); err != nil {
		// A dry run may still look: it runs nothing and writes nothing, and
		// seeing what an ungated queue would work is how an operator decides
		// what to label. It hears about the gate rather than hitting it.
		if !cfg.dryRun {
			return err
		}
		log.Printf("note: a real run would refuse to start here — %v", err)
	}
	if cfg.ungated && strings.EqualFold(repoView.Visibility, "PUBLIC") {
		// Said out loud like -remote and -post-summary are, and for the same
		// reason: the environment can set this too, and it is the one flag that
		// hands the queue to whoever can open an issue.
		log.Printf("-ungated on a public repository — every open issue is in the queue, whoever filed it")
	}
	// Defined up front rather than when a run first needs it: GitHub refuses to
	// apply a label the repository never declared, and the run that applies this
	// one is a headless session holding no grant that could create it. Discovering
	// that at the moment a question needs flagging is the expensive time to find
	// out — the question gets posted and then never waited on.
	//
	// Not on a dry run, which declares nothing: creating a label is a write, and
	// the promise is that it leaves the repository as it found it.
	if !cfg.dryRun {
		_ = ensureLabel(ctx, *cfg, awaitingAnswerLabel, "FBCA04",
			"polako is waiting for an answer on this issue")
	}
	cfg.claudeVersion = claudeVersion(ctx, *cfg)
	cfg.pluginVersion, cfg.pluginID = pluginVersion(ctx, *cfg)
	if snap, ok := probeUsage(ctx, *cfg); ok {
		cfg.usage = &snap
	}
	log.Printf("%s — running /%s per issue, polling every %s", cfg.repo, cfg.skill, cfg.poll)
	if err := versionSkewGate(polakoVersion(), *cfg); err != nil {
		if !cfg.dryRun {
			return err
		}
		log.Printf("note: a real run would refuse to start here — %v", err)
	} else {
		if self, plugin, behind, ok := skewComparison(polakoVersion(), *cfg); cfg.ignoreSkew && ok && behind {
			// Said out loud, the same shape -ungated gets on a public repo:
			// the gate itself already let this through (it reads
			// cfg.ignoreSkew, same as queueGate reads cfg.ungated), so this
			// is the operator's own line recording that an override actually
			// fired, not silent normal operation.
			log.Printf("-ignore-skew: the installed %s plugin (%s) is behind this binary (%s) — running anyway",
				pluginName, plugin, self)
		}
		// Only the "behind" case above ever refuses; a newer or ambiguous
		// mismatch — or a "behind" one -ignore-skew just let through — is
		// still worth a line, same as before this gate existed.
		warnOnVersionSkew(polakoVersion(), *cfg)
	}
	settingsBlock(preflightPairs(*cfg, logPath))
	return nil
}

// preflightPairs builds the startup recap as row pairs: what used to be a
// run of full sentences becomes one fact per row, aligned like status and
// stats. Every row but "queue" is gated by the exact condition its sentence
// used before this — reshaping what preflight already said, not changing
// what those rows disclose. "queue" (-label) is new disclosure, not a
// reshape; its own comment below says why. Row order inside the block
// matches the old sentence order, but warnOnVersionSkew (called just above,
// in preflight) now always prints ahead of the whole block rather than
// being interleaved with -dry-run the way the two sentences used to be — a
// real, if cosmetic, reordering worth knowing about before trusting this
// comment too literally.
func preflightPairs(cfg config, logPath string) [][2]string {
	var pairs [][2]string
	// Unconditional, unlike every row below it: a shift closes a finished
	// container (every child closed) on its own, writing to human-curated state
	// without being asked, which is the class of thing -post-summary and -remote
	// disclose at startup. Held with needs-human or proposed, it is left alone.
	pairs = append(pairs, [2]string{"epics",
		"a finished container (every sub-issue closed) is closed with a comment saying so — " +
			"put needs-human on one to hold it open"})
	if cfg.label != "" {
		// Not said at all before this: the issue asks for -label to surface
		// here. Unset (the default, unfiltered queue) says nothing, the same
		// way an off flag elsewhere earns no row — there is nothing to
		// disclose about it that -ungated's own warning does not already say.
		pairs = append(pairs, [2]string{"queue", fmt.Sprintf("label %q", cfg.label)})
	}
	if cfg.dryRun {
		pairs = append(pairs, [2]string{"dry-run",
			"resolving the next issue only — no claude run, no GitHub write, no run data"})
	}
	if notes := capNotes(cfg); notes != "" {
		pairs = append(pairs, [2]string{"caps", notes})
	}
	if cfg.usage != nil {
		if line := usageLine(*cfg.usage); line != "" {
			pairs = append(pairs, [2]string{"plan", line})
		}
	}
	if cfg.postSummary {
		// The environment can set this, so say it out loud: an operator who
		// forgot the variable is in their profile should not have to work out
		// where the PR comments are coming from.
		pairs = append(pairs, [2]string{"post-summary", "on — each merged PR gets one comment of run numbers"})
	}
	if cfg.notifyCmd != "" {
		pairs = append(pairs, [2]string{"notify", fmt.Sprintf(
			"on — `%s` runs when an issue parks, an issue asks a question, the backlog clears, or the shift stops early",
			cfg.notifyCmd)})
	}
	if cfg.remote {
		// Said every time, unprompted, like the recorder's line and for the same
		// reason — except that what it has to say is now the opposite. An
		// on-by-default flag that quietly does nothing is worse than one that
		// quietly does something, because the operator goes looking for sessions
		// that will never appear.
		pairs = append(pairs, [2]string{"remote",
			"on, but no claude CLI registers headless runs with Remote Control yet — runs stay on this machine " +
				"and unwatched, and nothing is sent anywhere (-remote=false silences this line; a later polako " +
				"lights the flag up once a CLI supports it)"})
	}
	if cfg.rec.enabled() {
		// Say where the data goes, every time, unprompted: it is the whole of
		// the answer to "what does this tool record".
		pairs = append(pairs, [2]string{"run data",
			fmt.Sprintf("%s — numbers only, never leaves this machine (-metrics off to disable)", cfg.rec.dir)})
		// The one place the id is ever shown. Nothing reads it back, so a
		// report on this drain alone is unaskable unless this line is where an
		// operator finds it — including while the drain is still running.
		pairs = append(pairs, [2]string{"shift",
			fmt.Sprintf("%s — `polako stats -shift %s` reports on it alone", cfg.shiftID, cfg.shiftID)})
	}
	if logPath != "" {
		// The disclosure, said every time like the recorder's line: unlike the
		// run-data records this file holds transcript text, so where it lives
		// and that it stays local is worth a line per shift.
		pairs = append(pairs, [2]string{"shift log",
			fmt.Sprintf("%s — the whole claude transcript stream, kept on this machine (-log off to disable)", logPath)})
	}
	return pairs
}

// settingsBlock narrates the startup recap as one aligned block: every row at
// settings severity — dim on a TTY, plain piped — through narrate, so each
// still gets its own timestamp and its own copy in the shift log exactly like
// any other narration. printPairs (stats.go) cannot be reused directly here:
// it writes straight to an io.Writer, bypassing emit() entirely, which would
// lose both of those. pairWidth is what the two share, so the startup block
// lines up the same way status and stats already do.
func settingsBlock(pairs [][2]string) {
	width := pairWidth(pairs)
	for _, p := range pairs {
		narrate(sevSettings, "  %-*s  %s", width, p[0], p[1])
	}
}

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
