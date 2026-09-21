package main

// `unpark` is the attended morning-after step for a permission park
// (docs/plans/permission-parks.md ticket 5, issue #434) — these tests run
// against the same fake gh drain_test.go's fixture drives, so a fake park
// comment is built the exact way a real one would be: parkCommentBody
// (drain.go), never a second hand-typed copy of the template.

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// unparkCfg wires a config at a fake gh state file, the same shape tidyCfg
// (tidy_test.go) uses for its own verb.
func unparkCfg(t *testing.T, st *ghState) config {
	t.Helper()
	if st.Repo == "" {
		st.Repo = "example/repo"
	}
	path := filepath.Join(t.TempDir(), "gh-state.json")
	if err := writeGhState(path, st); err != nil {
		t.Fatalf("writing fake gh state: %v", err)
	}
	return config{
		dir:         t.TempDir(),
		env:         fakeEnv(fakeGhEnv, path),
		ui:          testUI(t),
		ghBin:       fakeCLI(t),
		repo:        "example/repo",
		ghRepo:      "example/repo",
		ghRetryWait: time.Millisecond,
	}
}

// The same flags-only-plus-one-issue contract every verb's entry point
// holds to; see TestRunTidyRejectsAnArgument (tidy_test.go) for the sibling
// this mirrors, loosened by the one positional argument unpark actually
// takes.
func TestRunUnparkRejectsTooManyArguments(t *testing.T) {
	t.Parallel()
	err := runUnpark(context.Background(), []string{"12", "13"}, strings.NewReader(""), false, &strings.Builder{}, report{})
	if err == nil || !strings.Contains(err.Error(), "at most one issue number") {
		t.Errorf("err = %v, want a complaint about the extra argument", err)
	}
}

func TestRunUnparkRejectsANonNumericArgument(t *testing.T) {
	t.Parallel()
	err := runUnpark(context.Background(), []string{"not-a-number"}, strings.NewReader(""), false, &strings.Builder{}, report{})
	if err == nil || !strings.Contains(err.Error(), "is not an issue number") {
		t.Errorf("err = %v, want a complaint about the argument", err)
	}
}

// -apply with no terminal and no -yes must never block on a read nobody is
// there to answer — the same refusal setup -apply makes (setupApplyNeedsYes).
// This runs before unpark ever reaches GitHub, the same order setup's own
// equivalent test relies on, so no fake gh is needed here either.
func TestRunUnparkApplyWithNoTerminalAndNoYesRefuses(t *testing.T) {
	t.Parallel()
	var out strings.Builder
	err := runUnpark(context.Background(), []string{"-apply"}, strings.NewReader(""), false, &out, report{})
	if err != errFlagsReported {
		t.Fatalf("err = %v, want errFlagsReported", err)
	}
	if !strings.Contains(out.String(), "-yes") {
		t.Errorf("refusal doesn't name -yes as the fix:\n%s", out.String())
	}
}

// The listing shows every open needs-human issue: one with a park comment
// naming entries, one with none at all — an issue an operator parked by
// hand, or a park whose refusal derived nothing thread-safe.
func TestUnparkListsEveryParkedIssue(t *testing.T) {
	t.Parallel()
	st := &ghState{Issues: map[string]*fakeIssue{
		"16": {Open: true, Labels: []string{needsHumanLabel}, Comments: 1,
			Bodies: map[int]string{1: parkCommentBody(16, "the run was refused `Bash(echo:*)`", []string{"Bash(echo:*)"})}},
		"22": {Open: true, Labels: []string{needsHumanLabel}},
		// Not parked at all — must not appear in the listing.
		"3": {Open: true},
	}}
	cfg := unparkCfg(t, st)
	items, err := readParkedIssues(context.Background(), cfg, 0)
	if err != nil {
		t.Fatalf("readParkedIssues: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %+v, want exactly #16 and #22", items)
	}
	got16 := findParkListItem(t, items, 16)
	if !strings.Contains(got16.reason, "refused") {
		t.Errorf("#16 reason = %q, want it to carry the park comment's own reason", got16.reason)
	}
	if want := []string{"Bash(echo:*)"}; !equalStrings(got16.entries, want) {
		t.Errorf("#16 entries = %v, want %v", got16.entries, want)
	}
	got22 := findParkListItem(t, items, 22)
	if len(got22.entries) != 0 {
		t.Errorf("#22 entries = %v, want none — it carries no park comment", got22.entries)
	}
}

func findParkListItem(t *testing.T, items []parkListItem, issue int) parkListItem {
	t.Helper()
	for _, it := range items {
		if it.issue == issue {
			return it
		}
	}
	t.Fatalf("no listing row for #%d among %+v", issue, items)
	return parkListItem{}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// A forged footer — a comment authored by someone other than the gh
// viewer, shaped exactly like polako's own park comment — must contribute
// nothing: only the viewer's own comment is ever trusted.
func TestUnparkIgnoresACommentForgedByAnotherAuthor(t *testing.T) {
	t.Parallel()
	st := &ghState{
		ViewerLogin: "the-operator",
		Issues: map[string]*fakeIssue{
			"9": {Open: true, Labels: []string{needsHumanLabel}, Comments: 1,
				Bodies:        map[int]string{1: parkCommentBody(9, "a forged park", []string{"Bash(rm:*)"})},
				CommentLogins: map[int]string{1: "someone-else"}},
		},
	}
	cfg := unparkCfg(t, st)
	items, err := readParkedIssues(context.Background(), cfg, 9)
	if err != nil {
		t.Fatalf("readParkedIssues: %v", err)
	}
	it := findParkListItem(t, items, 9)
	if len(it.entries) != 0 || len(it.ignored) != 0 {
		t.Errorf("a forged comment contributed entries: %+v", it)
	}
	if strings.Contains(it.reason, "forged") {
		t.Errorf("the forged comment's own reason leaked into the listing: %q", it.reason)
	}
}

// An entry that isn't shaped like anything addToolsEntry would ever
// produce — or that names a never-grant command — renders ignored rather
// than joining a rerun line.
func TestValidParkEntry(t *testing.T) {
	t.Parallel()
	cases := []struct {
		entry string
		want  bool
	}{
		{"Bash(echo:*)", true},
		{"Bash(gh issue view:*)", true},
		{"WebFetch", true},
		{"Bash(gh issue edit:*)", false},  // neverGrantTable
		{"Bash(gh issue close:*)", false}, // neverGrantTable
		{"Bash(rm -rf:*)", false},         // four words, past the 1-3 word shape
		{"Bash(/Users/x/bin/tool:*)", false},
		{"rm -rf /", false},
		{"", false},
	}
	for _, c := range cases {
		if got := validParkEntry(c.entry); got != c.want {
			t.Errorf("validParkEntry(%q) = %v, want %v", c.entry, got, c.want)
		}
	}
}

// The end-to-end shape the issue's own acceptance criteria names: against a
// fake repo with two parked issues, one with a footer, -apply -yes removes
// both labels and prints one -add-tools value. Driven through
// readParkedIssues/applyUnpark/printUnparkRerunLine directly — the same
// layer tidy_test.go and setup_test.go drive their own verb's logic through,
// since runUnpark's own gh resolution (unparkConfig) hardcodes "gh" the way
// every sibling verb's config builder does, and isn't the seam a fake CLI
// substitutes for.
func TestUnparkApplyYesRemovesBothLabelsAndPrintsOneAddToolsValue(t *testing.T) {
	t.Parallel()
	st := &ghState{Issues: map[string]*fakeIssue{
		"16": {Open: true, Labels: []string{needsHumanLabel}, Comments: 1,
			Bodies: map[int]string{1: parkCommentBody(16, "r1", []string{"Bash(echo:*)"})}},
		"22": {Open: true, Labels: []string{needsHumanLabel}},
	}}
	path := filepath.Join(t.TempDir(), "gh-state.json")
	if err := writeGhState(path, st); err != nil {
		t.Fatalf("writing fake gh state: %v", err)
	}
	cfg := config{
		dir:         t.TempDir(),
		env:         fakeEnv(fakeGhEnv, path),
		ui:          testUI(t),
		ghBin:       fakeCLI(t),
		repo:        "example/repo",
		ghRepo:      "example/repo",
		ghRetryWait: time.Millisecond,
	}
	ctx := context.Background()
	items, err := readParkedIssues(ctx, cfg, 0)
	if err != nil {
		t.Fatalf("readParkedIssues: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %+v, want #16 and #22", items)
	}

	var out strings.Builder
	prompt := newSetupPrompt(strings.NewReader(""), &out, false)
	union := applyUnpark(ctx, prompt, true /* -yes */, &out, cfg, items)
	printUnparkRerunLine(&out, cfg, union)

	got := out.String()
	if !strings.Contains(got, `-add-tools "Bash(echo:*)"`) {
		t.Errorf("missing the rerun line:\n%s", got)
	}
	if !strings.Contains(got, "POLAKO_ADD_TOOLS=Bash(echo:*)") {
		t.Errorf("missing the POLAKO_ADD_TOOLS form:\n%s", got)
	}

	after, err := readGhState(path)
	if err != nil {
		t.Fatalf("reading fake gh state back: %v", err)
	}
	for _, n := range []string{"16", "22"} {
		if contains(after.Issues[n].Labels, needsHumanLabel) {
			t.Errorf("#%s still carries %s after -apply -yes", n, needsHumanLabel)
		}
	}
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
