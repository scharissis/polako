package main

// A terminal record is written only when a drain sees the merge. A shift
// interrupted during the merge wait never writes one, and the rerun never
// revisits a closed issue — so without this, an issue merged off-shift reads
// as in flight forever (issue #621: 78 of 81 "in flight" on one operator's
// data had a merged PR). stats asks GitHub instead, on every read, and writes
// nothing back: the records stay write-only, and the answer is never state.

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
// issue whose PR is older than that stays in flight, silently — only on a
// repo with that many PRs since the one in question. A per-PR GraphQL alias
// query would be exact, but this is the batched listing the issue asked for.
const statsResolveLimit = 1000

// prResolution is what the lookup came to: how many in-flight issues it
// settled, and the repositories it could not ask.
type prResolution struct {
	resolved  int
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

// resolveInFlight gives each in-flight issue whose PR has since merged or
// closed a terminal record synthesized from GitHub's answer — outcome and
// timestamp only, so no PR text can reach the output. One `gh pr list` per
// repository rather than one call per PR. An empty cfg.ghBin skips it: the
// test seam that keeps every other stats test off the network.
func resolveInFlight(ctx context.Context, cfg config, issues []*issueStats) prResolution {
	var res prResolution
	if cfg.ghBin == "" {
		return res
	}
	byRepo := map[string][]*issueStats{}
	for _, is := range issues {
		if is.terminal == nil && is.key.repo != "" && lastPR(is) != 0 {
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
	lists := make([]map[int]prState, len(repos))
	errs := make([]error, len(repos))
	var wg sync.WaitGroup
	for i, repo := range repos {
		wg.Go(func() { lists[i], errs[i] = listPRStates(ctx, cfg, repo) })
	}
	wg.Wait()
	for i, repo := range repos {
		states, err := lists[i], errs[i]
		if err != nil {
			res.unreached = append(res.unreached, repo)
			continue
		}
		for _, is := range byRepo[repo] {
			pr := lastPR(is)
			st, ok := states[pr]
			if !ok {
				continue
			}
			rec := issueRecord{Repo: is.key.repo, Issue: is.key.issue, PR: pr}
			switch st.State {
			case "MERGED":
				rec.Outcome, rec.TS = issueMerged, st.MergedAt
			case "CLOSED":
				rec.Outcome, rec.TS = issueClosed, st.ClosedAt
			default:
				continue // still open: in flight is the right answer
			}
			is.terminal = &rec
			res.resolved++
		}
	}
	return res
}

type prState struct {
	Number   int    `json:"number"`
	State    string `json:"state"`
	MergedAt string `json:"mergedAt"`
	ClosedAt string `json:"closedAt"`
}

// listPRStates is one repository's PRs, keyed by number. The field list is
// state and timestamps only, so a title or body never enters this process.
func listPRStates(ctx context.Context, cfg config, repo string) (map[int]prState, error) {
	cfg.ghRepo = repo
	out, err := gh(ctx, cfg, "pr", "list", "--state", "all",
		"--limit", strconv.Itoa(statsResolveLimit), "--json", "number,state,mergedAt,closedAt")
	if err != nil {
		return nil, err
	}
	var rows []prState
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, fmt.Errorf("parsing PR list: %w", err)
	}
	states := make(map[int]prState, len(rows))
	for _, r := range rows {
		states[r.Number] = r
	}
	return states, nil
}

// note is the one line the report prints when a lookup failed. It names the
// repositories, never gh's own error text, which can carry hostnames and
// token scopes.
func (res prResolution) note() string {
	if len(res.unreached) == 0 {
		return ""
	}
	return fmt.Sprintf("GitHub didn't answer for %s — their in-flight issues stay in flight; rerun to retry",
		andList(res.unreached))
}
