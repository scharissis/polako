# The README's demo. Render it with ./scripts/demo.sh, which builds the binary
# from this tree and puts it on PATH first.
#
# Both commands here are real and read-only: `status` only reads, and
# `-dry-run` resolves the next issue and stops without running claude or
# writing anything to GitHub. Nothing in this recording is staged, which is the
# point — a demo of an unattended agent should be something you can re-run.
#
# `polako stats` is deliberately not in it. Its header prints the metrics
# directory, which on most machines is a home directory with a name in it, and
# the numbers it shows are whatever the last two days happened to be. There is
# a sample report in run-data.md instead.

Output docs/demo.gif

Require polako

Set Shell zsh
Set FontSize 15
Set Width 1180
Set Height 560
Set Padding 24
Set TypingSpeed 45ms
Set Theme "Catppuccin Mocha"

# A plain prompt: the recording should not carry a machine name or a home
# directory around with it.
Hide
Type "export PS1='%F{cyan}$%f '" Enter
Type "clear" Enter
Show

Sleep 1s

# Where does the backlog stand? Reads GitHub, reports state rather than
# liveness, and ends with the line only a person can act on.
Type "polako status" Sleep 500ms Enter
Sleep 5s

Type "clear" Sleep 300ms Enter
Sleep 500ms

# What would a shift do next? -dry-run resolves the next issue and prints the
# exact invocation it would run, including the tool allowlist that bounds it.
Type "polako work -dry-run" Sleep 500ms Enter
Sleep 8s
