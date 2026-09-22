# The README's demo. Render it with ./scripts/demo.sh, which builds the binary
# from this tree and puts it on PATH first.
#
# Every command here is real and read-only: a bare `polako` prints the verb
# table, `status` only reads, and `-dry-run` — on `plan` and on `work` — prints
# the invocation and stops without running claude or writing anything to
# GitHub. Nothing in this recording is staged, which is the point — a demo of
# an unattended agent should be something you can re-run.
#
# Four verbs are named by the table and deliberately not run. `stats` prints
# the metrics directory, and `tidy` and `health -dry-run` print checkout paths
# — on most machines a home directory with a name in it — and stats' numbers
# are whatever the last two days happened to be. `update -check` warns about
# where the binary lives, and demo.sh builds it into a temp dir. Sample output
# for each is in run-data.md, reference.md and install.md instead.

Output docs/demo.gif

Require polako

Set Shell zsh
Set FontSize 15
Set Width 1180
Set Height 640
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

# What can it do? A bare invocation prints the seven verbs rather than
# defaulting to the one that starts an unattended loop.
Type "polako" Sleep 500ms Enter
Sleep 6s

Type "clear" Sleep 300ms Enter
Sleep 500ms

# Where does the backlog stand? Reads GitHub, reports state rather than
# liveness, and ends with the line only a person can act on.
Type "polako status" Sleep 500ms Enter
Sleep 10s

Type "clear" Sleep 300ms Enter
Sleep 500ms

# What would filling the backlog run? Same -dry-run, on `plan`: the cap, the
# milestone, and a narrower allowlist than work's.
Type "polako plan -design docs/VISION.md -dry-run" Sleep 500ms Enter
Sleep 7s

Type "clear" Sleep 300ms Enter
Sleep 500ms

# What would a shift do next? -dry-run resolves the next issue and prints the
# exact invocation it would run, including the tool allowlist that bounds it.
Type "polako work -label ready -dry-run" Sleep 500ms Enter
Sleep 8s
