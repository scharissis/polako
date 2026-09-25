# Visual evidence: a PR for a visual change shows the change

Scope: `implement-issue`'s skill text, one `work` flag, a two-line discount in
the left-work count, one eval case, the security and behaviour docs, one
CLAUDE.md invariant reworded · Behavior change: yes, by default — a run whose
change is visual starts the repo's dev server, takes screenshots, and pushes
them to a `polako-evidence` branch on origin; `-visual-evidence=false` restores
today's behaviour

A run that restyles a settings page opens a PR that says so in words. The
reviewer at the merge gate — one of the two places a human stands — has to
check the branch out and start the app to see what happened. On a React/Vite
project that is most PRs. `docs/VISION.md` says every feature "either widens
what the machine does between the gates or makes the gates cheaper to stand
at". This is the second kind.

The PR body already has the right section. `## Evidence` wants captured output,
before/after when both can be had. What it lacks is a way to take a picture and
a place to put one. This document is that design.

It answers four things. When a change counts as visual. How a run takes a
screenshot inside the grant it already has. Where the image lives so a PR body
can embed it. And what publishing pixels does to the rule that one thing leaves
the machine.

## What exists today

- The `## Evidence` spec (`skills/implement-issue/SKILL.md:499-515`). It allows
  "a fenced block of the real output (before/after when you can still reproduce
  both, after alone otherwise), or a link to an image already committed on the
  branch". It forbids the rest: "never attempt an asset upload — no upload tool
  is in this run's grant". `.github/pull_request_template.md:5-11` mirrors it.
- The spec says "Capture it when you run the manual check in step 1"
  (`SKILL.md:501`). Step 1 (`SKILL.md:267-272`) runs tests, typecheck and lint.
  It never defines a manual check, and nothing in the skill starts an app.
- `PLAN.md` and `PR_BODY.md` stay out of commits by wording alone — "Leave
  PLAN.md itself uncommitted" (`SKILL.md:478`), "never commit it"
  (`SKILL.md:528`). No `.git/info/exclude`, no explicit-path rule.
- Tests pin the PR body: `TestPRBodyKeepsItsSectionsAndClosingLine`
  (`repo_test.go:1064`) wants the five headings and the literal "Omit this
  section entirely when the change alters nothing a human sees";
  `TestPRBodySectionsAllHaveBudgets` (`repo_test.go:1118`) wants a budget word
  in every section; `TestSkillCarriesTheHouseStyle` (`repo_test.go:1096`).
- `defaultTools` (`flags.go:256`) grants `Bash(git:*)`, `Bash(npm:*)`,
  `Bash(npx:*)`, `pnpm`, `yarn`, `Read`, `Write`. It does not grant `node`,
  `curl`, `kill`, `gh api`, `gh pr edit`, `WebFetch` or any MCP tool.
  `TestDefaultToolsDoNotGrantGhWholesale` (`main_test.go:1302`) keeps it so.
- The fresh prompt is exactly `"/%s %d"` — `issueRun` (`claude.go:147`).
  `buildArgs` (`claude.go:212`) is the whole argv: no `--mcp-config`, no
  pass-through. `plan` already sends a second slash argument: `planPrompt`
  (`plan.go:401`) appends the focus through `planSlashArg` (`plan.go:422`).
- The closest precedent for a run writing extra content to its own PR:
  `prCommentTools` (`pr.go:347`) and the prompt sentence `prCommentHow`
  (`pr.go:354`), pinned by `TestEveryRemediationRunMayCommentOnItsOwnPR`
  (`drain_test.go:2516`). Nothing in the binary edits a PR body.
- The left-work count ignores exactly one path: `inspectLeftWork`
  (`park.go:193`) skips `planFile` (`park.go:231`, `sync.go:22`). Any other
  untracked file in a worktree blocks `tidy` and inflates a park message.
- `tidy` lists local branches only (`tidy.go:171`) and keeps those
  `issueForBranch` (`status.go:388`) accepts — prefix plus a positive integer.
  PR lookup is exact-name (`prForBranch`, `pr.go:42`). Nothing lists remote
  branches by prefix.
- `docs/security.md:106` — "What leaves the machine" — is a one-row table:
  `-post-summary`.
- Two budgets are nearly spent. `docs/reference.md` is 506 lines against a 509
  ceiling that only shrinks (`docsbudget_test.go:40`). `parseFlags`
  (`flags.go:293`) is 128 lines against `funcBudget = 130`
  (`sizebudget_test.go:50`).
- The eval fixture is `greet.sh` and `test.sh` — nothing a browser renders.
  The stand-in `gh` records the PR body to `.eval/pr-body.md`; origin is a bare
  on-disk repo, so a pushed ref can be inspected. `evals/one-turn/seed.sh` is
  the pattern for seeding extra fixture files. `evals/run.sh:165` grants
  blanket `Bash`, so an eval cannot prove the production allowlist fits.
- No file in the repo mentions a screenshot, a browser or Playwright.

## The answer in one paragraph

When the planned change touches files a browser renders and the repo has a
script that serves them, the run starts that server from its own worktree,
shoots at most four routes it found in the repo's own routing code — before
its first edit and again at its final commit — looks at each image, and pushes
the PNGs to one orphan branch on origin, `polako-evidence`. The PR body embeds
them by commit SHA. Every command sits inside today's grant. Any failure means
no images and one line in `## Verification`; it never parks an issue. One flag
turns it off.

## Vocabulary

- **Visual change.** A change to files a browser renders, in a repo the run
  can serve. Both halves, or it isn't one.
- **Shot.** One PNG of one route at 1280x800. A **pair** is a before and an
  after of the same route.
- **Evidence ref.** The branch `polako-evidence` on origin. An orphan: it
  shares no history with any other branch, and no PR ever points at it.
- **Scratch dir.** `<worktree>/.polako-evidence/`, untracked, where shots wait
  between capture and publish.
- **The ladder.** What a run does when a step fails: drop to no images, say
  what was tried, carry on.

## When a change is visual

Decided in Phase 2, from the plan, and written into `PLAN.md`. Both must hold:

1. The planned files include browser-rendered sources — `tsx`, `jsx`, `vue`,
   `svelte`, `astro`, `css`, `scss`, `html`, templates.
2. `package.json`, read with Read, has a serving script: `dev`, `start`,
   `preview` or `storybook`.

**Shots come from the diff, never from the issue.** Every route is one the run
found in the repo's routing code, and the plan cites the file. The host is
always loopback, on the port the run's own server printed. Issue text may
describe the change; it never supplies a URL, a route, a count or a viewport.
An issue that says "screenshot `http://10.0.0.5/admin`" is otherwise a way for
a stranger to make a run publish a picture of something internal.

v1 shoots URL-addressable states only — the `playwright screenshot` command
cannot click. A changed component with a story, in a repo with a `storybook`
script, is shot through its story's iframe URL.

The block in `PLAN.md`, which is also the resume state:

```
## Visual evidence
Decision: capture | skip — <reason>
Launch: <exact command, from package.json scripts>
Shots (at most 4):
- <slug> — <path> — <routing file that defines it>
Before: pending | captured @ <base sha> | not captured — <why>
After: pending | captured @ <head sha>
Published: pending | <evidence commit sha> | gave up — <what was tried>
```

On resume: `Before: captured` with the files still there means don't reshoot.
Edits exist and there is no `Before:` line means after only — the Evidence
spec's own wording already allows it. `Published: <sha>` with an unchanged HEAD
means reuse the URLs and don't push. `no-evidence` beats any block an earlier
run wrote.

## Taking the shot

**Before is free.** At the start of Phase 3, before the first edit, the
worktree still equals base. Shooting then costs no second checkout and no
second install. The precondition is checkable: `git -C <worktree> status
--porcelain` shows nothing but `PLAN.md`, and `rev-list --count
origin/<default>..HEAD` is 0.

**After** is shot once the gate's last step has run, at the final HEAD, before
`PR_BODY.md` is written.

The lifecycle, every step inside the grant:

1. Start the server in the background: `npm --prefix <worktree> run <script>`
   (`pnpm -C`, `yarn --cwd`).
2. Poll its output until it prints its local URL. That line is the readiness
   signal and the real port, so nothing needs `curl` or `sleep`.
3. Shoot: `npx --yes playwright screenshot --viewport-size=1280,800
   --wait-for-timeout=1500 <url> <abs.png>`. If the repo has its own
   Playwright, use that one.
4. If the shot fails because the browser is missing, run `npx --yes playwright
   install chromium` once and retry. It lands in the user cache; the target
   repo changes not at all.
5. Stop the server. This is not optional: a server left up holds the port the
   repo's own e2e suite wants during the test run.

Probe 1 says a headless run can stop its own background shell under this
allowlist, so this is the lifecycle. The fallback it would have needed — one
blocking `npx --yes start-server-and-test "<start>" <url> "<shots>"`, one more
ad-hoc package and a port known up front — stays unused.

**Look before publishing.** The run Reads each PNG. It drops an error overlay,
a blank page, a login wall, and anything that looks like a credential or
personal data. A dropped shot is a rung on the ladder, not a failure.

Caps, fixed in the skill: 1280x800, PNG, at most four shots or pairs, about
500 KB each. An oversized shot is dropped, not downscaled.

The scratch dir stays out of commits three ways: the skill stages by explicit
path, never `add -A`; a check before the PR that `git -C <worktree> log
--oneline <base>..HEAD -- .polako-evidence` is empty; and cleanup after `gh pr
create` with `git -C <worktree> clean -fdq -- .polako-evidence`, since `rm` is
not granted.

## Where the image lives

GitHub's drag-and-drop attachments have no API. So the image goes where the
code goes: a git ref on origin, written with `Bash(git:*)`.

Layout: `issue-<N>/<head sha7>/{before,after}-<slug>.png`. Append-only — a
re-shoot makes a new directory, so a URL never changes meaning. Commit subject:
`evidence: issue-<N> @ <sha7>, <k> shots [skip ci]`. No `#N`, which would put a
cross-reference on the issue's timeline.

The push is plumbing, each step one `git -C … <subcommand>` — no pipes, no
env-var prefixes, no stdin, all of which fall outside the prefix match of
`Bash(git:*)`:

1. `ls-remote --heads origin polako-evidence` — absent, or present.
2. If present, fetch it to `refs/remotes/origin/polako-evidence`. That is
   parent P.
3. `worktree add --no-checkout --detach <main>/.worktrees/evidence-tmp [P]`.
   It exists to own a private index; nothing is ever checked out into it.
4. If P: `read-tree P`.
5. Per PNG: `hash-object -w <abs.png>`, then `update-index --add --cacheinfo
   100644,<blob>,<path>`.
6. `write-tree`, then `commit-tree <tree> [-p P] -m "<subject>"`. With no
   parent, that commit is the orphan root — the first-ever creation.
7. `push origin <commit>:refs/heads/polako-evidence`. Never `--force`.
8. `worktree remove --force` the temp worktree.

`commit-tree` runs no husky or pre-commit hook. `--no-checkout` never
materialises a growing tree of PNGs. No local branch is made, so `tidy` never
sees one. One issue in flight means no concurrent pusher; a non-fast-forward
gets one refetch and retry, then the run gives up quietly.

The URL:
`https://<host>/<owner>/<repo>/blob/<evidence-commit-sha>/<path>?raw=true`.
Host, owner and repo come from `git -C <worktree> config --get
remote.origin.url` — not `remote get-url`, which expands `insteadOf` and would
hand back an ssh rewrite.

## What the PR body says

```
## Evidence
`/settings` at 1280x800.

| Before | After |
| --- | --- |
| ![before /settings](…/before-settings.png?raw=true) | ![after /settings](…/after-settings.png?raw=true) |
```

After-only is one image line per shot. The section's budget becomes "at most
four shots or pairs, plus at most a sentence of framing around each", which
satisfies the budget test. The literal Omit sentence stays. "never attempt an
asset upload — no upload tool is in this run's grant" becomes "the evidence ref
is the one sanctioned channel; never any other upload". "a link to an image
already committed on the branch" stays for repos that keep visual snapshots.
The skill's wording is about "any image this run captured as real output", not
about browsers, so a later capture tool needs no new channel.

**The ladder.** No serving script, install fails, browser download fails,
system libraries missing, a shot shows a login or error page, the push is
refused by a ruleset, the origin URL won't parse, the default branch is itself
named `polako-evidence` — each ends the same way: no images, one line in `##
Verification` saying what was tried. Never a park. Never a question on the
thread.

## The switch

`-visual-evidence` on `work`, a bool, default true. The channel to the skill
is a second slash argument: `arguments: [issue, evidence]` in the frontmatter,
and `issueRun` appends ` no-evidence` when the flag is false. With the flag on,
the prompt is byte-identical to today's, so every existing pin holds. By hand
it is `/polako:implement-issue 12 no-evidence`.

An environment variable is not a channel: the skill can't read one, since
`echo` and `printenv` are outside the grant. The token has no digits because
the fake CLI finds the issue number with `lastNumber` (`main_test.go:965`).
`POLAKO_VISUAL_EVIDENCE` comes free from `applyEnvDefaults` (`flags.go:452`).

## What this does to the invariants

- **One thing leaves the machine.** The destination is not new: the operator's
  own repo, where the code and the PR body already go. The content is new:
  pixels. A fresh worktree has no gitignored `.env` file, but the child
  inherits the operator's environment, and an app can render what it reads.
  A SHA-pinned image on a public repo is, in practice, not retractable. So this
  is said out loud: a second row in `docs/security.md`'s table, the CLAUDE.md
  invariant reworded to name it, and the PR that ships it saying so in its
  body. The look-before-publishing step is the mitigation, and the flag is the
  operator's answer when that isn't enough. Off-by-default on public repos was
  considered and not chosen; see below.
- **Nothing merges itself.** The evidence ref is an orphan. No PR is opened,
  merged or closed for it, and nothing is committed to the default branch.
- **All state lives in GitHub; nothing is read back.** The binary never reads
  the ref. The skill fetches it only to find a parent to append to. Delete the
  branch and no drain behaves differently; old PRs lose their pictures.
- **Unattended means no prompts.** Every command is `git -C`, a package
  manager with a path flag, `npx`, Read or Write. Stopping the server is
  TaskStop, which probe 1 saw run ungranted and unprompted.
- **Issue text is data**, and **issue text never makes a run dearer.** Routes,
  counts and sizes are fixed by the skill and the diff. The flag is the only
  switch.
- **`issue-N` branch naming is a contract.** The ref's name is fixed and not
  under `-branch-prefix`. It is never prefix-plus-integer, so it is never a
  tidy candidate and never heads a PR.
- Untouched: stdlib-only Go, the plan and health write surface, restart
  safety, the house-style copies.

## Drafted tickets

Ordered by dependency. Sizes are the shape of the work, not money.

### 1. The evidence scratch dir is not left work

**Problem.** `inspectLeftWork` counts every porcelain path but `PLAN.md`
(`park.go:231`). Shots left behind by a dead run would make `tidy` refuse the
worktree and pad a park message's file count.

**Shape.** `const evidenceDir = ".polako-evidence"` beside `planFile`
(`sync.go:22`); discount paths under it in `inspectLeftWork`. Tests are twins
of the PLAN.md ones — `TestReclaimRemovesAWorktreeHoldingOnlyThePlan`
(`tidy_test.go:146`) and its drain-side partner. A `repo_test.go` check that
arms once `SKILL.md` names the directory, so the two sides can't drift.

**Done when.** A worktree holding only `PLAN.md` and `.polako-evidence/*.png`
is reclaimed; one holding a real stray file is still refused.

Estimate: S.

### 2. Eval case `visual-change`

**Problem.** No case has anything to look at, so no skill change here could be
verified.

**Shape.** `evals/visual-change/` on the usual pattern. `seed.sh` adds
`index.html`, `style.css` and a `package.json` whose `dev` script is `python3
-m http.server 4173`, commits and pushes like `evals/one-turn/seed.sh:17-21`.
It sets origin's URL to `https://github.invalid/eval/fixture.git` with a
`url.<bare>.insteadOf` routing traffic to the bare repo, so the run has an
owner and repo to build a URL from. `build_evidence` in `evals/lib/grade.py`
also lists origin's refs and `ls-tree -r polako-evidence`. Graders: a PR was
opened; its body embeds `/blob/<40-hex>/issue-1/…png?raw=true` and that SHA is
on origin's evidence ref; no PNG or scratch dir is committed on `issue-1`;
only loopback was shot; the before shot precedes the first Edit — red until
ticket 6, and the case says so. One negative grader added to `clear-issue`: no
server started, no ref pushed. A row in `evals/README.md`'s table.

**Done when.** The case scaffolds and grades. It uses a real Chromium, about
150 MB once into the user cache; the suite is opt-in. This PR touches
`grade.py`, so its body defers verification to a human.

Estimate: M.

### 3. Evidence images get a channel

**Problem.** The Evidence spec forbids every way of showing a picture except
committing it to the branch under review.

**Shape.** In `skills/implement-issue/SKILL.md`: the publish recipe, the ref
layout, the URL form, the rewritten Evidence section, and the `evidence`
argument with its `no-evidence` value. Mirror the wording in
`.github/pull_request_template.md`. The second row and a paragraph in
`docs/security.md`; the CLAUDE.md invariant reworded. `repo_test.go` pins: the
ref name, "never `--force`" beside it, the `config --get remote.origin.url`
spelling, the Evidence budget, the frontmatter's `evidence`. Existing pins to
keep green: the Omit sentence, the budget regex, no `--fix` token.

**Done when.** `./scripts/check.sh` passes, and `clear-issue` was run with its
verdicts and spend quoted in the PR body.

Depends on: ticket 1. Estimate: M.

### 4. Web capture, after only

**Problem.** A channel with nothing to send. No step classifies a change as
visual, starts a server or takes a shot.

**Shape.** Phase 2 classification and the `PLAN.md` block. The server
lifecycle, in whichever form probe 1 allows. The look step, the caps, the
ladder, the scratch-dir guards and the cleanup beside the `PR_BODY.md` delete.
A paragraph in `docs/behaviour.md`. A row in `docs/experiments.md` for the
next batch's fresh `-run-tag`.

**Done when.** `visual-change` passes all but its before-ordering grader,
`clear-issue` stays green with its negative grader, verdicts and spend quoted.
If this lands at the edge of one PR, the split is classification-and-block
first, lifecycle-and-ladder second.

Depends on: tickets 2 and 3, probes 1 and 2. Estimate: L.

### 5. `-visual-evidence` on `work`

**Problem.** Default-on with no switch leaves an operator on a public repo one
choice: don't upgrade.

**Shape.** The config field and the flag — one or two lines in `parseFlags`,
which has two to spare, or extract a registration helper. `issueRun` appends
` no-evidence` when false. New `TestIssueRunPassesTheEvidenceSwitch`; extend
`TestDryRunPrintsTheInvocationARunWouldMake` (`dryrun_test.go:154`). One row
in `docs/reference.md`, which has room for exactly that; the argument lives in
`security.md`.

**Done when.** The default prompt is byte-identical to today's, `-dry-run
-visual-evidence=false` prints the argument, and `TestDocsDocumentEveryFlag`
passes.

Depends on: ticket 3. Estimate: S.

### 6. Before shots

**Problem.** After-only shows the result. It doesn't show the change.

**Shape.** Phase 3 step 0 with its precondition, the `Before:` line, the pair
table in Evidence, the resume rules.

**Done when.** The before-ordering grader passes, and `resume-existing-plan`
still does. Verdicts and spend quoted.

Depends on: ticket 4. Estimate: M.

### 7. A remediation run re-shoots — optional

**Problem.** A review-fix that moves a button leaves the PR body showing the
old after.

**Shape.** An `evidenceHow` sentence beside `prCommentHow` (`pr.go:354`),
added to the three remediation prompts only when the flag is on, carrying a
compact recipe. The new shot goes up as a PR comment through the existing
pinned `prCommentTools` grant — no new grant, and still nothing edits a PR
body. A test on the model of `TestEveryRemediationRunMayCommentOnItsOwnPR`. A
`repo_test.go` pin that Go and `SKILL.md` agree on ref name, scratch dir and
URL form.

**Done when.** A fake review-fix run's argv carries the sentence with the flag
on and lacks it with the flag off.

Depends on: tickets 4 and 5. Estimate: M. Waits for a ledger row to ask for it.

## Experiments — rows, not tickets

| tag | hypothesis | knob |
| --- | --- | --- |
| `visual-evidence-on` | Evidence adds little to `$/merged` on a frontend repo and causes zero parks. | Default on against `-visual-evidence=false`, same repo. |
| `evidence-preview` | Shots from `build` plus `preview` are steadier than shots from `dev`. | Skill wording: prefer `preview` when the script exists. |
| `evidence-webserver` | Where the repo has Playwright, a scratch `webServer` config beats background-and-stop. | Skill wording for that rung. |

## Considered and not proposed

- **PNGs committed on the issue branch.** They land in the reviewed diff and
  then in the default branch for good. Still allowed for repos that keep
  visual snapshots on purpose.
- **GitHub's user-attachments.** Browser session only; no API.
- **`gh release upload`, a gist, `gh api`.** Each is a new grant and a new
  destination. `gh api` is the one the allowlist exists to withhold.
- **An external image host.** A true second destination.
- **`data:` URIs in the body.** GitHub strips them.
- **A hidden ref or git notes.** Dodges clone weight, rulesets and CI, but no
  human can browse or delete it in the UI. What leaves the machine should be
  visible. Revisit if clone weight becomes a complaint.
- **`git worktree add --orphan`.** Needs git 2.42 and makes a local branch.
- **Force-pushing or pruning the evidence ref.** Breaks pinned URLs.
- **`.git/info/exclude` for the scratch dir.** A write under `.git` likely
  prompts. The left-work discount is sturdier.
- **A second checkout at base for a resumed run's "before".** Doubles the
  install. After-only is already house wording.
- **URLs from the issue, or a deployed preview.** Argued above.
- **A browser MCP server.** `buildArgs` has no `--mcp-config` and the
  reference says there is no pass-through on purpose.
- **Clicked-through states, GIFs, pixel diffs.** Later, if a row asks.
- **A fake `npx` in the eval.** The look step would be judging a canned image.
- **An `evidence:` label or a per-repo config file.** The flag and `-run-tag`
  are enough, and polako reads no config file today.
- **Off by default on public repos.** The binary knows visibility from
  preflight, the standalone skill doesn't, and a tri-state is more interface.
  Revisit the first time it bites.
- **Terminal UIs through `vhs`, the iOS simulator, desktop apps.** Out of
  scope. Ticket 3's channel is tool-agnostic so they stay possible.

## Open questions

Seven probes, each a few minutes, before ticket 1 starts — so the tickets are
built against what the CLI, git and GitHub do rather than what this document
assumes:

1. Under `-p` with exactly `defaultTools`: can a run start a background shell,
   read its output and stop it? Does the shell die with the session? The
   answer picks ticket 4's lifecycle.
2. Does `npx --yes playwright screenshot` fetch Chromium itself, or need the
   install command first? Behaviour with no TTY, cache path, size, and the
   failure text on a Linux box missing system libraries.
3. Does `blob/<sha>/…?raw=true` render inline in a private repo's PR body? Web
   first; the mobile app and GHES if either is to hand.
4. Does a common ruleset refuse a new branch name, and with what error? Does
   `commit-tree` trip over `commit.gpgsign` or a required-signature rule?
5. Does `[skip ci]` keep Vercel, Netlify and CircleCI off the ref? Confirm
   Actions runs nothing on an orphan with no workflow files.
6. After `worktree add --no-checkout --detach`, is the index empty, and does
   the plumbing sequence run clean through `git -C` on the oldest git polako
   supports, and on Windows?
7. Does Read on a PNG work headless, and what does one 1280x800 shot cost in
   tokens? It sets whether four is the right cap.

### Answers

Probes 3 to 6, run 2026-09-20 with a real push to this repo — one orphan
commit, `bee66f1`, on `polako-evidence` — and written up on issue #402.
Probes 1, 2 and 7, run the same day on claude 2.1.274, macOS arm64, node 24,
with no Playwright cache on the box, and written up on issue #404.

1. Yes to all three. `claude -p --permission-mode acceptEdits` with exactly
   `defaultTools` started `npm --prefix <dir> run dev` with Bash's
   `run_in_background`. The tool result names an output file; Read on that
   file showed the `Local:` line. No BashOutput, `sleep` or `curl` needed.
   TaskStop stopped the shell — it is a deferred tool, not in the grant, and
   ran with no prompt and no entry in `permission_denials`. A second server
   left running was gone once the session exited: the shell dies with the
   session. So the lifecycle is background-and-stop. Unchecked: a server slow
   to print its URL, where the run has to Read the file more than once.
2. No, it doesn't fetch Chromium itself. With no browser cached, `npx --yes
   playwright screenshot` exits 1 in about 13s on `Error: command.parse:
   Executable doesn't exist at <cache>/chromium_headless_shell-<rev>/…` plus a
   boxed "Please run … npx playwright install". Match on `Executable doesn't
   exist`. `npx --yes playwright install chromium` then ran clean with stdin
   at `/dev/null`: 66s, a 277 MiB download, 557 MB on disk under
   `~/Library/Caches/ms-playwright` (`~/.cache/ms-playwright` on Linux). Two
   CDN mirrors timed out at 30s each and it fell through to the next by
   itself, still exit 0. The retried shot took 3.5s and wrote a 13 KB
   1280x800 PNG. Unchecked: the failure text on a Linux box missing system
   libraries — no such box to hand — so the ladder treats any second failure
   as the rung, whatever it says.
3. Yes, on a public repo, web: the image renders inline in an issue comment,
   and the URL answers 200 `image/png`. A private repo, the mobile app and
   GHES are unchecked.
4. The push was accepted here, where the one branch ruleset targets the
   default branch only. `commit-tree` ignores `commit.gpgsign`, even with a
   broken `gpg.program`: it writes an unsigned commit and doesn't fail. So a
   required-signature rule covering all branches refuses the push — the
   ladder's ruleset rung. That refusal's error text is unseen.
5. Actions ran nothing: zero runs and zero check suites on the evidence
   commit. Vercel, Netlify and CircleCI are unchecked — none is installed
   here — so `[skip ci]` stays in the subject.
6. Yes on git 2.55, macOS. The index is empty and nothing is on disk after
   the `worktree add`, with or without a parent commit, so `read-tree P` is
   what makes an append. First creation and append both work, no local branch
   is made, and a push from a stale parent is rejected as non-fast-forward
   without `--force`. `ls-remote --heads` exits 0 either way: absent is empty
   output, not a failed command. Older git and Windows are unchecked.
7. Yes. Read on a 1280x800 PNG returns an image block headless, and the model
   described it correctly. Context grew by 1,488 tokens across the Read,
   about 1,400 of that the image. Four shots are under 6k tokens and four
   pairs under 12k, so four stays the cap.

## Work items

Each is one PR, one of the tickets above. Tickets 1 and 2 have no
dependencies and can go first in either order. Then 3, then 4 and 5. Tickets
1 to 5 ship in one minor release — the default-on behaviour never ships
without its switch — with an Operator impact line naming the dev server, the
one-time Chromium download, the `polako-evidence` branch and the flag. 6
follows in the next. 7 waits for a row to ask for it.

- [x] The seven probes have answers, recorded here
- [ ] The evidence scratch dir is not left work (ticket 1)
- [ ] Eval case `visual-change` (ticket 2)
- [ ] Evidence images get a channel (ticket 3)
- [ ] Web capture, after only (ticket 4)
- [ ] `-visual-evidence` on `work` (ticket 5)
- [ ] Before shots (ticket 6)
- [ ] A remediation run re-shoots — optional (ticket 7)
- [ ] The `visual-evidence-on` batch has a verdict in the ledger
