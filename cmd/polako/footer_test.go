package main

import (
	"slices"
	"strings"
	"testing"
)

func TestParsePlanFooter(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		body string
		want planFooter
		ok   bool
	}{
		{
			name: "template line as shipped",
			body: "## Summary\nblah\n\nProposed by polako plan from docs/VISION.md @ 1a2b3c4 — edit freely; remove the `proposed` label to queue it.\n",
			want: planFooter{doc: "docs/VISION.md", sha: "1a2b3c4"},
			ok:   true,
		},
		{
			name: "nested plan path",
			body: "Proposed by polako plan from docs/designs/plan-conventions.md @ fdfedcb — edit freely; remove the `proposed` label to queue it.\n",
			want: planFooter{doc: "docs/designs/plan-conventions.md", sha: "fdfedcb"},
			ok:   true,
		},
		{
			name: "edited tail",
			body: "Proposed by polako plan from docs/designs/foo.md @ abc1234 — reviewed by Sam, ready to go\n",
			want: planFooter{doc: "docs/designs/foo.md", sha: "abc1234"},
			ok:   true,
		},
		{
			name: "tail with plain hyphen",
			body: "Proposed by polako plan from docs/designs/foo.md @ abc1234 - edit freely\n",
			want: planFooter{doc: "docs/designs/foo.md", sha: "abc1234"},
			ok:   true,
		},
		{
			name: "missing sha, tail kept",
			body: "Proposed by polako plan from docs/designs/foo.md — edit freely; remove the `proposed` label to queue it.\n",
			want: planFooter{doc: "docs/designs/foo.md"},
			ok:   true,
		},
		{
			name: "missing sha, no tail",
			body: "Proposed by polako plan from docs/designs/foo.md\n",
			want: planFooter{doc: "docs/designs/foo.md"},
			ok:   true,
		},
		{
			name: "tail separator removed but tail kept",
			body: "Proposed by polako plan from docs/designs/foo.md @ abc1234 edit freely now\n",
			want: planFooter{doc: "docs/designs/foo.md", sha: "abc1234"},
			ok:   true,
		},
		{
			name: "extra lines below the footer",
			body: "Proposed by polako plan from docs/designs/foo.md @ abc1234 — edit freely\n\nPS added a note after the footer\n",
			want: planFooter{doc: "docs/designs/foo.md", sha: "abc1234"},
			ok:   true,
		},
		{
			name: "footer not the last line, indented",
			body: "    Proposed by polako plan from docs/designs/foo.md @ abc1234 — edit freely\nmore prose\n",
			want: planFooter{doc: "docs/designs/foo.md", sha: "abc1234"},
			ok:   true,
		},
		{
			name: "quoted earlier footer then the real one",
			body: "> Proposed by polako plan from docs/designs/old.md @ 0000000 — edit freely\n\nThis supersedes it.\n\nProposed by polako plan from docs/designs/new.md @ 1111111 — edit freely\n",
			want: planFooter{doc: "docs/designs/new.md", sha: "1111111"},
			ok:   true,
		},
		{
			name: "only a quoted footer is still a footer",
			body: "> Proposed by polako plan from docs/designs/old.md @ 0000000 — edit freely\n",
			want: planFooter{doc: "docs/designs/old.md", sha: "0000000"},
			ok:   true,
		},
		{
			name: "no footer",
			body: "## Summary\n\nJust a hand-filed issue with no footer at all.\n",
			ok:   false,
		},
		{
			name: "empty body",
			body: "",
			ok:   false,
		},
		{
			name: "prose mention mid-sentence does not parse",
			body: "The footer reads `Proposed by polako plan from <doc> @ <sha>` and nothing reads it yet.\n",
			ok:   false,
		},
		{
			name: "phrase present but nothing after it",
			body: "Proposed by polako plan from \n",
			ok:   false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parsePlanFooter(tc.body)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v (got %+v)", ok, tc.ok, got)
			}
			if ok && got != tc.want {
				t.Errorf("parsePlanFooter() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// parkFooter and parseParkFooter are two sides of the same contract as
// planFooter/parsePlanFooter — issue #432: parkIssue writes the footer,
// unpark and status (not yet built) read it back.
func TestParkFooterRoundTrips(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		entries []string
		want    string
	}{
		{name: "no entries, no footer", entries: nil, want: ""},
		{name: "one entry", entries: []string{"Bash(echo:*)"}, want: "Refused: Bash(echo:*)"},
		{
			name:    "several entries, comma joined",
			entries: []string{"Bash(echo:*)", "Bash(ssh:*)"},
			want:    "Refused: Bash(echo:*), Bash(ssh:*)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := parkFooter(tc.entries); got != tc.want {
				t.Errorf("parkFooter(%v) = %q, want %q", tc.entries, got, tc.want)
			}
			if tc.want == "" {
				return
			}
			body := "**polako parked this issue.** some reason.\n\n" + tc.want
			gotEntries, ok := parseParkFooter(body)
			if !ok || !slices.Equal(gotEntries, tc.entries) {
				t.Errorf("parseParkFooter(%q) = (%v, %v), want (%v, true)", body, gotEntries, ok, tc.entries)
			}
		})
	}
}

func TestParseParkFooter(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		body string
		want []string
		ok   bool
	}{
		{name: "no footer", body: "**polako parked this issue.** the run completed without opening a PR.", ok: false},
		{name: "empty body", body: "", ok: false},
		{
			name: "footer not the last line",
			body: "**polako parked this issue.** ...\n\nRefused: Bash(echo:*)\n\n" +
				"Nothing will run on it again until needs-human is removed.\n",
			want: []string{"Bash(echo:*)"},
			ok:   true,
		},
		{
			name: "quoted earlier footer then the real one",
			body: "> Refused: Bash(old:*)\n\nThis supersedes it.\n\nRefused: Bash(echo:*), Bash(ssh:*)\n",
			want: []string{"Bash(echo:*)", "Bash(ssh:*)"},
			ok:   true,
		},
		{name: "phrase present but nothing after it", body: "Refused: \n", ok: false},
		{
			name: "prose mention mid-sentence does not parse",
			body: "The comment ends `Refused: <entry>` when polako can derive one.\n",
			ok:   false,
		},
		{
			name: "Refused still found with a Park: category line after it",
			body: "**polako parked this issue.** ...\n\nRefused: Bash(echo:*)\nPark: permission_refused\n",
			want: []string{"Bash(echo:*)"},
			ok:   true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := parseParkFooter(tc.body)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v (got %v)", ok, tc.ok, got)
			}
			if ok && !slices.Equal(got, tc.want) {
				t.Errorf("parseParkFooter() = %v, want %v", got, tc.want)
			}
		})
	}
}

// parkCategoryFooter and parseParkCategory are two sides of the same
// contract as parkFooter/parseParkFooter — ticket 1 of docs/designs/unpark.md
// (#530): parkIssue writes the footer, unpark reads it back.
func TestParkCategoryFooterRoundTrips(t *testing.T) {
	t.Parallel()
	for _, category := range parkReasonOrder {
		category := category
		t.Run(category, func(t *testing.T) {
			t.Parallel()
			got := parkCategoryFooter(category)
			want := "Park: " + category
			if got != want {
				t.Errorf("parkCategoryFooter(%q) = %q, want %q", category, got, want)
			}
			body := "**polako parked this issue.** some reason.\n\n" + got
			if gotCategory := parseParkCategory(body); gotCategory != category {
				t.Errorf("parseParkCategory(%q) = %q, want %q", body, gotCategory, category)
			}
		})
	}
}

func TestParseParkCategory(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		body string
		want string
	}{
		{name: "no footer", body: "**polako parked this issue.** the run completed without opening a PR.", want: ""},
		{name: "empty body", body: "", want: ""},
		{name: "known category", body: "**polako parked this issue.** ...\n\nPark: budget\n", want: "budget"},
		{name: "unknown round-trips as itself", body: "Park: unknown\n", want: "unknown"},
		{
			name: "forged category not in parkReasonOrder parses as none",
			body: "**polako parked this issue.** ...\n\nPark: opus\n",
			want: "",
		},
		{
			name: "after a Refused: entries line",
			body: "**polako parked this issue.** ...\n\nRefused: Bash(echo:*)\nPark: permission_refused\n",
			want: "permission_refused",
		},
		{
			name: "quoted earlier footer then the real one",
			body: "> Park: budget\n\nThis supersedes it.\n\nPark: auth\n",
			want: "auth",
		},
		{
			name: "prose mention mid-sentence does not parse",
			body: "The comment ends `Park: <category>` when polako can classify it.\n",
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := parseParkCategory(tc.body); got != tc.want {
				t.Errorf("parseParkCategory(%q) = %q, want %q", tc.body, got, tc.want)
			}
		})
	}
}

// TestParkCommentBodyCategoryFooter pins parkCommentBody's placement of the
// Park: line — on its own for a budget park (no -add-tools entries), after
// Refused: for a permission park.
func TestParkCommentBodyCategoryFooter(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		entries  []string
		category string
		want     string
	}{
		{name: "budget park, no entries", entries: nil, category: parkBudget, want: "Park: budget"},
		{
			name:     "permission park, entries after Refused:",
			entries:  []string{"Bash(echo:*)"},
			category: parkPermission,
			want:     "Refused: Bash(echo:*)\nPark: permission_refused",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			body := parkCommentBody(16, "some reason", tc.entries, tc.category)
			if !strings.HasSuffix(body, tc.want) {
				t.Errorf("parkCommentBody(...) = %q, want suffix %q", body, tc.want)
			}
			if gotCategory := parseParkCategory(body); gotCategory != tc.category {
				t.Errorf("parseParkCategory(parkCommentBody(...)) = %q, want %q", gotCategory, tc.category)
			}
		})
	}
}
