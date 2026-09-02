# Eval suite for the shipped skills

Seven cases that grade what a run *does*, not what a `SKILL.md` says. Each one
scaffolds a scratch git repo, points a stand-in `gh` at fixtures, runs a real
skill invocation, and scores the artifacts left behind.

| case | skill | asserts |
| --- | --- | --- |
| `clear-issue` | implement-issue | a specified issue reaches a PR, plan written first, body ends `Closes #1` |
| `ambiguous-issue` | implement-issue | an under-specified issue produces questions and the `awaiting-answer` label — and no PR |
| `review-gate` | implement-issue | `/code-review` fires, aimed at `issue-1`, before `gh pr create` |
| `resume-existing-plan` | implement-issue | an existing worktree and PLAN.md are resumed, not rewritten |
| `one-turn` | implement-issue | a slow verification step is waited out in the turn, not deferred to one that never comes |
| `plan-vision` | plan-backlog | a vision document becomes labelled, sized, parented proposals — and the gap the backlog already covers is not re-proposed |
| `review-health` | review-health | a repo's planted structural problems become labelled, sized proposals, each resting on a measurement or a named location, the missing size gate proposed as its own issue, and the overlap the backlog already covers left alone |

`one-turn` is the slow one, and knowingly so: its issue asks for before and
after numbers from a benchmark that takes a minute and a quarter each time, and
waiting those out is the behaviour under test. `seed.sh` puts that benchmark in
the scratch repo for this case alone, so the other cases stay quick.

`plan-vision` and `review-health` are the odd shape: their subjects write no
code at all. `plan-vision` seeds a `VISION.md` and an open backlog that already
covers one of the document's four gaps; `review-health` seeds structural
problems into the repo itself — three near-duplicate functions, an oversized
file, no size gate — and an open issue already covering one of them. Both grade
what got created — labels, parenting, body sections, sizes — plus the one thing
these runs must never do, which is write anything but issues.

## Running it

From the repository root — `.` is the plugin, not this directory, and the
manifest it needs is `.claude-plugin/plugin.json` one level up:

```bash
claude plugin eval . --scaffold --allow-tools Bash Write Edit
```

`plugin eval` is itself in early access. Without the entitlement it prints one
line — *`plugin eval` is currently in early access* — and exits having run
nothing. That looks like a suite failure and is not one. `--help` does not tell
you which side of the gate you are on: it prints in full on a CLI that still
refuses to run, and it names no opt-in flag or env var, because the entitlement
is account-side. That one line is the only signal, so read it before reading
anything into a result.

Both flags are required and neither is defaulted on:

- `--scaffold` runs `scaffold.sh`, which is author-supplied bash executed as
  you. Read it before you run it — that is exactly why the CLI makes you ask.
- `--allow-tools` grants the gated tools the skill needs. Without them a run
  stalls on the first `git` call. Only `Bash`, `Write`, `Edit`, `WebFetch` and
  `mcp__*` are gated, so the read-only file tools need no grant.

### Three defaults worth knowing before you spend

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

So a first debugging run, under a ceiling, is:

```bash
claude plugin eval . --scaffold --allow-tools Bash Write Edit \
  --case clear-issue --ablation none --no-publish --keep-temp --max-cost-usd 40
```

`--keep-temp` leaves the scaffold directory behind, which is the difference
between reading a failure and paying for another run to guess at it. The
`40` is the whole debugging session's ceiling, and the flag bounds one
invocation: pass what is left of it on each rerun rather than the same number
again, or six invocations spend six times it.

Useful once it works: `--case <glob>` to run one, `--runs 3` when you want to
know whether a case is flaky rather than whether it works — every case here sets
`runs: 1`, so one run each is what you get otherwise — and `--json` or
`--report <path>` to keep the numbers somewhere.

## Running it by hand

The entitlement may never arrive, so the suite does not depend on it:

```bash
evals/run.sh                       # every case
evals/run.sh clear-issue           # one case
evals/run.sh --no-judge            # skip the llm judge; grade those yourself
evals/run.sh --plugin-dir ../wt    # test a plugin checkout other than this one
evals/run.sh --max-cost 5          # stop before the next case once $5 is spent
```

`run.sh` reproduces what `plugin eval` would do for this suite — scaffold each
case into a fresh workspace under `evals/results/<timestamp>-by-hand/`, run the
case's prompt in a headless session with the plugin loaded (from this checkout,
or from `--plugin-dir` — how an `implement-issue` run drives the suite from its
worktree while invoking the main checkout's copy of this script, so the tool
grant can stay a fixed `Bash(evals/run.sh:*)`), then grade what the run left
behind (`lib/grade.py`). `file_exists` graders are checked mechanically; `llm`
graders go to a judge session — haiku, the CLI's own default judge,
`--judge-model` to override — fed the recorded artifacts, repository state and a
tool-call timeline, all of which is also written to each case's `evidence.md` so
a verdict can be audited rather than trusted. Exit 0 is green; 1 means a grader
failed, a case timed out, or the harness broke, and the per-case line says
which — a harness error is not a skill verdict; `--no-judge` exits 3 until a
human scores what the judge would have; `--max-cost` (dollars, default off)
sums `total_cost_usd` across the cases run so far and exits 4 having skipped the
rest once the cap is reached. To re-run one wobbling case three times, invoke
`evals/run.sh <case>` three times — each invocation gets its own timestamped
results directory.

It costs the same money the real runner would — roughly $0.30–$1.60 per case,
plus cents of judging — and needs `claude`, `git` and `python3`. Two deliberate
divergences from a naive reading of the cases, each argued where it lives:

- The stand-in `gh` rides in on the **launch environment's** `PATH`, because a
  headless `claude -p` ignores the `settings.json` the scaffold writes
  (issue #126).
- `tool_used` graders are reported as `indicator:fired`/`not-fired`, not
  scored: slash-command expansion emits no `Skill` tool call, so they cannot
  fire on a path that never reaches `/code-review` (issue #127) — the CLI's
  own ablation mode demotes them the same way.

Results under `evals/results/` are scratch, and gitignored like the CLI's own.
The durable record of a run is the per-case verdicts quoted in the PR body —
"say what was verified", the convention `CLAUDE.md` sets — and, for tagged
skill experiments, a row in `plans/experiments.md`.

## This suite is deliberately not in CI

`scripts/check.sh` and the CI matrix stay hermetic: no network, no `gh`, no real
`claude`. This suite is all three, and it costs money per run — every case is a
full cycle driven by a live model. So it is opt-in, run by hand, and
`check.sh` does not know it exists.

That is a deliberate exception to the hermetic-tests convention in `CLAUDE.md`,
agreed on issue #9 rather than taken quietly.

An unattended `implement-issue` run is the other caller: when its own commits
change a shipped `SKILL.md` it runs the cases the change touches, on
`--max-cost 5`, and quotes the verdicts in the PR body (`CLAUDE.md`, "The suite
is the verification"). Still opt-in — nothing runs it unless a skill file moved.

The free half of skill coverage lives in `cmd/polako/repo_test.go`, which
asserts the contract-bearing lines of both skills — the review gate, the label
spellings, the branch name, the PR body's shape, the sizing contract — on every
platform on every push. Those tests check the promise is *written*. These cases
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
  subcommand no shipped skill is permitted, so a case cannot
  pass on a call the real run could never make — `defaultTools` is the set for
  `implement-issue`, and for `plan-backlog` it is the write surface its
  `SKILL.md` names, whose `issue list` and `issue create` are outside
  `defaultTools` because the `plan` verb that would grant them has not shipped.
  `issue create` both records and answers: it hands back an incrementing number
  from 100 up, so a plan run can file an epic and parent children to the number
  it got.
- `.claude/settings.json` — puts that `gh` first on `PATH`.
- `CLAUDE.md` — says the project is `repo/`. The prompt is a bare slash command
  with nowhere to name a directory, and Phase 0's `git worktree list` otherwise
  runs wherever the runner started: no repository at all, or — if the workspace
  sits inside one — the wrong one.

It refuses to run in a directory that already holds any of those, so a second
run in the same workspace, or a run pointed at a real project, stops with a
sentence about it rather than half-building on top.

Graders then read fixed paths (`.eval/pr-body.md`, `.eval/labels.log`,
`.eval/comments/0.md`, `.eval/created/*.md`, `repo/.worktrees/issue-1/PLAN.md`)
rather than having to work out where the run put its worktree.

## What a hand-run settled, and what only the CLI can

The suite has now run in anger — every case then in it, by hand, on 2026-08-28
(the "Running it by hand" path above) — so the scaffold, the stand-in `gh` and
the graders are no longer read-only theory. What that run settled:

- **The scratch world works as written.** `lib/scaffold.sh` and `lib/gh-fake.sh`
  behaved as designed on their first execution, including the seeds, and
  `plan-vision`'s bare `VISION.md` prompt was found through the workspace
  `CLAUDE.md` pointer without needing a `repo/` prefix.
- **The skills held.** 32/34 behavioral graders passed. The two reds were both
  genuine catches, not grader noise: a plan run improvised `gh label list`, a
  read outside its granted surface, which the stand-in rejected (issue #128);
  and a `review-gate` run skipped `SKILL.md`'s `--ff-only` mirror refresh and
  branched from `origin/main` instead (issue #131).
- **`.claude/settings.json` is not how the stand-in `gh` gets onto `PATH`**
  under a headless `claude -p` — the launch environment is (issue #126). The
  failure was loud, exactly as this section predicted.
- **`tool_used: Skill` never fires from slash-command expansion.** It fires
  only when a run happens to reach `/code-review` (issue #127), which is why
  the by-hand runner reports it as an indicator instead of scoring it.

`claude plugin eval .` itself has still never run — the entitlement gate at the
top of "Running it" — so everything CLI-specific is still unverified: the
grader key spellings beyond what `--help` shows (including the `regex` grader
recovered from the binary — `name` + `target` + `pattern`, `target:
last_message` reading the agent's final message — which no case uses yet);
whether the real runner's cwd is the workspace the scaffold assumes (if not,
set `EVAL_WORKSPACE` — `lib/scaffold.sh` honours it — rather than rewriting
paths in every case; `run.sh` guarantees the assumption by construction, so
the hand-run settles nothing here); whether the real runner applies the
workspace settings the scaffold writes (if it does, #126 is a by-hand quirk;
if not, every case fails loudly on its first `gh` call); whether its `llm`
graders can read workspace files or only the transcript; and whether
`--ablation none` scores `tool_used` graders it would otherwise treat as
indicators. Check those five on the first entitled run, before reading
anything into scores — then fold the answers in above and delete this section.
