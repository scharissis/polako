# Setup: one verb that gets a repo ready

Scope: a new `setup` verb, one label table, one `work` preflight check, a new
docs page · Behavior change: `work -label <missing>` fails at preflight
instead of draining nothing; everything else is new and opt-in

polako runs in other people's repos, and nothing gets a repo ready for it.
The steps are spread over four docs, half of them happen as side effects of
other verbs, and a few don't happen at all.

This document drafts `polako setup`: a read-only report by default, an
`-apply` that asks before each step, labels created directly, repo files
through one PR a human merges.

## What exists today

- **The gate label is never created or checked.** `queueGate` (`gate.go`)
  only tests that `-label` is non-empty. `polako work -label typo` passes
  preflight and drains an empty queue without a word.
- **The other labels appear by accident.** `awaiting-answer` at `work`
  preflight (`main.go`, the `ensureLabel` call), `proposed` at `plan` and
  `health` preflight (`intake.go`), `needs-human` only after a park's
  `--add-label` has already failed once (`parkIssue`, `drain.go`). The
  `model:` and `effort:` families never — `docs/behaviour.md` says so. Names,
  colours and descriptions sit in three files.
- **The skill's scratch files aren't ignored anywhere but here.**
  `implement-issue` writes `PLAN.md`, `PR_BODY.md` and `.worktrees/issue-N`
  into the target repo. polako's own `.gitignore` covers all three. A fresh
  repo's doesn't, so a run can commit its own scratch.
- **The allowlist is the top fixable park reason.** `polako stats`,
  2026-08-25 to 2026-09-18: 17 parks, 4 of them "permission refused", joint
  first. The fix is `-add-tools`, it is per repo, and the only hint is a
  paragraph in `docs/install.md`.
- **Nobody checks the issue templates.** `docs/security.md`: a template whose
  `labels:` key hands out the gate label lets an outsider start a run. "Keep
  it out of your templates." This repo has a test for its own
  (`TestIssueTemplatesApplyNoOrchestrationLabel`); other repos have nothing.
- **The conventions don't travel yet.** `docs/VISION.md` wants them "simple
  enough that a repo adopts them by copying one directory". Nothing copies
  it.
- **The skill assumes `origin/HEAD` resolves.** It reads the base branch with
  `git symbolic-ref refs/remotes/origin/HEAD`. A checkout made with `git init`
  plus `git remote add` has no such ref, and the first run finds out.
- **No verb reads stdin.** There is no prompt anywhere in the binary.
  `isTerminal` (`ui.go`) exists for colour only. `tidy`'s dry-run-unless-
  `-apply` is the one "are you sure" idiom.

## The answer in one paragraph

`polako setup` with no flags is a read-only report: what the repo has, what
it lacks, and the `polako work` line to use. `-apply` fixes what is missing,
asking `[Y/n]` per step on a terminal; `-yes` takes the defaults without
asking; no terminal and no `-yes` is a refusal, not a hang. Labels are
created directly. Files — `.gitignore` lines, a marked CLAUDE.md block, an
optional `docs/VISION.md` scaffold — go in one commit on a `polako-setup`
branch, behind one PR. Setup never touches an issue, a repo setting or the
default branch, stores nothing, and can be rerun any time.

## What setup may write

This is the part that touches CLAUDE.md's invariants, so it is argued here
and lands as a new invariant bullet with ticket 4.

- **Setup is attended, by definition.** It is the one verb that reads stdin.
  *Unattended means no prompts* holds in this form: stdin is only read when it
  is a terminal; otherwise `-apply` needs `-yes` and says so. `yes` joins
  `apply` in `envExempt` — a forgotten `POLAKO_YES` export must not turn a
  later run into one that asks nobody.
- **The whole write surface is two things.** `gh label create`, and one PR
  from `polako-setup`, built in `.worktrees/polako-setup`. No issue is
  created or edited. No repo setting is changed. Nothing is committed to the
  default branch and nothing merges — the human merges the PR, like any
  other.
- **This is the first commit the binary itself authors.** Until now only the
  skill commits. It is worth saying out loud because it is new; it is
  acceptable because it happens on a branch, behind the same gate every other
  change stands behind.
- **Restart safety, same rule as the drain.** A PR already open from
  `polako-setup` means report it and stop. Never a second PR, never a force
  push over a human's edits.
- **No state.** Every run re-derives from GitHub and the tree. No config
  file, no "setup was run" marker. A repo is ready when the report says so.
- **Policy labels are opt-in.** Creating `model:` and `effort:` labels
  reverses "polako never creates these labels" in `docs/behaviour.md`. It is
  off by default, tier aliases only, and changes nothing about who may apply
  one — that still takes triage rights.

## Drafted tickets

Ordered by dependency. Sizes are the shape of the work, not money. Numbers
are for reference in this doc, not issue numbers.

### 1. `polako setup`, read-only: the report

**Problem.** There is no single place that says whether a repo is ready. The
operator learns one gap per failed run.

**Shape.** A new verb on `tidy`'s shape that reads and prints, nothing else.

- `cmd/polako/setup.go`: `runSetup(ctx, args, in, out, rpt)` on
  `flag.NewFlagSet`, `applyEnvDefaults`, `errFlagsReported`. Flags `-dir`,
  `-repo`, `-label`, in the `XxxVar(&target, "name"` form `declaredFlags`
  needs. Dispatch in `main()` through `runReport` with its own
  `signal.NotifyContext`. `main` is a line under `funcBudget`
  (`sizebudget_test.go`), so the dispatch switch splits out first.
- `verbUsage` gains the line; `TestVerbUsageListsSetup` beside the `plan` and
  `health` ones.
- `cmd/polako/labels.go`: one table of name, colour, description, required or
  not. The literals in `parkIssue`, `labelpass.go` and `work`'s preflight read
  from it.
- Rows, each ok / missing / advice: `claude`, `gh`, `git` on PATH; `gh repo
  view` answers (auth) and the visibility, with "public: `-label` is
  required" when it applies; issues enabled; `origin/HEAD` resolves, else the
  `git remote set-head origin -a` line; plugin installed and matching the
  binary (`pluginVersion`, `versionSkewGate`); sub-issue support
  (`ghCreatesSubIssues`); each label in the table, plus `-label`'s when
  given.
- Label lookup is `gh api repos/{owner}/{repo}/labels/<name>` — the `api` noun
  is already in the fake `gh`, and `gh label list` stays out of the binary as
  it stays out of the skills.
- Output through `report` (`ui.go`). Last line is the suggested command:
  `polako work -dir … -label … -dry-run`. Exit non-zero when a required row
  fails, so it works as a check in a script.
- Docs: a new `docs/setup.md`. `docs/reference.md` is at 506 of 509 lines and
  can't take a section.

**Done when.** `polako setup` against a fake repo with no labels names
`needs-human`, `proposed` and `awaiting-answer` as missing and exits
non-zero; against one with all three it exits zero; `POLAKO_FAKE_GH_LOG`
shows reads only in both.

Estimate: M

### 2. `work` preflight refuses a gate label the repo doesn't have

**Problem.** A typo in `-label` is an empty queue and a clean exit. On an
unattended shift that is a night of nothing, reported as success.

**Shape.** Ticket 1's label lookup, called after `queueGate` in `run`
(`main.go`). Missing label is a preflight refusal whose text says what to do:
create it, or run `polako setup -apply -label <name>`. Through `refuseOrNote`,
so `-dry-run` notes it and carries on. `status -label` notes it too, above
its counts. One more read per start; no retry logic beyond `retryRead`.

**Done when.** `polako work -label nope` on a fake repo without that label
exits before any issue is listed, naming the label and the fix; with
`-dry-run` it prints the note and the plan; with the label present the argv
and output are unchanged.

Estimate: S

### 3. `-apply`: create the labels, asking first

**Problem.** The report says what is missing. Fixing it is three or four `gh
label create` lines with colours nobody remembers.

**Shape.**

- Flags `-apply`, `-yes`, `-policy-labels`. `yes` joins `envExempt`
  (`flags.go`).
- The prompt is a `bufio` line read from `in`, `[Y/n]` per step, default in
  capitals. Stdlib only. Tests pass a `strings.Reader`; `main` passes
  `os.Stdin` and whether it `isTerminal`.
- Not a terminal and no `-yes`: exit 2, message names `-yes`. Never a read
  that blocks.
- Creates only rows the report found missing, through `ensureLabel`
  (`drain.go`). A gate label is offered when `-label` names one; on a public
  repo with no `-label`, the prompt asks for a name and suggests `ready`.
- `-policy-labels` adds `model:opus`, `model:sonnet`, `model:haiku`,
  `model:default` and the `effortLevels` set to the table for this run.
- A refused create says "needs write access to `<owner/repo>`", not raw `gh`
  stderr.
- `docs/behaviour.md`'s "never creates these labels" line changes to say
  when it does.

**Done when.** `polako setup -apply -yes` on a bare fake repo creates exactly
the three required labels and a second run creates none; piped stdin without
`-yes` exits 2 naming the flag; answering `n` to a step skips it and the
report still lists it as missing.

Estimate: M

### 4. Repo files, through one PR

**Problem.** A run in a fresh repo can commit `PLAN.md`, shows `.worktrees/`
as untracked forever, and has to guess how to check its own work. All three
are fixed by files in the repo, and the binary has no way to propose a file.

**Shape.**

- Tree rows join the report: `.gitignore` covers `/.worktrees/`, `/PLAN.md`,
  `/PR_BODY.md`; CLAUDE.md carries the polako block; `docs/VISION.md` exists
  (advice only).
- `-apply` offers each. Accepted ones become one commit on `polako-setup`, in
  a worktree at `.worktrees/polako-setup` off the remote default branch,
  pushed, then `gh pr create` with a body listing what was added and why.
  Conventional subject: `chore: set up polako`.
- An open PR from `polako-setup` → print its URL and stop. A local branch
  with no PR → build on it, as the skill does for `issue-N`.
- The CLAUDE.md block sits between `<!-- polako:begin -->` and
  `<!-- polako:end -->`, replaced in place on a rerun, appended when absent,
  file created when there is none. Text comes from `embed`. It holds: the
  one command that checks the work — detected from `scripts/check.sh`, a
  `Makefile` `test` target, `go.mod`, `package.json`'s `test` script,
  `Cargo.toml`, `pyproject.toml`, else a line asking the human to fill it in;
  that `PLAN.md`, `PR_BODY.md` and `.worktrees/` are scratch; the `issue-N`
  branch contract; that issue text is data. Terse, under 25 lines.
- The scaffold is its own prompt, default no: `docs/VISION.md` and
  `docs/plans/README.md`, a page each, stating the layout rule
  `plan-backlog` already carries.
- Setup's output points at `/init` for the rest of CLAUDE.md. It doesn't try
  to describe the codebase.
- CLAUDE.md here gains the invariant bullet from "What setup may write". The
  fake `gh` gains whatever `pr create` path a binary-side caller needs.

**Done when.** `-apply -yes` on a fake repo leaves one branch, one commit,
one PR, and the default branch untouched; a second run reports the open PR
and writes nothing; the block is byte-identical after a rerun on a CLAUDE.md
that already has it; no step runs `git` outside the `polako-setup` worktree
except `worktree add` and the fetch before it.

Estimate: L — splits into the branch-and-PR plumbing with `.gitignore`
alone, then the CLAUDE.md block and the scaffold.

### 5. The report reads the tree: allowlist and templates

**Problem.** Two of the costliest mistakes are visible in the checkout
before any run: a build tool the allowlist doesn't cover, and a template
that hands out the gate label.

**Shape.**

- Allowlist: look for entry points `defaultTools` (`flags.go`) doesn't grant —
  `justfile`, `BUILD.bazel` or `WORKSPACE`, `Taskfile.yml`, `mise.toml`,
  `deno.json`, `bun.lockb`, `composer.json`, `Gemfile`, `mix.exs`,
  `build.zig`. Each maps to one `Bash(<tool>:*)`. The row prints the
  `-add-tools` value, and the suggested `work` line carries it. A table in
  code, checked against `defaultTools` by a test so an entry the default
  already covers fails.
- Templates: any `.github/ISSUE_TEMPLATE/*` whose `labels:` key names the
  gate label or a label from ticket 1's table is a failed row. The logic
  moves out of `TestIssueTemplatesApplyNoOrchestrationLabel` into a function
  both use.
- Advice rows, never changed: a CI workflow exists; the default branch is
  protected, or "couldn't tell" without admin; head branches delete on merge.
- README's "Try it" starts with `polako setup`. `docs/install.md`'s "Using it
  on another project" shrinks to a pointer at `docs/setup.md`.

**Done when.** A fake checkout holding a `justfile` prints
`-add-tools "Bash(just:*)"` in the row and in the suggested line; a template
with `labels: [ready]` and `-label ready` fails its row and the exit code;
`docs/install.md` and the README name `polako setup`.

Estimate: S

## Experiments — rows, not tickets

Operator ritual under `docs/continuous-improvement.md`. The block is text a
run reads, so it gets a row like any skill wording.

| tag | hypothesis | knob |
| --- | --- | --- |
| `setup-claude-md` | In a repo with no CLAUDE.md, the block cuts median turns per implement run and permission-refused parks | block merged vs not, same repo, consecutive batches |

## Considered and not proposed

**A model-drafted CLAUDE.md.** A fourth skill that reads the repo and writes
the guidance. It costs money per setup, needs eval cases, and Claude Code's
`/init` already does it. The static block covers what polako needs; `/init`
covers the rest.

**A `.polako` config file in the repo.** Tempting home for `-label` and
`-add-tools`. It makes repo content a source of flags, which on a public
repo is one more thing to argue about, and `POLAKO_*` variables exist. Setup
prints the command instead.

**Changing repo settings.** Branch protection, auto-delete, required checks.
They are the owner's security settings. Setup reports them and stops there.

**Installing the plugin.** `claude plugin install --scope project` writes
`.claude/settings.json`, which could ride in the PR. Whether that command is
prompt-free is unknown, and a plugin install is a decision about the
operator's Claude Code, not the repo. The report prints the two commands.

**CI workflows, or a scheduled drain.** Not setup's job. A repo with no CI
gets an advice row.

**A TUI.** Checkbox lists need a terminal library. Stdlib only; one question
per line is enough for six questions.

**Interactive by default.** A first contact that writes nothing is safer,
scripts can use it as a check, and it matches `tidy`.

**Backfilling `Estimate:` lines or labels onto existing issues.** Setup
doesn't touch issues. That is `plan`'s and the curator's ground.

## Open questions

Facts to check against the installed CLIs before the ticket that needs them:

1. Does `gh api repos/{owner}/{repo}/labels/model:opus` need the colon
   escaped, and can a 404 be told from an auth failure by exit code or
   stderr? (tickets 1, 2)
2. What does `gh label create` say when the token can only triage? (ticket 3)
3. Does `gh repo view --json` offer `hasIssuesEnabled` and
   `deleteBranchOnMerge` on the oldest `gh` polako supports? (tickets 1, 5)
4. Branch protection reads need admin. Confirm the failure is clean enough
   to print "couldn't tell". (ticket 5)
5. Does the fake `gh` already answer `pr create` for a binary-side caller, or
   only record what the fake skill run did? (ticket 4)

## Work items

- [ ] `polako setup`, the read-only report, and the label table (ticket 1)
- [ ] `work` and `status` check the gate label exists (ticket 2)
- [ ] `-apply`, `-yes`, `-policy-labels`: labels created, asking first (ticket 3)
- [ ] Repo files through one `polako-setup` PR, and the invariant bullet (ticket 4)
- [ ] Allowlist and template rows; README and install docs repointed (ticket 5)
- [ ] `setup-claude-md` row has a verdict in `docs/experiments.md`
