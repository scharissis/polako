# Sourced by lib/scaffold.sh, with $repo, $workspace and $case_dir in scope.
#
# Plants the structural problems review-health is meant to notice, on top of the
# shared greet fixture:
#
#   - notify.sh   — three functions (notify_slack/email/sms) that differ only in
#                   a channel name and an endpoint: the "same logic three times,
#                   one parameter apart" finding.
#   - handlers.sh — ~140 lines against a tree whose other files are 10–35, and
#                   two responsibilities (arg parsing, output rendering) that
#                   never call each other: the accretion / oversized-file
#                   finding.
#
# The repo has no size-budget or complexity gate of its own — test.sh checks
# behaviour only — so a correct run also proposes *that*, the self-propagating
# finding. issues.json seeds one open issue that already asks for the
# handlers.sh split, so re-proposing it is the dedup failure this case catches.

cp "$case_dir/plant/notify.sh" "$repo/notify.sh"
cp "$case_dir/plant/handlers.sh" "$repo/handlers.sh"
chmod +x "$repo/notify.sh" "$repo/handlers.sh"
git -C "$repo" add notify.sh handlers.sh
git -C "$repo" -c commit.gpgsign=false commit --quiet -m "feat: add notify and handlers helpers"
# Pushed so the short SHA the provenance footer names exists on the remote, the
# way it would on a real repository.
git -C "$repo" push --quiet origin main

echo "seeded $repo/notify.sh and $repo/handlers.sh, the planted structural problems"
