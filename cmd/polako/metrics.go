package main

// Run-data capture: one JSONL record per claude invocation, and one more per
// issue that reaches a terminal state.
//
// This is telemetry, not state. The drain loop never reads these files, no
// decision depends on them, and deleting the directory mid-drain changes
// nothing about what the supervisor does next — which is what keeps them
// compatible with "all state lives in GitHub". Writes are best-effort by
// construction: a record can be missing, it can never be required.
//
// Records hold numbers, identifiers and operator-chosen labels only. Issue and
// PR text never enters them: it is sensitive, and on any repository that
// accepts outside issues it is attacker-controllable. That is also what makes
// a record file safe to hand to a teammate without re-reading it first.
//
// Nothing here reaches the network. The one path that ever shows these numbers
// to anybody else is -post-summary, off unless an operator asks for it: it puts
// a numbers-only comment on their own merged PR, visible to exactly the people
// who could already see that PR. Nothing goes anywhere else, ever.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"log"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"
)

// recordVersion changes only if a field's meaning changes. Readers ignore
// unknown fields and unknown kinds, so the schema grows additively instead.
const recordVersion = 1

// metricsOff is the -metrics value that disables every write.
const metricsOff = "off"

// Why a run happened. Analysis leans on this: a remediation run and a first
// implementation attempt are not comparable work.
const (
	reasonImplement = "implement" // fresh skill run on an issue
	reasonResume    = "resume"    // --resume after a crash or a stall
	// --resume after a run that exited cleanly, opened no PR and left work on
	// disk anyway. Kept apart from reasonResume because nothing crashed: this is
	// the run that decided to wait for something, or ran out of road mid-task,
	// and how often it happens is the measurement that says whether the skill's
	// one-turn rule is working.
	reasonUnfinished = "unfinished"
	reasonAnswers    = "answers"   // fresh re-run after a human replied on the thread
	reasonRemediate  = "remediate" // conflict remediation while a PR is open
	reasonChecks     = "checks"    // CI remediation while a PR is open
	reasonReview     = "review"    // answering a reviewer who asked for changes
)

// What a run left behind. A remediation run pushes to a PR that already
// exists, so it leaves behind neither a new PR nor questions, and records
// outcomeNothing. outcomeUnknown covers the cases where the run ended but what
// it produced is genuinely not knowable: the GitHub call that would have told
// us failed, or a wall cut the run off before it could finish — Ctrl+C, or a
// usage limit refused mid-session. A record saying "we could not tell" beats
// forcing one of those into outcomeNothing, which would read as a run that
// decided to produce nothing and bias every rate computed from these files.
const (
	outcomeOpenedPR  = "opened_pr"
	outcomeQuestions = "posted_questions"
	outcomeNothing   = "nothing"
	outcomeUnknown   = "unknown"
	// outcomeClosedIssue is the fourth ending (#210): a run that verified the
	// issue needed no code change and closed it directly rather than opening a
	// PR. Distinct from outcomeNothing, which would bias every "did a run
	// produce something" rate this feeds — this run did produce something, it
	// just was not a PR.
	outcomeClosedIssue = "closed_issue"
)

// How an issue ended. Failures are the most valuable rows in the dataset, so
// the "needs a human" exits record one too.
const (
	issueMerged = "merged"
	issueClosed = "closed_unmerged"
	// issueClosedNoChange is the fourth ending's issue-level outcome: closed
	// with no PR at all, on evidence the run verified itself, and not a park —
	// nothing is left for a human. Not the same bucket as issueClosed, which is
	// a *park* category for a PR closed without merging, a human's decision.
	issueClosedNoChange = "closed_no_change"
	issueNeedsHuman     = "needs_human"
)

// Why an issue was handed back. issueNeedsHuman is one bucket, and "what parks
// issues most" is the ranking that says which half of the tool to spend the
// next change on — so every park names one of these, and the taxonomy is the
// park callsites themselves rather than a guess about them.
//
// Identifiers, never the park's message: the reason text quotes issue numbers,
// dollar figures and branch names, and records hold no text. parkUnknown is
// what a path that cannot name itself writes, which is not the same as a
// record written before this field existed — that one carries nothing at all,
// and stats counts it as unrecorded.
const (
	parkBudget  = "budget"            // a -max-cost or -max-issue-time cap
	parkRetries = "retries_exhausted" // claude kept dying and the resumes ran out
	parkNothing = "produced_nothing"  // a clean run that opened no PR and left nothing behind
	parkNoSkill = "no_skill"          // -skill names nothing this installation has
	parkAuth    = "auth"              // the API refused the token
	// The run's own final words, retained now rather than dropped, asked to be
	// granted a tool this allowlist refused. Filed apart from parkNothing for
	// the same reason parkBudget and parkRetries already are: the lever is
	// -add-tools, an operator setting, not the skill.
	parkPermission = "permission_refused"
	parkConflicts  = "conflict_remediation"
	parkChecks     = "checks_remediation"
	parkReview     = "review_remediation"
	parkPRState    = "pr_state" // the PR is in a state the supervisor does not know
	// Closed without merging — a human's decision, and the one park whose
	// record says closed_unmerged rather than needs_human. recordIssue drops
	// the reason there, because the outcome has already said it; the category
	// exists so the park itself is classified like every other one.
	parkPRClosed = "pr_closed"
	parkUnknown  = "unknown" // the park path could not say
)

// parkReasonOrder is the order stats lists them in: roughly the order they
// happen in an issue's life, from a run that never started to a PR nobody
// merged. breakdown appends anything a newer version wrote, so the list never
// has to be exhaustive.
var parkReasonOrder = []string{parkNoSkill, parkAuth, parkBudget, parkRetries,
	parkNothing, parkPermission, parkConflicts, parkChecks, parkReview, parkPRState, parkPRClosed, parkUnknown}

// Where a run's numbers came from. Crash, stall and interrupt never emit a
// result event, yet burned real tokens; those runs report the tally observed
// as they streamed, and say so.
const (
	usageResult   = "result"
	usageObserved = "observed"
)

// newShiftID names one process's records, so a report can single out the shift
// that wrote them. Random rather than derived from the clock or the pid,
// because the two drains hardest to tell apart are the ones a timestamp cannot
// separate: two started in the same second, and one holding a pid the kernel
// has just reused.
//
// Four bytes. The id is only ever compared against the handful of drains whose
// records share a directory, and it has to be short enough to retype from a
// startup line.
func newShiftID() string {
	var b [4]byte
	// crypto/rand.Read fills the buffer or crashes the program; there is no
	// half-filled outcome to guard against, and no fallback worth writing for
	// a machine whose randomness has failed.
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// tokenCounts is the token block, shared by the run total and each per-model
// entry.
type tokenCounts struct {
	In         int64 `json:"in"`
	Out        int64 `json:"out"`
	CacheRead  int64 `json:"cache_read"`
	CacheWrite int64 `json:"cache_write"`
}

func (t *tokenCounts) add(u streamUsage) {
	t.In += u.Input
	t.Out += u.Output
	t.CacheRead += u.CacheRead
	t.CacheWrite += u.CacheWrite
}

// addCounts folds one already-counted block into another — what every rollup
// over runs does, whether it is summing an issue or a whole window.
func (t *tokenCounts) addCounts(o tokenCounts) {
	t.In += o.In
	t.Out += o.Out
	t.CacheRead += o.CacheRead
	t.CacheWrite += o.CacheWrite
}

// total is the four ways a run spends tokens, added up.
func (t tokenCounts) total() int64 { return t.In + t.Out + t.CacheRead + t.CacheWrite }

// modelTokens is one model's share of a run. A single run routinely spans
// models — a cheap one for the small stuff — so "which model is cheapest per
// merged PR" needs the split, not just the total.
type modelTokens struct {
	tokenCounts
	CostUSD float64 `json:"cost_usd"`
}

// runContext is everything about a run that the event stream cannot know:
// which issue it was for, why it ran, and what it left behind.
type runContext struct {
	issue       int
	pr          int
	reason      string
	attempt     int
	resumedFrom string
	outcome     string
	// What the policy resolved for this run — the record's requested_model /
	// requested_effort and the source of each. Embedded, and for a remediation
	// run not necessarily cfg.model / cfg.effort, which is the whole reason it
	// travels here rather than being read off cfg in newRunRecord. One embed
	// rather than four fields so a new policy dimension (#363/#364) is threaded
	// through here once. See policy.go.
	runChoice
	started time.Time
	ended   time.Time
}

// runRecord is one claude invocation, written when it ends whatever its
// status. Every line is self-describing — the configuration under test is
// snapshotted into it — so a file can be analysed without knowing which flags
// produced which rows.
type runRecord struct {
	V     int    `json:"v"`
	Kind  string `json:"kind"`
	TS    string `json:"ts"`
	Ended string `json:"ended"`

	// Shift is the process that wrote this record. Every record one shift
	// writes carries the same id and no two drains share one, which is what
	// makes "what did last night's batch do" answerable at all: records from
	// overlapping and back-to-back drains interleave in one file, and a time
	// window cannot separate them.
	Shift string `json:"shift"`

	Repo  string `json:"repo"`
	Issue int    `json:"issue"`
	PR    int    `json:"pr"`

	Reason  string `json:"reason"`
	Attempt int    `json:"attempt"`
	Session string `json:"session"`
	// ResumedFrom is the session id this run passed to --resume: the resume
	// target, a fact about the invocation and not about the session it
	// produced. Today's CLI keeps the session id across a resume, so it is
	// always equal to Session whenever it is set (non-empty exactly when
	// Reason is "resume" or "unfinished"); it would diverge only against a CLI
	// that forks a new session on resume rather than continuing the old one —
	// which is also the change that would turn the sum below into a wrong one,
	// so this is the field that would notice. Regardless: a --resume'd result
	// event reports that invocation rather than the session (settled on issue
	// #78), so rows are summed exactly as written.
	ResumedFrom string `json:"resumed_from"`

	Status   string `json:"status"`
	Subtype  string `json:"subtype"`
	ExitCode int    `json:"exit_code"`
	Outcome  string `json:"outcome"`

	Turns    int   `json:"turns"`
	ToolUses int   `json:"tool_uses"`
	WallMS   int64 `json:"wall_ms"`
	APIMS    int64 `json:"api_ms"`

	CostUSD     float64                `json:"cost_usd"`
	UsageSource string                 `json:"usage_source"`
	Tokens      tokenCounts            `json:"tokens"`
	ModelUsage  map[string]modelTokens `json:"model_usage,omitempty"`

	Model           string `json:"model"`
	RequestedModel  string `json:"requested_model"`
	RequestedEffort string `json:"requested_effort"`
	// Where RequestedModel / RequestedEffort came from — one of the source
	// constants in policy.go (label, epic, remediation, size, flag, inherit).
	// Drain run records only: proposalRunHead carries neither, so plan and
	// health records are unchanged.
	ModelSource    string `json:"model_source"`
	EffortSource   string `json:"effort_source"`
	Skill          string `json:"skill"`
	PermissionMode string `json:"permission_mode"`
	Tag            string `json:"tag"`
	ToolsHash      string `json:"tools_hash"`

	PollS      int `json:"poll_s"`
	Retries    int `json:"retries"`
	RetryWaitS int `json:"retry_wait_s"`
	StallS     int `json:"stall_s"`
	// The spend caps, omitted while they are off — which is the default, so an
	// uncapped drain writes the line it always wrote. Present, they are what
	// explains a `budget` status or a run that is the last one on its issue.
	MaxCostUSD        float64 `json:"max_cost_usd,omitempty"`
	MaxIssueTimeS     int     `json:"max_issue_time_s,omitempty"`
	MaxSessionCostUSD float64 `json:"max_session_cost_usd,omitempty"`

	PolakoVersion string `json:"polako_version"`
	ClaudeVersion string `json:"claude_version"`
	PluginVersion string `json:"plugin_version"`
}

// issueRecord marks an issue reaching a terminal state. It deliberately holds
// only what run records cannot derive: everything summable — cost per issue,
// runs per issue, crashes, question rounds — is computed from run records at
// read time, which is what keeps the supervisor free of rollup state it would
// otherwise have to keep across restarts.
type issueRecord struct {
	V       int    `json:"v"`
	Kind    string `json:"kind"`
	TS      string `json:"ts"`
	Shift   string `json:"shift"`
	Repo    string `json:"repo"`
	Issue   int    `json:"issue"`
	PR      int    `json:"pr"`
	Outcome string `json:"outcome"`
	Tag     string `json:"tag"`

	// Why it was handed back, on issueNeedsHuman records alone: one of the
	// park reasons above, never the park's message. Absent everywhere else,
	// and absent on every record written before the field existed — which is
	// why parkUnknown is a value rather than an omission.
	ParkReason string `json:"park_reason,omitempty"`

	// What GitHub knew about the PR when the issue ended, folded in at
	// terminal state. Absent when there was no PR, when -metrics is off, or
	// when the lookup failed: the outcome is recorded either way, since
	// dropping a record over a failed enrichment would lose exactly the
	// failures this dataset exists to keep.
	Additions    int    `json:"additions,omitempty"`
	Deletions    int    `json:"deletions,omitempty"`
	ChangedFiles int    `json:"changed_files,omitempty"`
	Reviews      int    `json:"reviews,omitempty"`
	PROpened     string `json:"pr_opened,omitempty"`
	PRMerged     string `json:"pr_merged,omitempty"`

	// The plan's own week-usage percent, sampled as this issue was picked up
	// and again as it reached this terminal state — what the ledger needs to
	// say what the issue cost the plan. Omitted, same as the enrichment
	// above, whenever the usage gate was off or a probe along the way could
	// not answer; 0 and "not sampled" are indistinguishable here, the same
	// trade the fields above already make.
	WeekUsageAtPickup   int `json:"week_usage_at_pickup_pct,omitempty"`
	WeekUsageAtTerminal int `json:"week_usage_at_terminal_pct,omitempty"`
}

// issueUsageSamples is the plan's week-usage percent read at an issue's
// pickup and again at its terminal state, threaded through recordIssue
// separately from prFacts because it comes off the usage probe rather than
// off GitHub.
type issueUsageSamples struct {
	atPickup    int
	hasPickup   bool
	atTerminal  int
	hasTerminal bool
}

// prFacts is what GitHub knows about a PR that the event stream cannot: how
// large the change turned out to be, how much review it drew, and the two
// authoritative timestamps for how long it sat open. Reviews are counted,
// never quoted — the same rule the rest of these records keep.
type prFacts struct {
	Additions    int
	Deletions    int
	ChangedFiles int
	Reviews      int
	Opened       string
	Merged       string
}

// issueTally is what one process saw while working one issue: the numbers
// -post-summary reports. It is not state — nothing reads it back once the
// process ends, and a supervisor restarted mid-issue simply starts a fresh
// one. That is why the comment it feeds says it covers this drain, rather
// than claiming to cover the issue's whole history.
type issueTally struct {
	runs         int
	questions    int
	approximated int // runs whose numbers came from the streamed tally
	costUSD      float64
	tokens       tokenCounts
	wallMS       int64
}

func (t *issueTally) add(rec runRecord) {
	t.runs++
	if rec.Outcome == outcomeQuestions {
		t.questions++
	}
	if rec.UsageSource == usageObserved {
		t.approximated++
	}
	// Summing holds even when two of these records are the two halves of one
	// resumed session: a --resume'd result event reports that process's spend
	// alone (measured, issue #258 — see stream.go's result case). #258 first
	// proposed tracking per-session deltas here instead; that would have
	// undercounted every resume by the cost of the resumed process.
	t.costUSD += rec.CostUSD
	t.tokens.addCounts(rec.Tokens)
	t.wallMS += rec.WallMS
}

// summaryComment is the body -post-summary posts: one line of numbers for the
// work behind a merged PR, and a footnote saying where they came from. Numbers
// only, like every record — but this is the one line of them anybody other
// than the operator ever sees, so it says what it covers and what it does not.
func summaryComment(t issueTally) string {
	tool := pluginName
	if v := polakoVersion(); v != "" {
		tool += " " + v
	}
	// A run that crashed, stalled or was interrupted never emitted a result
	// event: its tokens are the tally seen streaming past, and its cost is
	// zero because pricing belongs to the CLI and this binary never guesses at
	// it. Saying so matters more here than in a local report — this line is
	// read by people who did not watch the drain, and an unqualified $0.00
	// reads as a PR that was free.
	caveat := ""
	if t.approximated > 0 {
		caveat = fmt.Sprintf(" %d of them never reported a cost (crash, stall or interrupt), "+
			"so tokens and dollars are undercounts.", t.approximated)
	}
	return fmt.Sprintf("**%s** — %s, %s, %s tokens, %s, %s of run time.\n\n"+
		"<sub>Recorded by %s, covering the runs this shift supervised.%s "+
		"Dollars are the Claude CLI's API-equivalent pricing.</sub>",
		pluginName, plural(t.runs, "run"), plural(t.questions, "question round"),
		count(t.tokens.total()), usd(t.costUSD),
		dur(time.Duration(t.wallMS)*time.Millisecond), tool, caveat)
}

// newRunRecord folds one run's report together with the supervisor's context.
func newRunRecord(cfg config, rc runContext, rep runReport) runRecord {
	rec := runRecord{
		V:     recordVersion,
		Kind:  "run",
		TS:    stamp(rc.started),
		Ended: stamp(rc.ended),

		Shift: cfg.shiftID,
		Repo:  cfg.repo,
		Issue: rc.issue,
		PR:    rc.pr,

		Reason:      rc.reason,
		Attempt:     rc.attempt,
		Session:     rep.sessionID,
		ResumedFrom: rc.resumedFrom,

		Status:   rep.status(),
		Subtype:  rep.subtype,
		ExitCode: rep.exitCode,
		Outcome:  rc.outcome,

		Turns:    rep.turns,
		ToolUses: rep.toolUses,
		WallMS:   rep.wallMS,
		APIMS:    rep.apiMS,

		CostUSD:     rep.costUSD,
		UsageSource: usageResult,
		Tokens:      rep.usage,
		ModelUsage:  rep.modelUsage,

		Model:           rep.model,
		RequestedModel:  rc.model,
		RequestedEffort: rc.effort,
		ModelSource:     rc.modelSource,
		EffortSource:    rc.effortSource,
		Skill:           cfg.skill,
		PermissionMode:  cfg.permissionMode,
		Tag:             cfg.tag,
		ToolsHash:       toolsHash(resolveTools(cfg.tools, cfg.addTools)),

		PollS:      seconds(cfg.poll),
		Retries:    cfg.retries,
		RetryWaitS: seconds(cfg.retryWait),
		StallS:     seconds(cfg.stall),

		MaxCostUSD:        cfg.maxCost,
		MaxIssueTimeS:     seconds(cfg.maxIssueTime),
		MaxSessionCostUSD: cfg.maxSessionCost,

		PolakoVersion: polakoVersion(),
		ClaudeVersion: cfg.claudeVersion,
		PluginVersion: cfg.pluginVersion,
	}
	// No result event: the run died mid-flight. Report what was seen going
	// past, flagged as the approximation it is, and time it from the clock
	// rather than from a duration nobody sent.
	if !rep.hasResult {
		rec.UsageSource = usageObserved
		rec.Turns = rep.observedTurns
		rec.WallMS = rc.ended.Sub(rc.started).Milliseconds()
	}
	// A result can arrive carrying no usage block at all — the same way older
	// CLIs omit modelUsage, and the way some error subtypes report nothing.
	// Recording that as an authoritative zero is the one mistake that would
	// bias the whole dataset: it prices the runs that fail at nothing, so
	// whichever configuration fails most reads as the cheapest.
	if rec.Tokens == (tokenCounts{}) {
		rec.UsageSource = usageObserved
		rec.Tokens = rep.observed
	}
	return rec
}

// proposalFacts is what a `plan` and a `health` run's own context share: what
// the enforcing label pass created and corrected, the -max-issues cap, and the
// run's clock bounds (taken before the label pass, so wall time is the run's
// own). Counts only — never an issue's title or body, the standing recorder
// rule. planFacts adds what the run planned from; healthFacts adds nothing yet.
type proposalFacts struct {
	issuesCreated  int
	epicsCreated   int
	cap            int
	labelsEnforced int
	started        time.Time
	ended          time.Time
}

// planFacts is what `polako plan` knows about a run that the event stream
// cannot: on top of proposalFacts, what it was planning from and the batch
// milestone. `vision` is the -vision path or the literal "(brief)": a path the
// operator typed is fine, the brief's own text is not.
type planFacts struct {
	proposalFacts
	vision    string
	milestone string
}

// proposalRunHead is the part of a plan or health record that is a runRecord's
// self-describing stream numbers and configuration snapshot, verbatim, minus
// every field a run that works no issue has no meaning for — the per-issue
// ones (issue/pr/reason/attempt/session) and the drain's strategy knobs.
// requested_model and requested_effort are kept: plan and health take a real
// -model/-effort (defaulting to opus), so what they asked for is worth pricing
// a batch by, same as for a drain run. Embedded in both records so a new stream
// or config field is added here once.
//
// It is split from proposalRunTail rather than being one embed because a plan
// record slots vision and milestone between the two — health adds nothing
// there. Go inlines an embedded struct's fields into the JSON object at the
// embed's position, so both records still marshal byte-for-byte identical to
// their old flat form: that identity is the whole reason this is safe, and
// TestPlanAndHealthRecordsMarshalUnchanged pins it.
type proposalRunHead struct {
	V     int    `json:"v"`
	Kind  string `json:"kind"`
	TS    string `json:"ts"`
	Ended string `json:"ended"`
	Shift string `json:"shift"`
	Repo  string `json:"repo"`

	Status   string `json:"status"`
	ExitCode int    `json:"exit_code"`

	Turns    int   `json:"turns"`
	ToolUses int   `json:"tool_uses"`
	WallMS   int64 `json:"wall_ms"`
	APIMS    int64 `json:"api_ms"`

	CostUSD     float64                `json:"cost_usd"`
	UsageSource string                 `json:"usage_source"`
	Tokens      tokenCounts            `json:"tokens"`
	ModelUsage  map[string]modelTokens `json:"model_usage,omitempty"`

	Model           string `json:"model"`
	RequestedModel  string `json:"requested_model"`
	RequestedEffort string `json:"requested_effort"`
	Skill           string `json:"skill"`
	PermissionMode  string `json:"permission_mode"`
	Tag             string `json:"tag"`
	ToolsHash       string `json:"tools_hash"`
}

// proposalRunTail is the part both records share after their own middle
// fields: what the label pass created and corrected, then the three build
// stamps. See proposalRunHead for why the pair is two embeds.
type proposalRunTail struct {
	IssuesCreated  int `json:"issues_created"`
	EpicsCreated   int `json:"epics_created"`
	Cap            int `json:"cap"`
	LabelsEnforced int `json:"labels_enforced"`

	PolakoVersion string `json:"polako_version"`
	ClaudeVersion string `json:"claude_version"`
	PluginVersion string `json:"plugin_version"`
}

// planRecord is one `polako plan` run, written when it ends whatever its
// status — a clean finish, the -max-issues cap, a crash, a Ctrl+C. Additive:
// readers ignore unknown kinds, so `stats` skips it until a later change
// teaches it to read one.
type planRecord struct {
	proposalRunHead
	// What the run planned from — plan's alone; health has no document.
	Vision    string `json:"vision"`
	Milestone string `json:"milestone"`
	proposalRunTail
}

// healthRecord is one `polako health` run, written when it ends whatever its
// status. planRecord's twin, minus Vision/Milestone.
type healthRecord struct {
	proposalRunHead
	proposalRunTail
}

// proposalHead copies the run-stream numbers and config snapshot off
// newRunRecord's result. kind is the record's own — "plan" or "health", not
// newRunRecord's "run".
func proposalHead(base runRecord, kind string) proposalRunHead {
	return proposalRunHead{
		V:     recordVersion,
		Kind:  kind,
		TS:    base.TS,
		Ended: base.Ended,
		Shift: base.Shift,
		Repo:  base.Repo,

		Status:   base.Status,
		ExitCode: base.ExitCode,

		Turns:    base.Turns,
		ToolUses: base.ToolUses,
		WallMS:   base.WallMS,
		APIMS:    base.APIMS,

		CostUSD:     base.CostUSD,
		UsageSource: base.UsageSource,
		Tokens:      base.Tokens,
		ModelUsage:  base.ModelUsage,

		Model:           base.Model,
		RequestedModel:  base.RequestedModel,
		RequestedEffort: base.RequestedEffort,
		Skill:           base.Skill,
		PermissionMode:  base.PermissionMode,
		Tag:             base.Tag,
		ToolsHash:       base.ToolsHash,
	}
}

// proposalTail copies the label-pass counts off proposalFacts and the build
// stamps off newRunRecord's result.
func proposalTail(base runRecord, pf proposalFacts) proposalRunTail {
	return proposalRunTail{
		IssuesCreated:  pf.issuesCreated,
		EpicsCreated:   pf.epicsCreated,
		Cap:            pf.cap,
		LabelsEnforced: pf.labelsEnforced,

		PolakoVersion: base.PolakoVersion,
		ClaudeVersion: base.ClaudeVersion,
		PluginVersion: base.PluginVersion,
	}
}

// newPlanRecord folds a plan run's report together with what `polako plan`
// learned around it. The stream half is built through newRunRecord, so the
// observed-usage fallback for a run the cap or a crash cut off before it
// reported one is shared rather than written twice; only the plan context and
// the fields a drained issue needs but a plan run does not differ.
func newPlanRecord(cfg config, rep runReport, pf planFacts) planRecord {
	// model/effort so requested_model / requested_effort stay right — plan
	// takes a real -model/-effort. The sources are left unset: proposalRunHead
	// carries neither model_source nor effort_source (the AC scopes those to
	// drain records), so the plan record's JSON is unchanged either way.
	base := newRunRecord(cfg, runContext{
		runChoice: runChoice{model: cfg.model, effort: cfg.effort},
		started:   pf.started, ended: pf.ended,
	}, rep)
	return planRecord{
		proposalRunHead: proposalHead(base, "plan"),
		Vision:          pf.vision,
		Milestone:       pf.milestone,
		proposalRunTail: proposalTail(base, pf.proposalFacts),
	}
}

// healthFacts is planFacts' twin for `polako health`: no vision or milestone —
// review-health plans from the repository itself, not a document, and attaches
// no milestone — and, like planFacts omits the brief's text, this omits
// -focus's: never document content, the standing recorder rule.
type healthFacts struct {
	proposalFacts
}

// newHealthRecord folds a health run's report together with what `polako
// health` learned around it, the same way newPlanRecord does for plan.
func newHealthRecord(cfg config, rep runReport, hf healthFacts) healthRecord {
	// model/effort for requested_model / requested_effort, sources left unset —
	// see newPlanRecord.
	base := newRunRecord(cfg, runContext{
		runChoice: runChoice{model: cfg.model, effort: cfg.effort},
		started:   hf.started, ended: hf.ended,
	}, rep)
	return healthRecord{
		proposalRunHead: proposalHead(base, "health"),
		proposalRunTail: proposalTail(base, hf.proposalFacts),
	}
}

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// seconds renders a flag duration for the record. Whole seconds: these are
// strategy knobs measured in minutes, and the field exists to be grouped by.
func seconds(d time.Duration) int { return int(d / time.Second) }

// toolsHash identifies an allowlist without carrying it. Comparisons only ever
// ask whether two runs used the same set, and the full list would triple the
// length of every line.
func toolsHash(tools string) string {
	h := fnv.New32a()
	h.Write([]byte(tools))
	return fmt.Sprintf("%08x", h.Sum32())
}

// polakoVersion is the release tag when the binary was stamped at build time,
// the module version when installed with `go install`, and the short VCS
// revision when built from a clone. Empty if the binary carries none of them —
// a `go run` of the package, or a test.
//
// The stamp comes first because it is the only one a cross-compiled release
// binary has: `go build` from a checkout records the revision but leaves the
// module version at "(devel)", so without it every published binary would
// report a bare SHA and no run could be attributed to a release.
var polakoVersion = sync.OnceValue(func() string {
	if version != "" {
		return version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	if v := info.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	rev, dirty := "", false
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	if rev != "" && dirty {
		return rev + "+dirty"
	}
	return rev
})

// recorder appends records to one file per repository. The zero value, and a
// nil *recorder, are both valid and write nothing — so a config built without
// one (every test that does not care) is safe to hand to any caller.
type recorder struct {
	dir    string // empty disables every write
	warned bool   // one warning per process: unattended logs are a cost too
}

// newRecorder resolves the -metrics flag: a directory, or "off". The default
// location is one documented path on every platform, deliberately outside any
// checkout — the skill commits things there, and cost data must not become
// committable by accident.
func newRecorder(spec string) *recorder {
	return &recorder{dir: resolveDataDir(spec, "metrics", "metrics", "to record run data in")}
}

// resolveDataDir resolves one "directory or off" flag — -metrics, -log — in
// one place, so the two cannot drift apart on the off-spelling, the home-dir
// fallback or the shape of the warning. An empty result means off; noun says
// what is being lost when there is no home directory to fall back to.
func resolveDataDir(spec, sub, flagName, noun string) string {
	dir := strings.TrimSpace(spec)
	if strings.EqualFold(dir, metricsOff) {
		return ""
	}
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			log.Printf("no home directory %s (%v) — continuing without it; "+
				"pass -%s <dir> to choose a location, or -%s off to stop asking", noun, err, flagName, flagName)
			return ""
		}
		dir = filepath.Join(home, ".polako", sub)
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	return dir
}

// defaultMetricsDir is where records live unless -metrics says otherwise, and
// where `stats` looks for them. One documented path on every platform beats
// per-OS idiom for a tool whose README promises "here is everything we write".
func defaultMetricsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".polako", "metrics"), nil
}

func (r *recorder) enabled() bool { return r != nil && r.dir != "" }

// metricsDir is the directory the plan report's pricing line reads records
// back from — the same one writes go to. Empty when writes are disabled
// (-metrics off, or no home directory to fall back to), which the pricing
// line takes as "nothing to read, and no file opened to find out".
func (r *recorder) metricsDir() string {
	if r == nil {
		return ""
	}
	return r.dir
}

// recordRun returns the record it wrote, so a caller can tally the same
// numbers it recorded. The record is built whether or not anything is written:
// -post-summary works with -metrics off, which is the escape hatch for an
// operator who wants no local files at all.
func (r *recorder) recordRun(cfg config, rc runContext, rep runReport) runRecord {
	rec := newRunRecord(cfg, rc, rep)
	r.append(cfg.repo, rec)
	return rec
}

// recordPlan writes the one record a `polako plan` run leaves: a line of
// numbers about the run and what the label pass did around it. Best-effort
// like the rest — a nil or -metrics-off recorder writes nothing.
func (r *recorder) recordPlan(cfg config, rep runReport, pf planFacts) {
	r.append(cfg.repo, newPlanRecord(cfg, rep, pf))
}

// recordHealth writes the one record a `polako health` run leaves. recordPlan's twin.
func (r *recorder) recordHealth(cfg config, rep runReport, hf healthFacts) {
	r.append(cfg.repo, newHealthRecord(cfg, rep, hf))
}

// recordIssue writes the terminal record. why is the park reason, and this is
// the one place the rule about it is kept: it is written for a hand-back and
// nowhere else, and a hand-back that named nothing records parkUnknown — so a
// missing field can only ever mean a record older than the field, and never a
// park nobody categorised.
func (r *recorder) recordIssue(cfg config, issue, pr int, outcome, why string, facts prFacts, usage issueUsageSamples) {
	if outcome != issueNeedsHuman {
		why = ""
	} else if why == "" {
		why = parkUnknown
	}
	rec := issueRecord{
		V:          recordVersion,
		Kind:       "issue",
		TS:         stamp(time.Now()),
		Shift:      cfg.shiftID,
		Repo:       cfg.repo,
		Issue:      issue,
		PR:         pr,
		Outcome:    outcome,
		Tag:        cfg.tag,
		ParkReason: why,

		Additions:    facts.Additions,
		Deletions:    facts.Deletions,
		ChangedFiles: facts.ChangedFiles,
		Reviews:      facts.Reviews,
		PROpened:     facts.Opened,
		PRMerged:     facts.Merged,
	}
	if usage.hasPickup {
		rec.WeekUsageAtPickup = usage.atPickup
	}
	if usage.hasTerminal {
		rec.WeekUsageAtTerminal = usage.atTerminal
	}
	r.append(cfg.repo, rec)
}

// append writes one record as one line. Every failure path warns at most once
// and returns: losing a metric must never fail a run, and a directory that
// vanished mid-drain is recreated on the next write rather than complained
// about on every one.
func (r *recorder) append(repo string, rec any) {
	if !r.enabled() {
		return
	}
	line, err := json.Marshal(rec)
	if err != nil {
		r.warn(err)
		return
	}
	// 0o700/0o600: on a shared machine this is which private repositories the
	// operator drains and what each one cost. The README promises it stays
	// private, and a default umask would not.
	if err := os.MkdirAll(r.dir, 0o700); err != nil {
		r.warn(err)
		return
	}
	// O_APPEND, no locking: one short line per write lands atomically at these
	// sizes on both platforms, so concurrent supervisors interleave whole
	// lines rather than tearing one.
	f, err := os.OpenFile(filepath.Join(r.dir, recordFile(repo)),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		r.warn(err)
		return
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		r.warn(err)
		return
	}
	if err := f.Close(); err != nil {
		r.warn(err)
	}
}

func (r *recorder) warn(err error) {
	if r.warned {
		return
	}
	r.warned = true
	narrate(sevWarning, "run data not recorded (%v) — the shift continues; -metrics off silences this", err)
}

// recordFile partitions records one file per repository, so deleting one
// project's data is `rm` on one file and aggregating across projects is a glob.
// The name is only partitioning: the repo field inside each record is
// authoritative, and nothing ever parses a filename back.
func recordFile(repo string) string {
	return repoSlug(repo) + ".jsonl"
}

// repoSlug flattens owner/repo into one filesystem-safe name, shared with the
// shift log so the two artifacts for one repository sort together.
func repoSlug(repo string) string {
	slug := strings.ReplaceAll(strings.TrimSpace(repo), "/", "--")
	slug = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '-', r == '_', r == '.':
			return r
		}
		return '_'
	}, slug)
	if slug == "" {
		slug = "unknown"
	}
	return slug
}
