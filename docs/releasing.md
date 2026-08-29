# Publishing and versioning

The repo ships two artefacts — a Claude plugin and a Go binary — and they share
one version number, the `version` field in
[`.claude-plugin/plugin.json`](../.claude-plugin/plugin.json). That field is the
source of truth; everything else is derived from it.

The plugin is the versioned unit and the skill versions with it — skill
frontmatter carries no version of its own, and should not grow one. The binary
takes the same number. **One number, one commit, two artefacts.**

That version is also the *update signal*: Claude Code caches an installed plugin
under its version and skips anything that resolves to the same string. Commits
that land on `main` without a bump reach nobody. Bumping the field is the only
thing that moves an installed user.

`TestShippingFixesDoNotSitUnreleased` enforces that: it fails once a non-merge
commit touching `skills/` or `cmd/` has sat above the newest release tag for
more than a day, naming the commits and pointing at a `chore(release)` bump.
Test-only changes and a release already in flight are exempt; the day of grace
is there so ordinary work between releases does not turn the build red.

## Cutting a release

A release is two merged PRs — one that cuts it, one that publishes it — with
workflows doing everything in between. Nothing releases on its own: the
pipeline only moves when the version in `plugin.json` changes on `main`, so
ordinary merges never cut anything, and bumping that field stays the
deliberate act it always was.

**One-time setup, per repository:** the workflows open PRs, and GitHub forbids
a workflow's own token that by default, whatever permissions the workflow asks
for. Enable it once — *Settings → Actions → General → Workflow permissions →
Allow GitHub Actions to create and approve pull requests* — or:

```bash
gh api -X PUT repos/{owner}/{repo}/actions/permissions/workflow -F can_approve_pull_request_reviews=true
```

Until then, *Start a release* and the publish step fail at `gh pr create`;
each says so, names the checkbox, and deletes the branch it pushed so a re-run
starts clean.

**Optional, but worth it:** a `RELEASE_TOKEN` repository secret holding a
fine-grained personal access token — this repository only, *Contents* and
*Pull requests* read/write, nothing else. With it, the workflows open the
release and publish PRs as **you** rather than as `github-actions[bot]`, which
matters because GitHub holds every bot-authored PR's `pull_request` CI behind
an *Approve workflows to run* button on a private repository, with no setting
to turn that off. Your-token PRs run CI on their own; without the secret the
pipeline still works and each PR just carries that one approval click. Mind
the token's expiry date: an expired one fails the PR-opening step loudly, and
refreshing the secret is the fix.

| Tag | Who needs it |
| --- | --- |
| `polako--v0.4.0` | The Claude plugin tooling. `claude plugin tag` creates it, and refuses if `plugin.json` and the marketplace entry disagree. It is also what the marketplace entry's `ref` pins to, and what a `dependencies` range would resolve against. |
| `v0.4.0` | Go modules — `go install ...@v0.4.0` only resolves semver tags — and the trigger for the binary release workflow. |

1. **Start a release** — Actions → *Start a release* → pick patch, minor or
   major ([What to bump](#what-to-bump)). It opens the release PR: one
   `chore(release): X.Y.Z` commit that bumps
   [`plugin.json`](../.claude-plugin/plugin.json) and writes the
   [`CHANGELOG.md`](../CHANGELOG.md) section, and does nothing else — folded
   into a feature commit, no commit would mean "this is X.Y.Z". The section is
   GitHub's release notes for the range: every merged PR as a linked bullet
   with its author and the issues it closed, capped by a compare link, with
   the previous cycle's release and publish PRs filtered out as plumbing. The
   same PR written by hand works identically; the workflow is a convenience,
   the PR is the contract.

2. **Read the notes, add what only a person can, then merge.** The linked
   list is complete on its own; what it cannot say is what a change means for
   an unattended machine, so add an **Operator impact** line when the release
   changes what a run does (the changelog's own preamble says why). Merging
   the release PR is the act that cuts the release: *Cut a release* sees the
   new version, refuses while the changelog section is missing or
   `claude plugin validate` objects — before either tag exists — then pushes
   both tags on the merge commit and hands `vX.Y.Z` to *Release*, which
   cross-compiles the five targets with the tag stamped in, publishes the
   GitHub release with the changelog section as its body, starts a *Smoke*
   run, and opens the publish PR.

3. **Merge the publish PR.** It moves the `ref` in
   [`marketplace.json`](../.claude-plugin/marketplace.json) to the new tag, and
   merging that one line is the moment anybody is exposed to the release. Its
   body says what to check first: the *Smoke* run — every check CI cannot make
   before the tags exist, from the five attached binaries and the `-ldflags`
   version stamp, through `go install ...@vX.Y.Z` resolving, to the plugin
   installing with the ref moved and a session listing the skill, all against
   a throwaway `CLAUDE_CONFIG_DIR` so no machine moves onto the release early
   (it runs [`smoke.sh`](../scripts/smoke.sh); `./scripts/smoke.sh` asks the same
   questions from a checkout) — and, when the skill half changed, one real
   issue driven through the release, which no workflow can do for you: see
   [Smoke-testing the skill](#smoke-testing-the-skill).

**Rolling back** is reverting the publish commit; tags never move. Tagging and
publishing are separate on purpose: a `ref` moved in the release commit would
have `main` advertising a tag that does not exist from the moment it merges
until the tag is pushed, and every install in that window fails. The gap is
also what gives the smoke run something to prove while nobody is on the
release yet.

**Every workflow in the chain is re-runnable.** Each re-derives where things
stand from GitHub — which tags, release and PR already exist — finishes what
is missing, and leaves what is done alone. A pipeline that died half-way is
recovered by re-running *Cut a release* from the Actions tab; *Release* can be
dispatched on the `vX.Y.Z` tag ref and *Smoke* on any version, the same way.

The by-hand path still works, and refuses in the same places.
[`release.sh`](../scripts/release.sh) / [`release.ps1`](../scripts/release.ps1) push
the same two tags once the release PR has merged — each refuses on a dirty
tree, a missing changelog section, and a Claude Code too old for `claude
plugin tag`, and runs `claude plugin validate .` before either tag exists,
because both tags go public before any CI sees them. Pushing `vX.Y.Z` by hand
triggers *Release*, so the two paths converge from there: binaries, GitHub
release, smoke run, publish PR.

**Installs resolve to a tag, not to `main`.** The marketplace entry is an
explicit `github` source with a `ref`, so a version number identifies exactly
one commit and two people reporting `0.4.0` are running the same thing. This is
also what keeps the two halves together: `go install ...@latest` resolves to the
newest `vX.Y.Z` tag, so a plugin tracking `main` would drift from its binary by
construction. A test holds the `ref` to never being *ahead* of `plugin.json` —
lagging is the normal state between steps 2 and 4.

That pin is also why adding your clone as a local marketplace does not develop
against `main`: `claude plugin marketplace add ./` registers the marketplace
from the working tree, but the entry it reads is a `github` source with a `ref`,
so the install that follows fetches the *tagged release* from GitHub and the
tree is never read. To run the tip of a working tree, see
[Running both halves from a working tree](../CONTRIBUTING.md#running-both-halves-from-a-working-tree).

## Smoke-testing the skill

`smoke.sh` proves the artefacts are real, installable and consistent. It proves
nothing about whether the skill still takes an issue to a PR, and nothing ever
will fully automate that: **nothing merges itself**, so the last link in the
loop is a human. The cheapest honest test is therefore not a scratch repo and a
seeded fake issue — it is to make the first issue you were going to work
anyway the smoke test.

Before merging the publish PR, install the release for real. **Not from the new
tag:** at this point the tag exists but the `ref` in `marketplace.json` has not
moved yet — that is what the publish PR is for — so a marketplace pinned at
`polako--vX.Y.Z` installs the *previous* plugin, and the run opens with the
`version skew` line the checklist below says must be absent. Pin at the publish
branch, which carries the moved `ref`:

```bash
claude plugin uninstall polako && claude plugin marketplace remove scharissis
claude plugin marketplace add scharissis/polako#publish-X.Y.Z && claude plugin install polako@scharissis
```

That also tests the very line the publish PR is about to merge. The binary half
needs nothing special — `vX.Y.Z` was pushed in step 2, so `go install
...@latest` already resolves to it — but it does need running, or the plugin is
X.Y.Z beside whatever binary the machine already had, and the first line of the
checklist below fails for the opposite reason:

```bash
go install github.com/scharissis/polako/cmd/polako@latest
```

Then run one issue, watched rather than unattended:

```bash
polako work -once
```

What to watch for, in order:

- Startup names the repository, `/polako:implement-issue`, and the same
  version on both halves — no `version skew` line.
- The skill opens a PR whose head branch is `issue-N`. That name is the
  contract between the two halves; a change there breaks PR discovery.
- The supervisor finds that PR and *waits* on it, rather than re-running the
  skill over the top of it.
- You merge. The supervisor notices and exits.
- `polako stats` counts that issue as `merged`.

**Unpin afterwards**, whichever way the test went — a marketplace left pinned
at `publish-X.Y.Z` silently holds the machine there when the next release
ships, and the branch may be deleted out from under it. Wait until the publish
PR has merged, though: bare `scharissis/polako` reads `main`'s
`marketplace.json`, and until that PR lands its `ref` still names the previous
release, so unpinning early swaps the plugin back a version and leaves it
skewed against the X.Y.Z binary.

```bash
claude plugin uninstall polako && claude plugin marketplace remove scharissis
claude plugin marketplace add scharissis/polako && claude plugin install polako@scharissis
```

Then say in the publish PR's body which issue you drove. If the backlog is
empty at release time, say **that** instead — "the skill half went
unexercised" is worth recording rather than leaving to be inferred from
silence.

## What to bump

Pre-1.0, **minor is the breaking axis**. That is the npm-semver reading —
`^0.3.0` means `0.3.x` — and plugin `dependencies` ranges resolve with npm
semver against `polako--v*` tags, so it is the reading that makes a
constraint on this plugin behave.

- **Patch** — bug fixes, doc changes, anything invisible to a caller.
- **Minor** — new flags, changed defaults, changes to the skill's phases. The
  `-tools` default counts: widening it changes what unattended runs may do.
- **Major** — `1.0.0` when the skill's contract with the supervisor settles.
  The coupling to watch is the branch name: the supervisor finds a PR by its
  head branch, so if the skill ever stops naming branches `issue-N`, that is a
  breaking change on both sides at once. After 1.0, that is what major means.

