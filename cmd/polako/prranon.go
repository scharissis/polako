package main

// The ran-on block: a marked `## Ran on` section in a PR body naming each
// provider · model · effort that worked on the PR (issues #614, #673). The skill
// can't write it — no event reports effort, and the provider is a CLI setting
// — so the binary does, after the run that opens the PR and after each
// remediation that pushes to it. Identifiers only: no cost, no tokens, no
// account; those stay behind -post-summary.

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const (
	ranOnBegin   = "<!-- polako:ran-on -->"
	ranOnEnd     = "<!-- /polako:ran-on -->"
	ranOnHeading = "## Ran on"
	ranOnBullet  = "- "
	// ranOnPrefix is the line shape before #673, still read so a block
	// already on an open PR merges rather than doubling up.
	ranOnPrefix = "Ran on "
)

// trailerLine matches the lines a PR body ends on — a closing keyword, the
// attribution, a co-author — which the block goes above.
var trailerLine = regexp.MustCompile(`(?i)^((close|resolve)[sd]?|fix(e[sd])?)\s+\S*#\d+|^🤖 generated with|^co-authored-by:`)

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
	return fmt.Sprintf("%s%s (%s)", ranOnBullet, l.combo, strings.Join(l.reasons, ", "))
}

// prCombo names what one run ran on, in stats' own words (ranOn): model is
// what the run's init reported, with what polako asked for beside it; effort
// is only ever what was asked for, since no event reports the effort that ran.
func prCombo(r runRecord) string {
	provider, model, effort := ranOn(r)
	if r.RequestedModel != "" {
		model += " (asked for " + r.RequestedModel + ")"
	}
	return fmt.Sprintf("%s · %s · effort %s", provider, model, effort)
}

// noteRanOn adds one finished run to the issue's lines, from the record the
// run just produced — built even under -metrics off — so the block and the
// run data can't disagree about what ran. A run that did no observable work
// names nothing: init reports a model before the first API call, so a run
// refused for a usage limit or a dead token still carries one.
func noteRanOn(st *issueState, rec runRecord, rep runReport) {
	if rec.Model == "" || !rep.progressed() {
		return
	}
	st.ranOn = mergeRanOn(st.ranOn, prRanOn{combo: prCombo(rec), reasons: []string{rec.Reason}})
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

// parseRanOn reads a line this file wrote back into its combo and reasons,
// in either shape.
func parseRanOn(line string) (prRanOn, bool) {
	rest, ok := strings.CutPrefix(line, ranOnBullet)
	if !ok {
		rest, ok = strings.CutPrefix(line, ranOnPrefix)
	}
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
// byte, bar the blank lines around a block that moves. A block goes above the
// body's closing trailers — the skill's `Closes #N`, the attribution — so
// those still end the PR; with no trailers, at the end. A block with only
// whitespace after it is moved there, which is how one written before #673,
// at the very end, gets its place; one with text after it stays put. A
// marker counts only on a line of its own, and the last complete block wins,
// so a PR body that quotes the markers in prose or code — this feature's own
// PRs do — is just text.
func spliceRanOn(body string, ours []prRanOn) string {
	begin, end := ranOnMarkers(body)
	var lines []prRanOn
	if begin >= 0 {
		for _, l := range strings.Split(body[begin+len(ranOnBegin):end], "\n") {
			l = strings.TrimRight(l, "\r")
			if t := strings.TrimSpace(l); t == "" || t == ranOnHeading {
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
	block := renderRanOn(lines)
	// after is how the body ended past a block being moved, restored so the
	// move doesn't also change the body's last byte.
	after := ""
	if begin >= 0 {
		after = body[end+len(ranOnEnd):]
		if strings.TrimSpace(after) != "" {
			return body[:begin] + block + after
		}
		body = strings.TrimRight(body[:begin], " \t\r\n")
	}
	if at := trailerStart(body); at >= 0 {
		return body[:at] + blankLineBefore(body[:at]) + block + "\n\n" + body[at:] + after
	}
	if begin < 0 {
		after = "\n"
	}
	return body + blankLineBefore(body) + block + after
}

// renderRanOn is the block itself, markers included. A line nobody could
// parse gets a blank line either side, or markdown folds it into a bullet.
func renderRanOn(lines []prRanOn) string {
	var b strings.Builder
	b.WriteString(ranOnBegin + "\n" + ranOnHeading + "\n\n")
	for i, l := range lines {
		if i > 0 && (l.raw != "" || lines[i-1].raw != "") {
			b.WriteString("\n")
		}
		b.WriteString(l.String() + "\n")
	}
	b.WriteString(ranOnEnd)
	return b.String()
}

// trailerStart is the offset of the first line in body's closing run of
// trailer lines, blank lines allowed between them; -1 when body doesn't end
// on one.
func trailerStart(body string) int {
	start := -1
	for rest := body; rest != ""; {
		i := strings.LastIndex(strings.TrimSuffix(rest, "\n"), "\n") + 1
		switch line := strings.TrimSpace(rest[i:]); {
		case line == "":
		case trailerLine.MatchString(line):
			start = i
		default:
			return start
		}
		rest = rest[:i]
	}
	return start
}

// blankLineBefore is what s needs appended so the next line is a new
// paragraph: nothing for an empty s or one already ending in a blank line.
func blankLineBefore(s string) string {
	if s == "" {
		return ""
	}
	switch strings.Count(s[len(strings.TrimRight(s, " \t\r\n")):], "\n") {
	case 0:
		return "\n\n"
	case 1:
		return "\n"
	}
	return ""
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
	// Nothing new since this process last wrote: skip the read and the edit.
	sent := fmt.Sprint(prNumber, st.ranOn)
	if sent == st.ranOnSent {
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
			st.ranOnSent = sent
			return
		}
		_, err = ghStdin(ctx, cfg, next, "pr", "edit", n, "--body-file", "-")
	}
	if err != nil {
		cfg.narrate(sevWarning, "could not note on PR #%d which provider, model and effort ran on it (%v) — "+
			"the shift continues; check `gh auth status` can edit PRs here, and the next run's write retries it",
			prNumber, err)
		return
	}
	st.ranOnSent = sent
	cfg.detailf("noted on PR #%d which provider, model and effort ran on it", prNumber)
}
