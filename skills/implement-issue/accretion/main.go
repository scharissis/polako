// Command accretion measures the numbers behind implement-issue's accretion
// check (SKILL.md, Phase 3 step 2e): every tracked source file's line count
// and comment share, the repository's medians of both, and — for each source
// file the branch changed — where that file stood at the base and where it
// stands now against min(median, ceiling). The model used to sample files and
// work the medians out itself; that's arithmetic with one right answer, so it
// lives here and the model keeps only the extract-or-note call.
//
// It runs in any repository the skill does, not just polako's, so it knows
// nothing about Go beyond being written in it: files are recognised by
// extension, and a comment line is one that starts with its language's
// comment marker or sits inside a block comment. Trailing comments and
// docstrings don't count — a first cut, and the ceiling is generous enough
// that the difference doesn't move a verdict.
//
// Usage: go -C <skill-dir> run ./accretion -repo <worktree> -base <ref>
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
)

// The absolute ceilings from step 2e. They bound the median from above so a
// young repo whose median is a machine's own accreted norm can't ratchet the
// check upward. main_test.go holds SKILL.md to these same numbers.
const (
	fileCeiling    = 1000
	commentCeiling = 0.40
)

// A file this short gets no comment-density verdict: a doc comment on a small
// new file is a high share of nothing, and extraction can't fix a ratio.
const minCommentLines = 50

type measure struct {
	lines, comments int
}

func (m measure) ratio() float64 {
	if m.lines == 0 {
		return 0
	}
	return float64(m.comments) / float64(m.lines)
}

func main() {
	repo := flag.String("repo", ".", "the worktree to measure")
	base := flag.String("base", "", "the ref the branch was cut from, e.g. origin/main")
	flag.Parse()
	if *base == "" {
		fmt.Fprintln(os.Stderr, "accretion: -base is required — pass the origin/… ref the branch was cut from")
		os.Exit(2)
	}
	if err := run(os.Stdout, *repo, *base); err != nil {
		fmt.Fprintln(os.Stderr, "accretion:", err)
		os.Exit(1)
	}
}

func run(w io.Writer, repo, base string) error {
	mergeBase, err := gitOut(repo, "merge-base", base, "HEAD")
	if err != nil {
		return fmt.Errorf("finding the merge base of %s and HEAD: %w", base, err)
	}
	mb := strings.TrimSpace(string(mergeBase))

	// The norm is the base's, not the branch's: measured after the change, a
	// branch's own new files would move the median it is then judged by.
	tracked, err := gitList(repo, "ls-tree", "-r", "-z", "--name-only", mb)
	if err != nil {
		return fmt.Errorf("listing the base's files: %w", err)
	}
	var specs []string
	var specStyles []style
	for _, p := range tracked {
		if st, ok := sourceStyle(p); ok {
			specs = append(specs, mb+":"+p)
			specStyles = append(specStyles, st)
		}
	}
	status, err := gitList(repo, "diff", "--name-status", "-z", "--diff-filter=AMR", mb, "HEAD")
	if err != nil {
		return fmt.Errorf("listing changed files: %w", err)
	}
	changes := parseNameStatus(status)
	// Committed content, not the worktree: the rows describe what diff listed,
	// and an uncommitted edit mid-extraction would otherwise skew them.
	wanted := append([]string(nil), specs...)
	for _, c := range changes {
		wanted = append(wanted, "HEAD:"+c.path, mb+":"+c.old)
	}
	blobs, err := catFile(repo, wanted)
	if err != nil {
		return fmt.Errorf("reading files: %w", err)
	}

	var all []measure
	for i, st := range specStyles {
		if src, ok := blobs[specs[i]]; ok {
			all = append(all, measureSource(src, st))
		}
	}
	if len(all) == 0 {
		fmt.Fprintln(w, "accretion: no source files recognised — measure by hand")
		return nil
	}

	lines := make([]float64, len(all))
	ratios := make([]float64, len(all))
	for i, m := range all {
		lines[i] = float64(m.lines)
		ratios[i] = m.ratio()
	}
	medLines, medRatio := median(lines), median(ratios)
	lineBound := min(medLines, fileCeiling)
	ratioBound := min(medRatio, commentCeiling)

	fmt.Fprintf(w, "accretion: %d source files; median %.0f lines, %.0f%% comment\n",
		len(all), medLines, 100*medRatio)
	lineSource, ratioSource := "median", "median"
	if medLines > fileCeiling {
		lineSource = fmt.Sprintf("ceiling %d", fileCeiling)
	}
	if medRatio > commentCeiling {
		ratioSource = fmt.Sprintf("ceiling %.0f%%", 100*commentCeiling)
	}
	fmt.Fprintf(w, "bounds: file length %.0f lines (%s), comment density %.0f%% (%s)\n\n",
		lineBound, lineSource, 100*ratioBound, ratioSource)

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	rows := 0
	for _, c := range changes {
		p := c.path
		st, ok := sourceStyle(p)
		if !ok {
			continue
		}
		src, ok := blobs["HEAD:"+p]
		if !ok {
			continue
		}
		head := measureSource(src, st)
		// A new file's base is the zero measure, so any excess over the bound
		// reads as worse than the base: it was born over.
		var before measure
		baseLines, baseRatio := "new", "new"
		if old, ok := blobs[mb+":"+c.old]; ok {
			before = measureSource(old, st)
			baseLines = fmt.Sprint(before.lines)
			baseRatio = fmt.Sprintf("%.0f%%", 100*before.ratio())
		}
		density := "ok"
		if head.lines >= minCommentLines {
			density = verdict(before.ratio(), head.ratio(), ratioBound)
		}
		if rows == 0 {
			fmt.Fprintln(tw, "file\tlines base→head\tfile length\tcomment base→head\tcomment density")
		}
		fmt.Fprintf(tw, "%s\t%s→%d\t%s\t%s→%.0f%%\t%s\n", p,
			baseLines, head.lines, verdict(float64(before.lines), float64(head.lines), lineBound),
			baseRatio, 100*head.ratio(), density)
		rows++
	}
	if rows == 0 {
		fmt.Fprintln(tw, "no source file changed since the base")
	}
	return tw.Flush()
}

// verdict is the whole rule, so the model doesn't redo it: under the bound is
// fine; over it but no worse than the base is someone else's debt; over it
// and worse than the base is this change's to extract or note.
func verdict(before, head, bound float64) string {
	switch {
	case head <= bound:
		return "ok"
	case head <= before:
		return "over, not worse"
	default:
		return "act"
	}
}

func median(xs []float64) float64 {
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}
