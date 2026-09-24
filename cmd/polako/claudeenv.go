package main

import (
	"os"
	"strings"
)

// effortEnv is the CLI's own effort override. Probed on 2.1.280 it beats
// --effort, fresh run or resume.
const effortEnv = "CLAUDE_CODE_EFFORT_LEVEL"

// lookupEnv reads one variable the way a child would see it: cfg.env first,
// last entry winning as it does for os/exec, then the process environment.
// Tests set or blank a variable through cfg.env, so a developer's own export
// can't reach them. An empty value reads as unset, which is also how the
// CLI treats it.
func lookupEnv(cfg config, name string) string {
	for i := len(cfg.env) - 1; i >= 0; i-- {
		if v, ok := strings.CutPrefix(cfg.env[i], name+"="); ok {
			return strings.TrimSpace(v)
		}
	}
	return strings.TrimSpace(os.Getenv(name))
}

// effortFlagsMatch reports whether every effort flag the operator set —
// -effort, -remediation-effort and each -effort-by-size cell — already
// equals the exported level, so the override changes nothing polako asked for.
func effortFlagsMatch(cfg config, env string) bool {
	levels := []string{cfg.effort, cfg.remediationEffort}
	cells, _ := parseEffortBySize(cfg.effortBySize) // parseFlags already refused a bad one
	for _, v := range cells {
		levels = append(levels, v)
	}
	for _, l := range levels {
		if l != "" && !strings.EqualFold(l, env) {
			return false
		}
	}
	return true
}

// warnEffortLabelEnv warns once per pickup when an effort: label (the issue's
// or its epic's) resolved while CLAUDE_CODE_EFFORT_LEVEL is exported with a
// different value — the label would do nothing. Once per pickup rather than
// per dispatch, which would repeat it on every resume. A label is a
// maintainer's call, not the operator's, so this warns rather than refuses.
func warnEffortLabelEnv(cfg config, issue int, labels labelChoice) {
	if !labels.effortSet {
		return
	}
	env := lookupEnv(cfg, effortEnv)
	if env == "" || strings.EqualFold(env, labels.effort) {
		return
	}
	cfg.narrate(sevWarning, "#%d's effort:%s label will not take: %s=%s is exported and the CLI lets it win — "+
		"unset it to run at the label's effort", issue, labels.effort, effortEnv, env)
}

// warnClaudeModelEnv says out loud when the operator's environment carries a
// model or effort override. The child inherits this process's environment by
// design (TestDispatchGivesTheChildTheOperatorsEnvironment pins cmd.Env nil so
// the egress proxy keeps working), so what the CLI does with each variable is
// what a run gets. Probed on 2.1.280: --model beats ANTHROPIC_MODEL;
// ANTHROPIC_DEFAULT_OPUS_MODEL remaps the opus alias itself, --model opus
// included (its sonnet and haiku siblings likely do the same, unprobed); and
// CLAUDE_CODE_EFFORT_LEVEL beats --effort.
func warnClaudeModelEnv(cfg config) {
	for _, v := range []struct{ name, effect string }{
		{"ANTHROPIC_MODEL", "it moves only runs that inherit their model — -model and model: labels still win"},
		{"ANTHROPIC_DEFAULT_OPUS_MODEL", "it remaps opus wherever a run asks for it, -model opus included"},
		{"ANTHROPIC_DEFAULT_SONNET_MODEL", "it likely remaps sonnet wherever a run asks for it (unprobed)"},
		{"ANTHROPIC_DEFAULT_HAIKU_MODEL", "it likely remaps haiku wherever a run asks for it (unprobed)"},
		{effortEnv, "it beats every effort polako passes — -effort, -remediation-effort, -effort-by-size and effort: labels"},
	} {
		if val := lookupEnv(cfg, v.name); val != "" {
			cfg.logf("%s=%s is exported — %s", v.name, val, v.effect)
		}
	}
}
