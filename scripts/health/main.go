// Command health prints the shape of a Go package tree: its longest files, its
// longest functions, how much of each file is comment, and the line totals. It
// reports and enforces nothing — there is no exit code beyond "the program
// ran". A sibling change turns these numbers into a budget that fails; this one
// only makes them visible, so a wrong number is printed rather than acted on.
//
// Run it through scripts/health.sh (or health.ps1), which sets the working
// directory to the repo root. By hand: `go run ./scripts/health [dir]`, dir
// defaulting to cmd/polako.
//
// Function lengths come from go/ast, not from counting braces: the sibling
// budget test measures the same way, and two disagreeing measures of one thing
// is worse than none.
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// What the report calls out. Deliberately loose: the point is to surface the
// handful of outliers, not to reprint the whole tree. A sibling issue picks the
// numbers a budget actually enforces — these only decide what shows here.
const (
	fileLineThreshold = 500
	funcLineThreshold = 60
)

type fileStat struct {
	path         string // relative to the repo root, e.g. cmd/polako/main.go
	lines        int
	commentLines int // distinct source lines touched by any comment
	isTest       bool
}

type funcStat struct {
	pos   string // file:line of the func keyword
	name  string
	lines int // func keyword line through closing-brace line, inclusive
}

func main() {
	dir := "cmd/polako"
	if len(os.Args) > 1 {
		dir = os.Args[1]
	}

	files, funcs, err := survey(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "health:", err)
		os.Exit(1)
	}
	report(dir, files, funcs)
}

// survey parses every .go file directly under dir. It parses regardless of
// build tags — ui_windows.go and its siblings are part of the shape too — which
// is safe because nothing here type-checks, it only measures.
func survey(dir string) ([]fileStat, []funcStat, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, err
	}
	fset := token.NewFileSet()
	var files []fileStat
	var funcs []funcStat
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		src, err := os.ReadFile(path)
		if err != nil {
			return nil, nil, err
		}
		f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
		if err != nil {
			return nil, nil, err
		}
		files = append(files, fileStat{
			path:         filepath.ToSlash(path),
			lines:        countLines(src),
			commentLines: commentLines(fset, f),
			isTest:       strings.HasSuffix(e.Name(), "_test.go"),
		})
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			// fn.Pos() is the func keyword, not the doc comment above it, so a
			// heavily-documented function is not counted as a long one.
			start := fset.Position(fn.Pos()).Line
			end := fset.Position(fn.End()).Line
			funcs = append(funcs, funcStat{
				pos:   fmt.Sprintf("%s:%d", filepath.ToSlash(path), start),
				name:  funcName(fn),
				lines: end - start + 1,
			})
		}
	}
	return files, funcs, nil
}

func countLines(src []byte) int {
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
		n++ // a final line with no trailing newline still counts
	}
	return n
}

func commentLines(fset *token.FileSet, f *ast.File) int {
	seen := map[int]bool{}
	for _, cg := range f.Comments {
		for _, c := range cg.List {
			// A trailing `x := 1 // note` counts its line as commented too;
			// "how many lines carry commentary" is the useful reading.
			for ln := fset.Position(c.Pos()).Line; ln <= fset.Position(c.End()).Line; ln++ {
				seen[ln] = true
			}
		}
	}
	return len(seen)
}

func funcName(fn *ast.FuncDecl) string {
	if fn.Recv != nil && len(fn.Recv.List) > 0 {
		return "(" + receiverType(fn.Recv.List[0].Type) + ") " + fn.Name.Name
	}
	return fn.Name.Name
}

func receiverType(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.StarExpr:
		return "*" + receiverType(t.X)
	case *ast.Ident:
		return t.Name
	case *ast.IndexExpr: // generic receiver: Foo[T]
		return receiverType(t.X)
	case *ast.IndexListExpr: // generic receiver: Foo[T, U]
		return receiverType(t.X)
	}
	return "?"
}

func report(dir string, files []fileStat, funcs []funcStat) {
	fmt.Printf("health: %s\n", dir)

	sort.SliceStable(files, func(i, j int) bool { return files[i].lines > files[j].lines })
	fmt.Printf("\nnon-test files over %d lines, longest first\n", fileLineThreshold)
	printed := false
	for _, f := range files {
		if f.isTest || f.lines <= fileLineThreshold {
			continue
		}
		fmt.Printf("  %5d  %s\n", f.lines, f.path)
		printed = true
	}
	if !printed {
		fmt.Println("  (none)")
	}

	sort.SliceStable(funcs, func(i, j int) bool { return funcs[i].lines > funcs[j].lines })
	fmt.Printf("\nfunctions over %d lines, longest first\n", funcLineThreshold)
	printed = false
	for _, fn := range funcs {
		if fn.lines <= funcLineThreshold {
			continue
		}
		fmt.Printf("  %5d  %s  %s\n", fn.lines, fn.pos, fn.name)
		printed = true
	}
	if !printed {
		fmt.Println("  (none)")
	}

	byComment := append([]fileStat(nil), files...)
	sort.SliceStable(byComment, func(i, j int) bool {
		return commentRatio(byComment[i]) > commentRatio(byComment[j])
	})
	fmt.Printf("\ncomment lines / total, non-test files, densest first\n")
	for _, f := range byComment {
		if f.isTest {
			continue
		}
		fmt.Printf("  %4.0f%%  %5d/%-5d  %s\n", 100*commentRatio(f), f.commentLines, f.lines, f.path)
	}

	var all, nonTest, test int
	for _, f := range files {
		all += f.lines
		if f.isTest {
			test += f.lines
		} else {
			nonTest += f.lines
		}
	}
	fmt.Printf("\ntotals\n")
	fmt.Printf("  %6d  all Go\n", all)
	fmt.Printf("  %6d  non-test Go\n", nonTest)
	fmt.Printf("  %6d  test Go\n", test)
}

func commentRatio(f fileStat) float64 {
	if f.lines == 0 {
		return 0
	}
	return float64(f.commentLines) / float64(f.lines)
}
