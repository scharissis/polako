# Where greet is going

`greet.sh` prints a greeting and does nothing else. That is fine as a starting
point and not fine as a tool anybody would install. This document says what it
should become; it does not say how, and it is deliberately not a backlog.

## The shape we want

A small, well-behaved command line tool. Predictable flags, useful output when
you get it wrong, and a test suite that actually covers the behaviour rather
than the happy path.

## What is missing today

**It cannot explain itself.** Run it with no arguments and it says
`greet: needs a name` and exits 2. That tells you that you got it wrong and
nothing about what right looks like. It should accept `--help` and `-h` and
print a usage line.

**It only greets in English.** Hello is hardcoded. People who would use this
tool do not all work in English, and a greeting is precisely the thing that
should not be. There should be a way to ask for a different language, with
English staying the default so nothing that works today stops working.

**It greets exactly one person.** `./greet.sh Ada Grace` ignores Grace
entirely, silently, which is the worst of the available behaviours. Several
names should produce several greetings, or one greeting naming everybody —
that choice is open.

**Nothing checks it on a clean machine.** `./test.sh` exists and is run by
whoever remembers. There is no continuous integration, so a change that breaks
the suite is found by the next person rather than by the push that caused it.

## What we are not doing

No configuration file, no plugin system, no interactive mode. This stays a
program you can read in one sitting.
