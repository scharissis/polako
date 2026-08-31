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

// errPlanCap marks a `polako plan` run dispatchClaude killed for reaching
// -max-issues. Not a crash: whatever the run created by then is real and stays,
// and runPlan runs the label pass over it exactly as it would after a clean
// exit — the cap bounds spend, it does not undo work.
var errPlanCap = errors.New("the plan run hit its -max-issues ceiling")

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
	cfg.addTools = resolveTools(cfg.addTools, issueLabelTools(issue))
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
func dispatchClaude(ctx context.Context, cfg config, prompt, resumeID, invokes string, limit time.Duration) (runReport, error) {
	rep := runReport{sessionID: resumeID, turns: -1, exitCode: -1}
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
		return rep, err
	}
	if err := cmd.Start(); err != nil {
		return rep, err
	}

	// Stall watchdog: if no events arrive for cfg.stall, kill the run.
	// The caller's retry logic then resumes the session, context intact.
	invokeStart := time.Now()
	var lastEvent atomic.Int64
	lastEvent.Store(invokeStart.UnixNano())
	var stalled atomic.Bool
	watchDone := make(chan struct{})
	defer close(watchDone)
	if cfg.stall > 0 {
		go func() {
			t := time.NewTicker(sampleTick(cfg.stall))
			defer t.Stop()
			for {
				select {
				case <-watchDone:
					return
				case <-t.C:
					idle := time.Since(time.Unix(0, lastEvent.Load()))
					if idle > cfg.stall {
						narrate(sevWarning, "no activity for %s — killing the run to resume it",
							idle.Round(time.Millisecond))
						stalled.Store(true)
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
	var overspent atomic.Bool
	if limit > 0 {
		budget := time.AfterFunc(limit, func() {
			log.Printf("this run has used the %s of -max-issue-time the issue had left — killing it",
				dur(limit))
			overspent.Store(true)
			_ = cmd.Process.Kill()
		})
		defer budget.Stop()
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
	var hbTools, hbPhase atomic.Int64
	hbDone := make(chan struct{})
	hbStopped := make(chan struct{}) // closed once the heartbeat goroutine has returned
	if cfg.heartbeat > 0 {
		go func() {
			defer close(hbStopped)
			t := time.NewTicker(sampleTick(cfg.heartbeat))
			defer t.Stop()
			for {
				select {
				case <-hbDone:
					return
				case <-t.C:
					// Closed-channel and tick can both be ready; a second,
					// non-blocking check keeps a heartbeat from landing after
					// the finish line, which is emitted once this goroutine has
					// joined (see the <-hbStopped below).
					select {
					case <-hbDone:
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
						time.Since(invokeStart), int(hbTools.Load()), stage(hbPhase.Load())))
				}
			}
		}()
	} else {
		close(hbStopped)
	}

	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64*1024), maxEventBytes)
	missing := "" // the diagnosis, once the inventory rules the prompt's command out
	var el eventLog
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
		el.event(ev)
		hbTools.Store(int64(rep.toolUses))
		hbPhase.Store(int64(el.stages.phase()))
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
		// `polako plan`'s issue cap. Killed here, in the reader, the same way a
		// stall or a missing skill is: the run created everything it was allowed
		// to, and paying for whatever it does next buys nothing the label pass
		// would keep. The pass runs regardless — see runPlan — so what is on
		// GitHub at the kill is normalised, not stranded.
		if cfg.maxIssues > 0 && rep.issueCreates >= cfg.maxIssues && !rep.capped {
			rep.capped = true
			narrate(sevWarning, "the run has filed %s, the whole of -max-issues — killing it; "+
				"the label pass still normalises what it created", plural(cfg.maxIssues, "issue"))
			_ = cmd.Process.Kill()
		}
	}

	// The stream is over, so the terminal is about to get the finish line and
	// nothing more from this invocation — stop the heartbeat and wait for it to
	// return before that line, so it cannot print past "finished (…)". Kept
	// ahead of cmd.Wait, which can block on an orphan holding the stderr pipe.
	close(hbDone)
	<-hbStopped

	// A read that failed leaves the child writing into a pipe nobody is
	// draining, so it blocks and cmd.Wait never returns — which is why the kill
	// comes before the wait rather than after it. Left to itself the failure
	// surfaced as a -stall kill up to fifteen minutes later, reported as a stall.
	scanErr := sc.Err()
	if scanErr != nil {
		_ = cmd.Process.Kill()
	}

	err = cmd.Wait()
	// Only ever returned in place of a nil: the process exited cleanly and
	// something it left behind was still holding the stderr pipe when
	// WaitDelay ran out. That is a fact about an orphan, not about the run.
	if errors.Is(err, exec.ErrWaitDelay) {
		err = nil
	}
	// The copier is done once Wait returns, so whatever the child left
	// unterminated can be rendered now.
	stderrLines.flush()
	if cmd.ProcessState != nil {
		rep.exitCode = cmd.ProcessState.ExitCode()
	}
	rep.stalled = stalled.Load()
	rep.interrupted = ctx.Err() != nil
	rep.overBudget = overspent.Load()
	rep.stderrTail = errTail.String()
	// One finish line for the run, emitted here rather than per result event:
	// the CLI sends a result per dequeued prompt, so a run that woke on five
	// background tasks streamed six, and observe has already summed the
	// per-turn fields across them (issue #227). Gated on a result having
	// arrived at all — a crash, stall or interrupt reaches this line too, and
	// each is reported as itself below, not as a finish.
	if rep.hasResult {
		sev, line := finishLine(&rep)
		narrate(sev, "%s", line)
	}
	if rep.skillMissing {
		return rep, fmt.Errorf("%w: %s", errNoWork, missing)
	}
	// Ahead of the stall and crash reports, because the kill produced both: the
	// caller has to see the deliberate stop rather than the crash it looks like.
	if rep.overBudget {
		return rep, fmt.Errorf("%w: the run used the %s of -max-issue-time this issue had left",
			errBudget, dur(limit))
	}
	// Ahead of the stall report for the same reason overBudget is: the cap kill
	// also ends the scan, and the caller has to see it as the deliberate stop it
	// is rather than the stall it would otherwise be read as.
	if rep.capped {
		return rep, fmt.Errorf("%w of %d", errPlanCap, cfg.maxIssues)
	}
	if rep.stalled {
		return rep, fmt.Errorf("run stalled: no output events for %s", cfg.stall)
	}
	// Behind the deliberate kills above, which close the pipe themselves and so
	// end the scan at EOF rather than in an error, and ahead of the crash report
	// below, which on this path is only the kill just made. An ordinary error
	// rather than a dead end, so the caller resumes: a resumed session re-reads
	// where it got to and need not produce the same oversized event again.
	if scanErr != nil {
		return rep, fmt.Errorf("could not read the event stream: %w — a single event larger than "+
			"%d MB is the likeliest cause, and the run was killed rather than left blocked "+
			"writing into a pipe nobody was reading", scanErr, maxEventBytes/(1024*1024))
	}
	// Deliberately not gated on the exit code: the CLI reports this as a
	// nonzero exit today, which is exactly what made it look like a crash
	// worth resuming, but a clean exit carrying the same result would be no
	// less of a dead end.
	if rep.authFailed {
		return rep, errAuth
	}
	// The same reasoning one wall over, and equally not gated on the exit code:
	// the refusal is the result text, whatever the exit said. Unlike a refused
	// token this wall falls on its own, so the caller waits for the reset the
	// message names rather than stopping the drain.
	if rep.limitMsg != "" {
		return rep, fmt.Errorf("%w: %s", errLimit, rep.limitMsg)
	}
	if err == nil && rep.turns == 0 {
		return rep, fmt.Errorf("%w: %q produced no work", errNoWork, prompt)
	}
	return rep, err
}
