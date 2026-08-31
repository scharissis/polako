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
// the operator's, so it names -add-tools and the skill rather than only
// reporting that something was refused.
const permissionParkReason = "the run stopped to ask for a permission this " +
	"allowlist does not grant — add the missing tool with -add-tools, or fix " +
	"the skill that reached for it, then remove needs-human to retry"

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

// toolRefusalSignatures are the CLI's own wrapper text for a tool_result the
// permission system refused outright — observed verbatim on issue #209
// (session 902c1c34-d4db-40cc-b00c-aa8f82242472): a plain "This command
// requires approval" for a single command, and, for a compound Bash command,
// "This Bash command contains multiple operations. The following parts
// require approval: ..." naming the parts. Unlike permissionAskSignatures
// this is CLI prose, not the model's, so — like authFailure and
// limitRefusal — it is trusted rather than treated as one phrasing among
// many.
var toolRefusalSignatures = []string{
	"this command requires approval",
	"this bash command contains multiple operations",
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
