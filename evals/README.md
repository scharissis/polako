# Eval suite for the implement-issue skill

Five cases that grade what a run *does*, not what `SKILL.md` says. Each one
scaffolds a scratch git repo, points a stand-in `gh` at a fixture issue, runs
`/polako:implement-issue 1` for real, and scores the artifacts left
behind.

| case | asserts |
| --- | --- |
| `clear-issue` | a specified issue reaches a PR, plan written first, body ends `Closes #1` |
| `ambiguous-issue` | an under-specified issue produces questions and the `awaiting-answer` label — and no PR |
| `review-gate` | `/code-review` fires, aimed at `issue-1`, before `gh pr create` |
| `resume-existing-plan` | an existing worktree and PLAN.md are resumed, not rewritten |
| `one-turn` | a slow verification step is waited out in the turn, not deferred to one that never comes |

`one-turn` is the slow one, and knowingly so: its issue asks for before and
after numbers from a benchmark that takes a minute and a quarter each time, and
waiting those out is the behaviour under test. `seed.sh` puts that benchmark in
the scratch repo for this case alone, so the other four stay quick.

## Running it

```bash
claude plugin eval . --scaffold --allow-tools Bash Write Edit
```

Both flags are required and neither is defaulted on:

- `--scaffold` runs `scaffold.sh`, which is author-supplied bash executed as
  you. Read it before you run it — that is exactly why the CLI makes you ask.
- `--allow-tools` grants the gated tools the skill needs. Without them a run
  stalls on the first `git` call.

Useful additions: `--case clear-issue` to run one, `--runs 3` when you want to
know whether a case is flaky rather than whether it works, `--max-cost-usd` if
you want a hard ceiling.

## This suite is deliberately not in CI

`scripts/check.sh` and the CI matrix stay hermetic: no network, no `gh`, no real
`claude`. This suite is all three, and it costs money per run — every case is a
full plan-to-PR cycle driven by a live model. So it is opt-in, run by hand, and
`check.sh` does not know it exists.

That is a deliberate exception to the hermetic-tests convention in `CLAUDE.md`,
agreed on issue #9 rather than taken quietly.

The free half of skill coverage lives in `cmd/polako/repo_test.go`, which
asserts the contract-bearing lines of `SKILL.md` — the review gate, the label
spelling, the branch name, the PR body's shape — on every platform on every
push. Those tests check the promise is *written*. These cases check it is
*kept*.

## How the scratch world works

`lib/scaffold.sh` builds, in the runner's working directory:

- `repo/` — a git repo seeded from `lib/fixture/`, with `origin` pointing at a
  bare repo on disk. `git fetch`, `symbolic-ref refs/remotes/origin/HEAD`,
  `worktree add` and `push` all work, and none of them reach the network.
- `.eval/bin/gh` — a stand-in that answers `issue view` from the case's
  `issue.json` and *records* every write into `.eval/` instead of performing it.
  It refuses any subcommand the unattended allowlist would not grant, so a case
  cannot pass on a call the real run could never make.
- `.claude/settings.json` — puts that `gh` first on `PATH`.
- `CLAUDE.md` — says the project is `repo/`. The prompt is a bare slash command
  with nowhere to name a directory, and Phase 0's `git worktree list` otherwise
  runs wherever the runner started: no repository at all, or — if the workspace
  sits inside one — the wrong one.

It refuses to run in a directory that already holds any of those, so a second
run in the same workspace, or a run pointed at a real project, stops with a
sentence about it rather than half-building on top.

Graders then read fixed paths (`.eval/pr-body.md`, `.eval/labels.log`,
`.eval/comments/0.md`, `repo-issue-1/PLAN.md`) rather than having to work out
where the run put its worktree.

## Known-unverified: this suite has never been executed

It was written in a session that could run neither `claude` nor the scaffold
itself, so `claude plugin eval .` has not been green here even once, and
`lib/scaffold.sh` and `lib/gh-fake.sh` have never been run — only read. Expect
the first run to be a debugging session, not a green one, and budget for it:
start with `--case clear-issue` rather than the whole suite.

The case format was recovered from the CLI binary rather than from
documentation. These are the parts most likely to need a correction:

- **Grader keys.** `file_exists` takes `name` + `path`; `llm` takes `name` +
  `focus` + `criteria`; `tool_used` takes `tool`. There is also a `regex` grader
  taking `name` + `target` + `pattern`, where `target: last_message` reads the
  agent's final message. If a key is wrong, `claude plugin eval` should say so.
- **Where `scaffold_script` runs, and with what working directory.** The
  scaffold assumes the runner's cwd is the workspace the agent then starts in.
  If that turns out to be wrong, set `EVAL_WORKSPACE` — `lib/scaffold.sh`
  honours it — rather than rewriting the paths in every case.
- **Whether `.claude/settings.json` is enough to get the stand-in `gh` onto the
  agent's `PATH`.** If it is not, the failure is loud rather than silent: the
  real `gh` finds an `origin` that is a local path, not a GitHub repo, and
  refuses.
- **Whether `llm` graders can read workspace files.** Several `criteria` name a
  path under `.eval/`. If graders only see the transcript, those need rewording
  against the transcript instead — the facts are all visible there too.
- **Whether `tool_used: Skill` ever fires here.** `SKILL.md` sets
  `disable-model-invocation: true`, so the skill is only ever reached as a slash
  command, and it is not obvious that arrives as a `Skill` tool call. The
  ablation notes describe that grader as a plugin-fired indicator rather than
  part of the score, which should make it harmless either way — but if it is
  scored and always fails, drop it from all five cases. It is the one grader
  here that asserts nothing about the skill's behaviour.

Fix what the first run finds and delete this section.
