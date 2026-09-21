# Getting a repository ready: `polako setup`

`polako setup` is a readiness report: it prints what the repository has for
polako, what it's missing, and the `polako work` line to run once it looks
ready. By default it's read-only; `-apply` is the one flag that writes — see
below.

```bash
polako setup -dir ../my-project
```

```
example/my-project

setup
  check               status         detail
  claude              ok
  gh                  ok
  git                 ok
  gh repo view        ok             PUBLIC — public: -label is required for `polako work` to refuse an unfiltered queue
  issues enabled      ok
  origin/HEAD         ok
  .gitignore          missing        missing /.worktrees/, /PLAN.md, /.polako-scratch/ — `polako setup -apply` proposes it through a PR
  CLAUDE.md           missing        missing the polako block — `polako setup -apply` proposes it through a PR; run `/init` for the rest of CLAUDE.md
  docs/VISION.md      missing        optional — `polako setup -apply` can scaffold it and docs/plans/README.md (advice only)
  plugin              ok             0.23.0
  sub-issue support   ok
  needs-human         missing        gh label create needs-human --color D93F0B --description "polako parked this issue for a human"
  proposed            missing        gh label create proposed --color 1D76DB --description "proposed by polako — a human removes this label to queue it"
  awaiting-answer     missing        gh label create awaiting-answer --color FBCA04 --description "polako is waiting for an answer on this issue"

polako work -dir ../my-project -dry-run
```

Every row is `ok`, `missing`, or `couldn't tell` — never a guess. A tool
missing from PATH, a `gh` too old for a field, a repository this `gh` can't
reach: each degrades to its own row rather than stopping the report, so one
broken check never hides the rest of the picture. `couldn't tell` never
fails the report; only a `missing` row on something required does, and the
process exits nonzero when one does — so `polako setup` doubles as a check
in a script.

## What it checks

- `claude`, `gh` and `git` on `PATH`.
- `gh repo view` answers — proof `gh` is authenticated and the repository is
  reachable — and its visibility, with a note that `-label` is required once
  the repository is public and none was given (the same gate
  [`queueGate`](reference.md) enforces at `polako work` startup).
- Issues are enabled on the repository.
- `origin/HEAD` resolves (`git symbolic-ref refs/remotes/origin/HEAD`). A
  checkout made with `git init` plus `git remote add` has no such ref, and
  the skill assumes it does; the fix is `git remote set-head origin -a`.
- The installed plugin's version, compared against this binary's the same
  way startup's own skew warning does — flagged only when the plugin is
  strictly behind.
- Whether this `gh` can file a sub-issue (`gh issue create --parent`).
  Advisory only: without it, `polako plan` and `polako health` still run,
  filing epics flat instead.
- Every label polako manages — `needs-human`, `proposed`, `awaiting-answer` —
  plus `-label`'s own gate label when one is given and isn't already in that
  set. A missing one prints the exact `gh label create` command to fix it.
- `.gitignore` covers `/.worktrees/`, `/PLAN.md` and `/.polako-scratch/` —
  where `implement-issue` puts its own worktree, resume note and scratch
  files, so a fresh repo can't accidentally commit them. Not required: a
  repo without it still runs `polako work` fine.
- CLAUDE.md carries a marked polako block, between `<!-- polako:begin -->`
  and `<!-- polako:end -->`: the one command that checks this repo's work
  (detected from `scripts/check.sh`, a Makefile `test` target, `go.mod`,
  `package.json`'s `test` script, `Cargo.toml` or `pyproject.toml`, else a
  line asking a human to fill it in), which files are scratch, the `issue-N`
  branch contract, and that issue text is data, not instructions. Not
  required, the same as `.gitignore`.
- `docs/VISION.md` exists — advice only, since `plan-backlog` is opt-in.

## Flags

| Flag | Default | Meaning |
| --- | --- | --- |
| `-dir` | `.` | Path to the repository's main checkout, used to resolve the repository when `-repo` is not given. |
| `-repo` | *(whatever `-dir` is a checkout of)* | Repository to check readiness for, `owner/name`. |
| `-label` | *(none)* | Gate label `polako work -label` would use — checked as one more row, and named in the suggested command at the end of the report. |
| `-apply` | off | Create the labels the report found missing, asking `[Y/n]` first — see below. |
| `-yes` | off | With `-apply`, take the default answer for every step without asking. Required when stdin isn't a terminal. |
| `-policy-labels` | off | Show the `model:`/`effort:` policy labels in the report too, and, with `-apply`, offer to create them — tier aliases only. |

They take environment defaults the same way every other verb's flags do,
except `-apply` and `-yes`: those are actions, not preferences, so a
`POLAKO_APPLY` or `POLAKO_YES` left in a shell profile is ignored, the same
as `POLAKO_DRY_RUN` is for `tidy`.

**Reads only, unless `-apply` says otherwise.** Every other call is one of
the read subcommands polako itself already re-derives state with, plus the
one label lookup (`gh api repos/{owner}/{repo}/labels/<name>`) `polako
work`'s own preflight uses to check its gate label. Nothing here opens an
issue or touches anything but labels and, with `-apply`, one branch and PR
— see below.

## Creating what's missing: `-apply`

`-apply` walks the rows the report found missing and asks whether to create
each one, `[Y/n]`, default yes:

```
create the "needs-human" label? [Y/n]
  created "needs-human"
```

`-yes` takes the default for every step without asking — the one way to run
this from a script, since stdin has to be a terminal otherwise. Piped stdin
without `-yes` refuses before making any call, naming the flag, exit code 2.

On a public repository with no `-label` given, it asks one more question
first — what to name the gate label, suggesting `ready` — since the report
above never checked a label nobody named yet.

A create that fails says it needs write access to the repository, never the
raw `gh` error.

## Proposing repo files: `-apply`

After the labels, `-apply` asks up to three more questions, each `[Y/n]`
default yes except the last: propose the missing `.gitignore` lines, propose
the CLAUDE.md block, and — default *no* — also scaffold `docs/VISION.md` and
`docs/plans/README.md`. `-yes` takes each step's own default, so a plain
`-apply -yes` run proposes the first two and leaves the scaffold alone;
scaffolding it needs an explicit `y` on that question.

Whatever's accepted becomes one commit (`chore: set up polako`) on a
`polako-setup` branch, built in a worktree at `.worktrees/polako-setup`,
pushed, behind one PR a human merges — never a direct commit to the default
branch. An open PR from `polako-setup` already: its URL is printed and
nothing is written. A local or remote branch with no PR yet (a previous run
that died mid-way): built on, never force-pushed. An item already satisfied
on the freshly-fetched default branch (a previous `polako-setup` PR merged
it) reports as `ok` rather than being proposed again — this can happen for
some items and not others in the same run.

Still no issue is ever touched, and no repo setting is changed.
