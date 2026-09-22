package main

// A guard so a new test can't land serial and quietly undo #318's win.
//
// 633 of 656 tests called t.Parallel() when #318 landed; nothing kept it
// that way. A test written without t.Parallel() compiles, passes, and runs
// serial with no red build to say when the suite's wall clock crept back up.
//
// Same shape as sizebudget_test.go: a go/ast walk with a shrinking
// allowlist, and the same reason it's a test rather than a line in
// CLAUDE.md — the gate can't depend on a model remembering.
//
// Detection stops at closures (t.Run subtests, defers, goroutines): a test
// that only parallelises its subtests still serialises everything before
// the first t.Run, so it does not count as calling t.Parallel() itself. Only
// a top-level Test* function's own body is inspected, matching "TestMain and
// subtests are out of the check" in the issue this guards.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// serialAllow lists every top-level test allowed to skip t.Parallel(), each
// with why in a few words. The rule is sizebudget_test.go's funcDebt rule:
// entries come off as tests are fixed, nothing new goes on, and a test that
// starts calling t.Parallel() or stops existing fails too, so the list can't
// rot. What belongs here: a test that sets real process environment that is
// itself under test (POLAKO_*, TERM/NO_COLOR, HOME, GIT_SSH_COMMAND) via
// t.Setenv, directly or through a helper — t.Setenv panics in a parallel
// test. Anything else is a bug this guard exists to catch, not a candidate
// for this list.
var serialAllow = map[string]string{
	"TestEffortFlagGate": "t.Setenv(POLAKO_FAKE_CLAUDE)",
	"TestDispatchGivesTheChildTheOperatorsEnvironment":    "t.Setenv(POLAKO_TEST_ENV_CANARY)",
	"TestGhAndGitInheritTheOperatorsEnvironmentToo":       "t.Setenv(POLAKO_FAKE_CLAUDE, POLAKO_TEST_ENV_CANARY)",
	"TestEnvDefaultsSetWhatWasNotPassed":                  "t.Setenv(POLAKO_POST_SUMMARY, POLAKO_MODEL, POLAKO_POLL)",
	"TestCommandLineBeatsTheEnvironment":                  "t.Setenv(POLAKO_MODEL, POLAKO_POST_SUMMARY)",
	"TestEnvDefaultsRejectAValueTheFlagCannotParse":       "t.Setenv(POLAKO_POLL)",
	"TestEnvDefaultsIgnoreTheActionFlags":                 "t.Setenv(POLAKO_VERSION, POLAKO_DRY_RUN, POLAKO_APPLY, POLAKO_YES)",
	"TestRecorderOffWritesNothing":                        "homeDir(t) sets HOME/USERPROFILE",
	"TestNewRecorderDefaultsUnderTheHomeDirectory":        "homeDir(t) sets HOME/USERPROFILE",
	"TestStatsReadsTheMetricsDirectoryFromTheEnvironment": "t.Setenv(POLAKO_METRICS)",
	"TestExplicitSinceBeatsAWindowEnvDefault":             "t.Setenv(POLAKO_WINDOW)",
	"TestExplicitWindowBeatsASinceEnvDefault":             "t.Setenv(POLAKO_SINCE)",
	"TestStyleForGatesColour":                             "t.Setenv(TERM, NO_COLOR)",
	"TestNewReportGoesThroughStyleFor":                    "t.Setenv(TERM)",
}

func TestNewTestsCallParallel(t *testing.T) {
	t.Parallel()
	tests, err := surveyParallel(t, ".")
	if err != nil {
		t.Fatalf("surveying test functions: %v", err)
	}

	seen := map[string]bool{}
	for _, pt := range tests {
		reason, listed := serialAllow[pt.name]
		if pt.parallel {
			if listed {
				t.Errorf("%s calls t.Parallel() but is still on serialAllow (%q) — remove its entry.",
					pt.name, reason)
			}
			continue
		}
		if !listed {
			t.Errorf("%s does not call t.Parallel() and is not on serialAllow — add t.Parallel(), or if"+
				" it must set real process environment under test (POLAKO_*, TERM/NO_COLOR, HOME,"+
				" GIT_SSH_COMMAND), add it to serialAllow with why.", pt.name)
			continue
		}
		seen[pt.name] = true
	}
	for name := range serialAllow {
		if !seen[name] {
			t.Errorf("serialAllow lists %q, which is not a serial top-level test here — remove the stale entry.", name)
		}
	}
}

type parallelTest struct {
	name     string
	parallel bool
}

// surveyParallel parses every _test.go file directly under dir and reports,
// for each top-level Test* function (TestMain excluded), whether its own
// body calls t.Parallel() on its *testing.T parameter.
func surveyParallel(t *testing.T, dir string) ([]parallelTest, error) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	var tests []parallelTest
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		src, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
		if err != nil {
			return nil, err
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || fn.Body == nil {
				continue
			}
			if !strings.HasPrefix(fn.Name.Name, "Test") || fn.Name.Name == "TestMain" {
				continue
			}
			param := testingTParam(fn)
			if param == "" {
				continue // not a top-level func(*testing.T) test
			}
			tests = append(tests, parallelTest{
				name:     fn.Name.Name,
				parallel: callsParallel(fn.Body, param),
			})
		}
	}
	return tests, nil
}

// testingTParam returns the parameter name of a func's sole *testing.T
// argument, or "" if it has none — the signature every top-level test has.
func testingTParam(fn *ast.FuncDecl) string {
	if fn.Type.Params == nil {
		return ""
	}
	for _, field := range fn.Type.Params.List {
		star, ok := field.Type.(*ast.StarExpr)
		if !ok {
			continue
		}
		sel, ok := star.X.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "T" {
			continue
		}
		if ident, ok := sel.X.(*ast.Ident); !ok || ident.Name != "testing" {
			continue
		}
		if len(field.Names) == 0 {
			return ""
		}
		return field.Names[0].Name
	}
	return ""
}

// callsParallel reports whether body calls <param>.Parallel() anywhere
// outside a nested function literal — a closure passed to t.Run or deferred
// is a subtest or callback, out of scope per the issue this guards.
func callsParallel(body *ast.BlockStmt, param string) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Parallel" {
			return true
		}
		if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == param {
			found = true
			return false
		}
		return true
	})
	return found
}
