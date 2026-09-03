package main

// The string and number helpers the whole cmd/polako package shares: money,
// durations, magnitudes, percentages, pluralisation, snake_case-to-prose, the
// span reducers, orList for a flag's choice list, and the mean/median
// generics. They collected in stats.go because the text report was their
// first caller, but labelpass.go, drain.go and metrics.go reach for them too
// — this is their honest home, with nothing stats-specific in it.
//
// Split out of stats.go (issue #149's accretion debt) as verbatim movement.

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// orList renders a choice the way the flag's help and its error both want it:
// "issue, model, tag, shift or reason", so a message that lists five reads as
// English rather than as a dump of the slice behind it.
func orList(items []string) string {
	if len(items) < 2 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " or " + items[len(items)-1]
}

// label renders a record's snake_case value as prose.
func label(s string) string { return strings.ReplaceAll(s, "_", " ") }

func percent(n, total int) string {
	if total == 0 {
		return "0%"
	}
	return fmt.Sprintf("%.0f%%", 100*float64(n)/float64(total))
}

// pct1 renders a percentage to one decimal place, trailing zero trimmed —
// trimZero's own rule, reused here rather than duplicated.
func pct1(f float64) string { return trimZero(f) + "%" }

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

// split renders a token block's four ways, divided by n — the same helper for
// a total (n = 1) and a per-issue mean. divideTokens is the division itself,
// its own function so statsDocIssuesFrom (statsjson.go) can read the same
// per-issue split as numbers rather than reducing tokensSplitSum a second
// time.
func split(t tokenCounts, n int64) string {
	d := divideTokens(t, n)
	return fmt.Sprintf("in %s, out %s, cache read %s, cache write %s",
		count(d.In), count(d.Out), count(d.CacheRead), count(d.CacheWrite))
}

func divideTokens(t tokenCounts, n int64) tokenCounts {
	if n < 1 {
		n = 1
	}
	return tokenCounts{In: t.In / n, Out: t.Out / n, CacheRead: t.CacheRead / n, CacheWrite: t.CacheWrite / n}
}

// count renders a magnitude at a glance: 8.1M reads, 8123400 does not.
func count(n int64) string {
	abs := n
	if abs < 0 {
		abs = -abs
	}
	switch {
	case abs >= 1_000_000_000:
		return trimZero(float64(n)/1e9) + "G"
	case abs >= 1_000_000:
		return trimZero(float64(n)/1e6) + "M"
	case abs >= 1_000:
		return trimZero(float64(n)/1e3) + "k"
	}
	return strconv.FormatInt(n, 10)
}

func trimZero(f float64) string {
	return strings.TrimSuffix(fmt.Sprintf("%.1f", f), ".0")
}

func usd(f float64) string { return fmt.Sprintf("$%.2f", f) }

// dur renders a duration the way a person reads one: no "0s" tail, and days
// once hours stop being a useful unit.
func dur(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	if d >= 48*time.Hour {
		return trimZero(d.Hours()/24) + "d"
	}
	d = d.Round(time.Second)
	h := int(d / time.Hour)
	m := int(d/time.Minute) % 60
	s := int(d/time.Second) % 60
	switch {
	case h > 0 && m > 0:
		return fmt.Sprintf("%dh%dm", h, m)
	case h > 0:
		return fmt.Sprintf("%dh", h)
	case m > 0 && s > 0:
		return fmt.Sprintf("%dm%ds", m, s)
	case m > 0:
		return fmt.Sprintf("%dm", m)
	}
	return fmt.Sprintf("%ds", s)
}

func spanSummary(spans []time.Duration) string {
	s := summarizeSpans(spans)
	if s.count == 0 {
		return "no spans in this window"
	}
	return fmt.Sprintf("%s — %s median, %s max", plural(s.count, "span"), dur(s.median), dur(s.max))
}

// spanStats is a span list's count, median and max, computed once so
// spanSummary (text) and statsDocSpansFrom (statsjson.go) format the same
// numbers rather than each reducing the slice itself.
type spanStats struct {
	count  int
	median time.Duration
	max    time.Duration
}

func summarizeSpans(spans []time.Duration) spanStats {
	if len(spans) == 0 {
		return spanStats{}
	}
	return spanStats{count: len(spans), median: median(spans), max: slices.Max(spans)}
}

// number is what the mean/median helpers work on: counts, dollars and
// durations (whose underlying type is int64) all qualify.
type number interface {
	~int | ~int64 | ~float64
}

func mean[T number](v []T) float64 {
	if len(v) == 0 {
		return 0
	}
	var sum float64
	for _, x := range v {
		sum += float64(x)
	}
	return sum / float64(len(v))
}

func median[T number](v []T) T {
	if len(v) == 0 {
		var zero T
		return zero
	}
	s := slices.Clone(v)
	slices.Sort(s)
	if n := len(s); n%2 == 1 {
		return s[n/2]
	}
	return (s[len(s)/2-1] + s[len(s)/2]) / 2
}
