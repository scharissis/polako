package main

// The ran-on block: a marked block at the end of a PR body naming each
// provider · model · effort that worked on the PR (issue #614). The skill
// can't write it — no event reports effort, and the provider is a CLI setting
// — so the binary does, after the run that opens the PR and after each
// remediation that pushes to it. Identifiers only: no cost, no tokens, no
// account; those stay behind -post-summary.

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

const (
	ranOnBegin  = "<!-- polako:ran-on -->"
	ranOnEnd    = "<!-- /polako:ran-on -->"
	ranOnPrefix = "Ran on "
)

// prRanOn is one line of the block: a provider · model · effort and the run
// reasons that used it, first seen first. raw is a line between the markers
// this couldn't parse — a hand edit, say — kept verbatim and never merged.
type prRanOn struct {
	combo   string
	reasons []string
	raw     string
}

func (l prRanOn) String() string {
	if l.raw != "" {
		return l.raw
	}
	return fmt.Sprintf("%s%s (%s)", ranOnPrefix, l.combo, strings.Join(l.reasons, ", "))
}

// prCombo names what one run ran on. Model is what the run's init reported,
// with what polako asked for beside it; effort is only ever what was asked
// for, since no event reports the effort that ran — the same rule stats'
// `ran on` line follows.
func prCombo(provider, model string, choice runChoice) string {
	if choice.model != "" {
		model += " (asked for " + choice.model + ")"
	}
	return fmt.Sprintf("%s · %s · effort %s", cmp.Or(provider, "unknown"), model, cmp.Or(choice.effort, "inherited"))
}

// noteRanOn adds one finished run to the issue's lines. A run whose init never
// reported a model never got as far as working on anything, so it names
// nothing.
func noteRanOn(st *issueState, provider, reason string, choice runChoice, rep runReport) {
	if rep.model == "" {
		return
	}
	st.ranOn = mergeRanOn(st.ranOn, prRanOn{combo: prCombo(provider, rep.model, choice), reasons: []string{reason}})
}

// mergeRanOn folds add into lines: a combo already there gains add's new
// reasons, a new one goes last.
func mergeRanOn(lines []prRanOn, add prRanOn) []prRanOn {
	if add.raw != "" {
		return append(lines, add)
	}
	i := slices.IndexFunc(lines, func(l prRanOn) bool { return l.raw == "" && l.combo == add.combo })
	if i < 0 {
		return append(lines, prRanOn{combo: add.combo, reasons: slices.Clone(add.reasons)})
	}
	for _, r := range add.reasons {
		if !slices.Contains(lines[i].reasons, r) {
			lines[i].reasons = append(lines[i].reasons, r)
		}
	}
	return lines
}

// parseRanOn reads a line this file wrote back into its combo and reasons.
func parseRanOn(line string) (prRanOn, bool) {
	rest, ok := strings.CutPrefix(line, ranOnPrefix)
	if !ok || !strings.HasSuffix(rest, ")") {
		return prRanOn{}, false
	}
	i := strings.LastIndex(rest, " (")
	if i <= 0 {
		return prRanOn{}, false
	}
	reasons := strings.Split(strings.TrimSuffix(rest[i+2:], ")"), ", ")
	return prRanOn{combo: rest[:i], reasons: reasons}, true
}

// spliceRanOn returns body with ours merged into its block. Lines already in
// the block stay, so a restarted supervisor or a later remediation adds to it
// rather than replacing it. Everything outside the markers is left byte for
// byte. With no block yet, one is appended at the very end — after the
// skill's own `Closes #N`. A marker counts only on a line of its own, and the
// last complete block wins, so a PR body that quotes the markers in prose or
// code — this feature's own PRs do — is just text.
func spliceRanOn(body string, ours []prRanOn) string {
	begin, end := ranOnMarkers(body)
	var lines []prRanOn
	if begin >= 0 {
		for _, l := range strings.Split(body[begin+len(ranOnBegin):end], "\n") {
			l = strings.TrimRight(l, "\r")
			if strings.TrimSpace(l) == "" {
				continue
			}
			p, ok := parseRanOn(l)
			if !ok {
				p = prRanOn{raw: l}
			}
			lines = mergeRanOn(lines, p)
		}
	}
	for _, l := range ours {
		lines = mergeRanOn(lines, l)
	}
	// A blank line between entries, or markdown joins them into one paragraph.
	rendered := make([]string, 0, len(lines))
	for _, l := range lines {
		rendered = append(rendered, l.String())
	}
	block := ranOnBegin + "\n" + strings.Join(rendered, "\n\n") + "\n" + ranOnEnd
	if begin >= 0 {
		return body[:begin] + block + body[end+len(ranOnEnd):]
	}
	switch {
	case body == "" || strings.HasSuffix(body, "\n\n"):
	case strings.HasSuffix(body, "\n"):
		block = "\n" + block
	default:
		block = "\n\n" + block
	}
	return body + block + "\n"
}

// ranOnMarkers finds the last begin line followed by an end line, returning
// the offset where each marker starts; (-1, -1) when there is no such pair.
func ranOnMarkers(body string) (begin, end int) {
	begin, end = -1, -1
	cand := -1
	for off := 0; off < len(body); {
		line, _, _ := strings.Cut(body[off:], "\n")
		switch strings.TrimSpace(line) {
		case ranOnBegin:
			cand = off + strings.Index(line, ranOnBegin)
		case ranOnEnd:
			if cand >= 0 {
				begin, end = cand, off+strings.Index(line, ranOnEnd)
				cand = -1
			}
		}
		off += len(line) + 1
	}
	return begin, end
}

// writeRanOn puts the issue's lines on its PR. Best-effort like postSummary:
// a failed read or write is one log line, never a park, a retry or a stopped
// shift. The body goes to gh on stdin, never argv — a failed call's error
// quotes its argv, and nothing from the body may reach the shift log.
func writeRanOn(ctx context.Context, cfg config, prNumber int, st *issueState) {
	if cfg.dryRun || prNumber == 0 || len(st.ranOn) == 0 {
		return
	}
	n := strconv.Itoa(prNumber)
	out, err := gh(ctx, cfg, "pr", "view", n, "--json", "body")
	var v struct {
		Body string `json:"body"`
	}
	if err == nil {
		err = json.Unmarshal(out, &v)
	}
	if err == nil {
		next := spliceRanOn(v.Body, st.ranOn)
		if next == v.Body {
			return
		}
		err = ghStdin(ctx, cfg, next, "pr", "edit", n, "--body-file", "-")
	}
	if err != nil {
		cfg.narrate(sevWarning, "could not note on PR #%d which model and effort ran on it (%v) — the shift continues",
			prNumber, err)
		return
	}
	cfg.logf("noted on PR #%d which provider, model and effort ran on it", prNumber)
}
