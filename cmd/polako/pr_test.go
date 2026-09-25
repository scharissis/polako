package main

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// The one read gh has no subcommand for, granted per run and pinned to the PR
// that run was dispatched for. `Bash(gh api:*)` in defaultTools would hand a
// prompt built out of attacker-supplied review text the whole GitHub API.
func TestPRReviewToolsStayPinnedToOnePR(t *testing.T) {
	t.Parallel()
	got := prReviewTools("acme/widgets", 42)
	if want := "Bash(gh api repos/acme/widgets/pulls/42/comments:*)"; got != want {
		t.Errorf("prReviewTools = %q, want %q", got, want)
	}
	if strings.Contains(defaultTools, "gh api") {
		t.Error("defaultTools grants gh api; it belongs in prReviewTools, where it is " +
			"bounded to the comments of the PR the run is already working on")
	}
	// The pinned entry has to survive alongside an operator's own -add-tools,
	// since that is how it reaches the run.
	merged := resolveTools(defaultTools, resolveTools("Bash(bazel:*)", got))
	for _, want := range []string{"Bash(bazel:*)", got} {
		if !strings.Contains(merged, want) {
			t.Errorf("resolved allowlist is missing %q\ngot: %s", want, merged)
		}
	}
}

// Every remediation prompt asks for a PR comment, so the grant has to exist and
// stay bounded: the PR number and --body-file are both inside the pattern. A
// bare `gh pr comment 42` prefix would also match PR 420, and `gh pr comment`
// in defaultTools would let a skill run comment on any PR in the repository.
func TestPRCommentToolsStayPinnedToOnePR(t *testing.T) {
	t.Parallel()
	got := prCommentTools(42)
	if want := "Bash(gh pr comment 42 --body-file:*)"; got != want {
		t.Errorf("prCommentTools = %q, want %q", got, want)
	}
	if strings.Contains(defaultTools, "gh pr comment") {
		t.Error("defaultTools grants gh pr comment; it belongs in prCommentTools, where " +
			"it is bounded to the PR a remediation run was dispatched to")
	}
	// The prompt has to name the exact spelling the grant allows, or the run
	// reaches for an inline --body and is refused.
	if how := prCommentHow(42); !strings.Contains(how, "gh pr comment 42 --body-file") {
		t.Errorf("prCommentHow does not spell the granted command: %q", how)
	}
}

// issue #386: a remediation run pasted raw `git push` stderr — naming the
// operator's SSH key — into a PR comment, twice on the same PR. All three
// remediation prompts (remediateConflicts, remediateChecks, remediateReview)
// share this one sentence, so one assertion covers all three.
func TestPRCommentHowSaysDescribeDontPaste(t *testing.T) {
	t.Parallel()
	how := prCommentHow(42)
	for _, marker := range []string{"own words", "never paste raw git, ssh, gh or env output"} {
		if !strings.Contains(how, marker) {
			t.Errorf("prCommentHow no longer says %q — without it a remediation run has"+
				" nothing telling it not to paste raw command output into a PR comment"+
				" (issue #386): %q", marker, how)
		}
	}
}

// Whether a review is still owed an answer is decided from one `pr view`
// payload alone, so that a drain restarted mid-flight reaches the same verdict
// as the one that dispatched the run.
func TestReviewOutstandingReadsOnePRView(t *testing.T) {
	t.Parallel()
	const (
		old    = "2026-08-19T10:00:00Z"
		newer  = "2026-08-20T10:00:00Z"
		newest = "2026-08-21T10:00:00Z"
	)
	// payload renders the review half of `pr view` the way the real thing does:
	// every review on the PR, oldest first.
	payload := func(decision, reviews, commits string) []byte {
		return []byte(fmt.Sprintf(
			`{"state":"OPEN","mergeable":"MERGEABLE","headRefOid":"abc","statusCheckRollup":[],`+
				`"reviewDecision":%q,"reviews":[%s],"commits":[%s]}`, decision, reviews, commits))
	}
	reviewAs := func(association, author, state, at string) string {
		return fmt.Sprintf(`{"author":{"login":%q},"authorAssociation":%q,"state":%q,"submittedAt":%q}`,
			author, association, state, at)
	}
	// A maintainer's review, unless a case says otherwise.
	review := func(author, state, at string) string { return reviewAs("MEMBER", author, state, at) }
	commit := func(at string) string { return fmt.Sprintf(`{"committedDate":%q}`, at) }
	outsider := ", " + outsiderNote

	cases := []struct {
		name string
		raw  []byte
		want bool
		note string
	}{{
		// The case the whole feature exists for, and the one this repository
		// itself produces: no branch protection, so reviewDecision is empty and
		// the review has to carry the verdict.
		name: "changes requested after the last commit",
		raw:  payload("", review("ann", reviewChangesRequested, newer), commit(old)),
		want: true,
	}, {
		name: "the branch moved after the review",
		raw:  payload("", review("ann", reviewChangesRequested, old), commit(newer)),
		want: false,
		note: ", changes requested and answered — waiting on a re-review",
	}, {
		// Only each reviewer's newest verdict stands, so a reviewer who asked
		// for changes and then approved is no longer in the way.
		name: "the reviewer came back and approved",
		raw: payload("APPROVED", review("ann", reviewChangesRequested, newer)+","+
			review("ann", reviewApproved, newest), commit(old)),
		want: false,
	}, {
		// The bug the `reviews` field exists to avoid: an ordinary comment is
		// not a verdict, so it cannot clear the request for changes under it.
		// gh's `latestReviews` would report this reviewer as COMMENTED and the
		// supervisor would go back to waiting for a merge nobody will perform.
		name: "a comment left after the request for changes",
		raw: payload("", review("ann", reviewChangesRequested, newer)+","+
			review("ann", "COMMENTED", newest), commit(old)),
		want: true,
	}, {
		// A dismissal is a verdict, and it is the one that stands.
		name: "the request for changes was dismissed",
		raw: payload("", review("ann", reviewChangesRequested, newer)+","+
			review("ann", reviewDismissed, newest), commit(old)),
		want: false,
	}, {
		name: "one approval does not cancel another reviewer's changes",
		raw: payload("", review("ann", reviewApproved, newer)+","+
			review("bob", reviewChangesRequested, newer), commit(old)),
		want: true,
	}, {
		// A repository that does require reviews says so here even when its
		// individual verdicts have been superseded. There is no date to hold the
		// branch against, so this waits on a person rather than guessing.
		name: "reviewDecision alone, with no review to date it",
		raw:  payload(reviewChangesRequested, "", commit(old)),
		want: false,
		note: ", changes requested",
	}, {
		// Nothing to chase and nothing to say: the ordinary open PR.
		name: "no reviews at all",
		raw:  payload("", "", commit(old)),
		want: false,
	}, {
		// An unreadable date must not be read as "reviewed at the epoch", which
		// would make every branch look newer than every review.
		name: "an unparseable review date",
		raw:  payload("", review("ann", reviewChangesRequested, "not a date"), commit(old)),
		want: false,
		note: ", changes requested",
	}, {
		// The mirror image: an unreadable commit date must not let a stale review
		// look outstanding forever, but it cannot show the branch moved either.
		name: "an unparseable commit date",
		raw:  payload("", review("ann", reviewChangesRequested, newer), commit("not a date")),
		want: true,
	}, {
		// The gap this guards: on a public repo anyone can request changes,
		// and a run is paid for and pushes what the review asks.
		name: "an outsider's request for changes",
		raw:  payload("", reviewAs("NONE", "mallory", reviewChangesRequested, newer), commit(old)),
		want: false,
		note: outsider,
	}, {
		// Having committed to the repo once is not being let in.
		name: "a past contributor's request for changes",
		raw:  payload("", reviewAs("CONTRIBUTOR", "carl", reviewChangesRequested, newer), commit(old)),
		want: false,
		note: outsider,
	}, {
		// A gh that never sent the field reads as an outsider: reported, not acted on.
		name: "no association at all",
		raw: payload("", fmt.Sprintf(`{"author":{"login":"ann"},"state":%q,"submittedAt":%q}`,
			reviewChangesRequested, newer), commit(old)),
		want: false,
		note: outsider,
	}, {
		// The outsider's later date must not make the collaborator's answered
		// review look unanswered: only a trusted review dates the request.
		name: "an outsider's newer request beside an answered one",
		raw: payload("", reviewAs("COLLABORATOR", "ann", reviewChangesRequested, old)+","+
			reviewAs("NONE", "mallory", reviewChangesRequested, newest), commit(newer)),
		want: false,
		note: ", changes requested and answered — waiting on a re-review",
	}, {
		// A trusted request still dispatches, whoever else asked beside it.
		name: "a collaborator's request beside an outsider's",
		raw: payload("", reviewAs("NONE", "mallory", reviewChangesRequested, newer)+","+
			reviewAs("COLLABORATOR", "ann", reviewChangesRequested, newer), commit(old)),
		want: true,
	}, {
		// Reduced before it's filtered: a reviewer's own latest verdict stands,
		// so an approval withdraws their earlier request even when GitHub no
		// longer counts them as a collaborator.
		name: "a reviewer who approved after losing access",
		raw: payload("", reviewAs("COLLABORATOR", "ann", reviewChangesRequested, newer)+","+
			reviewAs("CONTRIBUTOR", "ann", reviewApproved, newest), commit(old)),
		want: false,
	}, {
		// The other way round: the reviewer's latest verdict is a request made
		// as an outsider, so it is reported, not acted on.
		name: "a reviewer who requested changes after losing access",
		raw: payload("", reviewAs("COLLABORATOR", "ann", reviewApproved, newer)+","+
			reviewAs("CONTRIBUTOR", "ann", reviewChangesRequested, newest), commit(old)),
		want: false,
		note: outsider,
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pr, err := parsePRStatus(tc.raw)
			if err != nil {
				t.Fatalf("parsePRStatus: %v", err)
			}
			if got := pr.reviewOutstanding(); got != tc.want {
				t.Errorf("reviewOutstanding = %v, want %v", got, tc.want)
			}
			if got := pr.reviewNote(); got != tc.note {
				t.Errorf("reviewNote = %q, want %q", got, tc.note)
			}
		})
	}
}

// Every value of GitHub's CommentAuthorAssociation, sorted into the three let
// in by name and everyone else. A value GitHub adds later is an outsider until
// someone decides otherwise here.
func TestTrustedReviewerOverGitHubsWholeEnum(t *testing.T) {
	t.Parallel()
	for association, want := range map[string]bool{
		"OWNER":                  true,
		"MEMBER":                 true,
		"COLLABORATOR":           true,
		"CONTRIBUTOR":            false,
		"FIRST_TIME_CONTRIBUTOR": false,
		"FIRST_TIMER":            false,
		"MANNEQUIN":              false,
		"NONE":                   false,
		"":                       false,
		"SOMETHING_NEW":          false,
	} {
		if got := trustedReviewer(association); got != want {
			t.Errorf("trustedReviewer(%q) = %v, want %v", association, got, want)
		}
	}
}

// The binary dispatches only for a trusted reviewer, but the run reads every
// review and line comment on the PR. Its prompt has to name the same three
// and the fields that carry them, or an outsider's ask rides along on a run a
// maintainer started.
func TestReviewPromptHeedsOnlyTrustedReviewers(t *testing.T) {
	t.Parallel()
	for _, visual := range []bool{false, true} {
		prompt := reviewPrompt(config{branchPrefix: "issue-", repo: "o/r", visualEvidence: visual}, 46, 9)
		markers := append(slices.Clone(trustedAssociations), "`authorAssociation`", "`author_association`")
		for _, marker := range markers {
			if !strings.Contains(prompt, marker) {
				t.Errorf("visual-evidence=%v: review prompt doesn't name %s:\n%s", visual, marker, prompt)
			}
		}
	}
}

func TestPickPRPrefersOpenThenMerged(t *testing.T) {
	t.Parallel()
	closed := pullRequest{Number: 1, State: "CLOSED"}
	merged := pullRequest{Number: 2, State: "MERGED"}
	open := pullRequest{Number: 3, State: "OPEN"}

	cases := []struct {
		name string
		in   []pullRequest
		want int
	}{
		{"open wins", []pullRequest{closed, merged, open}, 3},
		{"merged beats closed", []pullRequest{closed, merged}, 2},
		{"falls back to the first", []pullRequest{closed}, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := pickPR(c.in)
			if got == nil || got.Number != c.want {
				t.Errorf("pickPR(%v) = %v, want PR #%d", c.in, got, c.want)
			}
		})
	}
	if pickPR(nil) != nil {
		t.Error("no PRs should yield nil, so the caller runs the skill")
	}
}

// The rollup is the one GitHub answer the supervisor reduces itself, and every
// verdict costs something to get wrong: a false "failing" spends a Claude run
// on healthy code, a false "passing" is the silent forever-wait issue #5 is
// about. Both node shapes GitHub can put in the array are exercised, because
// the CheckRun/StatusContext split is decoded by which fields came back empty.
func TestClassifyChecks(t *testing.T) {
	t.Parallel()
	run := func(status, conclusion, name string) checkNode {
		return checkNode{Name: name, Status: status, Conclusion: conclusion}
	}
	ctxNode := func(state, context string) checkNode {
		return checkNode{Context: context, State: state}
	}
	cases := []struct {
		name    string
		nodes   []checkNode
		want    string
		failing []string
	}{
		{"nothing reported yet", nil, checksNone, nil},
		{"all green", []checkNode{run("COMPLETED", "SUCCESS", "test")}, checksPassing, nil},
		{"one red", []checkNode{
			run("COMPLETED", "SUCCESS", "lint"),
			run("COMPLETED", "FAILURE", "test"),
		}, checksFailing, []string{"test"}},
		{"a legacy status context", []checkNode{ctxNode("FAILURE", "ci/travis")}, checksFailing, []string{"ci/travis"}},
		{"a green status context", []checkNode{ctxNode("SUCCESS", "ci/travis")}, checksPassing, nil},
		{"a queued run", []checkNode{run("QUEUED", "", "test")}, checksPending, nil},
		// Pending outranks failing: the suite can only add to the list, and
		// diagnosing half of one wastes the run.
		{"red while another is still going", []checkNode{
			run("COMPLETED", "FAILURE", "test"),
			run("IN_PROGRESS", "", "build"),
		}, checksPending, nil},
		{"a pending status context", []checkNode{ctxNode("PENDING", "ci/travis")}, checksPending, nil},
		// Nothing a change to the branch can fix, so none of these is a failure
		// worth spending an attempt on — but a cancelled or unapproved check
		// still blocks the merge, so the verdict may not read "passing".
		{"cancelled, skipped and awaiting a human", []checkNode{
			run("COMPLETED", "CANCELLED", "test"),
			run("COMPLETED", "SKIPPED", "deploy"),
			run("COMPLETED", "NEUTRAL", "advisory"),
			run("COMPLETED", "ACTION_REQUIRED", "approve"),
		}, checksHuman, nil},
		{"skipped and neutral alone are green", []checkNode{
			run("COMPLETED", "SKIPPED", "deploy"),
			run("COMPLETED", "NEUTRAL", "advisory"),
		}, checksPassing, nil},
		// A deployment gate never finishes on its own, so it must not be read as
		// a suite still running: that would hide the real failure beside it for
		// as long as nobody approves.
		{"red behind a deployment gate", []checkNode{
			run("COMPLETED", "FAILURE", "test"),
			run("WAITING", "", "deploy"),
		}, checksFailing, []string{"test"}},
		{"only a deployment gate", []checkNode{run("WAITING", "", "deploy")}, checksHuman, nil},
		{"the other ways a build breaks", []checkNode{
			run("COMPLETED", "TIMED_OUT", "slow"),
			run("COMPLETED", "STARTUP_FAILURE", "broken-yaml"),
		}, checksFailing, []string{"slow", "broken-yaml"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, failing := classifyChecks(c.nodes)
			if got != c.want {
				t.Errorf("verdict = %q, want %q", got, c.want)
			}
			if !slices.Equal(failing, c.failing) {
				t.Errorf("failing = %v, want %v", failing, c.failing)
			}
		})
	}
}

// remediable is what the spend caps ask instead of repeating supervisePR's
// switch, so it has to answer yes to every condition that switch dispatches at
// and no to a PR that is merely waiting. A fourth kind of remediation added
// without a line here is a hole in the budget.
func TestRemediableCoversEveryDispatch(t *testing.T) {
	t.Parallel()
	reviewed := prView{changesRequested: true, reviewedAt: time.Now()}
	for _, tc := range []struct {
		name string
		pr   prView
		want bool
	}{
		{"conflicting", prView{mergeable: "CONFLICTING"}, true},
		{"red checks", prView{checks: checksFailing, failing: []string{"build"}}, true},
		{"review outstanding", reviewed, true},
		{"green and mergeable", prView{mergeable: "MERGEABLE", checks: checksPassing}, false},
		{"checks still running", prView{mergeable: "MERGEABLE", checks: checksPending}, false},
		{"a check stopped on a person", prView{mergeable: "MERGEABLE", checks: checksHuman}, false},
		{"review already answered", prView{changesRequested: true,
			reviewedAt: reviewed.reviewedAt, branchAt: reviewed.reviewedAt.Add(time.Minute)}, false},
	} {
		if got := tc.pr.remediable(); got != tc.want {
			t.Errorf("%s: remediable = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// The caps gate runs, never waiting: a PR that needs no fixing is free to
// merge however much its issue has already cost, and parking it would hand
// back an issue whose work is finished and sitting on GitHub.
func TestSupervisePRStillWaitsOutAnOverspentPRNobodyHasToFix(t *testing.T) {
	t.Parallel()
	captureLog(t)
	cfg, _ := drainConfig(t, "stream", &ghState{
		Issues: map[string]*fakeIssue{"1": {Open: true}},
		PRs: map[string]*fakePR{"issue-1": {
			Number: 9, State: "OPEN", Mergeable: "MERGEABLE",
			Head: "abc123", Checks: []string{"SUCCESS"}, MergeOnRead: 2,
		}},
	})
	cfg.maxCost = 1
	tally := issueTally{runs: 1, costUSD: 9}

	state, err := supervisePR(context.Background(), cfg, 1, 9, &issueState{}, &tally, runChoice{})
	if err != nil {
		t.Fatalf("an overspent issue whose PR is green must still be waited out: %v", err)
	}
	if state != "MERGED" {
		t.Errorf("state = %q, want MERGED", state)
	}
	if tally.runs != 1 {
		t.Errorf("tally.runs = %d, want no run dispatched while waiting", tally.runs)
	}
}

// The other half: a PR that does need fixing is a run this issue can no longer
// afford, so it parks instead of dispatching one.
func TestSupervisePRParksRatherThanRemediateOnAnOverspentIssue(t *testing.T) {
	t.Parallel()
	captureLog(t)
	cfg, _ := drainConfig(t, "fixci", &ghState{
		Issues: map[string]*fakeIssue{"1": {Open: true}},
		PRs: map[string]*fakePR{"issue-1": {
			Number: 9, State: "OPEN", Mergeable: "MERGEABLE",
			Head: "abc123", Checks: []string{"FAILURE"},
		}},
	})
	cfg.maxIssueTime = time.Minute
	tally := issueTally{runs: 1, wallMS: (2 * time.Minute).Milliseconds()}

	_, err := supervisePR(context.Background(), cfg, 1, 9, &issueState{}, &tally, runChoice{})
	reason, parked := parkReason(err)
	if !parked {
		t.Fatalf("a red PR on an issue past its cap should park, got %v", err)
	}
	if !strings.Contains(reason, "-max-issue-time") {
		t.Errorf("park reason = %q, want it to name the cap that stopped the remediation", reason)
	}
	if tally.runs != 1 {
		t.Errorf("tally.runs = %d, want the remediation never dispatched", tally.runs)
	}
}

func TestParsePRFactsCountsReviewsWithoutQuotingThem(t *testing.T) {
	t.Parallel()
	// The shape `gh pr view --json additions,deletions,changedFiles,createdAt,mergedAt,reviews`
	// returns, reviews and all.
	raw := []byte(`{"additions":412,"deletions":38,"changedFiles":7,` +
		`"createdAt":"2026-08-24T10:34:02Z","mergedAt":"2026-08-24T14:02:00Z",` +
		`"reviews":[{"author":{"login":"someone"},"state":"APPROVED","body":"ship it"},` +
		`{"author":{"login":"else"},"state":"COMMENTED","body":"one nit, otherwise fine"}]}`)

	got, err := parsePRFacts(raw)
	if err != nil {
		t.Fatalf("parsePRFacts: %v", err)
	}
	want := prFacts{Additions: 412, Deletions: 38, ChangedFiles: 7, Reviews: 2,
		Opened: "2026-08-24T10:34:02Z", Merged: "2026-08-24T14:02:00Z"}
	if got != want {
		t.Errorf("facts = %+v, want %+v", got, want)
	}
	// prFacts has nowhere to put a review body, and that is the point: what a
	// reviewer wrote is text, and text never reaches a record.
	if strings.Contains(fmt.Sprintf("%+v", got), "nit") {
		t.Errorf("facts carry review text: %+v", got)
	}
}

func TestParsePRFactsToleratesAPRThatNeverMerged(t *testing.T) {
	t.Parallel()
	// mergedAt is null on a closed-unmerged PR, and a PR nobody reviewed comes
	// back with an empty list.
	got, err := parsePRFacts([]byte(`{"additions":3,"deletions":1,"changedFiles":1,` +
		`"createdAt":"2026-08-24T10:34:02Z","mergedAt":null,"reviews":[]}`))
	if err != nil {
		t.Fatalf("parsePRFacts: %v", err)
	}
	if got.Merged != "" || got.Reviews != 0 || got.Additions != 3 {
		t.Errorf("facts = %+v, want the numbers with no merge timestamp", got)
	}
	if _, err := parsePRFacts([]byte("not json")); err == nil {
		t.Error("junk from gh must be an error, so the outcome is recorded without it")
	}
}
