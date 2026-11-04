package main

// A terminal record is written only when a drain sees the merge. A shift
// interrupted during the merge wait never writes one, and the rerun never
// revisits a closed issue — so without this, an issue merged off-shift reads
// as in flight forever (issue #621: 78 of 81 "in flight" on one operator's
// data had a merged PR). A park is the same blind spot from the other side:
// the queue never revisits a needs-human issue, so one a human finished —
// fixing and merging polako's PR, or unparking it for a later run whose PR
// merged off-shift — keeps its park record forever (20 of 22 parks on that
// same operator's data had merged). stats asks GitHub instead, on every
// read, and writes nothing back: the records stay write-only, and the answer
// is never state.

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"sync"
	"time"
)

// statsResolveTimeout bounds every repo's lookup together, so an unreachable
// GitHub costs a report this much once rather than per repository.
const statsResolveTimeout = 10 * time.Second

// statsResolveLimit is how many PRs one listing reads back, newest first. An
// issue whose PR is older than that stays as its records left it, silently —
// only on a repo with that many PRs since the one in question. A per-PR
// GraphQL alias query would be exact, but this is the batched listing the
// issue asked for.
const statsResolveLimit = 1000

// prResolution is what the lookup came to: how many issues it settled, how
// many of those had been parked, and the repositories it could not ask.
type prResolution struct {
	resolved  int
	afterPark int
	unreached []string
}

// lastPR is the PR an issue's runs last recorded, 0 for none.
func lastPR(is *issueStats) int {
	for _, r := range slices.Backward(is.runs) {
		if r.PR != 0 {
			return r.PR
		}
	}
	return 0
}

// unsettled reports whether GitHub may know how an issue ended when its
// records don't: none says it ended, or the one that does is a park. A park
// with no readable time can't be put before or after a merge, so it stands.
func unsettled(is *issueStats) bool {
	switch {
	case is.key.repo == "":
		return false
	case is.terminal == nil:
		return true
	}
	return is.terminal.Outcome == issueNeedsHuman && !recTime(is.terminal.TS).IsZero()
}

// settledSince is when a PR must have ended after to speak for an issue: its
// park, or for one in flight its first run. A PR that ended before that was
// earlier work — an issue reopened and worked again reuses its branch name.
func settledSince(is *issueStats) time.Time {
	if is.terminal != nil {
		return recTime(is.terminal.TS)
	}
	if len(is.runs) == 0 {
		return time.Time{}
	}
	return recTime(is.runs[0].TS)
}

// resolveFromGitHub gives each unsettled issue GitHub can settle a terminal
// record synthesized from its answer — outcome and timestamp only, so no PR
// text can reach the output. A park it overturns moves to is.park rather
// than vanishing: parks are a signal, and only the outcome was wrong. One
// `gh pr list` per repository rather than one call per PR. An empty
// cfg.ghBin skips it: the test seam that keeps every other stats test off
// the network.
func resolveFromGitHub(ctx context.Context, cfg config, issues []*issueStats) prResolution {
	var res prResolution
	if cfg.ghBin == "" {
		return res
	}
	byRepo := map[string][]*issueStats{}
	for _, is := range issues {
		if unsettled(is) {
			byRepo[is.key.repo] = append(byRepo[is.key.repo], is)
		}
	}
	if len(byRepo) == 0 {
		return res
	}
	ctx, cancel := context.WithTimeout(ctx, statsResolveTimeout)
	defer cancel()
	// All at once, so one slow repo can't spend the others' share of the
	// timeout and get them reported as unreachable.
	repos := slices.Sorted(maps.Keys(byRepo))
	lists := make([]prListing, len(repos))
	errs := make([]error, len(repos))
	var wg sync.WaitGroup
	for i, repo := range repos {
		wg.Go(func() { lists[i], errs[i] = listPRStates(ctx, cfg, repo) })
	}
	wg.Wait()
	for i, repo := range repos {
		if errs[i] != nil {
			res.unreached = append(res.unreached, repo)
			continue
		}
		for _, is := range byRepo[repo] {
			rec, ok := lists[i].verdict(is, fmt.Sprintf("%s%d", cfg.branchPrefix, is.key.issue))
			if !ok {
				continue
			}
			if is.terminal != nil {
				is.park = is.terminal
				res.afterPark++
			}
			is.terminal, is.resolved = &rec, true
			res.resolved++
		}
	}
	return res
}

// verdict is GitHub's answer for one unsettled issue, if it has one. Two PRs
// can speak for it: the last one its runs recorded, and the newest on its
// branch — the issue-N contract, which finds a PR no run recorded. Either
// merging settles it as merged. A park takes nothing less: a PR closed or
// still open after one is the park's own story. An issue in flight ends
// closed once every PR speaking for it closed, and stays in flight while one
// is open.
func (l prListing) verdict(is *issueStats, branch string) (issueRecord, bool) {
	since := settledSince(is)
	var closed prState
	open := false
	for _, st := range []prState{l.byNumber[lastPR(is)], l.byBranch[branch]} {
		if !st.speaksSince(since) {
			continue
		}
		switch {
		case st.State == "MERGED":
			return issueRecord{Repo: is.key.repo, Issue: is.key.issue, PR: st.Number,
				Outcome: issueMerged, TS: st.MergedAt}, true
		case st.State == "CLOSED":
			if st.Number > closed.Number {
				closed = st
			}
		default:
			open = true
		}
	}
	if is.terminal != nil || open || closed.Number == 0 {
		return issueRecord{}, false
	}
	return issueRecord{Repo: is.key.repo, Issue: is.key.issue, PR: closed.Number,
		Outcome: issueClosed, TS: closed.ClosedAt}, true
}

type prState struct {
	Number      int    `json:"number"`
	State       string `json:"state"`
	MergedAt    string `json:"mergedAt"`
	ClosedAt    string `json:"closedAt"`
	HeadRefName string `json:"headRefName"`
}

// speaksSince reports whether this PR can be the issue's outcome: one the
// listing holds, still open or ended after since. An ended PR with no
// readable time is left out rather than guessed at.
func (st prState) speaksSince(since time.Time) bool {
	switch st.State {
	case "MERGED":
		return recTime(st.MergedAt).After(since)
	case "CLOSED":
		return recTime(st.ClosedAt).After(since)
	}
	return st.Number != 0 // the zero value is a lookup the listing missed
}

// prListing is one repository's PRs, by number and by head branch. A branch
// keeps only its newest: an issue worked twice reuses its branch name, and
// the newest PR on it is the attempt still in question.
type prListing struct {
	byNumber map[int]prState
	byBranch map[string]prState
}

// listPRStates is one repository's PRs. The field list is state, timestamps
// and head branch only, so a title or body never enters this process.
func listPRStates(ctx context.Context, cfg config, repo string) (prListing, error) {
	cfg.ghRepo = repo
	out, err := gh(ctx, cfg, "pr", "list", "--state", "all",
		"--limit", strconv.Itoa(statsResolveLimit), "--json", "number,state,mergedAt,closedAt,headRefName")
	if err != nil {
		return prListing{}, err
	}
	var rows []prState
	if err := json.Unmarshal(out, &rows); err != nil {
		return prListing{}, fmt.Errorf("parsing PR list: %w", err)
	}
	l := prListing{byNumber: make(map[int]prState, len(rows)), byBranch: map[string]prState{}}
	for _, r := range rows {
		l.byNumber[r.Number] = r
		if r.Number > l.byBranch[r.HeadRefName].Number {
			l.byBranch[r.HeadRefName] = r
		}
	}
	return l, nil
}

// note is the one line the report prints when a lookup failed. It names the
// repositories, never gh's own error text, which can carry hostnames and
// token scopes.
func (res prResolution) note() string {
	if len(res.unreached) == 0 {
		return ""
	}
	return fmt.Sprintf("GitHub didn't answer for %s — their in-flight and parked issues stay as they are; rerun to retry",
		andList(res.unreached))
}
