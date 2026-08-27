package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The fixture is deliberately awkward, because real files are: a line torn by
// a hard kill, a field this reader has never seen, a record kind it has never
// seen, an issue that reached a terminal state twice, a crash that streamed
// tokens without ever reporting a result, and the resumed session that
// finished its work.
const fixtureMain = `
{"v":1,"kind":"run","ts":"2026-08-20T09:00:00Z","ended":"2026-08-20T09:20:00Z","repo":"scharissis/polako","issue":12,"pr":0,"reason":"implement","attempt":0,"session":"s12a","status":"ok","subtype":"success","outcome":"posted_questions","turns":20,"tool_uses":18,"wall_ms":1200000,"api_ms":900000,"cost_usd":1.10,"usage_source":"result","tokens":{"in":2000,"out":30000,"cache_read":4000000,"cache_write":200000},"model":"claude-opus-5","tag":"baseline","future_field":"a field written by a newer version"}
{"v":1,"kind":"run","ts":"2026-08-20T12:30:00Z","ended":"2026-08-20T13:00:00Z","repo":"scharissis/polako","issue":12,"pr":34,"reason":"answers","attempt":0,"session":"s12b","status":"ok","subtype":"success","outcome":"opened_pr","turns":40,"tool_uses":35,"wall_ms":1800000,"api_ms":1400000,"cost_usd":2.50,"usage_source":"result","tokens":{"in":3000,"out":50000,"cache_read":6000000,"cache_write":300000},"model":"claude-opus-5","tag":"baseline"}
{"v":1,"kind":"issue","ts":"2026-08-20T14:00:00Z","repo":"scharissis/polako","issue":12,"pr":34,"outcome":"needs_human","tag":"baseline"}
{"v":1,"kind":"issue","ts":"2026-08-20T15:00:00Z","repo":"scharissis/polako","issue":12,"pr":34,"outcome":"merged","tag":"baseline"}
{"v":1,"kind":"run","ts":"2026-08-21T09:00:00Z","ended":"2026-08-21T09:05:00Z","repo":"scharissis/polako","issue":13,"pr":0,"reason":"implement","attempt":0,"session":"s13a","status":"crash","exit_code":7,"outcome":"nothing","turns":6,"tool_uses":5,"wall_ms":300000,"cost_usd":0,"usage_source":"observed","tokens":{"in":500,"out":6000,"cache_read":700000,"cache_write":40000},"model":"claude-opus-5","tag":"baseline"}
{"v":1,"kind":"run","ts":"2026-08-21T09:06:00Z","ended":"2026-08-21T09:40:00Z","repo":"scharissis/polako","issue":13,"pr":35,"reason":"resume","attempt":1,"session":"s13a","resumed_from":"s13a","status":"ok","subtype":"success","outcome":"opened_pr","turns":31,"tool_uses":27,"wall_ms":2040000,"api_ms":1500000,"cost_usd":3.00,"usage_source":"result","tokens":{"in":2500,"out":42000,"cache_read":5200000,"cache_write":260000},"model":"claude-opus-5","tag":"baseline"}
{"v":1,"kind":"issue","ts":"2026-08-21T11:00:00Z","repo":"scharissis/polako","issue":13,"pr":35,"outcome":"merged","tag":"baseline"}
{"v":1,"kind":"run","ts":"2026-08-22T09:00:00Z","ended":"2026-08-22T09:30:00Z","repo":"scharissis/polako","issue":14,"reason":"implement","status":"no-turns","subtype":"success","outcome":"nothing","turns":0,"wall_ms":1800000,"cost_usd":0.10,"usage_source":"result","tokens":{"in":100,"out":900,"cache_read":50000,"cache_write":2000},"model":"claude-sonnet-5","tag":"baseline"}
{"v":1,"kind":"issue","ts":"2026-08-22T09:31:00Z","repo":"scharissis/polako","issue":14,"pr":0,"outcome":"needs_human","tag":"baseline"}
{"v":1,"kind":"pr","ts":"2026-08-22T10:00:00Z","repo":"scharissis/polako","issue":14,"note":"a record kind written by a newer version"}
{"v":1,"kind":"run","ts":"2026-08-23T09:00:00Z","ended":"2026-08-23T09:12:00Z","repo":"scharissis/polako","issue":15,"pr":36,"reason":"remediate","status":"ok","subtype":"success","outcome":"nothing","turns":9,"tool_uses":8,"wall_ms":720000,"cost_usd":0.40,"usage_source":"result","tokens":{"in":400,"out":5000,"cache_read":600000,"cache_write":30000},"model":"claude-opus-5","tag":"terse-plan"}
{"v":1,"kind":"run","ts":"2026-08-23T10:00:00Z","ended":"2026-08-2`

const fixtureOther = `
{"v":1,"kind":"run","ts":"2026-08-24T09:00:00Z","ended":"2026-08-24T09:45:00Z","repo":"scharissis/other","issue":5,"pr":7,"reason":"implement","session":"s5","status":"ok","subtype":"success","outcome":"opened_pr","turns":25,"tool_uses":22,"wall_ms":2700000,"api_ms":2000000,"cost_usd":1.00,"usage_source":"result","tokens":{"in":900,"out":12000,"cache_read":1500000,"cache_write":80000},"model":"claude-sonnet-5","tag":"terse-plan"}
{"v":1,"kind":"issue","ts":"2026-08-24T11:03:11Z","repo":"scharissis/other","issue":5,"pr":7,"outcome":"merged","tag":"terse-plan"}
`

// fixtureNow is after every fixture timestamp, so -since windows are the same
// on every machine on every day.
var fixtureNow = time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)

func fixtureDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{
		"scharissis--polako.jsonl": fixtureMain,
		"scharissis--other.jsonl":  fixtureOther,
		// Not a record file: the reader globs *.jsonl, and anything else in
		// the directory is none of its business.
		"notes.txt": "nothing to do with run data",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(strings.TrimPrefix(body, "\n")), 0o600); err != nil {
			t.Fatalf("writing fixture %s: %v", name, err)
		}
	}
	return dir
}

// flat collapses whitespace, so an assertion about one line survives the
// column widths shifting when a longer label joins its section.
func flat(s string) string { return strings.Join(strings.Fields(s), " ") }

func hasLine(out, want string) bool { return strings.Contains(flat(out), flat(want)) }

func stats(t *testing.T, args ...string) string {
	t.Helper()
	clearEnvDefaults(t)
	var buf bytes.Buffer
	if err := runStats(args, &buf, fixtureNow); err != nil {
		t.Fatalf("stats %v: %v", args, err)
	}
	return buf.String()
}

// clearEnvDefaults keeps a test hermetic against the shell it runs in. Flags
// take their defaults from POLAKO_* now, so a maintainer who set one in
// their profile would otherwise be running a different suite from CI.
func clearEnvDefaults(t *testing.T) {
	t.Helper()
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if !strings.HasPrefix(name, envPrefix) {
			continue
		}
		t.Setenv(name, "") // registers the restore this test needs
		os.Unsetenv(name)  // then actually takes it out of the environment
	}
}

// One variable points both halves at the same directory: the drain writes
// there, and stats reads there, without either being told twice.
func TestStatsReadsTheMetricsDirectoryFromTheEnvironment(t *testing.T) {
	clearEnvDefaults(t)
	t.Setenv("POLAKO_METRICS", fixtureDir(t))

	var buf bytes.Buffer
	if err := runStats(nil, &buf, fixtureNow); err != nil {
		t.Fatalf("stats: %v", err)
	}
	if !hasLine(buf.String(), "terminal 4 — merged 3 (75%)") {
		t.Errorf("stats did not read the directory the environment named:\n%s", buf.String())
	}
	// An argument is a decision about this run, and beats the preference.
	var override bytes.Buffer
	if err := runStats([]string{"-metrics", t.TempDir()}, &override, fixtureNow); err != nil {
		t.Fatalf("stats: %v", err)
	}
	if !strings.Contains(override.String(), "no run data in") {
		t.Errorf("-metrics did not override the environment:\n%s", override.String())
	}
}

// The whole report, pinned. Every number here was worked out by hand from the
// fixture above: if a change moves one, the change has to be able to say why.
func TestStatsReport(t *testing.T) {
	dir := fixtureDir(t)
	want := fmt.Sprintf(`run data from %s
  read    2 files, 11 records (1 unreadable line skipped)
  window  2026-08-20T09:00:00Z → 2026-08-24T11:03:11Z (4.1d)
  repos   scharissis/other, scharissis/polako

issues
  terminal          4 — merged 3 (75%%), needs human 1
  in flight         1
  runs per issue    1.5 mean, 1.5 median
  cost per issue    $1.92 mean, $2.00 median
  tokens per issue  4.6M mean, 3.9M median (in 2.2k, out 35.2k, cache read 4.4M, cache write 220.5k)

runs
  total         7 — ok 5, no-turns 1, crash 1
  reasons       implement 4, resume 1, answers 1, remediate 1
  outcomes      opened pr 3, posted questions 1, nothing 3
  work          131 turns, 115 tool uses
  approximated  1 of 7 runs priced from the streamed tally, not a result event

cost
  total          $8.10 over 4.1d ($1.98/day)
  per merged PR  $2.70 across 3 merges
  tokens         19.1M (in 9.4k, out 145.9k, cache read 18.1M, cache write 912k)

human latency
  blocked on answers  1 span — 3h10m median, 3h10m max
  pr open to merge    3 spans — 1h20m median, 2h max (human availability, not the tool)
`, dir)

	if got := stats(t, "-metrics", dir); got != want {
		t.Errorf("report differs\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// The duplicate is not hypothetical: the supervisor can be killed between a
// merge and the record of it, and rerun later. The newest line is the one that
// happened — here, merged, not the needs_human that preceded it.
func TestStatsDedupesIssueRecordsLatestWins(t *testing.T) {
	out := stats(t, "-metrics", fixtureDir(t), "-by", byIssue)
	line := issueLine(t, out, "scharissis/polako#12")
	if !strings.Contains(line, "merged") {
		t.Errorf("issue #12 = %q, want the later merged record to win", line)
	}
	if strings.Contains(line, "needs human") {
		t.Errorf("issue #12 = %q, want the superseded record dropped", line)
	}
	// Both records must not be counted: four issues reached a terminal state.
	if !hasLine(out, "terminal 4 — merged 3 (75%)") {
		t.Errorf("terminal counts double-count a superseded record:\n%s", out)
	}
}

func issueLine(t *testing.T, out, prefix string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), prefix) {
			return strings.TrimSpace(line)
		}
	}
	t.Fatalf("no line for %s in:\n%s", prefix, out)
	return ""
}

// A crashed run and the resume that finished the work are two records of one
// stretch of work. Both count — dropping the crash would price the crashy
// configurations at nothing.
func TestStatsCountsBothHalvesOfAResumedSession(t *testing.T) {
	out := stats(t, "-metrics", fixtureDir(t), "-by", byIssue)
	line := issueLine(t, out, "scharissis/polako#13")
	if !strings.Contains(line, "  2  ") {
		t.Errorf("issue #13 = %q, want both the crash and the resume counted", line)
	}
	// Summed as written, with nothing subtracted for overlap and no note
	// warning of any: a --resume'd result event reports its own invocation and
	// not the session, settled on issue #78. $0.00 for the crash, $3.00 for the
	// resume, and the report says neither more nor less than that.
	if !strings.Contains(line, "$3.00") {
		t.Errorf("issue #13 = %q, want the resume's cost summed exactly as recorded", line)
	}
	if strings.Contains(out, "resumed an earlier session") {
		t.Errorf("the resumed-cost caveat is settled and should be gone:\n%s", out)
	}
}

func TestStatsFiltersByRepo(t *testing.T) {
	out := stats(t, "-metrics", fixtureDir(t), "-repo", "scharissis/other")
	if strings.Contains(out, "polako#") || strings.Contains(out, "$8.10") {
		t.Errorf("-repo let another repository's records through:\n%s", out)
	}
	for _, want := range []string{"filtered for scharissis/other", "total $1.00"} {
		if !hasLine(out, want) {
			t.Errorf("-repo report missing %q:\n%s", want, out)
		}
	}
	// Repository names are not case-sensitive on GitHub, and neither is this.
	if stats(t, "-metrics", fixtureDir(t), "-repo", "Scharissis/Other") == "" {
		t.Error("-repo should match regardless of case")
	}
}

func TestStatsFiltersBySince(t *testing.T) {
	dir := fixtureDir(t)
	// fixtureNow is 2026-08-25T09:00Z, so 30h reaches back to the 24th only.
	out := stats(t, "-metrics", dir, "-since", "30h")
	if !hasLine(out, "filtered in the last 30h") {
		t.Errorf("the window should be stated in the report:\n%s", out)
	}
	if strings.Contains(out, "resumed an earlier session") {
		t.Errorf("-since let older records through:\n%s", out)
	}
	if !hasLine(out, "total 1 — ok 1") {
		t.Errorf("want only the one run inside the window:\n%s", out)
	}
	// A window that reaches back before every record is the same as no window.
	if long, all := stats(t, "-metrics", dir, "-since", "8760h"), stats(t, "-metrics", dir); !sameBody(long, all) {
		t.Errorf("a window covering everything should report everything\n%s", long)
	}
}

// sameBody compares two reports ignoring the "filtered" line, which is the one
// thing a window is expected to change.
func sameBody(a, b string) bool {
	strip := func(s string) string {
		var kept []string
		for _, line := range strings.Split(s, "\n") {
			if !strings.Contains(line, "filtered") {
				kept = append(kept, flat(line))
			}
		}
		return strings.Join(kept, "\n")
	}
	return strip(a) == strip(b)
}

func TestStatsByIssue(t *testing.T) {
	out := stats(t, "-metrics", fixtureDir(t), "-by", byIssue)
	want := `by issue
  issue                 outcome      runs  questions   cost  tokens  wall
  scharissis/other#5    merged          1          0  $1.00    1.6M   45m
  scharissis/polako#12  merged          2          1  $3.60   10.6M   50m
  scharissis/polako#13  merged          2          0  $3.00    6.3M   39m
  scharissis/polako#14  needs human     1          0  $0.10     53k   30m
  scharissis/polako#15  in flight       1          0  $0.40  635.4k   12m
`
	if !strings.Contains(out, want) {
		t.Errorf("-by issue table differs\n--- got ---\n%s\n--- want ---\n%s", out, want)
	}
}

// The point of the whole exercise: price one configuration against another.
func TestStatsByModelAndTag(t *testing.T) {
	dir := fixtureDir(t)
	wantModel := `by model
  model            issues  merged  runs   cost  $/merged  tokens
  claude-opus-5         3       2     5  $7.00     $3.50   17.5M
  claude-sonnet-5       2       1     2  $1.10     $1.10    1.6M
`
	if got := stats(t, "-metrics", dir, "-by", byModel); !strings.Contains(got, wantModel) {
		t.Errorf("-by model table differs\n--- got ---\n%s\n--- want ---\n%s", got, wantModel)
	}
	wantTag := `by tag
  tag         issues  merged  runs   cost  $/merged  tokens
  baseline         3       2     5  $6.70     $3.35   16.9M
  terse-plan       2       1     2  $1.40     $1.40    2.2M
`
	if got := stats(t, "-metrics", dir, "-by", byTag); !strings.Contains(got, wantTag) {
		t.Errorf("-by tag table differs\n--- got ---\n%s\n--- want ---\n%s", got, wantTag)
	}
}

// The ledger every other section is a rollup of, and the only place a run's
// session id is ever shown. Pinned whole: the columns are the interface.
func TestStatsRunLog(t *testing.T) {
	out := stats(t, "-metrics", fixtureDir(t), "-runs")
	want := `run log
  started               issue                 reason     status    outcome           session  attempt   cost  tokens  wall
  2026-08-20T09:00:00Z  scharissis/polako#12  implement  ok        posted questions  s12a           0  $1.10    4.2M   20m
  2026-08-20T12:30:00Z  scharissis/polako#12  answers    ok        opened pr         s12b           0  $2.50    6.4M   30m
  2026-08-21T09:00:00Z  scharissis/polako#13  implement  crash     nothing           s13a           0  $0.00  746.5k    5m
  2026-08-21T09:06:00Z  scharissis/polako#13  resume     ok        opened pr         s13a           1  $3.00    5.5M   34m
  2026-08-22T09:00:00Z  scharissis/polako#14  implement  no-turns  nothing           —              0  $0.10     53k   30m
  2026-08-23T09:00:00Z  scharissis/polako#15  remediate  ok        nothing           —              0  $0.40  635.4k   12m
  2026-08-24T09:00:00Z  scharissis/other#5    implement  ok        opened pr         s5             0  $1.00    1.6M   45m
`
	if !strings.Contains(out, want) {
		t.Errorf("run log differs\n--- got ---\n%s\n--- want ---\n%s", out, want)
	}
	// The crash and the resume that finished its work carry one session id
	// between them: that is the row-to-row chain the whole table exists for.
	if strings.Count(out, "s13a") != 2 {
		t.Errorf("the resume should name the session it continued:\n%s", out)
	}
	// Off by default — the report is a summary, and seven rows of ledger in it
	// unasked would bury the numbers above them.
	if plain := stats(t, "-metrics", fixtureDir(t)); strings.Contains(plain, "run log") {
		t.Errorf("-runs should be opt-in:\n%s", plain)
	}
}

// The same filters as everything else, because it reads the same already
// filtered slice — asserted rather than assumed, since a table that quietly
// ignored -repo would be a report about the wrong project.
func TestStatsRunLogHonoursTheFilters(t *testing.T) {
	dir := fixtureDir(t)

	one := stats(t, "-metrics", dir, "-runs", "-repo", "scharissis/other")
	if strings.Contains(one, "scharissis/polako#") {
		t.Errorf("-repo let another repository's runs into the log:\n%s", one)
	}
	if !hasLine(one, "2026-08-24T09:00:00Z scharissis/other#5 implement ok opened pr s5 0 $1.00 1.6M 45m") {
		t.Errorf("the one run in scope should still be listed:\n%s", one)
	}

	// 30h back from fixtureNow reaches the 24th only.
	windowed := stats(t, "-metrics", dir, "-runs", "-since", "30h")
	if strings.Contains(windowed, "2026-08-20") {
		t.Errorf("-since let older runs into the log:\n%s", windowed)
	}

	// A window can keep an issue's terminal record while clipping every run
	// that produced it — the one way this table has a heading and no rows.
	// 71h45m back from fixtureNow lands between o/o's run and its merge.
	clipped := stats(t, "-metrics", drainFixtureDir(t), "-runs", "-since", "71h45m")
	if !hasLine(clipped, "run log (no runs in this window)") {
		t.Errorf("want the table to say it is empty rather than vanish:\n%s", clipped)
	}
}

// The drain fixture is what a real directory looks like the first time -drain
// is used: two processes that both worked issue #1 and disagreed about how it
// ended, the records a version before ids left behind, and a third drain on
// another repository whose records are the newest of the lot.
const fixtureShiftsMain = `
{"v":1,"kind":"run","ts":"2026-08-19T09:00:00Z","ended":"2026-08-19T09:10:00Z","repo":"r/r","issue":2,"pr":1,"reason":"implement","status":"ok","outcome":"opened_pr","cost_usd":0.50,"tokens":{"cache_read":500000},"model":"claude-opus-5"}
{"v":1,"kind":"issue","ts":"2026-08-19T09:30:00Z","repo":"r/r","issue":2,"pr":1,"outcome":"merged"}
{"v":1,"kind":"run","ts":"2026-08-20T09:00:00Z","ended":"2026-08-20T09:10:00Z","shift":"aaaa1111","repo":"r/r","issue":1,"pr":2,"reason":"implement","status":"ok","outcome":"opened_pr","cost_usd":1.00,"tokens":{"cache_read":1000000},"model":"claude-opus-5"}
{"v":1,"kind":"issue","ts":"2026-08-20T09:20:00Z","shift":"aaaa1111","repo":"r/r","issue":1,"pr":2,"outcome":"needs_human"}
{"v":1,"kind":"run","ts":"2026-08-21T09:00:00Z","ended":"2026-08-21T09:30:00Z","shift":"bbbb2222","repo":"r/r","issue":1,"pr":3,"reason":"implement","status":"ok","outcome":"opened_pr","cost_usd":2.00,"tokens":{"cache_read":2000000},"model":"claude-opus-5"}
{"v":1,"kind":"issue","ts":"2026-08-21T10:00:00Z","shift":"bbbb2222","repo":"r/r","issue":1,"pr":3,"outcome":"merged"}
`

const fixtureShiftsOther = `
{"v":1,"kind":"run","ts":"2026-08-22T09:00:00Z","ended":"2026-08-22T09:10:00Z","shift":"cccc3333","repo":"o/o","issue":9,"pr":4,"reason":"implement","status":"ok","outcome":"opened_pr","cost_usd":3.00,"tokens":{"cache_read":3000000},"model":"claude-opus-5"}
{"v":1,"kind":"issue","ts":"2026-08-22T09:30:00Z","shift":"cccc3333","repo":"o/o","issue":9,"pr":4,"outcome":"merged"}
`

func drainFixtureDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{
		"r--r.jsonl": fixtureShiftsMain,
		"o--o.jsonl": fixtureShiftsOther,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(strings.TrimPrefix(body, "\n")), 0o600); err != nil {
			t.Fatalf("writing fixture %s: %v", name, err)
		}
	}
	return dir
}

func TestStatsByShift(t *testing.T) {
	out := stats(t, "-metrics", drainFixtureDir(t), "-by", byShift)
	want := `by shift
  shift     issues  merged  runs   cost  $/merged  tokens
  cccc3333       1       1     1  $3.00     $3.00      3M
  bbbb2222       1       1     1  $2.00     $2.00      2M
  aaaa1111       1       1     1  $1.00     $1.00      1M
  (none)         1       1     1  $0.50     $0.50    500k
`
	if !strings.Contains(out, want) {
		t.Errorf("-by shift table differs\n--- got ---\n%s\n--- want ---\n%s", out, want)
	}
}

// Records written before the field existed are not a parse failure and not a
// gap: they are one more drain nobody can name.
func TestStatsGroupsRecordsWithNoShiftIDUnderNone(t *testing.T) {
	out := stats(t, "-metrics", fixtureDir(t), "-by", byShift)
	if !hasLine(out, "(none) 5 3 7") {
		t.Errorf("a file written before drain ids existed should still group, whole:\n%s", out)
	}
	// And it is selectable, so a row of that table can be typed straight back
	// in rather than being a dead end.
	if got := stats(t, "-metrics", fixtureDir(t), "-shift", noneGroup); !hasLine(got, "total 7 — ok 5") {
		t.Errorf("-shift %s should select exactly the records with no id:\n%s", noneGroup, got)
	}
}

// The filter has to run before the latest-wins dedupe of terminal records:
// both drains here recorded issue #1 ending, and the older one's report must
// show what that drain concluded rather than what the next one did.
func TestStatsFiltersToOneShiftBeforeDedupingIssues(t *testing.T) {
	dir := drainFixtureDir(t)

	older := stats(t, "-metrics", dir, "-shift", "aaaa1111")
	for _, want := range []string{
		"filtered from shift aaaa1111",
		"terminal 1 — merged 0 (0%), needs human 1",
		"total $1.00",
	} {
		if !hasLine(older, want) {
			t.Errorf("-shift aaaa1111 is missing %q:\n%s", want, older)
		}
	}

	newer := stats(t, "-metrics", dir, "-shift", "bbbb2222")
	if !hasLine(newer, "terminal 1 — merged 1 (100%)") || !hasLine(newer, "total $2.00") {
		t.Errorf("-shift bbbb2222 should report only its own work:\n%s", newer)
	}
}

// "last" is the flag's whole point on a drain that is still running: the id is
// in a startup line scrolled off the screen an hour ago.
func TestStatsShiftLastPicksTheNewestShiftInScope(t *testing.T) {
	dir := drainFixtureDir(t)

	// The report names the id it resolved to, not the word that was typed —
	// so a report pasted somewhere still says which drain it covered.
	last := stats(t, "-metrics", dir, "-shift", shiftLast)
	if !hasLine(last, "filtered from shift cccc3333") || !hasLine(last, "total $3.00") {
		t.Errorf(`-shift last should be the newest drain of all:\n%s`, last)
	}
	// In scope, not overall: the last drain to touch one repository is a
	// different question from the last drain anywhere.
	perRepo := stats(t, "-metrics", dir, "-shift", shiftLast, "-repo", "r/r")
	if !hasLine(perRepo, "filtered for r/r from shift bbbb2222") || !hasLine(perRepo, "total $2.00") {
		t.Errorf("-shift last should resolve inside -repo:\n%s", perRepo)
	}
}

// -shift narrows the records -since kept; it does not replace the window.
// Asserting that on "last" is impossible — the newest record is by
// construction never the one a window clips — so it takes a named drain whose
// records sit outside it.
func TestStatsShiftAndSinceBothApply(t *testing.T) {
	// 72h back from fixtureNow starts on the 22nd; every aaaa1111 record is
	// from the 20th.
	out := stats(t, "-metrics", drainFixtureDir(t), "-shift", "aaaa1111", "-since", "72h")
	if !strings.Contains(out, "no run data in") {
		t.Errorf("a window excluding the drain's records should report nothing:\n%s", out)
	}
	if !hasLine(out, "in the last 3d from shift aaaa1111") {
		t.Errorf("the empty report should name both filters, not just one:\n%s", out)
	}
}

// An id nobody wrote is not an error — it is a report with nothing in it, and
// the line has to say which drain came up empty.
func TestStatsOnAShiftWithNoRecords(t *testing.T) {
	out := stats(t, "-metrics", drainFixtureDir(t), "-shift", "deadbeef")
	if !strings.Contains(out, "from shift deadbeef") {
		t.Errorf("want the empty report to name the drain asked for:\n%s", out)
	}
}

// Batches are normally one tag each, so an issue worked under two of them is
// counted under both — and said so, rather than quietly inflating a column.
func TestStatsNotesIssuesSpanningGroups(t *testing.T) {
	dir := t.TempDir()
	body := `{"v":1,"kind":"run","ts":"2026-08-20T09:00:00Z","ended":"2026-08-20T09:10:00Z","repo":"r/r","issue":1,"reason":"implement","status":"ok","outcome":"nothing","cost_usd":1,"tag":"before"}
{"v":1,"kind":"run","ts":"2026-08-20T10:00:00Z","ended":"2026-08-20T10:10:00Z","repo":"r/r","issue":1,"reason":"implement","status":"ok","outcome":"opened_pr","pr":2,"cost_usd":2,"tag":"after"}
{"v":1,"kind":"issue","ts":"2026-08-20T11:00:00Z","repo":"r/r","issue":1,"pr":2,"outcome":"merged","tag":"after"}
`
	if err := os.WriteFile(filepath.Join(dir, "r--r.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	out := stats(t, "-metrics", dir, "-by", byTag)
	if !strings.Contains(out, "(1 issue spans more than one tag, and is counted under each)") {
		t.Errorf("want the overlap stated outright:\n%s", out)
	}
	// The headline must still count that issue once.
	if !hasLine(out, "terminal 1 — merged 1 (100%)") {
		t.Errorf("the overlap must not double-count the issue itself:\n%s", out)
	}
}

func TestStatsOnAnEmptyDirectory(t *testing.T) {
	out := stats(t, "-metrics", filepath.Join(t.TempDir(), "never-drained"))
	if !strings.Contains(out, "no run data in") {
		t.Errorf("want a plain answer, got:\n%s", out)
	}
}

// A run record on its own is a complete report: an issue in flight has no
// terminal record yet, and every rate has to cope with that.
func TestStatsWithNoTerminalIssueYet(t *testing.T) {
	dir := t.TempDir()
	body := `{"v":1,"kind":"run","ts":"2026-08-20T09:00:00Z","ended":"2026-08-20T09:10:00Z","repo":"r/r","issue":1,"reason":"implement","status":"ok","outcome":"opened_pr","pr":2,"cost_usd":1.5,"turns":9}
`
	if err := os.WriteFile(filepath.Join(dir, "r--r.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	out := stats(t, "-metrics", dir)
	for _, want := range []string{"none yet", "in flight 1", "total $1.50"} {
		if !hasLine(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// Nothing merged, so there is no per-merge price to quote — and inventing
	// one by dividing by zero would be worse than leaving it out.
	if strings.Contains(out, "per merged PR") {
		t.Errorf("want no per-merge line when nothing merged:\n%s", out)
	}
}

func TestStatsRejectsBadInput(t *testing.T) {
	cases := map[string][]string{
		"an unknown -by":     {"-by", "sideways"},
		"a stray argument":   {"stats-again"},
		"-metrics off":       {"-metrics", "off"},
		"an unknown flag":    {"-nope"},
		"an unparseable dur": {"-since", "forever"},
		"a negative -since":  {"-since", "-168h"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			clearEnvDefaults(t)
			var buf bytes.Buffer
			if err := runStats(args, &buf, fixtureNow); err == nil {
				t.Errorf("runStats(%v) succeeded, want an error explaining what to do", args)
			}
		})
	}
}

// -h is how a person finds the flags; it is not a failure.
func TestStatsHelpIsNotAnError(t *testing.T) {
	clearEnvDefaults(t)
	var buf bytes.Buffer
	if err := runStats([]string{"-h"}, &buf, fixtureNow); err != nil {
		t.Fatalf("stats -h: %v", err)
	}
	for _, want := range []string{"-by", "-repo", "-since", "-metrics"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("usage does not mention %s:\n%s", want, buf.String())
		}
	}
}

// --- the derivations, unit by unit ---

func TestAnswerSpansPairEachRoundWithItsReply(t *testing.T) {
	// The middle run does both: it folds in one reply and asks again.
	is := &issueStats{runs: []runRecord{
		{TS: "2026-08-20T09:00:00Z", Ended: "2026-08-20T09:30:00Z", Reason: reasonImplement, Outcome: outcomeQuestions},
		{TS: "2026-08-20T11:30:00Z", Ended: "2026-08-20T12:00:00Z", Reason: reasonAnswers, Outcome: outcomeQuestions},
		{TS: "2026-08-20T13:00:00Z", Ended: "2026-08-20T14:00:00Z", Reason: reasonAnswers, Outcome: outcomeOpenedPR},
	}}
	got := answerSpans(is)
	want := []time.Duration{2 * time.Hour, time.Hour}
	if len(got) != len(want) {
		t.Fatalf("spans = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("span %d = %s, want %s", i, got[i], want[i])
		}
	}
}

// Records are ordered by timestamp, never by attempt: attempt restarts at zero
// every time the supervisor does, so a chronology built on it is fiction.
func TestStatsOrdersRunsByTimestampNotAttempt(t *testing.T) {
	dir := t.TempDir()
	// Written out of order, with attempt counters that disagree with the clock.
	body := `{"v":1,"kind":"run","ts":"2026-08-20T13:00:00Z","ended":"2026-08-20T13:30:00Z","repo":"r/r","issue":1,"reason":"answers","attempt":0,"status":"ok","outcome":"opened_pr","pr":2,"cost_usd":1}
{"v":1,"kind":"run","ts":"2026-08-20T09:00:00Z","ended":"2026-08-20T09:30:00Z","repo":"r/r","issue":1,"reason":"implement","attempt":2,"status":"ok","outcome":"posted_questions","cost_usd":1}
`
	if err := os.WriteFile(filepath.Join(dir, "r--r.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	ds, err := loadRecords(dir, statsOptions{}, fixtureNow)
	if err != nil {
		t.Fatalf("loading: %v", err)
	}
	is := rollUpIssues(ds)[0]
	if is.runs[0].Reason != reasonImplement {
		t.Fatalf("runs are in %q order, want the earliest timestamp first", is.runs[0].Reason)
	}
	// Ordered wrongly, this span would be negative and silently dropped.
	if got := answerSpans(is); len(got) != 1 || got[0] != 3*time.Hour+30*time.Minute {
		t.Errorf("spans = %v, want one span of 3h30m", got)
	}
}

func TestMergeSpanIgnoresAnAbandonedPR(t *testing.T) {
	merged := issueRecord{TS: "2026-08-20T15:00:00Z", PR: 34, Outcome: issueMerged}
	is := &issueStats{
		terminal: &merged,
		runs: []runRecord{
			{TS: "2026-08-19T09:00:00Z", Ended: "2026-08-19T09:30:00Z", Outcome: outcomeOpenedPR, PR: 30},
			{TS: "2026-08-20T13:00:00Z", Ended: "2026-08-20T14:00:00Z", Outcome: outcomeOpenedPR, PR: 34},
		},
	}
	got, ok := mergeSpan(is)
	if !ok || got != time.Hour {
		t.Errorf("mergeSpan = %s (%v), want 1h measured from the PR that merged", got, ok)
	}
	// An issue that did not merge has no span to report.
	closed := issueRecord{TS: "2026-08-20T15:00:00Z", PR: 34, Outcome: issueClosed}
	if _, ok := mergeSpan(&issueStats{terminal: &closed, runs: is.runs}); ok {
		t.Error("a closed-unmerged issue has no open-to-merge span")
	}
}

func TestLoadRecordsSkipsWhatItCannotUse(t *testing.T) {
	dir := t.TempDir()
	body := "not json at all\n" +
		`{"v":1,"kind":"run","ts":"2026-08-20T09:00:00Z","repo":"r/r","issue":1,"cost_usd":1}` + "\n" +
		"\n" + // a blank line is not a torn one
		`{"v":1,"kind":"telemetry-of-the-future","ts":"2026-08-20T09:00:00Z"}` + "\n" +
		`{"v":1,"kind":"run","ts":"2026-08-20T09:30:00Z","repo":"r/r","issue":1,"tokens":"not an object"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "r--r.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	ds, err := loadRecords(dir, statsOptions{}, fixtureNow)
	if err != nil {
		t.Fatalf("loading: %v", err)
	}
	if len(ds.runs) != 1 {
		t.Errorf("kept %d runs, want the one that parsed", len(ds.runs))
	}
	// The junk line and the malformed record; the unknown kind is not damage,
	// it is a newer writer, and the blank line is nothing at all.
	if ds.skipped != 2 {
		t.Errorf("skipped %d lines, want 2", ds.skipped)
	}
}

func TestCountReadsAtAGlance(t *testing.T) {
	cases := map[int64]string{
		0: "0", 999: "999", 1000: "1k", 8123400: "8.1M", 1_500_000_000: "1.5G", 53000: "53k",
	}
	for n, want := range cases {
		if got := count(n); got != want {
			t.Errorf("count(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestDurDropsTheNoiseUnits(t *testing.T) {
	cases := map[time.Duration]string{
		30 * time.Second:                            "30s",
		90 * time.Second:                            "1m30s",
		2 * time.Hour:                               "2h",
		3*time.Hour + 10*time.Minute:                "3h10m",
		4*24*time.Hour + 2*time.Hour:                "4.1d",
		time.Hour + 18*time.Minute + 11*time.Second: "1h18m",
	}
	for d, want := range cases {
		if got := dur(d); got != want {
			t.Errorf("dur(%s) = %q, want %q", d, got, want)
		}
	}
}

func TestMedianAndMean(t *testing.T) {
	if got := median([]float64{3.60, 0.10, 3.00, 1.00}); got != 2.00 {
		t.Errorf("median of an even set = %v, want the midpoint 2", got)
	}
	if got := median([]int{5, 1, 3}); got != 3 {
		t.Errorf("median of an odd set = %v, want 3", got)
	}
	if got := median([]time.Duration{}); got != 0 {
		t.Errorf("median of nothing = %v, want the zero value", got)
	}
	if got := mean([]float64{1, 2, 4}); got != 7.0/3.0 {
		t.Errorf("mean = %v, want 7/3", got)
	}
}

// The drain writes; stats reads. Neither reaches for the other's job, which is
// what keeps run data telemetry rather than state.
func TestStatsNeverWrites(t *testing.T) {
	dir := fixtureDir(t)
	before, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading the directory: %v", err)
	}
	stats(t, "-metrics", dir, "-by", byIssue)
	after, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading the directory: %v", err)
	}
	if len(before) != len(after) {
		t.Errorf("stats changed the directory: %d entries before, %d after", len(before), len(after))
	}
}

// One issue worked under three tags is one issue spanning groups, not two —
// the surplus memberships add up faster than the issues do.
func TestStatsCountsSpanningIssuesNotSurplusMemberships(t *testing.T) {
	dir := t.TempDir()
	var body strings.Builder
	for i, tag := range []string{"a", "b", "c"} {
		fmt.Fprintf(&body, `{"v":1,"kind":"run","ts":"2026-08-20T0%d:00:00Z","ended":"2026-08-20T0%d:30:00Z","repo":"r/r","issue":1,"reason":"implement","status":"ok","outcome":"nothing","cost_usd":1,"tag":"%s"}`+"\n", i+1, i+1, tag)
	}
	if err := os.WriteFile(filepath.Join(dir, "r--r.jsonl"), []byte(body.String()), 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	out := stats(t, "-metrics", dir, "-by", byTag)
	if !strings.Contains(out, "(1 issue spans more than one tag") {
		t.Errorf("one issue across three tags is one spanning issue:\n%s", out)
	}
}

// A -since window can keep an issue's terminal record and drop every run that
// produced it. Averaging that issue in at $0 is how a filter turns expensive
// work into a cheap-looking batch.
func TestStatsDoesNotPriceIssuesWithNoRunsInScope(t *testing.T) {
	dir := t.TempDir()
	body := `{"v":1,"kind":"run","ts":"2026-08-20T09:00:00Z","ended":"2026-08-20T18:00:00Z","repo":"r/r","issue":1,"reason":"implement","status":"ok","outcome":"opened_pr","pr":2,"cost_usd":9.99,"turns":50}
{"v":1,"kind":"issue","ts":"2026-08-25T08:00:00Z","repo":"r/r","issue":1,"pr":2,"outcome":"merged"}
{"v":1,"kind":"run","ts":"2026-08-25T07:00:00Z","ended":"2026-08-25T07:30:00Z","repo":"r/r","issue":2,"reason":"implement","status":"ok","outcome":"opened_pr","pr":3,"cost_usd":2.00,"turns":10}
{"v":1,"kind":"issue","ts":"2026-08-25T08:30:00Z","repo":"r/r","issue":2,"pr":3,"outcome":"merged"}
`
	if err := os.WriteFile(filepath.Join(dir, "r--r.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	// fixtureNow is 2026-08-25T09:00Z: issue 1 merged inside the window but ran
	// well outside it; issue 2 is wholly inside.
	out := stats(t, "-metrics", dir, "-since", "24h")
	if hasLine(out, "cost per issue $0.00") {
		t.Errorf("an issue whose runs are out of scope must not be priced at zero:\n%s", out)
	}
	if !hasLine(out, "cost per issue $2.00 mean, $2.00 median (over the 1 with runs in this window)") {
		t.Errorf("want the priced issue alone, and the denominator stated:\n%s", out)
	}
	// Both still reached a terminal state; only the pricing denominator shrank.
	if !hasLine(out, "terminal 2 — merged 2 (100%)") {
		t.Errorf("the merge rate counts every terminal record:\n%s", out)
	}
	// A merge whose runs were clipped away adds nothing to the total, so it
	// must not be in the denominator that prices the tool either.
	if !hasLine(out, "per merged PR $2.00 across 1 merge") {
		t.Errorf("want the per-merge price over merges with runs in scope:\n%s", out)
	}
	// And when nothing at all can be priced, say so rather than printing zeros.
	// A 1h window from fixtureNow keeps both issue records and no runs at all.
	only := stats(t, "-metrics", dir, "-since", "1h")
	if !hasLine(only, "per issue nothing to price") {
		t.Errorf("want an explicit refusal to price, got:\n%s", only)
	}
	if hasLine(only, "per merged PR") {
		t.Errorf("nothing priceable means no per-merge figure either:\n%s", only)
	}
}

// The README suggests a shared -metrics directory for a team, and records are
// written 0600 — so someone else's file is normally unreadable. Reporting
// nothing at all in that case would make the shared setup useless.
func TestStatsReportsAroundAnUnreadableFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits are not how Windows decides this")
	}
	if os.Geteuid() == 0 {
		t.Skip("root reads everything, so there is no unreadable file to make")
	}
	dir := fixtureDir(t)
	blocked := filepath.Join(dir, "scharissis--other.jsonl")
	if err := os.Chmod(blocked, 0o000); err != nil {
		t.Fatalf("making a file unreadable: %v", err)
	}
	t.Cleanup(func() { os.Chmod(blocked, 0o600) })

	out := stats(t, "-metrics", dir)
	if !hasLine(out, "could not open 1 file — scharissis--other.jsonl") {
		t.Errorf("want the unreadable file named, not swallowed:\n%s", out)
	}
	// The readable file still reports in full.
	if !hasLine(out, "terminal 3 — merged 2 (67%), needs human 1") {
		t.Errorf("the readable records must still count:\n%s", out)
	}
}

// Naming the record file rather than its directory finds nothing, and "no run
// data" would send someone hunting for records in the path they just named.
func TestStatsRejectsAFileAsTheMetricsDirectory(t *testing.T) {
	dir := fixtureDir(t)
	var buf bytes.Buffer
	err := runStats([]string{"-metrics", filepath.Join(dir, "scharissis--other.jsonl")}, &buf, fixtureNow)
	if err == nil {
		t.Fatalf("naming a file succeeded, reporting:\n%s", buf.String())
	}
	if !strings.Contains(err.Error(), "is a file, not a directory") {
		t.Errorf("error = %v, want it to name the mistake and the fix", err)
	}
}

// Enriched terminal records: what GitHub said each PR turned out to be. The
// run records deliberately imply a different open-to-merge span, so the report
// proves it takes GitHub's timestamps over its own inference.
const fixtureEnriched = `{"v":1,"kind":"run","ts":"2026-08-20T12:00:00Z","ended":"2026-08-20T13:00:00Z","repo":"r/r","issue":20,"pr":40,"reason":"implement","status":"ok","outcome":"opened_pr","turns":10,"wall_ms":3600000,"cost_usd":2.00,"usage_source":"result","tokens":{"in":100,"out":1000},"model":"claude-opus-5"}
{"v":1,"kind":"issue","ts":"2026-08-20T15:00:00Z","repo":"r/r","issue":20,"pr":40,"outcome":"merged","additions":412,"deletions":38,"changed_files":7,"reviews":2,"pr_opened":"2026-08-20T10:00:00Z","pr_merged":"2026-08-20T14:30:00Z"}
{"v":1,"kind":"run","ts":"2026-08-21T08:00:00Z","ended":"2026-08-21T08:30:00Z","repo":"r/r","issue":21,"pr":41,"reason":"implement","status":"ok","outcome":"opened_pr","turns":8,"wall_ms":1800000,"cost_usd":1.00,"usage_source":"result","tokens":{"in":100,"out":900},"model":"claude-opus-5"}
{"v":1,"kind":"issue","ts":"2026-08-21T12:00:00Z","repo":"r/r","issue":21,"pr":41,"outcome":"merged","additions":200,"deletions":20,"changed_files":3,"pr_opened":"2026-08-21T09:00:00Z","pr_merged":"2026-08-21T10:30:00Z"}
`

func enrichedDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "r--r.jsonl"), []byte(fixtureEnriched), 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	return dir
}

func TestStatsReportsWhatTheMergedWorkChanged(t *testing.T) {
	out := stats(t, "-metrics", enrichedDir(t))
	if !hasLine(out, "change per issue +306 -29 across 5 files, 1 review (medians over 2 issues with PR data)") {
		t.Errorf("no change line built from the enrichment:\n%s", out)
	}
	// 4h30m and 1h30m, from GitHub's timestamps — not the 2h and 3h30m the
	// run records and terminal timestamps would have implied.
	if !hasLine(out, "pr open to merge 2 spans — 3h median, 4h30m max") {
		t.Errorf("open-to-merge spans are not GitHub's:\n%s", out)
	}
}

// Every record written before the enrichment existed still reads, and reports
// exactly what it did before: the line simply does not appear.
func TestStatsOmitsTheChangeLineWithoutEnrichment(t *testing.T) {
	out := stats(t, "-metrics", fixtureDir(t))
	if strings.Contains(out, "change per issue") {
		t.Errorf("unenriched records produced a change line:\n%s", out)
	}
}

func TestMergeSpanPrefersGitHubsTimestamps(t *testing.T) {
	merged := issueRecord{TS: "2026-08-20T15:00:00Z", PR: 34, Outcome: issueMerged,
		PROpened: "2026-08-20T09:00:00Z", PRMerged: "2026-08-20T14:00:00Z"}
	is := &issueStats{terminal: &merged, runs: []runRecord{
		{TS: "2026-08-20T12:00:00Z", Ended: "2026-08-20T13:00:00Z", Outcome: outcomeOpenedPR, PR: 34},
	}}
	if got, ok := mergeSpan(is); !ok || got != 5*time.Hour {
		t.Errorf("mergeSpan = %s (%v), want 5h from GitHub, not the 2h the records imply", got, ok)
	}
	// A record enriched with an opening but no merge — the supervisor recorded
	// the merge before GitHub had stamped it — falls back rather than guessing.
	half := merged
	half.PRMerged = ""
	if got, ok := mergeSpan(&issueStats{terminal: &half, runs: is.runs}); !ok || got != 2*time.Hour {
		t.Errorf("mergeSpan = %s (%v), want the 2h the run records imply", got, ok)
	}
}
