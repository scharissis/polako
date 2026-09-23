package main

// `design-plan` works one design request into a plan document under
// docs/designs/, behind a PR (docs/designs/design.md, ticket 2). It runs by
// hand as `/polako:design-plan N` until the `design` verb lands here beside
// these constants, the way healthSkillDir sits in health.go beside `health`.

// designSkillDir is the skill under skills/ a design run invokes.
const designSkillDir = "design-plan"

// defaultDesignSkill is the plugin-namespaced slash command, the same shape
// defaultPlanSkill and defaultHealthSkill have.
const defaultDesignSkill = "polako:" + designSkillDir
