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
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"sort"
	"strconv"
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

// A block opener is checked before the line markers, so Lua's `--[[` opens a
// block rather than reading as one `--` line.
type style struct {
	line   []string
	blocks [][2]string // open, close
}

var (
	cBlock    = [2]string{"/*", "*/"}
	htmlBlock = [2]string{"<!--", "-->"}

	slash  = style{line: []string{"//"}, blocks: [][2]string{cBlock}}
	markup = style{line: []string{"//"}, blocks: [][2]string{cBlock, htmlBlock}}
	php    = style{line: []string{"//", "#"}, blocks: [][2]string{cBlock}}
	hash   = style{line: []string{"#"}}
	css    = style{blocks: [][2]string{cBlock}}
	ps     = style{line: []string{"#"}, blocks: [][2]string{{"<#", "#>"}}}
	sql    = style{line: []string{"--"}, blocks: [][2]string{cBlock}}
	lua    = style{line: []string{"--"}, blocks: [][2]string{{"--[[", "]]"}}}
	hs     = style{line: []string{"--"}, blocks: [][2]string{{"{-", "-}"}}}
)

var styles = map[string]style{
	".go": slash, ".c": slash, ".h": slash, ".cc": slash, ".cpp": slash, ".hpp": slash,
	".cs": slash, ".java": slash, ".kt": slash, ".kts": slash, ".scala": slash,
	".swift": slash, ".rs": slash, ".dart": slash, ".php": php,
	".js": slash, ".jsx": slash, ".mjs": slash, ".cjs": slash, ".ts": slash, ".tsx": slash,
	".vue": markup, ".svelte": markup, ".astro": markup,
	".css": css, ".scss": slash, ".less": slash,
	".py": hash, ".rb": hash, ".sh": hash, ".bash": hash, ".zsh": hash, ".pl": hash,
	".r": hash, ".ex": hash, ".exs": hash, ".tf": hash, ".ps1": ps,
	".sql": sql, ".lua": lua, ".hs": hs,
}

// Paths a repo carries but didn't write; measuring them would set the median
// by someone else's code.
var skipDirs = []string{"vendor/", "node_modules/", "third_party/"}

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
	fmt.Fprintf(w, "bounds: file length %.0f lines (%s), comment density %.0f%% (%s)\n\n",
		lineBound, source(medLines, fileCeiling, "1000"), 100*ratioBound, source(medRatio, commentCeiling, "40%"))

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
		var before measure
		baseText := "new"
		if old, ok := blobs[mb+":"+c.old]; ok {
			before = measureSource(old, st)
			baseText = fmt.Sprint(before.lines)
		}
		density := "ok"
		if head.lines >= minCommentLines {
			density = verdict(before.ratio(), head.ratio(), ratioBound)
		}
		if rows == 0 {
			fmt.Fprintln(tw, "file\tlines base→head\tfile length\tcomment base→head\tcomment density")
		}
		fmt.Fprintf(tw, "%s\t%s→%d\t%s\t%s→%.0f%%\t%s\n", p,
			baseText, head.lines, verdict(float64(before.lines), float64(head.lines), lineBound),
			pct(baseText, before), 100*head.ratio(), density)
		rows++
	}
	if rows == 0 {
		fmt.Fprintln(tw, "no source file changed since the base")
	}
	return tw.Flush()
}

type change struct {
	old, path string // old is where the file lived at the base: path, unless renamed
}

// parseNameStatus reads `git diff --name-status -z`: a status field, then one
// path — or, for a rename, the old path and the new one. A moved file is
// measured against its old self, not as new.
func parseNameStatus(fields []string) []change {
	var out []change
	for i := 0; i+1 < len(fields); {
		if strings.HasPrefix(fields[i], "R") && i+2 < len(fields) {
			out = append(out, change{old: fields[i+1], path: fields[i+2]})
			i += 3
			continue
		}
		out = append(out, change{old: fields[i+1], path: fields[i+1]})
		i += 2
	}
	return out
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

func source(med, ceiling float64, spelled string) string {
	if med <= ceiling {
		return "median"
	}
	return "ceiling " + spelled
}

func pct(baseText string, m measure) string {
	if baseText == "new" {
		return "new"
	}
	return fmt.Sprintf("%.0f%%", 100*m.ratio())
}

func sourceStyle(p string) (style, bool) {
	for _, d := range skipDirs {
		if strings.HasPrefix(p, d) || strings.Contains(p, "/"+d) {
			return style{}, false
		}
	}
	if strings.HasSuffix(p, ".min.js") || strings.HasSuffix(p, ".min.css") {
		return style{}, false
	}
	st, ok := styles[strings.ToLower(path.Ext(p))]
	return st, ok
}

// measureSource counts lines the way scripts/health does — a final line with
// no newline still counts — and comment lines by leading marker.
func measureSource(src []byte, st style) measure {
	var m measure
	if len(src) == 0 {
		return m
	}
	closer := "" // non-empty while inside a block comment
	for _, raw := range bytes.Split(bytes.TrimSuffix(src, []byte("\n")), []byte("\n")) {
		m.lines++
		t := strings.TrimSpace(string(raw))
		if closer != "" {
			m.comments++
			if strings.Contains(t, closer) {
				closer = ""
			}
			continue
		}
		if t == "" {
			continue
		}
		if b, ok := openedBlock(t, st); ok {
			m.comments++
			if !strings.Contains(t[len(b[0]):], b[1]) {
				closer = b[1]
			}
			continue
		}
		if hasAnyPrefix(t, st.line) {
			m.comments++
		}
	}
	return m
}

func openedBlock(t string, st style) ([2]string, bool) {
	for _, b := range st.blocks {
		if strings.HasPrefix(t, b[0]) {
			return b, true
		}
	}
	return [2]string{}, false
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
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

func gitOut(repo string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, errors.New(msg)
		}
		return nil, err
	}
	return out, nil
}

// catFile reads every `<rev>:<path>` spec in one `git cat-file --batch`, not
// one process per file. A spec git can't resolve to a blob — missing, a
// submodule — is left out of the map.
func catFile(repo string, specs []string) (map[string][]byte, error) {
	var in bytes.Buffer
	var asked []string
	for _, s := range specs {
		if !strings.Contains(s, "\n") { // one spec per line; such a path can't be asked for
			in.WriteString(s + "\n")
			asked = append(asked, s)
		}
	}
	cmd := exec.Command("git", "-C", repo, "cat-file", "--batch")
	cmd.Stdin = &in
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	blobs := map[string][]byte{}
	for _, s := range asked {
		nl := bytes.IndexByte(out, '\n')
		if nl < 0 {
			return nil, errors.New("cat-file output ended early")
		}
		line := string(out[:nl])
		out = out[nl+1:]
		if strings.HasSuffix(line, " missing") || strings.HasSuffix(line, " ambiguous") {
			continue
		}
		header := strings.Fields(line) // "<oid> <type> <size>"
		if len(header) != 3 {
			return nil, fmt.Errorf("unreadable cat-file header %q", line)
		}
		size, err := strconv.Atoi(header[2])
		if err != nil || size+1 > len(out) {
			return nil, fmt.Errorf("unreadable cat-file header %q", header)
		}
		if header[1] == "blob" {
			blobs[s] = out[:size]
		}
		out = out[size+1:]
	}
	return blobs, nil
}

func gitList(repo string, args ...string) ([]string, error) {
	out, err := gitOut(repo, args...)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths, nil
}
