package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// designTestConfig is drainConfig pinned the way designConfig pins it, so
// processIssue runs as a design run against the fake CLIs.
func designTestConfig(t *testing.T, mode string, st *ghState) (config, string) {
	t.Helper()
	cfg, path := drainConfig(t, mode, st)
	cfg.skill = defaultDesignSkill
	cfg.verb = designVerb
	cfg.visualEvidence = true
	return cfg, path
}

func TestVerbUsageListsDesign(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	verbUsage(&b)
	if !strings.Contains(b.String(), "\n  design ") {
		t.Errorf("verbUsage does not list `design`:\n%s", b.String())
	}
}

// design takes work's per-issue flags and its own two, and nothing that
// belongs to a queue, a session or the size/remediation policy.
func TestDesignRegistersOnlyPerIssueFlags(t *testing.T) {
	t.Parallel()
	var cfg config
	var opt designOptions
	var local localFlags
	fs := designFlagSet(io.Discard, &cfg, &opt, &local)

	for name, def := range map[string]string{
		"skill": defaultDesignSkill, "tools": designTools, "model": "opus",
		"issue": "0", "wait": "false", "max-issue-time": defaultMaxIssueTime.String(),
	} {
		f := fs.Lookup(name)
		if f == nil {
			t.Errorf("-%s is not registered", name)
			continue
		}
		if f.DefValue != def {
			t.Errorf("-%s defaults to %q, want %q", name, f.DefValue, def)
		}
	}
	for _, name := range []string{"dir", "claude", "branch-prefix", "add-tools", "permission-mode", "effort",
		"poll", "retries", "retry-wait", "stall", "heartbeat", "max-cost", "notify", "remote", "run-tag",
		"post-summary", "metrics", "log", "verbose", "ignore-skew", "dry-run"} {
		if fs.Lookup(name) == nil {
			t.Errorf("-%s is not registered", name)
		}
	}
	for _, name := range []string{"label", "ungated", "skip", "once", "strict-order", "max-session-cost",
		"max-session-usage", "max-week-usage", "visual-evidence", "effort-by-size", "model-by-size",
		"remediation-model", "remediation-effort"} {
		if fs.Lookup(name) != nil {
			t.Errorf("-%s is registered, but it is a queue, session or policy flag design has no use for", name)
		}
	}
	for _, grant := range []string{"Bash(gh issue list:*)", "Bash(gh search issues:*)"} {
		if !strings.HasPrefix(designTools, defaultTools+",") || !strings.Contains(designTools, grant) {
			t.Errorf("designTools should be defaultTools plus %s: %s", grant, designTools)
		}
	}
}

func TestDesignConfigPinsVerbEvidenceAndWait(t *testing.T) {
	t.Parallel()
	for _, wait := range []bool{false, true} {
		args := []string{"-issue", "7", "-metrics", "off", "-log", "off"}
		if wait {
			args = append(args, "-wait")
		}
		cfg, opt, err := parseDesignFlags(args, io.Discard)
		if err != nil {
			t.Fatalf("parseDesignFlags(%v): %v", args, err)
		}
		if opt.issue != 7 || cfg.verb != designVerb || !cfg.visualEvidence || cfg.strictOrder != wait {
			t.Errorf("wait=%v: issue %d, verb %q, visualEvidence %v, strictOrder %v", wait,
				opt.issue, cfg.verb, cfg.visualEvidence, cfg.strictOrder)
		}
		if cfg.ghBin != "gh" || cfg.shiftID == "" || !filepath.IsAbs(cfg.dir) {
			t.Errorf("designConfig did not pin what parseFlags pins: ghBin %q, shiftID %q, dir %q",
				cfg.ghBin, cfg.shiftID, cfg.dir)
		}
	}

	for _, bad := range [][]string{
		{},                             // no -issue
		{"-issue", "0"},                // not an issue
		{"-issue", "7", "extra"},       // stray argument
		{"-issue", "7", "-label", "x"}, // a queue flag
		{"-issue", "7", "-effort", "ultracode"},
	} {
		if _, _, err := parseDesignFlags(bad, io.Discard); err == nil {
			t.Errorf("parseDesignFlags(%v) accepted it", bad)
		}
	}
	if !envExempt["issue"] {
		t.Error("POLAKO_ISSUE would pick the issue for a bare `polako design` — -issue must be in envExempt")
	}
	if _, _, err := parseDesignFlags([]string{"-h"}, io.Discard); !errors.Is(err, flag.ErrHelp) {
		t.Errorf("-h = %v, want flag.ErrHelp", err)
	}
}

// The startup recap names nothing a design run doesn't do.
func TestDesignPairsDropWhatOneIssueDoesNot(t *testing.T) {
	t.Parallel()
	cfg := config{shiftID: "s1", rec: newRecorder(t.TempDir()), notifyCmd: "tell-me", strictOrder: true}
	rows := map[string]string{}
	for _, p := range designPairs(cfg) {
		rows[p[0]] = p[1]
	}
	for _, gone := range []string{"epics", "shift"} {
		if _, ok := rows[gone]; ok {
			t.Errorf("design's recap has a %q row: %v", gone, rows)
		}
	}
	if strings.Contains(rows["notify"], "backlog clears") {
		t.Errorf("notify row promises a backlog event: %q", rows["notify"])
	}
	if rows["wait"] == "" {
		t.Errorf("-wait on, but no wait row: %v", rows)
	}
}

// Each refusal names the move that fixes it, and none writes the design label.
func TestDesignPreflightRefusesWithTheRemedy(t *testing.T) {
	t.Parallel()
	_, checkout := upstream(t)
	for name, tc := range map[string]struct {
		issue fakeIssue
		want  string
	}{
		"closed":      {fakeIssue{Open: false}, "reopen it"},
		"container":   {fakeIssue{Open: true, SubIssues: 2}, "a design request has no sub-issues"},
		"needs-human": {fakeIssue{Open: true, Labels: []string{needsHumanLabel}}, "polako unpark 1"},
		"proposed":    {fakeIssue{Open: true, Labels: []string{proposedLabel}}, "drop proposed first"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			is := tc.issue
			cfg, path := designTestConfig(t, "design", &ghState{Issues: map[string]*fakeIssue{"1": &is}})
			cfg.dir = checkout
			err := designPreflight(context.Background(), &cfg, 1)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("designPreflight = %v, want a refusal saying %q", err, tc.want)
			}
			if slices.Contains(finalGhState(t, path).Issues["1"].Labels, designLabel) {
				t.Error("a refused issue was labelled design anyway")
			}
		})
	}
}

// Naming an issue is the human act; preflight labels it so work stays off it,
// and declares both labels a run can write. A dry run does neither.
func TestDesignPreflightLabelsAnUnlabelledIssue(t *testing.T) {
	t.Parallel()
	_, checkout := upstream(t)
	for name, dry := range map[string]bool{"real": false, "dry-run": true} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			buf := captureLog(t)
			cfg, path := designTestConfig(t, "design", &ghState{Issues: map[string]*fakeIssue{"1": {Open: true}}})
			cfg.dir = checkout
			cfg.dryRun = dry
			if err := designPreflight(context.Background(), &cfg, 1); err != nil {
				t.Fatalf("designPreflight: %v", err)
			}
			st := finalGhState(t, path)
			labelled := slices.Contains(st.Issues["1"].Labels, designLabel)
			declared := slices.Contains(st.Labels, designLabel) && slices.Contains(st.Labels, awaitingAnswerLabel)
			if labelled == dry || declared == dry {
				t.Errorf("issue labels %v, repo labels %v", st.Issues["1"].Labels, st.Labels)
			}
			want := `added the "design" label`
			if dry {
				want = "a real run would add it"
			}
			if !strings.Contains(buf.String(), want) {
				t.Errorf("log is missing %q\ngot:\n%s", want, buf.String())
			}
		})
	}
}

func TestDesignRunsALabelledIssueToMerge(t *testing.T) {
	t.Parallel()
	buf := captureLog(t)
	cfg, path := designTestConfig(t, "design", &ghState{
		Issues: map[string]*fakeIssue{"1": {Open: true, Labels: []string{designLabel}}},
		Labels: []string{designLabel, awaitingAnswerLabel},
	})
	metrics := t.TempDir()
	cfg.rec = newRecorder(metrics)
	cfg.shiftID = "shift-design"
	args := watchClaudeArgs(t, &cfg)

	if err := designRun(context.Background(), cfg, 1); err != nil {
		t.Fatalf("designRun: %v", err)
	}
	if finalGhState(t, path).Issues["1"].Open {
		t.Error("issue 1 is still open after its PR merged")
	}
	if got := args(); len(got) != 1 {
		t.Errorf("claude ran %d times, want once: %v", len(got), got)
	}
	kinds := map[string]bool{}
	for _, line := range readRecords(t, metrics, cfg.repo) {
		var rec struct {
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("record is not JSON: %v\n%s", err, line)
		}
		kinds[rec.Kind] = true
	}
	if !kinds["design-run"] || !kinds["design-issue"] || kinds["run"] || kinds["issue"] {
		t.Errorf("record kinds = %v, want design-run and design-issue only", kinds)
	}
	out := buf.String()
	for _, want := range []string{
		"could not sweep finished worktrees", // the sweep ran (against a non-checkout)
		"issue #1: PR #42 merged",
		"polako plan -design",
		"summary: 1 issue merged, 0 issues parked, $0.50 spent",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log is missing %q\ngot:\n%s", want, out)
		}
	}
}

// Restart safety: a PR already on the branch means the skill never runs.
func TestDesignWithAnOpenPRSkipsTheClaudeRun(t *testing.T) {
	t.Parallel()
	captureLog(t)
	cfg, path := designTestConfig(t, "design", &ghState{
		Issues: map[string]*fakeIssue{"1": {Open: true, Labels: []string{designLabel}}},
		PRs:    map[string]*fakePR{"issue-1": {Number: 42, State: "OPEN", Mergeable: "MERGEABLE", MergeOnRead: 1}},
	})
	args := watchClaudeArgs(t, &cfg)

	if err := designRun(context.Background(), cfg, 1); err != nil {
		t.Fatalf("designRun: %v", err)
	}
	if got := args(); len(got) != 0 {
		t.Errorf("claude ran with a PR already open: %v", got)
	}
	if finalGhState(t, path).Issues["1"].Open {
		t.Error("issue 1 is still open after its PR merged")
	}
}

// A question ends the verb cleanly: exit 0, the label left up for a human,
// one notification, and the exact rerun line.
func TestDesignExitsZeroOnAQuestion(t *testing.T) {
	t.Parallel()
	buf := captureLog(t)
	cfg, path := designTestConfig(t, "designasks", &ghState{
		Issues: map[string]*fakeIssue{"1": {Open: true, Labels: []string{designLabel}}},
		Labels: []string{designLabel, awaitingAnswerLabel},
	})
	told := notifyLog(t, &cfg)

	if err := designRun(context.Background(), cfg, 1); err != nil {
		t.Fatalf("designRun: %v", err)
	}
	if got := finalGhState(t, path).Issues["1"].Labels; !slices.Contains(got, awaitingAnswerLabel) {
		t.Errorf("issue 1 labels = %v, want the question left flagged", got)
	}
	if got := told(); len(got) != 1 || !strings.Contains(got[0], notifyPrefix+"EVENT="+notifyAwaiting) {
		t.Errorf("notifications = %v, want exactly one %s", got, notifyAwaiting)
	}
	out := buf.String()
	for _, want := range []string{
		"reply on the thread, then rerun polako design -issue 1",
		"1 issue awaiting an answer",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log is missing %q\ngot:\n%s", want, out)
		}
	}
}

// -wait is the opt-in: the same question is waited out in one process, and
// the rerun it dispatches ships.
func TestDesignWaitRunsToMergeInOneProcess(t *testing.T) {
	t.Parallel()
	captureLog(t)
	cfg, path := designTestConfig(t, "designasks", &ghState{
		Issues: map[string]*fakeIssue{"1": {Open: true, Labels: []string{designLabel}}},
		Labels: []string{designLabel, awaitingAnswerLabel},
	})
	cfg.strictOrder = true // what -wait sets

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := designRun(ctx, cfg, 1); err != nil {
		t.Fatalf("designRun: %v", err)
	}
	is := finalGhState(t, path).Issues["1"]
	if is.Open || slices.Contains(is.Labels, awaitingAnswerLabel) {
		t.Errorf("issue 1 open=%v labels=%v, want it merged and the flag cleared", is.Open, is.Labels)
	}
}

func TestDesignDryRunWritesNothing(t *testing.T) {
	t.Parallel()
	captureLog(t)
	_, checkout := upstream(t)
	cfg, _ := designTestConfig(t, "design", &ghState{Issues: map[string]*fakeIssue{"1": {Open: true}}})
	cfg.dir = checkout
	cfg.dryRun = true
	ghLog := filepath.Join(t.TempDir(), "gh.log")
	setFakeEnv(&cfg, fakeGhLogEnv, ghLog)
	args := watchClaudeArgs(t, &cfg)

	var out bytes.Buffer
	if err := runDesign(context.Background(), cfg, 1, &out); err != nil {
		t.Fatalf("runDesign -dry-run: %v", err)
	}
	if !strings.Contains(out.String(), "'/polako:design-plan 1'") {
		t.Errorf("dry run printed %q, want the design-plan invocation", out.String())
	}
	b, err := os.ReadFile(ghLog)
	if err != nil {
		t.Fatal(err)
	}
	for _, write := range []string{"label create", "issue edit", "issue comment", "issue close"} {
		if strings.Contains(string(b), write) {
			t.Errorf("dry run made a %q call:\n%s", write, b)
		}
	}
	for _, a := range args() {
		if strings.Contains(a, "design-plan") {
			t.Errorf("dry run started a claude run: %s", a)
		}
	}
}

// A gh that stops answering is fatal, and a fatal exit tells the operator.
func TestDesignFatalFiresStopped(t *testing.T) {
	t.Parallel()
	buf := captureLog(t)
	cfg, _ := designTestConfig(t, "design", &ghState{
		Issues:    map[string]*fakeIssue{"1": {Open: true, Labels: []string{designLabel}}},
		FailReads: map[string]int{"pr list": 100},
	})
	told := notifyLog(t, &cfg)

	if err := designRun(context.Background(), cfg, 1); err == nil {
		t.Fatal("designRun succeeded with gh unable to answer")
	}
	if got := told(); len(got) != 1 || !strings.Contains(got[0], notifyPrefix+"EVENT="+notifyStopped) {
		t.Errorf("notifications = %v, want exactly one %s", got, notifyStopped)
	}
	// Unfinished is not an outcome: no summary calls it merged.
	if strings.Contains(buf.String(), "summary:") {
		t.Errorf("a fatal exit printed a summary\ngot:\n%s", buf.String())
	}
}
