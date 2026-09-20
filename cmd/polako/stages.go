package main

// Stage narration: a `polako work` run spends most of half an hour between its
// "session started" and "finished" milestones, and until now the terminal said
// nothing in between. This reads the one thing already in the event stream that
// says how far an `implement-issue` run has got — its tool calls — and turns it
// into at most one milestone per phase, so an operator glancing over sees the
// map rather than a gap.
//
// Two properties make it safe to key on calls as ordinary as Read:
//
//   - It only moves forward. A signal for a phase at or below the one already
//     reached says nothing. The review gate forks an agent that creates its own
//     worktree and re-reads the code minutes after the gate opened; without
//     monotonicity that activity would report "preparing the branch…" on the
//     way to a PR.
//   - It never backfills. A run whose first recognised signal is the PLAN.md
//     write says "writing the plan…" and does not also claim the phases it
//     skipped — only what was observed is said.
//
// It keys on tool calls alone, never on assistant prose (the model's words are
// not a contract). And nothing branches on a stage: these values reach no
// runReport field, no park reason, no record, no notify payload — they are
// narration and only narration.
//
// A `plan` or `health` run is a different shape — it reads, then files issues,
// and the only file it writes is a scratch issue body — so the implement-issue
// map misreads it: that scratch write narrated "implementing…" for a run that
// implements nothing. Intake mode has its own two-stop chain, study then
// filing, plus one line per issue as gh confirms it. Same rules: forward only,
// no backfill, narration only. The cap counts creates on its own, in
// runReport.observe; nothing here feeds it.

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

// stage is one point in an implement-issue run's progression. The chain is
// ordered, and stageNarrator only ever moves up it. stageAsking sits past the
// end deliberately: it is not part of the chain, is reached from any position,
// and advances nothing.
type stage int

const (
	stageNone stage = iota
	stageContext
	stageWorkspace
	stageStudy
	stagePlan
	stageImplement
	stageReview
	stagePR
	stageFiling // intake runs only: past every drain stage, so neither chain can reach the other's
	stageAsking
)

// stageLine is the milestone text for a stage, without the shared "[claude] "
// prefix its one caller adds. "" for stageNone, which is never narrated.
func stageLine(s stage) string {
	switch s {
	case stageContext:
		return "reading the issue…"
	case stageWorkspace:
		return "preparing the branch…"
	case stageStudy:
		return "reading the code…"
	case stagePlan:
		return "writing the plan…"
	case stageImplement:
		return "implementing…"
	case stageReview:
		return "running the review gate…"
	case stagePR:
		return "opening the PR…"
	case stageFiling:
		return "filing proposals…"
	case stageAsking:
		return "asking on the issue thread…"
	}
	return ""
}

// stageNarrator is the per-invocation recognizer. Its zero value is ready to
// use, and it belongs to one eventLog — one claude invocation — for the reason
// spelled out there: a shift works many issues through one process, and state
// that outlived an invocation would carry one issue's phase into the next run.
type stageNarrator struct {
	intake  bool            // a plan or health run: the study → filing chain, not implement-issue's
	reached stage           // highest chain stage narrated so far
	asked   bool            // the off-chain "asking" line has fired
	filing  map[string]bool // intake: ids of `gh issue create` calls still awaiting their result
}

// phase is the furthest chain stage the narrator has reached — what the
// heartbeat line names to say where a quiet run is. stageNone until the first
// recognised signal, which the caller renders as no stage clause. The
// off-chain "asking" line does not move it: a run that asked a question is
// about to stop, not sit quietly in a phase.
func (n *stageNarrator) phase() stage { return n.reached }

// observe folds one stream event into the narrator, emitting a milestone when —
// and only when — it advances the run past a phase it had not yet reported.
func (n *stageNarrator) observe(ev streamEvent) {
	if n.intake {
		n.observeIntake(ev)
		return
	}
	if ev.Type != "assistant" {
		return
	}
	for _, c := range ev.Message.Content {
		if c.Type != "tool_use" {
			continue
		}
		var in map[string]any
		_ = json.Unmarshal(c.Input, &in)

		if !n.asked && c.Name == "Bash" && cmdHasWords(strOf(in, "command"), "gh issue comment") {
			// Off the chain: a question on the thread can happen at any point
			// and is the tell that this run ends in a question, not a PR. It
			// neither advances nor blocks the chain.
			n.asked = true
			narrate(sevProgress, "[claude] %s", stageLine(stageAsking))
			continue
		}

		if s := recognizeStage(c.Name, in); s > n.reached {
			n.reached = s
			narrate(sevProgress, "[claude] %s", stageLine(s))
		}
	}
}

// filedIssue pulls the issue number out of the URL `gh issue create` prints on
// success — gh's own output, not the model's words.
var filedIssue = regexp.MustCompile(`/issues/(\d+)`)

// observeIntake is observe for a plan or health run. A create is narrated
// twice over: the first one opens the filing stage, and each one's result
// names the issue it made. On the result, not the call, for the reason the cap
// counts there — a create in flight has filed nothing yet. A result with no
// issue URL in it — an error, a gh that printed something else — says nothing.
func (n *stageNarrator) observeIntake(ev streamEvent) {
	for _, c := range ev.Message.Content {
		switch {
		case ev.Type == "assistant" && c.Type == "tool_use":
			s := stageNone
			switch {
			case c.Name == "Read", c.Name == "Grep", c.Name == "Glob":
				s = stageStudy
			case isIssueCreate(c.Name, c.Input):
				s = stageFiling
				if c.ID != "" {
					if n.filing == nil {
						n.filing = make(map[string]bool)
					}
					n.filing[c.ID] = true
				}
			}
			if s > n.reached {
				n.reached = s
				narrate(sevProgress, "[claude] %s", stageLine(s))
			}
		case ev.Type == "user" && c.Type == "tool_result" && n.filing[c.ToolUseID]:
			delete(n.filing, c.ToolUseID)
			if c.IsError {
				continue
			}
			if m := filedIssue.FindStringSubmatch(toolResultContentText(c.ResultText)); m != nil {
				if num, err := strconv.Atoi(m[1]); err == nil {
					narrate(sevProgress, "[claude] filed #%d", num)
				}
			}
		}
	}
}

// recognizeStage maps one tool call to at most one chain stage, stageNone for
// anything it does not recognise. It is deliberately narrow: an unrecognised
// call contributes nothing rather than a guess.
func recognizeStage(name string, in map[string]any) stage {
	switch name {
	case "Read", "Grep", "Glob":
		return stageStudy
	case "Write", "Edit":
		// planFile is the package's one name for this file; match it whichever
		// separator the run's platform hands the model in an absolute path.
		fp := strOf(in, "file_path")
		if fp == planFile ||
			strings.HasSuffix(fp, "/"+planFile) || strings.HasSuffix(fp, `\`+planFile) {
			return stagePlan
		}
		// Phase 1 writes scratchDir's .gitignore before anything is studied or
		// planned. Narrated as a change, that one write would say
		// "implementing…" and — forward only — swallow the two stages before it.
		if strings.Contains(fp, "/"+scratchDir+"/") || strings.Contains(fp, `\`+scratchDir+`\`) {
			return stageNone
		}
		return stageImplement
	case "Skill":
		if strOf(in, "skill") == "code-review" {
			return stageReview
		}
		return stageNone
	case "Bash":
		return bashStage(strOf(in, "command"))
	}
	return stageNone
}

// bashStage recognises the shell commands that mark a phase boundary. The git
// verbs are matched without pinning the branch name: -branch-prefix changes it.
// The verb alone is enough because the only earlier git-branch calls in the
// skill's flow are Phase 1's own `git branch --list` existence checks — already
// the workspace phase, so narrating it there is right, not premature — and
// monotonicity covers every `git branch`/`checkout`/`switch` that comes after.
func bashStage(cmd string) stage {
	switch {
	case cmdHasWords(cmd, "gh issue view"):
		return stageContext
	case cmdHasWords(cmd, "gh pr create"):
		return stagePR
	case cmdHasWords(cmd, "git worktree add"),
		cmdHasWords(cmd, "git branch"),
		cmdHasWords(cmd, "git checkout"),
		cmdHasWords(cmd, "git switch"):
		return stageWorkspace
	}
	return stageNone
}

// cmdHasWords reports whether a shell command line contains sub as a run of
// whitespace-separated words — so "gh issue view" matches `gh issue view 214`
// and `gh  issue  view 214` but not a flag or path that merely embeds the
// letters.
func cmdHasWords(cmd, sub string) bool {
	norm := " " + strings.Join(strings.Fields(cmd), " ") + " "
	return strings.Contains(norm, " "+sub+" ")
}

func strOf(in map[string]any, key string) string {
	v, _ := in[key].(string)
	return v
}
