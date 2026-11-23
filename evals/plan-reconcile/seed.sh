# Sourced by lib/scaffold.sh, with $repo, $workspace and $case_dir in scope.
#
# A vision with four milestones, two of which already have issues carrying
# their markers (issues.json, issues-closed.json), and the items file
# `polako plan` writes for the other two. The items file is shaped exactly as
# itemsManifest (cmd/polako/reconcile_manifest.go) renders it, so this grades
# the skill on what the binary actually hands it.

mkdir -p "$repo/docs"
cp "$case_dir/VISION.md" "$repo/docs/VISION.md"
git -C "$repo" add docs/VISION.md
git -C "$repo" -c commit.gpgsign=false commit --quiet -m "docs: add the vision document"
git -C "$repo" push --quiet origin main
sha=$(git -C "$repo" rev-parse --short HEAD)

mkdir -p "$repo/.polako-scratch"
cat > "$repo/.polako-scratch/plan-items.md" <<ITEMS
# Items to file

From docs/VISION.md (slug VISION) at $sha. File one issue per item below, its marker line
directly above the provenance footer.

## VISION/M2: A --lang flag picks the greeting's language, English by default

Its marker line:

    <!-- polako:item=VISION/M2 -->

The doc's entry:

> A \`--lang\` flag picks the greeting's language, English by default.

## VISION/M3: Several names give several greetings, in the order given

Its marker line:

    <!-- polako:item=VISION/M3 -->

The doc's entry:

> Several names give several greetings, in the order given.

## Items that already have an issue

For \`Depends on:\` and \`--blocked-by\`. Don't file these.

- VISION/M1: #1 (queued)
- VISION/M4: #3 (done)
ITEMS

echo "seeded docs/VISION.md and the items file for M2 and M3"
