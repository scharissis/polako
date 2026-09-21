package main

// docs/plans/setup.md ticket 5's template half: a `.github/ISSUE_TEMPLATE/*`
// form whose `labels:` key names orchestration state hands it to whoever
// files the issue, not just a maintainer — docs/security.md already warns
// "keep it out of your templates" by hand. This is the row that catches it,
// and the same scan this repository's own self-test
// (TestIssueTemplatesApplyNoOrchestrationLabel, repo_test.go) uses on
// polako's own templates — moved here so both read one copy.

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// templateLabelLines pulls the labels: key and its list items out of one
// issue template's YAML — quoted, dash-list, and inline forms alike, the same
// as a plain scan for the word "labels" would over-match on unrelated body
// text.
func templateLabelLines(form string) []string {
	var out []string
	list := false
	for _, line := range strings.Split(form, "\n") {
		switch {
		case strings.HasPrefix(line, "labels:"):
			out, list = append(out, line), true
		case list && strings.HasPrefix(strings.TrimSpace(line), "- "):
			out = append(out, line)
		default:
			list = false
		}
	}
	return out
}

// templateLabelTokens turns the labels: block templateLabelLines found into
// individual label names, stripping YAML's quoting — double, single, or
// none — whether written as a flow list (`[a, "b"]`), a bare scalar
// (`labels: a`), or a block list (`labels:` then `- a` / `- "b"` below it).
// A raw substring scan over the line text can't tell `proposed` (a leak) from
// `not-proposed` or `risk-model:high` (not); token boundaries can.
func templateLabelTokens(lines []string) []string {
	var tokens []string
	for _, line := range lines {
		if rest, ok := strings.CutPrefix(line, "labels:"); ok {
			rest = strings.TrimSuffix(strings.TrimSpace(rest), "]")
			rest = strings.TrimPrefix(rest, "[")
			for _, part := range strings.Split(rest, ",") {
				if part = unquoteYAMLScalar(part); part != "" {
					tokens = append(tokens, part)
				}
			}
			continue
		}
		item := strings.TrimPrefix(strings.TrimSpace(line), "- ")
		if part := unquoteYAMLScalar(item); part != "" {
			tokens = append(tokens, part)
		}
	}
	return tokens
}

// unquoteYAMLScalar trims whitespace and, if present, one layer of matching
// double or single quotes.
func unquoteYAMLScalar(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && ((s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'')) {
		return s[1 : len(s)-1]
	}
	return s
}

// leakedTemplateLabel reports the first label token that names a member of
// forbidden, or carries a model:/effort: prefix — the former lets an
// outsider queue work on a gated repo, the latter raises what an unattended
// run costs. "" means the template is clean.
func leakedTemplateLabel(lines []string, forbidden []string) string {
	for _, token := range templateLabelTokens(lines) {
		if slices.Contains(forbidden, token) {
			return token
		}
		for _, prefix := range []string{"model:", "effort:"} {
			if strings.HasPrefix(token, prefix) {
				return token
			}
		}
	}
	return ""
}

// setupTemplatesRow scans cfg.dir's issue templates for a labels: key naming
// the gate label or one of the three labelTable manages. Required: a leaky
// template defeats -label's own point, that only a maintainer can queue work
// on a gated repo — see docs/security.md.
func setupTemplatesRow(cfg config) setupRow {
	const name = "issue templates"
	dir := filepath.Join(cfg.dir, ".github", "ISSUE_TEMPLATE")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return setupRow{name: name, status: setupOK}
	}
	forbidden := []string{needsHumanLabel, proposedLabel, awaitingAnswerLabel}
	if cfg.label != "" {
		forbidden = append(forbidden, cfg.label)
	}
	for _, e := range entries {
		ext := filepath.Ext(e.Name())
		if ext != ".yml" && ext != ".yaml" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		if label := leakedTemplateLabel(templateLabelLines(string(b)), forbidden); label != "" {
			return setupRow{name: name, status: setupMissing, required: true,
				detail: fmt.Sprintf("%s applies %q — a template's labels are applied whoever files the "+
					"issue, so this lets an outsider queue work or raise its cost; remove it from labels:",
					e.Name(), label)}
		}
	}
	return setupRow{name: name, status: setupOK}
}
