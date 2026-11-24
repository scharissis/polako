package main

// `polako plan` on a doc with items reconciles before it proposes
// (docs/designs/reconcile.md): it prints the same table `status -design`
// does, asks the operator to confirm each guessed match, shows what it would
// change, and changes nothing until they say yes. Then the binary makes its
// share of the writes (reconcile_apply.go) and one plan-backlog run files the
// missing items from an items file, so a re-plan never files an item twice.
//
// A doc with no items plans exactly as before; this file is never reached.

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
)

// planDocHasItems reports whether plan should reconcile first: a -design doc
// with at least one item. A brief never has items. A doc whose items don't
// parse is an error here, before anything is read from GitHub: two items
// with one ID would make every marker for that ID ambiguous.
func planDocHasItems(cfg config, opt planOptions) (bool, error) {
	if opt.design == "" {
		return false, nil
	}
	content, err := os.ReadFile(filepath.Join(cfg.dir, filepath.FromSlash(opt.design)))
	if err != nil {
		return false, nil // planPreflight's stat says what's wrong with the path
	}
	doc, err := parseDocItems(filepath.ToSlash(filepath.Clean(opt.design)), string(content))
	if err != nil {
		return false, err
	}
	return len(doc.items) > 0, nil
}

// reconMilestones is -milestone as reconciliation reads it: "" for one
// GitHub milestone per milestone item (the default), "off" for none, or the
// batch title an operator typed.
func reconMilestones(opt planOptions) string {
	m := strings.TrimSpace(opt.milestone)
	if strings.EqualFold(m, milestonesOff) {
		return milestonesOff
	}
	return m
}

// planReconcile is plan for a doc with items.
func planReconcile(ctx context.Context, cfg config, opt planOptions, in io.Reader, isTTY bool, out io.Writer) error {
	if cfg.branchPrefix == "" {
		cfg.branchPrefix = "issue-"
	}
	rin, err := gatherRecon(ctx, cfg, opt.design)
	if err != nil {
		return err
	}
	res := reconcile(rin)
	renderRecon(out, report{}, cfg.repo, res)

	mode := reconMilestones(opt)
	guessed := guessedRows(res)
	if opt.dryRun {
		// A dry run asks nothing, so it shows what a run would change if the
		// operator confirmed every guess — the common answer, and the one
		// that shows the most.
		if len(guessed) > 0 {
			rin.confirmed = map[int][]string{}
			for _, r := range guessed {
				p, _ := r.primary()
				rin.confirmed[p.issue] = append(rin.confirmed[p.issue], strings.ToUpper(r.item.id))
			}
			res = reconcile(rin)
		}
		changes := planChanges(res, mode)
		if changes.empty() {
			fmt.Fprintln(out, "\nnothing to change: every item has its issue, marked and in place")
			return nil
		}
		cfg, opt = withItemsCap(cfg, opt, len(changes.creates))
		fmt.Fprintln(out)
		if len(guessed) > 0 {
			fmt.Fprintf(out, "a real run asks you to confirm the %d guessed matches first; if you confirmed them all, it would:\n", len(guessed))
		} else {
			fmt.Fprintln(out, "a real run asks first, then would:")
		}
		printChanges(out, changes.summary(res, opt.maxIssues))
		if len(changes.creates) > 0 {
			fmt.Fprintf(out, "\nthe plan-backlog run it would make, with %s written first:\n%s\n",
				itemsManifestPath, commandLine(cfg.claudeBin, buildArgs(cfg, planItemsPrompt(cfg, opt), "")))
		}
		return nil
	}

	if planChanges(res, mode).empty() && len(guessed) == 0 {
		fmt.Fprintln(out, "\nnothing to change: every item has its issue, marked and in place")
		return nil
	}
	if !isTTY && !opt.yes {
		return errors.New("plan has changes to make and stdin isn't a terminal — pass -yes to make them without asking " +
			"(guessed matches stay unconfirmed), run it where stdin is a terminal, or pass -dry-run to see them")
	}
	scan := bufio.NewScanner(in)
	if len(guessed) > 0 && !opt.yes {
		byNumber := make(map[int]reconIssue, len(rin.issues))
		for _, is := range rin.issues {
			byNumber[is.Number] = is
		}
		rin.confirmed, rin.rejected = askGuesses(out, scan, res, guessed, byNumber)
		res = reconcile(rin)
	}
	changes := planChanges(res, mode)
	cfg, opt = withItemsCap(cfg, opt, len(changes.creates))
	if changes.empty() {
		fmt.Fprintln(out)
		printChanges(out, changes.summary(res, opt.maxIssues))
		fmt.Fprintln(out, "nothing else to change")
		return nil
	}
	fmt.Fprintln(out)
	printChanges(out, changes.summary(res, opt.maxIssues))
	if !opt.yes {
		fmt.Fprint(out, "Make these changes? [y/N] ")
		if !scan.Scan() || !isYes(scan.Text()) {
			fmt.Fprintln(out, "nothing changed")
			return nil
		}
	}

	if len(changes.creates) > 0 {
		declareProposedLabel(ctx, cfg)
	}
	applied := applyChanges(ctx, cfg, res, changes)
	cfg.narrate(sevProgress, "plan: %s", appliedLine(applied))
	var runErr error
	if len(changes.creates) > 0 {
		runErr = planCreateItems(ctx, cfg, opt, res, changes, mode)
	}
	if len(applied.failures) > 0 {
		failed := fmt.Errorf("%s didn't take — rerun plan to finish; what did take is already marked:\n  %s",
			plural(len(applied.failures), "change"), strings.Join(applied.failures, "\n  "))
		return errors.Join(failed, runErr)
	}
	return runErr
}

// withItemsCap sets the cap the run runs under (planItemsCap) on both copies:
// cfg's is the kill in scanEvents, opt's the prompt, the log line and the
// record. Called after the last planChanges, since a rejected guess adds a
// create.
func withItemsCap(cfg config, opt planOptions, creates int) (config, planOptions) {
	opt.maxIssues = planItemsCap(creates, opt)
	cfg.maxIssues = opt.maxIssues
	return cfg, opt
}

// guessedRows is the rows matched only by a guess.
func guessedRows(res reconResult) []reconRow {
	var rows []reconRow
	for _, r := range res.rows {
		if r.guessed() {
			rows = append(rows, r)
		}
	}
	return rows
}

func printChanges(out io.Writer, lines []string) {
	for _, l := range lines {
		fmt.Fprintf(out, "  %s\n", l)
	}
}

func isYes(s string) bool {
	a := strings.ToLower(strings.TrimSpace(s))
	return a == "y" || a == "yes"
}

func appliedLine(a reconApplied) string {
	var parts []string
	for _, p := range []struct {
		n    int
		what string
	}{{a.milestones, "milestone"}, {a.marks, "marker"}, {a.moves, "move"}, {a.edits, "doc update"}, {a.comments, "comment"}} {
		if p.n > 0 {
			parts = append(parts, plural(p.n, p.what))
		}
	}
	if len(parts) == 0 {
		return "no existing issue needed a change"
	}
	return "made " + strings.Join(parts, ", ")
}

// askGuesses puts each guessed match to the operator. This is the one place
// polako prints issue text to the terminal: the operator can't confirm a
// match without seeing what the issue is called. Titles are cleaned of
// control characters and clipped first. The table's footnote has just said
// what a guess is, so the list says only what it adds.
//
// The answer: y confirms them all; n or nothing confirms none, which leaves
// those items alone this run; naming items says which are wrong — "M13"
// drops its guess, so plan files the item fresh, and "M13=#10" points it at
// #10 — and confirms the rest. An unclear answer is asked again; a closed
// stdin confirms nothing. The prompt spells that as ID and #N, never a
// sample ID: any real-looking one is some doc's real row, and reads as a
// hint that its guess is wrong.
func askGuesses(out io.Writer, scan *bufio.Scanner, res reconResult, guessed []reconRow, byNumber map[int]reconIssue) (confirmed map[int][]string, rejected map[string][]int) {
	fmt.Fprint(out, "\nThe guesses, with each issue's title:\n")
	for _, r := range guessed {
		p, _ := r.primary()
		fmt.Fprintf(out, "  %-5s #%-5d %s\n", r.item.id, p.issue, cleanTitle(byNumber[p.issue].Title))
	}
	for tries := 0; tries < 3; tries++ {
		fmt.Fprint(out, "Confirm them? [y = all, N = none, or name the wrong ones: ID drops its guess, ID=#N points it at #N] ")
		if !scan.Scan() {
			return nil, nil
		}
		ans, err := parseGuessAnswer(scan.Text(), res, guessed, byNumber)
		if err != nil {
			fmt.Fprintln(out, err)
			continue
		}
		return ans.confirmed, ans.rejected
	}
	fmt.Fprintln(out, "confirming none")
	return nil, nil
}

type guessAnswer struct {
	confirmed map[int][]string // issue → upper-case IDs it serves
	rejected  map[string][]int // upper-case ID → issues it isn't served by
}

// parseGuessAnswer reads the operator's answer to askGuesses.
func parseGuessAnswer(text string, res reconResult, guessed []reconRow, byNumber map[int]reconIssue) (guessAnswer, error) {
	ans := guessAnswer{confirmed: map[int][]string{}, rejected: map[string][]int{}}
	guessOf := map[string]int{}
	for _, r := range guessed {
		p, _ := r.primary()
		guessOf[strings.ToUpper(r.item.id)] = p.issue
	}
	isItem := map[string]bool{}
	for _, r := range res.rows {
		isItem[strings.ToUpper(r.item.id)] = true
	}
	confirm := func(id string, n int) { ans.confirmed[n] = append(ans.confirmed[n], id) }

	t := strings.ToLower(strings.TrimSpace(text))
	switch t {
	case "", "n", "no":
		return ans, nil
	case "y", "yes":
		for id, n := range guessOf {
			confirm(id, n)
		}
		return ans, nil
	}
	handled := map[string]bool{}
	for _, tok := range strings.FieldsFunc(text, func(r rune) bool { return r == ',' || unicode.IsSpace(r) }) {
		if isYes(tok) {
			continue
		}
		id, target, assign := strings.Cut(tok, "=")
		id = strings.ToUpper(strings.TrimSpace(id))
		if assign {
			n, err := strconv.Atoi(strings.TrimPrefix(strings.TrimSpace(target), "#"))
			if err != nil || !isItem[id] {
				return ans, fmt.Errorf("%q isn't ITEM=#ISSUE for an item of the doc — e.g. %s=#10", tok, firstID(guessed))
			}
			if _, ok := byNumber[n]; !ok {
				return ans, fmt.Errorf("#%d isn't an issue in this repository", n)
			}
			confirm(id, n)
			if g, ok := guessOf[id]; ok && g != n {
				ans.rejected[id] = append(ans.rejected[id], g)
			}
			handled[id] = true
			continue
		}
		g, ok := guessOf[id]
		if !ok {
			return ans, fmt.Errorf("%q isn't an item with a guess — name one of %s", tok, guessIDs(guessed))
		}
		ans.rejected[id] = append(ans.rejected[id], g)
		handled[id] = true
	}
	// Naming the wrong ones confirms the rest.
	for id, n := range guessOf {
		if !handled[id] {
			confirm(id, n)
		}
	}
	return ans, nil
}

func firstID(rows []reconRow) string {
	if len(rows) == 0 {
		return "M1"
	}
	return rows[0].item.id
}

func guessIDs(rows []reconRow) string {
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.item.id
	}
	return strings.Join(ids, ", ")
}

// cleanTitle readies an issue title for the terminal: no control
// characters, which could move the cursor or recolour what follows, one
// space between words, and clipped.
func cleanTitle(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	return clipRunes(strings.Join(strings.Fields(s), " "), 70)
}

// planCreateItems files the missing items: one plan-backlog run handed the
// items file, then a pass that checks every new issue carries its item's
// marker and puts it in that item's milestone. The file is scratch, gone once
// the run is.
func planCreateItems(ctx context.Context, cfg config, opt planOptions, res reconResult, changes reconChanges, mode string) error {
	path := filepath.Join(cfg.dir, filepath.FromSlash(itemsManifestPath))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("could not write the items file: %w", err)
	}
	if err := os.WriteFile(path, []byte(itemsManifest(res, changes.creates)), 0o600); err != nil {
		return fmt.Errorf("could not write the items file: %w", err)
	}
	defer os.Remove(path)

	titles := map[string]string{}
	if mode == "" {
		for _, r := range changes.creates {
			if r.item.kind == itemMilestone {
				titles[strings.ToUpper(r.item.id)] = itemMilestoneTitle(res.doc.slug, r.item)
			}
		}
	}
	batch := changes.batch
	spec := intakeRunSpec{
		verb:           "plan",
		announceTarget: fmt.Sprintf("from %s for %s", opt.design, plural(len(changes.creates), "missing item")),
		prompt:         planItemsPrompt(cfg, opt),
		record: func(cfg config, rep runReport, pf proposalFacts, used string) {
			cfg.rec.recordPlan(cfg, rep, planFacts{proposalFacts: pf, design: planDesignField(opt), milestone: used})
		},
		afterPass: func(ctx context.Context, cfg config, pass labelPassOutcome) []string {
			return placeNewIssues(ctx, cfg, pass.numbers, res, changes.creates, titles, batch)
		},
	}
	return intakeRun(ctx, cfg, opt.intakeOptions, spec)
}
