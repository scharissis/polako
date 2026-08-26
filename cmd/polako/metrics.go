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
	reasonAnswers   = "answers"   // fresh re-run after a human replied on the thread
	reasonRemediate = "remediate" // conflict remediation while a PR is open
	reasonChecks    = "checks"    // CI remediation while a PR is open
	reasonReview    = "review"    // answering a reviewer who asked for changes
)

// What a run left behind. A remediation run pushes to a PR that already
// exists, so it leaves behind neither a new PR nor questions, and records
// outcomeNothing. outcomeUnknown covers the narrow case where the run ended
// but the GitHub call that would have told us what it produced failed — a
// record saying "we could not look" beats a missing one, which would quietly
// bias every rate computed from these files.
const (
	outcomeOpenedPR  = "opened_pr"
	outcomeQuestions = "posted_questions"
	outcomeNothing   = "nothing"
	outcomeUnknown   = "unknown"
)

// How an issue ended. Failures are the most valuable rows in the dataset, so
// the "needs a human" exits record one too.
const (
	issueMerged     = "merged"
	issueClosed     = "closed_unmerged"
	issueNeedsHuman = "needs_human"
)

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
	started     time.Time
	ended       time.Time
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
	// ResumedFrom chains a resumed run back to the session it continued.
	// Unverified against the real CLI: if a --resume'd result event reports
	// cost cumulatively for the session rather than per invocation, summing
	// these rows double-counts, and this chain is what a reader needs to take
	// a per-session maximum instead. Worth settling before anything sums them.
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

	Model          string `json:"model"`
	RequestedModel string `json:"requested_model"`
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

		Model:          rep.model,
		RequestedModel: cfg.model,
		Skill:          cfg.skill,
		PermissionMode: cfg.permissionMode,
		Tag:            cfg.tag,
		ToolsHash:      toolsHash(resolveTools(cfg.tools, cfg.addTools)),

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
	dir := strings.TrimSpace(spec)
	if strings.EqualFold(dir, metricsOff) {
		return &recorder{}
	}
	if dir == "" {
		dflt, err := defaultMetricsDir()
		if err != nil {
			log.Printf("no home directory to record run data in (%v) — continuing without it; "+
				"pass -metrics <dir> to choose a location, or -metrics off to stop asking", err)
			return &recorder{}
		}
		dir = dflt
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	return &recorder{dir: dir}
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

// recordRun returns the record it wrote, so a caller can tally the same
// numbers it recorded. The record is built whether or not anything is written:
// -post-summary works with -metrics off, which is the escape hatch for an
// operator who wants no local files at all.
func (r *recorder) recordRun(cfg config, rc runContext, rep runReport) runRecord {
	rec := newRunRecord(cfg, rc, rep)
	r.append(cfg.repo, rec)
	return rec
}

func (r *recorder) recordIssue(cfg config, issue, pr int, outcome string, facts prFacts) {
	r.append(cfg.repo, issueRecord{
		V:       recordVersion,
		Kind:    "issue",
		TS:      stamp(time.Now()),
		Shift:   cfg.shiftID,
		Repo:    cfg.repo,
		Issue:   issue,
		PR:      pr,
		Outcome: outcome,
		Tag:     cfg.tag,

		Additions:    facts.Additions,
		Deletions:    facts.Deletions,
		ChangedFiles: facts.ChangedFiles,
		Reviews:      facts.Reviews,
		PROpened:     facts.Opened,
		PRMerged:     facts.Merged,
	})
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
	log.Printf("run data not recorded (%v) — the shift continues; -metrics off silences this", err)
}

// recordFile partitions records one file per repository, so deleting one
// project's data is `rm` on one file and aggregating across projects is a glob.
// The name is only partitioning: the repo field inside each record is
// authoritative, and nothing ever parses a filename back.
func recordFile(repo string) string {
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
	return slug + ".jsonl"
}
