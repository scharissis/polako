package main

// `polako unpark` is the attended morning-after step for a permission park:
// today, clearing one is two steps in two places, by hand — edit the launch
// line, then remove the label. This lists every parked issue with the reason
// and the -add-tools entries its own park comment named (see parkIssue,
// drain.go, and docs/plans/permission-parks.md ticket 3), asks before
// clearing each one, then prints the exact `polako work … -add-tools "…"`
// line to rerun with. It never grants anything, starts a drain, or stores
// anything — the label, read back and (with -apply) removed, is the whole of
// its write surface.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

type unparkOptions struct {
	dir          string
	repo         string
	apply        bool
	yes          bool
	branchPrefix string
}

// runUnpark is the `unpark` subcommand: parse its own flags, list the parked
// issues, and — with -apply — clear whichever ones the operator approves.
// isTTY is whether in is a terminal, the same seam runSetup takes: main
// passes isTerminal(os.Stdin), tests pass it directly.
func runUnpark(ctx context.Context, args []string, in io.Reader, isTTY bool, out io.Writer, rpt report) error {
	fs := flag.NewFlagSet("unpark", flag.ContinueOnError)
	fs.SetOutput(out)
	var opt unparkOptions
	fs.StringVar(&opt.dir, "dir", ".", "path to the repository's main checkout, when -repo is not given")
	fs.StringVar(&opt.repo, "repo", "",
		"repository to clear parks in (owner/name), instead of whichever -dir is a checkout of")
	fs.BoolVar(&opt.apply, "apply", false,
		"remove needs-human from the issues you approve, asking [y/N] first — needs a terminal, or -yes")
	fs.BoolVar(&opt.yes, "yes", false,
		"with -apply, clear every listed issue without asking — required when stdin isn't a terminal")
	fs.StringVar(&opt.branchPrefix, "branch-prefix", "issue-", "branch name prefix the skill uses")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "Usage: polako unpark [flags] [issue]\n\n"+
			"Lists every issue labelled needs-human, with the reason and any -add-tools\n"+
			"entries its own park comment named. -apply asks before clearing each one,\n"+
			"then prints the polako work line to rerun with. Naming an issue number\n"+
			"limits this to that one issue.\n\n"+envUsage+"\nFlags:\n")
		fs.PrintDefaults()
	}
	if err := applyEnvDefaults(fs); err != nil {
		return err
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return errFlagsReported
	}
	only, err := unparkIssueArg(fs.Args())
	if err != nil {
		return err
	}
	// Unattended means no prompts: the same refusal setup -apply's own
	// setupApplyNeedsYes makes, mirrored rather than shared across a file
	// boundary for the one caller each side has.
	if opt.apply && !opt.yes && !isTTY {
		fmt.Fprintln(out, "-apply is reading stdin that is not a terminal — pass -yes to clear every "+
			"listed issue without asking, or run this where stdin is a terminal")
		return errFlagsReported
	}

	cfg, err := unparkConfig(ctx, opt)
	if err != nil {
		return err
	}
	items, err := readParkedIssues(ctx, cfg, only)
	if err != nil {
		return err
	}
	renderUnpark(out, rpt, cfg, items, only != 0)
	if !opt.apply {
		printUnparkNextStep(out, items, only != 0)
	}
	if opt.apply {
		// -yes means "clear everything, don't ask" here — not setup's own
		// "take each step's own default", since every question below shares
		// one default (no) and a script passing -yes wants the opposite of
		// it. So the prompt itself is built with yes=false (confirmDefault's
		// own short-circuit would otherwise hand back that same no) and
		// opt.yes is applied as a bypass in applyUnpark instead; stdin is
		// never read once it's set, matching the no-terminal refusal above.
		prompt := newSetupPrompt(in, out, false)
		union := applyUnpark(ctx, prompt, opt.yes, out, cfg, items)
		printUnparkRerunLine(out, cfg, union)
	}
	return nil
}

// unparkIssueArg reads the one optional positional argument: an issue number
// to limit the listing (and, with -apply, the clearing) to.
func unparkIssueArg(rest []string) (int, error) {
	if len(rest) == 0 {
		return 0, nil
	}
	if len(rest) > 1 {
		return 0, fmt.Errorf("unexpected argument %q — unpark takes flags and at most one issue number", rest[1])
	}
	n, err := strconv.Atoi(rest[0])
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%q is not an issue number", rest[0])
	}
	return n, nil
}

// unparkConfig resolves which repository is being cleared in, the same way
// tidyConfig and statusConfig do: -repo names it outright, or gh resolves it
// from -dir.
func unparkConfig(ctx context.Context, opt unparkOptions) (config, error) {
	cfg := config{
		ghBin:        "gh",
		ghRetryWait:  ghRetryDelay,
		branchPrefix: opt.branchPrefix,
	}
	if _, err := exec.LookPath(cfg.ghBin); err != nil {
		return cfg, fmt.Errorf("%q not found on PATH (%w) — unpark reads and writes GitHub through it", cfg.ghBin, err)
	}
	abs, err := filepath.Abs(opt.dir)
	if err != nil {
		return cfg, fmt.Errorf("resolving -dir: %w", err)
	}
	cfg.dir = abs

	repo, err := parseRepoFlag(opt.repo)
	if err != nil {
		return cfg, err
	}
	if repo != "" {
		cfg.repo, cfg.ghRepo = repo, repo
		return cfg, nil
	}
	out, err := gh(ctx, cfg, "repo", "view", "--json", "nameWithOwner", "--jq", ".nameWithOwner")
	if err != nil {
		return cfg, fmt.Errorf("no GitHub repository reachable from %s (is gh authenticated?): %w — "+
			"or name one with -repo owner/name", cfg.dir, err)
	}
	cfg.repo = strings.TrimSpace(string(out))
	cfg.ghRepo = cfg.repo
	return cfg, nil
}

// --- reading ---

// parkListItem is one open needs-human issue, as unpark reports it: the
// reason flattened to one line — the table clips it, the one-issue view
// doesn't — its entries split into ones a rerun could actually use and ones
// ignored because they don't match anything parkIssue itself ever writes.
// category is the park's own identifier (metrics.go), "" when the comment
// carries none parseParkCategory recognizes.
type parkListItem struct {
	issue    int
	reason   string
	entries  []string
	ignored  []string
	category string
	// work is read separately from the rest of this struct — it needs
	// gh calls readParkListItem doesn't make, and readParkListItems is
	// shared with status.go's own needs-you line, which has no use for it.
	// See readParkWork.
	work parkWork
}

// unparkReasonWidth is where the table clips a reason. The fallback permission
// reason (permissionParkReason, refusals.go) runs to several times this, which
// is why naming one issue prints it whole instead.
const unparkReasonWidth = 100

// readParkedIssues lists every open needs-human issue — the same queue.parked
// the drain itself excludes, containers excluded structurally — and reads
// each one's latest park comment. only, when nonzero, narrows this to a
// single issue.
func readParkedIssues(ctx context.Context, cfg config, only int) ([]parkListItem, error) {
	// openQueues names the proposals the curation gate is holding, for a
	// shift's sake. They aren't this verb's subject, so the line is marked
	// said before it can be.
	if cfg.queue == nil {
		cfg.queue = new(queueMemo)
	}
	cfg.queue.saidProposed.Store(true)
	q, err := openQueues(ctx, cfg)
	if err != nil {
		return nil, err
	}
	parked := q.parked
	if only != 0 {
		if !slices.Contains(parked, only) {
			// Not just "isn't labelled needs-human" — an open, needs-human
			// issue with sub-issues is a container (selectableIssues,
			// backlog.go), structurally excluded from q.parked whatever its
			// labels, so that overclaims the reason as often as it states it.
			return nil, fmt.Errorf("#%d isn't a parked issue unpark can act on — check it's open, "+
				"labelled %s, and not a container", only, needsHumanLabel)
		}
		parked = []int{only}
	}
	byIssue, err := readParkListItems(ctx, cfg, parked)
	if err != nil {
		return nil, err
	}
	items := make([]parkListItem, len(parked))
	for i, issue := range parked {
		items[i] = byIssue[issue]
	}
	if len(items) > 0 {
		// Resolved once for the whole listing, not per issue — only the
		// no-PR/compare path below needs it, and a failure here still lets
		// every issue with an open PR render normally.
		def, _ := unparkDefaultBranch(ctx, cfg)
		for i := range items {
			items[i].work = readParkWork(ctx, cfg, items[i].issue, def)
		}
	}
	return items, nil
}

// readParkListItems reads every named issue's latest park comment, keyed by
// issue number — the one gh-viewer-login read and the per-issue comment
// reads readParkedIssues itself needs, factored out so status's own
// needs-you line (status.go) can reuse the same viewer-only read and
// strict-shape filter without re-deriving the parked queue a second time.
// Empty for no issues, so a caller with nothing parked pays for no gh call
// at all.
func readParkListItems(ctx context.Context, cfg config, issues []int) (map[int]parkListItem, error) {
	if len(issues) == 0 {
		return nil, nil
	}
	viewer, err := ghViewerLogin(ctx, cfg)
	if err != nil {
		return nil, err
	}
	items := make(map[int]parkListItem, len(issues))
	for _, issue := range issues {
		items[issue] = readParkListItem(ctx, cfg, issue, viewer)
	}
	return items, nil
}

// ghViewerLogin reads the login the gh CLI is authenticated as, so
// readParkListItem can tell polako's own park comment apart from anything
// else on the thread — a forged footer in a comment by another author must
// contribute nothing.
func ghViewerLogin(ctx context.Context, cfg config) (string, error) {
	out, err := gh(ctx, cfg, "api", "user", "--jq", ".login")
	if err != nil {
		return "", fmt.Errorf("could not read the authenticated gh account (%w) — is gh logged in?", err)
	}
	login := strings.TrimSpace(string(out))
	if login == "" {
		return "", fmt.Errorf("gh reported no authenticated account — is gh logged in?")
	}
	return login, nil
}

// readParkListItem reads one issue's thread and finds the newest comment
// that is both authored by viewer and shaped like parkIssue's own — walking
// newest-first, since a re-park after a reply appends a second one. Best
// effort: a thread that fails to read still gets a row, with a reason that
// says so, rather than dropping the issue from the listing entirely.
func readParkListItem(ctx context.Context, cfg config, issue int, viewer string) parkListItem {
	item := parkListItem{issue: issue}
	comments, err := issueComments(ctx, cfg, issue)
	if err != nil {
		item.reason = "could not read its comments"
		return item
	}
	for i := len(comments) - 1; i >= 0; i-- {
		c := comments[i]
		if c.User.Login != viewer || !strings.HasPrefix(c.Body, parkCommentPrefix) {
			continue
		}
		item.reason = strings.Join(strings.Fields(parkCommentReason(c.Body)), " ")
		item.category = parseParkCategory(c.Body)
		if entries, ok := parseParkFooter(c.Body); ok {
			for _, e := range entries {
				if validParkEntry(e) {
					item.entries = append(item.entries, e)
				} else {
					item.ignored = append(item.ignored, e)
				}
			}
		}
		return item
	}
	item.reason = "labelled by hand, or by another account — no park comment of polako's own to read; see the thread"
	return item
}

// parkCommentReason pulls the reason clause back out of a park comment's
// body: the text between parkCommentPrefix and the blank line parkIssue's
// own template always follows it with.
func parkCommentReason(body string) string {
	rest := strings.TrimPrefix(body, parkCommentPrefix)
	if i := strings.Index(rest, "\n\n"); i >= 0 {
		rest = rest[:i]
	}
	return rest
}

// --- reading work: branch, PR and CI ---

// parkWork is what unpark shows about a parked issue's branch, PR and CI —
// the first thing a human checks about a park: is there already a PR, is it
// green, or is there nothing pushed at all. read is false when the chain of
// gh calls below could not finish — the row still renders, as "not read",
// the same best-effort rule readParkListItem follows for reason.
//
// GitHub only, on purpose: local, unpushed work is a separate concern (a
// later child of #529) that would need the worktree on this machine, which
// a read-only listing has no business assuming exists.
type parkWork struct {
	read     bool
	branch   string
	prNumber int
	prURL    string
	checks   string   // one of the checks* verdicts (pr.go), "" when unread
	failing  []string // the checks that earned checksFailing
	onOrigin bool     // only meaningful when prNumber == 0
	ahead    int      // commits ahead of the default branch; only meaningful when onOrigin
}

// readParkWork reads one parked issue's branch: prForBranch first, and with
// a PR, its checks. With no PR, def (the repository's default branch, read
// once per listing by readParkedIssues) decides whether origin has the
// branch at all and how far ahead of def it sits.
func readParkWork(ctx context.Context, cfg config, issue int, def string) parkWork {
	branch := fmt.Sprintf("%s%d", cfg.branchPrefix, issue)
	w := parkWork{branch: branch}
	pr, err := prForBranch(ctx, cfg, branch)
	if err != nil {
		return w
	}
	if pr != nil {
		w.read, w.prNumber, w.prURL = true, pr.Number, pr.URL
		if view, verr := prStatus(ctx, cfg, pr.Number); verr == nil {
			w.checks, w.failing = view.checks, view.failing
		}
		return w
	}
	if def == "" {
		// The default-branch read already failed for the whole listing —
		// nothing left to compare against.
		return w
	}
	ahead, onOrigin, err := branchAheadOfDefault(ctx, cfg, def, branch)
	if err != nil {
		return w
	}
	w.read, w.onOrigin, w.ahead = true, onOrigin, ahead
	return w
}

// branchAheadOfDefault reads how far branch is ahead of def on origin. The
// gh CLI has no subcommand for a compare, only `gh api`; a 404 answers "no
// branch on origin" definitively, the same shape labelExists (labels.go)
// turns its own 404 into (false, nil) before retryRead ever sees it as
// something to retry.
func branchAheadOfDefault(ctx context.Context, cfg config, def, branch string) (ahead int, onOrigin bool, err error) {
	type compareResult struct {
		ahead    int
		onOrigin bool
	}
	r, err := retryRead(ctx, cfg, "comparing "+branch+" to "+def, func() (compareResult, error) {
		out, err := gh(ctx, cfg, "api",
			fmt.Sprintf("repos/{owner}/{repo}/compare/%s...%s", def, branch), "--jq", ".ahead_by")
		if err == nil {
			n, perr := strconv.Atoi(strings.TrimSpace(string(out)))
			if perr != nil {
				return compareResult{}, perr
			}
			return compareResult{ahead: n, onOrigin: true}, nil
		}
		if isNotFoundError(err) {
			return compareResult{}, nil
		}
		return compareResult{}, err
	})
	return r.ahead, r.onOrigin, err
}

// unparkDefaultBranch reads the repository's default branch name — the base
// half of the compare readParkWork uses for a pushed branch with no PR yet.
// Best-effort like ghViewerLogin: a failure here is reported by returning it,
// and readParkedIssues treats it the same way it treats any other read that
// could not finish — the listing still runs, minus the one thing it enabled.
func unparkDefaultBranch(ctx context.Context, cfg config) (string, error) {
	return retryRead(ctx, cfg, "reading the repository's default branch", func() (string, error) {
		out, err := gh(ctx, cfg, "api", "repos/{owner}/{repo}", "--jq", ".default_branch")
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(out)), nil
	})
}

// validParkEntryRe matches the two shapes addToolsEntry (refusals.go) ever
// produces: a Bash command wrapped Bash(...:*) — a single plain word, or,
// only for `gh` (the one case addToolsEntry takes three words instead of
// one), `gh` plus one or two more — or a bare tool name. Anything else in a
// footer didn't come from parkIssue's own write — a hand-edited comment, or
// one smuggled in by a forged author that ghViewerLogin's own check didn't
// already exclude — so it renders "ignored" instead of joining the rerun
// line.
// The bare-tool-name branch allows an underscore — addToolsEntry (refusals.go)
// sets that entry to r.tool verbatim, and an MCP tool's own name is commonly
// "mcp__server__tool".
var validParkEntryRe = regexp.MustCompile(`^(?:Bash\((gh(?: [\w.\-]+){1,2}|[\w.\-]+):\*\)|([A-Za-z][A-Za-z0-9_]*))$`)

// validParkEntry reports whether entry is safe to offer on the rerun line:
// shaped like something addToolsEntry could have produced, and not a
// never-grant command (hasNeverGrantPrefix, refusals.go) that entry's own
// producer would already have refused.
func validParkEntry(entry string) bool {
	m := validParkEntryRe.FindStringSubmatch(entry)
	if m == nil {
		return false
	}
	if m[1] != "" {
		return !hasNeverGrantPrefix(m[1])
	}
	return true
}

// --- applying ---

// applyUnpark asks about each listed issue in turn — "remove needs-human
// from #N? [y/N]", default no — and on yes, removes the label, its only
// write. autoApprove is -yes: every question approves without touching
// stdin at all, rather than taking that no default. It returns the union of
// the approved issues' own entries, deduped in first-seen order, for the
// rerun line.
func applyUnpark(ctx context.Context, prompt *setupPrompt, autoApprove bool, out io.Writer, cfg config, items []parkListItem) []string {
	seen := make(map[string]bool)
	var union []string
	for _, it := range items {
		approved := autoApprove
		if !autoApprove {
			approved = prompt.confirmDefault(fmt.Sprintf("remove %s from #%d?", needsHumanLabel, it.issue), false)
		}
		if !approved {
			continue
		}
		if _, err := gh(ctx, cfg, "issue", "edit", strconv.Itoa(it.issue), "--remove-label", needsHumanLabel); err != nil {
			fmt.Fprintf(out, "  could not remove %s from #%d: %v\n", needsHumanLabel, it.issue, err)
			continue
		}
		for _, e := range it.entries {
			if !seen[e] {
				seen[e] = true
				union = append(union, e)
			}
		}
	}
	return union
}

// printUnparkRerunLine prints the rerun line, once anything is actually
// approved — nothing when union is empty, the same "empty bucket is noise"
// rule parkGrantsBlock (drain.go) follows.
func printUnparkRerunLine(w io.Writer, cfg config, union []string) {
	if len(union) == 0 {
		return
	}
	value := strings.Join(union, ",")
	fmt.Fprintf(w, "\npolako work -dir %s -add-tools %q\nPOLAKO_ADD_TOOLS=%s\n", cfg.dir, value, value)
}

// --- rendering ---

// renderUnpark prints the listing. single is an issue named on the command
// line: that one prints as a block with its reason whole, since the table's
// clipped line is the only place the reason otherwise shows.
func renderUnpark(w io.Writer, rpt report, cfg config, items []parkListItem, single bool) {
	header := cfg.repo
	if header == "" {
		header = cfg.dir
	}
	fmt.Fprintf(w, "%s\n", rpt.bold(header))
	if len(items) == 0 {
		fmt.Fprintf(w, "nothing parked — no open issue is labelled %s\n", needsHumanLabel)
		return
	}
	if single {
		it := items[0]
		fmt.Fprintf(w, "\n%s\n", rpt.bold("#"+strconv.Itoa(it.issue)))
		if cfg.repo != "" {
			fmt.Fprintf(w, "  %s  https://github.com/%s/issues/%d\n", rpt.dim("thread   "), cfg.repo, it.issue)
		}
		renderParkWorkDetail(w, rpt, it.work)
		fmt.Fprintf(w, "  %s  %s\n", rpt.dim("reason   "), it.reason)
		fmt.Fprintf(w, "  %s  %s\n", rpt.dim("add-tools"), renderParkEntries(it))
		return
	}
	rows := make([][]string, len(items))
	for i, it := range items {
		rows[i] = []string{"#" + strconv.Itoa(it.issue), parkWorkSummary(it.work),
			clip(it.reason, unparkReasonWidth), renderParkEntries(it)}
	}
	printTable(w, rpt, "parked", []string{"issue", "work", "reason", "add-tools"}, rows, 4)
}

// printUnparkNextStep closes a listing that changed nothing with what to run
// next — without it, a bare `polako unpark` reads as the whole of the verb.
func printUnparkNextStep(w io.Writer, items []parkListItem, single bool) {
	if len(items) == 0 {
		return
	}
	fmt.Fprintln(w)
	if single {
		fmt.Fprintf(w, "fix what it names, then: polako unpark -apply %d\n", items[0].issue)
		return
	}
	fmt.Fprintln(w, "polako unpark <issue> prints one reason in full; polako unpark -apply asks before clearing each")
}

func renderParkEntries(it parkListItem) string {
	if len(it.entries) == 0 && len(it.ignored) == 0 {
		return "(none named)"
	}
	parts := append([]string{}, it.entries...)
	for _, e := range it.ignored {
		parts = append(parts, e+" (ignored)")
	}
	return strings.Join(parts, ", ")
}

// parkWorkSummary is the table's clipped work cell: whether there's a PR and
// whether its CI is red, or, with no PR, whether the branch reached origin
// at all and how far ahead it is.
func parkWorkSummary(w parkWork) string {
	switch {
	case !w.read:
		return "not read"
	case w.prNumber != 0:
		if w.checks == checksFailing {
			return fmt.Sprintf("PR #%d, CI red", w.prNumber)
		}
		return fmt.Sprintf("PR #%d", w.prNumber)
	case w.onOrigin:
		return fmt.Sprintf("%s, %s, no PR", w.branch, plural(w.ahead, "commit"))
	default:
		return "nothing pushed"
	}
}

// renderParkWorkDetail is the one-issue view's expansion of parkWorkSummary:
// the PR link and any failing check names, or the branch and its commit
// count when there's no PR yet.
func renderParkWorkDetail(w io.Writer, rpt report, work parkWork) {
	switch {
	case !work.read:
		fmt.Fprintf(w, "  %s  not read\n", rpt.dim("work     "))
	case work.prNumber != 0:
		fmt.Fprintf(w, "  %s  %s\n", rpt.dim("pr       "), work.prURL)
		if len(work.failing) > 0 {
			fmt.Fprintf(w, "  %s  %s\n", rpt.dim("checks   "), strings.Join(work.failing, ", "))
		}
	case work.onOrigin:
		fmt.Fprintf(w, "  %s  %s, %s, no PR\n", rpt.dim("branch   "), work.branch, plural(work.ahead, "commit"))
	default:
		fmt.Fprintf(w, "  %s  nothing pushed\n", rpt.dim("branch   "))
	}
}
