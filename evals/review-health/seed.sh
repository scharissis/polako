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
#   - skills/deploy/SKILL.md — a prompt file written for an older model:
#                   CRITICAL/MUST/NEVER and "!!", "think step by step", "be
#                   thorough, do not be lazy". The prompt-surface pass should
#                   propose rewriting it. It also holds one reasoned rule —
#                   never deploy without --dry-run, because staging has no
#                   rollback — which a correct run keeps.
#   - CLAUDE.md   — the same kind of cruft, but inside polako's own
#                   <!-- polako:begin --> block, which the skill leaves out:
#                   that text is polako's to fix, not this repo's.
#   - deploy.sh   — the script the skill drives, so the rule above points at
#                   something real rather than being a stale-fact finding.
#
# The repo has no size-budget or complexity gate of its own — test.sh checks
# behaviour only — so a correct run also proposes *that*, the self-propagating
# finding. issues.json seeds one open issue that already asks for the
# handlers.sh split, so re-proposing it is the dedup failure this case catches.

cp "$case_dir/plant/notify.sh" "$repo/notify.sh"
cp "$case_dir/plant/handlers.sh" "$repo/handlers.sh"
cp "$case_dir/plant/deploy.sh" "$repo/deploy.sh"
cp "$case_dir/plant/CLAUDE.md" "$repo/CLAUDE.md"
mkdir -p "$repo/skills/deploy"
cp "$case_dir/plant/skills/deploy/SKILL.md" "$repo/skills/deploy/SKILL.md"
chmod +x "$repo/notify.sh" "$repo/handlers.sh" "$repo/deploy.sh"
git -C "$repo" add notify.sh handlers.sh deploy.sh CLAUDE.md skills
git -C "$repo" -c commit.gpgsign=false commit --quiet -m "feat: add notify, handlers and a deploy skill"
# Pushed so the short SHA the provenance footer names exists on the remote, the
# way it would on a real repository.
git -C "$repo" push --quiet origin main

echo "seeded notify.sh, handlers.sh and the deploy skill, the planted structural and prompt problems"
