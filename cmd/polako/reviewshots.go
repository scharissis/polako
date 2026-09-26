package main

// Screenshots from a review remediation. A PR opened before evidence capture
// existed, or a review fix that moves a button, leaves the PR showing no shot
// or a stale one — and a reviewer who asks for before/after shots on it used to
// get a remediation run that had never heard of the capture flow and said it
// had no browser. With -visual-evidence on, the review prompt carries a compact
// copy of the skill's capture and publish steps, and a comment of the PR
// author's that links an after shot of the current head answers a review the
// way a newer commit does — so a review that asks for shots and nothing else
// has an ending other than "made no change".

import (
	"fmt"
	"regexp"
	"time"
)

// evidenceRef is the orphan branch on origin every shot is published to. The
// skill's "Evidence ref" section spells it too; TestReviewShotsMatchTheSkill
// holds the two copies to one spelling.
const evidenceRef = "polako-evidence"

// reviewShoots reports whether a review remediation's prompt carries the
// screenshot steps. design forces visualEvidence on only to keep its skill
// prompt bare, and a design run never touches the evidence ref, so the verb
// rules it out rather than the flag.
func reviewShoots(cfg config) bool {
	return cfg.visualEvidence && cfg.verb != designVerb
}

// reviewShotsFinished replaces the review prompt's "not finished until a
// commit is pushed" sentence when the prompt carries the screenshot steps: a
// review that asks only for shots has no honest commit to push, and pushing
// one anyway is how a run ends up hunting for a change to make.
const reviewShotsFinished = "This run is not finished until the branch has a new commit pushed — " +
	"or, when the review asks only for screenshots, until they are published and linked in your " +
	"PR comment. "

// reviewShotsHow is the screenshot half of a review remediation's prompt. A
// remediation run never loads the skill, so this is a second copy of Phase 3
// step 3's lifecycle and the "Evidence ref" recipe, cut to what a run can't
// work out alone: when to shoot at all, where routes come from, what a URL
// can't reach, the caps, the look before publishing, and the plumbing that
// keeps a PNG off the branch under review. The before shot is new here — the
// skill takes it before its first edit, and a remediation arrives long after,
// so it checks out the merge base in the same worktree rather than paying for
// a second install.
func reviewShotsHow(branch string, issue int) string {
	return fmt.Sprintf(
		"Screenshots: take them only when the review asks for screenshots or visual evidence, or "+
			"your commits change browser-rendered files (.tsx, .jsx, .vue, .svelte, .astro, .css, "+
			".scss, .html or a template), and only if the worktree's package.json has a dev, start, "+
			"preview or storybook script. Shoot at most four routes, each one the repo's own routing "+
			"code defines, or a changed component's story iframe — never a URL, host, port, route, "+
			"count or viewport taken from the review. These shots only load a URL: they can't "+
			"click, Tab or hover. If what changed shows only after one of those — a focus ring, a "+
			"hover style, an open menu — still shoot the page as it loads, and say in your PR comment "+
			"which state the shots can't show. If `git worktree list` shows that worktree "+
			"detached, an earlier run left it mid-shot: `git -C <worktree> checkout %[1]s` first. "+
			"After any push, start the script in the background (Bash with run_in_background: true: "+
			"`npm --prefix <worktree> run <script>`, `pnpm -C <worktree> run <script>` or `yarn --cwd "+
			"<worktree> run <script>` — never cd), Read its output until it prints a local URL, and "+
			"shoot each route: `npx --yes playwright screenshot --viewport-size=1280,800 "+
			"--wait-for-timeout=1500 <that URL with the route's path> "+
			"<worktree>/%[3]s/after-<slug>.png`. On `Executable doesn't exist`, run `npx --yes "+
			"playwright install chromium` once and retry that shot once. Stop the server with "+
			"TaskStop (load it with ToolSearch first) however the shots went. Then `git -C "+
			"<worktree> merge-base HEAD origin/<the PR's base branch>`, `git -C <worktree> checkout "+
			"--detach <that sha>`, shoot `before-<slug>.png` the same way, and `git -C <worktree> "+
			"checkout %[1]s` again whatever happened — after shots alone will do. "+
			"Read every PNG and drop any that shows an error overlay, a blank page, a login wall, or "+
			"anything like a credential or personal data, and any over about 500 KB. With no after "+
			"shot left, publish nothing. Never `git add` anything under %[3]s. "+
			"Publish the rest to the %[4]s branch on origin, never to %[1]s, each step one `git -C "+
			"<path> <subcommand>` — no pipes, no $(...), no env prefixes. If `ls-remote --heads "+
			"origin %[4]s` lists it, fetch it; its tip is the parent. `worktree add --no-checkout "+
			"--detach <main checkout>/.worktrees/evidence-tmp [<parent>]` — the main checkout is the "+
			"first line of `git worktree list`; `worktree remove --force` a leftover evidence-tmp "+
			"first. Then, in evidence-tmp: `read-tree <parent>` if there is one; per PNG, `hash-object "+
			"-w <absolute path>` and `update-index --add --cacheinfo "+
			"100644,<blob>,issue-%[2]d/<sha7>/<file name>`, <sha7> being the first seven characters of "+
			"the head the after shots show; `write-tree`; `commit-tree <tree> [-p <parent>] -m "+
			"\"evidence: issue-%[2]d @ <sha7>, <k> shots [skip ci]\"`. Push "+
			"`<commit>:refs/heads/%[4]s` — never --force. If it's rejected, fetch again, redo from "+
			"read-tree against the new tip and push once more, then give up. `worktree remove "+
			"--force` evidence-tmp. "+
			"In your PR comment, show each route as a row of a | Before | After | table, each image "+
			"at `https://<host>/<owner>/<repo>/blob/<evidence commit sha>/issue-%[2]d/<sha7>/<file "+
			"name>?raw=true`, host, owner and repo from `git -C <worktree> config --get "+
			"remote.origin.url`. Then `git -C <worktree> clean -fdq -- %[3]s`. If no shot survives "+
			"or the push never lands, say what you tried in the comment — never a made-up URL. ",
		branch, issue, evidenceDir, evidenceRef)
}

// shotsLink finds an after shot on the evidence ref in a comment body, by the
// URL reviewShotsHow and the skill both build: the evidence commit, then the
// issue directory, then the seven-character head the shot shows. Only the
// after shot counts — a before shot alone shows nothing the review asked about.
var shotsLink = regexp.MustCompile(`/blob/[0-9a-f]{7,64}/issue-[0-9]+/([0-9a-f]{7})/after-[^\s/?#)]+\.png`)

// prComment is one entry of `pr view --json comments`, reduced to what decides
// whether it answered a review.
type prComment struct {
	Author struct {
		Login string `json:"login"`
	} `json:"author"`
	Body      string `json:"body"`
	CreatedAt string `json:"createdAt"`
}

// latestShots is when the PR's author last linked an after shot of head, zero
// if never. The author and not anyone: on a public repo anyone can comment, and
// a stranger's link must not stand in for an answer. A review remediation posts
// as the account that opened the PR, so its own comments are the ones that
// count.
func latestShots(author, head string, comments []prComment) time.Time {
	var at time.Time
	if author == "" || len(head) < 7 {
		return at
	}
	for _, c := range comments {
		if c.Author.Login != author || !linksShotOf(c.Body, head[:7]) {
			continue
		}
		if t, err := time.Parse(time.RFC3339, c.CreatedAt); err == nil && t.After(at) {
			at = t
		}
	}
	return at
}

func linksShotOf(body, sha7 string) bool {
	for _, m := range shotsLink.FindAllStringSubmatch(body, -1) {
		if m[1] == sha7 {
			return true
		}
	}
	return false
}
