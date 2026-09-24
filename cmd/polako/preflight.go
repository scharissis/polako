package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// preflight fails fast on a misconfigured environment, so an unattended run
// can't die on its first gh call an hour after being started.
func preflight(ctx context.Context, cfg *config) error {
	if err := preflightShared(ctx, cfg, func(visibility string) error { return workGates(ctx, cfg, visibility) }); err != nil {
		return err
	}
	cfg.logf("%s — running /%s per issue, polling every %s", cfg.repo, cfg.skill, cfg.poll)
	settingsBlock(*cfg, preflightPairs(*cfg))
	return nil
}

// workGates is the part of work's preflight that exists because work drains
// a queue: the public-repo and -label gates, and the label a queued run's
// questions are flagged with.
func workGates(ctx context.Context, cfg *config, visibility string) error {
	// A dry run may still look past either gate below: it runs nothing and
	// writes nothing, and seeing what a real run would refuse is how an
	// operator decides what to change. refuseOrNote is that one carve-out,
	// shared so a real refusal and its dry-run preview can never say it two
	// different ways.
	//
	// The marked-label lookup below only runs in the one shape that is about
	// to refuse (or, on -dry-run, note the refusal) — asked of queueGate
	// itself, with no marked label yet, rather than a hand-derived copy of
	// its condition that could drift from it. Every other run of this
	// preflight costs nothing extra. Best-effort, and cfg.label itself is
	// never set from it: work still never scopes itself, this only changes
	// what the message names.
	var markedLabel string
	if queueGate(visibility, cfg.label, cfg.ungated, "") != nil {
		markedLabel, _, _ = markedGateLabel(ctx, *cfg)
	}
	if err := refuseOrNote(*cfg, queueGate(visibility, cfg.label, cfg.ungated, markedLabel), cfg.dryRun); err != nil {
		return err
	}
	// A -label the repository has never defined would otherwise pass the gate
	// above and drain an empty queue all shift, reported as success. A real
	// lookup failure (not a definitive "no") is not this gate's business — it
	// fails preflight outright, the same as preflightShared's `gh repo view`.
	if cfg.label != "" {
		exists, err := labelExists(ctx, *cfg, cfg.label)
		if err != nil {
			return fmt.Errorf("checking whether the %s label exists: %w", cfg.label, err)
		}
		if err := refuseOrNote(*cfg, labelGate(cfg.label, exists), cfg.dryRun); err != nil {
			return err
		}
	}
	if cfg.ungated && strings.EqualFold(visibility, "PUBLIC") {
		// Said out loud like -remote and -post-summary are, and for the same
		// reason: the environment can set this too, and it is the one flag that
		// hands the queue to whoever can open an issue.
		cfg.logf("-ungated on a public repository — every open issue is in the queue, whoever filed it")
	}
	// Defined up front rather than when a run first needs it: GitHub refuses to
	// apply a label the repository never declared, and the run that applies this
	// one is a headless session holding no grant that could create it. Discovering
	// that at the moment a question needs flagging is the expensive time to find
	// out — the question gets posted and then never waited on.
	//
	// Not on a dry run, which declares nothing: creating a label is a write, and
	// the promise is that it leaves the repository as it found it.
	if !cfg.dryRun {
		l := labelByName(awaitingAnswerLabel)
		_ = ensureLabel(ctx, *cfg, l.name, l.color, l.description)
	}
	return nil
}

// preflightShared is the half of preflight that doesn't depend on which verb
// is starting runs: tools, repository, shift log, versions and the gates on
// them. The caller's own gates go in gate, handed the repository's
// visibility — a verb that works one named issue has no queue to gate, and
// would be refused on a public repo for no reason if this checked it. gate
// runs as soon as the repository is known, not after this returns, so a
// refusal still lands before the version checks, usage probe and
// published-version read instead of paying for them first. nil means none.
func preflightShared(ctx context.Context, cfg *config, gate func(visibility string) error) error {
	for _, bin := range []string{cfg.claudeBin, cfg.ghBin, "git"} {
		if _, err := exec.LookPath(bin); err != nil {
			return fmt.Errorf("%q not found on PATH: %w", bin, err)
		}
	}
	if err := checkNotifyCommand(cfg.notifyCmd); err != nil {
		return err
	}
	if _, err := git(ctx, *cfg, "rev-parse", "--git-dir"); err != nil {
		return fmt.Errorf("-dir %s is not a git checkout: %w", cfg.dir, err)
	}
	out, err := gh(ctx, *cfg, "repo", "view", "--json", "nameWithOwner,visibility")
	if err != nil {
		return fmt.Errorf("no GitHub repository reachable from %s (is gh authenticated?): %w", cfg.dir, err)
	}
	var repoView struct {
		NameWithOwner string `json:"nameWithOwner"`
		Visibility    string `json:"visibility"`
	}
	if err := json.Unmarshal(out, &repoView); err != nil {
		return fmt.Errorf("unreadable `gh repo view` reply (is gh current?): %w", err)
	}
	cfg.repo = repoView.NameWithOwner
	// As soon as the repository is known, because the file is named after it.
	// Everything logged from here on lands in the shift log too — including a
	// preflight refusal, which is often the diagnosis an operator wants.
	if cfg.logDir != "" {
		path, err := cfg.sink().openShiftLog(cfg.logDir, cfg.repo, cfg.shiftID)
		if err != nil {
			cfg.narrate(sevWarning, logLostFmt, err)
		} else {
			cfg.logPath = path
		}
	}
	if gate != nil {
		if err := gate(repoView.Visibility); err != nil {
			return err
		}
	}
	cfg.claudeVersion = claudeVersion(ctx, *cfg)
	cfg.pluginVersion, _, _ = pluginVersion(ctx, *cfg)
	warnClaudeModelEnv(*cfg)
	if err := refuseOrNote(*cfg, effortFlagGate(ctx, *cfg), cfg.dryRun); err != nil {
		return err
	}
	if snap, ok := probeUsage(ctx, *cfg); ok {
		cfg.usage = &snap
	}
	skewErr := versionSkewGate(polakoVersion(), *cfg)
	if err := refuseOrNote(*cfg, skewErr, cfg.dryRun); err != nil {
		return err
	}
	if skewErr == nil {
		if self, plugin, behind, ok := skewComparison(polakoVersion(), *cfg); cfg.ignoreSkew && ok && behind {
			// Said out loud, the same shape -ungated gets on a public repo:
			// the gate itself already let this through (it reads
			// cfg.ignoreSkew, same as queueGate reads cfg.ungated), so this
			// is the operator's own line recording that an override actually
			// fired, not silent normal operation.
			cfg.logf("-ignore-skew: the installed %s plugin (%s) is behind this binary (%s) — running anyway",
				pluginName, plugin, self)
		}
		// Only the "behind" case above ever refuses; a newer or ambiguous
		// mismatch — or a "behind" one -ignore-skew just let through — is
		// still worth a line, same as before this gate existed. Gated on
		// skewErr rather than folded into the block above: refuseOrNote
		// already turned a dry-run refusal into a logged note and a nil
		// return, and that note must not also get this second, differently
		// worded line about the same skew.
		warnOnVersionSkew(polakoVersion(), *cfg)
	}
	// Independent of the skew block above — this compares against what's
	// published, not the binary against the plugin — so it runs whether or
	// not skewErr is nil, including a dry run whose skew gate just refused.
	if line := updateNoticeLine(ctx, polakoVersion(), *cfg); line != "" {
		cfg.narrate(sevWarning, "%s", line)
	}
	return nil
}

// preflightPairs builds the startup recap as row pairs: what used to be a
// run of full sentences becomes one fact per row, aligned like status and
// stats. Every row but "queue" is gated by the exact condition its sentence
// used before this — reshaping what preflight already said, not changing
// what those rows disclose. "queue" (-label) is new disclosure, not a
// reshape; its own comment below says why. Row order inside the block
// matches the old sentence order, but warnOnVersionSkew (called in
// preflightShared) now always prints ahead of the whole block rather than
// being interleaved with -dry-run the way the two sentences used to be — a
// real, if cosmetic, reordering worth knowing about before trusting this
// comment too literally.
func preflightPairs(cfg config) [][2]string {
	var pairs [][2]string
	// Unconditional, unlike every row below it: a shift closes a finished
	// container (every child closed) on its own, writing to human-curated state
	// without being asked, which is the class of thing -post-summary and -remote
	// disclose at startup. Held with needs-human or proposed, it is left alone.
	pairs = append(pairs, [2]string{"epics",
		"a finished container (every sub-issue closed) is closed with a comment saying so — " +
			"put needs-human on one to hold it open"})
	if cfg.label != "" {
		// Not said at all before this: the issue asks for -label to surface
		// here. Unset (the default, unfiltered queue) says nothing, the same
		// way an off flag elsewhere earns no row — there is nothing to
		// disclose about it that -ungated's own warning does not already say.
		pairs = append(pairs, [2]string{"queue", fmt.Sprintf("label %q", cfg.label)})
	}
	if line := modelEffortLine(cfg); line != "" {
		// One row for every model and effort knob. The environment can set
		// any of them (POLAKO_MODEL, POLAKO_REMEDIATION_EFFORT, ...), so an
		// operator who forgot the export
		// should not have to work out why every run is on a model they did not
		// type — the same reason -post-summary earns a row.
		pairs = append(pairs, [2]string{"model", line})
	}
	if cfg.dryRun {
		pairs = append(pairs, [2]string{"dry-run",
			"resolving the next issue only — no claude run, no GitHub write, no run data"})
	}
	if notes := capNotes(cfg); notes != "" {
		pairs = append(pairs, [2]string{"caps", notes})
	}
	if cfg.usage != nil {
		if line := usageLine(*cfg.usage); line != "" {
			pairs = append(pairs, [2]string{"plan", line})
		}
	}
	if cfg.postSummary {
		// The environment can set this, so say it out loud: an operator who
		// forgot the variable is in their profile should not have to work out
		// where the PR comments are coming from.
		pairs = append(pairs, [2]string{"post-summary", "on — each merged PR gets one comment of run numbers"})
	}
	if cfg.notifyCmd != "" {
		pairs = append(pairs, [2]string{"notify", fmt.Sprintf(
			"on — `%s` runs when an issue parks, an issue asks a question, the backlog clears, or the shift stops early",
			cfg.notifyCmd)})
	}
	if cfg.remote {
		// Said every time, unprompted, like the recorder's line and for the same
		// reason: this is the one thing a shift does that makes its sessions
		// visible somewhere other than this terminal, and it is on by default.
		pairs = append(pairs, [2]string{"remote",
			"on — each run registers with Remote Control under the operator's own claude.ai account, " +
				"watchable and typeable from claude.ai/code or the app (-remote=false keeps runs to this machine)"})
	}
	if cfg.rec.enabled() {
		// Say where the data goes, every time, unprompted: it is the whole of
		// the answer to "what does this tool record".
		pairs = append(pairs, [2]string{"run data",
			fmt.Sprintf("%s — numbers only, never leaves this machine (-metrics off to disable)", cfg.rec.dir)})
		// The one place the id is ever shown. Nothing reads it back, so a
		// report on this drain alone is unaskable unless this line is where an
		// operator finds it — including while the drain is still running.
		pairs = append(pairs, [2]string{"shift",
			fmt.Sprintf("%s — `polako stats -shift %s` reports on it alone", cfg.shiftID, cfg.shiftID)})
	}
	if cfg.logPath != "" {
		// The disclosure, said every time like the recorder's line: unlike the
		// run-data records this file holds transcript text, so where it lives
		// and that it stays local is worth a line per shift.
		pairs = append(pairs, [2]string{"shift log",
			fmt.Sprintf("%s — the whole claude transcript stream, kept on this machine (-log off to disable)", cfg.logPath)})
	}
	return pairs
}

// modelEffortLine renders the settings-block value for -model, -effort,
// -model-by-size, -effort-by-size, -remediation-model and
// -remediation-effort: whichever were set, "" when none were so the row is
// skipped. "inherit" is not spelled — an omitted flag adds nothing to
// disclose. Every one is shown since a POLAKO_* variable can set it silently.
func modelEffortLine(cfg config) string {
	var parts []string
	if cfg.model != "" {
		parts = append(parts, "model "+cfg.model)
	}
	if cfg.effort != "" {
		parts = append(parts, "effort "+cfg.effort)
	}
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
