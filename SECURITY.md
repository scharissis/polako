# Security policy

## Reporting a vulnerability

Use GitHub's private reporting: go to the
[Security tab](https://github.com/scharissis/polako/security/advisories/new)
and choose **Report a vulnerability**. The report stays private until there is
a fix.

Please do not open a public issue for a vulnerability. Issues on this
repository are the queue an unattended agent works, so a public report is both
disclosed and, potentially, picked up as work.

polako is maintained by one person in their own time. Expect an
acknowledgement within a week. If a report turns out to be valid it goes into
the next release, and you get credit in the release notes unless you would
rather not.

## Supported versions

The latest release only. This is pre-1.0 software: fixes ship in the next
version rather than being backported.

## What is in scope

- Anything that lets issue or comment text change what a run does, beyond
  making the change that text describes. The tool allowlist, the per-run label
  grant, and the review-comment API grant are the interesting surfaces.
- Anything that gets polako to merge a pull request, commit to a default
  branch, or work an issue the queue rules exclude — `needs-human`, `proposed`,
  a container issue, or an issue without the `-label` gate label.
- Anything that sends data off the machine other than the two paths named in
  [docs/security.md](docs/security.md): `-remote` and `-post-summary`.
- Anything that writes issue, comment or pull request text into a run-data
  record or a `-notify` hook. Those carry numbers, identifiers and your own
  labels by design, which is what makes them safe to share.

## What is known, and not a vulnerability

These are documented trade-offs rather than defects. All three are argued in
full in [docs/security.md](docs/security.md).

- **The tool allowlist is a narrowing, not a sandbox.** `Bash(go:*)`,
  `Bash(python:*)` and the rest are arbitrary code execution by construction,
  and build commands run whatever your repository's scripts contain. Point
  `-dir` at repositories you would run `make test` in yourself.
- **Allowlist entries are prefixes, not signatures.** The per-run grants are
  pinned to one issue number and one pull request, which narrows the blast
  radius; they are not a boundary.
- **An unattended run reads attacker-controllable text on purpose.** That is
  what an issue is. The bounds on it are the `-label` gate, which requires a
  maintainer to opt each issue in, and the human merge at the end.

## If you run polako yourself

On any repository that accepts issues from outside your team, run it with
`-label`. On a public repository that is not advice — `polako work` refuses to
start without a label gate or an explicit `-ungated`. And read the pull request
before you merge it; that step is the last check on what actually lands, and it
is deliberately yours.
