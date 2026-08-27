# Installing polako

Both halves install separately: the skill as a Claude Code plugin, the binary
with `go install` or a prebuilt release. The [README](../README.md#install) has
the short version of the first two.

## The skill, as a plugin (recommended)

The repo doubles as its own marketplace, so there is no clone step. Register
the marketplace once:

```bash
claude plugin marketplace add scharissis/polako
```

Then install the plugin from it:

```bash
claude plugin install polako@scharissis
```

`polako` is the plugin, `scharissis` is the marketplace it came from —
the name declared in [`.claude-plugin/marketplace.json`](../.claude-plugin/marketplace.json),
not the GitHub username, though here they happen to match. The plugin ships two
skills — `implement-issue` and `plan-backlog` — and costs a few dozen tokens of
always-on context; a skill body is only loaded when that skill fires.

Restart Claude Code, and `/polako:implement-issue 48` and
`/polako:plan-backlog docs/VISION.md` are available.

Note the namespace. Claude prefixes plugin skills with the plugin name, so the
command is *not* `/implement-issue` on this path. The supervisor's `-skill`
default matches the plugin form; see [the hand install](#the-skill-by-hand) for
the other one. To see what a session actually has, the `init` event lists them:

```bash
claude -p "hi" --output-format stream-json --verbose | head -1
```

Both commands take a `--scope`:

| Scope | Where it is declared | Use it for |
| --- | --- | --- |
| `user` *(default)* | `~/.claude/settings.json` | Your own machine, every project. |
| `project` | the repo's `.claude/settings.json` | Committing the marketplace + plugin so collaborators on *that* repo get the skill automatically. |
| `local` | the repo's git-ignored local settings | Trying it on one project without committing anything. |

So to make every contributor to some project pick the skill up, run both
commands with `--scope project` inside that project and commit the resulting
`.claude/settings.json`. They still each need read access to this repo.

To update, see [Getting updates](#getting-updates). To remove:

```bash
claude plugin uninstall polako && claude plugin marketplace remove scharissis
```

## The skill, by hand

If you would rather not involve the plugin system, copy the skill directories
in. They behave identically; they just will not update themselves. Take both, or
only `implement-issue` if you do not want the planning half.

```bash
cp -r skills/implement-issue skills/plan-backlog ~/.claude/skills/
```

```powershell
Copy-Item -Recurse skills\implement-issue,skills\plan-backlog $HOME\.claude\skills\
```

A skill installed this way is invoked bare, with no plugin prefix — so
`/plan-backlog`, not `/polako:plan-backlog`, and the supervisor needs telling:

```bash
polako work -skill implement-issue
```

Do one or the other, not both — two copies of the same skill drift apart
silently.

## The binary

```bash
go install github.com/scharissis/polako/cmd/polako@latest
```

Or build from a clone:

```bash
go build -o polako ./cmd/polako
```

Prebuilt binaries for Linux, macOS and Windows are attached to each tagged
release, and are the easiest option on a machine without Go. They are stamped
with their tag, so `polako -version` tells you what you are running.

## Getting updates

**Nothing updates on its own by default.** Auto-update is off for third-party
marketplaces, so an installed plugin stays exactly where it is until you ask:

```bash
claude plugin marketplace update scharissis && claude plugin update polako@scharissis
```

(`update` wants the full `plugin@marketplace` id; the bare name it reports as
not found, even installed.)

Then `/reload-plugins`, or restart. **Upgrade the binary in the same breath** —
the two halves are one release, and mixing them is not a supported combination:

```bash
go install github.com/scharissis/polako/cmd/polako@latest
```

If they end up mismatched anyway, the supervisor says so at startup and names
both versions. It is a warning, not a refusal — but the supervisor finds a PR by
the branch name the skill chooses, so a mismatched pair is a bug waiting for a
confusing moment.

To let it happen automatically instead: `/plugin` → **Marketplaces** →
`scharissis` → **Enable auto-update**. Claude Code then checks after a session
starts, with a random delay of up to ten minutes, and the new version loads on
`/reload-plugins` or at the next launch — never mid-session. The binary is not
covered; that is still yours to run.

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
sha=$(gh search commits --repo scharissis/polako "chore: publish 0.9.0" --json sha --jq '.[0].sha')
claude plugin marketplace add scharissis/polako#$sha
```

The `publish-X.Y.Z` branch has the same content, but it may be deleted once the
PR merges, so the SHA is the handle that keeps working. To stop holding, remove
the marketplace and add it back bare — a pinned marketplace does not move when
the next release ships, and says nothing about it.


## Using it on another project

Nothing here is tied to one repository or language — `-dir` points anywhere.
The one thing worth tuning per project is the tool allowlist, because an
unattended run stalls if a command it needs would raise a permission prompt.

The default `-tools` set covers git, the handful of gh subcommands the skill
uses (`gh issue view`/`comment`, `gh pr create`, plus read-only `gh pr
view`/`list`/`diff`), the tools the skill itself needs (`Read`, `Write`,
`Edit`, `Glob`, `Grep`, `Skill`, `TodoWrite`), and the usual entry points for
npm/pnpm/yarn, Go, Cargo, Make, Python/uv/pytest, dotnet, Maven and Gradle.
One more entry is added per run and is not in `-tools`: the run may add and
remove labels on the single issue it was dispatched for, which is how it raises
`awaiting-answer`. For anything else, widen it rather than replacing it:

```bash
polako work -add-tools "Bash(bazel:*),Bash(just:*)"
```

Two other knobs matter when moving between repos:

- `-branch-prefix` must match what the skill names its branches, since that is
  how a PR is matched back to its issue.
- `-label` is the cleanest way to opt individual issues in on a busy repo.

