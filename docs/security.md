# Security

An unattended run is a Claude session with `--permission-mode acceptEdits`
whose only input is issue bodies and comments — attacker-controlled on any
repo that takes outside issues. `polako work` bounds that at two layers,
below; `plan` and `health` run a narrower allowlist of their own, below too,
and `design` runs `work`'s on one issue.

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

Every remediation run — rebase, red check, review — gets one write, pinned
the same way: `Bash(gh pr comment 42 --body-file:*)`. Its prompt asks it to
say on the PR what it changed, or why a change to the branch can't fix it, and
without the grant that finding stays in a transcript. The flag is inside the
pin so the prefix can't match PR 420, and so comment text never passes through
shell quoting. Prefix caveat again: `--edit-last` or `--delete-last` appended
still match, which reaches your account's own comments on that one PR.

What the allowlist can't close: `Bash(git:*)` includes `git push`, which
opening a PR requires; build commands run whatever the checked-out repo's
scripts contain; `Bash(python:*)`, `Bash(npx:*)`, `Bash(uv:*)`, `Bash(go:*)`
are arbitrary code execution by construction. `Bash(evals/run.sh:*)` is one
named repo script in the default set — dormant on any repo without that path;
on polako itself it runs the eval suite, which spawns nested `claude`
sessions and spends money, so read it with the interpreter entries, not as
something smaller: the skill invokes it with `run.sh --max-cost`, but a
prefix grant doesn't force the argument. So it's a narrowing, not a
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
gate label on creation. Keep it out of your templates; `polako setup` checks
for this too (docs/setup.md) and fails its report if it finds one.

`setup` marks the gate label with a fixed description, and `status` reads
that marker back to scope itself when no `-label` is given. Trusting it adds
no one: creating or editing a label takes the same triage rights as applying
it. The description is only compared against one fixed string — never
parsed, never printed; only the label's name is.

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

A `-label` naming a label the repository has never defined refuses too,
public or not: without this, `-label typo` would pass the gate above and
drain an empty queue all shift, reported as success. `polako status` never
refuses — it reads only — so it prints the same message as a note instead
and carries on.

Beyond those: Phase 0 tells the skill to read issue and comment text as a
change to make, never instructions addressed to it, and to report anything
that tries to be rather than obey it. Defence in depth behind the two
above — the human merge step is still the last check on what lands.

## `polako plan` and `polako health`

Both run a different skill, unattended the same way, on a far narrower
allowlist of their own (`planTools`, `healthTools`): no `git push`, no `gh
pr`, no interpreters. The whole write surface is `gh issue create` plus a
scratch body file — no PR, no thread, nothing shaped like `-label`'s gate.

`healthTools` also grants `Skill(claude-api)`, scoped to that one skill, for
the prompt audit. Loading it grants no tool of its own. A `claude -p` probe
on CLI 2.1.280, with the grant in place, still refused `python3`, `curl` and
WebFetch. That probe also showed that `--allowedTools` bounds tools, not
skills: most skills, `polako:implement-issue` included, load under `-p`
with no grant at all. Loading one widens nothing, because every tool it
reaches for is still checked against the list.

What replaces it: every issue such a run creates must carry `proposed`
before it's workable, enforced supervisor-side, not left to the model
remembering `--label`. A label pass runs after the skill exits — always,
crash, cap-kill or Ctrl+C included — normalising every issue the run's
account created since, and `-max-issues` caps how many it can create at all.
So a fully subverted run's worst case is spam behind a label a human
lifts — the same prefix-not-signature, narrowing-not-sandbox caveats above
still apply. See [`plan`](reference.md#planning-a-backlog-unattended-polako-plan)
and [`health`](reference.md#auditing-repository-health-unattended-polako-health).

## `polako design`

`design` is `work`'s per-issue path on one issue, so its allowlist is
`work`'s plus two reads (`gh issue list`, `gh search issues` — a design
cites the open backlog), and every caveat above holds: prefix not signature,
narrowing not sandbox. Its write surface is `work`'s on that one issue —
the `issue-N` branch, one PR adding a file under `docs/designs/`, the
thread's question and pinned label edits — plus two label writes of its
own: preflight adds `design` to a `-issue N` that lacks it, and `-brief`
files the request issue itself. That issue is the one the binary creates
without `proposed`: you typed it at the command line, and the `design`
label already keeps `work` off it. The skill never creates an issue and
never touches the evidence ref.

**No queue, so no public-repo gate.** `work` refuses an unfiltered public
backlog because anyone who can open an issue can feed it. `design` works
the one issue you named, or the one it filed from your `-brief` — the same
opt-in `-label` stands for, one issue at a time — so the refusal has nothing
to guard. Its body and thread are still data, not instructions, on any
repo that takes outside issues: naming an issue opts its text in, and the
skill carries the same posture paragraph `implement-issue` does. See
[`design`](reference.md#designing-a-plan-document-polako-design).

## What leaves the machine

Two things, one of them only on request:

| What | Where it goes | Default |
| --- | --- | --- |
| [`-post-summary`](run-data.md#putting-it-on-the-pr--post-summary) | One line of run numbers, as a comment on your own merged PR — readable by exactly the people who can already see that PR. | Off. |
| Evidence images | A PNG the skill captured as real output, pushed to the `polako-evidence` orphan branch on your own origin and embedded in the PR body by commit sha — readable by anyone who can already read that repo. | On, for a run whose diff touches browser-rendered files in a repo with a script to serve them. [`-visual-evidence`](reference.md) on `polako work`, default on, is the off switch — it appends `no-evidence`, the skill's own second argument, to the invocation. |
| [`-remote`](reference.md#watching-a-shift-from-anywhere--remote) | Each run's session, registered with Remote Control through the operator's own claude.ai account — watchable and typeable from claude.ai/code or the app, the same visibility an interactive `claude --remote-control` session has. | On. `-remote=false` keeps runs to this machine. |

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

### `-remote`

The destination isn't new either: Remote Control is Claude Code's own
feature, and a registered session reaches nobody an interactive `claude
--remote-control` session wouldn't already reach — the operator's own
claude.ai account, already running the model and already holding the
transcript. What's new on [issue
#471](https://github.com/scharissis/polako/issues/471) is the channel a
headless run reaches it through: a `remote_control` control request written
to the child's stdin over the stream-json protocol, probed by hand and
confirmed by no doc or published SDK type. polako never waits on the reply —
a success logs the session URL, an error logs the CLI's own reason, and no
reply by the end of the run logs one line saying so — so a CLI that never
answers, or answers with a shape this run doesn't recognise, cannot hang,
fail or re-dispatch anything; the stall watchdog stays the only kill for a
silent CLI. `-remote=false` is the way to decline it, unchanged since before
this issue: no stdin, no `-n`, the same argv `work` always sent.

### The evidence ref

The destination isn't new — the operator's own repo, where the code and the
PR body already go. The content is pixels. A screenshot can render whatever
a rendered page reads, and a sha-pinned image on a public repo is, in
practice, not retractable once pushed. A run looks at each shot before
publishing it and drops anything that reads as an error, a blank page, a
login wall, or a credential — the mitigation for a rendered page reading
something it shouldn't. `-visual-evidence=false` on `polako work` is the way
to decline the whole thing.

### The published-version read, and why it isn't in that table either

[`polako update`](install.md#update) reads one public file —
`.claude-plugin/marketplace.json` on this project's own repository, through
`gh api` — to learn what's published. `polako work`'s preflight and `polako
status` make the same read now, passively: one line when the published
release is ahead of the binary or the installed plugin, nothing otherwise.
All three send nothing about your run, your repository or your account: the
request carries only the path to a file anyone can already fetch from a
browser. Not a second destination under the table above, but named here for
the same reason every row in it is: worth stating rather than dropping
quietly.
