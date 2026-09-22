# Permission parks that name their own fix

Scope: refusal capture in the stream reader, the park reason and comment, the
exit summary, `status`, a new `unpark` verb · Behavior change: a permission
park's thread comment names the `-add-tools` entries that would clear it;
everything else is new and opt-in

A permission park tells the operator to "find the tool it reached for — named
in the terminal right after this park, and saved in the shift log". After a
night's drain that is a log dig per parked issue. The thread comment names no
tool at all, and the terminal names one whole command, not the entry to grant.

This document drafts the fix: the park works out the grant itself, says it
where a human looks first, and a new attended verb, `polako unpark`, does the
morning-after step.

## What exists today

Issue #390, parked 2026-09-19, is the worked example. Its session drew five
refusals from the CLI. Verbatim, with the command that drew each:

| command | the CLI's refusal |
| --- | --- |
| `git fetch origin 2>&1; echo "exit:$?"` | `This Bash command contains multiple operations. The following part requires approval: echo "exit:$?"` |
| `git … remote -v; ssh-add -l 2>&1; echo SSH_AUTH_SOCK=$SSH_AUTH_SOCK` | `Contains simple_expansion` |
| `git … fetch origin 2>&1; echo RC=$?` | `Contains simple_expansion` |
| `echo "SSH_AUTH_SOCK=$SSH_AUTH_SOCK"; ls -la …; ssh -T git@github.com 2>&1` | `Contains simple_expansion` |
| `ssh -T git@github.com` | `This command requires approval` |

- **The CLI already names the fix, and polako drops it.** `git` was granted;
  only `echo` wasn't, and the refusal says so. `observe` (`stream.go`) latches
  on that text through `toolResultRefusal` (`refusals.go`) but keeps the whole
  compound command instead of the part. The operator read `git fetch origin
  2>&1; echo "exit:$?"` and had to work out that the answer was
  `Bash(echo:*)`.
- **Only the first refusal is kept.** `permissionRefusedDetail` is a
  first-wins string. Five refusals, one reported.
- **One refusal shape is invisible.** `Contains simple_expansion` — a `$VAR`
  in the command — isn't in `toolRefusalSignatures`. No grant fixes it; the
  command has to be phrased differently. Today it isn't counted at all.
- **The thread names nothing.** `parkCleanExit` (`issue.go`) puts the refused
  command in `aside`, which goes to the terminal only, because a Bash command
  can hold a local path and threads are public (#386).
  `TestDrainParksARefusedToolResultWithoutResuming` pins that. The rule is
  right for commands. It is stricter than it needs to be for `Bash(echo:*)`.
- **The exit summary repeats the paragraph.** `drainSummary` (`drain.go`)
  prints `permissionParkReason` per parked issue — the "go find it" text
  again, with no tool and no rerun line.
- **#390 was a misdiagnosis.** The run worked around every refusal. Its real
  blocker was an unreachable SSH agent: `git fetch origin` failed four times,
  and its last message said so. The latch is first-refusal-wins, so the park
  said "grant a tool". Granting `Bash(echo:*)` would have fixed nothing.
- **Un-parking alone changes nothing.** A retry is a fresh run against the
  same allowlist. Removing `needs-human` without changing `-add-tools` hits
  the same wall.
- **`status` says "decide what to do about #N".** It knows which issues are
  parked, not why.

## The answer in one paragraph

A park keeps every refusal and derives the `-add-tools` entries from the
CLI's own refusal text. The thread comment gets the entries. The terminal
gets the commands and the run's last words. The exit summary ends with one
paste-ready rerun line. `polako unpark` reads polako's own park comments back
from GitHub the next morning, asks before each issue, removes `needs-human`,
and prints the `work` line to rerun with. Nothing is ever granted without a
human typing it or confirming it.

## What this may read and write

This is the part that touches CLAUDE.md's invariants, so it is argued here.

- **Issue and comment text is data.** `unpark` reads a grant out of a
  comment, which is the risky move in this plan. So: it reads only comments
  whose author is the `gh` viewer; only one fixed footer line; only entries
  of a strict shape — `Bash(<one to three plain words>:*)` or a bare tool
  name — and none from ticket 2's never-suggested table. A run can post
  comments as the operator, so even that isn't
  trusted: `unpark` shows each entry and asks. And it never applies a grant.
  It prints a line; the human runs it. Issue text never widens an allowlist
  by itself.
- **The park footer is a contract, like the plan footer.** `parkIssue`
  writes `Refused: <entry>, <entry>`; `unpark` and `status` parse it; one
  test holds both sides to the same wording. It lands in CLAUDE.md beside
  the plan footer with ticket 3.
- **Unattended means no prompts.** `unpark` is attended, on `setup`'s terms
  (#415): stdin is read only when it is a terminal,
  otherwise `-apply` needs `-yes`, and no terminal with no `-yes` is a
  refusal, not a hang. `work` gains no prompt.
- **All state lives in GitHub.** `unpark` reads labels and comments. It
  reads no shift log and no run data. Session ids, log paths and shift ids
  stay off the thread, as now.
- **What goes on a public thread.** An entry, never a command. An entry
  holding `/`, `~`, `$` or `\` is withheld, and the comment says "one more is
  named in the terminal". `Bash(/Users/x/bin/tool:*)` is the case that rule
  is for.
- **The write surface of `unpark` is one call:** `gh issue edit N
  --remove-label needs-human`. No comment, no other label, no file.

## Drafted tickets

Ordered by dependency. Sizes are the shape of the work, not money. Numbers
are for reference in this doc, not issue numbers.

### 1. Keep every refusal, in the CLI's own words

**Problem.** Five refusals, one kept, and the one kept is the wrong string:
the whole command, when the CLI named the part.

**Shape.**

- `runReport` holds a slice of refusals — tool, command, the part the CLI
  named, and a kind — in place of `permissionRefusedDetail`. Deduplicated,
  capped at a small number so a looping run can't grow it.
- Parse the part list out of `The following part requires approval: …` and
  its plural. One part per entry.
- `contains simple_expansion` joins `toolRefusalSignatures` with kind
  "ungrantable". Head-anchored like the others. Whether it latches
  `permissionRefused` is the one judgment call: it should, since the run was
  refused, but the reason in ticket 3 must not suggest a grant for it.
- The fallback stays: a refusal with no matching `tool_use` keeps the
  tool_result text.

**Done when.** A test feeds `observe` the five pairs above, verbatim, and
gets five refusals: one with part `echo "exit:$?"`, three ungrantable, one
plain with command `ssh -T git@github.com`.

Estimate: S

### 2. Derive the `-add-tools` entry

**Problem.** Nothing turns a refusal into the string the operator has to
type. The park says `Bash(<command>:*)` and leaves the rest as an exercise.

**Shape.**

- One function in `refusals.go`: refusal plus the effective allowlist in, an
  entry or nothing out.
- Bash: `Bash(<first word>:*)`. Three words when the first is `gh` — `Bash(gh
  issue close:*)` — because `defaultTools` grants `gh` per subcommand on
  purpose (`flags.go`, the comment above it): `gh issue:*` carries `gh issue
  edit --add-label`, `gh pr:*` carries `gh pr merge`. Any other tool: its
  bare name.
- Some entries are never suggested, because a suggestion from polako reads
  as permission. A short table: `gh pr merge`, `gh issue edit`, `gh api`,
  `gh secret`, `gh repo`, `gh run rerun`, `gh label`. A refusal matching one
  yields no entry and kind "never": the reason in ticket 3 says polako
  doesn't grant it and the skill shouldn't reach for it. The operator can
  still type it into `-add-tools`; polako just won't hand it over.
- Source of the word: the CLI's named part when there is one. For a plain
  `This command requires approval`, the command's first word — but only when
  the command holds no `;`, `|` or `&`. Otherwise no entry; the terminal has
  the command.
- An entry the allowlist already grants (`resolveTools`) is dropped. An
  ungrantable refusal yields none.
- No shell parser. `splitCommand` (`notify.go`) says it won't become one,
  and the CLI does the splitting here.
- A second function says whether an entry is safe for a public thread: no
  `/`, `~`, `$`, `\`.

**Done when.** Table test: `echo "exit:$?"` → `Bash(echo:*)`; `ssh -T
git@github.com` → `Bash(ssh:*)`; `gh issue close 1 --comment x` → `Bash(gh
issue close:*)`; `gh pr merge 7` → nothing, kind "never"; tool `WebFetch` →
`WebFetch`; `git status` against the default
allowlist → nothing; `a; b` with no named part → nothing;
`/Users/x/bin/tool --flag` → an entry, marked not thread-safe.

Depends on 1.

Estimate: S

### 3. The park says the fix

**Problem.** The reason is a paragraph about where to look. It should be the
answer, and it should stop claiming a grant is the fix when the run said
otherwise.

**Shape.**

- The reason is built from the refusals, not a constant. With entries: "the
  run was refused `Bash(echo:*)`, `Bash(ssh:*)`. Rerun with `-add-tools
  "Bash(echo:*),Bash(ssh:*)"`, then remove needs-human — or fix the skill if
  it shouldn't reach for these." With only ungrantable ones: "a command held
  a `$VAR`, which no grant allows; the skill has to phrase it differently."
  With none derivable: today's pointer at the terminal.
- The truthful case. When the run made successful tool calls after its last
  refusal and its final message doesn't read as an ask (`permissionRefusal`),
  the reason leads with "the run opened no PR, and was refused N calls along
  the way" and says the grant may not be the blocker. The category stays
  `permission_refused`, and the park is still immediate — #126 showed a run
  ends in innocuous prose after a refusal that did block it, so the latch
  doesn't change, only the wording.
- The terminal `aside` lists every refused command, and the run's clipped
  final message in that case. Terminal only: #390's held an SSH key name.
- `parkIssue` ends the comment with `Refused: <entry>, <entry>` — thread-safe
  entries only. No entries, no line.
- `TestDrainParksARefusedToolResultWithoutResuming` changes from "the
  command is absent" to: command absent, entry present, a path-bearing entry
  absent.
- `docs/behaviour.md`, "When an issue can't be finished": the new wording
  and the sample summary line. CLAUDE.md gains the footer contract.

**Done when.** The fake `toolrefused` run's comment holds `Bash(gh issue
close:*)` and a `Refused:` line and no `gh issue close 1`; a new fake run
shaped like #390 parks with the "along the way" wording and its final
message in the log, not in any `gh` call.

Depends on 2.

Estimate: M

### 4. The exit summary ends with the line to paste

**Problem.** The summary is what the operator reads first in the morning. It
lists parks and stops.

**Shape.**

- When any park carries entries, `drainSummary` appends a short block: the
  union as one `-add-tools "…"` value, the same as `POLAKO_ADD_TOOLS=…`, and
  one `gh issue edit N --remove-label needs-human` per issue — or `polako
  unpark` once ticket 5 lands.
- The per-issue `parked` line shortens to the entries. The paragraph is in
  the park lines above it already.
- The notify command gets `POLAKO_NOTIFY_GRANTS`, thread-safe entries only,
  since a notify command often posts somewhere.

**Done when.** A drain with two permission parks prints one combined
`-add-tools` value with no duplicates; a drain with none prints no block.

Depends on 3.

Estimate: S

### 5. `polako unpark`

**Problem.** Clearing a permission park is two steps in two places — edit the
launch line, remove a label — per issue, by hand.

**Shape.**

- A verb on `tidy`'s shape (`runTidy`): no flags is a read-only listing,
  `-apply` acts. Flags `-dir`, `-repo`, `-apply`, `-yes`, and an optional
  issue number.
- Listing: every open `needs-human` issue, polako's latest park comment
  reason clipped to a line, and the parsed `Refused:` entries. Comments by
  anyone but the `gh` viewer are ignored. Entries failing the strict shape
  are shown as "ignored", not used.
- `-apply`: per issue, `remove needs-human from #N? [y/N]`. Default no. On
  yes, the one `gh issue edit`. An issue with no footer can still be
  un-parked; it just contributes no entry.
- Ends by printing the rerun line: `polako work … -add-tools "<union of
  approved entries>"`, and the `POLAKO_ADD_TOOLS` form. It doesn't start a
  drain and stores nothing.
- The prompt helper is `setup`'s (ticket 3 there) if it has landed; if not,
  this ticket brings it and that one reuses it.
- `docs/reference.md` gets the verb and its flags; `verbUsage` gets the
  line. Hermetic tests through `fakeCLI`.

**Done when.** Against a fake repo with two parked issues, one with a
footer: the listing shows both; `-apply -yes` removes both labels and prints
one `-add-tools` value; a forged footer in a comment by another author
contributes nothing; no terminal and no `-yes` exits non-zero saying so.

Depends on 3.

Estimate: M

### 6. `status` names the fix

**Problem.** `status` says "decide what to do about #390". It could say
what.

**Shape.** For each parked issue, read the latest viewer-authored park
comment and parse the footer — the same function as ticket 5. The "yours to
do" line becomes "grant `Bash(echo:*)` or fix the skill, then `polako
unpark`". `-json` gains the entries per parked issue. One comment read per
parked issue, on `status` only, never on the drain path.

**Done when.** `status` against the fake repo prints the entry for the
footed issue and today's line for the other.

Depends on 3.

Estimate: S

## Considered and not proposed

**The full command on the thread.** Simplest to read. It is what #386 is
about: commands hold home paths and key names, and threads are public.

**Granting automatically on retry.** A `-auto-grant`, or `work` reading the
footer itself. Then a comment widens the allowlist, and a run can write
comments. The human types the grant or confirms it; that is the design.

**Persisting grants.** A `.polako` file, or writing `.claude/settings.json`.
`setup.md` already turned the config file down, and `POLAKO_ADD_TOOLS`
exists. `unpark` prints the line instead.

**`unpark` reopening the old session for a human to approve the prompt.**
The session id lives only in run data, which nothing but `stats` may read.
Putting it on the thread is the other way, and it was kept off on purpose.
`resumeHint` still prints `claude --resume <id>` for whoever wants it.

**Reading `permission_denials` from the result event.** Not seen in the
stream polako reads. The tool_result text is verbatim, already trusted, and
names the part.

**A different park category for worked-around refusals.** Tempting after
#390. #126 is the counter-example: refused, then a calm last message, and
the refusal was the blocker. The stream can't tell the two apart reliably,
so the wording hedges and the category stays.

**A shell parser.** Stdlib has none, and the CLI already names the part.
