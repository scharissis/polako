package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// ranOnBlock is the block as spliceRanOn renders it, bullets given in order.
func ranOnBlock(bullets ...string) string {
	return ranOnBegin + "\n" + ranOnHeading + "\n\n" + strings.Join(bullets, "\n") + "\n" + ranOnEnd
}

// A remediation on the same combo adds its reason to the line already there
// rather than repeating it; a new combo goes last; a line nobody can parse
// stays where it was, verbatim. A block with text after it stays put, and
// one in the pre-#673 shape comes back in the new one.
func TestSpliceRanOnMergesIntoTheBlock(t *testing.T) {
	t.Parallel()
	body := "Summary.\r\n\r\nCloses #7\r\n\r\n" + ranOnBegin + "\r\n" +
		"Ran on anthropic · m1 · effort inherited (implement)\r\n\r\n" +
		"a hand edit\r\n" + ranOnEnd + "\r\nfooter\r\n"
	got := spliceRanOn(body, []prRanOn{
		{combo: "anthropic · m1 · effort inherited", reasons: []string{"implement", "checks"}},
		{combo: "anthropic · m2 (asked for sonnet) · effort medium", reasons: []string{"review"}},
	})
	want := "Summary.\r\n\r\nCloses #7\r\n\r\n" + ranOnBlock(
		"- anthropic · m1 · effort inherited (implement, checks)",
		"",
		"a hand edit",
		"",
		"- anthropic · m2 (asked for sonnet) · effort medium (review)",
	) + "\r\nfooter\r\n"
	if got != want {
		t.Errorf("spliceRanOn:\n got %q\nwant %q", got, want)
	}
	if again := spliceRanOn(got, nil); again != got {
		t.Errorf("a second splice with nothing new changed the body:\n got %q\nwant %q", again, got)
	}
}

// With no closing trailers the first write goes at the end, with a blank
// line between, however the body ended.
func TestSpliceRanOnAppendsAtTheEnd(t *testing.T) {
	t.Parallel()
	line := []prRanOn{{combo: "anthropic · m · effort high", reasons: []string{"implement"}}}
	block := ranOnBlock("- anthropic · m · effort high (implement)") + "\n"
	for _, body := range []string{"Summary.", "Summary.\n", "Summary.\n\n", "Summary.\n\n\n", "Summary. \r\n\r\n\r\n", "Closes #7\n\nThen more.\n"} {
		got, want := spliceRanOn(body, line), strings.TrimRight(body, " \r\n")+"\n\n"+block
		if got != want {
			t.Errorf("body %q:\n got %q\nwant %q", body, got, want)
		}
		if again := spliceRanOn(got, line); again != got {
			t.Errorf("body %q: a second splice changed it:\n got %q\nwant %q", body, again, got)
		}
	}
	if got, want := spliceRanOn("", line), block; got != want {
		t.Errorf("empty body:\n got %q\nwant %q", got, want)
	}
}

// The first write goes above the body's closing run of trailers, so
// `Closes #N` and the attribution still end the PR.
func TestSpliceRanOnGoesAboveTheTrailers(t *testing.T) {
	t.Parallel()
	line := []prRanOn{{combo: "c", reasons: []string{"implement"}}}
	block := ranOnBlock("- c (implement)")
	for _, tc := range []struct{ head, trailers string }{
		{"Summary.\n\n", "Closes #7\n"},
		{"Summary.\n\n", "Closes #7\n\n🤖 Generated with [Claude Code](https://claude.com/claude-code)\n"},
		{"Summary.\n", "Fixes #7"},
		{"Summary.\r\n\r\n", "resolved owner/repo#7\r\nCo-Authored-By: someone <a@b.c>\r\n"},
		{"", "Closes #7\n"},
	} {
		body := tc.head + tc.trailers
		gap := "\n"
		if tc.head == "" || strings.HasSuffix(tc.head, "\n\n") || strings.HasSuffix(tc.head, "\r\n\r\n") {
			gap = ""
		}
		want := tc.head + gap + block + "\n\n" + tc.trailers
		got := spliceRanOn(body, line)
		if got != want {
			t.Errorf("body %q:\n got %q\nwant %q", body, got, want)
		}
		if again := spliceRanOn(got, line); again != got {
			t.Errorf("body %q: a second splice changed it:\n got %q\nwant %q", body, again, got)
		}
	}
}

// A block written before #673 — old lines, at the very end, below the
// trailers — is merged, reshaped and moved above them, not doubled.
func TestSpliceRanOnMovesAnOldBlockAboveTheTrailers(t *testing.T) {
	t.Parallel()
	trailers := "Closes #7\n\n🤖 Generated with X\n"
	body := "Summary.\n\n" + trailers + "\n" + ranOnBegin + "\nRan on c (implement)\n" + ranOnEnd + "\n"
	got := spliceRanOn(body, []prRanOn{
		{combo: "c", reasons: []string{"remediate"}},
		{combo: "d", reasons: []string{"review"}},
	})
	want := "Summary.\n\n" + ranOnBlock("- c (implement, remediate)", "- d (review)") + "\n\n" + trailers
	if got != want {
		t.Errorf("spliceRanOn:\n got %q\nwant %q", got, want)
	}
	if again := spliceRanOn(got, nil); again != got {
		t.Errorf("a second splice with nothing new changed the body:\n got %q\nwant %q", again, got)
	}
}

// A run refused before doing anything — a usage limit, a dead token — reports
// a model from init all the same, and still names nothing.
func TestNoteRanOnSkipsARunThatDidNothing(t *testing.T) {
	t.Parallel()
	rec := runRecord{Provider: "anthropic", Model: "m", Reason: reasonImplement}
	st := &issueState{}
	noteRanOn(st, rec, runReport{model: "m"})
	if len(st.ranOn) != 0 {
		t.Errorf("a run with no work noted %v", st.ranOn)
	}
	noteRanOn(st, rec, runReport{model: "m", toolUses: 1})
	if len(st.ranOn) != 1 {
		t.Errorf("a run that did work noted %v, want one line", st.ranOn)
	}
}

// Markers quoted inline — this feature's own PR bodies do it — aren't a block:
// the body is left alone and the real block goes in above `Closes #N`.
func TestSpliceRanOnIgnoresQuotedMarkers(t *testing.T) {
	t.Parallel()
	prose := "Writes between `" + ranOnBegin + "` and `" + ranOnEnd + "`.\n\n"
	got := spliceRanOn(prose+"Closes #7\n", []prRanOn{{combo: "c", reasons: []string{"implement"}}})
	want := prose + ranOnBlock("- c (implement)") + "\n\nCloses #7\n"
	if got != want {
		t.Errorf("spliceRanOn:\n got %q\nwant %q", got, want)
	}
}

func TestPRComboNamesWhatRanAndWhatWasAskedFor(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		rec  runRecord
		want string
	}{
		{runRecord{Provider: "anthropic", Model: "claude-opus-5-5[1m]"}, "anthropic · claude-opus-5-5[1m] · effort inherited"},
		{runRecord{Provider: "bedrock", Model: "m", RequestedModel: "sonnet", RequestedEffort: "high"},
			"bedrock · m (asked for sonnet) · effort high"},
		// The probe failed: stats' word for it, not a second one.
		{runRecord{Model: "m"}, "unrecorded · m · effort inherited"},
	} {
		if got := prCombo(tc.rec); got != tc.want {
			t.Errorf("prCombo(%+v) = %q, want %q", tc.rec, got, tc.want)
		}
	}
}

// The whole round trip, under -metrics off: the implement run's line lands
// above `Closes #1`, the remediation run's joins it, and the body outside the
// markers is what the skill wrote. Each write's success line is a detail:
// the shift log gets it, the terminal doesn't.
func TestDrainNotesRanOnInThePRBody(t *testing.T) {
	t.Parallel()
	var term, buf bytes.Buffer
	captureUI(t, &ui{terminal: &term, file: &buf})
	cfg, path := drainConfig(t, "implementthenrebase", &ghState{
		Issues: map[string]*fakeIssue{"1": {Open: true}},
	})
	cfg.provider = "anthropic"
	cfg.remediationModel, cfg.remediationEffort = "sonnet", "medium"

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := drain(ctx, cfg); err != nil {
		t.Fatalf("drain: %v", err)
	}
	got := finalGhState(t, path).PRs["issue-1"].Body
	want := "Summary.\n\n" + ranOnBlock(
		"- anthropic · claude-opus-5 · effort inherited (implement)",
		"- anthropic · claude-opus-5 (asked for sonnet) · effort medium (remediate)",
	) + "\n\nCloses #1\n"
	if got != want {
		t.Errorf("PR body:\n got %q\nwant %q\nlog:\n%s", got, want, buf.String())
	}
	if n := strings.Count(buf.String(), "noted on PR #42 which provider"); n != 2 {
		t.Errorf("logged %d ran-on writes, want 2 (implement, rebase)\n%s", n, buf.String())
	}
	if strings.Contains(term.String(), "noted on PR") {
		t.Errorf("the ran-on success line reached the terminal:\n%s", term.String())
	}
}

// Best-effort: an edit that fails on every try is one warning, and the drain
// goes on to merge.
func TestDrainCarriesOnWhenTheRanOnWriteFails(t *testing.T) {
	t.Parallel()
	buf := captureLog(t)
	cfg, path := drainConfig(t, "implementmerged", &ghState{
		Issues:    map[string]*fakeIssue{"1": {Open: true}},
		FailReads: map[string]int{"pr edit": 100},
	})
	if err := drain(context.Background(), cfg); err != nil {
		t.Fatalf("drain: %v", err)
	}
	st := finalGhState(t, path)
	if st.Issues["1"].Open {
		t.Error("issue 1 should have closed once its PR merged")
	}
	if strings.Contains(st.PRs["issue-1"].Body, ranOnBegin) {
		t.Errorf("the failed edit still wrote the block: %q", st.PRs["issue-1"].Body)
	}
	if want := "could not note on PR #42 which provider, model and effort ran on it"; !strings.Contains(buf.String(), want) {
		t.Errorf("log is missing %q\n%s", want, buf.String())
	}
}

// gh502 is gh's own text for a 502 from the proxy in front of GitHub, page
// and all, as issue #775 logged it.
const gh502 = `non-200 OK status code: 502 Bad Gateway body: "<html>\r\n<head><title>502 Bad Gateway</title></head>\r\n<body>\r\n<center><h1>502 Bad Gateway</h1></center>\r\n</body>\r\n</html>\r\n"`

const (
	ranOnNoted  = "noted on PR #42 which provider"
	ranOnFailed = "could not note on PR #42"
	ranOnAuth   = "gh auth status"
	ranOnServer = "GitHub had a server error"
)

// ranOnWrite runs one writeRanOn against a PR whose `pr edit` fails the way
// fail says, and hands back the log and the state it left.
func ranOnWrite(t *testing.T, fail *fakeFailure) (string, *ghState) {
	t.Helper()
	buf := captureLog(t)
	cfg, path := drainConfig(t, "stream", &ghState{
		PRs:      map[string]*fakePR{"issue-1": {Number: 42, Body: "Summary.\n\nCloses #1\n"}},
		FailWith: map[string]*fakeFailure{"pr edit": fail},
	})
	st := &issueState{ranOn: []prRanOn{{combo: "c", reasons: []string{"implement"}}}}
	writeRanOn(context.Background(), cfg, 42, st)
	return buf.String(), finalGhState(t, path)
}

// GitHub saved the edit and lost the reply (issue #775): the next try's read
// finds the block, sends nothing more, and it ends as a write that worked.
func TestWriteRanOnTakesALostReplyForALandedEdit(t *testing.T) {
	t.Parallel()
	log, st := ranOnWrite(t, &fakeFailure{Times: 100, Stderr: gh502, Apply: true})
	if n := strings.Count(st.PRs["issue-1"].Body, ranOnBegin); n != 1 {
		t.Errorf("PR body holds %d blocks, want 1: %q", n, st.PRs["issue-1"].Body)
	}
	if left := st.FailWith["pr edit"].Times; left != 99 {
		t.Errorf("pr edit ran %d times, want 1: the block was there on the second read", 100-left)
	}
	if !strings.Contains(log, ranOnNoted) || strings.Contains(log, ranOnFailed) {
		t.Errorf("want the success line and no warning:\n%s", log)
	}
	if strings.Contains(log, "<html>") || !strings.Contains(log, "502 Bad Gateway") {
		t.Errorf("want the status without the page:\n%s", log)
	}
}

func TestWriteRanOnRetriesAFailedEdit(t *testing.T) {
	t.Parallel()
	log, st := ranOnWrite(t, &fakeFailure{Times: 2, Stderr: gh502})
	if !strings.Contains(st.PRs["issue-1"].Body, "- c (implement)") {
		t.Errorf("the third try didn't land the block: %q\n%s", st.PRs["issue-1"].Body, log)
	}
	if n := strings.Count(log, "transient: noting on PR #42"); n != 2 {
		t.Errorf("logged %d retries, want 2:\n%s", n, log)
	}
	if !strings.Contains(log, ranOnNoted) || strings.Contains(log, ranOnFailed) {
		t.Errorf("want the success line and no warning:\n%s", log)
	}
}

// A refusal answers the same way every time, so it gets one try and the
// hint that fits it.
func TestWriteRanOnDoesNotRetryARefusal(t *testing.T) {
	t.Parallel()
	log, st := ranOnWrite(t, &fakeFailure{Times: 100, Stderr: "HTTP 403: Resource not accessible by integration (https://api.github.com/graphql)"})
	if left := st.FailWith["pr edit"].Times; left != 99 {
		t.Errorf("pr edit ran %d times, want 1", 100-left)
	}
	if strings.Count(log, ranOnFailed) != 1 || !strings.Contains(log, ranOnAuth) || strings.Contains(log, "transient:") {
		t.Errorf("want one warning with the auth hint and no retry:\n%s", log)
	}
}

// Every try fails and the last read still shows no block: one warning, and a
// 502 isn't the operator's to fix.
func TestWriteRanOnWarnsOnceWhenEveryTryFails(t *testing.T) {
	t.Parallel()
	log, st := ranOnWrite(t, &fakeFailure{Times: 100, Stderr: gh502})
	if left := st.FailWith["pr edit"].Times; left != 100-ghReads {
		t.Errorf("pr edit ran %d times, want %d", 100-left, ghReads)
	}
	if strings.Contains(st.PRs["issue-1"].Body, ranOnBegin) {
		t.Errorf("a failed edit wrote the block: %q", st.PRs["issue-1"].Body)
	}
	if strings.Count(log, ranOnFailed) != 1 || !strings.Contains(log, ranOnServer) {
		t.Errorf("want one warning with the server-error hint:\n%s", log)
	}
	for _, not := range []string{ranOnAuth, "next run", "<html>", ranOnNoted} {
		if strings.Contains(log, not) {
			t.Errorf("log has %q:\n%s", not, log)
		}
	}
}

// A body that can't be read is the other way to the warning: ghReads reads,
// no edit, and the read's own error in the one warning.
func TestWriteRanOnWarnsOnceWhenTheBodyCannotBeRead(t *testing.T) {
	t.Parallel()
	buf := captureLog(t)
	cfg, path := drainConfig(t, "stream", &ghState{
		PRs:      map[string]*fakePR{"issue-1": {Number: 42, Body: "Closes #1\n"}},
		FailWith: map[string]*fakeFailure{"pr view": {Times: 100, Stderr: "HTTP 502: Bad Gateway (https://api.github.com/graphql)"}},
	})
	st := &issueState{ranOn: []prRanOn{{combo: "c", reasons: []string{"implement"}}}}
	writeRanOn(context.Background(), cfg, 42, st)
	final := finalGhState(t, path)
	if left := final.FailWith["pr view"].Times; left != 100-ghReads {
		t.Errorf("pr view ran %d times, want %d", 100-left, ghReads)
	}
	if strings.Contains(final.PRs["issue-1"].Body, ranOnBegin) {
		t.Errorf("an unread body was edited: %q", final.PRs["issue-1"].Body)
	}
	log := buf.String()
	if strings.Count(log, ranOnFailed) != 1 || strings.Count(log, "transient:") != ghReads-1 || !strings.Contains(log, " pr view 42") {
		t.Errorf("want %d retries and one warning naming the read:\n%s", ghReads-1, log)
	}
}

// A 403 that names a rate limit passes with time, so it isn't a refusal; an
// error of neither kind gets no hint at all.
func TestRanOnHintFitsTheError(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		stderr  string
		refused bool
		hint    string
	}{
		{"HTTP 401: Bad credentials (https://api.github.com/graphql)", true, ranOnAuth},
		{"HTTP 403: Must have admin rights to Repository.", true, ranOnAuth},
		{"GraphQL: Resource not accessible by personal access token (updatePullRequest)", true, ranOnAuth},
		{"GraphQL: Your token has not been granted the required scopes to execute this query.", true, ranOnAuth},
		{"GraphQL: someone does not have the correct permissions to execute `UpdatePullRequest` (updatePullRequest)", true, ranOnAuth},
		{"To get started with GitHub CLI, please run:  gh auth login", true, ranOnAuth},
		{"HTTP 403: API rate limit exceeded for user ID 1.", false, ""},
		{"HTTP 403: You have exceeded a secondary rate limit.", false, ""},
		{"HTTP 403: You have triggered an abuse detection mechanism. Please wait a few minutes.", false, ""},
		{"HTTP 502: Bad Gateway (https://api.github.com/graphql)", false, ranOnServer},
		{"non-200 OK status code: 504 Gateway Timeout", false, ranOnServer},
		{"could not connect to api.github.com", false, ""},
	} {
		err := errors.New("gh pr edit 502 --body-file -: exit status 1: " + tc.stderr)
		if got := ghRefused(err); got != tc.refused {
			t.Errorf("ghRefused(%q) = %v, want %v", tc.stderr, got, tc.refused)
		}
		got := ranOnHint(err)
		if (tc.hint == "") != (got == "") || !strings.Contains(got, tc.hint) {
			t.Errorf("ranOnHint(%q) = %q, want it to hold %q", tc.stderr, got, tc.hint)
		}
	}
}

// The page goes, the status before it stays, and stderr with no page —
// other lines around one included — is left alone.
func TestTrimErrorPageKeepsTheStatus(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		gh502:                            "non-200 OK status code: 502 Bad Gateway",
		"warn\n" + gh502 + "\nlast line": "warn\nnon-200 OK status code: 502 Bad Gateway\nlast line",
		`status code: 503 body: "<!DOCTYPE html><html></html>"`: "status code: 503",
		"could not connect to api.github.com\n":                 "could not connect to api.github.com\n",
		`HTTP 422: Validation Failed body: "not a page"`:        `HTTP 422: Validation Failed body: "not a page"`,
		"": "",
	} {
		if got := trimErrorPage(in); got != want {
			t.Errorf("trimErrorPage(%q) = %q, want %q", in, got, want)
		}
	}
}

// A remediation that adds nothing since the last write costs no gh call: the
// gh here doesn't exist, so any call would log a warning.
func TestWriteRanOnSkipsWhenNothingIsNew(t *testing.T) {
	t.Parallel()
	buf := captureLog(t)
	cfg, path := drainConfig(t, "stream", &ghState{PRs: map[string]*fakePR{"issue-1": {Number: 42, Body: "Closes #1\n"}}})
	st := &issueState{ranOn: []prRanOn{{combo: "c", reasons: []string{"implement"}}}}
	writeRanOn(context.Background(), cfg, 42, st)
	if !strings.Contains(finalGhState(t, path).PRs["issue-1"].Body, "- c (implement)") {
		t.Fatalf("the first write didn't land:\n%s", buf.String())
	}
	buf.Reset()
	cfg.ghBin = "/nonexistent/gh"
	writeRanOn(context.Background(), cfg, 42, st)
	if buf.Len() != 0 {
		t.Errorf("a write with nothing new called gh:\n%s", buf.String())
	}
}

// A dry run writes nothing — not even the read before the write.
func TestWriteRanOnSkipsADryRun(t *testing.T) {
	t.Parallel()
	buf := captureLog(t)
	cfg, _ := drainConfig(t, "stream", &ghState{})
	cfg.dryRun = true
	cfg.ghBin = "/nonexistent/gh"
	st := &issueState{ranOn: []prRanOn{{combo: "c", reasons: []string{"implement"}}}}
	writeRanOn(context.Background(), cfg, 42, st)
	if buf.Len() != 0 {
		t.Errorf("a dry run logged something:\n%s", buf.String())
	}
}
