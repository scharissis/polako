package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func statsByTagMoves(t *testing.T, dir string) []statsDocTagMove {
	t.Helper()
	var doc struct {
		By statsDocBy `json:"by"`
	}
	out := stats(t, "-metrics", dir, "-json", "-by", "tag")
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("output did not parse as JSON: %v\n%s", err, out)
	}
	return doc.By.ModelMoved
}

// An inherit-only batch that ran on two models gets the note in all three
// outputs. The sonnet run beside it is a second request on its own model, so
// it adds nothing to the note and doesn't hide it either.
func TestStatsByTagNotesAModelThatMovedUnderABatch(t *testing.T) {
	t.Parallel()
	dir := writeRecords(t, `{"v":1,"kind":"run","ts":"2026-09-20T09:00:00Z","repo":"r/r","issue":1,"reason":"implement","status":"ok","tag":"baseline","model":"claude-sonnet-5"}
{"v":1,"kind":"run","ts":"2026-09-23T09:00:00Z","repo":"r/r","issue":2,"reason":"implement","status":"ok","tag":"baseline","model":"claude-opus-5-5[1m]"}
{"v":1,"kind":"run","ts":"2026-09-23T10:00:00Z","repo":"r/r","issue":2,"reason":"remediate","status":"ok","tag":"baseline","model":"claude-sonnet-5","requested_model":"sonnet"}
`)
	want := "(model moved under baseline: inherit ran on claude-sonnet-5 and claude-opus-5-5[1m])"
	if out := stats(t, "-metrics", dir, "-by", "tag"); !hasLine(out, want) {
		t.Errorf("want %q under the table in:\n%s", want, out)
	}
	if page := htmlReportOf(t, dir, "-by", "tag"); !strings.Contains(page, want) {
		t.Errorf("want %q as the HTML table's note", want)
	}
	got := statsByTagMoves(t, dir)
	wantDoc := []statsDocTagMove{{Tag: "baseline", Requested: "inherit", Models: []string{"claude-sonnet-5", "claude-opus-5-5[1m]"}}}
	if !reflect.DeepEqual(got, wantDoc) {
		t.Errorf("by.model_moved = %+v, want %+v", got, wantDoc)
	}
}

// Two requests, each on its own model, is a tier swap: the model is the
// change, so there is nothing to note. Untagged runs aren't a batch, and a
// resume's bare id is the [1m] flap, not a move.
func TestStatsByTagQuietWhenTheModelIsTheChange(t *testing.T) {
	t.Parallel()
	dir := writeRecords(t, `{"v":1,"kind":"run","ts":"2026-09-20T09:00:00Z","repo":"r/r","issue":1,"reason":"implement","status":"ok","tag":"plan-best","model":"claude-opus-5-5","requested_model":"opus"}
{"v":1,"kind":"run","ts":"2026-09-21T09:00:00Z","repo":"r/r","issue":2,"reason":"implement","status":"ok","tag":"plan-best","model":"claude-fable-5-1","requested_model":"best"}
{"v":1,"kind":"run","ts":"2026-09-20T09:00:00Z","repo":"r/r","issue":3,"reason":"implement","status":"ok","model":"claude-sonnet-5"}
{"v":1,"kind":"run","ts":"2026-09-23T09:00:00Z","repo":"r/r","issue":4,"reason":"implement","status":"ok","model":"claude-opus-5-5[1m]"}
{"v":1,"kind":"run","ts":"2026-09-23T09:00:00Z","repo":"r/r","issue":5,"reason":"implement","status":"crash","tag":"flap","model":"claude-opus-5-5[1m]"}
{"v":1,"kind":"run","ts":"2026-09-23T09:10:00Z","repo":"r/r","issue":5,"reason":"resume","status":"ok","tag":"flap","model":"claude-opus-5-5"}
{"v":1,"kind":"run","ts":"2026-09-23T11:00:00Z","repo":"r/r","issue":5,"reason":"checks","resumed_from":"s1","status":"ok","tag":"flap","model":"claude-opus-5-5"}
`)
	if out := stats(t, "-metrics", dir, "-by", "tag"); strings.Contains(out, "model moved") {
		t.Errorf("noted a move that isn't one:\n%s", out)
	}
	if page := htmlReportOf(t, dir, "-by", "tag"); strings.Contains(page, "model moved") {
		t.Errorf("the HTML table noted a move that isn't one")
	}
	if got := statsByTagMoves(t, dir); got != nil {
		t.Errorf("by.model_moved = %+v, want it absent", got)
	}
}

// The note is -by tag's alone: -by model already splits the models out.
func TestStatsByModelHasNoMoveNote(t *testing.T) {
	t.Parallel()
	dir := writeRecords(t, `{"v":1,"kind":"run","ts":"2026-09-20T09:00:00Z","repo":"r/r","issue":1,"reason":"implement","status":"ok","tag":"baseline","model":"claude-sonnet-5"}
{"v":1,"kind":"run","ts":"2026-09-23T09:00:00Z","repo":"r/r","issue":2,"reason":"implement","status":"ok","tag":"baseline","model":"claude-opus-5-5[1m]"}
`)
	if out := stats(t, "-metrics", dir, "-by", "model"); strings.Contains(out, "model moved") {
		t.Errorf("-by model printed the tag note:\n%s", out)
	}
}
