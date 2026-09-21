package main

// docs/plans/setup.md ticket 5's three advice rows: never required, so these
// tests only check status/detail, never setupFailed.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupCIWorkflowRow(t *testing.T) {
	t.Parallel()
	if row := setupCIWorkflowRow(config{dir: t.TempDir()}); row.status != setupMissing || row.required {
		t.Errorf("row = %+v, want missing and not required with no .github/workflows", row)
	}

	dir := t.TempDir()
	wf := filepath.Join(dir, ".github", "workflows")
	if err := os.MkdirAll(wf, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", wf, err)
	}
	if err := os.WriteFile(filepath.Join(wf, "ci.yml"), []byte("on: push\n"), 0o644); err != nil {
		t.Fatalf("writing ci.yml: %v", err)
	}
	if row := setupCIWorkflowRow(config{dir: dir}); row.status != setupOK {
		t.Errorf("row = %+v, want ok with a workflow present", row)
	}
}

// A review finding: a real read failure on .github/workflows (here, it
// exists as a plain file, not a directory) must not read as "no CI".
func TestSetupCIWorkflowRowUnknownOnAReadFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".github"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".github", "workflows"), nil, 0o644); err != nil {
		t.Fatalf("writing: %v", err)
	}
	if row := setupCIWorkflowRow(config{dir: dir}); row.status != setupUnknown {
		t.Errorf("row = %+v, want %q — a real read failure, not \"no CI\"", row, setupUnknown)
	}
}

func TestSetupBranchProtectionRow(t *testing.T) {
	t.Parallel()
	_, checkout := upstream(t)

	cfg := setupCfg(t, &ghState{BranchProtected: true}, checkout)
	if row := setupBranchProtectionRow(context.Background(), cfg, true, true); row.status != setupOK {
		t.Errorf("protected: row = %+v, want ok", row)
	}

	cfg = setupCfg(t, &ghState{}, checkout)
	if row := setupBranchProtectionRow(context.Background(), cfg, true, true); row.status != setupMissing || row.required {
		t.Errorf("unprotected: row = %+v, want missing and not required (advice only)", row)
	}

	cfg = setupCfg(t, &ghState{NoAdminAccess: true}, checkout)
	row := setupBranchProtectionRow(context.Background(), cfg, true, true)
	if row.status != setupUnknown {
		t.Errorf("no admin access: row = %+v, want %q", row, setupUnknown)
	}
	if !strings.Contains(row.detail, "admin") {
		t.Errorf("detail = %q, want it to name the admin requirement", row.detail)
	}
}

// A review finding: on a -dir that isn't a git checkout at all, this row
// must say so rather than the more general (and here misleading)
// "origin/HEAD does not resolve" — the same distinction setupOriginHeadRow
// already draws for the same underlying git failure.
func TestSetupBranchProtectionRowNamesANonCheckout(t *testing.T) {
	t.Parallel()
	cfg := config{dir: t.TempDir()} // no .git here at all
	row := setupBranchProtectionRow(context.Background(), cfg, true, true)
	if row.status != setupUnknown || !strings.Contains(row.detail, "not a git checkout") {
		t.Errorf("row = %+v, want it to name -dir as not a git checkout", row)
	}
}

func TestSetupBranchProtectionRowUnknownWithoutRepoOrGit(t *testing.T) {
	t.Parallel()
	_, checkout := upstream(t)
	cfg := setupCfg(t, &ghState{}, checkout)
	if row := setupBranchProtectionRow(context.Background(), cfg, false, true); row.status != setupUnknown {
		t.Errorf("no repo: row = %+v, want %q", row, setupUnknown)
	}
	if row := setupBranchProtectionRow(context.Background(), cfg, true, false); row.status != setupUnknown {
		t.Errorf("no git: row = %+v, want %q", row, setupUnknown)
	}
}

func TestSetupDeleteBranchOnMergeRow(t *testing.T) {
	t.Parallel()
	_, checkout := upstream(t)

	cfg := setupCfg(t, &ghState{DeleteBranchOnMerge: true}, checkout)
	if row := setupDeleteBranchOnMergeRow(context.Background(), cfg, true); row.status != setupOK {
		t.Errorf("enabled: row = %+v, want ok", row)
	}

	cfg = setupCfg(t, &ghState{}, checkout)
	if row := setupDeleteBranchOnMergeRow(context.Background(), cfg, true); row.status != setupMissing || row.required {
		t.Errorf("disabled: row = %+v, want missing and not required (advice only)", row)
	}

	cfg = setupCfg(t, &ghState{NoDeleteBranchOnMergeField: true}, checkout)
	if row := setupDeleteBranchOnMergeRow(context.Background(), cfg, true); row.status != setupUnknown {
		t.Errorf("old gh: row = %+v, want %q", row, setupUnknown)
	}

	cfg = setupCfg(t, &ghState{}, checkout)
	if row := setupDeleteBranchOnMergeRow(context.Background(), cfg, false); row.status != setupUnknown {
		t.Errorf("no repo: row = %+v, want %q", row, setupUnknown)
	}
}
