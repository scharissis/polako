# Contributing

Bug reports and pull requests are welcome. Run the checks below before opening
one, and see [docs/releasing.md](docs/releasing.md) for how a release is cut.

Two things to know first. This project ships a
[Code of Conduct](CODE_OF_CONDUCT.md), and taking part means keeping to it. And
if what you have found is a security problem, please
[report it privately](SECURITY.md) rather than opening an issue — issues here
are public, and they are also the queue an unattended agent works.

## Checking your work

```bash
go test ./...
```

```bash
./scripts/check.sh
```

```powershell
.\scripts\check.ps1
```

`check` runs `gofmt`, `go vet` and the full test suite — the same three things
CI runs, on Linux, macOS and Windows. `smoke` is its opposite number and is not
part of this loop: it needs the network, `gh` and a real `claude`, and it only
has anything to check once a release is tagged — see
[Cutting a release](docs/releasing.md#cutting-a-release). The plugin side has its own validator,
which the release scripts and workflows all run for you:

```bash
claude plugin validate .
```

The suite is hermetic: no network, no `gh`, no real `claude`. Tests that need a
Claude process spawn a fake CLI — the test package built once, without the race
detector, then re-entered as a child — that streams canned `stream-json` events,
which covers the streaming, session-capture, crash and stall-watchdog paths for
real. A second group of tests keeps the repository honest — the plugin manifest,
the shipped skill and the documented flags all have to agree with the code.

## Re-recording the demo

The GIF at the top of the README is rendered from
[docs/demo.tape](docs/demo.tape) by [vhs](https://github.com/charmbracelet/vhs):

```bash
brew install vhs   # once
./scripts/demo.sh
```

The script builds the binary from your working tree and puts it on `PATH`, so
the recording shows this tree rather than whatever is installed. Both commands
in the tape are real and read-only, which means the GIF shows whatever your
backlog actually said when you rendered it — look at it before you commit it.

## Evaluating the skill

The hermetic suite can only check that
[`SKILL.md`](skills/implement-issue/SKILL.md) still *says* what the supervisor
depends on: that the review gate is there and names a branch, that the label
command is spelled the one way the allowlist grants, that the PR body ends by
closing the issue. Whether a run *keeps* those promises is what
[`evals/`](evals) is for:

```bash
claude plugin eval . --scaffold --allow-tools Bash Write Edit
```

Read [`evals/README.md`](evals/README.md) before you run that, not after: as
written it runs every case twice — `--ablation` defaults to a no-plugin
baseline arm — and publishes an HTML report of the prompts and grader verdicts
to claude.ai. `--ablation none --no-publish` is the debugging shape, and the
README spells out the rest of the flags and a ceiling.

Six cases, each one a real run against a scratch repo and a stand-in `gh`. Five
drive `implement-issue` from plan to PR: a specified issue reaching a PR, an
under-specified one producing questions instead of guesses, the review gate
firing before the PR, an existing plan being resumed rather than rewritten, and
a slow verification step waited out in the turn rather than deferred to one that
never comes. The sixth drives `plan-backlog`, and is the one whose subject
writes no code: a vision document becoming labelled, sized, parented proposals.

This one is not part of `check.sh` and not in CI. It needs the network, a real
`claude` and money — a deliberate exception to the hermetic rule, and the
reasoning is in [`evals/README.md`](evals/README.md) along with the caveat that
the suite is new and has not yet had a green run.

## Running both halves from a working tree

The hermetic suite never runs the skill, and the eval cases run it against a
scratch repo rather than a real backlog, so a change to
[`SKILL.md`](skills/implement-issue/SKILL.md) is still worth proving by driving
a real issue with it. That means running your working tree, not an install — and
an install is what every marketplace path gives you, because the entry pins a
`ref` ([Publishing and versioning](docs/releasing.md)).

`--plugin-dir` is the way in. It loads a plugin from a directory for one
session:

```bash
claude --plugin-dir /path/to/polako
```

Two things make it the right tool rather than a workaround. The skill keeps its
namespaced form, `/polako:implement-issue` — the same name the
supervisor's `-skill` default already expects — so nothing needs telling. And it
replaces an installed plugin of the same name for that session, so you do not
have to uninstall your working copy to test tip and reinstall it afterwards.
To confirm which one a session actually loaded, the `init` event names the path
and version it came from:

```bash
claude --plugin-dir /path/to/polako -p "hi" --output-format stream-json --verbose | head -1
```

The supervisor has no pass-through for extra `claude` arguments, so a shift
cannot ask for `--plugin-dir` itself. Wrap it instead — save one of these as
`~/bin/claude-tip`, or `claude-tip.cmd` on Windows — and point `-claude` at it:

```sh
#!/bin/sh
exec claude --plugin-dir /path/to/polako "$@"
```

```cmd
@echo off
claude --plugin-dir C:\path\to\polako %*
```

```bash
chmod +x ~/bin/claude-tip && polako -claude ~/bin/claude-tip
```

Both startup probes are ordinary `claude` invocations — `claude --version` and
`claude plugin list --json` — so a wrapper that `exec`s the real thing carries
them through — and the second one then lists the tree's copy at `session` scope
beside whatever is installed. That session copy is the version the supervisor
reports and records, because it is the one replacing the install for the run; a
stale install left behind does not shadow it. The `init` event above still names
the directory it came from. Build the binary from the same tree and both halves
are tip:

```bash
go build -o polako ./cmd/polako && ./polako -claude ~/bin/claude-tip -once
```

Which path when:

| Path | What it runs | Use it for |
| --- | --- | --- |
| `claude --plugin-dir <clone>` | the working tree, for one session | Developing against `main`. |
| `claude plugin install polako@scharissis` | the tagged release | Smoke-testing a release, and normal use — see [Smoke-testing the skill](docs/releasing.md#smoke-testing-the-skill). |
| [Hand install](docs/install.md#the-skill-by-hand) | a copy you made | Not involving the plugin system at all. Remember `-skill implement-issue`. |

