package main

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The near-match hint is what turns "no such command" into a fix, and the
// bare-vs-namespaced confusion is symmetrical, so both directions must hit.
func TestNearMatchesBridgesPluginNamespacing(t *testing.T) {
	t.Parallel()
	inv := []string{"compact", "polako:implement-issue"}
	if got := nearMatches(inv, "implement-issue"); !slices.Equal(got, []string{"/polako:implement-issue"}) {
		t.Errorf("a bare -skill should surface the namespaced spelling, got %v", got)
	}
	if got := nearMatches([]string{"compact", "implement-issue"}, "polako:implement-issue"); !slices.Equal(got, []string{"/implement-issue"}) {
		t.Errorf("a namespaced -skill should surface the bare spelling, got %v", got)
	}
	if got := nearMatches(inv, "something-else"); got != nil {
		t.Errorf("no relative should mean no hint, got %v", got)
	}
}

// A wrong "missing" verdict kills a healthy run, so the check may only fire
// on positive evidence: an inventory that is present and lacks the command.
func TestLacksCommandNeedsPositiveEvidence(t *testing.T) {
	t.Parallel()
	list := []string{"compact", "cost", "polako:implement-issue"}
	if lacksCommand(list, "polako:implement-issue") {
		t.Error("a listed command must be found")
	}
	if !lacksCommand(list, "implement-issue") {
		t.Error("a command absent from a populated inventory is missing")
	}
	if lacksCommand(nil, "implement-issue") {
		t.Error("an absent inventory (older CLIs) must not read as missing")
	}
	if lacksCommand(list, "") {
		t.Error("a plain prompt invokes nothing, so nothing can be missing")
	}
	if lacksCommand([]string{"/compact", "/implement-issue"}, "implement-issue") {
		t.Error("a leading slash on inventory entries must not hide the command")
	}
}

// Two independent gates keep the matcher off a healthy run, and this repo's
// own backlog is what makes both necessary: it contains OAuth issues, so runs
// legitimately talk about authentication errors while succeeding.
func TestAuthFailureNeedsAFailedResultThatLeadsWithIt(t *testing.T) {
	t.Parallel()
	const refused = `Failed to authenticate. API Error: 401 {"type":"error",` +
		`"error":{"type":"authentication_error","message":"OAuth access token is invalid."}}`
	const mentions = "Fixed the OAuth issue: an authentication_error no longer retries."

	cases := []struct {
		name   string
		ev     streamEvent
		want   bool
		reason string
	}{
		{"refused credentials", streamEvent{Type: "result", Subtype: "success", IsError: true, Result: refused},
			true, "the CLI's own refusal has to be recognised"},
		{"success mentioning auth", streamEvent{Type: "result", Subtype: "success", Result: mentions},
			false, "a run that succeeded is not a failure to authenticate"},
		{"failure mentioning auth", streamEvent{Type: "result", Subtype: "success", IsError: true, Result: mentions},
			false, "a failed run that merely discusses auth must still be retried"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var rep runReport
			rep.observe(c.ev)
			if rep.authFailed != c.want {
				t.Errorf("authFailed = %v, want %v — %s", rep.authFailed, c.want, c.reason)
			}
		})
	}
}

func TestAuthFailureMatchesTheWaysTheCLIReportsIt(t *testing.T) {
	t.Parallel()
	refused := []string{
		`Failed to authenticate. API Error: 401 {"type":"error","error":{"type":"authentication_error","message":"OAuth access token is invalid."},"request_id":null}`,
		"OAuth token has expired. Please run /login",
		"API Error: 401 Unauthorized",
		"Invalid x-api-key",
	}
	for _, r := range refused {
		if !authFailure(r) {
			t.Errorf("should read as refused credentials: %s", clip(r, 80))
		}
	}
	fine := []string{
		"Opened a PR for issue 2.",
		"API Error: 500 Internal Server Error", // transient, and worth every retry
		"API Error: 429 rate limit exceeded",   // likewise
		"Unknown skill: polako:implement-issue",
		// The reason the match is anchored: this repo's own backlog contains
		// OAuth issues, and a run that quotes one while failing for an
		// unrelated reason must still be retried, not treated as a token
		// this process cannot fix.
		"I reproduced the report in issue #13, where the drain logged " +
			`Failed to authenticate. API Error: 401 {"type":"error","error":` +
			`{"type":"authentication_error","message":"OAuth access token is invalid."}} ` +
			"and then retried three times. I ran out of turns before opening the PR.",
	}
	for _, r := range fine {
		if authFailure(r) {
			t.Errorf("should not read as refused credentials: %s", clip(r, 80))
		}
	}
}

// Issue #157: a clean exit used to have its result text read once — for
// authFailed/limitMsg, both gated on IsError — and otherwise dropped, so a
// park could assert "no questions" over a run whose final message was
// verbatim one. observe now classifies it on a success result too, since
// #138's run ended cleanly.
func TestObserveClassifiesACleanExitsFinalText(t *testing.T) {
	t.Parallel()
	const asked = "This requires user confirmation to switch the session's " +
		"working directory into the worktree. Can you approve entering `/tmp/x`?"
	const ordinary = "Opened a PR for issue 7."

	cases := []struct {
		name           string
		result         string
		wantPermission bool
	}{
		{"a permission request", asked, true},
		{"an ordinary result", ordinary, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var rep runReport
			rep.observe(streamEvent{Type: "result", Subtype: "success", Result: c.result})
			if rep.permissionRefused != c.wantPermission {
				t.Errorf("permissionRefused = %v, want %v", rep.permissionRefused, c.wantPermission)
			}
		})
	}
}

// permissionRefusal matches the model's own wording, not a CLI wrapper
// string, so it has no single canonical form the way authFailure's does — but
// it still has to leave the same quoting risk closed: an issue about tool
// permissions that gets quoted back mid-message must not park the issue over
// a run that actually succeeded.
func TestPermissionRefusalMatchesTheWaysARunAsksApproval(t *testing.T) {
	t.Parallel()
	asks := []string{
		"This requires user confirmation to switch the session's working " +
			"directory into the worktree. Can you approve entering `/tmp/x`?",
		"This requires confirmation before I can proceed.",
		"This requires approval to run the migration.",
		"This requires your approval before I continue.",
		"Can you approve granting Bash(cd:*) so I can finish?",
		"Could you approve this tool before I continue?",
		"I need permission to use the EnterWorktree tool.",
		"I don't have permission to run that command.",
		"I do not have permission to write outside the worktree.",
		// A markdown bullet ahead of the signature, the other wrapping
		// resultHead has to see through besides a heading or an asterisk.
		"- This requires approval to write outside the worktree. Can you approve?",
	}
	for _, r := range asks {
		if !permissionRefusal(r) {
			t.Errorf("should read as a permission request: %s", clip(r, 80))
		}
	}
	fine := []string{
		"Opened a PR for issue 7.",
		"Unknown skill: polako:implement-issue",
		// The word-boundary check: a signature is a raw byte prefix of this
		// sentence, but the run asked for nothing — a false match here would
		// park a possibly-salvageable run over sandbox tooling, not a refused
		// tool.
		"I need permission tooling wasn't available in this sandbox, so I " +
			"worked around it and left notes on the branch.",
		// The reason the match is anchored: a run whose issue is *about*
		// permission prompts can legitimately end by describing one without
		// asking for anything itself.
		"Fixed #156, which was about a run that says " +
			`"This requires user confirmation" when cd is not allowlisted. ` +
			"Opened a PR.",
		// Issue #209: #126's three actual final texts, verbatim, from session
		// 902c1c34-d4db-40cc-b00c-aa8f82242472. Each run had already been
		// refused a tool_result mid-run ("This command requires approval"),
		// but none of these closing messages themselves read as an ask — that
		// is the bug: the prose route stays blind to all three on purpose,
		// pinned here, and TestObserveLatchesOnARefusedToolResult is what now
		// catches them instead, off the structural signal.
		"Issue #126 is resolved: my earlier run confirmed PR #132 already " +
			"shipped the fix and asked whether to close it, and you replied " +
			`"Mark this issue as resolved by #132." I've cleared the ` +
			"awaiting-answer label. Closing the issue with an explanatory " +
			"comment needs your approval — please confirm the gh issue close " +
			`126 --comment "..." command above so I can finish.`,
		"I've hit a hard blocker: the gh issue close command requires " +
			"interactive approval that isn't coming through in this turn, and " +
			"I can't bypass permission prompts — that's a safety boundary I " +
			"won't cross, not a workaround I can find my way around.",
		"Re-verified — nothing has changed since the last check: no new " +
			"commits, no PR, issue still open, awaiting-answer already " +
			`cleared. Retrying gh issue close produced the identical ` +
			`"requires approval" block again.`,
	}
	for _, r := range fine {
		if permissionRefusal(r) {
			t.Errorf("should not read as a permission request: %s", clip(r, 80))
		}
	}
}

// Issue #209: the CLI's own wrapper text for a refused tool_result, not the
// model's prose — both real signatures observed on session
// 902c1c34-d4db-40cc-b00c-aa8f82242472, and a real is_error tool_result from
// the same session that is *not* this: a working-directory security block,
// which must not be mistaken for a refused permission.
func TestToolResultRefusalMatchesTheCLIsOwnWrapperText(t *testing.T) {
	t.Parallel()
	refused := []string{
		"This command requires approval",
		"This Bash command contains multiple operations. The following " +
			"parts require approval: ls /Users/stef/code/polako-issue-126/PLAN.md, " +
			"cat /Users/stef/code/polako-issue-126/PLAN.md",
		// 2.1.312's words, for a CLI that sends them without the event.
		"Claude requested permissions to use WebFetch, but you haven't granted it yet.",
		"A variable in this command can't be checked before it runs",
		"A variable set in this command can't be checked before it runs",
		"A nested command in this command can't be checked before it runs",
		"This command changes directory before running a version-control command, which can pick up " +
			"untrusted hooks or repository configuration from the target directory. Approve only if you trust it.",
	}
	for _, r := range refused {
		if !toolResultRefusal(r) {
			t.Errorf("should read as a refused tool_result: %s", clip(r, 80))
		}
	}
	fine := []string{
		"",
		"issue #48",
		// Real capture, same session: a working-directory restriction, not a
		// permission the allowlist could grant.
		"rm in '/Users/stef/code/polako-issue-126/ISSUE_COMMENT.md' was " +
			"blocked. For security, Claude Code may only remove files from " +
			"the allowed working directories for this session: " +
			"'/Users/stef/code/polako'.",
	}
	for _, r := range fine {
		if toolResultRefusal(r) {
			t.Errorf("should not read as a refused tool_result: %s", clip(r, 80))
		}
	}
}

// Issue #209: a refused tool_result is a structural fact the CLI states
// itself, mid-run — unlike permissionRefusal's prose, observe latches it
// straight onto permissionRefused (which already parks without spending the
// resume budget) and names the refused command, correlated back to its
// tool_use by id since a single-command refusal's own text does not name it.
func TestObserveLatchesOnARefusedToolResult(t *testing.T) {
	t.Parallel()
	toolUse := func(id, name string, input string) streamEvent {
		ev, ok := parseEvent([]byte(`{"type":"assistant","message":{"content":[` +
			`{"type":"tool_use","id":` + strconv.Quote(id) + `,"name":` + strconv.Quote(name) +
			`,"input":` + input + `}]}}`))
		if !ok {
			t.Fatalf("could not build tool_use turn")
		}
		return ev
	}
	toolResult := func(id, content string, isErr bool) streamEvent {
		ev, ok := parseEvent([]byte(`{"type":"user","message":{"content":[` +
			`{"type":"tool_result","tool_use_id":` + strconv.Quote(id) +
			`,"is_error":` + strconv.FormatBool(isErr) + `,"content":` + strconv.Quote(content) + `}]}}`))
		if !ok {
			t.Fatalf("could not build tool_result turn")
		}
		return ev
	}

	var rep runReport
	rep.observe(toolUse("toolu_1", "Bash", `{"command":"gh issue close 126 --comment \"...\""}`))
	rep.observe(toolResult("toolu_1", "This command requires approval", true))
	// #126's actual final text: ordinary prose, no ask of its own.
	rep.observe(streamEvent{Type: "result", Subtype: "success", Result: "Issue #126 is resolved: my earlier " +
		"run confirmed PR #132 already shipped the fix."})

	if !rep.permissionRefused {
		t.Error("permissionRefused should latch from the refused tool_result " +
			"and survive an ordinary final result — the overwrite this issue fixes")
	}
	if want := "Bash: gh issue close 126"; !strings.Contains(rep.lastRefusalDetail(), want) {
		t.Errorf("lastRefusalDetail() = %q, want it to name the correlated command (contains %q)",
			rep.lastRefusalDetail(), want)
	}

	// A tool_result that is not an error, or does not read as a refusal, must
	// not latch.
	var ok runReport
	ok.observe(toolUse("toolu_2", "Bash", `{"command":"gh issue view 126"}`))
	ok.observe(toolResult("toolu_2", "issue #126: ...", false))
	ok.observe(streamEvent{Type: "result", Subtype: "success", Result: "Opened a PR."})
	if ok.permissionRefused {
		t.Error("permissionRefused should stay false — no refusal on the stream")
	}

	var blockedElsewhere runReport
	blockedElsewhere.observe(toolUse("toolu_3", "Bash", `{"command":"rm ISSUE_COMMENT.md"}`))
	blockedElsewhere.observe(toolResult("toolu_3",
		"rm in '/x/ISSUE_COMMENT.md' was blocked. For security, Claude Code may only "+
			"remove files from the allowed working directories for this session: '/x'.", true))
	blockedElsewhere.observe(streamEvent{Type: "result", Subtype: "success", Result: "Opened a PR."})
	if blockedElsewhere.permissionRefused {
		t.Error("permissionRefused should stay false — a directory restriction is not a permission refusal")
	}
}

// Issue #461: #402 and #318 both refused a tool mid-run, then kept going —
// more tool calls succeeded, and the run ended on a calm word that was not
// itself an ask. refusalWorkedAround is what tells that shape apart from
// #126's, which looks the same up to the refusal but has nothing after it,
// and from #138's, a final-message ask with no tool_result refusal to work
// around at all.
func TestRefusalWorkedAround(t *testing.T) {
	t.Parallel()
	observe := func(t *testing.T, lines ...string) runReport {
		t.Helper()
		var rep runReport
		for _, l := range lines {
			ev, ok := parseEvent([]byte(l))
			if !ok {
				t.Fatalf("parseEvent rejected %s", l)
			}
			rep.observe(ev)
		}
		return rep
	}
	result := func(text string) string {
		return `{"type":"result","subtype":"success","result":` + jsonString(text) + `}`
	}

	cases := []struct {
		name   string
		lines  []string
		want   bool
		reason string
	}{
		{
			"refused, a successful call followed, calm final word — #402/#318's shape",
			[]string{
				toolUseID("toolu_1", "Bash", `{"command":"cd /w && gofmt -l ."}`),
				toolResult("toolu_1", "This command requires approval", true),
				toolUseID("toolu_2", "Bash", `{"command":"gofmt -l /w"}`),
				toolResult("toolu_2", "", false),
				result("14 commits landed; the review gate finished."),
			},
			true, "successful calls followed the last refusal and the final word is not an ask",
		},
		{
			"refused, nothing after it — #126's shape",
			[]string{
				toolUseID("toolu_1", "Bash", `{"command":"gh issue close 126"}`),
				toolResult("toolu_1", "This command requires approval", true),
				result("Issue #126 is resolved: an earlier run confirmed the fix shipped."),
			},
			false, "no successful call followed the refusal, so nothing was worked around",
		},
		{
			"refused, a success followed, but the final word is itself an ask",
			[]string{
				toolUseID("toolu_1", "Bash", `{"command":"cd /w"}`),
				toolResult("toolu_1", "This command requires approval", true),
				toolUseID("toolu_2", "Read", `{"file_path":"PLAN.md"}`),
				toolResult("toolu_2", "...", false),
				result("This requires user confirmation to proceed. Can you approve?"),
			},
			false, "a final message that reads as an ask is disqualifying on its own",
		},
		{
			"a second refusal with nothing successful after it, despite a success after the first",
			[]string{
				toolUseID("toolu_1", "Bash", `{"command":"cd /w"}`),
				toolResult("toolu_1", "This command requires approval", true),
				toolUseID("toolu_2", "Read", `{"file_path":"PLAN.md"}`),
				toolResult("toolu_2", "...", false),
				toolUseID("toolu_3", "Bash", `{"command":"gh pr merge 1"}`),
				toolResult("toolu_3", "This command requires approval", true),
				result("Nothing left to do here."),
			},
			false, "scoped to the *last* refusal, whose own aftermath had no success",
		},
		{
			"no refusal at all",
			[]string{result("Opened a PR.")},
			false, "nothing to work around",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rep := observe(t, c.lines...)
			if got := rep.refusalWorkedAround(); got != c.want {
				t.Errorf("refusalWorkedAround = %v, want %v — %s", got, c.want, c.reason)
			}
		})
	}

	// Review finding on issue #461: the refusal detail used to latch on the
	// *first* refusal, but refusalWorkedAround judges the *last* one's
	// aftermath — a run whose first refusal got worked around and whose
	// second did not must still name the second (the actual blocker) in the
	// park, not the resolved first one.
	t.Run("names the last refusal, not the first", func(t *testing.T) {
		rep := observe(t,
			toolUseID("toolu_1", "Bash", `{"command":"cd /w"}`),
			toolResult("toolu_1", "This command requires approval", true),
			toolUseID("toolu_2", "Read", `{"file_path":"PLAN.md"}`),
			toolResult("toolu_2", "...", false),
			toolUseID("toolu_3", "Bash", `{"command":"gh pr merge 1"}`),
			toolResult("toolu_3", "This command requires approval", true),
			result("Nothing left to do here."),
		)
		if want := "Bash: gh pr merge 1"; !strings.Contains(rep.lastRefusalDetail(), want) {
			t.Errorf("lastRefusalDetail() = %q, want the last (unresolved) refusal %q",
				rep.lastRefusalDetail(), want)
		}
	})
}

// Issue #430: #390's session drew five refusals and the old
// permissionRefusedDetail kept only the first, as the whole compound
// command even where the CLI had already named the one part it refused.
// observe now keeps all five, classified by the CLI's own words — the table
// from issue #430, fed verbatim.
func TestObserveKeepsEveryRefusalFromTheCLIsOwnWords(t *testing.T) {
	t.Parallel()
	pairs := []struct {
		command string
		refusal string
	}{
		{
			`git fetch origin 2>&1; echo "exit:$?"`,
			`This Bash command contains multiple operations. The following ` +
				`part requires approval: echo "exit:$?"`,
		},
		{
			`git … remote -v; ssh-add -l 2>&1; echo SSH_AUTH_SOCK=$SSH_AUTH_SOCK`,
			"Contains simple_expansion",
		},
		{
			`git … fetch origin 2>&1; echo RC=$?`,
			"Contains simple_expansion",
		},
		{
			`echo "SSH_AUTH_SOCK=$SSH_AUTH_SOCK"; ls -la …; ssh -T git@github.com 2>&1`,
			"Contains simple_expansion",
		},
		{
			"ssh -T git@github.com",
			"This command requires approval",
		},
	}

	var rep runReport
	for i, p := range pairs {
		id := fmt.Sprintf("toolu_%d", i+1)
		ev, ok := parseEvent([]byte(toolUseID(id, "Bash", `{"command":`+jsonString(p.command)+`}`)))
		if !ok {
			t.Fatalf("could not build tool_use for pair %d", i)
		}
		rep.observe(ev)
		ev, ok = parseEvent([]byte(toolResult(id, p.refusal, true)))
		if !ok {
			t.Fatalf("could not build tool_result for pair %d", i)
		}
		rep.observe(ev)
	}

	if len(rep.refusals) != 5 {
		t.Fatalf("got %d refusals, want 5: %+v", len(rep.refusals), rep.refusals)
	}

	var parts, ungrantable, plain int
	for _, r := range rep.refusals {
		switch r.kind {
		case refusalPart:
			parts++
			if r.part != `echo "exit:$?"` {
				t.Errorf("part refusal = %+v, want part %q", r, `echo "exit:$?"`)
			}
		case refusalUngrantable:
			ungrantable++
		case refusalPlain:
			plain++
			if r.command != "ssh -T git@github.com" {
				t.Errorf("plain refusal = %+v, want command %q", r, "ssh -T git@github.com")
			}
		default:
			t.Errorf("unexpected kind %q on %+v", r.kind, r)
		}
	}
	if parts != 1 || ungrantable != 3 || plain != 1 {
		t.Errorf("got %d part, %d ungrantable, %d plain refusals, want 1, 3, 1", parts, ungrantable, plain)
	}
}

// A refused non-Bash tool must still name its actual target, not just the
// CLI's generic refusal sentence — the detail toolDetail already extracts
// for a log line (file_path, pattern, query, description, skill+args), read
// here unclipped through the same field list.
func TestObserveRefusalNamesANonBashTarget(t *testing.T) {
	t.Parallel()
	var rep runReport
	ev, _ := parseEvent([]byte(toolUseID("toolu_1", "Read", `{"file_path":"PLAN.md"}`)))
	rep.observe(ev)
	ev, _ = parseEvent([]byte(toolResult("toolu_1", "This command requires approval", true)))
	rep.observe(ev)
	if want := "Read: PLAN.md"; rep.lastRefusalDetail() != want {
		t.Errorf("lastRefusalDetail() = %q, want %q — the refused file, not the CLI's generic text",
			rep.lastRefusalDetail(), want)
	}

	rep = runReport{}
	ev, _ = parseEvent([]byte(toolUseID("toolu_2", "Skill", `{"skill":"review-health","args":"--dry-run"}`)))
	rep.observe(ev)
	ev, _ = parseEvent([]byte(toolResult("toolu_2", "This command requires approval", true)))
	rep.observe(ev)
	if want := "Skill: review-health --dry-run"; rep.lastRefusalDetail() != want {
		t.Errorf("lastRefusalDetail() = %q, want %q — the refused skill and its args",
			rep.lastRefusalDetail(), want)
	}
}

// When the CLI's "multiple operations" wording doesn't carry a parseable
// part list, the fallback entry must still be kind refusalPart — the CLI's
// own wording said this was a compound-command refusal, and losing the part
// list is no reason to also misclassify it as a single-command one.
func TestObserveRefusalKindSurvivesAnUnparseablePartList(t *testing.T) {
	t.Parallel()
	var rep runReport
	ev, _ := parseEvent([]byte(toolUseID("toolu_1", "Bash", `{"command":"a; b"}`)))
	rep.observe(ev)
	ev, _ = parseEvent([]byte(toolResult("toolu_1",
		"This Bash command contains multiple operations.", true)))
	rep.observe(ev)
	if len(rep.refusals) != 1 {
		t.Fatalf("got %d refusals, want 1", len(rep.refusals))
	}
	if got := rep.refusals[0].kind; got != refusalPart {
		t.Errorf("kind = %q, want %q — the CLI still called this a compound-command refusal",
			got, refusalPart)
	}
}

// A run stuck looping on the same wall must not grow runReport.refusals
// without bound: an identical refusal, repeated, dedups to one entry, and
// past refusalCap the oldest distinct refusal is evicted, never the newest.
func TestObserveRefusalsDedupsAndCaps(t *testing.T) {
	t.Parallel()
	var rep runReport
	for i := 0; i < refusalCap+5; i++ {
		id := fmt.Sprintf("toolu_%d", i)
		ev, _ := parseEvent([]byte(toolUseID(id, "Bash", `{"command":"ssh -T git@github.com"}`)))
		rep.observe(ev)
		ev, _ = parseEvent([]byte(toolResult(id, "This command requires approval", true)))
		rep.observe(ev)
	}
	if len(rep.refusals) != 1 {
		t.Errorf("got %d refusals for an identical one repeated, want 1 (deduplicated)", len(rep.refusals))
	}

	rep = runReport{}
	for i := 0; i < refusalCap+5; i++ {
		id := fmt.Sprintf("toolu_%d", i)
		input := `{"command":` + jsonString(fmt.Sprintf("tool%d --flag", i)) + `}`
		ev, _ := parseEvent([]byte(toolUseID(id, "Bash", input)))
		rep.observe(ev)
		ev, _ = parseEvent([]byte(toolResult(id, "This command requires approval", true)))
		rep.observe(ev)
	}
	if len(rep.refusals) != refusalCap {
		t.Errorf("got %d refusals for %d distinct ones, want capped at %d", len(rep.refusals), refusalCap+5, refusalCap)
	}
	if want := "Bash: tool" + fmt.Sprint(refusalCap+4) + " --flag"; rep.lastRefusalDetail() != want {
		t.Errorf("lastRefusalDetail() = %q, want %q — the cap must evict the oldest, never the newest",
			rep.lastRefusalDetail(), want)
	}

	// A refusal identical to an earlier, non-consecutive one must still be
	// read as the *last* one: recurring after a distinct refusal B means B
	// is resolved (or at least not what's currently blocking), and reporting
	// B instead of the recurrence would send an operator to grant the wrong
	// command.
	rep = runReport{}
	a := toolUseID("toolu_a", "Bash", `{"command":"git fetch origin"}`)
	b := toolUseID("toolu_b", "Bash", `{"command":"npm test"}`)
	refuse := func(id string) string { return toolResult(id, "This command requires approval", true) }
	for _, l := range []string{a, refuse("toolu_a"), b, refuse("toolu_b")} {
		ev, _ := parseEvent([]byte(l))
		rep.observe(ev)
	}
	aAgain := toolUseID("toolu_a2", "Bash", `{"command":"git fetch origin"}`)
	for _, l := range []string{aAgain, refuse("toolu_a2")} {
		ev, _ := parseEvent([]byte(l))
		rep.observe(ev)
	}
	if len(rep.refusals) != 2 {
		t.Fatalf("got %d refusals, want 2 (A and B, A's recurrence deduplicated)", len(rep.refusals))
	}
	if want := "Bash: git fetch origin"; rep.lastRefusalDetail() != want {
		t.Errorf("lastRefusalDetail() = %q, want %q — A recurred after B, so A is the last one, not B",
			rep.lastRefusalDetail(), want)
	}
}

// The acceptance table from issue #431, plus the CLI-named-part,
// ungrantable and uncorrelated cases ticket 1 (#430) already covers.
func TestAddToolsEntryDerivesTheGrantOrExplainsWhyNot(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		refusal   refusal
		wantEntry string
		wantKind  addToolsKind
	}{
		{
			name:      "bash refusal takes the first word",
			refusal:   refusal{tool: "Bash", command: `echo "exit:$?"`, kind: refusalPlain},
			wantEntry: `Bash(echo:*)`,
		},
		{
			name:      "an @ in the command doesn't confuse the split",
			refusal:   refusal{tool: "Bash", command: "ssh -T git@github.com", kind: refusalPlain},
			wantEntry: "Bash(ssh:*)",
		},
		{
			name:     "gh takes three words and lands on the never table",
			refusal:  refusal{tool: "Bash", command: "gh issue close 1 --comment x", kind: refusalPlain},
			wantKind: addToolsNever,
		},
		{
			name:     "gh pr merge is never granted",
			refusal:  refusal{tool: "Bash", command: "gh pr merge 7", kind: refusalPlain},
			wantKind: addToolsNever,
		},
		{
			name:      "a non-Bash tool names itself, ignoring its command",
			refusal:   refusal{tool: "WebFetch", command: "https://example.com", kind: refusalPlain},
			wantEntry: "WebFetch",
		},
		{
			name:     "an entry the allowlist already grants is dropped",
			refusal:  refusal{tool: "Bash", command: "git status", kind: refusalPlain},
			wantKind: addToolsGranted,
		},
		{
			name:     "no named part and a shell operator is ambiguous, not guessed at",
			refusal:  refusal{tool: "Bash", command: "a; b", kind: refusalPart},
			wantKind: addToolsAmbiguous,
		},
		{
			name:      "an absolute path still yields an entry",
			refusal:   refusal{tool: "Bash", command: "/Users/x/bin/tool --flag", kind: refusalPlain},
			wantEntry: "Bash(/Users/x/bin/tool:*)",
		},
		{
			name: "the CLI's named part is trusted over the whole compound line",
			refusal: refusal{
				tool:    "Bash",
				command: `git fetch origin 2>&1; echo "exit:$?"`,
				part:    `echo "exit:$?"`,
				kind:    refusalPart,
			},
			wantEntry: `Bash(echo:*)`,
		},
		{
			name:     "ungrantable yields no entry",
			refusal:  refusal{tool: "Bash", command: "git fetch origin 2>&1; echo RC=$?", kind: refusalUngrantable},
			wantKind: addToolsUngrantable,
		},
		{
			// Read as plain, this would hand out Bash(ls:*), which lets
			// nothing through: the $(…) is what the CLI refused.
			name:     "unrecognized words derive nothing, even with a command to read",
			refusal:  refusal{tool: "Bash", command: "ls $(pwd)", kind: refusalUnrecognized},
			wantKind: addToolsUnrecognized,
		},
		{
			name:     "an uncorrelated refusal has nothing to derive from",
			refusal:  refusal{command: "This command requires approval", kind: refusalPlain},
			wantKind: addToolsUnknown,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			entry, kind := addToolsEntry(tt.refusal, defaultTools)
			if entry != tt.wantEntry || kind != tt.wantKind {
				t.Errorf("addToolsEntry(%+v) = (%q, %q), want (%q, %q)",
					tt.refusal, entry, kind, tt.wantEntry, tt.wantKind)
			}
		})
	}
}

func TestAddToolsEntryThreadSafe(t *testing.T) {
	t.Parallel()
	tests := []struct {
		entry string
		safe  bool
	}{
		{"Bash(echo:*)", true},
		{"Bash(ssh:*)", true},
		{"WebFetch", true},
		{"Bash(/Users/x/bin/tool:*)", false},
		{"Bash(rm -rf ~/data:*)", false},
		{`Bash(echo $HOME:*)`, false},
		{`Bash(a\b:*)`, false},
	}
	for _, tt := range tests {
		if got := addToolsEntryThreadSafe(tt.entry); got != tt.safe {
			t.Errorf("addToolsEntryThreadSafe(%q) = %v, want %v", tt.entry, got, tt.safe)
		}
	}
}

// Issue #432: permissionParkAdviceFrom's
// four cases, over addToolsEntry's own already-tested classifications.
func TestPermissionParkAdviceNamesTheFixOrSaysWhyNot(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		refusals   []refusal
		wantOK     bool
		wantParts  []string // substrings advice must contain when wantOK
		wantAbsent []string // substrings it must not
	}{
		{
			name:     "no refusals at all has nothing to say",
			refusals: nil,
			wantOK:   false,
		},
		{
			name: "an entry names it and gives the rerun line",
			refusals: []refusal{
				{tool: "Bash", command: `echo "exit:$?"`, kind: refusalPlain},
			},
			wantOK:    true,
			wantParts: []string{"`Bash(echo:*)`", `-add-tools "Bash(echo:*)"`, "remove needs-human"},
		},
		{
			name: "several entries join into one rerun value",
			refusals: []refusal{
				{tool: "Bash", command: `echo "exit:$?"`, kind: refusalPlain},
				{tool: "Bash", command: "ssh -T git@github.com", kind: refusalPlain},
			},
			wantOK:    true,
			wantParts: []string{`-add-tools "Bash(echo:*),Bash(ssh:*)"`},
		},
		{
			name: "only ungrantable says the $VAR wording",
			refusals: []refusal{
				{tool: "Bash", command: "git fetch origin 2>&1; echo RC=$?", kind: refusalUngrantable},
			},
			wantOK:    true,
			wantParts: []string{"$VAR", "phrase it differently"},
		},
		{
			name: "a cd joined to git is named as that, not as a $VAR",
			refusals: []refusal{
				{tool: "Bash", command: "cd .. && git status", kind: refusalUngrantable, shape: shapeCdVCS},
			},
			wantOK:     true,
			wantParts:  []string{"a `cd` joined to git", "phrase it differently"},
			wantAbsent: []string{"$VAR", "-add-tools \""},
		},
		{
			name: "two shapes are both named, each once",
			refusals: []refusal{
				{tool: "Bash", command: "git -C $PWD status", kind: refusalUngrantable, shape: shapeVariable},
				{tool: "Bash", command: "ls $(pwd)", kind: refusalUngrantable, shape: shapeNested},
				{tool: "Bash", command: "X=1; echo $X", kind: refusalUngrantable, shape: shapeVariable},
			},
			wantOK:    true,
			wantParts: []string{"a command with a `$VAR` and a command with a `$(…)`,", "phrase them differently"},
		},
		{
			// A rerun with only the grant would hit the cd again.
			name: "a grant and a shape no grant lifts are both said",
			refusals: []refusal{
				{tool: "Bash", command: "ssh -T git@github.com", kind: refusalPlain},
				{tool: "Bash", command: "cd .. && git status", kind: refusalUngrantable, shape: shapeCdVCS},
			},
			wantOK: true,
			wantParts: []string{`-add-tools "Bash(ssh:*)"`,
				"It was also refused a `cd` joined to git, which no `-add-tools` entry allows"},
		},
		{
			name: "two shapes and a never-grant command are each said, not counted as both",
			refusals: []refusal{
				{tool: "Bash", command: "git -C $PWD status", kind: refusalUngrantable, shape: shapeVariable},
				{tool: "Bash", command: "cd .. && git status", kind: refusalUngrantable, shape: shapeCdVCS},
				{tool: "Bash", command: "gh pr merge 7", kind: refusalPlain},
			},
			wantOK: true,
			wantParts: []string{"a command with a `$VAR` and a `cd` joined to git", "phrase them differently",
				"It was also refused a command whose grant polako doesn't hand out automatically"},
			wantAbsent: []string{"both"},
		},
		{
			name: "unrecognized words have nothing to say",
			refusals: []refusal{
				{tool: "Bash", command: "curl -s https://example.com", kind: refusalUnrecognized},
			},
			wantOK: false,
		},
		{
			name: "only never says polako doesn't hand it out",
			refusals: []refusal{
				{tool: "Bash", command: "gh pr merge 7", kind: refusalPlain},
			},
			wantOK:    true,
			wantParts: []string{"doesn't hand that grant out"},
		},
		{
			name: "ungrantable and never together names both, with no entries to fall back on",
			refusals: []refusal{
				{tool: "Bash", command: "git fetch origin 2>&1; echo RC=$?", kind: refusalUngrantable},
				{tool: "Bash", command: "gh pr merge 7", kind: refusalPlain},
			},
			wantOK:    true,
			wantParts: []string{"$VAR", "doesn't hand out automatically"},
		},
		{
			name: "a path-bearing entry is filtered out, leaving nothing derivable",
			refusals: []refusal{
				{tool: "Bash", command: "/Users/x/bin/tool --flag", kind: refusalPlain},
			},
			wantOK: false,
		},
		{
			name: "an uncorrelated refusal has nothing to derive from",
			refusals: []refusal{
				{command: "This command requires approval", kind: refusalPlain},
			},
			wantOK: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			entries, ungrantable, never := permissionParkEntries(tt.refusals, defaultTools)
			advice, ok := permissionParkAdviceFrom(entries, ungrantable, never)
			if ok != tt.wantOK {
				t.Fatalf("permissionParkAdviceFrom() ok = %v, want %v (advice %q)", ok, tt.wantOK, advice)
			}
			for _, part := range tt.wantParts {
				if !strings.Contains(advice, part) {
					t.Errorf("advice %q missing %q", advice, part)
				}
			}
			for _, part := range tt.wantAbsent {
				if strings.Contains(advice, part) {
					t.Errorf("advice %q should not say %q", advice, part)
				}
			}
		})
	}
}

// permissionParkReasonAndEntries falls back to the fixed pointer at the
// terminal exactly when permissionParkAdviceFrom has nothing to say, and
// otherwise uses its advice verbatim.
func TestPermissionParkReasonAndEntriesFallsBackOnlyWhenAdviceHasNothing(t *testing.T) {
	t.Parallel()
	if got, _ := permissionParkReasonAndEntries(nil, defaultTools); got != permissionParkReason {
		t.Errorf("permissionParkReasonAndEntries(nil, ...) = %q, want the fixed fallback", got)
	}
	refusals := []refusal{{tool: "Bash", command: "ssh -T git@github.com", kind: refusalPlain}}
	if got, _ := permissionParkReasonAndEntries(refusals, defaultTools); got == permissionParkReason {
		t.Errorf("permissionParkReasonAndEntries(%+v, ...) should not fall back — an entry was derivable", refusals)
	}
}

// The worked-around wording always leads with the count and the hedge,
// whatever advice has to add — issue #390 is the case where naming the
// entry would have been actively misleading, not just insufficient.
func TestPermissionParkReasonWorkedAroundLeadsWithTheCountAndHedge(t *testing.T) {
	t.Parallel()
	refusals := []refusal{
		{tool: "Bash", command: "ssh -T git@github.com", kind: refusalPlain},
		{tool: "Bash", command: "git fetch origin 2>&1; echo RC=$?", kind: refusalUngrantable},
	}
	got, _ := permissionParkReasonWorkedAroundAndEntries(refusals, defaultTools)
	for _, want := range []string{
		"the run opened no PR, and was refused 2 calls along the way",
		"a wider grant may not be the blocker",
		"but if it is: the run was refused `Bash(ssh:*)`",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("permissionParkReasonWorkedAroundAndEntries(...) = %q, missing %q", got, want)
		}
	}
	if got, _ := permissionParkReasonWorkedAroundAndEntries(nil, defaultTools); !strings.Contains(got, "0 calls") ||
		!strings.Contains(got, permissionParkReason) {
		t.Errorf("permissionParkReasonWorkedAroundAndEntries(nil, ...) = %q, want the count and the fixed fallback", got)
	}
}

// permissionDenied is the system event Claude Code 2.1.312 sends ahead of a
// refused call's tool_result, cut to the fields polako reads plus the reason,
// a plain string where every other event's "message" is an object. The
// tool_result that follows repeats it.
func permissionDenied(id, words string) string {
	return `{"type":"system","subtype":"permission_denied","tool_use_id":"` + id +
		`","message":` + jsonString(words) + `}`
}

// observeLines feeds stream lines to a fresh report, failing on any line
// parseEvent rejects — a rejected line is one the scanner drops unread.
func observeLines(t *testing.T, lines ...string) runReport {
	t.Helper()
	var rep runReport
	for _, l := range lines {
		ev, ok := parseEvent([]byte(l))
		if !ok {
			t.Fatalf("parseEvent rejected %s", l)
		}
		rep.observe(ev)
	}
	return rep
}

// dontAskWords is the one line 2.1.312 refuses every call with under
// -permission-mode dontAsk, whatever the command: nothing in it says
// whether a grant would help.
const dontAskWords = "Permission to use Bash has been denied because Claude Code is running in " +
	"don't ask mode. IMPORTANT: You *may* attempt to accomplish this action using other tools that " +
	"might naturally be used to accomplish this goal, e.g. using head instead of cat."

// A permission_denied event's "message" is a string where every other
// event's is an object, and the scanner drops a line it cannot parse — so the
// event is only read at all if the string form parses.
func TestParseEventReadsAPermissionDeniedEvent(t *testing.T) {
	t.Parallel()
	// 2.1.312, verbatim but for the ids.
	line := `{"type":"system","subtype":"permission_denied","tool_name":"Bash","tool_use_id":"toolu_1",` +
		`"decision_reason_type":"other","decision_reason":"A variable in this command can't be checked before it runs",` +
		`"message":"A variable in this command can't be checked before it runs","uuid":"u","session_id":"s"}`
	ev, ok := parseEvent([]byte(line))
	if !ok {
		t.Fatal("parseEvent rejected a permission_denied event")
	}
	if ev.Type != "system" || ev.Subtype != "permission_denied" || ev.ToolUseID != "toolu_1" {
		t.Errorf("parsed type %q, subtype %q, tool_use_id %q — want the event and the call it refused",
			ev.Type, ev.Subtype, ev.ToolUseID)
	}

	ev, ok = parseEvent([]byte(toolUseID("toolu_2", "Bash", `{"command":"git status"}`)))
	if !ok || len(ev.Message.Content) != 1 || ev.Message.Content[0].ID != "toolu_2" {
		t.Errorf("an object message must parse as before, got ok=%v %+v", ok, ev.Message)
	}
	if _, ok := parseEvent([]byte(`{"type":"user","message":null}`)); !ok {
		t.Error("a null message must still parse")
	}
}

// Claude Code 2.1.312's own words for each refusal shape, probed 2026-10-28
// under acceptEdits: the command, the reason its permission_denied event and
// tool_result both carried, and what a park should make of it. With the
// grant polako would derive already in place, each ungrantable one was
// refused again.
func TestObserveReadsEachRefusalInTheCLIsCurrentWords(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, tool, input, words string
		kind                     refusalKind
		shape                    refusalShape
		wantEntry                string
		wantWhy                  addToolsKind
	}{
		{
			name: "a command the allowlist doesn't grant", tool: "Bash",
			input: `{"command":"ssh -T git@github.com"}`, words: "This command requires approval",
			kind: refusalPlain, wantEntry: "Bash(ssh:*)",
		},
		{
			name: "a tool the allowlist doesn't name", tool: "WebFetch",
			input: `{"url":"https://example.com","prompt":"title"}`,
			words: "Claude requested permissions to use WebFetch, but you haven't granted it yet.",
			kind:  refusalPlain, wantEntry: "WebFetch",
		},
		{
			name: "an echo of $? after a ;", tool: "Bash",
			input: `{"command":"git status; echo \"exit=$?\""}`,
			words: `This Bash command contains multiple operations. The following part requires approval: echo "exit=$?"`,
			kind:  refusalPart, wantEntry: "Bash(echo:*)",
		},
		{
			name: "a $VAR", tool: "Bash",
			input: `{"command":"git -C $PWD status"}`, words: "A variable in this command can't be checked before it runs",
			kind: refusalUngrantable, shape: shapeVariable, wantWhy: addToolsUngrantable,
		},
		{
			name: "a variable the command sets", tool: "Bash",
			input: `{"command":"X=hello; echo $X"}`, words: "A variable set in this command can't be checked before it runs",
			kind: refusalUngrantable, shape: shapeVariable, wantWhy: addToolsUngrantable,
		},
		{
			name: "a $(…)", tool: "Bash",
			input: `{"command":"ls $(pwd)"}`, words: "A nested command in this command can't be checked before it runs",
			kind: refusalUngrantable, shape: shapeNested, wantWhy: addToolsUngrantable,
		},
		{
			name: "a cd joined to git", tool: "Bash",
			input: `{"command":"cd .. && git status"}`,
			words: "This command changes directory before running a version-control command, which can pick up " +
				"untrusted hooks or repository configuration from the target directory. Approve only if you trust it.",
			kind: refusalUngrantable, shape: shapeCdVCS, wantWhy: addToolsUngrantable,
		},
		{
			name: "dontAsk's one line for every refusal", tool: "Bash",
			input: `{"command":"curl -s https://example.com"}`, words: dontAskWords,
			kind: refusalUnrecognized, wantWhy: addToolsUnrecognized,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			rep := observeLines(t,
				toolUseID("toolu_1", c.tool, c.input),
				permissionDenied("toolu_1", c.words),
				toolResult("toolu_1", c.words, true),
				`{"type":"result","subtype":"success","result":"Stopping here."}`,
			)
			if !rep.permissionRefused {
				t.Fatal("permissionRefused should latch — the CLI said it refused the call")
			}
			if len(rep.refusals) != 1 {
				t.Fatalf("got %d refusals, want 1: %+v", len(rep.refusals), rep.refusals)
			}
			r := rep.refusals[0]
			if r.tool != c.tool || r.kind != c.kind || r.shape != c.shape {
				t.Errorf("refusal = %+v, want tool %q, kind %q, shape %q", r, c.tool, c.kind, c.shape)
			}
			if entry, why := addToolsEntry(r, defaultTools); entry != c.wantEntry || why != c.wantWhy {
				t.Errorf("addToolsEntry = (%q, %q), want (%q, %q)", entry, why, c.wantEntry, c.wantWhy)
			}
		})
	}
}

// Words refusalWordings doesn't know — dontAsk's, a hook's, the next CLI
// release's — still count when the CLI's own event names the call. Without
// the event, the same tool_result reads as an ordinary error: the gap the
// event closes, since a CLI new enough to reword a refusal sends one.
func TestObserveCountsARefusalInUnknownWordsOnlyOffItsEvent(t *testing.T) {
	t.Parallel()
	use := toolUseID("toolu_1", "Bash", `{"command":"curl -s https://example.com"}`)
	refused := toolResult("toolu_1", dontAskWords, true)
	end := `{"type":"result","subtype":"success","result":"Stopping here."}`

	if rep := observeLines(t, use, permissionDenied("toolu_1", dontAskWords), refused, end); !rep.permissionRefused {
		t.Error("permissionRefused should latch off the event, whatever its words")
	}
	if rep := observeLines(t, use, refused, end); rep.permissionRefused {
		t.Error("with no event, an error in unknown words is not evidence of a refusal")
	}
}

// #209 keeps a working-directory block out of the refusals — no -add-tools
// entry reaches a path outside the session — and 2.1.312 sends a
// permission_denied event for one too. The event must not overturn that.
func TestObserveLeavesAWorkingDirectoryBlockOutDespiteItsEvent(t *testing.T) {
	t.Parallel()
	const words = "ls in '/' was blocked. For security, Claude Code may only list files in the " +
		"allowed working directories for this session: '/x/scratch'."
	rep := observeLines(t,
		toolUseID("toolu_1", "Bash", `{"command":"ls /"}`),
		permissionDenied("toolu_1", words),
		toolResult("toolu_1", words, true),
		`{"type":"result","subtype":"success","result":"Opened a PR."}`,
	)
	if rep.permissionRefused || len(rep.refusals) != 0 {
		t.Errorf("a working-directory block latched as a refusal: %+v", rep.refusals)
	}
}

// The event and the tool_result are two reports of one refusal. It counts
// once, and the id the event left behind goes with the tool_result, so a
// long run's marks don't pile up — nor do they from an event for a call no
// tool_result will answer again.
func TestObserveCountsADeniedCallOnce(t *testing.T) {
	t.Parallel()
	const words = "A nested command in this command can't be checked before it runs"
	use := toolUseID("toolu_1", "Bash", `{"command":"ls $(pwd)"}`)
	for name, lines := range map[string][]string{
		"event first, as the CLI sends it": {use, permissionDenied("toolu_1", words), toolResult("toolu_1", words, true)},
		"event after its tool_result":      {use, toolResult("toolu_1", words, true), permissionDenied("toolu_1", words)},
		"event for a call never seen":      {use, toolResult("toolu_1", words, true), permissionDenied("toolu_9", words)},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			rep := observeLines(t, lines...)
			if len(rep.refusals) != 1 {
				t.Errorf("got %d refusals for one refused call, want 1: %+v", len(rep.refusals), rep.refusals)
			}
			if len(rep.deniedCalls) != 0 {
				t.Errorf("deniedCalls = %v at the end, want it empty", rep.deniedCalls)
			}
		})
	}
}

// The sandbox's block reads differently per command, but always names the
// sandbox in the same sentence — and a refusal never does.
func TestWorkingDirBlockMatchesEachCommandsBlock(t *testing.T) {
	t.Parallel()
	for _, s := range []string{
		"ls in '/' was blocked. For security, Claude Code may only list files in the allowed working directories for this session: '/x'.",
		"cd in '/etc' was blocked. For security, Claude Code may only change directories to the allowed working directories for this session: '/x'.",
		"rm in '/x/y.md' was blocked. For security, Claude Code may only remove files from the allowed working directories for this session: '/x'.",
	} {
		if !workingDirBlock(s) {
			t.Errorf("should read as the sandbox's block: %s", clip(s, 60))
		}
	}
	for _, s := range []string{
		"This command requires approval",
		"A variable in this command can't be checked before it runs",
		dontAskWords,
		"error: the commit was blocked. See below.\nFor security, Claude Code may only ...",
	} {
		if workingDirBlock(s) {
			t.Errorf("should not read as the sandbox's block: %s", clip(s, 60))
		}
	}
}

// Issue #182: on #169 the run asked for an ungranted tool in a turn partway
// through, then ended on a different sentence the head anchor could not catch,
// and parked as "no PR and no questions". observe now reads every assistant
// turn, not only the result text, so the ask is not lost.
func TestObserveReadsAPermissionAskFromAnEarlierTurn(t *testing.T) {
	t.Parallel()
	turn := func(text string) streamEvent {
		ev, ok := parseEvent([]byte(`{"type":"assistant","message":{"content":[{"type":"text","text":` +
			strconv.Quote(text) + `}]}}`))
		if !ok {
			t.Fatalf("could not build assistant turn: %q", text)
		}
		return ev
	}

	// #169's actual final text, verbatim so the wording that slipped through
	// cannot regress: on its own it still does not read as a permission stop —
	// the ask is not at the head, and the head anchor is right to miss it.
	const finalText = "The sandbox restricts Bash to the launch directory, so I " +
		"need the `EnterWorktree` tool to move into `/Users/stef/code/polako-issue-169` " +
		"(already created via `git worktree add`) …"
	if permissionRefusal(finalText) {
		t.Fatal("#169's final text is not itself an approval request")
	}

	var rep runReport
	rep.observe(turn("Looking at the worktree layout now."))
	rep.observe(turn("This requires user confirmation to proceed. I'll wait for approval before continuing."))
	rep.observe(turn("Still blocked on that."))
	rep.observe(streamEvent{Type: "result", Subtype: "success", Result: finalText})

	if rep.permissionRefused {
		t.Error("permissionRefused should stay false — the final text was not the ask")
	}
	if !rep.permissionAsked {
		t.Error("permissionAsked should be true — an earlier turn asked for approval")
	}

	// A turn that only quotes the phrase mid-sentence must not latch it, the
	// same false positive the head anchor closes on the result text.
	var quoting runReport
	quoting.observe(turn("Earlier the run said \"This requires user confirmation\" and then stopped; fixing that."))
	quoting.observe(streamEvent{Type: "result", Subtype: "success", Result: "Opened a PR."})
	if quoting.permissionAsked {
		t.Error("permissionAsked should be false — the turn quoted the phrase, it did not ask")
	}

	// A mid-run turn that opens by reporting a missing permission and then
	// works around it is not a stop-to-ask: the "I lack permission" phrasings
	// are in permissionRefusal for a final-word match only, not the mid-run
	// scan, so this must not latch.
	var workaround runReport
	workaround.observe(turn("I don't have permission to run the full suite here, but I'll run the affected package."))
	workaround.observe(streamEvent{Type: "result", Subtype: "success", Result: "Opened a PR."})
	if workaround.permissionAsked {
		t.Error("permissionAsked should be false — the turn described a wall it then went around")
	}
}
