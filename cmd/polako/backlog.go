package main

// Reading the open backlog off GitHub and sorting it into the queues a drain
// acts on: ready now, waiting on a human answer, parked, proposed, held back by
// an unmerged dependency, or a container that is never worked at all. Both
// readers — the drain and `status` — come through selectableIssues, so an
// exclusion added here reaches every one of them at once.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
)

// openIssues asks GitHub what there is to work: the issues ready now, the
// ones a run already flagged for a human, and the containers — never worked
// themselves, but the drain still needs them to notice a finished one.
// -strict-order folds the second list back into the first, which is the whole
// of what the flag does — a flagged issue keeps its place in the queue, and
// everything behind it waits. heldBack is untouched by the flag either way:
// unlike an awaiting-answer issue, running one again this pass cannot reveal
// anything the same listing didn't already know, so it never rejoins ready
// and is reported alongside instead.
func openIssues(ctx context.Context, cfg config) (ready, blocked []int, heldBack []heldBackInfo, containers []containerInfo, err error) {
	q, err := openQueues(ctx, cfg)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if cfg.strictOrder {
		return append(q.ready, q.blocked...), nil, q.heldBack, q.containers, nil
	}
	return q.ready, q.blocked, q.heldBack, q.containers, nil
}

// What the queue is derived from: the labels the exclusions read, the
// sub-issue rollup that says an issue is a container rather than a work item,
// and the blockedBy connection that says a ready issue has an unmerged
// prerequisite. See listOpenIssues for the gh that cannot serve the last two.
const (
	issueFields    = "number,labels"
	subIssuesField = "subIssuesSummary"
	blockedByField = "blockedBy"
)

// openQueues reads the open backlog off GitHub and sorts it. Both readers of
// the queue come through here — the drain and `-dry-run` by way of openIssues,
// `status` directly — so an exclusion added to selectableIssues reaches every
// one of them at once and cannot drift between two copies of the same argv.
func openQueues(ctx context.Context, cfg config) (issueQueues, error) {
	out, err := retryRead(ctx, cfg, "listing open issues", func() ([]byte, error) {
		return listOpenIssues(ctx, cfg)
	})
	if err != nil {
		return issueQueues{}, err
	}
	q, err := selectableIssues(out)
	if err != nil {
		return issueQueues{}, err
	}
	cfg.sayProposals(len(q.proposed))
	return q, nil
}

// listOpenIssues makes the listing call, and copes with a gh too old to know
// what a sub-issue or a blockedBy dependency is: that one rejects the whole
// --json set before it asks GitHub anything, so the fallback is to ask again
// without either field. Inside one retryRead attempt rather than around it, so
// the fallback costs the caller no part of its retry allowance, and remembered
// for the shift, so the rejected call is paid for once rather than once per
// issue.
func listOpenIssues(ctx context.Context, cfg config) ([]byte, error) {
	args := func(fields string) []string {
		a := []string{"issue", "list", "--state", "open", "--limit", "200", "--json", fields}
		if cfg.label != "" {
			a = append(a, "--label", cfg.label)
		}
		return a
	}
	if !cfg.seesExtendedFields() {
		return gh(ctx, cfg, args(issueFields)...)
	}
	out, err := gh(ctx, cfg, args(issueFields+","+subIssuesField+","+blockedByField)...)
	if unknownJSONField(err) {
		cfg.dropExtendedFields()
		return gh(ctx, cfg, args(issueFields)...)
	}
	return out, err
}

// unknownJSONField reports whether gh turned the listing down because it does
// not have one of the fields asked for. Matched on gh's own wording rather than
// on the exit status: every other way that call can fail — a network that has
// not come back, a token GitHub refuses — has to keep its retry rather than
// quietly costing the shift its container skip for good.
func unknownJSONField(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "json field")
}

// issueQueues is the open backlog sorted into what a drain does with each
// issue: work it now, leave it for the human it asked, or leave it alone
// entirely. The drain reads the first two and throws the rest away, since a
// parked issue is one it has agreed not to touch and a proposed one is not its
// to choose; `status` is what parked exists for, because "what is parked?" is a
// question about the backlog rather than about the next run, and proposed is
// what the startup line counts.
//
// Containers are in no queue a drain reads. An issue with sub-issues is a
// tracking container rather than a work item, and it is not held back by
// anything a human could release: reporting it as parked would send an operator
// to take off a label that would change nothing. They are still listed, because
// "not workable" is not "not open" — anything asking which open issues exist,
// `status` deciding whose PR is still live among them, has to see them.
//
// heldBack is a fifth bucket, carved only out of what would otherwise be
// ready: an issue with an open blockedBy dependency. Unlike every other
// exclusion here it is not written anywhere and not a judgement a human made —
// it is recomputed from this same listing every pass, so it holds no queue up
// for longer than its blocker stays open.
type issueQueues struct {
	ready      []int
	blocked    []int
	parked     []int
	proposed   []int
	containers []containerInfo
	heldBack   []heldBackInfo
}

// containerInfo is one container issue and the sub-issue rollup that says
// whether the epic it tracks is done in substance.
type containerInfo struct {
	number    int
	total     int
	completed int
	// held is true when a human has put needs-human or proposed on the
	// container: either one means hands off, so a finished container carrying
	// one is left for a person to close rather than closed by the drain. The
	// same exclusion precedence selectableIssues uses everywhere else — a label
	// a human wrote outranks what the drain would otherwise do.
	held bool
}

// heldBackInfo is one otherwise-ready issue put down for this pass because at
// least one of its blockedBy dependencies is still open, and the open ones
// among them, ascending — what the skip log names.
type heldBackInfo struct {
	number   int
	blockers []int
}

// finished is the one predicate for "this epic is done", so the rest of the
// backlog-fill epic (#101) has a single place to ask rather than each child
// inventing its own. total == 0 cannot happen — a container only exists
// because SubIssues.Total > 0 — but is not finished either way a defensive
// read reaches it.
func (c containerInfo) finished() bool {
	return c.total > 0 && c.completed == c.total
}

// open is every issue the listing found, whichever queue it landed in. The
// question it answers is "is this issue still open?" rather than "would a drain
// work it", so an exclusion must not shorten it.
func (q issueQueues) open() []int {
	all := make([]int, 0, len(q.ready)+len(q.blocked)+len(q.parked)+len(q.proposed)+
		len(q.containers)+len(q.heldBack))
	for _, list := range [][]int{q.ready, q.blocked, q.parked, q.proposed} {
		all = append(all, list...)
	}
	for _, c := range q.containers {
		all = append(all, c.number)
	}
	for _, h := range q.heldBack {
		all = append(all, h.number)
	}
	return all
}

// selectableIssues reads a `gh issue list
// --json number,labels,subIssuesSummary,blockedBy` payload and sorts it into
// the queues: issues ready now, issues already waiting on a human answer,
// issues a previous drain parked, issues a machine proposed that nobody has
// approved, and issues put down for this pass because a dependency has not
// merged. Only the first two are worth working, which is what stops the queue
// handing back the same unimplementable issue on every pass. Labels are
// matched case-insensitively, the way GitHub itself treats them.
//
// The order of the cases is the precedence, and three of them are decisions.
// A container is dropped ahead of every label, because "never a work item" is
// structural and outranks anything written on it. Needs-human beats proposed,
// because parking is a judgement a human has already made about that issue —
// which also keeps the ignoring-proposals line honest, since every issue it
// counts really would queue if the label came off. And an open blockedBy
// dependency is checked last, only against what the switch would otherwise
// call ready: a needs-human, proposed or awaiting-answer classification wins
// outright regardless of any blocker. Awaiting-answer in particular keeps its
// own dedicated poll for a reply (awaitAnswer) running whether or not some
// unrelated dependency has merged — demoting it to held-back on a blocker
// would silently stop that poll with nothing to say so. Held-back is also the
// one exclusion here that is not a durable, labelled judgement: it is
// recomputed from this same listing every pass, so it sits below all four.
//
// Every list comes back ascending because the drain works them lowest first,
// and `gh issue list` guarantees no order of its own.
func selectableIssues(raw []byte) (issueQueues, error) {
	var issues []ghIssue
	if err := json.Unmarshal(raw, &issues); err != nil {
		return issueQueues{}, fmt.Errorf("parsing issue list: %w", err)
	}
	// The state a blockedBy node names is what settles openness. A gh whose
	// node carries no state at all falls back to this — presence among the
	// numbers this same open-issues listing already found — numbers already
	// in hand, no second request paid for an approximation. Built only when
	// something could actually use it: most listings carry no blockedBy node
	// at all, whether because no issue names a dependency yet or because
	// dropExtendedFields already gave up on the field for the shift, and the
	// common case should not pay for a map this fallback will never consult.
	var seenOpen map[int]bool
	if slices.ContainsFunc(issues, func(is ghIssue) bool { return len(is.BlockedBy.Nodes) > 0 }) {
		seenOpen = make(map[int]bool, len(issues))
		for _, is := range issues {
			seenOpen[is.Number] = true
		}
	}
	q := issueQueues{ready: make([]int, 0, len(issues))}
	for _, is := range issues {
		switch {
		case is.SubIssues.Total > 0:
			// A container, and containers are never worked — whatever their
			// labels, so a parent somebody made by hand is protected too.
			q.containers = append(q.containers, containerInfo{
				number:    is.Number,
				total:     is.SubIssues.Total,
				completed: is.SubIssues.Completed,
				held:      is.hasLabel(needsHumanLabel) || is.hasLabel(proposedLabel),
			})
		case is.hasLabel(needsHumanLabel):
			q.parked = append(q.parked, is.Number)
		case is.hasLabel(proposedLabel):
			q.proposed = append(q.proposed, is.Number)
		case is.hasLabel(awaitingAnswerLabel):
			q.blocked = append(q.blocked, is.Number)
		default:
			if blockers := openBlockers(is, seenOpen); len(blockers) > 0 {
				q.heldBack = append(q.heldBack, heldBackInfo{number: is.Number, blockers: blockers})
			} else {
				q.ready = append(q.ready, is.Number)
			}
		}
	}
	slices.Sort(q.ready)
	slices.Sort(q.blocked)
	slices.Sort(q.parked)
	slices.Sort(q.proposed)
	slices.SortFunc(q.containers, func(a, b containerInfo) int { return a.number - b.number })
	slices.SortFunc(q.heldBack, func(a, b heldBackInfo) int { return a.number - b.number })
	return q, nil
}

// openBlockers returns, ascending, the blockedBy dependencies of is that this
// listing cannot show as closed — the set that keeps an otherwise-ready issue
// from being worked this pass. Two blockers that name each other resolve
// independently and in the same pass, so a dependency cycle costs each issue
// one skip rather than a hang: nothing here iterates on anything but is's own
// (short, GitHub-bounded) blocker list.
func openBlockers(is ghIssue, seenOpen map[int]bool) []int {
	var out []int
	for _, b := range is.BlockedBy.Nodes {
		if b.open(seenOpen) {
			out = append(out, b.Number)
		}
	}
	slices.Sort(out)
	return out
}

// ghIssue is one row of
// `gh issue list --json number,labels,subIssuesSummary,blockedBy`.
//
// SubIssues is absent from the payload of a gh too old to know the field, and
// absent on GitHub for an issue with no children — the same zero either way,
// which is the whole of what the old-gh degradation costs: containers read as
// ordinary work items, and a warning says so. BlockedBy degrades the same way
// — absent means no dependency this run can see, so it never holds up a
// listing that never asked for it.
type ghIssue struct {
	Number    int       `json:"number"`
	Labels    []ghLabel `json:"labels"`
	SubIssues struct {
		Total     int `json:"total"`
		Completed int `json:"completed"`
	} `json:"subIssuesSummary"`
	BlockedBy struct {
		Nodes []ghBlocker `json:"nodes"`
	} `json:"blockedBy"`
	// Parent is the epic this issue is a sub-issue of, populated only by
	// issueLabelPolicy's own `issue view --json labels,parent` (#364). nil when
	// the issue has no parent, or when the gh serving that view is too old to
	// report one — either way the issue's own labels are all there is. Only the
	// number is read; the epic's model:/effort: labels take a second view.
	Parent *struct {
		Number int `json:"number"`
	} `json:"parent"`
}

type ghLabel struct {
	Name string `json:"name"`
}

// ghBlocker is one issue named in another's blockedBy connection.
type ghBlocker struct {
	Number int `json:"number"`
	// State is GitHub's own "OPEN"/"CLOSED", matched case-insensitively since
	// nothing pins gh to one casing. Empty on a gh whose blockedBy support
	// does not carry it — open falls back to the listing itself then.
	State string `json:"state"`
}

// open reports whether b still blocks. A state this call actually named wins
// outright, and settles it correctly regardless of `-label`: a blocker this
// same call would never have listed on its own account — wrong label, or
// none at all — still blocks while it is open, because its blockedBy state
// travels with the connection rather than with the top-level filter. Without
// a state, seenOpen — every number this same open-issues listing found —
// stands in instead, and that guarantee narrows: a blocker outside -label's
// scope has no row of its own to be found by, so this path reads it as closed
// whether or not it truly is. The gap is the price of the no-second-request
// rule on a gh old enough to omit state in the first place.
func (b ghBlocker) open(seenOpen map[int]bool) bool {
	if b.State != "" {
		return !strings.EqualFold(b.State, "closed")
	}
	return seenOpen[b.Number]
}

// hasLabel matches case-insensitively, the way GitHub treats label names.
func (i ghIssue) hasLabel(name string) bool {
	return hasLabel(i.Labels, name)
}

// hasLabel is the label match itself, shared with plans.go's own
// gh-list rows — both read the same `labels` shape off a different
// --json field set, and a container's held state is decided the same way in
// both places.
func hasLabel(labels []ghLabel, name string) bool {
	return slices.ContainsFunc(labels, func(l ghLabel) bool {
		return strings.EqualFold(l.Name, name)
	})
}

// issueHasLabel asks GitHub whether one issue carries one label. Matched
// case-insensitively, the way GitHub itself treats label names.
func issueHasLabel(ctx context.Context, cfg config, issue int, name string) (bool, error) {
	out, err := retryRead(ctx, cfg, fmt.Sprintf("checking #%d's labels", issue), func() ([]byte, error) {
		return gh(ctx, cfg, "issue", "view", strconv.Itoa(issue), "--json", "labels")
	})
	if err != nil {
		return false, err
	}
	var v ghIssue
	if err := json.Unmarshal(out, &v); err != nil {
		return false, fmt.Errorf("parsing issue labels: %w", err)
	}
	return v.hasLabel(name), nil
}

// issueLabelPolicy reads the model: and effort: labels that decide one run's
// model and effort, resolved once at pickup. The issue's own labels come first;
// any family it leaves unset is filled from its parent epic's own labels
// (#364), marked epic-sourced. A read or parse that fails outright is not
// fatal: model and effort are a preference, not orchestration state, and a run
// on the default beats no run — it warns and falls through to the flags.
//
// Two gh calls at most. The first widens #363's `--json labels` to
// `--json labels,parent`; a gh that does not serve `parent` on `issue view`
// retries it with today's field set through unknownJSONField, inside the
// retryRead attempt like listOpenIssues so the rejection costs no retry
// allowance, and an epic's labels simply do not reach its children on that
// install. The second call — a plain `--json labels` on the parent — runs only
// when the issue settled fewer than both families itself and it has a parent.
func issueLabelPolicy(ctx context.Context, cfg config, issue int) labelChoice {
	out, err := retryRead(ctx, cfg, fmt.Sprintf("reading #%d's model/effort labels", issue), func() ([]byte, error) {
		out, err := gh(ctx, cfg, "issue", "view", strconv.Itoa(issue), "--json", "labels,parent")
		if unknownJSONField(err) {
			return gh(ctx, cfg, "issue", "view", strconv.Itoa(issue), "--json", "labels")
		}
		return out, err
	})
	if err != nil {
		narrate(sevWarning, "could not read #%d's labels (%v) — model and effort fall through to the flags", issue, err)
		return labelChoice{}
	}
	var v ghIssue
	if err := json.Unmarshal(out, &v); err != nil {
		narrate(sevWarning, "could not parse #%d's labels (%v) — model and effort fall through to the flags", issue, err)
		return labelChoice{}
	}
	child := labelPolicy(v.Labels)
	if child.complete() || v.Parent == nil {
		return child
	}

	parent := v.Parent.Number
	out, err = retryRead(ctx, cfg, fmt.Sprintf("reading epic #%d's model/effort labels", parent), func() ([]byte, error) {
		return gh(ctx, cfg, "issue", "view", strconv.Itoa(parent), "--json", "labels")
	})
	if err != nil {
		narrate(sevWarning, "could not read epic #%d's labels (%v) — #%d's unset model/effort fall through to the flags",
			parent, err, issue)
		return child
	}
	var pv ghIssue
	if err := json.Unmarshal(out, &pv); err != nil {
		narrate(sevWarning, "could not parse epic #%d's labels (%v) — #%d's unset model/effort fall through to the flags",
			parent, err, issue)
		return child
	}
	return child.inheritFrom(labelPolicy(pv.Labels))
}

// issueComment is the part of one thread comment a wait decides on: which
// comment it is, and whether a person or a machine wrote it.
type issueComment struct {
	ID   int64 `json:"id"`
	User struct {
		// Type is "Bot" for a GitHub App — Actions, Dependabot, a CI
		// reporter — and "User" for everyone else.
		Type string `json:"type"`
	} `json:"user"`
	// CreatedAt is read by `status` alone, to say how long a thread has been
	// quiet. No wait decides on it: comment ids only ever increase, and a
	// baseline made of them needs no clock and survives an edit.
	CreatedAt string `json:"created_at"`
	// Body is read by nobody but commentFinishedContainers, checking a
	// thread for the epic-finished marker before posting a second one. Every
	// other reader of this type only ever needed metadata — the text itself
	// is data, not something the rest of the drain acts on.
	Body string `json:"body"`
}

func (c issueComment) fromBot() bool { return c.User.Type == "Bot" }

// issueComments reads a thread oldest-first.
//
// Through `gh api` rather than `gh issue view --json comments`, which is the
// obvious call and the wrong one: its author payload is a login and nothing
// else. A GitHub App comes back from it as plain "dependabot" — no is_bot
// field, and no "[bot]" suffix on the login to key off either — so which
// comments are a machine's is simply not in that answer. REST puts a type on
// every author, which is the whole reason for the detour.
//
// The path's {owner}/{repo} are gh's own placeholders, filled in from whatever
// repository it resolved out of cfg.dir — which is how every call here reads on
// a drain. ghArgs substitutes them by hand when cfg.ghRepo names one instead,
// since `gh api` has no --repo to take.
func issueComments(ctx context.Context, cfg config, issue int) ([]issueComment, error) {
	path := fmt.Sprintf("repos/{owner}/{repo}/issues/%d/comments?per_page=100", issue)
	out, err := retryRead(ctx, cfg, fmt.Sprintf("reading #%d's comments", issue), func() ([]byte, error) {
		return gh(ctx, cfg, "api", path, "--paginate")
	})
	if err != nil {
		return nil, err
	}
	// Decoded page by page rather than in one Unmarshal: --paginate concatenates
	// what each request answered, and whether that arrives as one merged array
	// or several back-to-back is gh's business, not this function's.
	var all []issueComment
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var page []issueComment
		if err := dec.Decode(&page); errors.Is(err, io.EOF) {
			return all, nil
		} else if err != nil {
			return nil, fmt.Errorf("parsing #%d's comments: %w", issue, err)
		}
		all = append(all, page...)
	}
}

// commentBaseline marks where a thread stood when a question was flagged: the
// newest comment on it, which is the question itself. Comment ids only ever
// increase, so "newer than the baseline" needs no clock and survives an edit or
// a deletion further up the thread, neither of which an index or a count does.
func commentBaseline(comments []issueComment) int64 {
	if len(comments) == 0 {
		return 0
	}
	return comments[len(comments)-1].ID
}

// replyArrived reports whether somebody has answered since the baseline.
//
// Only a person ends a wait. CI, a linked-PR notice, a stale-bot nudge and a
// release announcement all land on a thread that is still exactly as blocked as
// it was, and each one used to cost a full Claude run to discover that.
//
// Comments from the account the drain itself runs as are deliberately *not*
// excluded. The drain writes three issue comments and none of them can end a
// live wait: the question is what the baseline is taken after, parking removes
// awaitingAnswerLabel on its way past, and "Shipped in #N" closes the issue.
// -post-summary comments on the PR, not here. Meanwhile the drain authenticates
// as whoever's `gh` credentials it was started with — usually the very person
// being asked — so filtering the account out would swallow the answer and wait
// forever. A wait that never ends is worse than a run that ends it early.
func replyArrived(comments []issueComment, baseline int64) bool {
	return slices.ContainsFunc(comments, func(c issueComment) bool {
		return c.ID > baseline && !c.fromBot()
	})
}

// queueMemo is the two things a shift finds out while listing its backlog that
// it only wants to act on, and say, once. Neither is durable and neither is
// read back after the process ends: one is a fact about this gh binary, the
// other is a line an operator needs at the top of a shift rather than once per
// issue. See config.queue.
type queueMemo struct {
	extendedFieldsOff atomic.Bool
	saidProposed      atomic.Bool
}

// seesExtendedFields reports whether the listing should still ask for the
// sub-issue rollup and the blockedBy dependency connection — true until a gh
// turns out not to have one of them. The two share one flag and one warning
// rather than each getting their own: a gh that rejects either is old enough
// to assume it lacks both, and probing them separately would only cost the
// shift a second retry to learn the same thing.
func (c config) seesExtendedFields() bool {
	return c.queue == nil || !c.queue.extendedFieldsOff.Load()
}

// dropExtendedFields gives up on the rollup and the dependency connection for
// the rest of the shift and says so. Nil-safe like seesExtendedFields, and
// losing the memo costs little: one rejected call per listing, and the
// warning repeated with it.
func (c config) dropExtendedFields() {
	if c.queue != nil && c.queue.extendedFieldsOff.Swap(true) {
		return
	}
	narrate(sevWarning, "gh too old to see sub-issues or blockedBy dependencies; "+
		"container issues will be treated as workable and blocked issues will be treated as ready — upgrade gh")
}

// sayProposals names what the curation gate is holding back, once a shift, so a
// forgotten batch of proposals surfaces on every startup instead of rotting
// silently. Said only when there are some, so a shift with none is as quiet as
// it was before the gate existed — which also means proposals filed mid-shift
// are still named the first time a listing sees them.
func (c config) sayProposals(n int) {
	if n == 0 {
		return
	}
	if c.queue != nil && c.queue.saidProposed.Swap(true) {
		return
	}
	narrate(sevWarning, "ignoring %d proposed issue(s) awaiting curation — remove the %s label to queue them",
		n, proposedLabel)
}
