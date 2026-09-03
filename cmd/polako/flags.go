package main

// Command-line flags and the config they fill. parseFlags is the whole of the
// interface for `polako work`; each config field is documented at its
// declaration, and any flag's default can come from the environment — see
// applyEnvDefaults.

import (
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

type config struct {
	dir       string
	claudeBin string
	// ghBin is a test seam, not an interface: the suite is hermetic and never
	// calls a real gh, but the drain loop is mostly GitHub bookkeeping and is
	// worth covering end to end. No flag sets it — parseFlags pins it to "gh".
	ghBin string
	// ghRepo names the repository on every gh call, for a read that has to work
	// from a directory which is not a checkout of it. Empty — the drain's own
	// setting, always — lets gh resolve the repository from cfg.dir, which is
	// what every call here did before `status` existed. See gh().
	ghRepo string
	// ghRetryWait is how long retryRead waits between attempts at a GitHub read
	// that failed. A seam like ghBin rather than a flag: what it is really
	// waiting for is a network coming back after a wake, which is not a
	// preference anyone has, and the suite sets it low so a proved retry costs
	// no wall clock. parseFlags pins it to ghRetryDelay.
	ghRetryWait time.Duration
	// resumeCeiling is the backstop below, and a seam for the same reason
	// ghRetryWait is: reaching it costs one process per resume, and the suite
	// proves the ceiling stops the loop rather than proving its size.
	// parseFlags pins it to defaultResumeCeiling.
	resumeCeiling int
	skill         string
	branchPrefix  string
	label         string
	// ignoreSkew is consent to what versionSkewGate otherwise refuses:
	// starting a drain whose installed skill is an older release than this
	// binary, which is the #239 shape — a shift on a skill missing recent
	// cost fixes. The same override shape as ungated, for the same reason:
	// an operator testing a tip binary against an installed release is doing
	// something deliberate.
	ignoreSkew bool
	// ungated is consent to what queueGate otherwise refuses: working a public
	// repository's backlog with no label between "anyone opened an issue" and
	// "an unattended agent implements it".
	ungated        bool
	tools          string
	addTools       string
	permissionMode string
	model          string
	// effort is claude --effort — how hard a run thinks, one of the CLI's
	// closed set (effortLevels) or empty. Empty omits the flag and lets the
	// CLI resolve effort the way it would for a terminal session.
	effort string
	poll   time.Duration
	retries        int
	retryWait      time.Duration
	stall          time.Duration
	// heartbeat is how long the terminal may stay quiet before a run says one
	// "still working" line, repeated every heartbeat of continued silence (0
	// disables). It watches the terminal, not the event stream: -stall samples
	// the stream and kills, this samples the terminal and speaks. For most of a
	// run the stream is busy and the terminal is quiet only because the sinks
	// filtered its events out. `polako plan` leaves it unset — the stage
	// recognizer the line names is an implement-issue thing.
	heartbeat time.Duration
	// The spend caps. maxCost and maxIssueTime bound one issue and park it
	// when it breaches; maxSessionCost bounds the whole drain and ends it
	// cleanly instead. They are what -stall is not: that watchdog catches
	// silence, and a run that loops productively but uselessly for hours
	// emits events the whole way.
	//
	// maxCost and maxSessionCost default to zero — off — unless an operator
	// asks for them. maxIssueTime does not: parseFlags pins it to
	// defaultMaxIssueTime (issue #256) — the #216 shift ($29.57, 90m30s on
	// one issue) is exactly what -stall cannot catch, and only a wall-clock
	// default needs no pricing, so it is the one cap on by default. `0` still
	// disables it.
	//
	// maxIssueTime counts the run time this drain spent on the issue, not the
	// wall clock since it was picked up: an issue spends most of its life
	// waiting for a person to merge its PR, and parking issues over how long
	// that took would punish nobody's slowness but the reviewer's.
	maxCost        float64
	maxIssueTime   time.Duration
	maxSessionCost float64
	// maxSessionUsage and maxWeekUsage are the plan's own limits rather than
	// this binary's arithmetic: percentages read off probeUsage's "session"
	// and "week (all models)" pools. Checked between issues exactly where
	// maxSessionCost is, for the same reason — ending a shift cleanly means
	// declining more work, not killing a run part-way — and like it, never a
	// park: nothing is wrong with the issue, so it stays in the queue for
	// whichever shift finds the pool reset. Both zero — off — unless an
	// operator sets one.
	maxSessionUsage int
	maxWeekUsage    int
	// maxIssues is `polako plan`'s and `polako health`'s ceiling on the issues
	// one run may create, epics included — shared by both the way maxCost
	// happens to be. Zero (the drain, always) disables the cap; dispatchClaude
	// counts `gh issue create` tool calls and kills the run the way the stall
	// watchdog does when the count reaches it. See errIssueCap and
	// runReport.issueCreates.
	maxIssues int
	skip      map[int]bool
	once      bool
	// strictOrder keeps the queue in strict ascending order, so an issue
	// waiting on a human answer holds up every issue behind it. Off by
	// default: the no-conflict guarantee comes from one issue being in flight
	// at a time, and an issue nobody is working is not in flight.
	strictOrder bool

	// dryRun resolves the next issue, says what it would do to it, and stops.
	// No claude process, no GitHub write, no run-data record — which is what
	// makes it safe to point at a repository nobody here has drained before.
	dryRun bool

	// Remote Control. A shift's runs are unattended by design and invisible
	// with it: while a run is in flight its output exists only in the terminal
	// that started it. remote asks for those runs to be watchable from
	// claude.ai/code or the mobile app instead.
	//
	// Asks, and today gets nothing — which is why no invocation carries
	// `--remote-control` any more. Claude Code accepts the flag under -p, emits
	// a normal init event, runs to completion and never starts the remote
	// bridge; print mode is the whole differentiator, and the init event carries
	// no field to detect the ignore from (issue #82). Sending a flag that does
	// nothing only kept the promise alive, so the flag is not sent and startup
	// says so. What remains is interface: the flag is still accepted and still
	// documented, so the day a CLI registers headless runs there is one place to
	// light it up again and the argument for it is already on issue #52.
	remote bool
	// queue is what a shift learns about listing its own backlog and only wants
	// to find out — and say — once: that this gh is too old to see sub-issues,
	// and that there are proposals it is leaving behind the curation gate. A
	// pointer because config is passed by value everywhere: the drain lists the
	// backlog once per issue, so a bool would re-probe an old gh and re-log both
	// lines every time round the loop. Nil-safe because a config assembled
	// outside parseFlags — a test, mostly — carries no shared memo, and losing
	// it costs only the repetition. Nothing durable — a fact about this gh and
	// this shift, not orchestration state.
	queue *queueMemo
	// notifyCmd is run whenever the drain reaches a state a person has to move
	// past. Empty, the default, means no hook at all. See notify.go.
	notifyCmd string

	// Run-data capture. tag labels a batch of runs so configurations can be
	// compared later; rec is the sink, and writes nothing when -metrics is off.
	// postSummary is the one opt-in that shows those numbers to anybody else:
	// a numbers-only comment on each merged PR.
	//
	// shiftID stamps every record this process writes, so `stats -shift` can
	// report on one drain rather than on whatever a time window happens to
	// catch. Generated per process and persisted nowhere but the records
	// themselves: nothing reads it back, so telemetry stays write-only.
	tag         string
	shiftID     string
	rec         *recorder
	postSummary bool

	// Where the per-shift log lives; empty means -log off. The file itself is
	// opened by preflight, which learns the repository it is named after.
	logDir  string
	verbose bool
	// logPath is that file's full path, filled in by preflight once it is
	// opened (empty when -log is off or opening it failed). A park needs it
	// to point an operator back at the one place the exact refused tool call
	// survives — the reason posted to the issue thread cannot carry it, since
	// the tool detail can hold a local absolute path (see permissionRefusedDetail).
	logPath string

	// Filled in by preflight, recorded with every run: which repository this
	// is, which CLI produced its numbers, and which release of the skill it
	// drove. pluginVersion is empty when -skill names a hand-installed skill,
	// which has no plugin to report a version.
	repo          string
	claudeVersion string
	pluginVersion string
	// The `<plugin>@<marketplace>` id of the copy pluginVersion read, set only
	// when one copy was unambiguously in the running. The skew warning prints it
	// in the `claude plugin update` command it recommends — that command needs
	// the full id, and the marketplace half is operator-chosen, so it cannot be
	// rebuilt from the plugin name.
	pluginID string
	// usage is the account's own plan, as of the one probe preflight (or
	// statusConfig) ran — nil when the probe never answered, never a zero
	// snapshot standing in for "could not tell". See usage.go.
	usage *usageSnapshot
	// usageTimeout bounds probeUsage's own exec, separate from execClaude's
	// stall watchdog — the probe never goes through execClaude. A seam like
	// ghRetryWait: parseFlags and statusConfig pin it to
	// defaultUsageProbeTimeout, and a test shrinks it to prove the timeout
	// path without waiting out the real one.
	usageTimeout time.Duration
}

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
// silently — the one failure mode worse than being too narrow. The checks and
// run lookups are what a CI remediation diagnoses a red build with; they read
// and nothing else, which is why `gh run:*` is not granted — that would carry
// `gh run rerun`, `gh run cancel` and `gh run delete`. Nothing else that writes
// is granted; that is what -add-tools is for.
//
// The one label command the skill does need is not here either: it is minted
// per run and pinned to that run's issue number — see issueLabelTools.
//
// Bash(evals/run.sh:*) is here for the one repository that has such a script —
// polako itself, drained by polako. A run that changes a shipped SKILL.md runs
// the eval cases its change touches (CLAUDE.md, "The suite is the
// verification"); the grant is a fixed prefix because the skill invokes the
// main checkout's copy of the script from that checkout's cwd, passing
// --plugin-dir to aim it at the worktree. On every other repo the path does
// not exist, so the entry grants nothing. On polako itself it runs the eval
// suite — nested `claude` sessions and real spend, the same class as the
// Bash(go:*)/Bash(python:*) entries below. The skill passes run.sh with
// --max-cost, but a prefix grant does not force the argument, so treat this
// as narrowing the audit trail, not capping the blast radius.
const defaultTools = "Bash(git:*)," +
	"Bash(gh issue view:*),Bash(gh issue comment:*)," +
	"Bash(gh pr create:*),Bash(gh pr view:*),Bash(gh pr list:*),Bash(gh pr diff:*)," +
	"Bash(gh pr checks:*),Bash(gh run list:*),Bash(gh run view:*)," +
	"Read,Write,Edit,Glob,Grep,TodoWrite,Skill," +
	"Bash(npm:*),Bash(npx:*),Bash(pnpm:*),Bash(yarn:*)," +
	"Bash(go:*),Bash(cargo:*),Bash(make:*)," +
	"Bash(python:*),Bash(python3:*),Bash(pytest:*),Bash(uv:*)," +
	"Bash(dotnet:*),Bash(mvn:*),Bash(gradle:*)," +
	"Bash(evals/run.sh:*)"

// defaultMaxIssueTime is -max-issue-time's default (issue #256). #239's two
// normal shifts ran 30m10s and 34m29s of run time; the #216 shift this ticket
// cites ran 90m30s and cost $29.57 on one issue with -stall powerless to
// catch it, since it kept emitting events the whole way. 45m sits above the
// normal range and at half of #216's length, so that shape parks with spend
// to spare rather than at the wire. `-max-issue-time 0` restores the old
// unbounded behaviour.
const defaultMaxIssueTime = 45 * time.Minute

// verbUsage is the bare invocation's answer: the one line of etymology that
// explains the name, then the verbs. It lists only verbs that exist, so the
// usage never advertises a verb that errors.
func verbUsage(w io.Writer) {
	fmt.Fprint(w,
		"polako — Croatian for \"take it slow\": works a GitHub issue backlog to zero,\n"+
			"one issue at a time, with a human at every gate.\n\n"+
			"Usage: polako <verb> [flags]\n\n"+
			"  work    work the backlog: run the skill per issue, wait for each merge, unattended\n"+
			"  plan    propose a backlog from a vision document, behind the `proposed` label, unattended\n"+
			"  health  propose a backlog from the repository's own shape, behind the `proposed` label, unattended\n"+
			"  status  print where the backlog stands, from GitHub (read-only)\n"+
			"  stats   report on the run data already recorded (local, read-only)\n"+
			"  tidy    reclaim the worktrees and branches of finished issues (dry-run by default)\n\n"+
			"Run 'polako <verb> -h' for that verb's flags.\n")
}

func parseFlags() config {
	var cfg config
	var skip, metrics, logSpec string
	var showVersion bool
	flag.BoolVar(&showVersion, "version", false, "print the version of this binary and exit")
	flag.StringVar(&cfg.dir, "dir", ".", "path to the repository's main checkout")
	flag.StringVar(&cfg.claudeBin, "claude", "claude", "claude binary to invoke")
	flag.StringVar(&cfg.skill, "skill", defaultSkill, "skill to run per issue")
	flag.StringVar(&cfg.branchPrefix, "branch-prefix", "issue-", "branch name prefix the skill uses")
	flag.StringVar(&cfg.label, "label", "", "only process issues carrying this label (empty = all)")
	flag.BoolVar(&cfg.ungated, "ungated", false,
		"work a public repository without a -label gate (anyone who can open an issue can feed the queue)")
	flag.BoolVar(&cfg.ignoreSkew, "ignore-skew", false,
		"start even when the installed skill is an older release than this binary (default: refuse — see docs/install.md)")
	flag.StringVar(&cfg.tools, "tools", defaultTools,
		"comma-separated --allowedTools for unattended runs")
	flag.StringVar(&cfg.addTools, "add-tools", "",
		"extra --allowedTools entries, appended to -tools instead of replacing it")
	flag.StringVar(&cfg.permissionMode, "permission-mode", "acceptEdits", "claude --permission-mode")
	flag.StringVar(&cfg.model, "model", "", "claude --model for every run (empty = whatever the CLI defaults to)")
	flag.StringVar(&cfg.effort, "effort", "",
		"claude --effort for every run — one of "+strings.Join(effortLevels, ", ")+" (empty = whatever the CLI defaults to)")
	flag.DurationVar(&cfg.poll, "poll", 5*time.Minute, "interval between GitHub checks while waiting")
	flag.IntVar(&cfg.retries, "retries", 3, "resume attempts after a crashed claude run (nonzero exit)")
	flag.DurationVar(&cfg.retryWait, "retry-wait", 30*time.Second, "wait before each resume attempt")
	flag.DurationVar(&cfg.stall, "stall", 15*time.Minute, "kill and resume a run with no output events for this long (0 disables)")
	flag.DurationVar(&cfg.heartbeat, "heartbeat", 5*time.Minute,
		"say a one-line note while a run is quiet on the terminal, repeated every interval of continued silence (0 disables)")
	flag.Float64Var(&cfg.maxCost, "max-cost", 0,
		"park an issue once this shift's runs on it have cost this many dollars (0 disables)")
	flag.DurationVar(&cfg.maxIssueTime, "max-issue-time", defaultMaxIssueTime,
		"park an issue once this shift's runs on it have taken this much run time (0 disables)")
	flag.Float64Var(&cfg.maxSessionCost, "max-session-cost", 0,
		"end the shift between issues once its runs have cost this many dollars (0 disables)")
	flag.IntVar(&cfg.maxSessionUsage, "max-session-usage", 0,
		"pause the shift between issues while the plan's current-session usage is at or over this percent, waiting the pool's own reset out then carrying on (0 disables)")
	flag.IntVar(&cfg.maxWeekUsage, "max-week-usage", 0,
		"pause the shift between issues while the plan's current-week usage is at or over this percent, waited out the same way — a weekly reset can be days off (0 disables)")
	flag.StringVar(&skip, "skip", "", "comma-separated issue numbers to skip (head-of-line escape hatch)")
	flag.BoolVar(&cfg.once, "once", false,
		"process a single issue to a merge, a park or a question, then exit")
	flag.BoolVar(&cfg.strictOrder, "strict-order", false,
		"work issues in strict ascending order: wait on one that asked a question instead of moving past it")
	flag.BoolVar(&cfg.dryRun, "dry-run", false,
		"resolve the next issue, print the claude invocation it would get, and exit without running or writing anything")
	flag.StringVar(&cfg.notifyCmd, "notify", "",
		"command to run when polako needs a human, with context in "+notifyPrefix+"* (see docs/reference.md)")
	flag.BoolVar(&cfg.remote, "remote", true,
		"ask for each run to be watchable from claude.ai/code or the phone (no CLI registers headless runs yet)")
	flag.StringVar(&cfg.tag, "run-tag", "", "label recorded with every run, for comparing one batch against another")
	flag.BoolVar(&cfg.postSummary, "post-summary", false,
		"comment one line of run numbers on each merged PR (runs, tokens, dollars, wall time)")
	flag.StringVar(&metrics, "metrics", "",
		`directory for run-data records, or "off" (default ~/.polako/metrics)`)
	flag.StringVar(&logSpec, "log", "",
		`directory for the full per-shift log, or "off" (default ~/.polako/logs)`)
	flag.BoolVar(&cfg.verbose, "verbose", false,
		"mirror the full claude event stream and its stderr to the terminal, not only the shift log")
	flag.Usage = func() {
		fmt.Fprint(flag.CommandLine.Output(),
			"Usage: polako work [flags]\n\n"+
				"Work the backlog to zero: run the skill on the lowest open issue, wait\n"+
				"for its PR to merge — or park the issue for a human — then advance.\n"+
				"Unattended; nothing here merges. `polako -h` lists the other verbs.\n\n"+
				envUsage+"\nFlags:\n")
		flag.PrintDefaults()
	}
	// Before Parse, so an argument on the command line always wins over a
	// preference set in the environment.
	if err := applyEnvDefaults(flag.CommandLine); err != nil {
		log.Fatalf("%v", err)
	}
	flag.Parse()

	// Rejected here, before the process commits to anything: an -effort the CLI
	// cannot take would otherwise surface as a usage error an hour in, looking
	// like a crash. Same exit shape as an unparseable env default above.
	if err := validateEffort(cfg.effort); err != nil {
		log.Fatalf("%v", err)
	}

	// Answered before anything else a flag implies, so it stays usable on a
	// machine where -dir points nowhere: this is the flag an operator reaches
	// for when the startup warning says the two halves disagree.
	if showVersion {
		fmt.Println(describeVersion())
		os.Exit(0)
	}

	// A dry run writes nothing, run data and shift log included. Both are
	// preferences an operator may well have set in their environment and
	// forgotten, and a record of a run that never happened is worse than no
	// record at all.
	if cfg.dryRun {
		metrics = metricsOff
		logSpec = metricsOff
	}
	cfg.rec = newRecorder(metrics)
	cfg.logDir = resolveLogDir(logSpec)
	cfg.shiftID = newShiftID()
	cfg.queue = new(queueMemo)
	cfg.ghBin = "gh"
	cfg.ghRetryWait = ghRetryDelay
	cfg.resumeCeiling = defaultResumeCeiling
	cfg.usageTimeout = defaultUsageProbeTimeout
	cfg.skip = parseSkip(skip)
	abs, err := filepath.Abs(cfg.dir)
	if err != nil {
		log.Fatalf("resolving -dir: %v", err)
	}
	cfg.dir = abs
	return cfg
}

// envPrefix namespaces the variables that set flag defaults: -post-summary
// reads POLAKO_POST_SUMMARY.
const envPrefix = "POLAKO_"

// envUsage is the one line of help that makes the mechanism discoverable. A
// default nobody knows how to set is a default nobody sets.
const envUsage = "Any flag below can take its default from the environment:\n" +
	"POLAKO_<FLAG>, e.g. POLAKO_POST_SUMMARY=1. Arguments win.\n"

// envExempt are the flags the environment may not set. These are actions
// rather than preferences, and each one left in a profile turns every later
// run into something other than what its operator typed that day.
// POLAKO_VERSION is exactly the variable a Dockerfile or CI job pins an
// install with; POLAKO_DRY_RUN is the one an operator exports to preview an
// unfamiliar repository once and then forgets, after which the nightly drain
// reports success on a backlog nobody touched. POLAKO_APPLY is that risk
// mirrored onto `tidy`: a forgotten export would turn every future dry-run
// preview into a live worktree-and-branch deletion run, which is exactly the
// case -dry-run defaulting on is meant to prevent for the reason -apply
// exists at all.
var envExempt = map[string]bool{"version": true, "dry-run": true, "apply": true}

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
