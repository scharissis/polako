package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

// A remediation on the same combo adds its reason to the line already there
// rather than repeating it; a new combo goes last; a line nobody can parse
// stays where it was, verbatim.
func TestSpliceRanOnMergesIntoTheBlock(t *testing.T) {
	t.Parallel()
	body := "Summary.\r\n\r\nCloses #7\r\n\r\n" + ranOnBegin + "\r\n" +
		"Ran on anthropic · m1 · effort inherited (implement)\r\n\r\n" +
		"a hand edit\r\n" + ranOnEnd + "\r\nfooter\r\n"
	got := spliceRanOn(body, []prRanOn{
		{combo: "anthropic · m1 · effort inherited", reasons: []string{"implement", "checks"}},
		{combo: "anthropic · m2 (asked for sonnet) · effort medium", reasons: []string{"review"}},
	})
	want := "Summary.\r\n\r\nCloses #7\r\n\r\n" + ranOnBegin + "\n" +
		"Ran on anthropic · m1 · effort inherited (implement, checks)\n\n" +
		"a hand edit\n\n" +
		"Ran on anthropic · m2 (asked for sonnet) · effort medium (review)\n" + ranOnEnd + "\r\nfooter\r\n"
	if got != want {
		t.Errorf("spliceRanOn:\n got %q\nwant %q", got, want)
	}
	if again := spliceRanOn(got, nil); again != got {
		t.Errorf("a second splice with nothing new changed the body:\n got %q\nwant %q", again, got)
	}
}

// The first write appends after whatever the body ends with — the skill's own
// `Closes #N` — with a blank line between, however the body ended.
func TestSpliceRanOnAppendsAtTheEnd(t *testing.T) {
	t.Parallel()
	line := []prRanOn{{combo: "anthropic · m · effort high", reasons: []string{"implement"}}}
	block := ranOnBegin + "\nRan on anthropic · m · effort high (implement)\n" + ranOnEnd + "\n"
	for _, body := range []string{"Closes #7", "Closes #7\n", "Closes #7\n\n"} {
		if got, want := spliceRanOn(body, line), strings.TrimRight(body, "\n")+"\n\n"+block; got != want {
			t.Errorf("body %q:\n got %q\nwant %q", body, got, want)
		}
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
// the body is left alone and the real block goes on the end.
func TestSpliceRanOnIgnoresQuotedMarkers(t *testing.T) {
	t.Parallel()
	body := "Writes between `" + ranOnBegin + "` and `" + ranOnEnd + "`.\n\nCloses #7\n"
	got := spliceRanOn(body, []prRanOn{{combo: "c", reasons: []string{"implement"}}})
	want := body + "\n" + ranOnBegin + "\nRan on c (implement)\n" + ranOnEnd + "\n"
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
// after `Closes #1`, the remediation run's joins it, and the body outside the
// markers is what the skill wrote.
func TestDrainNotesRanOnInThePRBody(t *testing.T) {
	t.Parallel()
	buf := captureLog(t)
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
	want := "Summary.\n\nCloses #1\n\n" + ranOnBegin + "\n" +
		"Ran on anthropic · claude-opus-5 · effort inherited (implement)\n\n" +
		"Ran on anthropic · claude-opus-5 (asked for sonnet) · effort medium (remediate)\n" +
		ranOnEnd + "\n"
	if got != want {
		t.Errorf("PR body:\n got %q\nwant %q\nlog:\n%s", got, want, buf.String())
	}
	if n := strings.Count(buf.String(), "noted on PR #42 which provider"); n != 2 {
		t.Errorf("logged %d ran-on writes, want 2 (implement, rebase)\n%s", n, buf.String())
	}
}

// Best-effort: a refused edit is one warning, and the drain goes on to merge.
func TestDrainCarriesOnWhenTheRanOnWriteFails(t *testing.T) {
	t.Parallel()
	buf := captureLog(t)
	cfg, path := drainConfig(t, "implementmerged", &ghState{
		Issues:    map[string]*fakeIssue{"1": {Open: true}},
		FailReads: map[string]int{"pr edit": 1},
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
	if want := "could not note on PR #42 which model and effort ran on it"; !strings.Contains(buf.String(), want) {
		t.Errorf("log is missing %q\n%s", want, buf.String())
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
