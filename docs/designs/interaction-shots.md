# Interaction shots: screenshots after a Tab or a hover

Scope: `skills/implement-issue/SKILL.md`'s capture steps, the review
remediation prompt (`cmd/polako/reviewshots.go`), the `focus-change` eval
case · Behavior change: a PR whose change shows only on keyboard focus or
hover carries shots of that state, not just of the page as it loads

Evidence capture shoots a URL and nothing else. A downstream focus-ring fix
hit that wall: the ring shows only after Tab, the run reasoned that a URL
shot can't reach it, and the PR shipped with no screenshot. A reviewer had
to check it out and tab through it by hand, which is the exact chore
evidence capture exists to remove.

A companion fix keeps such a change at `capture` and names what the shots
can't reach on an `Unreached:` line. That stops the silent skip, but the
state the reviewer cares about is still missing. This design shoots it: a
throwaway script that loads the route, focuses or hovers one element the
diff changed, and takes the shot. It runs through the `npx` grant runs
already have, and a probe shows it runs without a prompt.

## What exists today

Measured on this worktree at `bb6a542`, 2026-09-26, with the Chromium and
Playwright 1.56.1 this container ships.

- **Capture is URL-only by design.** `SKILL.md` says `playwright screenshot`
  "can't click, so v1 only reaches URL-addressable states" (Phase 2 step 2).
  The original plan put "clicked-through states" under later
  (`docs/designs/done/visual-evidence.md`, "Considered and not proposed").
  The review prompt's recipe is the same URL-only shot
  (`reviewshots.go:reviewShotsHow`).
- **Runs have `npx` but not `node`.** `defaultTools` grants `Bash(npx:*)`
  and no `Bash(node:*)` (`flags.go:defaultTools`).
- **A scratch Playwright test can't import its runner.** A spec in a
  directory with no `node_modules`, run with `npx --yes -p @playwright/test
  playwright test`, fails with "Cannot find package '@playwright/test'". Run
  by hand. So `playwright test` only works in a repo that already depends on
  it.
- **A scratch script through `npx -p playwright node` works.** A script that
  resolves `playwright` with `createRequire` from the npx cache directory on
  `PATH` launched Chromium, loaded a page and took shots. Run by hand.
- **Both ways of focusing match `:focus-visible`.** Two `Tab` presses and
  Playwright's `locator.focus()` each left the target matching
  `:focus-visible`. On a page with the ring clipped by an `overflow: hidden`
  parent, the focus shot showed no ring; with the ring drawn inside
  (`outline-offset: -5px`), it showed. `locator.hover()` showed the hover
  background. Run by hand; images inspected.
- **The command needs no new grant.** `claude -p --allowedTools
  "Bash(npx:*)"` ran `npx --yes -p playwright@1.56.1 node shoot.mjs` with an
  empty `permission_denials` list. Run on haiku.
- **`npx` can resolve two Playwright versions.** `npx --yes playwright
  --version` reported 1.56.1, which matches the cached browser. `npx -p
  playwright` fetched a newer one that wanted a browser build that wasn't
  there. Run by hand. So the script and the browser install must pin one
  version.

## The answer in one paragraph

When the diff changes how an element looks on focus or hover, the run
writes a short script to `<worktree>/.polako-scratch/shoot.mjs` from a fixed
template: load the loopback URL, focus or hover one selector, screenshot at
1280x800. It runs it with `npx --yes -p playwright@<v> node`, where `<v>` is
the version `npx --yes playwright --version` reports, before and after the
change, like any other shot. The selector comes from the code the diff
changed, never from issue or review text. Focus and hover only: a click can
have side effects on a dev server wired to real services, so a state that
needs one stays on the `Unreached:` line. If the script fails, the run falls
back to the plain URL shot and the `Unreached:` line. Nothing new leaves the
machine, and no grant widens.

## What this may read and write

- **The worktree's scratch directory.** One script file under
  `.polako-scratch/`, which ignores itself and is never committed. It's
  written from the template, not composed freely, so a reviewer of the skill
  text knows every call it makes.
- **The dev server, on loopback.** The same server the URL shots already
  start. Focus and hover do what a person tabbing or pointing through the
  page does: a hover may prefetch a route, but neither submits anything.
- **`npx`'s cache.** `-p playwright@<v>` may fetch that package into the
  operator's npm cache, the same as today's `npx --yes playwright`. With
  `playwright install chromium`, it may also fetch a browser, as today.
- **The evidence ref.** Unchanged: the same PNGs, caps and look step, through
  the same publish recipe. The *What leaves the machine* invariant needs no
  edit, since the destination and the content are the same.
- **Issue and review text.** Read as data, as ever. It may describe the
  state ("the ring after Tab"). It never names the selector, the action or
  the URL. Issue text may make a run cheaper, never wider.

## Drafted tickets

Ordered by dependency. Sizes are the shape of the work, not money.

### 1. Shoot focus and hover states from a scratch script

**Problem.** A change that shows only on focus or hover gets shots of the
page as it loads and an `Unreached:` line. The reviewer still has to check
out the branch and tab through it.

**Shape.** `skills/implement-issue/SKILL.md`, Phase 2 step 2: a shot entry
may carry an interaction, `- <slug> — <path> — <routing file> — focus|hover
<selector> — <file that defines it>`, and `Unreached:` shrinks to states
that need a click. Phase 3 step 3 (and step 0 for the before shot): read the
Playwright version once, write the script from a template in the skill
text, with the URL and selector as JSON strings rather than spliced into the
code, and run it with `npx --yes -p playwright@<v> node`. Install the
browser with the same `<v>`. A failed script falls back to the URL shot.
The `focus-change` eval case's graders move from "names the focus state as
unshot" to "shot it": a `tool_used` grader for the pinned `npx -p
playwright@… node` call, and an llm grader that the PR body doesn't list
focus as unreached. A `repo_test.go` pin on the template's calls. An
`interaction-shots` row in `docs/experiments.md`, since skill wording earns
one.

**Done when.** `go test ./cmd/polako/` passes with the new pin. Because
the PR changes an eval case, an unattended run can't grade it (CLAUDE.md,
"The suite is the verification"): the PR says so, and a human runs
`evals/run.sh focus-change visual-change` from the branch before merging.

Estimate: M

### 2. Give the review run the same script

**Problem.** A reviewer who asks for shots of a focus ring gets the page as
it loads and a sentence saying the ring isn't shown.

**Shape.** `cmd/polako/reviewshots.go:reviewShotsHow`: the same template and
the same version pin, cut to the review prompt's length.
`TestReviewShotsMatchTheSkill` (`cmd/polako/reviewshots_test.go`) holds the
two copies of the template's calls to one spelling.

**Done when.** `./scripts/check.sh` is green and the contract test fails
when either copy's `npx -p playwright@<v> node` call drifts.

Depends on 1.

Estimate: S

### 3. Say so in the docs

**Problem.** `docs/behaviour.md` and `docs/security.md` describe capture as
URL-only.

**Shape.** One sentence in `docs/behaviour.md`'s "A visual change shows
itself" paragraph, within its line budget (`docsbudget_test.go`). In
`docs/security.md`'s evidence-ref section, one sentence on the scratch
script: template-only, focus and hover only, no new grant.

**Done when.** `./scripts/check.sh` is green.

Depends on 1.

Estimate: S

## Considered and not proposed

**A scratch spec under `playwright test`.** It needs `@playwright/test` in the
repo's own `node_modules`, which most repos don't have. Measured above.

**A `Bash(node:*)` grant.** It would add an interpreter grant to every run
for one use `npx` already covers.

**Clicks.** A click can submit, sync or delete on a dev server wired to real
services, and nothing in the code tells a safe toggle from a write. A state
that needs one stays on `Unreached:`.

**CDP's `CSS.forcePseudoState`.** It can force `:focus-visible` or `:hover`
without an interaction, but `locator.focus()` already matches
`:focus-visible` (measured above), and the protocol call is Chromium-only
plumbing a reviewer has to trust.

**A browser MCP server.** `buildArgs` passes no `--mcp-config`, and the
original plan rejected it for that reason.

**GIFs and pixel diffs.** A still of the state is what the reviewer asked
for. Motion and diffs wait until a ledger row asks.
