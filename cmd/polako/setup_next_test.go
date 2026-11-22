package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTestFile(t *testing.T, dir, rel string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("# doc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

var readyRows = []setupRow{{name: "gh", status: setupOK}, {name: "branch protection", status: setupMissing}}

// A required row missing names that row and the rerun, and no `work` line:
// work would refuse, or worse, start.
func TestSetupNextStepNamesTheFirstRequiredFix(t *testing.T) {
	t.Parallel()
	rows := []setupRow{
		{name: "CI workflow", status: setupMissing},
		{name: "gh repo view", status: setupMissing, required: true},
		{name: needsHumanLabel, status: setupMissing, required: true},
	}
	cfg := config{dir: t.TempDir()}
	got := setupNextStep(context.Background(), cfg, rows, "polako setup -dir x")
	want := "next: fix the gh repo view row, then rerun `polako setup -dir x`"
	if got != want {
		t.Errorf("setupNextStep = %q, want %q", got, want)
	}
}

func TestSetupNextStepWithOpenIssuesIsTheWorkLine(t *testing.T) {
	t.Parallel()
	_, checkout := upstream(t)
	writeTestFile(t, checkout, visionMdPath)
	cfg := setupCfg(t, &ghState{Issues: map[string]*fakeIssue{"7": {Open: true}}}, checkout)
	// The error path prints the work line too, so check the read itself.
	if open, err := hasOpenIssues(context.Background(), cfg); err != nil || !open {
		t.Fatalf("hasOpenIssues = %v, %v, want true, nil", open, err)
	}
	got := setupNextStep(context.Background(), cfg, readyRows, "")
	if want := suggestedWorkLine(cfg); got != want {
		t.Errorf("setupNextStep = %q, want the work line %q", got, want)
	}
}

func TestSetupNextStepWithNoIssuesPointsAtPlan(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		files []string
		doc   string
	}{
		{"docs vision", []string{visionMdPath, rootVisionPath}, visionMdPath},
		{"root vision", []string{rootVisionPath}, rootVisionPath},
		{"draft design", []string{designsReadmePath, "docs/designs/widgets.md"}, "docs/designs/widgets.md"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			_, checkout := upstream(t)
			for _, f := range c.files {
				writeTestFile(t, checkout, f)
			}
			// A closed issue is not something work can pick up.
			cfg := setupCfg(t, &ghState{Issues: map[string]*fakeIssue{"3": {Open: false}}}, checkout)
			got := setupNextStep(context.Background(), cfg, readyRows, "")
			want := "polako plan -dir " + checkout + " -design " + c.doc + " -dry-run"
			if got != want {
				t.Errorf("setupNextStep = %q, want %q", got, want)
			}
		})
	}
}

// No issues and no doc: both ways in, and the designs README alone is not a
// doc to plan from.
func TestSetupNextStepWithNothingNamesDesignAndHealth(t *testing.T) {
	t.Parallel()
	_, checkout := upstream(t)
	writeTestFile(t, checkout, designsReadmePath)
	cfg := setupCfg(t, &ghState{}, checkout)
	got := setupNextStep(context.Background(), cfg, readyRows, "")
	for _, want := range []string{`polako design -dir ` + checkout + ` -brief "<idea>"`, "polako health -dir " + checkout} {
		if !strings.Contains(got, want) {
			t.Errorf("setupNextStep = %q, want it to contain %q", got, want)
		}
	}
	if strings.Contains(got, "polako work") {
		t.Errorf("setupNextStep = %q, want no work line on an empty repo", got)
	}
}

func TestSetupRerunLineKeepsRepoAndLabel(t *testing.T) {
	t.Parallel()
	got := setupRerunLine(setupOptions{repo: "example/repo", policyLabels: true}, config{dir: "/x", label: "ready"})
	if want := "polako setup -dir /x -repo example/repo -label ready -policy-labels"; got != want {
		t.Errorf("setupRerunLine = %q, want %q", got, want)
	}
}

// A root VISION.md is found, not re-offered as a scaffold.
func TestSetupVisionRowNoticesARootVision(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeTestFile(t, dir, rootVisionPath)
	r := setupVisionRow(config{dir: dir})
	if r.status != setupOK || !strings.Contains(r.detail, "-design VISION.md") {
		t.Errorf("setupVisionRow = %+v, want ok naming -design VISION.md", r)
	}
}
