# Security

An unattended run is a Claude session with `--permission-mode acceptEdits`
whose only input is issue bodies and comments — attacker-controlled on any
repo that takes outside issues. `polako work` bounds that at two layers,
below; `plan` and `health` run a narrower allowlist of their own, below too.

## `polako work`

**The tool allowlist bounds what a run can do.** `-tools` is enforced by
Claude Code, not the skill's good behaviour. `gh` is granted per subcommand —
`Bash(gh issue view:*)`, `Bash(gh pr create:*)`, a few more — never as a
blanket `Bash(gh:*)`, which would also permit `gh api`, `gh secret set`, `gh
repo delete`. Even a per-verb grant is too wide: `Bash(gh pr:*)` includes `gh
pr merge`; `Bash(gh issue:*)` includes `gh issue edit --add-label`, enough to
pull an unlabelled issue into a `-label`-gated queue; `Bash(gh run:*)`
includes `gh run rerun/cancel/delete` — so a red build is diagnosed with the
read-only `gh pr checks` and `gh run list/view` instead. Need more? Use
`-add-tools`, never widen back to a whole verb.

The skill gets one label command too, to raise `awaiting-answer` when it
stops to ask something — minted per run, pinned to that run's own issue
number (`Bash(gh issue edit 42 --add-label:*)` and its `--remove-label`
twin), so attacker-supplied text can ordinarily reach no further than the
issue already being worked, where the worst it can do is park or unpark
itself. Like every grant here it's a prefix, not a signature — one appended
after the flag still matches — so read this as narrowing the blast radius to
something an audit of the run's own commands would catch, not a boundary.

A review remediation gets one more grant, pinned the same way: `Bash(gh api
repos/you/project/pulls/42/comments:*)`, since `gh` has no `pr` subcommand
that prints per-line diff comments — one PR of one repo, not the blanket
`Bash(gh api:*)`, the entire API, secrets and repo deletion included. The
same prefix caveat applies, harder: anything after `comments` still
matches — a `--method DELETE`, a `../..` the API host resolves back out of.
Granting nothing isn't safer either: a tripped permission prompt hangs a run
in silence until the stall watchdog kills it.

What the allowlist can't close: `Bash(git:*)` includes `git push`, which
opening a PR requires; build commands run whatever the checked-out repo's
scripts contain; `Bash(python:*)`, `Bash(npx:*)`, `Bash(uv:*)`, `Bash(go:*)`
are arbitrary code execution by construction. So it's a narrowing, not a
sandbox — point `-dir` at repos you'd run `make test` in yourself, and drop
interpreter entries from `-tools` you don't need.

The rest of that gap needs a boundary outside the process — egress first,
since a run with a shell has the network, and a prompt injection that gets
something *out* is what the human merge step can't catch.
[hardening.md](hardening.md) covers building your own firewall around a
shift, and why that stays yours rather than a polako flag.

**`-label` bounds *which* issues are eligible.** Applying a label takes
triage permission or better, so it means a maintainer opts each issue in
before the supervisor touches it. An outsider can still file an issue, just
not start a run with it — unless a template's `labels:` key hands them the
gate label on creation. Keep it out of your templates.

```bash
polako work -label ready-for-claude
```

Run it that way on any repo open to outside issues — "a maintainer chose
this one" instead of "anyone can queue an unattended agent".

On a *public* repo the gate isn't advice: `polako work` refuses to start
without a `-label`, the one shape where the risk is structural. `-ungated`
overrules it, an explicit flag so the unfiltered queue is something an
operator says; a [`-dry-run`](reference.md#looking-before-you-leap--dry-run)
may still look without either, since it runs nothing.

Beyond those: Phase 0 tells the skill to read issue and comment text as a
change to make, never instructions addressed to it, and to report anything
that tries to be rather than obey it. Defence in depth behind the two
above — the human merge step is still the last check on what lands.

## `polako plan` and `polako health`

Both run a different skill, unattended the same way, on a far narrower
allowlist of their own (`planTools`, `healthTools`): no `git push`, no `gh
pr`, no interpreters. The whole write surface is `gh issue create` plus a
scratch body file — no PR, no thread, nothing shaped like `-label`'s gate.

What replaces it: every issue such a run creates must carry `proposed`
before it's workable, enforced supervisor-side, not left to the model
remembering `--label`. A label pass runs after the skill exits — always,
crash, cap-kill or Ctrl+C included — normalising every issue the run's
account created since, and `-max-issues` caps how many it can create at all.
So a fully subverted run's worst case is spam behind a label a human
lifts — the same prefix-not-signature, narrowing-not-sandbox caveats above
still apply. See [`plan`](reference.md#planning-a-backlog-unattended-polako-plan)
and [`health`](reference.md#auditing-repository-health-unattended-polako-health).

## What leaves the machine

One thing, and only on request:

| What | Where it goes | Default |
| --- | --- | --- |
| [`-post-summary`](run-data.md#putting-it-on-the-pr--post-summary) | One line of run numbers, as a comment on your own merged PR — readable by exactly the people who can already see that PR. | Off. |

`plan` and `health` don't change this — neither has a `-post-summary` of its
own, and neither posts anything anywhere.

Everything else stays local. Run data is written to disk and read by
nothing but `polako stats`, whose [`-html`](run-data.md#keeping-a-copy--html)
writes those numbers to a second local file and fetches nothing when
opened; the [shift log](reference.md#the-shift-log--log) — the one local
file holding transcript text — is written `0600` to your home directory,
read back by nothing, turned off with `-log off`; `-notify` runs a command
of yours on your own machine; the skill half, being a prompt, collects
nothing at all.

### `-remote`, and why it isn't in that table

[`-remote`](reference.md#watching-a-shift-from-anywhere--remote) is on by
default and used to be the second outward path — session *text*, not
numbers. It isn't, today: no `claude` CLI registers headless runs with
Remote Control — the current one takes `--remote-control` under `-p` and
never starts the bridge. polako stops passing the flag at all, so with
`-remote` on or off, no session content goes anywhere.

Worth stating rather than dropping quietly, because the reverse matters. If
a future CLI registers headless runs and polako passes the flag again, this
table gains a row: a registered session is readable through the claude.ai
account the CLI already authenticates as — the same account running the
model and holding the transcript — reaching you and nobody else, the same
visibility an interactive `claude --remote-control` session has.
`-remote=false` would again be the way to decline it. Until then there's
nothing to decline.
