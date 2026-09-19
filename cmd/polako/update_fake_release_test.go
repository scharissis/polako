package main

// The fake `gh release download` half of ticket 4's test seam — split out of
// drain_test.go (already well past this file's own accretion ceiling) rather
// than grown further, since this block is self-contained: nothing else in
// the fake-gh plumbing reads or writes fakeRelease.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// fakeRelease is what `gh release download` serves — applyStampedBinary's
// whole interface to a real release. Assets is keyed by exact asset name:
// the fake never globs `--pattern`, since production only ever asks for the
// one literal name releaseAssetName produces for the host GOOS/GOARCH, plus
// the literal "checksums.txt".
type fakeRelease struct {
	Assets map[string][]byte `json:"assets,omitempty"`
	// NoChecksums makes the release serve every asset but checksums.txt
	// itself — applyStampedBinary's "no checksums.txt to verify against"
	// refusal.
	NoChecksums bool `json:"no_checksums,omitempty"`
	// BadChecksum names one asset to give a deliberately wrong sum for —
	// applyStampedBinary's checksum-mismatch refusal. Ignored when
	// NoChecksums is set; there is no checksums.txt to put a wrong sum in.
	BadChecksum string `json:"bad_checksum,omitempty"`
}

// releaseChecksumsFile renders r's checksums.txt — sha256sum's own format,
// sorted by name for a deterministic fixture.
func releaseChecksumsFile(r *fakeRelease) []byte {
	var b strings.Builder
	for _, name := range slices.Sorted(maps.Keys(r.Assets)) {
		sum := sha256.Sum256(r.Assets[name])
		hexSum := hex.EncodeToString(sum[:])
		if name == r.BadChecksum {
			hexSum = strings.Repeat("0", 64)
		}
		fmt.Fprintf(&b, "%s  %s\n", hexSum, name)
	}
	return []byte(b.String())
}

// answerReleaseDownload writes whichever of Assets/checksums.txt the
// invocation's --pattern flags and --dir ask for straight to disk. No
// ghState mutation: this is a filesystem side effect, not shared repository
// state, the same way a real `gh release download` never changes the
// release it reads from.
func answerReleaseDownload(st *ghState, args []string) (out string, changed bool, code int) {
	if st.Release == nil {
		fmt.Fprintln(os.Stderr, "release not found")
		return "", false, 1
	}
	var patterns []string
	dir := "."
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--pattern":
			if i+1 < len(args) {
				patterns = append(patterns, args[i+1])
				i++
			}
		case "--dir":
			if i+1 < len(args) {
				dir = args[i+1]
				i++
			}
		}
	}
	var wrote int
	for _, p := range patterns {
		var content []byte
		switch {
		case p == "checksums.txt":
			if st.Release.NoChecksums {
				continue
			}
			content = releaseChecksumsFile(st.Release)
		default:
			c, ok := st.Release.Assets[p]
			if !ok {
				continue
			}
			content = c
		}
		if err := os.WriteFile(filepath.Join(dir, p), content, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "fake gh: %v\n", err)
			return "", false, 1
		}
		wrote++
	}
	if wrote == 0 {
		fmt.Fprintln(os.Stderr, "no assets match the given patterns")
		return "", false, 1
	}
	return "", false, 0
}
