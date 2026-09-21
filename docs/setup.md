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
issue or touches anything but labels.

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
raw `gh` error. The whole write surface is `gh label create`: no issue, no
repo file, no PR — see `docs/plans/setup.md` for what those become later.
