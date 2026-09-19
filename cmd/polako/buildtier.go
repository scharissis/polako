package main

// Which of the three ways a running binary knows its own version it was
// built: stamped at release time, installed as a Go module, or built from a
// local clone. polakoVersion (used throughout the package, and stamped on
// every run record) only ever needed the version string; `update` needs the
// tier too, to decide what updating the binary even means — a `go install`
// upgrades in place, a release binary is a download, a clone is the
// operator's own rebuild.

import (
	"runtime/debug"
	"sync"
)

// polakoVersion is the release tag when the binary was stamped at build time,
// the module version when installed with `go install`, and the short VCS
// revision when built from a clone. Empty if the binary carries none of them —
// a `go run` of the package, or a test.
var polakoVersion = sync.OnceValue(func() string {
	_, v := polakoBuildTier()
	return v
})

// buildTier is which of the three ways above answered.
type buildTier int

const (
	tierUnknown buildTier = iota
	tierStamped           // -ldflags "-X main.version=..." — a release binary
	tierModule            // `go install`
	tierVCS               // built from a local clone
)

// polakoBuildTier reads the process's own build info and classifies it.
// Thin wrapper over buildTierFrom so the classification itself — the part
// worth getting right and testing — takes a plain debug.BuildInfo rather
// than needing a real build to exercise.
func polakoBuildTier() (buildTier, string) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		info = &debug.BuildInfo{}
	}
	return buildTierFrom(version, *info, ok)
}

// buildTierFrom is polakoVersion's original fallback chain, reshaped to name
// which tier answered rather than only the string: the stamp first, because
// it is the only one a cross-compiled release binary has (`go build` from a
// checkout records the revision but leaves the module version at "(devel)",
// so without it every published binary would report a bare SHA and no run
// could be attributed to a release); then the module version; then the VCS
// revision, shortened and marked dirty the same way polakoVersion always did.
func buildTierFrom(stamped string, info debug.BuildInfo, hasInfo bool) (buildTier, string) {
	if stamped != "" {
		return tierStamped, stamped
	}
	if !hasInfo {
		return tierUnknown, ""
	}
	if v := info.Main.Version; v != "" && v != "(devel)" {
		return tierModule, v
	}
	rev, dirty := "", false
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	if rev == "" {
		return tierUnknown, ""
	}
	if dirty {
		rev += "+dirty"
	}
	return tierVCS, rev
}
