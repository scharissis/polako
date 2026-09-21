package main

// docs/plans/setup.md ticket 5's allowlist half: `polako work`'s default tool
// allowlist (defaultTools, flags.go) covers the common ecosystems, but a
// build tool it doesn't know about is invisible until an unattended run stalls
// on the permission prompt nobody is there to answer — "permission refused"
// was joint first among parks in the run that motivated this plan. This row
// spots the entry-point file a checkout for one of those tools would have and
// prints the -add-tools value to grant it, before a run ever gets there.

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// buildToolDef is one entry point this table watches for and the bare command
// name it needs granted as Bash(<tool>:*). Every entry here must be a tool
// defaultTools does not already cover — TestBuildToolTableDoesNotDuplicateDefaultTools
// guards that, since a duplicate would just be confusing advice.
type buildToolDef struct {
	marker string
	tool   string
}

// buildToolTable is docs/plans/setup.md ticket 5's own list: not exhaustive,
// just the ecosystems that plan named as visible in a checkout before any run.
var buildToolTable = []buildToolDef{
	{marker: "justfile", tool: "just"},
	{marker: "BUILD.bazel", tool: "bazel"},
	{marker: "WORKSPACE", tool: "bazel"},
	{marker: "Taskfile.yml", tool: "task"},
	{marker: "mise.toml", tool: "mise"},
	{marker: "deno.json", tool: "deno"},
	{marker: "bun.lockb", tool: "bun"},
	{marker: "composer.json", tool: "composer"},
	{marker: "Gemfile", tool: "bundle"},
	{marker: "mix.exs", tool: "mix"},
	{marker: "build.zig", tool: "zig"},
}

// missingBuildTools is which of buildToolTable's tools cfg.dir's root has a
// marker for, deduped (BUILD.bazel and WORKSPACE both mean bazel) and in
// buildToolTable's own order. Shared by the row below and suggestedWorkLine,
// so the two can never name a different set. A stat failure other than the
// marker simply not existing (permission denied, a transient FS error) comes
// back as an error rather than being read as "not present" — the caller
// decides what that means for its own row.
func missingBuildTools(cfg config) ([]string, error) {
	var tools []string
	for _, d := range buildToolTable {
		_, err := os.Stat(filepath.Join(cfg.dir, d.marker))
		switch {
		case err == nil:
			if !slices.Contains(tools, d.tool) {
				tools = append(tools, d.tool)
			}
		case os.IsNotExist(err):
		default:
			return nil, err
		}
	}
	return tools, nil
}

// addToolsFlag renders tools as the -add-tools value a human would paste onto
// their own `polako work` invocation.
func addToolsFlag(tools []string) string {
	grants := make([]string, len(tools))
	for i, t := range tools {
		grants[i] = fmt.Sprintf("Bash(%s:*)", t)
	}
	return fmt.Sprintf("-add-tools %q", strings.Join(grants, ","))
}

// setupBuildToolsRow is advisory only, like .gitignore and CLAUDE.md: a
// checkout with an uncovered build tool still runs `polako work` fine for
// everything that doesn't shell out to it, so this never fails the report —
// it just heads off a stall an operator would otherwise find at 2am.
func setupBuildToolsRow(cfg config) setupRow {
	const name = "build tools"
	tools, err := missingBuildTools(cfg)
	if err != nil {
		return setupRow{name: name, status: setupUnknown, detail: fmt.Sprintf("could not check the checkout for build tools (%v)", err)}
	}
	if len(tools) == 0 {
		return setupRow{name: name, status: setupOK}
	}
	return setupRow{name: name, status: setupMissing,
		detail: fmt.Sprintf("not in the default allowlist — pass %s", addToolsFlag(tools))}
}
