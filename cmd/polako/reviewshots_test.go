package main

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// A review that asks for screenshots and nothing else is answered by the PR
// author's comment linking an after shot of the current head — read off one
// `pr view` payload, like every other answer, so a restarted drain agrees.
func TestReviewShotsAnswerAReview(t *testing.T) {
	t.Parallel()
	const (
		head     = "abc1234def5678"
		old      = "2026-08-19T10:00:00Z"
		reviewed = "2026-08-20T10:00:00Z"
		later    = "2026-08-20T12:00:00Z"
		evidence = "0123456789abcdef0123456789abcdef01234567"
	)
	payload := func(author string, comments ...string) []byte {
		return []byte(fmt.Sprintf(
			`{"state":"OPEN","mergeable":"MERGEABLE","headRefOid":%q,"statusCheckRollup":[],`+
				`"reviewDecision":"","reviews":[{"author":{"login":"ann"},"authorAssociation":"OWNER",`+
				`"state":%q,"submittedAt":%q}],`+
				`"commits":[{"committedDate":%q}],"author":{"login":%q},"comments":[%s]}`,
			head, reviewChangesRequested, reviewed, old, author, strings.Join(comments, ",")))
	}
	comment := func(login, body, at string) string {
		return fmt.Sprintf(`{"author":{"login":%q},"body":%q,"createdAt":%q}`, login, body, at)
	}
	// shot is an image line the way the skill and reviewShotsHow both build it.
	shot := func(sha7, file string) string {
		return fmt.Sprintf("![%s](https://github.com/o/r/blob/%s/issue-46/%s/%s?raw=true)",
			file, evidence, sha7, file)
	}
	pair := "| " + shot(head[:7], "before-home.png") + " | " + shot(head[:7], "after-home.png") + " |"

	cases := []struct {
		name        string
		raw         []byte
		outstanding bool
	}{
		{"the author's after shot of the head, after the review",
			payload("polako-bot", comment("polako-bot", pair, later)), false},
		{"a before shot alone shows nothing the review asked about",
			payload("polako-bot", comment("polako-bot", shot(head[:7], "before-home.png"), later)), true},
		{"a shot of a head the branch has moved past",
			payload("polako-bot", comment("polako-bot", shot("fedcba9", "after-home.png"), later)), true},
		{"shots posted before the review",
			payload("polako-bot", comment("polako-bot", pair, old)), true},
		// On a public repo anyone can comment; a stranger's link is not the
		// answer, or anyone could stand a review down.
		{"someone else's comment",
			payload("polako-bot", comment("mallory", pair, later)), true},
		{"no PR author to hold comments to",
			payload("", comment("polako-bot", pair, later)), true},
		{"a comment that only talks about shots",
			payload("polako-bot", comment("polako-bot", "I can't take screenshots here.", later)), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pr, err := parsePRStatus(tc.raw)
			if err != nil {
				t.Fatalf("parsePRStatus: %v", err)
			}
			if got := pr.reviewOutstanding(); got != tc.outstanding {
				t.Errorf("reviewOutstanding = %v, want %v", got, tc.outstanding)
			}
			if got := pr.shotsAnswer(); got == tc.outstanding {
				t.Errorf("shotsAnswer = %v, want %v", got, !tc.outstanding)
			}
			wantNote := ""
			if !tc.outstanding {
				wantNote = ", changes requested and answered — waiting on a re-review"
			}
			if got := pr.reviewNote(); got != wantNote {
				t.Errorf("reviewNote = %q, want %q", got, wantNote)
			}
		})
	}
}

// The screenshot steps ride on -visual-evidence, and never on a design run:
// design forces the flag on only to keep its skill prompt bare, and a design
// run never touches the evidence ref.
func TestReviewPromptCarriesShotsOnlyWhenOn(t *testing.T) {
	t.Parallel()
	const plainFinish = "This run is not finished until the branch has a new commit pushed. "
	cases := []struct {
		name  string
		cfg   config
		shots bool
	}{
		{"flag on", config{branchPrefix: "issue-", repo: "o/r", visualEvidence: true}, true},
		{"flag off", config{branchPrefix: "issue-", repo: "o/r"}, false},
		{"design run", config{branchPrefix: "issue-", repo: "o/r", visualEvidence: true, verb: designVerb}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			prompt := reviewPrompt(tc.cfg, 46, 9)
			for _, marker := range []string{"Screenshots:", evidenceRef, evidenceDir, reviewShotsFinished,
				"which state the shots can't show"} {
				if got := strings.Contains(prompt, marker); got != tc.shots {
					t.Errorf("prompt contains %q = %v, want %v:\n%s", marker, got, tc.shots, prompt)
				}
			}
			if got := strings.Contains(prompt, plainFinish); got == tc.shots {
				t.Errorf("prompt keeps the push-only finish line = %v, want %v:\n%s", got, !tc.shots, prompt)
			}
			if !strings.Contains(prompt, "`gh pr comment 9 --body-file <file>`") {
				t.Errorf("prompt lost the one comment spelling this run is granted:\n%s", prompt)
			}
		})
	}
}

// The review prompt carries a second copy of the skill's capture and publish
// steps, so the spellings a shot's URL and the supervisor's own link check
// depend on are held to the skill's: the ref, the layout, the commit subject,
// the URL form, the capture commands. A drift in either copy fails here
// rather than as a review that never reads as answered.
func TestReviewShotsMatchTheSkill(t *testing.T) {
	t.Parallel()
	skill := strings.Join(strings.Fields(readRepoFile(t, "skills", skillDir, "SKILL.md")), " ")
	how := reviewShotsHow("issue-46", 46)

	pairs := []struct{ skill, how string }{
		{"`polako-evidence`", evidenceRef},
		{"`issue-$issue/<head sha7>/{before,after}-<slug>.png`", "issue-46/<sha7>/<file name>"},
		{"`evidence: issue-$issue @ <sha7>, <k> shots [skip ci]`", "evidence: issue-46 @ <sha7>, <k> shots [skip ci]"},
		{"`https://<host>/<owner>/<repo>/blob/<evidence-commit-sha>/<path>?raw=true`",
			"`https://<host>/<owner>/<repo>/blob/<evidence commit sha>/issue-46/<sha7>/<file name>?raw=true`"},
		{"config --get remote.origin.url", "config --get remote.origin.url"},
		{"`npx --yes playwright screenshot --viewport-size=1280,800 --wait-for-timeout=1500",
			"`npx --yes playwright screenshot --viewport-size=1280,800 --wait-for-timeout=1500"},
		{"`npx --yes playwright install chromium`", "`npx --yes playwright install chromium`"},
		{"<worktree>/" + evidenceDir + "/after-<slug>.png", "<worktree>/" + evidenceDir + "/after-<slug>.png"},
		{"Never `--force`", "never --force"},
	}
	for _, p := range pairs {
		if !strings.Contains(skill, p.skill) {
			t.Errorf("SKILL.md no longer says %q — reviewShotsHow copies it, so change both", p.skill)
		}
		if !strings.Contains(how, p.how) {
			t.Errorf("reviewShotsHow no longer says %q — it has to match the skill's %q", p.how, p.skill)
		}
	}

	// The URL both copies build, filled in, is what latestShots looks for.
	url := "https://github.com/o/r/blob/0123456789abcdef0123456789abcdef01234567/issue-46/abc1234/after-settings.png?raw=true"
	if !linksShotOf("![after /settings]("+url+")", "abc1234") {
		t.Errorf("shotsLink no longer matches the URL the skill and the review prompt build: %s", url)
	}
}

// End to end: a review asking only for screenshots gets one remediation run,
// which posts them and pushes nothing — and that is an answer, not "made no
// change". The drain waits on the re-review and the PR goes on to merge.
func TestDrainTakesScreenshotsAsAnAnswer(t *testing.T) {
	t.Parallel()
	buf := captureLog(t)
	cfg, path := drainConfig(t, "shootreview", &ghState{
		Issues: map[string]*fakeIssue{"1": {Open: true}},
		PRs: map[string]*fakePR{"issue-1": {
			Number: 9, State: "OPEN", Mergeable: "MERGEABLE",
			Head: "abc1234def5678", Checks: []string{"SUCCESS"},
			Reviews:     []fakeReview{{State: reviewChangesRequested, SubmittedAt: "2026-08-20T10:00:00Z"}},
			CommittedAt: "2026-08-19T10:00:00Z",
			// One read to dispatch, one for the run's own check, one poll that
			// sees the answer, then the merge.
			MergeOnRead: 4,
		}},
	})

	if err := drain(context.Background(), cfg); err != nil {
		t.Fatalf("drain: %v", err)
	}

	st := finalGhState(t, path)
	if st.Issues["1"].Open {
		t.Error("issue 1 should have been closed once the PR merged")
	}
	if got := st.Issues["1"].Labels; slices.Contains(got, needsHumanLabel) {
		t.Errorf("issue 1 labels = %v, want a review answered with shots not to park", got)
	}
	if got := len(st.PRs["issue-1"].Comments); got != 1 {
		t.Errorf("PR carries %d comments, want the one with the shots", got)
	}

	out := buf.String()
	if got := strings.Count(out, "dispatching remediation"); got != 1 {
		t.Errorf("dispatched %d remediation runs, want exactly 1\ngot:\n%s", got, out)
	}
	if strings.Contains(out, errNoPush.Error()) {
		t.Errorf("an answer with screenshots was taken for a run that did nothing\ngot:\n%s", out)
	}
	if want := "changes requested and answered — waiting on a re-review"; !strings.Contains(out, want) {
		t.Errorf("log is missing %q\ngot:\n%s", want, out)
	}
}

// The same run, but the PR is someone else's, so the shots comment — posted as
// the viewer — is not its author's answer. It reads as a run that moved
// nothing, and the issue parks rather than waiting on a review nobody answered.
func TestDrainParksScreenshotsTheAuthorDidNotPost(t *testing.T) {
	t.Parallel()
	buf := captureLog(t)
	cfg, path := drainConfig(t, "shootreview", &ghState{
		Issues: map[string]*fakeIssue{"1": {Open: true}},
		Labels: []string{needsHumanLabel},
		PRs: map[string]*fakePR{"issue-1": {
			Number: 9, State: "OPEN", Mergeable: "MERGEABLE",
			Head: "abc1234def5678", Checks: []string{"SUCCESS"},
			Author:      "someone-else",
			Reviews:     []fakeReview{{State: reviewChangesRequested, SubmittedAt: "2026-08-20T10:00:00Z"}},
			CommittedAt: "2026-08-19T10:00:00Z",
			// The park lands on the third read. A drain that took the shots
			// for an answer would wait instead; this merge ends that wait, so
			// the regression fails the label check below rather than hanging.
			MergeOnRead: 5,
		}},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := drain(ctx, cfg); err != nil {
		t.Fatalf("drain: %v", err)
	}

	st := finalGhState(t, path)
	if got := st.Issues["1"].Labels; !slices.Contains(got, needsHumanLabel) {
		t.Errorf("issue 1 labels = %v, want it parked", got)
	}
	out := buf.String()
	if want := "review remediation 1/1 failed (" + errNoPush.Error() + ")"; !strings.Contains(out, want) {
		t.Errorf("log is missing %q\ngot:\n%s", want, out)
	}
	if want := "changes requested on PR #9 are still outstanding after 1 remediation runs"; !strings.Contains(out, want) {
		t.Errorf("log is missing %q\ngot:\n%s", want, out)
	}
}
