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
	if cfg.logDir != "" {
		path, err := sinks.openShiftLog(cfg.logDir, cfg.repo, cfg.shiftID)
		if err != nil {
			narrate(sevWarning, logLostFmt, err)
		} else {
			cfg.logPath = path
		}
	}
	// A dry run may still look past either gate below: it runs nothing and
	// writes nothing, and seeing what a real run would refuse is how an
	// operator decides what to change. refuseOrNote is that one carve-out,
	// shared so a real refusal and its dry-run preview can never say it two
	// different ways.
	if err := refuseOrNote(queueGate(repoView.Visibility, cfg.label, cfg.ungated), cfg.dryRun); err != nil {
		return err
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
	warnClaudeModelEnv()
	if err := refuseOrNote(effortFlagGate(ctx, *cfg), cfg.dryRun); err != nil {
		return err
	}
	if snap, ok := probeUsage(ctx, *cfg); ok {
		cfg.usage = &snap
	}
	log.Printf("%s — running /%s per issue, polling every %s", cfg.repo, cfg.skill, cfg.poll)
	skewErr := versionSkewGate(polakoVersion(), *cfg)
	if err := refuseOrNote(skewErr, cfg.dryRun); err != nil {
		return err
	}
	if skewErr == nil {
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
		// still worth a line, same as before this gate existed. Gated on
		// skewErr rather than folded into the block above: refuseOrNote
		// already turned a dry-run refusal into a logged note and a nil
		// return, and that note must not also get this second, differently
		// worded line about the same skew.
		warnOnVersionSkew(polakoVersion(), *cfg)
	}
	settingsBlock(preflightPairs(*cfg))
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
func preflightPairs(cfg config) [][2]string {
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
	if line := modelEffortLine(cfg); line != "" {
		// One row for both dispatch knobs. The environment can set either
		// (POLAKO_MODEL, POLAKO_EFFORT), so an operator who forgot the export
		// should not have to work out why every run is on a model they did not
		// type — the same reason -post-summary earns a row.
		pairs = append(pairs, [2]string{"model", line})
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
	if cfg.logPath != "" {
		// The disclosure, said every time like the recorder's line: unlike the
		// run-data records this file holds transcript text, so where it lives
		// and that it stays local is worth a line per shift.
		pairs = append(pairs, [2]string{"shift log",
			fmt.Sprintf("%s — the whole claude transcript stream, kept on this machine (-log off to disable)", cfg.logPath)})
	}
	return pairs
}

// modelEffortLine renders the settings-block value for -model and -effort:
// whichever were set, "" when neither was so the row is skipped. "inherit" is
// not spelled — an omitted flag adds nothing to disclose.
func modelEffortLine(cfg config) string {
	var parts []string
	if cfg.model != "" {
		parts = append(parts, "model "+cfg.model)
	}
	if cfg.effort != "" {
		parts = append(parts, "effort "+cfg.effort)
	}
	return strings.Join(parts, ", ")
}

// settingsBlock narrates the startup recap as one aligned block: every row at
// settings severity — dim on a TTY, plain piped — through narrate, so each
// still gets its own timestamp and its own copy in the shift log exactly like
// any other narration. printPairs (statsrender.go) cannot be reused directly here:
// it writes straight to an io.Writer, bypassing emit() entirely, which would
// lose both of those. pairWidth is what the two share, so the startup block
// lines up the same way status and stats already do.
func settingsBlock(pairs [][2]string) {
	width := pairWidth(pairs)
	for _, p := range pairs {
		narrate(sevSettings, "  %-*s  %s", width, p[0], p[1])
	}
}
