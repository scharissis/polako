package main

import "testing"

func TestParsePlanFooter(t *testing.T) {
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
			body: "Proposed by polako plan from docs/plans/plan-conventions.md @ fdfedcb — edit freely; remove the `proposed` label to queue it.\n",
			want: planFooter{doc: "docs/plans/plan-conventions.md", sha: "fdfedcb"},
			ok:   true,
		},
		{
			name: "edited tail",
			body: "Proposed by polako plan from docs/plans/foo.md @ abc1234 — reviewed by Sam, ready to go\n",
			want: planFooter{doc: "docs/plans/foo.md", sha: "abc1234"},
			ok:   true,
		},
		{
			name: "tail with plain hyphen",
			body: "Proposed by polako plan from docs/plans/foo.md @ abc1234 - edit freely\n",
			want: planFooter{doc: "docs/plans/foo.md", sha: "abc1234"},
			ok:   true,
		},
		{
			name: "missing sha, tail kept",
			body: "Proposed by polako plan from docs/plans/foo.md — edit freely; remove the `proposed` label to queue it.\n",
			want: planFooter{doc: "docs/plans/foo.md"},
			ok:   true,
		},
		{
			name: "missing sha, no tail",
			body: "Proposed by polako plan from docs/plans/foo.md\n",
			want: planFooter{doc: "docs/plans/foo.md"},
			ok:   true,
		},
		{
			name: "tail separator removed but tail kept",
			body: "Proposed by polako plan from docs/plans/foo.md @ abc1234 edit freely now\n",
			want: planFooter{doc: "docs/plans/foo.md", sha: "abc1234"},
			ok:   true,
		},
		{
			name: "extra lines below the footer",
			body: "Proposed by polako plan from docs/plans/foo.md @ abc1234 — edit freely\n\nPS added a note after the footer\n",
			want: planFooter{doc: "docs/plans/foo.md", sha: "abc1234"},
			ok:   true,
		},
		{
			name: "footer not the last line, indented",
			body: "    Proposed by polako plan from docs/plans/foo.md @ abc1234 — edit freely\nmore prose\n",
			want: planFooter{doc: "docs/plans/foo.md", sha: "abc1234"},
			ok:   true,
		},
		{
			name: "quoted earlier footer then the real one",
			body: "> Proposed by polako plan from docs/plans/old.md @ 0000000 — edit freely\n\nThis supersedes it.\n\nProposed by polako plan from docs/plans/new.md @ 1111111 — edit freely\n",
			want: planFooter{doc: "docs/plans/new.md", sha: "1111111"},
			ok:   true,
		},
		{
			name: "only a quoted footer is still a footer",
			body: "> Proposed by polako plan from docs/plans/old.md @ 0000000 — edit freely\n",
			want: planFooter{doc: "docs/plans/old.md", sha: "0000000"},
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
