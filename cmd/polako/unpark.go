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
	"strconv"
	"strings"
)

type unparkOptions struct {
	dir   string
	repo  string
	apply bool
	yes   bool
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
		"with -apply, take the default answer (no) for every question without asking — required when stdin isn't a terminal")
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
		fmt.Fprintln(out, "-apply is reading stdin that is not a terminal — pass -yes to take the "+
			"default (no) for every question without asking, or run this where stdin is a terminal")
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
	renderUnpark(out, rpt, cfg, items)
	if opt.apply {
		prompt := newSetupPrompt(in, out, opt.yes)
		union := applyUnpark(ctx, prompt, out, cfg, items)
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
		ghBin:       "gh",
		ghRetryWait: ghRetryDelay,
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
// reason clipped to a line, its entries split into ones a rerun could
// actually use and ones ignored because they don't match anything parkIssue
// itself ever writes.
type parkListItem struct {
	issue   int
	reason  string
	entries []string
	ignored []string
}

// readParkedIssues lists every open needs-human issue — the same queue.parked
// the drain itself excludes, containers excluded structurally — and reads
// each one's latest park comment. only, when nonzero, narrows this to a
// single issue.
func readParkedIssues(ctx context.Context, cfg config, only int) ([]parkListItem, error) {
	q, err := openQueues(ctx, cfg)
	if err != nil {
		return nil, err
	}
	parked := q.parked
	if only != 0 {
		if !containsInt(parked, only) {
			return nil, fmt.Errorf("#%d is not open and labelled %s — nothing to unpark", only, needsHumanLabel)
		}
		parked = []int{only}
	}
	viewer, err := ghViewerLogin(ctx, cfg)
	if err != nil {
		return nil, err
	}
	items := make([]parkListItem, len(parked))
	for i, issue := range parked {
		items[i] = readParkListItem(ctx, cfg, issue, viewer)
	}
	return items, nil
}

func containsInt(xs []int, x int) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
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
		item.reason = clip(parkCommentReason(c.Body), 100)
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
	item.reason = "no park comment on the thread from this account"
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

// validParkEntryRe matches the two shapes addToolsEntry (refusals.go) ever
// produces: a Bash command of one to three plain words wrapped Bash(...:*),
// or a bare tool name. Anything else in a footer didn't come from parkIssue's
// own write — a hand-edited comment, or one smuggled in by a forged author
// that ghViewerLogin's own check didn't already exclude — so it renders
// "ignored" instead of joining the rerun line.
var validParkEntryRe = regexp.MustCompile(`^(?:Bash\(([\w.\-]+(?: [\w.\-]+){0,2}):\*\)|([A-Za-z][A-Za-z0-9]*))$`)

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
// write. It returns the union of the approved issues' own entries, deduped
// in first-seen order, for the rerun line.
func applyUnpark(ctx context.Context, prompt *setupPrompt, out io.Writer, cfg config, items []parkListItem) []string {
	seen := make(map[string]bool)
	var union []string
	for _, it := range items {
		if !prompt.confirmDefault(fmt.Sprintf("remove %s from #%d?", needsHumanLabel, it.issue), false) {
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

func renderUnpark(w io.Writer, rpt report, cfg config, items []parkListItem) {
	header := cfg.repo
	if header == "" {
		header = cfg.dir
	}
	fmt.Fprintf(w, "%s\n", rpt.bold(header))
	if len(items) == 0 {
		fmt.Fprintf(w, "nothing parked — no open issue is labelled %s\n", needsHumanLabel)
		return
	}
	rows := make([][]string, len(items))
	for i, it := range items {
		rows[i] = []string{"#" + strconv.Itoa(it.issue), it.reason, renderParkEntries(it)}
	}
	printTable(w, rpt, "parked", []string{"issue", "reason", "entries"}, rows, 3)
}

func renderParkEntries(it parkListItem) string {
	if len(it.entries) == 0 && len(it.ignored) == 0 {
		return "(none)"
	}
	parts := append([]string{}, it.entries...)
	for _, e := range it.ignored {
		parts = append(parts, e+" (ignored)")
	}
	return strings.Join(parts, ", ")
}
