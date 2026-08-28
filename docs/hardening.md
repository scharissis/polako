# Hardening

[docs/security.md](security.md) says what polako bounds itself: the tool
allowlist, and the `-label` gate on which issues are eligible. It is also
explicit that the allowlist "is a narrowing, not a sandbox" — build commands
run whatever the checked-out repository's scripts contain, and `Bash(python:*)`
is arbitrary code execution by construction.

This page is about the half polako does not close, and what you can wrap around
it if you want that half closed: an egress firewall between the run and the
network. Everything here is the operator's, sitting outside polako. There is no
flag for any of it, and polako enforces none of it.

## The threat

An unattended shift is a Claude session whose only input is issue and comment
text. On a repository that accepts issues from outside your team, a stranger
writes that input. The skill is told to read it as a description of work rather
than as orders, and that is a real mitigation — but it is a mitigation inside
the model, not a boundary outside it.

The consequence worth spending effort on is **exfiltration**. A prompt
injection that persuades a run to open a pull request you would not have
merged is caught by the fact that [nothing merges
itself](../README.md#the-rules-it-follows): you read the diff. A prompt
injection that persuades a run to `curl` your `.env`, your SSH keys or the
private source it is standing in to an address the attacker controls is caught
by nothing in polako at all. The run has a shell, and the shell has the
network.

Egress control is the mitigation that matches that shape. Deny by default,
allow the handful of hosts a shift genuinely needs, and log what was asked
for.

## Wrapping a shift in an egress proxy

[iron-proxy](https://github.com/paradigmxyz/iron-proxy)
([docs](https://docs.iron.sh/)) is an egress firewall built for exactly this
case — untrusted agent workloads, default-deny host allowlist, credentials
injected at the boundary so the workload never holds them, and a JSON audit log
of every request. It composes with polako today with no code changes at all:

```bash
HTTPS_PROXY=http://localhost:8443 \
  SSL_CERT_FILE=/path/to/iron-ca.pem \
  polako work -dir ../my-project -label ready
```

That works because polako never overrides the environment of the processes it
starts. `claude`, `gh` and `git` are all spawned without setting `cmd.Env`, so
`os/exec` hands each child the environment polako was started with, and a
`-notify` hook gets it too — its own `POLAKO_NOTIFY_*` variables are appended
to the parent environment rather than replacing it. Whatever you export on the
left of that command line reaches every process a shift creates.

### Which variable each child actually reads

The one-liner above is the short form, and it is not quite enough on its own.
Intercepting TLS means every client has to trust the proxy's CA, and the three
clients a shift runs read three different variables for that:

| Process | Proxy | CA certificate |
| --- | --- | --- |
| `claude` | `HTTPS_PROXY` | `NODE_EXTRA_CA_CERTS` — it is a JavaScript runtime, and those do not read `SSL_CERT_FILE` |
| `gh` | `HTTPS_PROXY` | `SSL_CERT_FILE` — Linux only; Go ignores it on macOS (below) |
| `git` | `HTTPS_PROXY` | `GIT_SSL_CAINFO` (libcurl, so `CURL_CA_BUNDLE` also works) |

So export all of them, pointed at the same file:

```bash
export HTTPS_PROXY=http://localhost:8443
export SSL_CERT_FILE=/path/to/iron-ca.pem
export NODE_EXTRA_CA_CERTS=$SSL_CERT_FILE
export GIT_SSL_CAINFO=$SSL_CERT_FILE
polako work -dir ../my-project -label ready
```

**On macOS the variables are not the whole story.** Go reads `SSL_CERT_FILE`
only on Unix systems *other than* macOS — there it verifies against the system
keychain instead — so `gh` ignores that file however you point it. The system
`git` can ignore `GIT_SSL_CAINFO` too, depending on the TLS backend its libcurl
was built against. Trust the CA at the machine level:

```bash
sudo security add-trusted-cert -d -r trustRoot \
  -k /Library/Keychains/System.keychain /path/to/iron-ca.pem
```

`claude` is unaffected — `NODE_EXTRA_CA_CERTS` is Node's own and works
everywhere. Adding a trusted root is exactly the system-footprint change the
last section gives as a reason polako does not do this for you.

Getting one of these wrong does not fail loudly in a useful place. It fails as
a TLS error inside a child, which reaches you as a crashed run or a park — see
"when it goes wrong" below.

**An SSH remote bypasses the whole arrangement.** `HTTPS_PROXY` is an HTTP
client convention; `git@github.com:you/project.git` is a TCP connection over
port 22 and never consults it. A shift pushes branches, so if the drained
repository's `origin` is an SSH remote, the one traffic flow most worth
watching goes around the boundary. Use an HTTPS remote for a repository you
intend to run behind a proxy, or enforce at the network layer instead (below).

## An allowlist for a shift

What polako can tell you is the *host list*. The config file that carries it is
iron-proxy's, iron-proxy is pre-1.0, and its keys move between releases — so
read the current schema off [docs.iron.sh](https://docs.iron.sh/) and treat
this as the shape rather than as something to paste:

```yaml
# Illustrative — check the key names against iron-proxy's current release.
default: deny
allow:
  # The model.
  - api.anthropic.com
  # Signing in. An API key needs neither of these; a CLI authenticated as a
  # claude.ai account refreshes a short-lived token against them, and a
  # refresh denied four hours into a shift fails every run after it.
  - claude.ai
  - console.anthropic.com
  # Claude Code's own telemetry and error reporting. Denying these is
  # survivable; it is not silent, so decide deliberately rather than by
  # omission.
  - statsig.anthropic.com
  - api.statsig.com
  - sentry.io
  # GitHub: gh talks to the API, git pushes and fetches over HTTPS.
  - github.com
  - api.github.com
  - codeload.github.com
  - objects.githubusercontent.com
```

That covers polako and the skill. It does **not** cover the repository being
drained. A shift runs that project's build and test commands, and if those
resolve dependencies over the network — `proxy.golang.org` and
`sum.golang.org`, `registry.npmjs.org`, `pypi.org` and
`files.pythonhosted.org`, a private registry, a container mirror — every one of
those is a host you have to add. polako cannot know them, which is most of the
reason it does not ship a list.

The reliable way to find them is to let the proxy tell you: run one shift in
whatever observe-or-log-only mode the current iron-proxy offers, read the audit
log, and promote what you see into the allowlist. Guessing costs you a park per
missing host.

### When it goes wrong

A wrong allowlist does not present as a permission prompt or a clear error. It
presents as **403s from the proxy, surfacing as a run that crashes or an issue
that gets parked** — and the diagnosis is one layer further down than the shift
summary reaches. Two places to look:

- The [shift log](reference.md#the-shift-log--log), which carries the child's
  stderr line by line, prefixed `[claude stderr]`. A TLS or proxy failure says
  so there.
- The proxy's own audit log, which is the only place that names the host that
  was denied.

`polako stats` will show the park, and the park reason will not say "firewall",
because from the supervisor's side nothing distinguishes a blocked host from
an issue the model could not finish.

## Why polako does not do this itself

Reasonable to ask, given it would be a flag or two. Four reasons, and they are
the same four each time:

- **The allowlist cannot be known here.** It is a property of the repository
  being drained, not of polako. A wrong one ships as mysterious 403s and a park
  mid-shift, which is a worse failure than no firewall — it looks like the
  model failing.
- **Neither posture is polako's to choose.** Fail-open makes the boundary
  theatre. Fail-closed adds a hard runtime dependency and a new fatal mode to a
  tool whose whole job is to survive the night unattended. Only the operator
  knows which they want.
- **Interception is a system-footprint change.** Trusting a CA certificate and
  repointing proxy and DNS is a change to the machine, not to a process. That
  is outside the lane of something you `go install`.
- **It would break the distribution contract in spirit.** One stdlib-only
  binary, cross-compiled with nothing but the Go toolchain, is a promise about
  what running polako costs you. "Also install and operate a proxy" is not that
  promise, kept optional.

Composition is the better shape anyway: the firewall is yours, it wraps polako
the same way it wraps anything else you do not trust with a socket, and you can
verify it without reading polako's source.

## What this still does not close

Stated plainly, in the same spirit as the allowlist section of
[security.md](security.md):

- **An environment variable is a default, not a boundary.** Every client in
  the table above honours `HTTPS_PROXY` because it chooses to. A run has a
  shell; anything it invokes can unset the variable, or open a socket that
  never consulted it. If you need enforcement rather than cooperation, the
  proxy has to be the only route out — a container network namespace, a VM, or
  firewall rules that drop everything not addressed to it. That is the real
  version of this, and it is a machine-level arrangement rather than a
  command-line one.
- **An allowlisted host is still a host.** `github.com` is on the list because
  a shift cannot work without it, and it is also somewhere data can be written.
  Egress control raises the cost of exfiltration; it does not make it
  impossible.
- **Nothing here is a substitute for the merge step.** You reading the pull
  request is still the last check on what lands, and the only one that sees
  intent.

Point `-dir` at repositories you would run `make test` in yourself. That advice
does not change because there is a firewall in front of it.
