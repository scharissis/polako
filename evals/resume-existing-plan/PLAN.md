# Plan — issue #1: --shout flag for greet.sh

Status: **FINAL**. Nothing is blocked; this is ready to implement.

## Approach

Parse `--shout` as an optional leading flag in `greet.sh`, before the
missing-name check, so `--shout` with no name still errors and exits 2.

Upper-case with `tr '[:lower:]' '[:upper:]'` rather than a bash `${var^^}`
expansion. The script's shebang is `/usr/bin/env sh`, and `${var^^}` is a
bashism that fails under dash — the fixture is POSIX sh on purpose and it stays
that way.

## Files to touch

- `greet.sh` — flag parsing and the upper-casing.
- `test.sh` — one case for `--shout World`, one confirming the unshouted path
  is unchanged.

## Deliberately left out

- A short `-s` alias. The issue asks for `--shout` and nothing else, and an
  alias nobody requested is an interface to support forever.
- Upper-casing anything but the final output line, so the error path keeps its
  existing wording.
