package main

// The `models` line: which models inherited runs resolved to, and when. The
// inherited model changed twice in four weeks and `stats` said nothing unless
// asked with -by model, so a switch now shows up in the default report.

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// modelEpoch is one model inherited runs resolved to: when it was first and
// last seen, how many runs, and the CLI version it first appeared on.
type modelEpoch struct {
	model         string
	first, last   time.Time
	runs          int
	claudeVersion string
}

// buildModelEpochs lists models, not consecutive epochs: inherit can differ
// per repo through .claude/settings.json, so one machine draining two repos
// would flap between them. Only fresh, inherited runs count. A run that asked
// for a model says nothing about what inherit resolves to, and a resume can
// report the same model under another id — the bare id where the fresh run
// had a [1m] suffix — which would print one model as two. nil when fewer than
// two models are left, since one model is nothing to report.
func buildModelEpochs(ds dataset) []modelEpoch {
	var epochs []modelEpoch
	for _, r := range ds.runs {
		if r.Model == "" || r.RequestedModel != "" || r.Reason == reasonResume || r.Reason == reasonUnfinished {
			continue
		}
		t := recTime(r.TS)
		i := slices.IndexFunc(epochs, func(e modelEpoch) bool { return e.model == r.Model })
		if i < 0 {
			// ds.runs is in timestamp order, so the first run seen is the
			// earliest and its CLI version is the one the model arrived on.
			epochs = append(epochs, modelEpoch{model: r.Model, first: t, claudeVersion: r.ClaudeVersion})
			i = len(epochs) - 1
		}
		e := &epochs[i]
		e.runs++
		if t.After(e.last) {
			e.last = t
		}
		// No backfill from a later run: an unknown first version stays
		// unknown rather than naming a CLI the model didn't arrive on.
	}
	if len(epochs) < 2 {
		return nil
	}
	return epochs
}

// modelsLine is the text form, shared by the text report and the HTML
// sections through runPairs.
func modelsLine(epochs []modelEpoch) string {
	parts := make([]string, 0, len(epochs))
	for _, e := range epochs {
		part := fmt.Sprintf("%s %s → %s, %s", e.model, recDay(e.first), recDay(e.last), plural(e.runs, "run"))
		if e.claudeVersion != "" {
			part += ", from CLI " + e.claudeVersion
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, "; ")
}

func recDay(t time.Time) string {
	if t.IsZero() {
		return noValue
	}
	return t.UTC().Format(time.DateOnly)
}

// statsDocEpoch is modelEpoch's -json twin.
type statsDocEpoch struct {
	Model              string `json:"model"`
	First              string `json:"first,omitempty"`
	Last               string `json:"last,omitempty"`
	Runs               int    `json:"runs"`
	FirstClaudeVersion string `json:"first_claude_version,omitempty"`
}

// statsDocEpochsFrom is always non-nil: `epochs` is present in every
// document, [] when the text report prints no models line.
func statsDocEpochsFrom(epochs []modelEpoch) []statsDocEpoch {
	out := make([]statsDocEpoch, 0, len(epochs))
	for _, e := range epochs {
		doc := statsDocEpoch{Model: e.model, Runs: e.runs, FirstClaudeVersion: e.claudeVersion}
		if !e.first.IsZero() {
			doc.First, doc.Last = stamp(e.first), stamp(e.last)
		}
		out = append(out, doc)
	}
	return out
}
