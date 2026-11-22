package main

// Docs that state how many verbs polako has go stale each time a verb lands
// (issue #641: three pages said seven while the binary had nine). This pins
// every stated count to verbUsage, the one list that can't advertise a verb
// that doesn't exist. Near docsbudget_test.go's walk — README.md, CLAUDE.md,
// docs/*.md and docs/demo.tape, not docs/designs/, whose plan documents record the count as
// it was when each was written.
//
// Only totals are pinned: "<N> verbs" and "the other <N> verbs". Partial
// counts like "Four start Claude runs" need a run/no-run split the usage text
// doesn't carry, so they stay prose.

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

var countWords = map[string]int{
	"one": 1, "two": 2, "three": 3, "four": 4, "five": 5, "six": 6,
	"seven": 7, "eight": 8, "nine": 9, "ten": 10, "eleven": 11, "twelve": 12,
	"thirteen": 13, "fourteen": 14, "fifteen": 15,
}

// statedVerbCount matches a count directly before "verbs", with the word
// before it so "the other nine verbs" can be told from "ten verbs" and a
// partial like "the binary's other six verbs" can be skipped.
var statedVerbCount = regexp.MustCompile(`(?i)(\S+\s+)?(\S+)\s+(\d+|[a-z]+)\s+verbs\b`)

func usageVerbs(t *testing.T) []string {
	t.Helper()
	var b strings.Builder
	verbUsage(&b)
	var verbs []string
	for _, m := range regexp.MustCompile(`(?m)^  ([a-z]+) +\S`).FindAllStringSubmatch(b.String(), -1) {
		verbs = append(verbs, m[1])
	}
	if len(verbs) == 0 {
		t.Fatalf("found no verbs in verbUsage's output:\n%s", b.String())
	}
	return verbs
}

func TestUsageVerbsParsesEveryVerb(t *testing.T) {
	t.Parallel()
	got := usageVerbs(t)
	for _, want := range []string{"work", "plan", "health", "design", "status", "stats", "tidy", "unpark", "update", "setup"} {
		found := false
		for _, v := range got {
			found = found || v == want
		}
		if !found {
			t.Errorf("usageVerbs misses %q; got %v", want, got)
		}
	}
}

// The bare usage is read in a terminal before anything else (issue #730):
// it fits 80 columns, says where a new repo starts, and lists the verbs in
// the order you reach for them.
func TestVerbUsageIsGroupedByStage(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	verbUsage(&b)
	out := b.String()
	for _, line := range strings.Split(out, "\n") {
		if n := utf8.RuneCountInString(line); n > 80 {
			t.Errorf("line is %d columns, over 80: %q", n, line)
		}
	}
	if !strings.Contains(out, "New repo? Run polako setup.") {
		t.Errorf("usage doesn't say where a new repo starts:\n%s", out)
	}
	want := []string{"setup", "design", "plan", "health", "work", "status", "stats", "unpark", "tidy", "update"}
	if got := usageVerbs(t); !slices.Equal(got, want) {
		t.Errorf("verb order = %v, want %v", got, want)
	}
	last := -1
	for _, h := range []string{"\nGet ready\n", "\nFill the backlog\n", "\nWork it\n", "\nLook after it\n"} {
		i := strings.Index(out, h)
		if i <= last {
			t.Errorf("group heading %q missing or out of order:\n%s", strings.TrimSpace(h), out)
		}
		last = i
	}
}

func TestDocsStateTheVerbCountVerbUsageLists(t *testing.T) {
	t.Parallel()
	total := len(usageVerbs(t))

	files := [][]string{{"README.md"}, {"CLAUDE.md"}}
	entries, err := os.ReadDir(filepath.Join(repoRoot(), "docs"))
	if err != nil {
		t.Fatalf("reading docs: %v", err)
	}
	for _, e := range entries {
		// demo.tape too: its comments narrate the verb table the gif shows.
		if ext := filepath.Ext(e.Name()); !e.IsDir() && (ext == ".md" || ext == ".tape") {
			files = append(files, []string{"docs", e.Name()})
		}
	}

	checked := 0
	for _, parts := range files {
		// Counts wrap across lines, so match on the whitespace-folded text.
		text := strings.Join(strings.Fields(readRepoFile(t, parts...)), " ")
		for _, m := range statedVerbCount.FindAllStringSubmatch(text, -1) {
			n, ok := countWords[strings.ToLower(m[3])]
			if !ok {
				if n, err = strconv.Atoi(m[3]); err != nil {
					continue
				}
			}
			want := total
			if strings.EqualFold(m[2], "other") {
				if !strings.EqualFold(strings.TrimSpace(m[1]), "the") {
					continue
				}
				want = total - 1
			}
			checked++
			if n != want {
				t.Errorf("%s says %q, but verbUsage lists %d verbs — fix the count, or drop it.",
					filepath.Join(parts...), m[0], total)
			}
		}
	}
	if checked == 0 {
		t.Error("no doc states a verb count — if that's deliberate, delete this test; otherwise the matcher broke")
	}
}
