package main

// The usage probe: one reader for `claude -p "/usage" --output-format
// json`, and the one line both `work`'s startup banner and `polako status`
// print from it. It decides nothing and records nothing — the gate and the
// ledger that will build on this reader are #135's other two siblings, not
// this file.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// defaultUsageProbeTimeout bounds the probe's own exec. CLI 2.1.250 answers
// in about a second — no model turn happens — so anything this much slower
// is not coming back, and the caller's wait is better spent elsewhere.
const defaultUsageProbeTimeout = 20 * time.Second

// usagePool is one meter the CLI's own /usage reports — "session", "week
// (all models)", "week (Fable)" today, but the set is account-dependent, so
// nothing here assumes a count.
type usagePool struct {
	name     string
	percent  int
	reset    time.Time
	hasReset bool
}

// usageAttribution is the probe's second block — what contributed to the
// pools above, over the CLI's own lookback window. Self-described by the
// CLI as approximate and machine-local, unlike the pools' percentages and
// resets, and reported for exactly that reason: nothing here decides
// anything, only the one number the banner names as this binary's own
// share of the window.
type usageAttribution struct {
	windowLabel string
	// plugin is the id parseUsageAttribution searched the "Top plugins" line
	// for — cfg.skill's own plugin half, not a compile-time constant, because
	// -skill is documented as pointing at any plugin, not only this repo's own.
	plugin           string
	pluginPercent    int
	hasPluginPercent bool
}

// usageSnapshot is the whole answer probeUsage can give.
type usageSnapshot struct {
	pools          []usagePool
	attribution    usageAttribution
	hasAttribution bool
}

// probeUsage asks the CLI what the account's own plan usage looks like. It
// never goes through execClaude: that path streams events, drives the stall
// watchdog and writes the shift log, none of which a roughly one-second,
// zero-turn call needs — and execClaude's own bookkeeping would count a
// call that burns no tokens as a run. ok is false whenever the probe cannot
// be trusted — the exec failed, the payload didn't parse, the CLI reported
// an error, or the text isn't usage at all (an account with no subscription
// answers /usage in prose this cannot parse, and API-key auth is a
// supported way to run this tool) — never a zero snapshot standing in for
// "could not tell". A probe that fails says so in exactly one log line:
// never a park, a retry storm, or a stopped shift. It writes no run record
// and counts toward no cap — it is not a run.
func probeUsage(ctx context.Context, cfg config) (usageSnapshot, bool) {
	timeout := cfg.usageTimeout
	if timeout <= 0 {
		timeout = defaultUsageProbeTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, err := capture(ctx, cfg.dir, cfg.claudeBin, "-p", "/usage", "--output-format", "json")
	if err != nil {
		log.Printf("usage probe: %v", err)
		return usageSnapshot{}, false
	}
	// streamEvent's fields line up with a plain (non-streaming) `--output-format
	// json` reply too — the same IsError/Result pair the CLI's stream-json
	// "result" event carries — so this reuses that type rather than
	// redeclaring the same two fields under a new name.
	var res streamEvent
	if err := json.Unmarshal(out, &res); err != nil {
		log.Printf("usage probe: parsing the CLI's response: %v", err)
		return usageSnapshot{}, false
	}
	if res.IsError || strings.TrimSpace(res.Result) == "" {
		log.Printf("usage probe: the CLI reported no usage")
		return usageSnapshot{}, false
	}
	// The plugin this run's own share should be read off is whichever plugin
	// -skill names, not always this repo's own: -skill is documented as
	// pointing anywhere, and warnOnVersionSkew already cuts cfg.skill this
	// same way for the same reason. status has no -skill at all (cfg.skill
	// is always "" there) and reports on this repo's own plugin by default,
	// same as an unconfigured -skill would; only an explicit -skill naming a
	// hand-installed skill (no "plugin:" prefix) names no plugin to
	// attribute to at all.
	plugin := pluginName
	if cfg.skill != "" {
		name, _, ok := strings.Cut(cfg.skill, ":")
		if !ok {
			name = ""
		}
		plugin = name
	}
	snap, ok := parseUsage(res.Result, time.Now(), plugin)
	if !ok {
		log.Printf("usage probe: could not read a plan out of the CLI's /usage output")
	}
	return snap, ok
}

// usagePoolRe reads one pool line — "Current session: 42% used · resets Aug
// 28 at 10:20pm (Europe/London)" — per the CLI's own /usage wording. The
// reset clause is optional, and the separator before it (a middle dot
// today) is not itself part of the match, so a wording change to the
// separator alone still leaves the pool and its reset readable.
var usagePoolRe = regexp.MustCompile(`(?im)^Current\s+(.+?):\s*(\d{1,3})%\s+used(?:[^\n]*?\bresets\s+([^\n]+))?\s*$`)

// parseUsage reads pools and, best-effort, an attribution line out of the
// CLI's /usage text. ok is true iff at least one pool parsed — a wording
// change and a no-subscription account (API-key auth) both read as zero
// pools, which is "no plan to report" rather than an error. A payload that
// parses in part — one pool line readable, another not — yields the pools
// it understood, per the same doctrine limitRefusal and limitReset use:
// head-anchored matching, and no guessed value ever stands in for one that
// could not be read.
func parseUsage(text string, now time.Time, plugin string) (usageSnapshot, bool) {
	var snap usageSnapshot
	for _, m := range usagePoolRe.FindAllStringSubmatch(text, -1) {
		pct, err := strconv.Atoi(m[2])
		if err != nil || pct < 0 || pct > 100 {
			continue
		}
		pool := usagePool{name: strings.TrimSpace(m[1]), percent: pct}
		if clause := strings.TrimSpace(m[3]); clause != "" {
			if reset, ok := usageReset(clause, now); ok {
				pool.reset, pool.hasReset = reset, true
			}
		}
		snap.pools = append(snap.pools, pool)
	}
	if len(snap.pools) == 0 {
		return usageSnapshot{}, false
	}
	snap.attribution, snap.hasAttribution = parseUsageAttribution(text, plugin)
	return snap, true
}

// usageResetRe reads a usage probe's reset clause — "Aug 28 at 10:20pm
// (Europe/London)" — a dated instant, unlike a limit refusal's bare
// next-occurrence clock. limitResetRe explicitly cannot read this form (see
// limit_test.go's "a dated reset is beyond this parser"); this is the dated
// sibling, sharing clock12h/resolveZone rather than weakening that parser
// to cover both shapes.
var usageResetRe = regexp.MustCompile(`(?i)^([A-Za-z]{3,9})\s+(\d{1,2})\s+at\s+(\d{1,2})(?::([0-5]\d))?\s*([ap]m)(?:\s*\(([^)]+)\))?$`)

// usageResetStaleGrace is how far behind now a computed reset may fall
// before it is read as last year's date rather than this year's — as
// opposed to a genuine year-boundary date. A session or weekly reset is
// never more than a few days out, so a gap this size can only mean the
// clause names a date that has already turned over into a new year; a
// smaller gap is far more likely this probe's clock reads a little behind
// the CLI's than that this account's meter is over a month stale, and
// rolling the year forward on that guess would be wrong far more
// dramatically than leaving a slightly-stale-looking date alone.
const usageResetStaleGrace = 30 * 24 * time.Hour

// usageReset turns a dated reset clause into the instant it names. False
// whenever any part cannot be trusted, for the same reason limitReset is: a
// wrong date shown is worse than none.
func usageReset(clause string, now time.Time) (time.Time, bool) {
	m := usageResetRe.FindStringSubmatch(strings.TrimSpace(clause))
	if m == nil {
		return time.Time{}, false
	}
	month, ok := parseMonthName(m[1])
	if !ok {
		return time.Time{}, false
	}
	day, err := strconv.Atoi(m[2])
	if err != nil || day < 1 || day > 31 {
		return time.Time{}, false
	}
	hour, minute, ok := clock12h(m[3], m[4], m[5])
	if !ok {
		return time.Time{}, false
	}
	loc, ok := resolveZone(m[6], now.Location())
	if !ok {
		return time.Time{}, false
	}
	at := now.In(loc)
	// Tried this year and next, rather than at.Year() alone: time.Date does
	// not reject an invalid day, it normalizes one, and a Feb 29 clause is
	// only ever a real date in a leap year. Round-tripping the month/day
	// catches that case (this year's Feb 29 candidate would otherwise
	// silently become Mar 1 and then be judged stale) as well as any other
	// day/month combination this build's own bounds do not already rule
	// out.
	for _, year := range [...]int{at.Year(), at.Year() + 1} {
		reset := time.Date(year, month, day, hour, minute, 0, 0, loc)
		if reset.Month() != month || reset.Day() != day {
			continue
		}
		if at.Sub(reset) <= usageResetStaleGrace {
			return reset, true
		}
	}
	return time.Time{}, false
}

// parseMonthName accepts both the short ("Aug") and long ("August") forms
// the CLI might print, case-insensitively.
func parseMonthName(s string) (time.Month, bool) {
	for m := time.January; m <= time.December; m++ {
		name := m.String()
		if strings.EqualFold(name, s) || strings.EqualFold(name[:3], s) {
			return m, true
		}
	}
	return 0, false
}

// usageWindowRe reads the attribution block's lookback window — "Last 24h ·
// 3084 requests · 47 sessions" — down to the short label the banner names
// ("24h"). The request/session counts are not carried further: nothing
// here has a use for them beyond the CLI's own printed report, which this
// probe does not reproduce.
var usageWindowRe = regexp.MustCompile(`(?im)^Last\s+(\S+)`)

// usageTopPluginsRe reads the "Top plugins: polako 29%, other 5%" line.
var usageTopPluginsRe = regexp.MustCompile(`(?im)^\s*Top plugins:\s*(.+)$`)

// usagePluginShareRe pulls one "name N%" entry out of a Top plugins line.
var usagePluginShareRe = regexp.MustCompile(`([\w.:/-]+)\s+(\d{1,3})%`)

// parseUsageAttribution is the attribution half of parseUsage, kept
// separate because it is optional in a way pools are not: an account or CLI
// version that answers /usage with pools alone (no attribution section) is
// still a full answer, not a partial one. ok is true if either half — the
// window label or this binary's own plugin share — was found; the banner
// only ever needs both together, but a probe partial in this half too is
// still worth carrying. plugin is empty for a hand-installed skill (no
// "plugin:" prefix on -skill), which names no plugin to search for at all.
func parseUsageAttribution(text string, plugin string) (usageAttribution, bool) {
	var attr usageAttribution
	found := false
	if m := usageWindowRe.FindStringSubmatch(text); m != nil {
		attr.windowLabel = m[1]
		found = true
	}
	if plugin != "" {
		if m := usageTopPluginsRe.FindStringSubmatch(text); m != nil {
			for _, p := range usagePluginShareRe.FindAllStringSubmatch(m[1], -1) {
				if strings.EqualFold(p[1], plugin) {
					if pct, err := strconv.Atoi(p[2]); err == nil && pct >= 0 && pct <= 100 {
						attr.plugin, attr.pluginPercent, attr.hasPluginPercent = p[1], pct, true
						found = true
					}
					break
				}
			}
		}
	}
	return attr, found
}

// poolByLabel finds the pool a short banner label should be read off: an
// exact name match, or with none, the "(all models)" qualified form a
// multi-model account reports instead of a bare "week".
func poolByLabel(pools []usagePool, label string) (usagePool, bool) {
	for _, p := range pools {
		if strings.EqualFold(p.name, label) {
			return p, true
		}
	}
	for _, p := range pools {
		if strings.EqualFold(p.name, label+" (all models)") {
			return p, true
		}
	}
	return usagePool{}, false
}

// formatUsageReset renders a reset instant the way the banner names it —
// "Sep 2, 6pm", minutes only when they are not zero ("Aug 28, 10:20pm").
func formatUsageReset(t time.Time) string {
	if t.Minute() == 0 {
		return t.Format("Jan 2, 3pm")
	}
	return t.Format("Jan 2, 3:04pm")
}

// usageLine builds the one line both work's startup banner and `polako
// status` print from a snapshot: "plan: session 42%, week 52% (resets Sep
// 2, 6pm) — polako was 29% of the last 24h". Empty when there is nothing to
// say — no session and no week pool at all — which callers treat as
// absent rather than printing a row with nothing in it: a missing line
// must never be mistakable for zero usage, and this, paired with the
// caller's own nil check on the snapshot pointer, is the whole of how that
// holds.
func usageLine(snap usageSnapshot) string {
	session, hasSession := poolByLabel(snap.pools, "session")
	week, hasWeek := poolByLabel(snap.pools, "week")
	if !hasSession && !hasWeek {
		return ""
	}
	var parts []string
	if hasSession {
		parts = append(parts, fmt.Sprintf("session %d%%", session.percent))
	}
	if hasWeek {
		w := fmt.Sprintf("week %d%%", week.percent)
		if week.hasReset {
			w += fmt.Sprintf(" (resets %s)", formatUsageReset(week.reset))
		}
		parts = append(parts, w)
	}
	line := "plan: " + strings.Join(parts, ", ")
	if snap.hasAttribution && snap.attribution.hasPluginPercent {
		period := "the reported window"
		if snap.attribution.windowLabel != "" {
			period = "the last " + snap.attribution.windowLabel
		}
		line += fmt.Sprintf(" — %s was %d%% of %s", snap.attribution.plugin, snap.attribution.pluginPercent, period)
	}
	return line
}
