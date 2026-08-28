# Security

An unattended run is a Claude session with `--permission-mode acceptEdits`
whose only input is issue bodies and comments. On any repository that accepts
issues from outside the team, that input is attacker-controllable. Two things
constrain it, and they work at different layers:

**The tool allowlist bounds what a run can do.** `-tools` is enforced by Claude
Code itself, not by the skill's good behaviour, so it does not depend on the
model declining a request. gh is granted per subcommand — `Bash(gh issue
view:*)`, `Bash(gh pr create:*)` and a few more — rather than as a blanket
`Bash(gh:*)`, which would also permit `gh api`, `gh secret set` and `gh repo
delete`. Even a per-verb grant is too wide: `Bash(gh pr:*)` includes
`gh pr merge`, `Bash(gh issue:*)` includes `gh issue edit --add-label`, which is
enough to pull an unlabelled issue into a `-label`-gated queue, and
`Bash(gh run:*)` includes `gh run rerun`, `gh run cancel` and `gh run delete` —
so a red build is diagnosed with `gh pr checks`, `gh run list` and
`gh run view`, which only read. If
your project genuinely needs more, add it explicitly with `-add-tools` rather
than widening back to a whole verb.

The skill does need one label command, to raise `awaiting-answer` when it stops
to ask something. Rather than granting `gh issue edit` at large, the supervisor
mints that grant per run and pins it to the issue number that run was
dispatched for — `Bash(gh issue edit 42 --add-label:*)` and its `--remove-label`
twin. Ordinarily the furthest attacker-supplied issue text can then reach is the
issue the run is already working on, where the worst it can do is park or unpark
itself. Like every entry in the list this is a prefix, not a signature: `gh issue
edit` takes several numbers, and one appended *after* the flag still starts with
the granted prefix. So read it as narrowing the blast radius from every issue in
the repository to something an audit of the run's own commands would catch —
which is what the rest of this section says about the allowlist generally.

A review remediation gets one more, minted and pinned the same way. Most of a
review's substance is in the comments left on individual lines of the diff, and
gh has no `pr` subcommand that prints them, so that run alone is granted
`Bash(gh api repos/you/project/pulls/42/comments:*)` — one PR of one repository,
where a blanket `Bash(gh api:*)` would be the entire GitHub API, secrets and
repository deletion included. Ordinarily the furthest it reaches is the comment
thread of that one PR. The same caveat as above applies, and harder: this is a
prefix, not a signature, so anything appended after `comments` still matches —
a `--method DELETE`, or a `../..` the API host resolves back out of the path.
Read it, like the label grant, as narrowing the blast radius to something an
audit of the run's own commands would catch, not as a boundary. Granting nothing
would not be safer either: an unattended run that trips a permission prompt hangs
in silence until the stall watchdog kills it.

What the allowlist does *not* close, and cannot: `Bash(git:*)` includes
`git push`, which is what opening a PR requires; the build commands run
whatever the checked-out repo's scripts contain; and `Bash(python:*)`,
`Bash(npx:*)`, `Bash(uv:*)` and `Bash(go:*)` are arbitrary code execution by
construction — `python -c` can run anything the user can, gh included. So the
allowlist is a narrowing, not a sandbox. Point `-dir` at repositories you would
run `make test` in yourself, and drop the interpreter entries from `-tools` if
your project does not need them.

**`-label` bounds *which* issues are eligible.** Applying a label takes triage
permission or better, so requiring one means a maintainer has to opt each issue
in before the supervisor will touch it. An outsider can still file an issue;
they just cannot start a run with it — unless an issue template hands them the
label, since the `labels:` key on a template or issue form is applied on
creation whoever files it. Keep the gate label out of your templates.

```bash
polako work -label ready-for-claude
```

On any repository open to issues from outside the team, run it that way. It is
the difference between "anyone can queue work for an unattended agent" and
"a maintainer chose this one".

On a *public* repository the gate is not just advice: `polako work` refuses to
start there without a `-label`, because that is the one repository shape where
the risk is structural rather than a judgement call. `-ungated` overrules it —
an explicit flag, so choosing the unfiltered queue is something an operator
says rather than something that happens — and a [`-dry-run`](reference.md#looking-before-you-leap--dry-run)
may still look without either, since it runs nothing and seeing the queue is
how you decide what to label.

Beyond those, the skill is told in Phase 0 to read issue and comment text as a
description of a change to make, never as instructions addressed to it, and to
report anything that tries to be — rather than obey it — in the PR body. That
is a mitigation, not a boundary: treat it as defence in depth behind the two
above, and keep the human merge step as the last check on what actually lands.

## What leaves the machine

One thing, and only on request:

| What | Where it goes | Default |
| --- | --- | --- |
| [`-post-summary`](run-data.md#putting-it-on-the-pr--post-summary) | One line of run numbers, as a comment on your own merged PR — readable by exactly the people who can already see that PR. | Off. |

Everything else stays local. Run data is written to your disk and read by
nothing but `polako stats`, whose [`-html`](run-data.md#keeping-a-copy--html) writes those
numbers to a second local file and fetches nothing when you open it; the
[shift log](reference.md#the-shift-log--log) — the one local file that holds
transcript text rather than numbers — is written `0600` to your home
directory, read back by nothing, and turned off with `-log off`; `-notify`
runs a command of yours on your own machine; and the skill half, being a
prompt, collects nothing at all.

### `-remote`, and why it is not in that table

[`-remote`](reference.md#watching-a-shift-from-anywhere--remote) is on by
default and used to be listed here as the second outward path — the one
carrying session *text* rather than numbers. It is not one today. No `claude`
CLI registers headless runs with Remote Control: the current one accepts
`--remote-control` under `-p` and never starts the bridge. polako therefore
stops passing the flag at all, so with `-remote` on or off, no session content
goes anywhere.

That is worth stating rather than quietly dropping, because the reverse is what
would matter to you. If a future CLI does register headless runs and polako
starts passing the flag again, this table gains a row and this section goes
back to arguing the trade: a registered session is readable through the
claude.ai account the CLI is already authenticated as — the same account that is
running the model and already holds the transcript — over Claude Code's own
channel, reaching you and nobody else. That is the same visibility an
interactive session started with `claude --remote-control` has, and
`-remote=false` would again be the way to decline it. Until then there is
nothing to decline.
