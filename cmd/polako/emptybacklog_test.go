package main

import (
	"context"
	"strings"
	"testing"
)

// Issue #736: an empty backlog names the commands that fill it, in work's
// last line and status's next row — a plan run when the checkout has a doc,
// design or health when it has none.

const noDocHint = "fill it with `polako design -brief \"<idea>\"` or `polako health`"

func TestDrainOnAnEmptyBacklogSaysHowToFillIt(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		files []string
		want  string
	}{
		{"no doc", nil, noDocHint},
		{"designs README alone", []string{designsReadmePath}, noDocHint},
		{"vision", []string{visionMdPath}, "fill it with `polako plan -design docs/VISION.md`"},
		{"draft design", []string{"docs/designs/widgets.md"}, "fill it with `polako plan -design docs/designs/widgets.md`"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			buf := captureLog(t)
			cfg, _ := drainConfig(t, "stream", &ghState{
				Issues: map[string]*fakeIssue{"1": {Open: false}},
			})
			for _, f := range c.files {
				writeTestFile(t, cfg.dir, f)
			}
			told := notifyLog(t, &cfg)
			if err := drain(context.Background(), cfg); err != nil {
				t.Fatalf("drain: %v", err)
			}
			out := buf.String()
			if want := "no open issues — backlog cleared; " + c.want; !strings.Contains(out, want) {
				t.Errorf("log is missing %q\ngot:\n%s", want, out)
			}
			// The notification's reason stays what it was.
			if got := strings.Join(told(), "\n"); !strings.Contains(got, "no open issues left to work") ||
				strings.Contains(got, "fill it") {
				t.Errorf("notifications = %q, want the cleared reason unchanged", got)
			}
		})
	}
}

// Every open issue held is not an empty backlog: no fill hint.
func TestDrainWithOnlyHeldIssuesGivesNoFillHint(t *testing.T) {
	t.Parallel()
	buf := captureLog(t)
	cfg, _ := drainConfig(t, "stream", &ghState{
		Issues: map[string]*fakeIssue{"1": {Open: true, Labels: []string{proposedLabel}}},
	})
	if err := drain(context.Background(), cfg); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if out := buf.String(); strings.Contains(out, "fill it") {
		t.Errorf("a proposed issue is still open, so no fill hint\ngot:\n%s", out)
	}
}

func TestStatusOnAnEmptyBacklogSaysHowToFillIt(t *testing.T) {
	t.Parallel()
	t.Run("no doc", func(t *testing.T) {
		t.Parallel()
		cfg, _ := statusConfigFor(t, &ghState{Issues: map[string]*fakeIssue{"1": {Open: false}}})
		out := renderedStatus(t, cfg)
		if want := "next   nothing — no open issue at all; " + noDocHint; !strings.Contains(out, want) {
			t.Errorf("report is missing %q\ngot:\n%s", want, out)
		}
		if strings.Contains(out, "needs you") {
			t.Errorf("no draft doc, so nothing needs you:\n%s", out)
		}
	})
	t.Run("draft design", func(t *testing.T) {
		t.Parallel()
		cfg, _ := statusConfigFor(t, &ghState{Issues: map[string]*fakeIssue{"1": {Open: false}}})
		writeTestFile(t, cfg.dir, designsReadmePath)
		writeTestFile(t, cfg.dir, "docs/designs/widgets.md")
		out := renderedStatus(t, cfg)
		for _, want := range []string{
			"next   nothing — no open issue at all; fill it with `polako plan -design docs/designs/widgets.md`",
			"propose tickets from docs/designs/widgets.md — polako plan -design docs/designs/widgets.md",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("report is missing %q\ngot:\n%s", want, out)
			}
		}
		if strings.Contains(out, "propose tickets from "+designsReadmePath) {
			t.Errorf("the designs README is not a plan:\n%s", out)
		}
	})
}

// A held backlog keeps naming its hold, with no fill hint.
func TestStatusWithOnlyHeldIssuesGivesNoFillHint(t *testing.T) {
	t.Parallel()
	cfg, _ := statusConfigFor(t, &ghState{
		Issues: map[string]*fakeIssue{"1": {Open: true, Labels: []string{proposedLabel}}},
	})
	if out := renderedStatus(t, cfg); strings.Contains(out, "fill it") {
		t.Errorf("a proposed issue is still open, so no fill hint\ngot:\n%s", out)
	}
}

func renderedStatus(t *testing.T, cfg config) string {
	t.Helper()
	snap, err := readStatus(context.Background(), cfg, statusNow)
	if err != nil {
		t.Fatalf("readStatus: %v", err)
	}
	var out strings.Builder
	renderStatus(&out, report{}, cfg, snap)
	return out.String()
}
