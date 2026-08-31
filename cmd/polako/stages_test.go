package main

import (
	"bytes"
	"slices"
	"strings"
	"testing"
)

// toolUse is one assistant event carrying a single tool call, the shape the
// stage narrator keys on.
func toolUse(name, input string) string {
	return `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"` +
		name + `","input":` + input + `}]}}`
}

// bash is the common case: a tool_use for Bash with just a command.
func bash(cmd string) string {
	return toolUse("Bash", `{"command":`+jsonString(cmd)+`}`)
}

func jsonString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// narratedStages runs a sequence of stream events through a fresh stageNarrator
// and returns the stage lines it emitted, in order, stripped of stamp and
// "[claude] " prefix.
func narratedStages(t *testing.T, events ...string) []string {
	t.Helper()
	buf := captureLog(t)
	var n stageNarrator
	for _, e := range events {
		ev, ok := parseEvent([]byte(e))
		if !ok {
			t.Fatalf("parseEvent rejected %s", e)
		}
		n.observe(ev)
	}
	var got []string
	for _, ln := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if i := strings.Index(ln, "[claude] "); i >= 0 {
			got = append(got, strings.TrimSpace(ln[i+len("[claude] "):]))
		}
	}
	return got
}

func TestStageNarrationHappyPath(t *testing.T) {
	got := narratedStages(t,
		bash("gh issue view 214 --json number,title,state,body,comments"),
		bash("git worktree list"),
		bash("git worktree add ../polako-issue-214 -b issue-214 origin/main"),
		toolUse("Read", `{"file_path":"/Users/x/polako-issue-214/cmd/polako/main.go"}`),
		toolUse("Grep", `{"pattern":"logEvent"}`),
		toolUse("Write", `{"file_path":"/Users/x/polako-issue-214/PLAN.md","content":"..."}`),
		toolUse("Edit", `{"file_path":"/Users/x/polako-issue-214/cmd/polako/stages.go","old_string":"a","new_string":"b"}`),
		toolUse("Skill", `{"skill":"code-review","args":"high --fix issue-214"}`),
		bash(`gh pr create --head issue-214 --title "feat: x" --body-file PR_BODY.md`),
	)
	want := []string{
		"reading the issue…",
		"preparing the branch…",
		"reading the code…",
		"writing the plan…",
		"implementing…",
		"running the review gate…",
		"opening the PR…",
	}
	if !slices.Equal(got, want) {
		t.Errorf("stage lines =\n%v\nwant\n%v", got, want)
	}
}

// The first recognised signal names its own phase and nothing before it: a run
// whose stream opens on the PLAN.md write does not backfill "reading the
// issue…" and "preparing the branch…".
func TestStageNarrationNeverBackfills(t *testing.T) {
	got := narratedStages(t,
		toolUse("Write", `{"file_path":"PLAN.md","content":"..."}`),
		toolUse("Read", `{"file_path":"main.go"}`), // study < plan: silent
		toolUse("Edit", `{"file_path":"main.go","old_string":"a","new_string":"b"}`),
	)
	want := []string{"writing the plan…", "implementing…"}
	if !slices.Equal(got, want) {
		t.Errorf("stage lines = %v, want %v", got, want)
	}
}

// A repeated signal for a phase already reported says nothing the second time.
func TestStageNarrationEmitsEachPhaseAtMostOnce(t *testing.T) {
	got := narratedStages(t,
		bash("gh issue view 214 --json body"),
		bash("gh issue view 214 --json comments"),
		toolUse("Read", `{"file_path":"a.go"}`),
		toolUse("Glob", `{"pattern":"**/*.go"}`),
	)
	want := []string{"reading the issue…", "reading the code…"}
	if !slices.Equal(got, want) {
		t.Errorf("stage lines = %v, want %v", got, want)
	}
}

// A stream of calls the recognizer does not know contributes nothing — no
// fallback stage, no filler.
func TestStageNarrationStaysSilentOnAnUnrecognisedStream(t *testing.T) {
	got := narratedStages(t,
		`{"type":"system","subtype":"init","model":"claude-opus-5"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"thinking it over"}]}}`,
		bash("go test ./..."),
		bash("gh pr view 41"),
		toolUse("TodoWrite", `{"todos":[]}`),
		`{"type":"result","subtype":"success","result":"done"}`,
	)
	if len(got) != 0 {
		t.Errorf("an unrecognised stream narrated %v, want nothing", got)
	}
}

// The review gate forks an agent that makes its own worktree and re-reads the
// code minutes after the gate opened. Monotonicity is what keeps that from
// reporting "preparing the branch…" on the way to a PR.
func TestStageNarrationIgnoresThePostReviewWorktree(t *testing.T) {
	got := narratedStages(t,
		toolUse("Skill", `{"skill":"code-review","args":"high --fix issue-139"}`),
		bash("git worktree add /tmp/issue139-wt issue-139"),
		toolUse("Read", `{"file_path":"/tmp/issue139-wt/main.go"}`),
		bash("git checkout issue-139"),
		bash(`gh pr create --head issue-139 --title "x" --body-file PR_BODY.md`),
	)
	want := []string{"running the review gate…", "opening the PR…"}
	if !slices.Equal(got, want) {
		t.Errorf("stage lines = %v, want %v", got, want)
	}
}

// The asking line fires at most once, from any position, and neither advances
// nor blocks the chain.
func TestStageNarrationAskingLine(t *testing.T) {
	got := narratedStages(t,
		bash("gh issue view 214 --json body"),
		bash("gh issue comment 214 --body-file q.md"),
		bash("gh issue comment 214 --body more"), // no repeat
		toolUse("Read", `{"file_path":"a.go"}`),  // chain still advances
	)
	want := []string{"reading the issue…", "asking on the issue thread…", "reading the code…"}
	if !slices.Equal(got, want) {
		t.Errorf("stage lines = %v, want %v", got, want)
	}
}

func TestStageNarrationAskingFromFirstPosition(t *testing.T) {
	got := narratedStages(t, bash("gh issue comment 214 --body-file q.md"))
	if !slices.Equal(got, []string{"asking on the issue thread…"}) {
		t.Errorf("stage lines = %v", got)
	}
}

// Two tool calls in one assistant event are each considered; monotonicity still
// holds, so a Read alongside `gh issue view` lands on the later phase only when
// the block order puts context first.
func TestStageNarrationHandlesParallelToolCalls(t *testing.T) {
	got := narratedStages(t,
		`{"type":"assistant","message":{"content":[`+
			`{"type":"tool_use","name":"Bash","input":{"command":"gh issue view 214 --json body"}},`+
			`{"type":"tool_use","name":"Read","input":{"file_path":"main.go"}}]}}`,
	)
	want := []string{"reading the issue…", "reading the code…"}
	if !slices.Equal(got, want) {
		t.Errorf("stage lines = %v, want %v", got, want)
	}
}

// Wired through eventLog.event, a stage line is a milestone: it reaches the
// terminal and the shift log on the same terms as "session started".
func TestStageNarrationIsAMilestoneOnBothSinks(t *testing.T) {
	var term, file bytes.Buffer
	wireSinks(t, &ui{terminal: &term, file: &file})

	var el eventLog
	for _, e := range []string{
		`{"type":"system","subtype":"init","model":"claude-opus-5","session_id":"s1"}`,
		bash("gh issue view 214 --json body"),
	} {
		ev, ok := parseEvent([]byte(e))
		if !ok {
			t.Fatalf("parseEvent rejected %s", e)
		}
		el.event(ev)
	}
	for _, sink := range []struct {
		name string
		got  string
	}{{"terminal", term.String()}, {"shift log", file.String()}} {
		if !strings.Contains(sink.got, "[claude] reading the issue…") {
			t.Errorf("%s missing the stage milestone\ngot:\n%s", sink.name, sink.got)
		}
	}
}
