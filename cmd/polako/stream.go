package main

// The stream-json event stream: streamEvent is the single parse every consumer
// shares, runReport is what one invocation folds down to as the stream arrives
// (valid even for a run that crashed before reporting), and eventLog renders the
// milestones a watching terminal sees. Spawning the process is claude.go;
// classifying its refusal text is refusals.go.

import (
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"
)

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
			// ID identifies a tool_use block (assistant); ToolUseID is
			// the same value echoed back on the tool_result (user) that
			// answers it, so the two can be correlated.
			ID         string          `json:"id"`
			ToolUseID  string          `json:"tool_use_id"`
			IsError    bool            `json:"is_error"`
			ResultText json.RawMessage `json:"content"`
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

// pendingTool is a tool_use event's name and raw input, kept only until its
// matching tool_result arrives — see runReport.pendingTools.
type pendingTool struct {
	name  string
	input json.RawMessage
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
	// issueCreates counts the `gh issue create` tool calls the run completed —
	// on the tool_result, not the tool_use, so a create still in flight when
	// the cap is reached is never counted (issue #340: counting the dispatch
	// let the Nth create be killed mid-flight, so N filed N-1). The one
	// action `polako plan` caps: dispatchClaude kills the run the way it
	// kills a stalled one once it reaches cfg.maxIssues; see capped below.
	issueCreates int

	// started says the session got going at all: the CLI announces itself with
	// an init event before it does anything else, so a run that emitted none
	// never reached a model. On a --resume that is the tell for a session the
	// CLI cannot honour — see processIssue, which stops resuming it.
	started bool

	exitCode     int
	stalled      bool
	capped       bool // `polako plan` hit -max-issues and dispatchClaude killed the run
	interrupted  bool
	skillMissing bool // the session's command list lacks the skill the prompt invokes
	authFailed   bool // the result text is the CLI reporting refused credentials
	// limitMsg holds the result text when a failing run was refused over the
	// account's usage limit, and stays empty otherwise. The text rather than a
	// bool, because the refusal carries the reset time the wait is read from.
	limitMsg string
	// permissionRefused is the third clean-exit case a park has to tell apart
	// from "decided nothing": either the run's own final words asked the
	// operator to approve a tool this allowlist never granted and got nobody
	// to answer (permissionRefusal, on every result event), or — issue #209 —
	// the CLI itself reported a refused tool_result mid-run (toolResultRefusal,
	// latched and never cleared by a later ordinary result: see the OR in the
	// result case below). A clean exit used to have its final text read once
	// (for authFailed/limitMsg, both gated on IsError) and otherwise dropped,
	// which is what let a park assert "no questions" over a run whose final
	// message was verbatim one.
	permissionRefused bool
	// permissionRefusedDetail names what was refused, when the stream gives
	// it: the tool_use correlated by id to the refused tool_result (preferred
	// — it has the actual command, which a single-command refusal's own
	// content text does not), or failing that the tool_result's own text
	// (which does name the parts, for a refused compound Bash command). May
	// hold a local absolute path (a worktree path in a Bash command), so — like
	// leftWork.where() — it belongs in a park's aside, never its reason.
	permissionRefusedDetail string
	// pendingTools tracks each in-flight tool_use's id to enough of it to name
	// later, so a refused tool_result — which the CLI reports as flat prose
	// with no command of its own for a single-command refusal — can still be
	// named. The raw pieces, not toolDetail's formatted string: that call
	// parses the input JSON, and the near-totality of tool calls are never
	// refused, so paying for it up front on every one would be waste — it
	// only runs once a refusal is actually confirmed, below. Cleared as each
	// id's result arrives; an id still in-flight when the run ends is simply
	// never read.
	pendingTools map[string]pendingTool
	// permissionAsked is the weaker sibling: some assistant turn along the way
	// — not necessarily the last — read as a request to use a tool this
	// allowlist never granted, even though the final result text did not (or
	// permissionRefused above would have caught it). It does not pre-empt a
	// resume the way permissionRefused does, because the run's closing words
	// were something else and it may have found a way round; it only sharpens
	// the park reason if the run parks anyway. Issue #182: on #169 the ask
	// ("This requires user confirmation to proceed") landed mid-run and the
	// run then wrapped up on a sentence the head anchor could not match, so
	// the issue parked as "no PR and no questions".
	permissionAsked bool
	overBudget      bool // -max-issue-time ran out while this run was still going
	// stderrTail is the last few KB the child wrote to stderr — for a crashed
	// run, often the only cause on record and worth a terminal line, since the
	// full copy is off in the shift log.
	stderrTail string
}

// status maps a run to exactly one value, most specific first: a run stopped
// over a missing skill was killed deliberately, so was one the budget stopped,
// an interrupted run is a nonzero exit too, and so is a stalled one — and so is
// a run the API refused to authenticate, which is the one worth telling apart
// from a crash.
func (r runReport) status() string {
	switch {
	case r.skillMissing:
		return "no-skill"
	case r.overBudget:
		return "budget"
	case r.interrupted:
		return "interrupted"
	case r.stalled:
		return "stalled"
	case r.authFailed:
		return "auth"
	case r.limitMsg != "":
		return "limit"
	case r.exitCode != 0:
		return "crash"
	case r.isError:
		return "error"
	case r.hasResult && r.turns == 0:
		return "no-turns"
	}
	return "ok"
}

// progressed reports whether a run got real work done before it ended. Only
// the observed counters can answer that: a run that crashed, stalled or was
// interrupted never emitted a result event, so its own turns and cost stay at
// nothing however long it actually ran.
//
// What it decides is whether a crash spent the -retries budget. That budget
// exists for a session that resumes and dies straight back; a run that worked
// for an hour and was cut off by a host going to sleep is not that, and
// charging it for one is how a healthy issue gets parked after four naps.
//
// Evidence of work, not evidence of an event: observedTurns counts every
// assistant event, and a --resume the CLI kills on arrival emits exactly
// one, with an empty usage block and no tool use — the CLI's death rattle,
// indistinguishable from real progress if an event were enough. Output
// tokens actually observed, or a tool use, are not.
func (r runReport) progressed() bool { return r.observed.Out > 0 || r.toolUses > 0 }

// observe folds one event into the report.
func (r *runReport) observe(ev streamEvent) {
	if ev.SessionID != "" {
		r.sessionID = ev.SessionID
	}
	switch ev.Type {
	case "system":
		if ev.Subtype == "init" {
			r.started = true
			if ev.Model != "" {
				r.model = ev.Model
			}
		}
	case "assistant":
		r.observedTurns++
		r.observed.add(ev.Message.Usage)
		for _, c := range ev.Message.Content {
			switch c.Type {
			case "tool_use":
				r.toolUses++
				if c.ID != "" {
					if r.pendingTools == nil {
						r.pendingTools = make(map[string]pendingTool)
					}
					r.pendingTools[c.ID] = pendingTool{name: c.Name, input: c.Input}
				}
			case "text":
				// A turn that opens by asking for approval is the run
				// narrating its own block, not a permissions issue quoted back
				// in a summary — the head anchor's guard against the latter
				// survives being read earlier. Latched: a later ordinary turn
				// does not unsay it.
				if permissionAskMidRun(c.Text) {
					r.permissionAsked = true
				}
			}
		}
	case "user":
		// The CLI's own fact that a tool was refused, not the model's later
		// retelling of it — see toolResultRefusal. Latched: unlike
		// permissionAsked this pre-empts a resume (below), because resuming
		// replays the identical session against the identical allowlist.
		for _, c := range ev.Message.Content {
			if c.Type != "tool_result" {
				continue
			}
			tool, hadTool := r.pendingTools[c.ToolUseID]
			delete(r.pendingTools, c.ToolUseID)
			// Counted here, not on the tool_use above: a create still in
			// flight when -max-issues is reached must not count, or the kill
			// lands before it returns and N files N-1 (issue #340). A
			// rejected create the skill retries still counts — see
			// ghIssueCreate's comment on that coarseness.
			if hadTool && isIssueCreate(tool.name, tool.input) {
				r.issueCreates++
			}
			if !c.IsError {
				continue
			}
			if text := toolResultContentText(c.ResultText); toolResultRefusal(text) {
				r.permissionRefused = true
				if r.permissionRefusedDetail == "" {
					if hadTool {
						r.permissionRefusedDetail = tool.name + toolDetail(tool.input)
					} else {
						r.permissionRefusedDetail = text
					}
				}
			}
		}
	case "result":
		firstResult := !r.hasResult
		r.hasResult = true
		r.subtype, r.isError = ev.Subtype, ev.IsError
		r.authFailed = ev.IsError && authFailure(ev.Result)
		if ev.IsError && limitRefusal(ev.Result) {
			r.limitMsg = ev.Result
		}
		// OR, not assign: a mid-run refused tool_result (above) must survive
		// a clean final result whose own text does not itself read as an
		// ask — issue #209, where every one of #126's three final messages
		// was ordinary prose despite the run having been refused a tool.
		r.permissionRefused = r.permissionRefused || permissionRefusal(ev.Result)
		// The CLI emits one result event per dequeued prompt, not one per run:
		// a run woken by ten finished background subagents streams ten, all
		// flushed at exit (issue #227). num_turns, the two durations and the
		// top-level usage block are that prompt turn's alone, so they add up
		// across the events; total_cost_usd and modelUsage are
		// process-cumulative and already whole on every event, so they stay
		// last-wins. The two families are indistinguishable in the JSON — only
		// what the CLI puts in them differs — so treating them differently is
		// deliberate, not an oversight to "fix" back to matching assignments.
		//
		// Process-cumulative, not session-: the counter resets with the
		// process, so a --resume reports its own spend and not the session it
		// continued — which is why tally.add may sum records. Measured on CLI
		// 2.1.252 for issue #258: one session across three processes billed
		// $0.0695, then $0.0165, then $0.0091, and a cumulative counter
		// cannot go down. #227 left that open because this word was loose.
		if firstResult {
			r.turns = 0 // clear the -1 pre-result sentinel errNoWork reads
		}
		r.turns += ev.NumTurns
		r.wallMS += ev.DurationMS
		r.apiMS += ev.DurationAPIMS
		r.usage.add(ev.Usage)
		r.costUSD = ev.TotalCost
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

// ghIssueCreate matches a shell command that files a GitHub issue — the one
// tool call `polako plan` caps. Whitespace-flexible so `gh  issue create` and a
// leading `cd x && gh issue create` both count, and anchored on `gh` at a word
// boundary so a path ending in "gh" or a `gh issue create-foo` subcommand that
// never existed does not. A compound command that files two issues in one
// tool call still counts once — observe counts calls, not issues, the same
// coarseness every other counter here has — and the reason -max-issues is
// documented as a ceiling rather than an exact stop: a rejected create (an old
// gh refusing `--parent`, say) that the skill retries flat counts twice, so the
// cap can fire a create or two early. That is the safe direction to be wrong in.
var ghIssueCreate = regexp.MustCompile(`(^|[^\w./-])gh\s+issue\s+create(\s|$)`)

// isIssueCreate reports whether a tool_use is a Bash call that files an issue.
// A `--help` invocation is not one: it is the capability probe, and counting it
// against the cap would be absurd.
func isIssueCreate(name string, input json.RawMessage) bool {
	if name != "Bash" {
		return false
	}
	var in struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(input, &in) != nil {
		return false
	}
	if strings.Contains(in.Command, "--help") {
		return false
	}
	return ghIssueCreate.MatchString(in.Command)
}

// eventLog carries the one thing rendering the stream needs to remember: that
// this invocation has already announced itself. The CLI emits a system/init
// event for every dequeued prompt, not once per process — so a review-gate
// subagent finishing and waking the main loop looks byte-for-byte like a
// session starting again (issue #224). The first init is the run's milestone;
// the rest are that wakeup, and belong in the shift log rather than on an
// operator's glance. Per invocation, not per process: a genuine --resume is a
// new dispatchClaude call with a new eventLog, so it still announces itself,
// and the drain_test.go assertions counting "session started" per run stay
// exact. The stage narrator lives here for the same per-invocation reason —
// see stages.go.
type eventLog struct {
	started bool
	stages  stageNarrator
}

// event renders one stream-json event as a single progress line. A run's start
// is a milestone and its finish is another — but the finish is emitted once by
// dispatchClaude from the whole run's standing (finishLine), because the CLI
// sends a result event per dequeued prompt and observe sums their per-turn
// fields into the one run total. The turns between start and finish — every
// tool call and assistant message — are detail, save for the stage narrator's
// one milestone per phase (stages.go): a watching terminal sees a run as its
// phases, and the shift log keeps the whole conversation.
func (el *eventLog) event(ev streamEvent) {
	el.stages.observe(ev)
	switch ev.Type {
	case "system":
		if ev.Subtype == "init" {
			// The id is the only handle that reopens this run in full, and the
			// stream is the only place it is ever announced. Omitted when the
			// event carries none, rather than logging an empty pair of
			// parentheses for a CLI that did not report one.
			session := ""
			if ev.SessionID != "" {
				session = ", session " + ev.SessionID
			}
			if el.started {
				// A later init is the main loop waking on a finished background
				// task, not a new session — same model, same id. To the shift
				// log, so a heavy review gate does not read as a crash loop.
				detail.Printf("[claude] resumed after a background task (model %s%s)", ev.Model, session)
				return
			}
			el.started = true
			log.Printf("[claude] session started (model %s%s)", ev.Model, session)
		}
	case "assistant":
		for _, c := range ev.Message.Content {
			switch c.Type {
			case "text":
				if t := strings.TrimSpace(c.Text); t != "" {
					detail.Printf("[claude] %s", clip(t, 160))
				}
			case "tool_use":
				detail.Printf("[claude] → %s%s", c.Name, toolDetail(c.Input))
			}
		}
	case "result":
		// The result text: for a healthy run it restates the last assistant
		// message and is detail, but a result the CLI synthesized itself —
		// "Unknown skill: x" — appears nowhere else in the stream and is
		// usually the whole diagnosis, so an error's text is a milestone. The
		// "finished" line itself is not emitted here — see finishLine — because
		// a background-task wakeup ends with its own result event too, and ten
		// of those read as a run that cost ten times what it did.
		if t := strings.TrimSpace(ev.Result); t != "" {
			if ev.IsError {
				log.Printf("[claude] %s", clip(t, 160))
			} else {
				detail.Printf("[claude] %s", clip(t, 160))
			}
		}
	}
}

// finishLine renders a run's one finish milestone from the report observe
// built — turns and wall summed across every result event the run streamed,
// the same numbers the run record and the exit status carry. Its caller emits
// it once, after the stream ends, only when a result actually arrived: a
// crash, a stall or an interrupt is the caller's to report, not a finish.
func finishLine(rep *runReport) (severity, string) {
	status, sev := "ok", sevSuccess
	if rep.isError {
		// is_error is the authority, not the subtype: the CLI reports an
		// authentication failure as is_error with subtype "success", which
		// rendered as the self-contradicting "ERROR: success".
		status, sev = "ERROR", sevError
		if rep.subtype != "" && rep.subtype != "success" {
			status += ": " + rep.subtype
		}
	}
	return sev, fmt.Sprintf("[claude] finished (%s) — %d turns, %s, $%.2f", status, rep.turns,
		(time.Duration(rep.wallMS) * time.Millisecond).Round(time.Second), rep.costUSD)
}

// heartbeatLine is the "still working" note the heartbeat emits while the
// terminal is quiet: how long this claude invocation has run, how many tool
// calls it has made, and the phase the stage recognizer last named (dropped
// until one is recognised). Elapsed is rounded to the minute — this line is a
// reassurance, not a measurement, and the issue that asked for it renders it
// that way. Not cost and not tokens: both read zero until the result event,
// and a number that is always $0.00 mid-run is worse than no number.
func heartbeatLine(elapsed time.Duration, toolUses int, phase stage) string {
	line := fmt.Sprintf("still working — %s in, %s",
		dur(elapsed.Round(time.Minute)), plural(toolUses, "tool call"))
	if s := strings.TrimSuffix(stageLine(phase), "…"); s != "" {
		line += ", " + s
	}
	return line
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
	// Skill carries none of those keys, so without this the review gate — the
	// most consequential tool call in an implement-issue run — reads as a bare
	// "→ Skill". Name the skill, and the arguments beside it when there are any.
	if v, ok := in["skill"].(string); ok && v != "" {
		if args, _ := in["args"].(string); args != "" {
			v += " " + args
		}
		return ": " + clip(v, 120)
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
