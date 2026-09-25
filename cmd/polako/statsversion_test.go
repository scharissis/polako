package main

import (
	"strings"
	"testing"
)

// Version order, not cost: 0.9.0 before 0.10.0 even though 0.10.0 cost more,
// (none) first as the oldest history, and a non-release string last. Issue #2
// ran on two versions, so it counts under each and the footnote says so.
const fixtureVersions = `{"v":1,"kind":"run","ts":"2026-09-01T09:00:00Z","repo":"r/r","issue":1,"reason":"implement","status":"ok","cost_usd":0.25}
{"v":1,"kind":"run","ts":"2026-09-10T09:00:00Z","repo":"r/r","issue":2,"reason":"implement","status":"ok","cost_usd":1,"plugin_version":"0.9.0"}
{"v":1,"kind":"run","ts":"2026-09-12T09:00:00Z","repo":"r/r","issue":2,"reason":"remediate","status":"ok","cost_usd":2,"plugin_version":"0.10.0"}
{"v":1,"kind":"run","ts":"2026-09-13T09:00:00Z","repo":"r/r","issue":3,"reason":"implement","status":"ok","cost_usd":5,"plugin_version":"0.10.0"}
{"v":1,"kind":"run","ts":"2026-09-14T09:00:00Z","repo":"r/r","issue":4,"reason":"implement","status":"ok","cost_usd":9,"plugin_version":"dev"}
{"v":1,"kind":"issue","ts":"2026-09-14T10:00:00Z","repo":"r/r","issue":3,"outcome":"merged"}
`

func TestStatsByVersionSortsOldestFirst(t *testing.T) {
	t.Parallel()
	out := stats(t, "-metrics", writeRecords(t, fixtureVersions), "-by", byVersion)
	want := `by version
  version  issues  merged  runs   cost  $/merged  tokens
  (none)        1       0     1  $0.25         —       0
  0.9.0         1       0     1  $1.00         —       0
  0.10.0        2       1     2  $7.00     $7.00       0
  dev           1       0     1  $9.00         —       0
  (1 issue spans more than one version, and is counted under each)
`
	if !hasLine(out, want) {
		t.Errorf("-by version table differs\n--- got ---\n%s\n--- want ---\n%s", out, want)
	}
}

func TestStatsByVersionInJSONAndHTML(t *testing.T) {
	t.Parallel()
	dir := writeRecords(t, fixtureVersions)
	var doc statsDoc
	mustUnmarshal(t, stats(t, "-metrics", dir, "-json", "-by", byVersion), &doc)
	if doc.By == nil || doc.By.Kind != byVersion {
		t.Fatalf("by = %+v, want kind %q", doc.By, byVersion)
	}
	var names []string
	for _, g := range doc.By.Groups {
		names = append(names, g.Name)
	}
	if got, want := strings.Join(names, " "), "(none) 0.9.0 0.10.0 dev"; got != want {
		t.Errorf("group order = %q, want %q", got, want)
	}
	if doc.By.Spanning != 1 {
		t.Errorf("spanning = %d, want 1", doc.By.Spanning)
	}

	page := htmlReportOf(t, dir, "-by", byVersion)
	for _, want := range []string{"by version", "0.10.0", "spans more than one version"} {
		if !strings.Contains(page, want) {
			t.Errorf("-html -by version page lacks %q", want)
		}
	}
}
