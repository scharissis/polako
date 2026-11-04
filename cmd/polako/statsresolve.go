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
	"strings"
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

// statsSizeChunk is how many PRs one size query names. One query naming 100
// took 1.4–7.1s on polako's own repo (median 1.9s), too near
// statsResolveTimeout at the slow end, so each names half that.
const statsSizeChunk = 50

// statsSizeParallel is how many size queries run at once, so a long record
// history doesn't arrive at GitHub as one burst of them.
const statsSizeParallel = 4

// prResolution is what the lookup came to: how many issues it settled, how
// many of those had been parked, how many it settled but couldn't size, and
// the repositories it could not ask.
type prResolution struct {
	resolved  int
	afterPark int
	unsized   int
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
// record synthesized from its answer — outcome, timestamp and, where no
// drain recorded one, the PR's size, so no PR text can reach the output.
// Without that size, the change line would sample only the merges a drain
// saw. A park it overturns moves to is.park rather than vanishing: parks are
// a signal, and only the outcome was wrong. One `gh pr list` per repository
// for the outcome, and beside it one GraphQL query per statsSizeChunk PRs
// for the size. An empty cfg.ghBin skips it: the test seam that keeps every
// other stats test off the network.
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
	// timeout and get them reported as unreachable. The sizes of the PRs the
	// records name are asked beside the listing too, not after it: one slow
	// run that waited lost a whole batch to the timeout (242 issues sized,
	// against 292 on the three runs after it). That asks after some PRs
	// still open, whose answers go unused.
	repos := slices.Sorted(maps.Keys(byRepo))
	early := map[string][]int{}
	for repo, list := range byRepo {
		for _, is := range list {
			if pr := lastPR(is); pr != 0 && unsizedByDrain(is) {
				early[repo] = append(early[repo], pr)
			}
		}
	}
	lists := make([]prListing, len(repos))
	errs := make([]error, len(repos))
	var sizes map[string]map[int]prSize
	var wg sync.WaitGroup
	for i, repo := range repos {
		wg.Go(func() { lists[i], errs[i] = listPRStates(ctx, cfg, repo) })
	}
	wg.Go(func() { sizes = lookupSizes(ctx, cfg, early) })
	wg.Wait()
	var needed []*issueStats
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
			if unsizedByDrain(is) {
				needed = append(needed, is)
			}
		}
	}
	res.unsized = sizeSettled(ctx, cfg, needed, sizes)
	return res
}

// unsizedByDrain reports whether an issue's PR size has to come from GitHub:
// no record a drain wrote for it carries one. The same call answers before
// resolution, when written is an unsettled issue's own park or nothing, and
// after it.
func unsizedByDrain(is *issueStats) bool {
	w := is.written()
	return w == nil || !w.enriched()
}

// sizeSettled puts GitHub's size on each settled issue's synthesized record,
// asking now for any PR the early round didn't answer — one found only on
// its branch, or one whose batch failed — in whatever time is left. A size
// it still can't get costs only the size, never the outcome: it returns how
// many, for the report's note.
func sizeSettled(ctx context.Context, cfg config, needed []*issueStats, sizes map[string]map[int]prSize) int {
	late := map[string][]int{}
	for _, is := range needed {
		if _, ok := sizes[is.key.repo][is.terminal.PR]; !ok {
			late[is.key.repo] = append(late[is.key.repo], is.terminal.PR)
		}
	}
	for repo, got := range lookupSizes(ctx, cfg, late) {
		if sizes[repo] == nil {
			sizes[repo] = map[int]prSize{}
		}
		maps.Copy(sizes[repo], got)
	}
	missed := 0
	for _, is := range needed {
		sz, ok := sizes[is.key.repo][is.terminal.PR]
		if !ok {
			missed++
			continue
		}
		is.terminal.Additions, is.terminal.Deletions, is.terminal.ChangedFiles =
			sz.Additions, sz.Deletions, sz.ChangedFiles
	}
	return missed
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

// prListFields is state, timestamps and head branch only, so a title or body
// never enters this process. Size isn't here on purpose: adding additions,
// deletions and changedFiles took a 1000-PR listing of polako's own repo
// from 3s to 13–26s (median 21s), and GitHub answered one such listing with
// a 502. lookupSizes asks for it instead, for the few PRs that need it.
const prListFields = "number,state,mergedAt,closedAt,headRefName"

// listPRStates is one repository's PRs.
func listPRStates(ctx context.Context, cfg config, repo string) (prListing, error) {
	cfg.ghRepo = repo
	out, err := gh(ctx, cfg, "pr", "list", "--state", "all",
		"--limit", strconv.Itoa(statsResolveLimit), "--json", prListFields)
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

// prSize is the one thing a size query asks about a PR. Reviews aren't
// asked: GitHub gives them only with their authors and bodies, so a resolved
// issue carries no review count.
type prSize struct {
	Additions    int `json:"additions"`
	Deletions    int `json:"deletions"`
	ChangedFiles int `json:"changedFiles"`
}

// lookupSizes is GitHub's size for each PR named, by repository and number.
// Best-effort throughout: a query that fails or runs out of time leaves its
// PRs out, so the change line counts their issues out rather than as zeros.
func lookupSizes(ctx context.Context, cfg config, numbers map[string][]int) map[string]map[int]prSize {
	chunks := sizeChunks(numbers)
	answers := make([]map[int]prSize, len(chunks))
	slots := make(chan struct{}, statsSizeParallel)
	var wg sync.WaitGroup
	for i, c := range chunks {
		wg.Go(func() {
			slots <- struct{}{}
			defer func() { <-slots }()
			answers[i] = lookupPRSizes(ctx, cfg, c)
		})
	}
	wg.Wait()
	sizes := map[string]map[int]prSize{}
	for i, c := range chunks {
		if sizes[c.repo] == nil {
			sizes[c.repo] = map[int]prSize{}
		}
		maps.Copy(sizes[c.repo], answers[i])
	}
	return sizes
}

// sizeChunk is one size query's worth: a repository and up to statsSizeChunk
// of its PR numbers.
type sizeChunk struct {
	repo    string
	numbers []int
}

// sizeChunks splits each repo's PR numbers into queries, each PR asked about
// once even when two issues settled on it.
func sizeChunks(byRepo map[string][]int) []sizeChunk {
	var chunks []sizeChunk
	for _, repo := range slices.Sorted(maps.Keys(byRepo)) {
		numbers := slices.Clone(byRepo[repo])
		slices.Sort(numbers)
		for c := range slices.Chunk(slices.Compact(numbers), statsSizeChunk) {
			chunks = append(chunks, sizeChunk{repo, c})
		}
	}
	return chunks
}

// sizeQuery names each PR once, by number, under an alias, and asks for its
// size and nothing else.
func sizeQuery(numbers []int) string {
	var b strings.Builder
	b.WriteString("query($owner: String!, $name: String!) { repository(owner: $owner, name: $name) {")
	for _, n := range numbers {
		fmt.Fprintf(&b, " p%d: pullRequest(number: %d) { additions deletions changedFiles }", n, n)
	}
	b.WriteString(" } }")
	return b.String()
}

// lookupPRSizes is one chunk's sizes, keyed by PR number. GitHub answers a
// number it can't resolve with null beside the rest, and gh exits 1 over
// that, so the body is read whatever the exit and every PR it did answer
// for is kept.
func lookupPRSizes(ctx context.Context, cfg config, c sizeChunk) map[int]prSize {
	owner, name, _ := strings.Cut(c.repo, "/")
	out, _ := captureAll(ctx, cfg.dir, cfg.env, nil, cfg.ghBin, "api", "graphql",
		"-f", "query="+sizeQuery(c.numbers), "-f", "owner="+owner, "-f", "name="+name)
	var resp struct {
		Data struct {
			Repository map[string]*prSize `json:"repository"`
		} `json:"data"`
	}
	if json.Unmarshal(out, &resp) != nil {
		return nil
	}
	sizes := map[int]prSize{}
	for alias, sz := range resp.Data.Repository {
		n, err := strconv.Atoi(strings.TrimPrefix(alias, "p"))
		if sz != nil && err == nil {
			sizes[n] = *sz
		}
	}
	return sizes
}

// note is the one line the report prints when a lookup failed: a repo
// GitHub didn't answer for, or a size it didn't give, so a short change line
// never passes for a whole one. It names repositories and counts, never
// gh's own error text, which can carry hostnames and token scopes.
func (res prResolution) note() string {
	var parts []string
	if len(res.unreached) > 0 {
		parts = append(parts, fmt.Sprintf("GitHub didn't answer for %s — their in-flight and parked issues stay as they are",
			andList(res.unreached)))
	}
	if res.unsized > 0 {
		them := "them"
		if res.unsized == 1 {
			them = "it"
		}
		parts = append(parts, fmt.Sprintf("GitHub gave no PR size for %s it settled, so change per issue leaves %s out",
			plural(res.unsized, "issue"), them))
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "; ") + "; rerun to retry"
}
