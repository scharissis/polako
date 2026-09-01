package main

// A line budget for the docs, enforced as a test so it fails CI. Same shape
// and same reasoning as sizebudget_test.go's fileBudget/fileDebt: a rewrite
// that leaves docs terser reverses in three PRs without a gate that does not
// depend on a model remembering. This does not judge prose — it only stops
// re-bloat.
//
// Scope: every .md file directly under docs/ (os.ReadDir, not recursive —
// same walk TestDocsDocumentEveryFlag uses) plus README.md at the repo root.
// That non-recursive walk already excludes docs/plans/: those are plan
// documents, not doc pages, the same category as the top-level plans/
// directory, which this test does not touch either. CLAUDE.md, CONTRIBUTING.md,
// SECURITY.md, CODE_OF_CONDUCT.md, CHANGELOG.md and skill SKILL.md files are
// out of scope the same way — nothing here ever looks at them.

import (
	"os"
	"path/filepath"
	"testing"
)

// docsBudget is chosen from today's line counts so that almost the whole set
// already clears it and docsDebt stays short — the entries are then the real
// outliers, not a census. Six of eight files are already under 250; the
// other two (reference.md, run-data.md) are docsDebt's whole contents.
const docsBudget = 500

// docsDebt holds the offenders present the day this test landed, each with
// the length measured then. Same rule as fileDebt: entries come off as the
// debt is paid, nothing new goes on, and the recorded number is a ceiling —
// raising it is off the table.
var docsDebt = map[string]int{
	"reference.md": 635,
	"run-data.md":  702,
}

func TestDocsStayWithinLineBudget(t *testing.T) {
	files := map[string]int{"README.md": countRepoFileLines(t, "README.md")}

	dir := filepath.Join(repoRoot(), "docs")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".md" {
			continue
		}
		files[e.Name()] = countRepoFileLines(t, "docs", e.Name())
	}

	seen := map[string]bool{}
	for name, lines := range files {
		if debt, listed := docsDebt[name]; listed {
			seen[name] = true
			switch {
			case lines > debt:
				t.Errorf("%s is %d lines, past its allowed %d — trim it back down. The allowlist only shrinks: do not raise the number.",
					name, lines, debt)
			case lines <= docsBudget:
				t.Errorf("%s is down to %d lines, within the %d-line budget. Remove its docsDebt entry.",
					name, lines, docsBudget)
			}
			continue
		}
		if lines > docsBudget {
			t.Errorf("%s is %d lines, over the %d-line docs budget — trim it, or add a docsDebt entry with today's count and a reason in the PR.",
				name, lines, docsBudget)
		}
	}
	for name := range docsDebt {
		if !seen[name] {
			t.Errorf("docsDebt lists %q, which is not a docs file this test walks — remove the stale entry.", name)
		}
	}
}

func countRepoFileLines(t *testing.T, parts ...string) int {
	t.Helper()
	src := []byte(readRepoFile(t, parts...))
	if len(src) == 0 {
		return 0
	}
	n := 0
	for _, b := range src {
		if b == '\n' {
			n++
		}
	}
	if src[len(src)-1] != '\n' {
		n++
	}
	return n
}
