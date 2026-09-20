package main

import (
	"runtime/debug"
	"testing"
)

// buildTierFrom is polakoVersion's fallback chain, reshaped so `polako
// update` can tell which tier answered — tested directly against a literal
// debug.BuildInfo rather than needing a real stamped/module/VCS build to
// exercise each branch.
func TestBuildTierFrom(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		stamped  string
		info     debug.BuildInfo
		hasInfo  bool
		wantTier buildTier
		want     string
	}{{
		name:     "stamped release binary",
		stamped:  "0.23.0",
		hasInfo:  true,
		info:     debug.BuildInfo{Main: debug.Module{Version: "(devel)"}},
		wantTier: tierStamped,
		want:     "0.23.0",
	}, {
		name:     "no build info at all",
		hasInfo:  false,
		wantTier: tierUnknown,
	}, {
		name:     "go install module version",
		hasInfo:  true,
		info:     debug.BuildInfo{Main: debug.Module{Version: "v0.23.0"}},
		wantTier: tierModule,
		want:     "v0.23.0",
	}, {
		name:    "a checkout build reports (devel), so the VCS revision is next",
		hasInfo: true,
		info: debug.BuildInfo{
			Main: debug.Module{Version: "(devel)"},
			Settings: []debug.BuildSetting{
				{Key: "vcs.revision", Value: "a1b2c3d4e5f60708090a0b0c0d0e0f"},
			},
		},
		wantTier: tierVCS,
		want:     "a1b2c3d4e5f6", // cut to 12 characters
	}, {
		name:    "a dirty checkout marks the revision",
		hasInfo: true,
		info: debug.BuildInfo{
			Main: debug.Module{Version: "(devel)"},
			Settings: []debug.BuildSetting{
				{Key: "vcs.revision", Value: "a1b2c3d4e5f6"},
				{Key: "vcs.modified", Value: "true"},
			},
		},
		wantTier: tierVCS,
		want:     "a1b2c3d4e5f6+dirty",
	}, {
		name:     "devel with no VCS info — a `go run`, or a stripped build",
		hasInfo:  true,
		info:     debug.BuildInfo{Main: debug.Module{Version: "(devel)"}},
		wantTier: tierUnknown,
	}} {
		t.Run(tc.name, func(t *testing.T) {
			gotTier, got := buildTierFrom(tc.stamped, tc.info, tc.hasInfo)
			if gotTier != tc.wantTier || got != tc.want {
				t.Errorf("buildTierFrom(%q, ..., %v) = (%v, %q), want (%v, %q)",
					tc.stamped, tc.hasInfo, gotTier, got, tc.wantTier, tc.want)
			}
		})
	}
}
