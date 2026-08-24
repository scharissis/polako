// Command backlog-drain drives Claude Code through a repository's GitHub
// issues, strictly in ascending order, one at a time.
//
// For each open issue it runs `claude -p "/implement-issue N"`, then waits on
// GitHub until the resulting PR is merged before advancing — so every run
// branches from a default branch that already contains the previous merge, and
// sequential runs can't conflict with each other.
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

// errNoWork marks a run that exited cleanly without taking a single turn.
// In practice that means the prompt never resolved — almost always a -skill
// naming a slash command this installation does not have.
var errNoWork = errors.New("claude took no turns")

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
	dir            string
	claudeBin      string
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
	tag string
	rec *recorder

	// Filled in by preflight, recorded with every run: which repository this
	// is, and which CLI produced its numbers.
	repo          string
	claudeVersion string
}

func main() {
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
	flag.BoolVar(&cfg.once, "once", false, "process a single issue to merge, then exit")
	flag.StringVar(&cfg.tag, "run-tag", "", "label recorded with every run, for comparing one batch against another")
	flag.StringVar(&metrics, "metrics", "",
		`directory for run-data records, or "off" (default ~/.backlog-drain/metrics)`)
	flag.Parse()

	cfg.rec = newRecorder(metrics)
	cfg.skip = parseSkip(skip)
	abs, err := filepath.Abs(cfg.dir)
	if err != nil {
		log.Fatalf("resolving -dir: %v", err)
	}
	cfg.dir = abs
	return cfg
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
	for {
		issue, err := lowestOpenIssue(ctx, cfg)
		if err != nil {
			return err
		}
		if issue == 0 {
			log.Println("no open issues — backlog drained")
			return nil
		}
		log.Printf("=== issue #%d ===", issue)

		if err := processIssue(ctx, cfg, issue); err != nil {
			return fmt.Errorf("issue #%d: %w", issue, err)
		}
		if cfg.once {
			log.Println("-once set — exiting after one issue")
			return nil
		}
	}
}

// preflight fails fast on a misconfigured environment, so an unattended run
// can't die on its first gh call an hour after being started.
func preflight(ctx context.Context, cfg *config) error {
	for _, bin := range []string{cfg.claudeBin, "gh", "git"} {
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
	log.Printf("%s — running /%s per issue, polling every %s", cfg.repo, cfg.skill, cfg.poll)
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

// processIssue advances one issue all the way to merged, however many
// implement/wait cycles that takes.
func processIssue(ctx context.Context, cfg config, issue int) error {
	branch := fmt.Sprintf("%s%d", cfg.branchPrefix, issue)
	attempt := 0      // 0 = fresh skill run; >0 = retry after a crash
	sessionID := ""   // from the run's init event; resume target for retries
	answered := false // a human replied, so the next run folds the answers in

	// terminal marks how the issue ended, failures included — they are the
	// most informative rows in the dataset, and every one of them exits the
	// process. Transient GitHub errors and Ctrl+C are deliberately not
	// outcomes: the issue is still open, and the next drain resumes it.
	terminal := func(prNumber int, outcome string) {
		cfg.rec.recordIssue(cfg, issue, prNumber, outcome)
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
				cfg.rec.recordRun(cfg, rc, rep)
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
					return fmt.Errorf("claude crashed and %d resume attempts failed — needs a human", cfg.retries)
				default:
					// Clean exit, yet no PR and no questions: Claude decided
					// nothing, which a machine shouldn't paper over.
					record(0, outcomeNothing)
					terminal(0, issueNeedsHuman)
					return errors.New("run completed but produced no PR and no questions — needs a human")
				}
			}
			record(pr.Number, outcomeOpenedPR)
			attempt = 0
		}

		switch pr.State {
		case "OPEN":
			log.Printf("PR #%d open — waiting for merge (%s)", pr.Number, pr.URL)
			state, err := supervisePR(ctx, cfg, issue, pr.Number)
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
				return ensureIssueClosed(ctx, cfg, issue, pr.Number)
			}
			terminal(pr.Number, issueClosed)
			return fmt.Errorf("PR #%d closed without merge — needs a human decision", pr.Number)
		default:
			terminal(pr.Number, issueNeedsHuman)
			return fmt.Errorf("PR #%d in unexpected state %q", pr.Number, pr.State)
		}
	}
}

// --- Claude ---

// runClaude executes one headless skill run. A non-empty resumeID continues
// that exact session instead of starting the skill fresh.
func runClaude(ctx context.Context, cfg config, issue int, resumeID string) (runReport, error) {
	prompt := fmt.Sprintf("/%s %d", cfg.skill, issue)
	if resumeID != "" {
		prompt = fmt.Sprintf("Continue the /%s %d workflow exactly where it stopped.", cfg.skill, issue)
	}
	return execClaude(ctx, cfg, prompt, resumeID)
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
	Message   struct {
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

	exitCode    int
	stalled     bool
	interrupted bool
}

// status maps a run to exactly one value, most specific first: an interrupted
// run is a nonzero exit too, and so is a stalled one.
func (r runReport) status() string {
	switch {
	case r.interrupted:
		return "interrupted"
	case r.stalled:
		return "stalled"
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

// execClaude runs one headless claude invocation with the shared streaming,
// logging, and stall-watchdog machinery. The report it returns is valid on
// every path, error included: the retry logic needs the session ID to resume,
// and the run burned real tokens whether or not it lived to report them.
func execClaude(ctx context.Context, cfg config, prompt, resumeID string) (runReport, error) {
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
	}

	err = cmd.Wait()
	if cmd.ProcessState != nil {
		rep.exitCode = cmd.ProcessState.ExitCode()
	}
	rep.stalled = stalled.Load()
	rep.interrupted = ctx.Err() != nil
	if rep.stalled {
		return rep, fmt.Errorf("run stalled: no output events for %s", cfg.stall)
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
		status := "ok"
		if ev.IsError {
			status = "ERROR: " + ev.Subtype
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
	args := []string{"issue", "list", "--state", "open", "--limit", "200", "--json", "number"}
	if cfg.label != "" {
		args = append(args, "--label", cfg.label)
	}
	out, err := gh(ctx, cfg, args...)
	if err != nil {
		return 0, err
	}
	var issues []struct {
		Number int `json:"number"`
	}
	if err := json.Unmarshal(out, &issues); err != nil {
		return 0, fmt.Errorf("parsing issue list: %w", err)
	}
	numbers := make([]int, len(issues))
	for i, is := range issues {
		numbers[i] = is.Number
	}
	return pickLowest(numbers, cfg.skip), nil
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
func supervisePR(ctx context.Context, cfg config, issue, prNumber int) (string, error) {
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
			if rerr := remediateConflicts(ctx, cfg, issue, prNumber); rerr != nil {
				if ctx.Err() != nil {
					return "", ctx.Err()
				}
				failures++
				log.Printf("remediation attempt %d/%d failed (%v)", failures, cfg.retries, rerr)
				if failures >= cfg.retries {
					return "", fmt.Errorf("conflict remediation for PR #%d failed %d times — needs a human",
						prNumber, failures)
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
func remediateConflicts(ctx context.Context, cfg config, issue, prNumber int) error {
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
	rep, err := execClaude(ctx, cfg, prompt, "")
	// A remediation run pushes to a PR that already exists, so it leaves
	// behind neither a new PR nor questions.
	cfg.rec.recordRun(cfg, runContext{
		issue: issue, pr: prNumber, reason: reasonRemediate, outcome: outcomeNothing,
		started: started, ended: time.Now(),
	}, rep)
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
	return capture(ctx, cfg.dir, "gh", args...)
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
