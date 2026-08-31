package main

// A size budget for cmd/polako, enforced as a test so it fails CI.
//
// Diff-scoped review is structurally blind to accretion: every one of the
// merged PRs on this tree can be individually good and main.go still runs to
// thousands of lines, because no single diff is where it went wrong. Nothing
// that looks at a diff catches that. This looks at the result.
//
// It is a test and not a line in CLAUDE.md for the reason that document gives
// about a different gate — "the gate must not depend on a model remembering". A
// failing test gets fixed inside the run that broke it. A convention gets read,
// or does not.
//
// Companion to scripts/health/main.go, which prints the same measurements and
// enforces nothing. Function lengths here are measured exactly as that report
// measures them — go/ast, the func keyword line through the closing-brace line
// inclusive — because two disagreeing measures of one thing is worse than none.
// The AST walk is duplicated rather than shared: scripts/health is a separate
// package main, cmd/polako reaching into scripts/ would be backwards, and the
// repo-consistency tests in repo_test.go already set duplication-with-a-
// cross-reference as the pattern for this kind of check.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The budgets. Chosen from scripts/health.sh's baseline so that almost the
// whole tree already clears them and the allowlists below stay short — the
// entries are then the real outliers, not a census.
//
//   - fileBudget: every non-test file but stats.go is already under 1,000
//     lines, and stats.go is what is left of the accretion epic. The
//     next-largest file (plan.go) sits ~140 lines clear.
//   - funcBudget: just over two of the health report's 60-line call-out
//     screens, plus a signature and braces. Three functions exceed it; the
//     next-longest (supervisePR) sits a few lines clear, so an ordinary edit
//     does not trip it.
const (
	fileBudget = 1000
	funcBudget = 130
)

// fileDebt and funcDebt are the offenders present the day this test landed,
// each with the length measured then. The rule, same as the contract
// assertions in repo_test.go: entries come off as the debt is paid, nothing
// new goes on, and a fresh violation is fixed rather than listed.
//
// The recorded number is a ceiling, not a licence. An entry that grows past it
// fails exactly like an unlisted file would — "the allowlist only shrinks"
// means raising the number is off the table, because that is adding to the
// allowlist by another name. An entry that drops to the budget or below fails
// too, asking to be removed. So the lists only ever get shorter.
//
// Keys: base filename for fileDebt, "file.go:funcName" for funcDebt (the
// receiver-qualified name for methods, as scripts/health prints it).
var fileDebt = map[string]int{
	"stats.go": 1920, // the report builders and the record loaders are separable; deferred by issue #149
}

var funcDebt = map[string]int{
	"claude.go:dispatchClaude": 273, // arg assembly, spawn, and stream handling are three functions
	"drain.go:drain":           179, // the per-issue loop body wants to be its own function
}

// splitFileHint and splitFuncHint turn a budget failure into an instruction,
// per the errors convention — what to do about it, not just what tripped.
func splitFileHint(name string) string {
	if strings.HasPrefix(name, "main.go") {
		return "extract a cohesive group of functions into its own file"
	}
	return "the file is doing more than one job; separate them"
}

func splitFuncHint() string {
	return "extract the distinct steps into named helpers"
}

func TestSourceStaysWithinSizeBudget(t *testing.T) {
	files, funcs := surveyBudget(t, ".")

	seenFile := map[string]bool{}
	for _, f := range files {
		if f.isTest {
			// Test files are exempt on purpose: drain_test.go at ~3,900 lines
			// is table-driven coverage, which is a different thing from a
			// source file that has taken on too many jobs.
			continue
		}
		if debt, listed := fileDebt[f.base]; listed {
			seenFile[f.base] = true
			switch {
			case f.lines > debt:
				t.Errorf("%s is %d lines, past its allowed %d — %s. The allowlist only shrinks: do not raise the number.",
					f.base, f.lines, debt, splitFileHint(f.base))
			case f.lines <= fileBudget:
				t.Errorf("%s is down to %d lines, within the %d-line budget. Remove its fileDebt entry.",
					f.base, f.lines, fileBudget)
			}
			continue
		}
		if f.lines > fileBudget {
			t.Errorf("%s is %d lines, over the %d-line budget for a non-test source file — %s. A deliberate exception is a discussion for the PR, not a new fileDebt entry.",
				f.base, f.lines, fileBudget, splitFileHint(f.base))
		}
	}
	for name := range fileDebt {
		if !seenFile[name] {
			t.Errorf("fileDebt lists %q, which is not a non-test file here — remove the stale entry.", name)
		}
	}

	seenFunc := map[string]bool{}
	for _, fn := range funcs {
		if debt, listed := funcDebt[fn.key]; listed {
			seenFunc[fn.key] = true
			switch {
			case fn.lines > debt:
				t.Errorf("%s is %d lines, past its allowed %d — %s. The allowlist only shrinks: do not raise the number.",
					fn.key, fn.lines, debt, splitFuncHint())
			case fn.lines <= funcBudget:
				t.Errorf("%s is down to %d lines, within the %d-line budget. Remove its funcDebt entry.",
					fn.key, fn.lines, funcBudget)
			}
			continue
		}
		if fn.lines > funcBudget {
			t.Errorf("%s is %d lines, over the %d-line budget — %s.",
				fn.key, fn.lines, funcBudget, splitFuncHint())
		}
	}
	for key := range funcDebt {
		if !seenFunc[key] {
			t.Errorf("funcDebt lists %q, which is not a function in a non-test file here — remove the stale entry.", key)
		}
	}
}

type budgetFile struct {
	base   string
	lines  int
	isTest bool
}

type budgetFunc struct {
	key   string // "file.go:funcName"
	lines int
}

// surveyBudget parses every .go file directly under dir and measures file
// lengths and function lengths. It mirrors survey() in scripts/health/main.go:
// build tags are ignored (ui_windows.go and its siblings are part of the shape
// too, and nothing here type-checks), and a function is measured from its func
// keyword — not its doc comment — through its closing brace, inclusive.
func surveyBudget(t *testing.T, dir string) ([]budgetFile, []budgetFunc) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	fset := token.NewFileSet()
	var files []budgetFile
	var funcs []budgetFunc
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		isTest := strings.HasSuffix(e.Name(), "_test.go")
		files = append(files, budgetFile{base: e.Name(), lines: countSourceLines(src), isTest: isTest})
		if isTest {
			continue // functions in test files are exempt, same as the files
		}
		f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
		if err != nil {
			t.Fatalf("parsing %s: %v", path, err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			start := fset.Position(fn.Pos()).Line
			end := fset.Position(fn.End()).Line
			funcs = append(funcs, budgetFunc{
				key:   fmt.Sprintf("%s:%s", e.Name(), budgetFuncName(fn)),
				lines: end - start + 1,
			})
		}
	}
	// SliceStable, matching report() in scripts/health/main.go (commit a2aeeba):
	// functions at the same length hold os.ReadDir's name order rather than
	// reshuffling arbitrarily, so a budget failure names the same function run
	// to run.
	sort.SliceStable(funcs, func(i, j int) bool { return funcs[i].lines > funcs[j].lines })
	return files, funcs
}

func countSourceLines(src []byte) int {
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

// budgetFuncName matches funcName in scripts/health/main.go: a method is
// "(*recv) name", so a same-named method on another type keeps its own entry.
func budgetFuncName(fn *ast.FuncDecl) string {
	if fn.Recv != nil && len(fn.Recv.List) > 0 {
		return "(" + budgetReceiverType(fn.Recv.List[0].Type) + ") " + fn.Name.Name
	}
	return fn.Name.Name
}

func budgetReceiverType(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.StarExpr:
		return "*" + budgetReceiverType(t.X)
	case *ast.Ident:
		return t.Name
	case *ast.IndexExpr:
		return budgetReceiverType(t.X)
	case *ast.IndexListExpr:
		return budgetReceiverType(t.X)
	}
	return "?"
}
