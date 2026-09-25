package main

// The `ran on` line: every provider · model · effort the runs in scope used,
// always printed. The `models` line (statsmodels.go) answers whether inherit
// moved; this one answers what every run used, so it counts all runs.

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
)

// ranOnCombo is one provider · model · effort and how many runs used it.
type ranOnCombo struct {
	provider, model, effort string
	runs                    int
}

// ranOn is the three as a report names them. Records from before provider
// existed, and runs that died before reporting a model, read unrecorded.
// Effort is what was asked for: no event reports the effort that ran, so an
// empty request reads inherited rather than guessing what inherit meant.
func ranOn(r runRecord) (provider, model, effort string) {
	return cmp.Or(r.Provider, "unrecorded"), cmp.Or(r.Model, "unrecorded"), cmp.Or(r.RequestedEffort, "inherited")
}

// buildRanOn groups ds.runs by ranOn, most runs first; ties keep the order
// each first appeared, so the same data prints the same way twice.
func buildRanOn(ds dataset) []ranOnCombo {
	var combos []ranOnCombo
	for _, r := range ds.runs {
		p, m, e := ranOn(r)
		i := slices.IndexFunc(combos, func(c ranOnCombo) bool { return c.provider == p && c.model == m && c.effort == e })
		if i < 0 {
			combos = append(combos, ranOnCombo{provider: p, model: m, effort: e})
			i = len(combos) - 1
		}
		combos[i].runs++
	}
	slices.SortStableFunc(combos, func(a, b ranOnCombo) int { return b.runs - a.runs })
	return combos
}

// ranOnLine is the text form, shared by the text report and the HTML sections
// through runPairs.
func ranOnLine(combos []ranOnCombo) string {
	if len(combos) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(combos))
	for _, c := range combos {
		parts = append(parts, fmt.Sprintf("%s · %s · %s, %s", c.provider, c.model, c.effort, plural(c.runs, "run")))
	}
	return strings.Join(parts, "; ")
}

// statsDocRanOn is ranOnCombo's -json twin.
type statsDocRanOn struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Effort   string `json:"effort"`
	Runs     int    `json:"runs"`
}

// statsDocRanOnFrom is always non-nil: `ran_on` is in every document.
func statsDocRanOnFrom(combos []ranOnCombo) []statsDocRanOn {
	out := make([]statsDocRanOn, 0, len(combos))
	for _, c := range combos {
		out = append(out, statsDocRanOn{Provider: c.provider, Model: c.model, Effort: c.effort, Runs: c.runs})
	}
	return out
}
