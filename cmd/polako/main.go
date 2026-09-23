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
	"errors"
	"fmt"
	"os"
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
	if dispatchVerb() {
		return
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
			sinks.logf("interrupted — state is on GitHub; rerun to resume")
			os.Exit(130)
		}
		sinks.fatal("stopping: %v", err)
	}
}

// dispatchVerb handles every verb but `work`: parses none of work's own
// flags, since each of these owns its own flag.FlagSet. Returns true once
// the verb is fully handled, telling main to stop; false only for `work`
// (after dropping the verb word so the flag package parses what follows
// it), which main then carries on to run itself — the one verb that isn't
// a self-contained report.
//
// Split out of main so the switch itself doesn't count against main's own
// budget (sizebudget_test.go's funcBudget) — main was already at it before
// this verb, `update`, existed.
func dispatchVerb() bool {
	runReport := func(name string, run func() error) {
		// Narration (transient retries, gh warnings, the proposed-issues
		// notice) goes through the same sinks and rendering rules work's
		// does — colour on a capable stderr TTY, plain otherwise. Stamps stay
		// off unconditionally: unlike work, status and stats never open a
		// shift log, so there's no stamped copy elsewhere to justify a
		// terminal that drops them, and turning stamps on here would put a
		// timestamp on piped output that never had one.
		sinks.stamp = stampOff
		if isTerminal(os.Stderr) {
			sinks.style = styleFor(true)
		}
		if err := run(); err != nil {
			if errors.Is(err, errFlagsReported) {
				os.Exit(2) // the usage is already on screen
			}
			sinks.fatal("%s: %v", name, err)
		}
	}
	switch os.Args[1] {
	case "work":
		// Drop the verb so the flag package parses what follows it.
		os.Args = append(os.Args[:1], os.Args[2:]...)
		return false
	case "plan":
		runReport("plan", withShutdownContext(func(ctx context.Context) error {
			return runPlan(ctx, os.Args[2:], os.Stdout)
		}))
	case "health":
		runReport("health", withShutdownContext(func(ctx context.Context) error {
			return runHealth(ctx, os.Args[2:], os.Stdout)
		}))
	case "stats":
		rpt := newReport(isTerminal(os.Stdout))
		runReport("stats", func() error { return runStats(os.Args[2:], os.Stdout, os.Stderr, time.Now(), rpt) })
	case "status":
		rpt := newReport(isTerminal(os.Stdout))
		runReport("status", withShutdownContext(func(ctx context.Context) error {
			return runStatus(ctx, os.Args[2:], os.Stdout, time.Now(), rpt)
		}))
	case "tidy":
		rpt := newReport(isTerminal(os.Stdout))
		runReport("tidy", withShutdownContext(func(ctx context.Context) error {
			return runTidy(ctx, os.Args[2:], os.Stdout, rpt)
		}))
	case "unpark":
		rpt := newReport(isTerminal(os.Stdout))
		runReport("unpark", withShutdownContext(func(ctx context.Context) error {
			return runUnpark(ctx, os.Args[2:], os.Stdin, isTerminal(os.Stdin), os.Stdout, rpt)
		}))
	case "update":
		runReport("update", withShutdownContext(func(ctx context.Context) error {
			return runUpdate(ctx, config{}, os.Args[2:], os.Stdout)
		}))
	case "setup":
		rpt := newReport(isTerminal(os.Stdout))
		runReport("setup", withShutdownContext(func(ctx context.Context) error {
			return runSetup(ctx, os.Args[2:], os.Stdin, isTerminal(os.Stdin), os.Stdout, rpt)
		}))
	case "version", "-version", "--version":
		// Reachable without a verb, because it is what an operator asks
		// exactly when they are unsure what they are running.
		fmt.Println(describeVersion())
	case "help", "-h", "-help", "--help":
		verbUsage(os.Stdout)
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
	return true
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

// withShutdownContext wraps fn in the context every gh/git-calling verb in
// dispatchVerb needs: each makes a handful of those calls, some mutating, and
// Ctrl+C partway through should end them rather than be ignored. The context
// is cancelled by the same signals work honours, and stop() runs once fn
// returns.
func withShutdownContext(fn func(ctx context.Context) error) func() error {
	return func() error {
		ctx, stop := signal.NotifyContext(context.Background(), shutdownSignals()...)
		defer stop()
		return fn(ctx)
	}
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

// settingsBlock narrates the startup recap as one aligned block: every row at
// settings severity — dim on a TTY, plain piped — through narrate, so each
// still gets its own timestamp and its own copy in the shift log exactly like
// any other narration. printPairs (statsrender.go) cannot be reused directly here:
// it writes straight to an io.Writer, bypassing emit() entirely, which would
// lose both of those. pairWidth is what the two share, so the startup block
// lines up the same way status and stats already do.
func settingsBlock(cfg config, pairs [][2]string) {
	width := pairWidth(pairs)
	for _, p := range pairs {
		cfg.narrate(sevSettings, "  %-*s  %s", width, p[0], p[1])
	}
}
