package main

import (
	"strings"
	"testing"
)

// issue #678: a focus or hover state is shot from a scratch script written
// from a fixed template, so a reviewer of the skill knows every call it
// makes. The version pin keeps `npx -p` from fetching a Playwright the
// cached browser doesn't match; the JSON-string values keep a selector from
// being spliced into code; clicks stay out.
func TestSkillShootsInteractionsFromAPinnedTemplate(t *testing.T) {
	t.Parallel()
	skill := strings.Join(strings.Fields(readRepoFile(t, "skills", skillDir, "SKILL.md")), " ")

	for _, want := range []struct{ text, why string }{
		{"- <slug> — <path> — <routing file> — focus|hover <selector> — <file that defines it>",
			"the Visual evidence block must carry the interaction shot entry"},
		{"never from the issue or a comment",
			"the selector must come from code, never issue text"},
		{"A click stays unshot",
			"clicks can write on a dev server and must stay on Unreached:"},
		{"`npx --yes playwright --version`",
			"the version is read once, to pin the script and the browser"},
		{"`npx --yes -p playwright@<v> node <worktree>/.polako-scratch/shoot.mjs`",
			"the script runs through the pinned npx call, no bare node grant"},
		{"`npx --yes playwright@<v> install chromium`",
			"the browser install must match the script's version"},
		{"each a JSON string literal",
			"URL and selector go in as JSON strings, never spliced into code"},
		{"({ chromium } = createRequire(path.join(bin, '..', '..', 'package.json'))('playwright'));",
			"a scratch script outside node_modules resolves playwright from npx's PATH, the one import"},
		{"const browser = await chromium.launch();", "the template launches Chromium"},
		{"await page.goto(url);", "the template loads the loopback URL"},
		{"if (action === 'focus') await target.focus(); else await target.hover();",
			"focus and hover are the only two actions"},
		{"await page.screenshot({ path: out });", "the template's one screenshot"},
		{"falls back: shoot that entry's path as a plain URL shot",
			"a failed script must fall back to the URL shot, not stop"},
	} {
		if !strings.Contains(skill, want.text) {
			t.Errorf("SKILL.md no longer says %q — %s", want.text, want.why)
		}
	}
	for _, banned := range []string{".click(", "page.evaluate(", "page.fill("} {
		if strings.Contains(skill, banned) {
			t.Errorf("SKILL.md's shot template calls %s — it is focus and hover only", banned)
		}
	}
}
