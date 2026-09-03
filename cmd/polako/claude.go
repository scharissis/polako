package main

// Spawning one headless `claude -p` invocation and turning its exit into a
// verdict. runClaude builds the prompt (fresh skill run or a resume), execClaude
// and dispatchClaude run the process with the stall, budget and heartbeat
// watchdogs and stream its stdout, and the errXxx sentinels are the dead-end
// exits the caller acts on. Parsing the stream itself lives in stream.go;
// reading the CLI's refusal text lives in refusals.go.

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os/exec"
	"slices"
	"strings"
	"sync/atomic"
	"time"
)

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

// errBudget marks a run one of the per-issue caps killed. Like errAuth it is a
// dead end rather than a crash: resuming would spend the same budget over
// again on the same issue and reach the same kill, so a run carrying it parks
// its issue instead of being retried.
var errBudget = errors.New("the issue's budget is spent")

// errIssueCap marks a `polako plan` or `polako health` run dispatchClaude
// killed for reaching -max-issues. Not a crash: whatever the run created by
// then is real and stays, and the caller runs the label pass over it exactly
// as it would after a clean exit — the cap bounds spend, it does not undo
// work.
var errIssueCap = errors.New("the run hit its -max-issues ceiling")

// errLimit marks a run the CLI refused over the account's usage limit. Neither
// a dead end nor a crash: unlike a refused token this wall falls on its own —
// the refusal names when the limit resets — so the run's issue is neither
// parked nor charged a retry. processIssue waits the reset out and resumes.
var errLimit = errors.New("claude is over its usage limit")

// authAdvice attaches the fix to an authentication failure. This process runs
// unattended, so its last log line is usually the whole diagnosis a human
// gets, and "needs a human" without the remedy wastes the trip.
func authAdvice(err error) error {
	return fmt.Errorf("%w — check `claude auth status`, then `claude auth login` "+
		"(or `claude setup-token` for an unattended host), and start `polako work` again", err)
}

// runClaude executes one headless skill run. A non-empty resumeID continues
// that exact session instead of starting the skill fresh, reason says why —
// which is what picks the wording that session is resumed with — and a nonzero
// limit is what -max-issue-time has left for this issue.
func runClaude(ctx context.Context, cfg config, issue int, resumeID, reason string, limit time.Duration) (runReport, error) {
	cfg, prompt, invokes := issueRun(cfg, issue)
	if resumeID != "" {
		// Plain English, so invokes is empty: the resumed session already
		// holds the skill's context and never re-resolves the command.
		prompt, invokes = resumePrompt(cfg.skill, issue, reason), ""
	}
	return execClaude(ctx, cfg, prompt, resumeID, invokes, limit)
}

// resumePrompt is what a resumed session is told, and it is told to re-derive
// rather than to carry on. Every reason we resume — a stall, a kill, a host
// that slept — lands mid-action, and the CLI says as much: "The response above
// may be incomplete". An edit can have applied without the check that was going
// to follow it, a commit can be half-staged, a `gh pr create` can have
// succeeded with only its reply lost. Told to continue "exactly where it
// stopped", a run takes that last step for done.
//
// The issue thread is named alongside the branch and the PR because it holds
// the one piece of orchestration state a mid-action kill can leave inconsistent
// in a way nothing else recovers: a question posted with `gh issue comment` and
// awaitingAnswerLabel not yet raised. The supervisor reads an unflagged run as
// having produced nothing and parks a healthy issue, leaving the question
// unanswered — so "re-derive" has to reach the thread, not only the workspace.
//
// reasonUnfinished is the other thing we resume, and it needs its own opening
// and its own closing. "Interrupted part-way through an action" is true of a
// crash and false here, and the difference is load-bearing: a session that
// believes it started something in the background and will be brought back to
// it, resumed with a prompt that does not contradict that belief, has every
// reason to end its turn waiting again. So that text says outright that ending
// a turn ended the process, that nothing will wake it, and that there is no
// later turn to finish in — then names the finish, because "continue the
// workflow" is what the last turn thought it was doing.
//
// The re-derive paragraph in the middle is shared deliberately. It is the part
// both flavours need identically, and the part that would quietly rot if there
// were two copies of it.
//
// Two properties this wording has to keep, both pinned by a test. It must not
// begin with "/", or execClaude's contract would want it declared as a slash
// command it is not. And the issue number must stay the last number in it: a
// resume carries no "/skill N" for the fake CLI in the tests to read, so that
// is how an invocation is traced back to the issue it was dispatched for.
func resumePrompt(skill string, issue int, reason string) string {
	opening := fmt.Sprintf("The previous attempt at the /%s %d workflow was interrupted "+
		"part-way through an action, so whatever it did last may be incomplete. ", skill, issue)
	closing := "Then continue the workflow from what you found, rather than from " +
		"what the last step looks like it was doing."
	if reason == reasonUnfinished {
		opening = fmt.Sprintf("The previous attempt at the /%s %d workflow ended its turn without "+
			"opening a pull request. Ending a turn ends this process: there is no later turn, "+
			"nothing will wake you, and no background command, monitor or timer will be waited "+
			"on. Whatever that attempt meant to come back to, it has to be finished now instead. ",
			skill, issue)
		closing = "Then finish it in this turn: commit whatever is uncommitted, run the review " +
			"gate, and open the pull request."
	}
	return opening +
		"Before doing anything else, re-derive where things actually stand: " +
		"run `git status`, check whether the branch and the pull request " +
		"already exist, and re-read the issue thread. A question the last " +
		"attempt posted there may be missing the " + awaitingAnswerLabel + " label it needed, in " +
		"which case raise the label rather than asking again; a question " +
		"that already carries the label must not be posted twice. " +
		closing
}

// issueRun is what a fresh skill run on one issue consists of: the config it
// runs under, the prompt it is given, and the slash command that prompt
// invokes. It exists as a function of its own so -dry-run prints the invocation
// runClaude would make rather than a second rendering of it, which is the sort
// of pair that drifts the first time either side changes.
//
// The config is a copy, so the widened allowlist reaches this invocation and
// nothing else: the recorder's tools_hash goes on identifying the operator's
// -tools/-add-tools, which is the thing worth grouping runs by, rather than
// changing with every issue number.
func issueRun(cfg config, issue int) (config, string, string) {
	cfg.addTools = resolveTools(cfg.addTools, issueLabelTools(issue)+","+issueCloseTool(issue))
	return cfg, fmt.Sprintf("/%s %d", cfg.skill, issue), cfg.skill
}

// issueLabelTools grants a run the two commands it needs to raise and lower
// awaitingAnswerLabel, pinned to the issue it was dispatched for.
//
// A single `Bash(gh issue edit:*)` in defaultTools would cover both and is
// exactly what that list refuses to grant: it would let attacker-supplied issue
// text label some *other* issue, which on a -label-gated queue is enough to
// queue work no maintainer triaged. Pinned to one number, the ordinary reach is
// the issue the run is already working on — where the worst it can do is park
// or unpark itself, neither of which is an escalation. It is a prefix and not a
// signature, so a second issue number appended after the flag would still match
// it; this narrows the blast radius rather than sealing it, the same caveat the
// README puts on the allowlist as a whole.
//
// The number comes first because that is how the skill is told to spell the
// command and how anyone would write it by hand; a prefix that did not match
// would raise a permission prompt, and an unattended run has nobody to answer.
func issueLabelTools(issue int) string {
	return fmt.Sprintf("Bash(gh issue edit %d --add-label:*),Bash(gh issue edit %d --remove-label:*)",
		issue, issue)
}

// issueCloseTool grants a run the one command its fourth ending needs: an
// issue it verified needs no code change — already fixed elsewhere, a
// duplicate — closed rather than parked for a human to close by hand (#210).
// Pinned to the issue the run was dispatched for, for the same reason
// issueLabelTools is: a blanket `Bash(gh issue close:*)` would let
// attacker-supplied issue text close some *other* issue, and closing one is
// not an escalation an unattended run should be able to reach for outside its
// own dispatch. The worst case here is narrower than the label grant already
// accepted — a wrongly closed issue is one `gh issue reopen` away, the same
// one-click undo #197 already leans on for a container close.
func issueCloseTool(issue int) string {
	return fmt.Sprintf("Bash(gh issue close %d:*)", issue)
}

// effortLevels is the closed set `claude --effort` takes. Closed because the
// CLI's is: a level polako let through that the CLI then rejected would fail a
// run for a typo. `ultracode`, which the CLI's docs also list, is deliberately
// not here — it is a multi-agent workflow mode, not a level, and an unattended
// run has no business fanning out a fleet.
var effortLevels = []string{"low", "medium", "high", "xhigh", "max"}

// validateEffort checks -effort against effortLevels. Empty passes: the flag is
// omitted and the CLI resolves effort the way it would for a terminal session.
// The error says which word to pass instead, because this runs unattended and
// its output is the only diagnostic.
func validateEffort(effort string) error {
	if effort == "" || slices.Contains(effortLevels, effort) {
		return nil
	}
	if effort == "ultracode" {
		return fmt.Errorf("-effort ultracode is a multi-agent workflow mode, not an effort level — "+
			"an unattended run must not fan out a fleet; pass one of %s", strings.Join(effortLevels, ", "))
	}
	return fmt.Errorf("-effort %q is not a claude effort level — pass one of %s",
		effort, strings.Join(effortLevels, ", "))
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
	// --effort only on a fresh run: the session already carries the effort it
	// was started with, so re-passing it on a resume is at best redundant and
	// at worst a usage error, depending on whether the CLI treats effort as
	// session-fixed (unverified — see docs/plans/model-and-effort.md). Either
	// way "a resume keeps its run's choice" holds without the flag.
	if cfg.effort != "" && resumeID == "" {
		args = append(args, "--effort", cfg.effort)
	}
	// No --remote-control, whether -remote is on or off: today's CLI takes the
	// flag under -p and ignores it (see config.remote), so passing it buys an
	// argument pair and a false promise.
	return append(args,
		"--allowedTools", resolveTools(cfg.tools, cfg.addTools),
		"--output-format", "stream-json", // one JSON event per message, in real time
		"--verbose", // required for stream-json in print mode
	)
}

// maxEventBytes is the largest stream-json event the reader will accept.
// Generous because an event can carry a whole file's contents, and bounded
// because a scanner with no ceiling would grow its buffer to whatever a run
// emits. An event over it is a read error rather than a truncation — see
// execClaude, which reports it and kills the run instead of letting the child
// block on a pipe that stopped being drained.
const maxEventBytes = 32 * 1024 * 1024

// sampleTick is how often a watchdog checks its clock, given the interval it
// is enforcing: often enough to honour a short one promptly, capped so a long
// production interval still samples cheaply, floored so a tiny one (a test's)
// does not spin. Shared by the stall watchdog and the heartbeat.
func sampleTick(interval time.Duration) time.Duration {
	tick := interval / 4
	if tick > 30*time.Second {
		tick = 30 * time.Second
	}
	if tick < 50*time.Millisecond {
		tick = 50 * time.Millisecond
	}
	return tick
}

// execClaude runs one headless claude invocation with the shared streaming,
// logging, and stall-watchdog machinery. invokes names the slash command the
// prompt starts with — "" for a plain-text prompt — so a session whose init
// inventory lacks it can be stopped instead of run; the caller states it
// outright because re-deriving it from the prompt would make "never start a
// plain prompt with /" a load-bearing rule enforced nowhere. limit is what
// -max-issue-time has left for the issue this run is part of, or 0 for no
// bound at all. The report it returns is valid on every path, error included:
// the retry logic needs the session ID to resume, and the run burned real
// tokens whether or not it lived to report them.
func execClaude(ctx context.Context, cfg config, prompt, resumeID, invokes string, limit time.Duration) (runReport, error) {
	rep, err := dispatchClaude(ctx, cfg, prompt, resumeID, invokes, limit)
	// The last words on stderr, said beside whatever error the caller is about
	// to report. Here rather than at any one call site, because every dispatch
	// funnels through this function — a crashed remediation's cause is worth
	// the line as much as a crashed issue run's. Clipped: the full copy is in
	// the shift log, under -verbose it already streamed past, and an interrupt
	// is the operator's own doing, not a cause to explain.
	if err != nil && ctx.Err() == nil && !cfg.verbose {
		if t := strings.TrimSpace(rep.stderrTail); t != "" {
			log.Printf("last stderr: %s", clip(t, 300))
		}
	}
	return rep, err
}

// stderrWaitDelay is how long cmd.Wait will go on waiting for the stderr pipe
// once the process itself is gone. Long enough that ordinary output in flight
// is never truncated, short enough that an orphan holding the pipe costs
// seconds rather than the rest of the night.
const stderrWaitDelay = 5 * time.Second

// maxStderrTail bounds what a dispatch remembers of its child's stderr. Only
// the last few lines can be a usage error, and a run that logs for six hours
// must not be held in memory to find one.
const maxStderrTail = 4096

// tailWriter keeps the last maxStderrTail bytes written through it. Every byte
// still reaches the operator's terminal — this sits behind an io.MultiWriter,
// never in front of one. Unsynchronised on purpose: exec.Cmd.Wait waits for the
// goroutine copying stderr to finish, so a read after Wait is ordered behind
// every write, and there is no other reader.
type tailWriter struct{ buf []byte }

func (t *tailWriter) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > maxStderrTail {
		// Copied rather than resliced: a slice walked forward keeps the whole
		// original array alive behind it.
		t.buf = append([]byte(nil), t.buf[len(t.buf)-maxStderrTail:]...)
	}
	return len(p), nil
}

func (t *tailWriter) String() string { return string(t.buf) }

// dispatchClaude is one attempt at the invocation execClaude describes: it
// starts the process, streams it and reports what it did. The split exists for
// the one caller above, which may have to make the same attempt twice.
//
// The four steps read top to bottom: startClaude spawns the child, armWatchdogs
// arms the stall, budget and heartbeat kills, scanEvents reads the event stream,
// and claudeVerdict turns what happened into one of nine return values. The
// teardown order between scanEvents and claudeVerdict is load-bearing too —
// join the heartbeat before the finish line, kill on a read error before
// cmd.Wait — so it stays inline here rather than moving into a helper.
func dispatchClaude(ctx context.Context, cfg config, prompt, resumeID, invokes string, limit time.Duration) (runReport, error) {
	rep := runReport{sessionID: resumeID, turns: -1, exitCode: -1}
	invokeStart := time.Now()

	proc, err := startClaude(ctx, cfg, prompt, resumeID)
	if err != nil {
		return rep, err
	}

	w := armWatchdogs(cfg, proc.cmd, invokeStart, limit)
	defer w.stop()

	missing, scanErr := scanEvents(proc.stdout, cfg, proc.cmd, invokes, w, &rep)

	// The stream is over, so the terminal is about to get the finish line and
	// nothing more from this invocation — stop the heartbeat and wait for it to
	// return before that line, so it cannot print past "finished (…)".
	w.stopHeartbeat()

	// A read that failed leaves the child writing into a pipe nobody is
	// draining, so it blocks and cmd.Wait never returns — which is why the kill
	// comes before the wait rather than after it. Left to itself the failure
	// surfaced as a -stall kill up to fifteen minutes later, reported as a stall.
	if scanErr != nil {
		_ = proc.cmd.Process.Kill()
	}

	err = proc.cmd.Wait()
	// Only ever returned in place of a nil: the process exited cleanly and
	// something it left behind was still holding the stderr pipe when
	// WaitDelay ran out. That is a fact about an orphan, not about the run.
	if errors.Is(err, exec.ErrWaitDelay) {
		err = nil
	}
	// The copier is done once Wait returns, so whatever the child left
	// unterminated can be rendered now.
	proc.stderrLines.flush()
	if proc.cmd.ProcessState != nil {
		rep.exitCode = proc.cmd.ProcessState.ExitCode()
	}
	rep.stalled = w.stalled.Load()
	rep.interrupted = ctx.Err() != nil
	rep.overBudget = w.overspent.Load()
	rep.stderrTail = proc.errTail.String()

	return rep, claudeVerdict(&rep, cfg, prompt, missing, limit, scanErr, err)
}

// claudeProc is the handle to a started child: the command, its stdout pipe for
// the scanner, and the two stderr sinks the teardown reads back — stderrLines to
// flush the child's last unterminated line, errTail for the diagnosis stdout
// cannot carry.
type claudeProc struct {
	cmd         *exec.Cmd
	stdout      io.ReadCloser
	stderrLines *lineWriter
	errTail     *tailWriter
}

// startClaude builds one headless claude invocation, wires its output, and
// starts it. On any error dispatchClaude still returns the initialised
// runReport: its sessionID is what a retry resumes.
func startClaude(ctx context.Context, cfg config, prompt, resumeID string) (*claudeProc, error) {
	args := buildArgs(cfg, prompt, resumeID)
	detail.Printf("running: %s %s", cfg.claudeBin, strings.Join(args, " "))
	cmd := exec.CommandContext(ctx, cfg.claudeBin, args...)
	cmd.Dir = cfg.dir
	// The child's stderr goes into the narration stream line by line, so it
	// lands in the shift log stamped and attributed rather than raw across the
	// terminal. The tail is remembered besides, for the diagnoses stdout
	// cannot carry: a CLI that refuses the registration flags prints a usage
	// error and emits no events at all, and a crashed run's last words are
	// often the only cause on record.
	stderrLines := &lineWriter{prefix: "[claude stderr]"}
	errTail := &tailWriter{}
	cmd.Stderr = io.MultiWriter(stderrLines, errTail)
	// A writer that is not an *os.File makes os/exec hand the child a pipe
	// and copy it here, and cmd.Wait then waits on that pipe rather than on
	// the process — so a background command the run left behind, holding the
	// inherited stderr, would hold this drain open forever. The bound is what
	// keeps the promise that capturing stderr cannot hang a run.
	cmd.WaitDelay = stderrWaitDelay
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &claudeProc{cmd: cmd, stdout: stdout, stderrLines: stderrLines, errTail: errTail}, nil
}

// watchdogs bundles the three kills armWatchdogs arms around a running child and
// the state they share with the scan loop. The atomics are written on one side
// and read on the other — lastEvent by the scan loop and the stall goroutine,
// hbTools/hbPhase by the scan loop and the heartbeat, stalled and overspent by a
// goroutine and the teardown — so the struct is always used by pointer. stop and
// stopHeartbeat are two shutdown points, kept apart because the heartbeat has to
// be joined mid-teardown while the other two are torn down on return.
type watchdogs struct {
	lastEvent atomic.Int64
	hbTools   atomic.Int64
	hbPhase   atomic.Int64
	stalled   atomic.Bool
	overspent atomic.Bool

	watchDone chan struct{} // closed by stop to end the stall goroutine
	hbDone    chan struct{} // closed by stopHeartbeat to end the heartbeat
	hbStopped chan struct{} // closed by the heartbeat goroutine once it has returned
	budget    *time.Timer   // nil when limit is 0
}

// armWatchdogs starts the stall, budget and heartbeat watchdogs for cmd and
// returns the handle the scan loop feeds and the teardown reads.
func armWatchdogs(cfg config, cmd *exec.Cmd, invokeStart time.Time, limit time.Duration) *watchdogs {
	w := &watchdogs{
		watchDone: make(chan struct{}),
		hbDone:    make(chan struct{}),
		hbStopped: make(chan struct{}),
	}
	w.lastEvent.Store(invokeStart.UnixNano())

	// Stall watchdog: if no events arrive for cfg.stall, kill the run.
	// The caller's retry logic then resumes the session, context intact.
	if cfg.stall > 0 {
		go func() {
			t := time.NewTicker(sampleTick(cfg.stall))
			defer t.Stop()
			for {
				select {
				case <-w.watchDone:
					return
				case <-t.C:
					idle := time.Since(time.Unix(0, w.lastEvent.Load()))
					if idle > cfg.stall {
						narrate(sevWarning, "no activity for %s — killing the run to resume it",
							idle.Round(time.Millisecond))
						w.stalled.Store(true)
						_ = cmd.Process.Kill()
						return
					}
				}
			}
		}()
	}

	// Budget watchdog. Deliberately separate from the one above and blind to
	// what it watches: -stall samples for silence, and the run this exists for
	// is never silent — it emits events the whole way through the three hours
	// it spends going nowhere. A timer is all it takes, because unlike idleness
	// there is nothing to sample.
	if limit > 0 {
		w.budget = time.AfterFunc(limit, func() {
			log.Printf("this run has used the %s of -max-issue-time the issue had left — killing it",
				dur(limit))
			w.overspent.Store(true)
			_ = cmd.Process.Kill()
		})
	}

	// Heartbeat: while the terminal itself has been quiet for cfg.heartbeat,
	// say one "still working" line, and again every cfg.heartbeat it stays
	// quiet. It samples the terminal, not the stream the two watchdogs above
	// watch: for most of a run the stream is busy and the terminal is quiet
	// only because the sinks filtered its events out. Keying on terminal
	// silence is also why it says nothing under -verbose, where every event
	// reaches the terminal and the clock is reset before it can expire.
	//
	// It deliberately keeps speaking while the stream is quiet too. A stalled
	// run is not silently killed at fifteen minutes: the heartbeats before the
	// kill — a frozen tool count beside "running the review gate…" — are the
	// run-up that makes it legible. -stall decides whether a quiet run is
	// wedged; this only reports that the process has not exited, and with what
	// context.
	//
	// hbTools and hbPhase are the scan loop's publish-only mirror of two
	// values the goroutine may not read straight from rep/el without racing
	// the loop that fills them. Nothing branches on any of this.
	if cfg.heartbeat > 0 {
		go func() {
			defer close(w.hbStopped)
			t := time.NewTicker(sampleTick(cfg.heartbeat))
			defer t.Stop()
			for {
				select {
				case <-w.hbDone:
					return
				case <-t.C:
					// Closed-channel and tick can both be ready; a second,
					// non-blocking check keeps a heartbeat from landing after
					// the finish line, which is emitted once this goroutine has
					// joined (see stopHeartbeat).
					select {
					case <-w.hbDone:
						return
					default:
					}
					// max(lastTerm, invokeStart): sinks outlives one
					// invocation, so a previous issue's last line must not
					// read as this run's silence.
					since := invokeStart
					if lt := time.Unix(0, activeUI().lastTerm.Load()); lt.After(since) {
						since = lt
					}
					if time.Since(since) < cfg.heartbeat {
						continue
					}
					narrate(sevProgress, "[claude] %s", heartbeatLine(
						time.Since(invokeStart), int(w.hbTools.Load()), stage(w.hbPhase.Load())))
				}
			}
		}()
	} else {
		close(w.hbStopped)
	}
	return w
}

// stopHeartbeat ends the heartbeat goroutine and waits for it to return. Called
// from the teardown ahead of claudeVerdict's finish line, so a "still working"
// line can never print past "finished (…)", and ahead of cmd.Wait, which can
// block on an orphan holding the stderr pipe.
func (w *watchdogs) stopHeartbeat() {
	close(w.hbDone)
	<-w.hbStopped
}

// stop tears down the stall goroutine and the budget timer. Deferred in
// dispatchClaude: the teardown reads w.stalled and w.overspent first, exactly as
// it read the bare atomics before the two defers this replaces.
func (w *watchdogs) stop() {
	close(w.watchDone)
	if w.budget != nil {
		w.budget.Stop()
	}
}

// scanEvents reads the child's event stream to EOF, updating rep and the
// watchdog mirrors as it goes and killing the run on the two conditions the
// reader owns: a prompt whose slash command the session's init inventory does
// not list, and plan's/health's -max-issues cap. It returns the missing-command
// diagnosis (empty unless that kill fired) and the scanner's error.
func scanEvents(stdout io.Reader, cfg config, cmd *exec.Cmd, invokes string, w *watchdogs, rep *runReport) (missing string, scanErr error) {
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64*1024), maxEventBytes)
	var el eventLog
	for sc.Scan() {
		w.lastEvent.Store(time.Now().UnixNano())
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
		el.event(ev)
		w.hbTools.Store(int64(rep.toolUses))
		w.hbPhase.Store(int64(el.stages.phase()))
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
		// The issue cap, plan's and health's alike. Killed here, in the reader,
		// the same way a stall or a missing skill is: the run created everything
		// it was allowed to, and paying for whatever it does next buys nothing
		// the label pass (runPlan, runHealth) would keep. Normalised, not stranded.
		if cfg.maxIssues > 0 && rep.issueCreates >= cfg.maxIssues && !rep.capped {
			rep.capped = true
			narrate(sevWarning, "the run has filed %s, the whole of -max-issues — killing it; "+
				"the label pass still normalises what it created", plural(cfg.maxIssues, "issue"))
			_ = cmd.Process.Kill()
		}
	}
	return missing, sc.Err()
}

// claudeVerdict emits the run's one finish line and then turns what happened
// into the caller's outcome. The nine arms are in a deliberate order — each
// comment says why its arm sits where it does — and every one is load-bearing:
// skillMissing, overBudget, capped, stalled, scanErr, authFailed, limitMsg,
// zero-turn, then the bare wait error.
func claudeVerdict(rep *runReport, cfg config, prompt, missing string, limit time.Duration, scanErr, waitErr error) error {
	// One finish line for the run, emitted here rather than per result event:
	// the CLI sends a result per dequeued prompt, so a run that woke on five
	// background tasks streamed six, and observe has already summed the
	// per-turn fields across them (issue #227). Gated on a result having
	// arrived at all — a crash, stall or interrupt reaches this line too, and
	// each is reported as itself below, not as a finish.
	if rep.hasResult {
		sev, line := finishLine(rep)
		narrate(sev, "%s", line)
	}
	if rep.skillMissing {
		return fmt.Errorf("%w: %s", errNoWork, missing)
	}
	// Ahead of the stall and crash reports, because the kill produced both: the
	// caller has to see the deliberate stop rather than the crash it looks like.
	if rep.overBudget {
		return fmt.Errorf("%w: the run used the %s of -max-issue-time this issue had left",
			errBudget, dur(limit))
	}
	// Ahead of the stall report for the same reason overBudget is: the cap kill
	// also ends the scan, and the caller has to see it as the deliberate stop it
	// is rather than the stall it would otherwise be read as.
	if rep.capped {
		return fmt.Errorf("%w of %d", errIssueCap, cfg.maxIssues)
	}
	if rep.stalled {
		return fmt.Errorf("run stalled: no output events for %s", cfg.stall)
	}
	// Behind the deliberate kills above, which close the pipe themselves and so
	// end the scan at EOF rather than in an error, and ahead of the crash report
	// below, which on this path is only the kill just made. An ordinary error
	// rather than a dead end, so the caller resumes: a resumed session re-reads
	// where it got to and need not produce the same oversized event again.
	if scanErr != nil {
		return fmt.Errorf("could not read the event stream: %w — a single event larger than "+
			"%d MB is the likeliest cause, and the run was killed rather than left blocked "+
			"writing into a pipe nobody was reading", scanErr, maxEventBytes/(1024*1024))
	}
	// Deliberately not gated on the exit code: the CLI reports this as a
	// nonzero exit today, which is exactly what made it look like a crash
	// worth resuming, but a clean exit carrying the same result would be no
	// less of a dead end.
	if rep.authFailed {
		return errAuth
	}
	// The same reasoning one wall over, and equally not gated on the exit code:
	// the refusal is the result text, whatever the exit said. Unlike a refused
	// token this wall falls on its own, so the caller waits for the reset the
	// message names rather than stopping the drain.
	if rep.limitMsg != "" {
		return fmt.Errorf("%w: %s", errLimit, rep.limitMsg)
	}
	if waitErr == nil && rep.turns == 0 {
		return fmt.Errorf("%w: %q produced no work", errNoWork, prompt)
	}
	return waitErr
}
