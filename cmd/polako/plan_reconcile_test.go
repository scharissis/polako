package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestWithItemMarker(t *testing.T) {
	t.Parallel()
	footer := planFooterPrefix + "docs/VISION.md @ 1111111 — edit freely."
	mk := itemMarker{slug: "VISION", id: "M2", at: "2222222"}
	cases := []struct {
		name, body, from, want string
	}{
		{"above the footer", "## Summary\nx\n\n" + footer + "\n", "",
			"## Summary\nx\n\n" + mk.String() + "\n" + footer + "\n"},
		{"joins markers already there", "x\n\n<!-- polako:item=VISION/M1 -->\n" + footer, "",
			"x\n\n<!-- polako:item=VISION/M1 -->\n" + mk.String() + "\n" + footer + "\n"},
		{"replaces its own, so at moves", "x\n<!-- polako:item=VISION/M2 at=1111111 -->\n" + footer, "",
			"x\n" + mk.String() + "\n" + footer},
		{"rewrites a renumbered one", "x\n<!-- polako:item=VISION/M1 -->\n" + footer, "M1",
			"x\n" + mk.String() + "\n" + footer},
		{"another doc's stays", "x\n<!-- polako:item=other/M2 -->\n" + footer, "",
			"x\n<!-- polako:item=other/M2 -->\n" + mk.String() + "\n" + footer + "\n"},
		{"no footer: at the end", "Filed by hand.", "", "Filed by hand.\n\n" + mk.String() + "\n"},
	}
	for _, tc := range cases {
		if got := withItemMarker(tc.body, mk, tc.from); got != tc.want {
			t.Errorf("%s:\ngot:\n%s\nwant:\n%s", tc.name, got, tc.want)
		}
	}
	got := withDocUpdate("x\n\n"+mk.String()+"\n"+footer+"\n", "M2 changed.")
	if want := "x\n\n## Doc update\n\nM2 changed.\n\n" + mk.String() + "\n" + footer + "\n"; got != want {
		t.Errorf("a doc update goes above the markers and footer:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestParseGuessAnswer(t *testing.T) {
	t.Parallel()
	in := reconFixture(t, "issues.json")
	res := reconcile(in)
	guessed := guessedRows(res) // M2 #4, M6 #8, M7 #9, M8 #9, M12 #13
	byNumber := map[int]reconIssue{}
	for _, is := range in.issues {
		byNumber[is.Number] = is
	}
	cases := []struct {
		answer    string
		confirmed map[int][]string
		rejected  map[string][]int
		err       string
	}{
		{answer: "y", confirmed: map[int][]string{4: {"M2"}, 8: {"M6"}, 9: {"M7", "M8"}, 13: {"M12"}}},
		{answer: "", confirmed: map[int][]string{}},
		{answer: "N", confirmed: map[int][]string{}},
		// Naming the wrong ones confirms the rest.
		{answer: "m2, M12=#14", confirmed: map[int][]string{8: {"M6"}, 9: {"M7", "M8"}, 14: {"M12"}},
			rejected: map[string][]int{"M2": {4}, "M12": {13}}},
		// A missing item can be pointed at the issue that did it.
		{answer: "y M9=#17", confirmed: map[int][]string{4: {"M2"}, 8: {"M6"}, 9: {"M7", "M8"}, 13: {"M12"}, 17: {"M9"}}},
		{answer: "M3", err: "isn't an item with a guess"},
		{answer: "M2=#999", err: "#999 isn't an issue"},
		{answer: "M99=#4", err: "isn't ITEM=#ISSUE"},
	}
	for _, tc := range cases {
		ans, err := parseGuessAnswer(tc.answer, res, guessed, byNumber)
		if tc.err != "" {
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("%q: err = %v, want %q", tc.answer, err, tc.err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: %v", tc.answer, err)
			continue
		}
		for n := range ans.confirmed {
			slices.Sort(ans.confirmed[n])
		}
		if !mapsEqual(ans.confirmed, tc.confirmed) || !mapsEqual(ans.rejected, orEmpty(tc.rejected)) {
			t.Errorf("%q: confirmed %v rejected %v, want %v and %v", tc.answer, ans.confirmed, ans.rejected, tc.confirmed, tc.rejected)
		}
	}
}

func orEmpty(m map[string][]int) map[string][]int {
	if m == nil {
		return map[string][]int{}
	}
	return m
}

func mapsEqual[K comparable, V comparable](a, b map[K][]V) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if !slices.Equal(v, b[k]) {
			return false
		}
	}
	return true
}

func TestPlanChangesFollowTheMilestoneFlag(t *testing.T) {
	t.Parallel()
	in := reconFixture(t, "issues.json")
	in.confirmed = map[int][]string{4: {"M2"}, 8: {"M6"}, 9: {"M7", "M8"}, 13: {"M12"}}
	res := reconcile(in)

	c := planChanges(res, "")
	var ms []string
	for _, it := range c.milestones {
		ms = append(ms, it.id)
	}
	// M9 is a create; its milestone exists already, under the nightly
	// workflow's old name, and gets renamed. M1, M4 and M5 are already in
	// theirs.
	if want := []string{"M2", "M3", "M7", "M9", "M10", "M11", "M12"}; !slices.Equal(ms, want) {
		t.Errorf("milestones = %v, want %v", ms, want)
	}
	if len(c.marks) != 6 || len(c.moves) != 6 || len(c.edits) != 1 || len(c.creates) != 1 || len(c.held) != 0 {
		t.Errorf("marks %d, moves %d, edits %d, creates %d, held %d — want 6, 6, 1, 1, 0",
			len(c.marks), len(c.moves), len(c.edits), len(c.creates), len(c.held))
	}

	if off := planChanges(res, milestonesOff); len(off.milestones) != 0 || len(off.moves) != 0 || off.batch != "" {
		t.Errorf("-milestone off still touches milestones: %+v", off)
	}
	if batch := planChanges(res, "Batch 7"); batch.batch != "Batch 7" || len(batch.moves) != 0 || len(batch.milestones) != 0 {
		t.Errorf("an explicit -milestone is the new issues' batch and moves nothing: %+v", batch)
	}
}

// reconPlanRig is a checkout and a pretend GitHub shaped like the fixtures,
// for plan's reconciling half end to end.
type reconPlanRig struct {
	cfg              config
	statePath        string
	logPath          string
	issuesPath       string // the canned `issue list --state all` answer
	oldSHA, headSHA  string
	original         []map[string]any
	checkout, design string
}

func newReconPlanRig(t *testing.T) *reconPlanRig {
	t.Helper()
	cfg, statePath, checkout := planTestConfig(t, &ghState{})
	r := &reconPlanRig{cfg: cfg, statePath: statePath, checkout: checkout, design: "docs/VISION.md"}
	r.cfg.repo, r.cfg.ghRepo = "example/repo", "example/repo"
	r.cfg.maxIssues = 10
	r.logPath = filepath.Join(t.TempDir(), "gh.log")
	setFakeEnv(&r.cfg, fakeClaudeEnv, "planitems", fakeGhLogEnv, r.logPath)

	commit := func(content string) string {
		writeFileAt(t, checkout, r.design, content)
		gitAt(t, checkout, "add", "-A")
		gitAt(t, checkout, "commit", "-q", "-m", "vision")
		return gitAt(t, checkout, "rev-parse", "--short", "HEAD")
	}
	r.oldSHA = commit(string(readReconFixture(t, "vision-old.md")))
	r.headSHA = commit(string(readReconFixture(t, "vision.md")))

	raw := strings.NewReplacer("1111111", r.oldSHA, "2222222", r.headSHA).Replace(string(readReconFixture(t, "issues.json")))
	if err := json.Unmarshal([]byte(raw), &r.original); err != nil {
		t.Fatal(err)
	}
	r.issuesPath = filepath.Join(t.TempDir(), "issues.json")
	prs, _ := filepath.Abs(filepath.Join("testdata", "reconcile", "prs.json"))

	st := &ghState{Repo: "example/repo", ViewerLogin: "op", Labels: []string{needsHumanLabel},
		Issues: map[string]*fakeIssue{},
		Canned: map[string]string{"issue list --state all": r.issuesPath, "pr list --state all": prs}}
	for _, is := range r.original {
		n := strconv.Itoa(int(is["number"].(float64)))
		fi := &fakeIssue{Open: is["state"] == "OPEN", Title: is["title"].(string), Body: is["body"].(string)}
		for _, l := range is["labels"].([]any) {
			fi.Labels = append(fi.Labels, l.(map[string]any)["name"].(string))
		}
		if m, ok := is["milestone"].(map[string]any); ok {
			fi.Milestone = m["title"].(string)
			if !slices.Contains(st.Milestones, fi.Milestone) {
				st.Milestones = append(st.Milestones, fi.Milestone)
			}
		}
		st.Issues[n] = fi
	}
	if err := writeGhState(statePath, st); err != nil {
		t.Fatal(err)
	}
	r.syncListing(t)
	return r
}

func writeFileAt(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// syncListing rewrites the canned issue listing from the pretend repo, so
// the next read sees every body, milestone and new issue the last run wrote.
func (r *reconPlanRig) syncListing(t *testing.T) {
	t.Helper()
	st := r.state(t)
	var rows []map[string]any
	seen := map[string]bool{}
	for _, orig := range r.original {
		n := strconv.Itoa(int(orig["number"].(float64)))
		seen[n] = true
		row := map[string]any{}
		for k, v := range orig {
			row[k] = v
		}
		row["body"] = st.Issues[n].Body
		row["milestone"] = milestoneJSON(st.Issues[n].Milestone)
		rows = append(rows, row)
	}
	for n, is := range st.Issues {
		if seen[n] {
			continue
		}
		num, _ := strconv.Atoi(n)
		var labels []map[string]string
		for _, l := range is.Labels {
			labels = append(labels, map[string]string{"name": l})
		}
		rows = append(rows, map[string]any{"number": num, "title": is.Title, "body": is.Body, "state": "OPEN",
			"stateReason": "", "labels": labels, "milestone": milestoneJSON(is.Milestone), "author": map[string]string{"login": "op"},
			"closedByPullRequestsReferences": []any{}, "subIssuesSummary": map[string]int{"total": 0}})
	}
	b, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.issuesPath, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func milestoneJSON(title string) any {
	if title == "" {
		return nil
	}
	return map[string]string{"title": title}
}

func (r *reconPlanRig) state(t *testing.T) *ghState {
	t.Helper()
	st, err := readGhState(r.statePath)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// writes is every gh call in the log that changes something.
func (r *reconPlanRig) writes(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(r.logPath)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "issue edit") || strings.HasPrefix(l, "issue create") || strings.HasPrefix(l, "issue comment") ||
			strings.HasPrefix(l, "label create") || strings.Contains(l, "-X PATCH") ||
			(strings.Contains(l, "milestones") && strings.Contains(l, " -f ")) {
			out = append(out, l)
		}
	}
	return out
}

func (r *reconPlanRig) run(t *testing.T, opt planOptions, stdin string, tty bool) (string, error) {
	t.Helper()
	opt.design = r.design
	if opt.maxIssues == 0 {
		opt.maxIssues = 10
	}
	var out strings.Builder
	err := planReconcile(context.Background(), r.cfg, opt, strings.NewReader(stdin), tty, &out)
	return out.String(), err
}

// The whole promise: plan confirms its guesses with the operator, makes the
// changes, files only what was missing — and the next run, over what that
// left on GitHub, changes nothing at all.
func TestPlanReconcileAppliesThenTheNextRunChangesNothing(t *testing.T) {
	t.Parallel()
	captureLog(t)
	r := newReconPlanRig(t)

	out, err := r.run(t, planOptions{}, "y\ny\n", true)
	if err != nil {
		t.Fatalf("first run: %v\n%s", err, out)
	}
	for _, want := range []string{
		"#4     feat: add the http fetcher", // the guess, shown by its title to be confirmed
		"Confirm them?",
		"Make these changes? [y/N]",
		"create      1 issue, one plan-backlog run capped at 10: M9",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("first run's output missing %q:\n%s", want, out)
		}
	}

	st := r.state(t)
	marked := func(n int, ids ...string) {
		t.Helper()
		body := st.Issues[strconv.Itoa(n)].Body
		for _, id := range ids {
			if !strings.Contains(body, "<!-- polako:item=VISION/"+id+" at="+r.headSHA+" -->") {
				t.Errorf("#%d isn't marked %s at HEAD:\n%s", n, id, body)
			}
		}
	}
	marked(4, "M2")
	marked(8, "M6")
	marked(9, "M7", "M8")
	marked(10, "M10")
	marked(13, "M12")
	marked(5, "M3")
	if body := st.Issues["10"].Body; strings.Contains(body, "VISION/M9") {
		t.Errorf("#10's renumbered marker wasn't rewritten:\n%s", body)
	}
	if body := st.Issues["5"].Body; !strings.Contains(body, "## Doc update") || !strings.Contains(body, "> Widget importer with fixtures.") {
		t.Errorf("#5 is stale and queued, so its body gets the doc's new text:\n%s", body)
	}
	for n, want := range map[string]string{
		"4": "[VISION] M2: HTTP fetcher with retries", "5": "[VISION] M3: Widget importer with fixtures",
		"9": "[VISION] M7: Store and lifecycle, with health-aware misses", "10": "[VISION] M10: Nightly workflow that commits data",
		"13": "[VISION] M12: Known-postings import", "17": "[VISION] M11: Pay parsing", "8": "VISION", // dropped: never moved
	} {
		if got := st.Issues[n].Milestone; got != want {
			t.Errorf("#%s's milestone = %q, want %q", n, got, want)
		}
	}
	if !slices.Contains(st.Milestones, "[VISION] M9: explain command") ||
		slices.Contains(st.Milestones, "[VISION] M9: Nightly workflow that commits data") {
		t.Errorf("M9's milestone should be renamed to the item's new name, not left behind: %q", st.Milestones)
	}
	var filed []*fakeIssue
	for _, is := range st.Issues {
		if is.Mine {
			filed = append(filed, is)
		}
	}
	if len(filed) != 1 || !strings.Contains(filed[0].Body, "<!-- polako:item=VISION/M9 -->") ||
		filed[0].Milestone != "[VISION] M9: explain command" || !slices.Equal(filed[0].Labels, []string{proposedLabel}) {
		t.Errorf("filed %d issues, want one for M9, marked, proposed, in its milestone: %+v", len(filed), filed)
	}
	if _, err := os.Stat(filepath.Join(r.checkout, itemsManifestPath)); !os.IsNotExist(err) {
		t.Errorf("the items file outlived the run (err %v)", err)
	}

	// The next run reads what the first left behind.
	r.syncListing(t)
	if err := os.Remove(r.logPath); err != nil {
		t.Fatal(err)
	}
	out, err = r.run(t, planOptions{}, "", false)
	if err != nil {
		t.Fatalf("second run: %v\n%s", err, out)
	}
	if !strings.Contains(out, "nothing to change") {
		t.Errorf("second run's output:\n%s\nwant it to say there's nothing to change", out)
	}
	if w := r.writes(t); len(w) > 0 {
		t.Errorf("the second run wrote to GitHub:\n  %s", strings.Join(w, "\n  "))
	}
}

func TestPlanReconcileAsksFirst(t *testing.T) {
	t.Parallel()
	captureLog(t)
	r := newReconPlanRig(t)

	if _, err := r.run(t, planOptions{}, "", false); err == nil || !strings.Contains(err.Error(), "pass -yes") {
		t.Errorf("no terminal, no -yes: err = %v, want a refusal naming -yes", err)
	}
	out, err := r.run(t, planOptions{}, "y\nn\n", true)
	if err != nil || !strings.Contains(out, "nothing changed") {
		t.Errorf("a no at the prompt: err %v, output:\n%s", err, out)
	}
	out, err = r.run(t, planOptions{intakeOptions: intakeOptions{dryRun: true}}, "", false)
	if err != nil || !strings.Contains(out, "if you confirmed them all, it would:") || !strings.Contains(out, itemsManifestPath) {
		t.Errorf("-dry-run: err %v, output:\n%s", err, out)
	}
	if w := r.writes(t); len(w) > 0 {
		t.Errorf("nothing approved, yet plan wrote:\n  %s", strings.Join(w, "\n  "))
	}
}

// -yes makes the settled changes and files what's missing, but a guess needs
// a person: its item is neither marked nor filed, so nothing is duplicated.
func TestPlanReconcileYesLeavesGuessesAlone(t *testing.T) {
	t.Parallel()
	captureLog(t)
	r := newReconPlanRig(t)
	out, err := r.run(t, planOptions{yes: true}, "", false)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if strings.Contains(out, "Confirm them?") || !strings.Contains(out, "leave       5 items matched only by a guess") {
		t.Errorf("-yes output:\n%s", out)
	}
	st := r.state(t)
	for _, n := range []string{"4", "8", "9", "13"} {
		if strings.Contains(st.Issues[n].Body, "polako:item=VISION/") {
			t.Errorf("#%s was only guessed, yet -yes marked it:\n%s", n, st.Issues[n].Body)
		}
	}
	if !strings.Contains(st.Issues["10"].Body, "VISION/M10") || !strings.Contains(st.Issues["5"].Body, "## Doc update") {
		t.Error("-yes should still make the settled changes: #10's renumbered marker and #5's doc update")
	}
}
