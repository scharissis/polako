package main

// The `-by tag` model note: a batch that asked for one thing but ran on two
// models reads as a verdict on whatever the tag was about, when half of it
// may just be a Claude Code release moving the inherited model underneath.

import (
	"slices"
	"strings"
)

// tagMove is one request inside one tag that resolved to more than one model.
type tagMove struct {
	tag, requested string // requested "" is inherit
	models         []string
}

// movedTags keys on (tag, requested model), not the tag alone: a tag holding
// two requests — a deliberate tier swap, plan-best's opus against best, an
// inherit batch with -remediation-model sonnet — has two models because the
// model *is* the change, and stays quiet. Inherit counts as one request.
// (none) is skipped, since untagged runs aren't a batch and would note on
// almost every report; resumes are skipped for the [1m] flap
// buildInheritedModels describes.
func movedTags(ds dataset) []tagMove {
	var all []tagMove
	for _, r := range ds.runs {
		if r.Tag == "" || r.Model == "" || resumedRun(r) {
			continue
		}
		i := slices.IndexFunc(all, func(m tagMove) bool { return m.tag == r.Tag && m.requested == r.RequestedModel })
		if i < 0 {
			all = append(all, tagMove{tag: r.Tag, requested: r.RequestedModel})
			i = len(all) - 1
		}
		if m := &all[i]; !slices.Contains(m.models, r.Model) {
			m.models = append(m.models, r.Model)
		}
	}
	var moved []tagMove
	for _, m := range all {
		if len(m.models) > 1 {
			moved = append(moved, m)
		}
	}
	return moved
}

// requestName is how the note and -json spell a request.
func requestName(requested string) string {
	if requested == "" {
		return "inherit"
	}
	return requested
}

// movedNote is the text form, shared by the text table and the HTML Note.
func movedNote(moves []tagMove) string {
	if len(moves) == 0 {
		return ""
	}
	parts := make([]string, 0, len(moves))
	for _, m := range moves {
		parts = append(parts, "under "+m.tag+": "+requestName(m.requested)+" ran on "+andList(m.models))
	}
	return "(model moved " + strings.Join(parts, "; ") + ")"
}

func andList(items []string) string {
	if len(items) < 2 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}

// statsDocTagMove is tagMove's -json twin.
type statsDocTagMove struct {
	Tag       string   `json:"tag"`
	Requested string   `json:"requested"`
	Models    []string `json:"models"`
}

func statsDocTagMovesFrom(moves []tagMove) []statsDocTagMove {
	if len(moves) == 0 {
		return nil
	}
	out := make([]statsDocTagMove, 0, len(moves))
	for _, m := range moves {
		out = append(out, statsDocTagMove{Tag: m.tag, Requested: requestName(m.requested), Models: m.models})
	}
	return out
}
