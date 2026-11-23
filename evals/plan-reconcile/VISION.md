# Where greet is going

`greet.sh` prints a greeting and nothing else. This is the plan for making it
a tool worth installing.

## Milestones

1. `--help` and `-h` print a usage line and exit 0.
2. A `--lang` flag picks the greeting's language, English by default.
3. Several names give several greetings, in the order given.
4. CI runs `./test.sh` on every push.

## Not doing

No configuration file, no plugin system, no interactive mode.
