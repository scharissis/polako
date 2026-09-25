package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// answerPeek is the fake gh's side of `gh api <path> -i [-H If-None-Match]`:
// a PR's ETag moves with its state, mergeability and head, an issue's with its
// comment count. A matching If-None-Match answers the way gh 2.101.0 does —
// the 304 status line on stdout, "gh: HTTP 304" on stderr, exit 1.
func answerPeek(st *ghState, args []string) (string, bool, int) {
	path := args[1]
	var inm string
	if i := slices.Index(args, "-H"); i >= 0 && i+1 < len(args) {
		inm, _ = strings.CutPrefix(args[i+1], "If-None-Match: ")
	}
	var etag string
	var noETag, always200, changed bool
	if _, n, ok := strings.Cut(path, "/pulls/"); ok {
		var pr *fakePR
		for _, p := range st.PRs {
			if strconv.Itoa(p.Number) == n {
				pr = p
			}
		}
		if pr == nil {
			fmt.Fprintf(os.Stderr, "gh: Not Found (HTTP 404)\n")
			return "HTTP/2.0 404 Not Found\r\n\r\n{}", false, 1
		}
		if pr.MergeOnPeek > 0 {
			pr.MergeOnPeek--
			if pr.MergeOnPeek == 0 {
				pr.State = "MERGED"
			}
			changed = true
		}
		etag = fmt.Sprintf(`W/"%s-%s-%s"`, pr.State, pr.Mergeable, pr.Head)
		noETag, always200 = pr.NoETag, pr.SameETag200
	} else {
		is := st.Issues[apiIssue(path)]
		if is == nil {
			fmt.Fprintf(os.Stderr, "gh: Not Found (HTTP 404)\n")
			return "HTTP/2.0 404 Not Found\r\n\r\n{}", false, 1
		}
		changed = is.ReplyOnPeek > 0 || is.BotOnPeek > 0
		if is.ReplyOnPeek > 0 {
			is.ReplyOnPeek--
			if is.ReplyOnPeek == 0 {
				is.Comments++
			}
		}
		if is.BotOnPeek > 0 {
			is.BotOnPeek--
			if is.BotOnPeek == 0 {
				is.Comments++
				is.Bots = append(is.Bots, is.Comments)
			}
		}
		etag = fmt.Sprintf(`W/"%d"`, is.Comments)
	}
	if !noETag && !always200 && inm == etag {
		fmt.Fprintln(os.Stderr, "gh: HTTP 304")
		return "HTTP/2.0 304 Not Modified\r\nEtag: " + etag + "\r\n\r\n", changed, 1
	}
	head := "HTTP/2.0 200 OK\r\nContent-Type: application/json; charset=utf-8\r\n"
	if !noETag {
		head += "Etag: " + etag + "\r\n"
	}
	return head + "\r\n{}", changed, 0
}

// peekConfig is drainConfig with the free check on and every gh call logged.
func peekConfig(t *testing.T, st *ghState, poll, peek time.Duration) (config, string) {
	t.Helper()
	cfg, _ := drainConfig(t, "stream", st)
	cfg.poll, cfg.peek = poll, peek
	logPath := filepath.Join(t.TempDir(), "gh.log")
	cfg.env = append(cfg.env, fakeGhLogEnv+"="+logPath)
	return cfg, logPath
}

// ghCalls counts the logged gh calls that start with prefix.
func ghCalls(t *testing.T, logPath, prefix string) int {
	t.Helper()
	b, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("reading gh log: %v", err)
	}
	n := 0
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, prefix) {
			n++
		}
	}
	return n
}

func openPR(mut func(*fakePR)) *ghState {
	pr := &fakePR{Number: 9, State: "OPEN", Mergeable: "MERGEABLE", Head: "abc123", Checks: []string{"SUCCESS"}}
	mut(pr)
	return &ghState{Issues: map[string]*fakeIssue{"1": {Open: true}}, PRs: map[string]*fakePR{"issue-1": pr}}
}

// A PR nobody touched answers 304 (or 200 with the same ETag) to every peek,
// and neither wakes the full check: it runs on entry and at each -poll only.
func TestPeekUnchangedPRWakesNothing(t *testing.T) {
	t.Parallel()
	for name, same200 := range map[string]bool{"304": false, "200 same ETag": true} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cfg, logPath := peekConfig(t, openPR(func(pr *fakePR) {
				pr.MergeOnRead, pr.SameETag200 = 2, same200
			}), 300*time.Millisecond, 20*time.Millisecond)

			state, err := supervisePR(context.Background(), cfg, 1, 9, &issueState{}, &issueTally{}, runChoice{})
			if err != nil || state != "MERGED" {
				t.Fatalf("supervisePR = %q, %v; want MERGED", state, err)
			}
			if got := ghCalls(t, logPath, "pr view 9"); got != 2 {
				t.Errorf("ran the full check %d times, want 2 (entry, then one -poll)", got)
			}
			if got := ghCalls(t, logPath, "api repos/{owner}/{repo}/pulls/9 -i -H If-None-Match: "); got < 2 {
				t.Errorf("sent %d conditional peeks, want several between the full checks", got)
			}
		})
	}
}

// The point of the issue: a merge is noticed at the next peek, not at -poll.
// With -poll an hour, only the peek can end this wait inside the deadline.
func TestPeekChangedPREndsSupervisePREarly(t *testing.T) {
	t.Parallel()
	cfg, logPath := peekConfig(t, openPR(func(pr *fakePR) { pr.MergeOnPeek = 3 }),
		time.Hour, 10*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	state, err := supervisePR(ctx, cfg, 1, 9, &issueState{}, &issueTally{}, runChoice{})
	if err != nil || state != "MERGED" {
		t.Fatalf("supervisePR = %q, %v; want MERGED well before -poll", state, err)
	}
	if got := ghCalls(t, logPath, "pr view 9"); got != 2 {
		t.Errorf("ran the full check %d times, want 2 (entry, then the wake)", got)
	}
}

// No ETag, nothing to send back: the watch turns itself off after one peek
// rather than becoming a full check every few seconds.
func TestPeekNoETagFallsBackToPlainPolling(t *testing.T) {
	t.Parallel()
	cfg, logPath := peekConfig(t, openPR(func(pr *fakePR) { pr.MergeOnRead, pr.NoETag = 3, true }),
		50*time.Millisecond, 5*time.Millisecond)

	state, err := supervisePR(context.Background(), cfg, 1, 9, &issueState{}, &issueTally{}, runChoice{})
	if err != nil || state != "MERGED" {
		t.Fatalf("supervisePR = %q, %v; want MERGED", state, err)
	}
	if got := ghCalls(t, logPath, "api repos/{owner}/{repo}/pulls/9 -i"); got != 1 {
		t.Errorf("peeked %d times, want 1 and then plain polling", got)
	}
	if got := ghCalls(t, logPath, "pr view 9"); got != 3 {
		t.Errorf("ran the full check %d times, want 3", got)
	}
}

// A person's reply wakes awaitAnswer at the next peek. A bot's comment wakes
// it too, but only to read the thread and go back to waiting.
func TestPeekReplyWakesAwaitAnswerEarly(t *testing.T) {
	t.Parallel()
	buf := captureLog(t)
	cfg, _ := peekConfig(t, &ghState{Issues: map[string]*fakeIssue{
		"1": {Open: true, BotOnPeek: 2, ReplyOnPeek: 4},
	}}, time.Hour, 10*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	states := map[int]*issueState{1: {awaiting: true}}

	issue, err := awaitAnswer(ctx, cfg, []int{1}, states)
	if err != nil || issue != 1 {
		t.Fatalf("awaitAnswer = %d, %v; want 1 well before -poll", issue, err)
	}
	if !states[1].answered {
		t.Error("the reply should mark the issue answered")
	}
	out := buf.String()
	for _, want := range []string{"none of them from a person", "somebody replied on #1",
		"next full check in 1h0m0s, a free one every 10ms"} {
		if !strings.Contains(out, want) {
			t.Errorf("log is missing %q\ngot:\n%s", want, out)
		}
	}
}

// A bot alone never ends the wait early: awaitAnswer still hands back to the
// drain at -poll, and not before.
func TestPeekBotDoesNotEndAwaitAnswer(t *testing.T) {
	t.Parallel()
	const poll = 300 * time.Millisecond
	cfg, _ := peekConfig(t, &ghState{Issues: map[string]*fakeIssue{
		"1": {Open: true, BotOnPeek: 2},
	}}, poll, 10*time.Millisecond)
	states := map[int]*issueState{1: {awaiting: true}}

	start := time.Now()
	issue, err := awaitAnswer(context.Background(), cfg, []int{1}, states)
	if err != nil || issue != 0 {
		t.Fatalf("awaitAnswer = %d, %v; want 0 at -poll", issue, err)
	}
	if took := time.Since(start); took < poll {
		t.Errorf("returned after %s, before -poll (%s)", took, poll)
	}
	if states[1].thread == nil || states[1].thread.etag == "" {
		t.Error("the thread's ETag should outlive the call, for the drain's next pass")
	}
}

func TestPeekReplyWakesWaitForReplyEarly(t *testing.T) {
	t.Parallel()
	cfg, _ := peekConfig(t, &ghState{Issues: map[string]*fakeIssue{
		"1": {Open: true, ReplyOnPeek: 3},
	}}, time.Hour, 10*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := waitForReply(ctx, cfg, 1, 0); err != nil {
		t.Fatalf("waitForReply: %v; want the reply noticed well before -poll", err)
	}
}

func TestParsePeek(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, out string
		status    int
		etag      string
	}{
		{"200 weak", "HTTP/2.0 200 OK\r\nETag: W/\"abc\"\r\nX-Other: y\r\n\r\n{\"etag\":\"no\"}", 200, `W/"abc"`},
		{"304", "HTTP/2.0 304 Not Modified\r\netag: \"abc\"\r\n\r\n", 304, `"abc"`},
		{"no etag", "HTTP/1.1 200 OK\nContent-Type: x\n\n{}", 200, ""},
		{"empty", "", 0, ""},
		{"not http", "{}", 0, ""},
	}
	for _, c := range cases {
		status, etag := parsePeek([]byte(c.out))
		if status != c.status || etag != c.etag {
			t.Errorf("%s: parsePeek = %d, %q; want %d, %q", c.name, status, etag, c.status, c.etag)
		}
	}
}

// Zero — every hand-built config — and anything at or above -poll leave the
// wait exactly as it was.
func TestPeekOffUnlessShorterThanPoll(t *testing.T) {
	t.Parallel()
	w := []*etagWatch{prWatch(1)}
	for _, c := range []struct {
		peek, poll time.Duration
		want       bool
	}{
		{0, time.Minute, false},
		{time.Minute, time.Minute, false},
		{2 * time.Minute, time.Minute, false},
		{15 * time.Second, 5 * time.Minute, true},
	} {
		if got := peeking(config{peek: c.peek, poll: c.poll}, w); got != c.want {
			t.Errorf("peeking(peek %s, poll %s) = %v, want %v", c.peek, c.poll, got, c.want)
		}
	}
	if got := nextCheck(config{poll: 5 * time.Minute}); got != "next check in 5m0s" {
		t.Errorf("nextCheck with peek off = %q", got)
	}
}
