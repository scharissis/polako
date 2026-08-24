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
// Nothing here leaves the machine. There is no network path out of this file.

import (
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

	DrainVersion  string `json:"drain_version"`
	ClaudeVersion string `json:"claude_version"`
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
	Repo    string `json:"repo"`
	Issue   int    `json:"issue"`
	PR      int    `json:"pr"`
	Outcome string `json:"outcome"`
	Tag     string `json:"tag"`
}

// newRunRecord folds one run's report together with the supervisor's context.
func newRunRecord(cfg config, rc runContext, rep runReport) runRecord {
	rec := runRecord{
		V:     recordVersion,
		Kind:  "run",
		TS:    stamp(rc.started),
		Ended: stamp(rc.ended),

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

		DrainVersion:  drainVersion(),
		ClaudeVersion: cfg.claudeVersion,
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

// drainVersion is the module version when installed with `go install`, and the
// short VCS revision when built from a clone. Empty if the binary carries
// neither — a `go run` of the package, or a test.
var drainVersion = sync.OnceValue(func() string {
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
		home, err := os.UserHomeDir()
		if err != nil {
			log.Printf("no home directory to record run data in (%v) — continuing without it; "+
				"pass -metrics <dir> to choose a location, or -metrics off to stop asking", err)
			return &recorder{}
		}
		dir = filepath.Join(home, ".backlog-drain", "metrics")
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	return &recorder{dir: dir}
}

func (r *recorder) enabled() bool { return r != nil && r.dir != "" }

func (r *recorder) recordRun(cfg config, rc runContext, rep runReport) {
	r.append(cfg.repo, newRunRecord(cfg, rc, rep))
}

func (r *recorder) recordIssue(cfg config, issue, pr int, outcome string) {
	r.append(cfg.repo, issueRecord{
		V:       recordVersion,
		Kind:    "issue",
		TS:      stamp(time.Now()),
		Repo:    cfg.repo,
		Issue:   issue,
		PR:      pr,
		Outcome: outcome,
		Tag:     cfg.tag,
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
	log.Printf("run data not recorded (%v) — the drain continues; -metrics off silences this", err)
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
