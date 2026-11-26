package main

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// startupHeader is the first line of a shift: what binary, which repo, which
// issues, which skill, how often it looks. The rows under it are the settings.
func startupHeader(version, repo, scope, skill string, poll time.Duration) string {
	head := repo
	if version != "" {
		head = "polako " + version + " · " + repo
	}
	return fmt.Sprintf("%s — %s, /%s, polling every %s", head, scope, skill, dur(poll))
}

// queueScope says which issues a work shift drains.
func queueScope(label string) string {
	if label == "" {
		return "all open issues"
	}
	return fmt.Sprintf("issues labelled %q", label)
}

// modelRow is never empty: the model is the setting that most changes what a
// run costs, so it always shows. With nothing set it names no model, since an
// inherited one isn't known until a run starts and each run logs the one it
// gets. Every knob is shown when set because a POLAKO_* variable can set it
// silently.
func modelRow(cfg config) string {
	model := "Claude Code's default"
	if cfg.model != "" {
		model = cfg.model
	}
	effort := "effort default"
	if cfg.effort != "" {
		effort = "effort " + cfg.effort
	}
	parts := []string{model, effort}
	if cfg.modelBySize != "" {
		parts = append(parts, "model-by-size "+cfg.modelBySize)
	}
	if cfg.effortBySize != "" {
		parts = append(parts, "effort-by-size "+cfg.effortBySize)
	}
	if cfg.remediationModel != "" {
		parts = append(parts, "remediation-model "+cfg.remediationModel)
	}
	if cfg.remediationEffort != "" {
		parts = append(parts, "remediation-effort "+cfg.remediationEffort)
	}
	return strings.Join(parts, ", ")
}

// limitsRow names the cost and time caps in force, in plain words rather than
// flag names. Worth saying because the environment can set any of them, so a
// park quoting a cap nobody typed has its number on screen at startup. The
// usage thresholds live on the usage row, beside the readings they're
// compared against.
func limitsRow(cfg config) string {
	var perIssue []string
	if cfg.maxCost > 0 {
		perIssue = append(perIssue, roundUSD(cfg.maxCost))
	}
	if cfg.maxIssueTime > 0 {
		perIssue = append(perIssue, dur(cfg.maxIssueTime))
	}
	var parts []string
	if len(perIssue) > 0 {
		parts = append(parts, strings.Join(perIssue, " and ")+" per issue")
	}
	if cfg.maxSessionCost > 0 {
		parts = append(parts, roundUSD(cfg.maxSessionCost)+" per shift")
	}
	return strings.Join(parts, ", ")
}

// roundUSD drops the cents from a whole-dollar cap: "$15", but "$2.50".
func roundUSD(f float64) string {
	if f == math.Trunc(f) {
		return fmt.Sprintf("$%.0f", f)
	}
	return usd(f)
}

// usageRow puts each plan pool's reading beside the threshold that pauses the
// shift. A threshold with no reading says so rather than looking like zero.
// Empty when there is neither a reading nor a threshold. status keeps its own
// plan: line (usageLine); this one is only the startup row.
func usageRow(snap *usageSnapshot, sessionMax, weekMax int) string {
	var read, unknown []string
	for _, pool := range []struct {
		label string
		max   int
	}{{"session", sessionMax}, {"week", weekMax}} {
		var p usagePool
		ok := false
		if snap != nil {
			p, ok = poolByLabel(snap.pools, pool.label)
		}
		if !ok {
			if pool.max > 0 {
				unknown = append(unknown, fmt.Sprintf("%s %d%%", pool.label, pool.max))
			}
			continue
		}
		var notes []string
		if pool.max > 0 {
			notes = append(notes, fmt.Sprintf("pauses at %d%%", pool.max))
		}
		if p.hasReset && pool.label == "week" {
			notes = append(notes, "resets "+formatUsageReset(p.reset))
		}
		s := fmt.Sprintf("%s %d%%", pool.label, p.percent)
		if len(notes) > 0 {
			s += " (" + strings.Join(notes, ", ") + ")"
		}
		read = append(read, s)
	}
	if len(unknown) > 0 {
		read = append(read, "pauses at "+strings.Join(unknown, ", ")+" (current usage unknown)")
	}
	return strings.Join(read, " · ")
}

// homeTilde shortens a path under the home directory to start with ~.
func homeTilde(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == home {
		return "~"
	}
	if rest, ok := strings.CutPrefix(path, home+string(filepath.Separator)); ok {
		return "~" + string(filepath.Separator) + rest
	}
	return path
}
