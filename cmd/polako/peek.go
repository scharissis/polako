package main

// The free check between full polls. A drain waiting on a merge or a reply
// used to notice it only at the next -poll: a mean 131s late across 135
// merges. A conditional GET whose ETag still matches answers 304, and a 304
// doesn't count against the rate limit (measured: ten of them moved
// X-Ratelimit-Used by zero), so a wait can ask "did anything change?" every
// few seconds and run the full check only when something did. The full poll
// stays as the fallback, on its own clock.
//
// Not a webhook: `gh webhook forward` is unsupported for production use, needs
// repo admin, allows one forwarder per repo and gives up after three
// reconnects, which a laptop's sleep uses up.

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// defaultPeekInterval is the gap between free checks. What pinConfig pins
// config.peek to.
const defaultPeekInterval = 15 * time.Second

// peekTimeout bounds one free check. A 304 comes back in well under a second;
// anything slower is a network that isn't there, and the full poll handles it.
const peekTimeout = 10 * time.Second

// etagWatch is one resource a wait peeks at. The ETag lives here and nowhere
// else: a restart loses it and starts from a full check, as it always did.
type etagWatch struct {
	path string // a gh api path; {owner}/{repo} is filled in by gh or ghArgs
	etag string
	// off is a response that carried no ETag. With nothing to send back, every
	// peek would be a 200, so the watch stops rather than turn into a full
	// check every few seconds.
	off bool
}

func prWatch(prNumber int) *etagWatch {
	return &etagWatch{path: fmt.Sprintf("repos/{owner}/{repo}/pulls/%d", prNumber)}
}

// issueWatch watches the issue, not its comments: the comments list is paged
// at 100, so page one's ETag stays put when a reply lands on a long thread.
// The issue's own comment count and updated_at move instead.
func issueWatch(issue int) *etagWatch {
	return &etagWatch{path: fmt.Sprintf("repos/{owner}/{repo}/issues/%d", issue)}
}

// peekOn says whether the seam is set and shorter than the full poll.
func peekOn(cfg config) bool {
	return cfg.peek > 0 && cfg.peek < cfg.poll
}

// peeking says whether a wait should peek at all: peekOn, and something left
// to watch.
func peeking(cfg config, watches []*etagWatch) bool {
	if !peekOn(cfg) {
		return false
	}
	for _, w := range watches {
		if !w.off {
			return true
		}
	}
	return false
}

// nextCheck is the tail of every wait's log line, so the log says which clock
// is which.
func nextCheck(cfg config) string {
	if !peekOn(cfg) {
		return "next check in " + cfg.poll.String()
	}
	return fmt.Sprintf("next full check in %s, a free one every %s", cfg.poll, cfg.peek)
}

// waitForChange sleeps until deadline, peeking at each watch every cfg.peek on
// the way. It returns the indexes of the watches that changed, or none once the
// deadline is reached — the caller's cue for its full check. A caller that
// finds an early wake was nothing it cares about calls again with the same
// deadline, so the full check still runs at least every -poll.
func waitForChange(ctx context.Context, cfg config, deadline time.Time, watches []*etagWatch) ([]int, error) {
	if !peeking(cfg, watches) {
		return nil, sleep(ctx, time.Until(deadline))
	}
	// Prime first: a watch with no ETag yet takes one now, right after the full
	// check the caller just made, so a change in the next few seconds still
	// shows up as a change.
	for _, w := range watches {
		if w.etag == "" {
			w.check(ctx, cfg)
		}
	}
	for {
		left := time.Until(deadline)
		if left <= 0 || !peeking(cfg, watches) {
			return nil, sleep(ctx, left)
		}
		if err := sleep(ctx, min(cfg.peek, left)); err != nil {
			return nil, err
		}
		if time.Until(deadline) <= 0 {
			return nil, nil
		}
		var changed []int
		for i, w := range watches {
			if w.check(ctx, cfg) {
				changed = append(changed, i)
			}
		}
		if len(changed) > 0 {
			return changed, nil
		}
	}
}

// check makes one conditional GET and reports whether the resource changed
// since the last one. Silent on a 304 and on any error: the full poll reports
// failures already, and a missed peek costs no more than the wait did before.
func (w *etagWatch) check(ctx context.Context, cfg config) bool {
	if w.off {
		return false
	}
	// Path first, flags after: gh takes either order, and the fake gh routes
	// on the path.
	args := []string{"api", w.path, "-i"}
	if w.etag != "" {
		// A weak W/"…" ETag works sent back as-is.
		args = append(args, "-H", "If-None-Match: "+w.etag)
	}
	// Bounded, so a peek hung on a dead connection after a laptop sleep can't
	// hold the wait past its deadline and the full check behind it.
	ctx, cancel := context.WithTimeout(ctx, peekTimeout)
	defer cancel()
	// gh exits 1 on a 304 (stderr "gh: HTTP 304", gh 2.101.0), so the status
	// line is read off stdout whatever the exit code.
	out, _ := captureAll(ctx, cfg.dir, cfg.env, nil, cfg.ghBin, ghArgs(cfg.ghRepo, args)...)
	status, etag := parsePeek(out)
	switch {
	case status != 200:
		return false
	case etag == "":
		w.off = true
		return false
	case w.etag == "":
		w.etag = etag
		return false
	case etag == w.etag:
		return false
	default:
		w.etag = etag
		return true
	}
}

// parsePeek reads the status code and ETag off `gh api -i` output: a status
// line, headers, a blank line, then the body, which is ignored. Zero status
// means the output wasn't that shape.
func parsePeek(out []byte) (status int, etag string) {
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	if !sc.Scan() {
		return 0, ""
	}
	fields := strings.Fields(sc.Text())
	if len(fields) < 2 || !strings.HasPrefix(fields[0], "HTTP/") {
		return 0, ""
	}
	status, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, ""
	}
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if line == "" {
			break
		}
		name, value, ok := strings.Cut(line, ":")
		if ok && strings.EqualFold(strings.TrimSpace(name), "etag") {
			etag = strings.TrimSpace(value)
		}
	}
	return status, etag
}
