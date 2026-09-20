# Installing polako

Both halves install separately: the skill as a Claude Code plugin, the binary
with `go install` or a prebuilt release. The binary is one install for all
seven verbs — `work`, `plan`, `health`, `status`, `stats`, `tidy`, `update` —
nothing here repeats per verb. The
[README](../README.md#install) has the short version of the first two.

## Install

The repo doubles as its own marketplace, so there's no clone step. Register
the marketplace once, then install the plugin from it:

```bash
claude plugin marketplace add scharissis/polako
claude plugin install polako@scharissis
```

`polako` is the plugin, `scharissis` the marketplace it came from — the name
declared in [`.claude-plugin/marketplace.json`](../.claude-plugin/marketplace.json),
not the GitHub username, though here they happen to match. The plugin ships
three skills — `implement-issue`, `plan-backlog` and `review-health` — for a
few dozen tokens of always-on context; a skill's body loads only when it
fires.

Restart Claude Code, and `/polako:implement-issue 48`,
`/polako:plan-backlog docs/VISION.md` and `/polako:review-health .` are
available.

Note the namespace. Claude prefixes plugin skills with the plugin name, so the
command is *not* `/implement-issue` on this path. The supervisor's `-skill`
default matches the plugin form; see [By hand](#by-hand) for the other one. To
see what a session actually has, the `init` event lists them:

```bash
claude -p "hi" --output-format stream-json --verbose | head -1
```

Then the binary:

```bash
go install github.com/scharissis/polako/cmd/polako@latest
```

Or build from a clone:

```bash
go build -o polako ./cmd/polako
```

Prebuilt binaries for Linux, macOS and Windows are attached to each tagged
release, and are the easiest option on a machine without Go. They're stamped
with their tag, so `polako -version` tells you what you're running.

## Update

**Nothing updates on its own by default.** Auto-update is off for third-party
marketplaces, so an installed plugin stays exactly where it is until you ask.
`polako update` brings both halves to the release this project has actually
published — never `@latest`, which can land the binary ahead of what the
plugin side resolves to:

```bash
polako update
```

Run it between shifts, not during one — `work` never updates itself, so
there's no point mid-shift where swapping the binary or the plugin under it
would be safe.

"Published" means the tag the publish PR's `marketplace.json` names, which
lands one merge after the release tag itself exists
([Publishing and versioning](releasing.md#cutting-a-release)). In the window
between those two merges, `@latest` and the plugin can resolve to different
releases — the gap `polako update` reads around instead of falling into.

`polako update -check` prints the same plan without changing anything. Then
`/reload-plugins`, or restart.

| Flag | Default | Meaning |
| --- | --- | --- |
| `-check` | `false` | Print the plan and change nothing. |
| `-skill` | `polako:implement-issue` | Skill `polako work` would run, so `update` knows whether that's the plugin or a hand-copied skill. A hand-copied one isn't `update`'s to manage. |
| `-claude` | `claude` | `claude` binary to invoke, when it isn't on PATH under that name. |
| `-gh` | `gh` | `gh` binary to invoke, same reason. |

Each takes its default from the
[environment](reference.md#setting-defaults-from-the-environment) too.

Running a prebuilt release binary rather than a `go install`? `update`
replaces that too: it downloads the release asset for your GOOS/GOARCH and
`checksums.txt`, verifies the download, and swaps it in over the running
binary. A checksum mismatch or a release with no `checksums.txt` refuses and
leaves the old binary in place. On macOS, if the new binary refuses to run
citing an unidentified developer, clear the quarantine attribute yourself:
`xattr -d com.apple.quarantine <path>`.

By hand, that's the same two commands as always — `update` wants the full
`plugin@marketplace` id; the bare name it reports as not found, even
installed — followed by the binary, pinned to the published version rather
than `@latest`. `polako update -check` prints that version as `published:`:

```bash
claude plugin marketplace update scharissis && claude plugin update polako@scharissis
go install github.com/scharissis/polako/cmd/polako@vX.Y.Z
```

If they end up mismatched anyway, the supervisor says so at startup and names
both versions, and how to fix it is the same either way: `polako update`. A
skill *newer* than the binary — testing a tip binary against an installed
release, say — is only a warning: that direction is deliberate often enough
that refusing would be more annoying than useful, and the supervisor still
finds a PR by the branch name the skill chooses either way.

A skill *older* than the binary is the direction that actually costs money —
[issue #239](https://github.com/scharissis/polako/issues/239) is a shift that
ran a plugin three releases stale and paid for the pre-#225 review gate on
every issue, with neither #216's resume point nor #217's polling floor — so
`polako work` refuses to start on it. `-ignore-skew` overrules that, out loud,
the same way `-ungated` overrules the public-repo label gate.

## Auto-update, pinning and uninstall

To let it happen automatically instead: `/plugin` → **Marketplaces** →
`scharissis` → **Enable auto-update**. Claude Code then checks after a session
starts, with a random delay of up to ten minutes, and the new version loads on
`/reload-plugins` or at the next launch — never mid-session. The binary isn't
covered; that's still yours to run.

To hold a machine at one release, pin the marketplace itself and it stops
moving — but mind *what* you pin it to. **A release tag is the wrong target:**
`polako--vX.Y.Z` is pushed on the release PR's merge commit, and the `ref`
inside `marketplace.json` only moves when the separate publish PR merges after
it ([Publishing and versioning](releasing.md#cutting-a-release) says why the two
are apart). So the `marketplace.json` frozen inside a release tag still declares
the *previous* release, and pinning at `polako--v0.9.0` holds the plugin at
0.8.0.

Pin at the publish commit instead — the `chore: publish X.Y.Z` commit on `main`
is the first one whose `marketplace.json` names X.Y.Z:

```bash
version=0.9.0
sha=$(gh api "repos/scharissis/polako/commits?path=.claude-plugin/marketplace.json&per_page=100" \
  --jq "map(select(.commit.message | startswith(\"chore: publish $version\")))[0].sha // empty")
[ -n "$sha" ] && claude plugin marketplace add "scharissis/polako#$sha"
```

Both halves of that are load-bearing. Matching the commit-message prefix on the
file's own history picks the publish commit and nothing else — a free-text
search also matches the `Revert "chore: publish X.Y.Z"` that a
[rollback](releasing.md#cutting-a-release) leaves behind, whose
`marketplace.json` names the release *before* X.Y.Z. And the `[ -n "$sha" ]`
guard is what stops an empty result — wrong version, unauthenticated `gh` —
from pinning the marketplace at nothing and quietly leaving it tracking the
default branch, which is the opposite of holding still.

The `publish-X.Y.Z` branch has the same content, but it may be deleted once the
PR merges, so the SHA is the handle that keeps working. To stop holding, remove
the marketplace and add it back bare — a pinned marketplace doesn't move when
the next release ships, and says nothing about it.

To remove the plugin entirely:

```bash
claude plugin uninstall polako && claude plugin marketplace remove scharissis
```

## By hand

Both commands under [Install](#install) take a `--scope`:

| Scope | Where it is declared | Use it for |
| --- | --- | --- |
| `user` *(default)* | `~/.claude/settings.json` | Your own machine, every project. |
| `project` | the repo's `.claude/settings.json` | Committing the marketplace + plugin so collaborators on *that* repo get the skill automatically. |
| `local` | the repo's git-ignored local settings | Trying it on one project without committing anything. |

So to make every contributor to some project pick the skill up, run both
commands with `--scope project` inside that project and commit the resulting
`.claude/settings.json`. They still each need read access to this repo.

If you'd rather not involve the plugin system at all, copy the skill
directories in. They behave identically; they just won't update themselves.
Take all three, or only `implement-issue` if you don't want the planning half.

```bash
cp -r skills/implement-issue skills/plan-backlog skills/review-health ~/.claude/skills/
```

```powershell
Copy-Item -Recurse skills\implement-issue,skills\plan-backlog,skills\review-health $HOME\.claude\skills\
```

A skill installed this way is invoked bare, with no plugin prefix — so
`/plan-backlog`, not `/polako:plan-backlog` — and the supervisor needs telling:

```bash
polako work -skill implement-issue
```

Do one or the other, not both — two copies of the same skill drift apart
silently.

## Using it on another project

Nothing here is tied to one repository or language — `-dir` points anywhere.
The one thing worth tuning per project is the tool allowlist, because an
unattended run stalls if a command it needs would raise a permission prompt.

The default `-tools` set covers git, the handful of gh subcommands the skill
uses (`gh issue view`/`comment`, `gh pr create`, plus read-only `gh pr
view`/`list`/`diff`), the tools the skill itself needs (`Read`, `Write`,
`Edit`, `Glob`, `Grep`, `Skill`, `TodoWrite`), and the usual entry points for
npm/pnpm/yarn, Go, Cargo, Make, Python/uv/pytest, dotnet, Maven and Gradle.
One more entry is added per run and isn't in `-tools`: the run may add and
remove labels on the single issue it was dispatched for, which is how it raises
`awaiting-answer`. For anything else, widen it rather than replacing it:

```bash
polako work -add-tools "Bash(bazel:*),Bash(just:*)"
```

Two other knobs matter when moving between repos:

- `-branch-prefix` must match what the skill names its branches, since that's
  how a PR is matched back to its issue.
- `-label` is the cleanest way to opt individual issues in on a busy repo.
