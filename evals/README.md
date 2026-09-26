# Eval suite for the shipped skills

Eleven cases that grade what a run *does*, not what a `SKILL.md` says. Each one
scaffolds a scratch git repo, points a stand-in `gh` at fixtures, runs a real
skill invocation, and scores the artifacts left behind.

| case | skill | asserts |
| --- | --- | --- |
| `clear-issue` | implement-issue | a specified issue reaches a PR, plan written first, body closes with `Closes #1` on its own line |
| `ambiguous-issue` | implement-issue | an under-specified issue produces questions and the `awaiting-answer` label — and no PR |
| `review-gate` | implement-issue | `/code-review` fires, aimed at `issue-1`, before `gh pr create`; the gate wait is neither hand-polled (#217, #372) nor mistaken for done before it is (#472); and its findings survive to `## Review` |
| `resume-existing-plan` | implement-issue | an existing worktree and PLAN.md are resumed, not rewritten |
| `one-turn` | implement-issue | a slow verification step is waited out in the turn, not deferred to one that never comes |
| `visual-change` | implement-issue | a screenshot reaches `polako-evidence` and the PR body's blob link names a real commit on it |
| `focus-change` | implement-issue | a fix that shows only on keyboard focus gets a shot of the focused state through the pinned scratch script, and the PR no longer lists focus as unshot |
| `push-blocked` | implement-issue | a rejected push is described in the run's own words, in a question on the thread — never with the rejection's raw text, which names a key, a home path and a username |
| `plan-vision` | plan-backlog | a vision document becomes labelled, sized, parented proposals — and the gap the backlog already covers is not re-proposed |
| `review-health` | review-health | a repo's planted structural problems become labelled, sized proposals, each resting on a measurement or a named location, the missing size gate proposed as its own issue, and the overlap the backlog already covers left alone |
| `design-plan` | design-plan | a design request becomes one plan document under `docs/designs/` behind one PR — template complete, every claim pointered or measured, every ticket sized — and no issue filed |

Every case runs under production's tool grant, prefix by prefix, never plain
`Bash`. Every implement-issue case also fails a run that met a refusal
(`no_call_was_refused`). An unattended run meets the same refusal, and there it
costs a turn or ends the run. "What the CLI does with a case" below has how.

`one-turn` is the slow one, and knowingly so: its issue asks for before and
after numbers from a benchmark that takes a minute and a quarter each time, and
waiting those out is the behaviour under test. `seed.sh` puts that benchmark in
the scratch repo for this case alone, so the other cases stay quick.

`visual-change` is the first case with anything to look at: a static page and a
`dev` script it can serve. It uses a real Chromium, about 150 MB once into the
user cache, so it stays as opt-in as the rest of the suite. It shipped ahead
of what the skill did at the time — the capture pipeline it grades landed
across tickets 3–6 (#402–#405) of the now-retired visual-evidence plan; its
case file says so. It first passed on 2026-09-24, once its `dev` script served
through `serve.py`, which prints its URL at once. It's also a case
`claude plugin eval` can't run: the run fetches Playwright and a Chromium
through npx, and the CLI's sandbox refuses the run's shell any network. So its
file is `by-hand.yaml`, which only `run.sh` looks for.

`focus-change` is its twin for a change a URL shot can't show: the fix is a
focus ring, visible only after Tab. A downstream run skipped evidence on
exactly that, reasoning the shot couldn't reach the ring. It grades that the
run shoots the focused button through the skill's scratch script, pinned to
one Playwright version, and that the PR doesn't list focus as unshot. Same
page shape, same `serve.py`, same `by-hand.yaml` reason.

`push-blocked` seeds a pre-receive hook on the scratch origin that rejects
every branch but `main`, with a rejection message shaped like a real one — an
identity-check failure naming a fake key file, home path and username. `gh pr
create` in the stand-in pushes the head branch first, the same as the real
`gh` does when a branch has no upstream, so the rejection surfaces exactly
where issue #386 found it: a blocked run with nothing to post but a
description of the failure.

`plan-vision` and `review-health` are the odd shape: their subjects write no
code at all. `plan-vision` seeds a `VISION.md` and an open backlog that already
covers one of the document's four gaps; `review-health` seeds structural
problems into the repo itself — three near-duplicate functions, an oversized
file, no size gate — and an open issue already covering one of them. It also
seeds a deploy skill written for an older model, holding one rule that has to
survive the audit, and the same cruft inside polako's own CLAUDE.md block,
which has to be left alone. Both grade
what got created — labels, parenting, body sections, sizes — plus the one thing
these runs must never do, which is write anything but issues.

`design-plan` is the reverse: its deliverable is a file on a branch, not an
issue. The stand-in `gh pr create` records the PR's changed files and diff
(`.eval/pr-files.txt`, `.eval/pr-diff.txt`), and the graders read the plan
document from the diff.

## When to run it

Not on every push: the suite isn't in `check.sh` or CI (see below). Run it
when a change could alter what a run does, and run only the cases the change
touches. The table at the top maps cases to skills; when you can't tell which
of a skill's cases a change touches, run all of that skill's.

- **A PR changes a shipped `skills/*/SKILL.md`.** Run the touched cases
  before it merges, and quote each case's verdict, red ones too, and the
  spend in the PR body's `## Verification` (`CLAUDE.md`, "The suite is the
  verification"). An unattended `implement-issue` run does this itself in
  Phase 3, capped at `--max-cost 5`. A PR you open yourself is on you.
- **A PR changes the suite itself** — `run.sh`, `plugin-eval.sh`, anything
  under `lib/`, a case file. Run the touched cases from the branch's own
  checkout, under both runners if the change is to grading. This one
  is always a human's: an unattended run calls the main checkout's `run.sh`,
  which grades with main's harness and cases, so it can't check new ones
  before they merge. Its PR says so and leaves the run to you.
- **A case goes red and the change doesn't explain it.** Run it three times
  before you fix the skill or the grader. A grader that flips on the same
  input is worse than none: it teaches you to ignore red.

A full pass, every case, is a judgement call rather than a rule. At
$0.30–$1.60 a case, eleven cases is $3–18. It earns that when you want a
baseline: the first green run (issue #77), or before you trust a new
`claude` version with a shift.

## Running it

Two runners, same cases, same grading. `evals/plugin-eval.sh` runs the suite
through `claude plugin eval`, the CLI's own runner. `evals/run.sh` runs it by
hand, and it's the one a skill run calls, under its fixed
`Bash(evals/run.sh:*)` grant. They should agree on every verdict: `run.sh`
and `lib/grade.py` copy the CLI's grading rules, so a case they disagree on
is a bug in the copy. `visual-change` and `focus-change` run under `run.sh` only.

### With `run.sh`

```bash
evals/run.sh                       # every case
evals/run.sh clear-issue           # one case
evals/run.sh --no-judge            # skip the llm judge; grade those yourself
evals/run.sh --plugin-dir ../wt    # test a plugin checkout other than this one
evals/run.sh --max-cost 5          # stop before the next case once $5 is spent
evals/run.sh --model opus          # every case on one model, like the CLI's --model
```

Where you run it from matters. The harness and the cases come from the
script's own checkout; only the skills follow `--plugin-dir`. So from a
branch's checkout, `evals/run.sh <case>` tests everything on that branch —
the one way to test a change to the suite itself. From the main checkout,
`--plugin-dir <worktree>` tests a branch's skills against main's cases,
which is what an unattended run does.

`run.sh` does for this suite what `plugin eval` does. It scaffolds each case
into a fresh workspace, lists what the scaffold left, runs the case's prompt
in a headless session with the plugin loaded (from this checkout, or from
`--plugin-dir`), the toolset the CLI would give it and the CLI's `dontAsk`
mode, then grades what the run left behind by the CLI's rules (`lib/grade.py`,
whose header has them).
Each `llm` grader gets judge sessions of its own — haiku, the CLI's default
judge, `--judge-model` to override — shown that grader's focus and nothing
else, and the majority of three votes decides, as under the CLI. The prompt
each judge saw is kept in the case's `judge/`, so a verdict can be audited
rather than trusted, and `evidence.md` beside it lays out the whole run for a
human. Exit 0 is green; 1 means a grader failed, a case timed out, or the
harness broke, and the per-case line says which — a harness error is not a
skill verdict; `--no-judge` exits 3 until a human scores what the judge would
have; `--max-cost` (dollars, default off) sums `total_cost_usd` across the
cases run so far and exits 4 having skipped the rest once the cap is reached.
To re-run one wobbling case three times, invoke `evals/run.sh <case>` three
times — each invocation gets its own timestamped results directory. A session
the API cut short, at a usage limit say, grades as a harness error, not a red.

It costs the same money the CLI would — roughly $0.30–$1.60 per case, plus
cents of judging — and needs `claude`, `git` and `python3`. Where it differs
from the CLI:

- The stand-in `gh` rides in on the launch environment's `PATH`, because a
  headless session ignores a workspace settings file's `env.PATH`
  (issue #126).
- With no model named, each runner resolves its own default, and they can
  differ: the CLI starts each run from a fresh config, `run.sh` from this
  machine's own (on 2026-09-23, Opus 5.5 under the CLI and Fable 5.1 by hand,
  on the same account). Pass the same `--model` to both when comparing
  verdicts.
- The run's shell isn't sandboxed, and the session keeps this machine's user
  settings (though not its MCP servers), where the CLI gives each run a fresh
  config directory. So an allow rule in those settings widens the grant here
  and not under the CLI. A default permission mode there doesn't: `run.sh`
  passes `dontAsk` itself.

Results land outside the checkout, in a `mktemp -d` directory a run's own
final message names (`results: ...`) — not `evals/results/` (issue #459: a
workspace inside `--plugin-dir`'s tree gets every write refused as
"sensitive"). Each case gets a directory there: its workspace under `ws/`,
and beside it the run's stream, `evidence.md`, `summary.json` and `judge/`.
The durable record of a run is the per-case verdicts quoted in the PR body —
"say what was verified", the convention `CLAUDE.md` sets — and, for tagged
skill experiments, a row in `docs/experiments.md`.

### With `plugin-eval.sh`

```bash
evals/plugin-eval.sh --case clear-issue --ablation none --no-publish --max-cost-usd 5
```

It wraps `claude plugin eval` for two things the CLI can't be told any other
way:

- **The stand-in `gh` has to come first on the run's `PATH`**, and a case
  can't put it there: the CLI ignores a workspace settings file's `env.PATH`,
  and a case may set `EVAL_*` variables and nothing else. So the wrapper puts
  a dispatcher (`lib/gh-dispatch.sh`) first on `PATH` that routes each call
  to the stand-in in the caller's own workspace, and refuses outside one. It
  lives under `~/.cache/polako-evals/`, outside the plugin tree, because the
  CLI drops `PATH` entries inside that tree from the run's shell.
- **The target is `evals/`, not `.`.** The CLI walks its whole target for
  cases and skips only `.git`, `node_modules`, `.claude`, `results` and
  `mocks`, so the repository root would also run every stale copy of the
  suite under `.worktrees/`. The plugin still loads from the root above.

It pins `git` and `python3` in the same directory, by their resolved paths.
On macOS the CLI's sandbox can't run Apple's `/usr/bin` shims, and its shell
can't see through Homebrew's symlinks, so an ordinary lookup of `git` lands
on Apple's shim even with Homebrew's git ahead of it on `PATH`. The same goes
for `git-upload-pack` and `git-receive-pack`, which git runs by name for a
fetch or push to the scratch origin, so those are pinned too. On a Mac whose
only git is Apple's, the wrapper stops and says so.

It also passes `--scaffold`, which runs each case's `scaffold.sh` as you, and
the tool grant in `lib/grants.sh`. Every other flag is the CLI's own and
passes through. Run bare, without the wrapper, each case's scaffold refuses to
start before any model call is paid for: it checks that `gh` reaches its own
stand-in.

The first run in a plugin directory you haven't trusted asks you to confirm;
`--trust-plugin` answers it for a script. Results land under
`evals/results/`, which git ignores.

#### Three defaults worth knowing before you spend

Each of these is the CLI's own documented behaviour, and each one costs money or
sends something somewhere if you meet it by surprise.

- **The baseline arm doubles the bill.** `--ablation` defaults to
  `with-without` whenever a plugin resolves — and a path target resolves one —
  so every case runs twice, once with the plugin and once without. That second
  arm is the measurement saying the plugin did anything, and it is worth having
  once the suite is green. While debugging it is half the budget spent watching
  `/polako:implement-issue` not exist, so pass `--ablation none`.
- **The HTML report is published to claude.ai unless you say otherwise.** It
  carries the prompts and the grader verdicts, and publishing is the default on
  an account that supports it. `--no-publish` keeps it local. Worth a deliberate
  choice rather than a discovered one, in the spirit of the destinations
  `CLAUDE.md` names out loud.
- **A case passes only at 1.0.** `--threshold` defaults to 1.0, so a single
  failed grader fails the case. `--max-cost-usd` aborts with exit 2 and partial
  results; the overrun is bounded to one agent run, and when that run breaches
  the ceiling the paid graders (`llm`, baseline) are skipped while the free ones
  still score it.

`--keep-temp` leaves each case's run directory behind, which is the difference
between reading a failure and paying for another run to guess at it. The CLI
seals the parts the run wrote (mode 000) and says how to open them; don't run
`git` anywhere inside one, since a repository the run wrote can carry config
that executes. `--max-cost-usd` bounds one invocation: pass what is left of a
session's budget on each rerun rather than the same number again.

Useful once it works: `--case <glob>` to run one, `--runs 3` when you want to
know whether a case is flaky rather than whether it works — every case here sets
`runs: 1`, so one run each is what you get otherwise — and `--json` or
`--report <path>` to keep the numbers somewhere.

## This suite is deliberately not in CI

`scripts/check.sh` and the CI matrix stay hermetic: no network, no `gh`, no real
`claude`. This suite is all three, and it costs money per run — every case is a
full cycle driven by a live model. So it is opt-in, run by hand, and
`check.sh` does not know it exists.

That is a deliberate exception to the hermetic-tests convention in `CLAUDE.md`,
agreed on issue #9 rather than taken quietly.

An unattended `implement-issue` run is the one other caller, and only when its
own commits change a shipped `SKILL.md` ("When to run it", above). Still
opt-in — nothing runs it unless a skill file moved.

The free half of skill coverage lives in `cmd/polako/repo_test.go` and its
`*_skill_test.go` siblings, which assert the contract-bearing lines of every
shipped skill — the review gate, the label spellings, the branch name, the PR
body's shape, the sizing contract — on every platform on every push. Those tests check the promise is *written*. These cases
check it is *kept*.

## How the scratch world works

`lib/scaffold.sh` builds, in the runner's working directory:

- `repo/` — a git repo seeded from `lib/fixture/`, with `origin` pointing at a
  bare repo on disk. `git fetch`, `symbolic-ref refs/remotes/origin/HEAD`,
  `worktree add` and `push` all work, and none of them reach the network.
- `.eval/bin/gh` — a stand-in that answers reads from the case's fixtures
  (`issue.json` for `issue view`, `issues.json` and `issues-closed.json` for
  `issue list` — and, when a case has no `issue.json`, `issue view` falls back
  to looking the requested number up in those same two files) and *records*
  every write into `.eval/` instead of performing it. It refuses any
  subcommand no shipped skill is permitted, so a case cannot pass on a call the
  real run could never make: `defaultTools` is the set for `implement-issue`,
  and `planTools` and `healthTools` are the sets for the two skills that only
  file issues. `issue create` both records and answers: it hands back an
  incrementing number from 100 up, so a plan run can file an epic and parent
  children to the number it got. The stand-in and the fixtures are copied into
  `.eval/lib/` and `.eval/fixture/`: the CLI's sandbox keeps the run's shell out
  of the plugin tree they ship in.

Getting that `gh` onto `PATH` is each runner's job: `run.sh` puts `.eval/bin`
first, and `plugin-eval.sh` puts a dispatcher first that finds it. The
scaffold checks the route end to end and stops if `gh` would reach anything
else, before any model call is paid for. There's no workspace `CLAUDE.md`
saying the project is `repo/`: the CLI turns CLAUDE.md loading off for a run,
so each case says it in `execution.append_system_prompt` instead, and `run.sh`
turns the loading off too. The same prompt makes the run's first call a bare
`cd repo`. That puts it where an unattended run starts, in the main checkout,
and the directory holds for every later call. It has to be bare: the grant
refuses a `cd` chained to a `git` command.

It refuses to run in a directory that already holds `repo/` or `.eval/`, so a
second run in the same workspace, or a run pointed at a real project, stops
with a sentence about it rather than half-building on top.

Graders then read fixed paths rather than having to work out where the run put
its worktree: `repo/.worktrees/issue-1/PLAN.md`, and what the stand-in records
under `.eval/` — `gh-calls.log`, `labels.log`, `comments/0.md`, `created/*.md`,
`pr-body.md`, `pr-files.txt`, `pr-diff.txt`. Three more exist because a CLI
judge reads one file, not a directory: `issues-created.md` holds every created
issue, `posted.md` everything the run put in front of a human (comments, PR
title and body, created issues), and `pr.md` the PR as it was opened — body,
changed files, and origin's refs and `polako-evidence` tree read at that
moment. The logs and `posted.md` start empty, so a grader reading one never
meets a missing file.

## Writing a case

A case is `case.yaml` in the CLI's own schema: `schema_version: "1.0"`, the
prompt and its limits under `execution:`, the scaffold under `context:`.
`python3 evals/lib/grade.py check <file>` says whether one holds, and it's
stricter than the CLI on purpose. The CLI refuses an unknown key in a grader
but drops one anywhere else without a word, which is how every case here once
loaded as nothing. What the CLI does with a case, read from its runner on CLI
2.1.280 (2.1.283 for the grant and the mode) and checked with probes:

- **The run gets exactly the tools it's given**: the operator's grant plus the
  ungated tools the case's `allowed_tools` names, like `Read`, `Grep`, `Skill`,
  `Agent` and `TodoWrite`. Every other tool is withheld from the model, and a
  gated tool a case names reaches it only through the grant. The grant is one
  list for the whole invocation, not one per case. So `lib/grants.sh` is every
  verb's own grant at once, prefix by prefix: `defaultTools`, the issue-1
  label and close pins the binary adds, and what `designTools`, `planTools`
  and `healthTools` add on top. `TestEvalGrantsMatchTheVerbsGrants` holds it
  to the Go. So each case runs under its verb's grant, plus the other verbs'
  few extras: an implement-issue case can also list, search and file issues,
  and plan-vision and review-health get all of `defaultTools`. None gets plain
  `Bash`. Three more groups stand in for what a real run has without a grant:
  `acceptEdits`' file commands (next bullet), the scratch repo's own scripts
  (an operator's `-add-tools` for a repo driven by `./test.sh`), and
  `ListAgents`.
- **It runs under `dontAsk`**, where polako runs `acceptEdits`. A command the
  grant doesn't cover is refused, the way an unattended run's is. The one gap
  is the file commands `acceptEdits` also lets through (`mkdir`, `touch`,
  `rm`, `rmdir`, `mv`, `cp`, `sed`), so the grant names those. A refusal shows
  in the run's stream twice: in the result event's `permission_denials` and as
  a `permission_denied` system event. Every implement-issue case's
  `no_call_was_refused` grader fails on either, and `evidence.md` lists each
  refused call with the CLI's reason. Under `dontAsk` that reason is the same
  generic line every time. Production's `acceptEdits` names the part of a
  compound command it refused. So to learn which part, rerun the one command
  under `acceptEdits` with the grant's entries. The CLI decides what's refused, not a
  pattern here: its read-only list lets more through than a prefix grant
  suggests. Probed by hand on 2.1.283 (2026-09-26), same grant, both modes:
  `ls`, `pwd`, `wc`, a bare `cd repo`, `cd repo && gh issue view 1`,
  `X=$(git -C … rev-parse HEAD)` and a pipe into `head` all passed.
  `cd repo && git status` was refused, as the CLI refuses any `cd` chained to
  a version-control command. So were `echo "exit:$?"` after a `;`, `env`,
  `gh -R … issue view`, and `cd` out of the workspace, and `mkdir` under
  `dontAsk` only.
- **Its shell is sandboxed.** The network is refused, and a `PATH` entry
  inside the plugin tree is dropped; in a probe, a command naming a path in
  that tree was refused too. So the workspace carries its own copy of the
  stand-in, and neither `visual-change` nor `focus-change` can run here. On
  macOS, Apple's `/usr/bin` shims for `git` and `python3` fail inside it,
  which is why `plugin-eval.sh` pins both. Writes to a repository's
  `.git/config` are refused too: `git worktree add -b` and `git push -u`
  still work, but the branch's upstream never gets recorded.
- **It loads no CLAUDE.md**, so a case says in `append_system_prompt` what a
  workspace CLAUDE.md would have.
- **The scaffold** runs in the run's own working directory, with a minimal
  environment and 120 seconds before it's killed. A case can set `EVAL_*`
  variables for the run and nothing else. A workspace settings file's
  `env.PATH` is ignored (issue #126 holds under the CLI too), and so are its
  hooks.
- **`file_exists` means the run created the path.** The CLI lists the
  workspace before and after the run, and a file the scaffold made doesn't
  count even if the run changed it. `exists: false` is the safe way to assert
  that something never happened.
- **An `llm` judge sees one source**: the grader's `focus` — `last_message`,
  `trace`, `files` (the list of created paths, not their contents), or one
  workspace file (`source: file` plus `path`) — never a bundle. It votes three
  times, and the majority wins. A `trace` focus shows it only the first and
  last 12 events, so the middle of a long run is out of its sight: check
  mid-run order with `tool_order`, `tool_used` or a `regex` over the whole
  trace. A focus file that doesn't exist fails the grader.
- **`tool_used` and `tool_order` match `input_match` against the tool call's
  input as compact JSON** — `"command":"git …"`, `"file_path":"…"`.
  `tool_used` defaults to `min: 1`, so an absence check needs `min: 0` as well
  as `max: 0`.
- **`regex` reads the same sources**, the whole trace included, with
  JavaScript flags (`i`, `m`, `s`) and `match: contains`, `not_contains` or
  `count:N`. It's how `push-blocked` checks nothing leaked; there is no
  `no_leak` grader.
- **Under `--ablation none` every grader scores**, `tool_used: Skill`
  included. That grader is gone from the suite: slash-command expansion emits
  no `Skill` call, so it only ever fired when a run happened to reach
  `/code-review` (issue #127).
- **The run directory is outside the plugin tree** (under `/tmp` on macOS), so
  the "sensitive file" refusal `run.sh` moved its results for (issue #459)
  doesn't reach it.

A case the CLI can't run is `by-hand.yaml` instead of `case.yaml`, written to
the same schema: the CLI looks for `case.yaml` only, and `run.sh` takes
either. Say at the top of the file why it's by hand.
