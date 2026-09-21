package main

// Reading a run's final words when it produced no PR: whether the session lacks
// the skill the prompt names, whether the CLI refused the credentials or the
// usage limit, and whether the run (or the CLI) stopped to ask for a permission
// this allowlist never granted. Head-anchored throughout — an issue that quotes
// one of these messages is the likelier false positive — and limitReset turns a
// usage refusal's clock into the instant to resume at.

import (
	"encoding/json"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// lacksCommand reports whether an init event's command inventory is present
// and cmd is missing from it. An absent inventory (CLIs before 2.1.85) is no
// evidence either way, and entries are compared with any leading slash
// stripped: a wrong "missing" verdict kills a healthy run, so every
// uncertainty has to resolve toward "found".
func lacksCommand(commands []string, cmd string) bool {
	if cmd == "" || len(commands) == 0 {
		return false
	}
	return !slices.ContainsFunc(commands, func(c string) bool {
		return strings.TrimPrefix(c, "/") == cmd
	})
}

// nearMatches returns inventory entries that differ from cmd only by plugin
// namespacing — the exact confusion the missing-skill error warns about, so
// naming the spelling the session does have turns that warning into a fix.
func nearMatches(commands []string, cmd string) []string {
	tail := func(s string) string {
		if i := strings.LastIndexByte(s, ':'); i >= 0 {
			return s[i+1:]
		}
		return s
	}
	var near []string
	for _, c := range commands {
		if c = strings.TrimPrefix(c, "/"); tail(c) == tail(cmd) {
			near = append(near, "/"+c)
		}
	}
	return near
}

// resultHead reduces a result event's text to what authFailure, limitRefusal
// and permissionRefusal each match against: lowercased, and stripped of the
// markdown a CLI or a model sometimes wraps its own text in — a heading, a
// bullet (`*` or `-`), stray spaces — so that wrapping does not by itself
// defeat a head anchor.
func resultHead(result string) string {
	return strings.ToLower(strings.TrimLeft(strings.TrimSpace(result), "*#- "))
}

// headMatchesAny reports whether result's head starts with one of sigs, as a
// whole word or phrase rather than a raw byte prefix: "i need permission to"
// must not match "I need permission tooling wasn't available", so whatever
// character follows a matched signature — if the head continues at all — has
// to end the phrase (space, punctuation, ...) rather than continue a word.
func headMatchesAny(result string, sigs ...string) bool {
	head := resultHead(result)
	return slices.ContainsFunc(sigs, func(sig string) bool {
		if !strings.HasPrefix(head, sig) {
			return false
		}
		rest := head[len(sig):]
		if rest == "" {
			return true
		}
		c := rest[0]
		return !(c >= 'a' && c <= 'z') && !(c >= '0' && c <= '9')
	})
}

// authFailure reports whether a run's final text is the CLI saying the API
// refused its credentials.
//
// The match is anchored to the head of the message, not merely contained in
// it, because a run that *quotes* a 401 is the likelier sight on this repo's
// own backlog: an issue about OAuth, a run that then hits max turns, and a
// final message repeating the error out of the issue body. Refusing that run
// a retry and stopping the drain over a healthy token is a worse failure than
// missing an unrecognised wrapper — which costs only the retries this code
// already spent before. So, as with lacksCommand, every uncertainty resolves
// toward "keep going".
func authFailure(result string) bool {
	return headMatchesAny(result,
		"failed to authenticate",        // the CLI's own wrapper, and the one observed
		"oauth token has expired",       // a credential it could not refresh
		"oauth access token is invalid", // a revoked or corrupt stored one
		"invalid api key",               // the ANTHROPIC_API_KEY spellings
		"invalid x-api-key",
		"api error: 401",       // the bare status, when no wrapper survives
		"authentication_error", // the raw API envelope, unwrapped
	)
}

// limitRefusal reports whether a failing run's result text is the CLI refusing
// to work over the account's usage limit. Head-anchored for the same reason
// authFailure is, and here the quoting risk is not hypothetical: issue #67 on
// this very repository carries these messages verbatim in its body, so a run
// implementing it could end with a final message that merely repeats one. The
// residual mis-read costs one bounded wait rather than a park or a stopped
// drain, which is the cheap side of the same trade authFailure makes.
func limitRefusal(result string) bool {
	return headMatchesAny(result,
		"you've hit your session limit", // the CLI's wording, and the one observed
		"you've hit your usage limit",   // its sibling for the account-wide pools
		"session limit reached",         // shorter spellings, defensively
		"usage limit reached",
		"5-hour limit reached",
		"weekly limit reached",
	)
}

// permissionParkReason is the park message both permission paths share —
// permissionRefused, where the result text itself was the ask and the issue
// parks without a resume, and permissionAsked, where an earlier turn was and
// the issue parks after any resume has run its course. Either way the lever is
// the operator's, so this spells out where to find the specific tool (the
// terminal, right after this park, and the shift log resumeHint names beside
// it — never here, since the tool detail can carry a local absolute path) and
// what to do with it once found, rather than only reporting that something
// was refused.
const permissionParkReason = "the run stopped to ask for a permission this " +
	"allowlist does not grant. To fix it: find the tool it reached for — " +
	"named in the terminal right after this park, and saved in the shift " +
	"log named right after that — then either grant it with -add-tools " +
	"(for a Bash command, add an entry shaped like " +
	"`-add-tools \"Bash(<command>:*)\"`) and remove needs-human to retry, " +
	"or, if the skill should not have reached for that tool at all, fix the " +
	"skill instead"

// permissionRefusal reports whether a clean run's final text is the run itself
// asking the operator to approve a tool it was refused — the shape observed on
// issue #138, where `cd` and `EnterWorktree` both sat outside --allowedTools
// and the run ended its turn asking in prose instead of on the issue thread.
//
// #156 gave the skill a documented route to ask there instead — post the
// question and raise awaiting-answer — so a current skill run taking that
// route never reaches here at all: deferReason catches it earlier in
// processIssue's switch, over in the `asked` branch. #138 predates that
// route; this function is the backstop for what it does not cover once it
// exists — an older skill install (CLAUDE.md notes a version bump is the only
// thing that moves an installed user) and, more durably, a model that simply
// does not take the documented route on a given run.
//
// Unlike authFailure and limitRefusal this is not the CLI's own wrapper text —
// it is the model's own words, so the exact wording varies run to run and the
// signatures below cannot be exhaustive. Head-anchored for the same reason as
// both: an issue that discusses tool permissions and gets quoted back in a
// final message is the likelier false positive, so every uncertainty resolves
// toward "this was an ordinary run" and the list stays conservative rather
// than broad.
func permissionRefusal(result string) bool {
	return headMatchesAny(result, slices.Concat(permissionAskSignatures, []string{
		// "I lack permission for X" — an accurate description of a wall the
		// run hit. As a run's *final* words with no PR it reads the same as
		// the asks above; mid-turn it is as often the run narrating a
		// workaround ("i don't have permission to run the full suite here,
		// but ..."), so permissionAskMidRun leaves these out.
		"i need permission to",
		"i don't have permission to",
		"i do not have permission to",
	})...)
}

// permissionAskSignatures are the phrasings that read as the run stopping its
// turn to ask the operator to approve something — not merely reporting a
// missing permission. permissionRefusal adds the weaker "I lack permission"
// forms; permissionAskMidRun does not, because those turn up mid-run in prose
// that then works around the wall rather than stopping on it.
var permissionAskSignatures = []string{
	"this requires user confirmation", // the wording observed on #138
	"this requires confirmation",
	"this requires approval",
	"this requires your approval",
	"can you approve",
	"could you approve",
}

// permissionAskMidRun reports whether an assistant turn that is not the run's
// last word is nonetheless the run stopping to ask for approval — the shape
// #169 hit, where the ask ("This requires user confirmation to proceed") landed
// partway through and the run then wrapped up on a sentence permissionRefusal's
// head anchor could not catch. Same head anchor as permissionRefusal so a
// permissions issue quoted mid-sentence still does not match, but a narrower
// signature set: a mid-run turn saying only that it "does not have permission"
// is too often the run describing a wall it then goes around.
func permissionAskMidRun(text string) bool {
	return headMatchesAny(text, permissionAskSignatures...)
}

// sigMultipleOps and sigSimpleExpansion are named separately from
// toolRefusalSignatures' third member (the plain "this command requires
// approval" has no separate classification to share) because refusalKindOf
// below has to test for them individually — naming them once keeps the two
// switches from drifting apart on the exact wording.
const (
	sigMultipleOps     = "this bash command contains multiple operations"
	sigSimpleExpansion = "contains simple_expansion"
)

// toolRefusalSignatures are the CLI's own wrapper text for a tool_result the
// permission system refused outright — observed verbatim on issue #209
// (session 902c1c34-d4db-40cc-b00c-aa8f82242472): a plain "This command
// requires approval" for a single command, and, for a compound Bash command,
// "This Bash command contains multiple operations. The following parts
// require approval: ..." naming the parts. "Contains simple_expansion"
// joined this list on issue #430 (#390's own session): a `$VAR` in the
// command, refused for a reason no `-add-tools` entry fixes — the command has
// to be phrased differently — but still a refusal, so it still latches
// permissionRefused below. Unlike permissionAskSignatures this is CLI prose,
// not the model's, so — like authFailure and limitRefusal — it is trusted
// rather than treated as one phrasing among many.
var toolRefusalSignatures = []string{
	"this command requires approval",
	sigMultipleOps,
	sigSimpleExpansion,
}

// refusalKind tells apart a refusal a wider allowlist can fix from one no
// grant can — see refusalKindOf.
type refusalKind string

const (
	// refusalPlain is a whole single command the CLI refused outright —
	// "This command requires approval".
	refusalPlain refusalKind = "plain"
	// refusalPart is one named part of a compound Bash command the CLI
	// refused — "The following part(s) require approval: …" — one refusal
	// entry per part it named.
	refusalPart refusalKind = "part"
	// refusalUngrantable is "Contains simple_expansion": a $VAR in the
	// command. No -add-tools entry fixes it.
	refusalUngrantable refusalKind = "ungrantable"
)

// refusalKindOf classifies a refused tool_result's own text into which of
// toolRefusalSignatures matched. Only called once toolResultRefusal has
// already said this text is a refusal at all.
func refusalKindOf(text string) refusalKind {
	switch {
	case headMatchesAny(text, sigMultipleOps):
		return refusalPart
	case headMatchesAny(text, sigSimpleExpansion):
		return refusalUngrantable
	default:
		return refusalPlain
	}
}

// refusalPartsRe extracts the CLI's own comma-separated list of parts it
// refused out of a compound Bash command's refusal text — "The following
// part requires approval: X" or, naming more than one, "...parts require
// approval: X, Y".
var refusalPartsRe = regexp.MustCompile(`(?i)the following parts? requires? approval:\s*(.+)$`)

// refusalParts splits the CLI's own part list — its own text, not a shell
// parse; see splitCommand's (notify.go) own refusal to become one. Nil when
// the refusal text does not name any parts. The split is a plain ", ", so a
// part whose own text happens to contain ", " (a quoted string with a comma
// in it) splits wrong — the CLI's own text gives no escaping to parse
// against, so no client-side split can fully disambiguate it; a wrong split
// still yields refusalPart entries, just with the join point in the wrong
// place, and the raw refusal text always survives in the correlated
// tool_use's own command besides.
func refusalParts(text string) []string {
	m := refusalPartsRe.FindStringSubmatch(strings.TrimSpace(text))
	if m == nil {
		return nil
	}
	fields := strings.Split(m[1], ", ")
	parts := make([]string, 0, len(fields))
	for _, f := range fields {
		if f = strings.TrimSpace(f); f != "" {
			parts = append(parts, f)
		}
	}
	return parts
}

// refusal is one CLI-reported refusal of a tool call, kept in the CLI's own
// words: which tool it was (when correlated back to its tool_use), the
// command that tool_use carried, the specific part the CLI named for a
// compound-command refusal, and a kind telling a grantable refusal apart from
// one no grant fixes. Comparable, so addRefusals can dedup with slices.Index.
type refusal struct {
	tool    string
	command string
	part    string
	kind    refusalKind
}

// refusalCap bounds runReport.refusals so a run stuck looping on the same
// wall cannot grow it without bound. addRefusals evicts the oldest entry
// once this is reached, never the newest — a park reports what is actually
// still blocking the run, not whatever happened to arrive first.
const refusalCap = 20

// addRefusals appends new refusal entries, keeping refusals ordered by
// recency: a refusal identical to one already present is moved to the end
// rather than dropped in place, so lastRefusalDetail — which reads the last
// entry — always names the most recently occurring refusal, not the first
// time it happened. Capped at refusalCap by evicting the oldest entry, so a
// run stuck looping on the same wall cannot grow this without bound while
// the most recent refusals — the ones a park would actually report — are
// never the ones forgotten.
func (r *runReport) addRefusals(new []refusal) {
	for _, nr := range new {
		if i := slices.Index(r.refusals, nr); i >= 0 {
			r.refusals = append(r.refusals[:i], r.refusals[i+1:]...)
		}
		r.refusals = append(r.refusals, nr)
		if len(r.refusals) > refusalCap {
			r.refusals = r.refusals[len(r.refusals)-refusalCap:]
		}
	}
}

// newRefusals turns one refused tool_result into the refusal entries it
// names — more than one when the CLI's own text lists several parts of a
// compound Bash command (one entry per part), exactly one otherwise. tool and
// hadTool are the tool_use correlated by id, when the stream still had it
// pending; text is the tool_result's own content, kept as command when
// correlation failed — the same fallback permissionRefusedDetail used to
// fall back to. command is read through toolInputDetail — the same field
// list toolDetail renders for a log line, here unclipped — so a refused
// non-Bash tool (Read, Skill, WebFetch, …) still names its actual target
// instead of just the CLI's generic refusal text.
func newRefusals(tool pendingTool, hadTool bool, text string) []refusal {
	toolName, command := "", text
	if hadTool {
		toolName = tool.name
		if c, ok := toolInputDetail(tool.input); ok {
			command = c
		}
	}
	switch kind := refusalKindOf(text); kind {
	case refusalPart:
		parts := refusalParts(text)
		if len(parts) == 0 {
			// The CLI said "multiple operations" but this build could not
			// parse which parts — one entry rather than losing the refusal
			// entirely, still kind refusalPart: the CLI's own wording said
			// this was a compound-command refusal, and that classification
			// doesn't change just because this build couldn't split it.
			return []refusal{{tool: toolName, command: command, kind: kind}}
		}
		out := make([]refusal, len(parts))
		for i, p := range parts {
			out[i] = refusal{tool: toolName, command: command, part: p, kind: refusalPart}
		}
		return out
	default:
		return []refusal{{tool: toolName, command: command, kind: kind}}
	}
}

// refusalWorkedAround reports whether this run's permission refusal is issue
// #461's shape rather than #126's: the CLI refused a tool_result mid-run, the
// run kept going and completed further tool calls anyway, and its final word
// does not itself read as an ask. #126 is still caught — no successful call
// followed its refusal — and so is #138 (permissionRefused with no
// tool_result refusal at all: a final-message ask has nothing to work
// around).
func (r runReport) refusalWorkedAround() bool {
	return len(r.refusals) > 0 && r.toolSucceededAfterRefusal && !r.lastResultIsAsk
}

// lastRefusalDetail renders the most recent refusal the way a park still
// wants to read it today — "<tool>: <command>" when the refusal was
// correlated to a tool_use, or the tool_result's own text otherwise — the
// same rendering permissionRefusedDetail used to produce, minus the 120-char
// clip toolDetail applied for a log line (command is data here, not
// display). Turning the fuller record below into an -add-tools entry, and
// any change to what a park says, is ticket 2 and 3 of
// docs/plans/permission-parks.md; until then every caller that used to read
// permissionRefusedDetail reads this instead.
func (r runReport) lastRefusalDetail() string {
	if len(r.refusals) == 0 {
		return ""
	}
	last := r.refusals[len(r.refusals)-1]
	if last.tool == "" {
		return last.command
	}
	return last.tool + ": " + last.command
}

// toolResultRefusal reports whether a tool_result's content is the CLI
// itself refusing a command outside --allowedTools — the structural fact
// issue #209 classifies on, rather than the model's own retelling of it a
// turn or more later. Head-anchored for the same reason as authFailure: a
// tool_result that merely quotes this text (a grep hit, a file read) is the
// likelier false positive.
func toolResultRefusal(text string) bool {
	return headMatchesAny(text, toolRefusalSignatures...)
}

// addToolsKind explains why addToolsEntry returned no entry; the zero value
// means an entry was returned.
type addToolsKind string

const (
	// addToolsNever is a refusal matching neverGrantTable — a command
	// polako will not propose, whatever the refusal.
	addToolsNever addToolsKind = "never"
	// addToolsGranted is a refusal the given allowlist already covers.
	addToolsGranted addToolsKind = "granted"
	// addToolsAmbiguous is a Bash refusal with no CLI-named part and a `;`,
	// `|` or `&` in the source: no single word can be pulled out of it
	// safely, the same restraint splitCommand (notify.go) holds itself to.
	addToolsAmbiguous addToolsKind = "ambiguous"
	// addToolsUngrantable mirrors refusalUngrantable: a `$VAR` refusal no
	// -add-tools entry ever fixes — the command has to be phrased
	// differently.
	addToolsUngrantable addToolsKind = "ungrantable"
	// addToolsUnknown is a refusal whose tool_use never correlated, so
	// r.command holds the CLI's own refusal text rather than the command it
	// refused — nothing to derive a grant from.
	addToolsUnknown addToolsKind = "unknown"
)

// neverGrantTable lists gh subcommands addToolsEntry will not propose,
// whatever the refusal: each lets a run do something CLAUDE.md keeps to a
// human — merge its own PR, relabel or close an issue beyond the one grant
// its own dispatch is pinned to (issueLabelTools, issueCloseTool in
// claude.go), or reach gh api/secret/repo/run rerun/label, none of which
// defaultTools grants at all (flags.go). A suggestion here reads as
// permission; the operator can still type the entry into -add-tools by
// hand.
var neverGrantTable = []string{
	"gh pr merge",
	"gh issue edit",
	"gh issue close",
	"gh api",
	"gh secret",
	"gh repo",
	"gh run rerun",
	"gh label",
}

// hasCommandPrefix reports whether source starts with prefix as whole
// words, so "gh issue edit 431 ..." matches "gh issue edit" but "gh
// issue-edit ..." does not.
func hasCommandPrefix(source, prefix string) bool {
	if !strings.HasPrefix(source, prefix) {
		return false
	}
	rest := source[len(prefix):]
	return rest == "" || rest[0] == ' '
}

// addToolsEntry turns one refusal into the -add-tools entry that would let
// it through on a rerun, or explains in kind why there is none. allowlist
// is the effective allowlist the run actually used (resolveTools(cfg.tools,
// cfg.addTools)) — an entry it already covers is dropped as addToolsGranted
// rather than suggested again.
//
// A Bash refusal uses the CLI's own named part when the refusal named one —
// a compound-command approval lists the exact part, so that word is trusted
// over the whole line — else the command's own first word. gh takes three
// words instead of one: defaultTools grants gh per subcommand (flags.go's
// comment on defaultTools explains why), so the entry has to match at that
// grain. A source holding `;`, `|` or `&` is refused rather than guessed at
// — splitCommand (notify.go) already declines to become a shell parser, and
// this does the same on the CLI's own text. Any other tool's refusal names
// the bare tool, unconditionally: a non-Bash refusal is the whole tool
// asking, not a command inside it.
func addToolsEntry(r refusal, allowlist string) (entry string, kind addToolsKind) {
	if r.kind == refusalUngrantable {
		return "", addToolsUngrantable
	}
	if r.tool == "" {
		return "", addToolsUnknown
	}
	if r.tool == "Bash" {
		source := r.part
		if source == "" {
			source = r.command
		}
		source = strings.TrimSpace(source)
		if source == "" || strings.ContainsAny(source, ";|&") {
			return "", addToolsAmbiguous
		}
		if hasNeverGrantPrefix(source) {
			return "", addToolsNever
		}
		fields := strings.Fields(source)
		n := 1
		if fields[0] == "gh" {
			n = min(3, len(fields))
		}
		entry = "Bash(" + strings.Join(fields[:n], " ") + ":*)"
	} else {
		entry = r.tool
	}
	if resolveTools(allowlist, entry) == allowlist {
		return "", addToolsGranted
	}
	return entry, ""
}

// hasNeverGrantPrefix reports whether source is, or leads with, one of
// neverGrantTable's entries.
func hasNeverGrantPrefix(source string) bool {
	return slices.ContainsFunc(neverGrantTable, func(p string) bool {
		return hasCommandPrefix(source, p)
	})
}

// addToolsEntryThreadSafe reports whether entry is safe to name on a public
// issue thread: no absolute path, home shorthand, environment expansion or
// escape. `Bash(/Users/x/bin/tool:*)` is the case this guards — the same
// class of local detail the skill's "Describe, don't paste" rule already
// keeps off threads.
func addToolsEntryThreadSafe(entry string) bool {
	return !strings.ContainsAny(entry, "/~$\\")
}

// toolResultContentText reads a tool_result content field. The CLI has only
// ever been observed sending a plain string, but the underlying API also
// allows an array of {type:"text",text:...} blocks (evals/lib/grade.py's
// timeline() handles both off real captured runs), so both are read here.
func toolResultContentText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var sb strings.Builder
	for _, b := range blocks {
		if b.Type != "text" {
			continue
		}
		if sb.Len() > 0 {
			sb.WriteByte(' ')
		}
		sb.WriteString(b.Text)
	}
	return sb.String()
}

// limitResetRe reads the reset clause out of a limit refusal — "resets 10:50am
// (Europe/London)". Minutes and the zone are optional; a clause this does not
// match (a weekly limit's "resets Oct 14, 10am", a wording change) is not an
// error, it just means the caller polls instead of sleeping to a clock.
var limitResetRe = regexp.MustCompile(`(?i)\bresets\s+(\d{1,2})(?::([0-5]\d))?([ap]m)(?:\s*\(([^)]+)\))?`)

// limitReset turns a limit refusal into the instant the limit lifts: the next
// occurrence of the named wall-clock time, in the named zone. False whenever
// any part cannot be trusted — no clause, an hour that is not one, a zone this
// build cannot resolve — because a wait computed from a misread clock is worse
// than the poll fallback the caller has.
func limitReset(msg string, now time.Time) (time.Time, bool) {
	m := limitResetRe.FindStringSubmatch(msg)
	if m == nil {
		return time.Time{}, false
	}
	hour, minute, ok := clock12h(m[1], m[2], m[3])
	if !ok {
		return time.Time{}, false
	}
	loc, ok := resolveZone(m[4], now.Location())
	if !ok {
		return time.Time{}, false
	}
	at := now.In(loc)
	reset := time.Date(at.Year(), at.Month(), at.Day(), hour, minute, 0, 0, loc)
	if !reset.After(now) {
		// The named time is already behind the clock, so it means tomorrow.
		// AddDate rather than 24h, so a DST change cannot shift the wall time.
		reset = reset.AddDate(0, 0, 1)
	}
	return reset, true
}

// clock12h turns an hour/minute/meridiem triple — the shape both a limit
// refusal's clock and the usage probe's dated reset clause spell a time in —
// into 24-hour components. False for an hour outside 1-12, the one shape
// neither caller can trust.
func clock12h(hourStr, minuteStr, meridiem string) (hour, minute int, ok bool) {
	hour, err := strconv.Atoi(hourStr)
	if err != nil || hour < 1 || hour > 12 {
		return 0, 0, false
	}
	if minuteStr != "" {
		minute, _ = strconv.Atoi(minuteStr)
	}
	if strings.EqualFold(meridiem, "pm") {
		if hour != 12 {
			hour += 12
		}
	} else if hour == 12 {
		hour = 0
	}
	return hour, minute, true
}

// resolveZone reads an optional zone name — empty meaning "the caller's own",
// which is what a clause naming no zone at all means. False only for a name
// this build's tzdata cannot resolve.
func resolveZone(name string, fallback *time.Location) (*time.Location, bool) {
	if name == "" {
		return fallback, true
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, false
	}
	return loc, true
}
