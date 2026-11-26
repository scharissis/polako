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
	cfg.logf("%s", startupHeader(polakoVersion(), cfg.repo, queueScope(cfg.label), cfg.skill, cfg.poll))
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
	if err := checkoutError(ctx, *cfg); err != nil {
		return err
	}
	out, err := gh(ctx, *cfg, "repo", "view", "--json", "nameWithOwner,visibility")
	if err != nil {
		return repoViewError(ctx, *cfg, err, "")
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
	// The records and each PR's ran-on block read provider, the block even
	// under -metrics off; a dry run writes neither, so it skips the extra
	// process — and the account details its reply carries.
	if !cfg.dryRun {
		cfg.provider = claudeProvider(ctx, *cfg)
	}
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

// preflightPairs builds the startup recap under the header, one setting per
// row, in the order an operator reads them: what runs, its limits, what
// reaches past this terminal, then where to look afterwards. Only settings
// earn a row. The epic auto-close is a rule no flag changes, so it isn't one:
// the close is reported where it happens (thread comment, exit summary,
// epic-done), not on every shift of a repo that may have no containers.
// Warnings (version skew, -ungated, ANTHROPIC_*) print as full lines before
// this block, not as rows.
func preflightPairs(cfg config) [][2]string {
	var pairs [][2]string
	if cfg.dryRun {
		pairs = append(pairs, [2]string{"dry-run",
			"resolving the next issue only — no claude run, no GitHub write, no run data"})
	}
	pairs = append(pairs, [2]string{"model", modelRow(cfg)})
	if line := limitsRow(cfg); line != "" {
		pairs = append(pairs, [2]string{"limits", line})
	}
	if line := usageRow(cfg.usage, cfg.maxSessionUsage, cfg.maxWeekUsage); line != "" {
		pairs = append(pairs, [2]string{"usage", line})
	}
	if cfg.notifyCmd != "" {
		pairs = append(pairs, [2]string{"notify", cfg.notifyCmd + " — on park, question, empty backlog, early stop"})
	}
	if cfg.postSummary {
		// The environment can set this, so say it out loud: an operator who
		// forgot the variable is in their profile should not have to work out
		// where the PR comments are coming from.
		pairs = append(pairs, [2]string{"post-summary", "on — each merged PR gets one comment of run numbers"})
	}
	if cfg.remote {
		// Said every time, unprompted: this is the one thing a shift does that
		// makes its sessions visible somewhere other than this terminal, it is
		// on by default, and registration asks nothing — this row is the
		// operator's only notice that runs land in their claude.ai account.
		pairs = append(pairs, [2]string{"remote",
			"on — runs show in your claude.ai account to watch and type into (-remote=false keeps them local)"})
	}
	if cfg.rec.enabled() {
		// The one place the id is ever shown. Nothing reads it back, so a
		// report on this drain alone is unaskable unless this line is where an
		// operator finds it — including while the drain is still running.
		pairs = append(pairs, [2]string{"shift", fmt.Sprintf("%s — polako stats -shift %s", cfg.shiftID, cfg.shiftID)})
	}
	if cfg.logPath != "" {
		// The disclosure, said every time: unlike the run-data records this
		// file holds transcript text, so where it lives and that it stays local
		// is worth a line per shift.
		pairs = append(pairs, [2]string{"log",
			homeTilde(cfg.logPath) + " — full transcript, local (-log off)"})
	}
	if cfg.rec.enabled() {
		// Say where the data goes, every time, unprompted: it is the whole of
		// the answer to "what does this tool record".
		pairs = append(pairs, [2]string{"run data",
			homeTilde(cfg.rec.dir) + " — numbers only, local (-metrics off)"})
	}
	return pairs
}
