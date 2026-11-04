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
	"strings"
	"sync"
	"time"
)

// statsResolveTimeout bounds every repo's lookup together, so an unreachable
// GitHub costs a report this much once rather than per repository.
const statsResolveTimeout = 10 * time.Second

// statsQueryIssues is how many issues one GraphQL query asks about, at two
// lookups apiece. Timed on polako's own repo, five runs over 297 issues: 25
// a query, 8 at once, took 3.4–4.9s (median 4.4s). 4 at once took 5.8–9.1s
// at 25 a query and 4.3–8.5s at 50, too near statsResolveTimeout at the slow
// end.
const statsQueryIssues = 25

// statsQueryParallel is how many queries run at once: enough to fit a long
// record history in statsResolveTimeout (above), few enough that it doesn't
// reach GitHub as one burst. A query costs one point of GraphQL rate limit.
const statsQueryParallel = 8

// prResolution is what the lookup came to: how many issues it settled, how
// many of those had been parked, the repositories GitHub answered nothing
// for, and how many issues elsewhere sat in a query it never answered.
type prResolution struct {
	resolved   int
	afterPark  int
	unreached  []string
	unanswered int
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
// record synthesized from its answer — outcome, timestamp and PR size, so no
// PR text can reach the output. Without that size, the change line would
// sample only the merges a drain saw; where a drain did size the PR, its
// record still wins (changeRecord). A park it overturns moves to is.park
// rather than vanishing: parks are a signal, and only the outcome was wrong.
// One GraphQL query per statsQueryIssues issues. An empty cfg.ghBin skips
// it: the test seam that keeps every other stats test off the network.
//
// Rejected: the one `gh pr list --limit 1000` per repo this replaced. It took
// 3s whatever the history, but an issue whose PR was older than the newest
// 1000 stayed as its records left it, silently, and adding size fields took
// it to 13–26s (median 21s) and once a 502.
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
	queries := prQueries(byRepo, cfg.branchPrefix)
	answers := askAll(ctx, cfg, queries)
	// A repo none of whose queries answered is named, since a token that
	// can't see it and a network that's down read the same from here; one
	// that answered some is counted by issue. Either way a short answer
	// never passes for a whole one.
	reached := map[string]bool{}
	for i, q := range queries {
		reached[q.repo] = reached[q.repo] || answers[i] != nil
	}
	for i, q := range queries {
		switch {
		case !reached[q.repo]:
			if !slices.Contains(res.unreached, q.repo) {
				res.unreached = append(res.unreached, q.repo)
			}
			continue
		case answers[i] == nil:
			res.unanswered += len(q.issues)
			continue
		}
		for j, is := range q.issues {
			rec, ok := answers[i].verdict(is, q.branches[j])
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

// prQuery is one GraphQL query's worth: a repository, up to statsQueryIssues
// of its unsettled issues, and the branch each one's work goes on.
type prQuery struct {
	repo     string
	issues   []*issueStats
	branches []string // issues[i]'s
}

// prQueries splits each repo's unsettled issues into queries, in repo and
// issue order, so a report asks the same questions every time.
func prQueries(byRepo map[string][]*issueStats, branchPrefix string) []prQuery {
	var queries []prQuery
	for _, repo := range slices.Sorted(maps.Keys(byRepo)) {
		list := slices.SortedFunc(slices.Values(byRepo[repo]), func(a, b *issueStats) int {
			return a.key.issue - b.key.issue
		})
		for c := range slices.Chunk(list, statsQueryIssues) {
			q := prQuery{repo: repo, issues: c}
			for _, is := range c {
				q.branches = append(q.branches, fmt.Sprintf("%s%d", branchPrefix, is.key.issue))
			}
			queries = append(queries, q)
		}
	}
	return queries
}

// prFields is everything a query asks about a PR: state, timestamps and
// size, so a title or body never enters this process. Reviews aren't here:
// GitHub gives them only with their authors and bodies, so a resolved issue
// carries no review count.
const prFields = "number state mergedAt closedAt additions deletions changedFiles"

// text asks about each issue's two candidate PRs (see verdict) under
// aliases: pN for recorded PR N, once however many issues recorded it, and
// bN for the newest PR on issue N's branch. Branch names go as variables,
// so none needs quoting.
func (q prQuery) text() string {
	var vars, asks strings.Builder
	asked := map[int]bool{}
	for _, is := range q.issues {
		n := is.key.issue
		fmt.Fprintf(&vars, ", $b%d: String!", n)
		if pr := lastPR(is); pr != 0 && !asked[pr] {
			asked[pr] = true
			fmt.Fprintf(&asks, " p%d: pullRequest(number: %d) { ...pr }", pr, pr)
		}
		fmt.Fprintf(&asks, " b%d: pullRequests(headRefName: $b%d, first: 1, "+
			"orderBy: {field: CREATED_AT, direction: DESC}) { nodes { ...pr } }", n, n)
	}
	return "query($owner: String!, $name: String!" + vars.String() + ") " +
		"{ repository(owner: $owner, name: $name) {" + asks.String() + " } } " +
		"fragment pr on PullRequest { " + prFields + " }"
}

// askAll runs every query, statsQueryParallel at once, each one asked again
// if the network lost it, in whatever time is left. nil marks a query GitHub
// never answered.
func askAll(ctx context.Context, cfg config, queries []prQuery) []*prAnswers {
	answers := make([]*prAnswers, len(queries))
	slots := make(chan struct{}, statsQueryParallel)
	var wg sync.WaitGroup
	for i, q := range queries {
		wg.Go(func() {
			slots <- struct{}{}
			defer func() { <-slots }()
			for try := 0; try < 2 && ctx.Err() == nil; try++ {
				ans, retry := q.ask(ctx, cfg)
				if answers[i] = ans; !retry {
					return
				}
			}
		})
	}
	wg.Wait()
	return answers
}

// ask is one query's answers, nil when GitHub gave none. GitHub answers a PR
// number it can't resolve with null beside the rest, and gh exits 1 over
// that, so the body is read whatever the exit and every PR it did answer for
// is kept. retry is false once GitHub itself answered: a repository the
// token can't see comes back null, and asking again won't change that.
func (q prQuery) ask(ctx context.Context, cfg config) (ans *prAnswers, retry bool) {
	owner, name, _ := strings.Cut(q.repo, "/")
	args := []string{"api", "graphql", "-f", "query=" + q.text(), "-f", "owner=" + owner, "-f", "name=" + name}
	for i, is := range q.issues {
		args = append(args, "-f", fmt.Sprintf("b%d=%s", is.key.issue, q.branches[i]))
	}
	out, _ := captureAll(ctx, cfg.dir, cfg.env, nil, cfg.ghBin, args...)
	var resp struct {
		Data *struct {
			Repository map[string]json.RawMessage `json:"repository"`
		} `json:"data"`
	}
	if json.Unmarshal(out, &resp) != nil || resp.Data == nil {
		return nil, true
	}
	if resp.Data.Repository == nil {
		return nil, false
	}
	alias := resp.Data.Repository
	ans = &prAnswers{byNumber: map[int]prState{}, byBranch: map[string]prState{}}
	for i, is := range q.issues {
		var branch struct {
			Nodes []prState `json:"nodes"`
		}
		if json.Unmarshal(alias[fmt.Sprintf("b%d", is.key.issue)], &branch) == nil && len(branch.Nodes) > 0 {
			ans.byBranch[q.branches[i]] = branch.Nodes[0]
		}
		var pr *prState
		if n := lastPR(is); n != 0 && json.Unmarshal(alias[fmt.Sprintf("p%d", n)], &pr) == nil && pr != nil {
			ans.byNumber[n] = *pr
		}
	}
	return ans, false
}

// prAnswers is what GitHub said about one query's PRs: each recorded PR by
// number, and the newest PR on each issue's branch. A branch gives only its
// newest because an issue worked twice reuses its branch name, and the
// newest PR on it is the attempt still in question.
type prAnswers struct {
	byNumber map[int]prState
	byBranch map[string]prState
}

// verdict is GitHub's answer for one unsettled issue, if it has one. Two PRs
// can speak for it: the last one its runs recorded, and the newest on its
// branch — the issue-N contract, which finds a PR no run recorded. Either
// merging settles it as merged. A park takes nothing less: a PR closed or
// still open after one is the park's own story. An issue in flight ends
// closed once every PR speaking for it closed, and stays in flight while one
// is open.
func (a prAnswers) verdict(is *issueStats, branch string) (issueRecord, bool) {
	since := settledSince(is)
	var closed prState
	open := false
	for _, st := range []prState{a.byNumber[lastPR(is)], a.byBranch[branch]} {
		if !st.speaksSince(since) {
			continue
		}
		switch {
		case st.State == "MERGED":
			return st.record(is, issueMerged, st.MergedAt), true
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
	return closed.record(is, issueClosed, closed.ClosedAt), true
}

type prState struct {
	Number       int    `json:"number"`
	State        string `json:"state"`
	MergedAt     string `json:"mergedAt"`
	ClosedAt     string `json:"closedAt"`
	Additions    int    `json:"additions"`
	Deletions    int    `json:"deletions"`
	ChangedFiles int    `json:"changedFiles"`
}

// speaksSince reports whether this PR can be the issue's outcome: one GitHub
// answered with, still open or ended after since. An ended PR with no
// readable time is left out rather than guessed at.
func (st prState) speaksSince(since time.Time) bool {
	switch st.State {
	case "MERGED":
		return recTime(st.MergedAt).After(since)
	case "CLOSED":
		return recTime(st.ClosedAt).After(since)
	}
	return st.Number != 0 // the zero value is a PR GitHub didn't answer with
}

// record is the terminal record this PR settles an issue with.
func (st prState) record(is *issueStats, outcome, ts string) issueRecord {
	return issueRecord{Repo: is.key.repo, Issue: is.key.issue, PR: st.Number, Outcome: outcome, TS: ts,
		Additions: st.Additions, Deletions: st.Deletions, ChangedFiles: st.ChangedFiles}
}

// note is the one line the report prints when a lookup failed: a repo GitHub
// didn't answer for, or a query it didn't answer, so a short report never
// passes for a whole one. It names repositories and counts, never gh's own
// error text, which can carry hostnames and token scopes.
func (res prResolution) note() string {
	var says string
	switch n := res.unanswered; {
	case len(res.unreached) > 0 && n > 0:
		do := "do"
		if n == 1 {
			do = "does"
		}
		says = fmt.Sprintf("GitHub didn't answer for %s — their in-flight and parked issues stay as they are, as %s %s elsewhere",
			andList(res.unreached), do, plural(n, "more issue"))
	case len(res.unreached) > 0:
		says = fmt.Sprintf("GitHub didn't answer for %s — their in-flight and parked issues stay as they are",
			andList(res.unreached))
	case n == 1:
		says = "GitHub didn't answer about 1 issue — it stays as it is"
	case n > 1:
		says = fmt.Sprintf("GitHub didn't answer about %d issues — they stay as they are", n)
	default:
		return ""
	}
	return says + "; rerun to retry"
}
