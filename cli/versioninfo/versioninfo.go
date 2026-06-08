// Package versioninfo exposes the trace CLI version metadata.
//
// The Version and Commit variables are set at build time via ldflags by
// goreleaser:
//
//	-X github.com/GrayCodeAI/trace/cli/versioninfo.Version={{.Version}}
//	-X github.com/GrayCodeAI/trace/cli/versioninfo.Commit={{.ShortCommit}}
//
// The VERSION file at the repo root is the canonical source of truth used by
// release tooling (release-please) and CI; goreleaser derives the version
// from the matching git tag at release time.
//
// The default "dev" value applies only to local builds without ldflags, so
// developers can immediately see when they're running an unreleased binary.
package versioninfo

import (
	"runtime/debug"
	"strings"
)

var (
	// Version is the semantic version. Set via ldflags at release time.
	Version = "dev"

	// Commit is the git commit short SHA. Set via ldflags at release time.
	Commit = "none"
)

// Load fills Version and Commit from the binary's build info when ldflags left
// them at their defaults. Call once from main() before either is read.
func Load() {
	info, ok := debug.ReadBuildInfo()
	Version, Commit = resolve(Version, Commit, info, ok)
}

// resolve fills Version/Commit from build info only when ldflags left them at
// their defaults; an explicit ldflags stamp always wins. A module install
// (@<version>) carries the version as info.Main.Version; a local build
// reports "(devel)" there but records the commit under vcs.revision. (Go
// already marks a dirty tree with a "+dirty" suffix on the version, so we
// don't track vcs.modified ourselves.)
func resolve(version, commit string, info *debug.BuildInfo, ok bool) (string, string) {
	if version != "dev" || !ok || info == nil {
		return version, commit
	}

	if v := info.Main.Version; v != "" && v != "(devel)" {
		version = strings.TrimPrefix(v, "v") // match GoReleaser's {{.Version}}
	}
	// Only fill an unset commit; an explicit ldflags stamp always wins.
	if commit == "none" || commit == "unknown" {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" && setting.Value != "" {
				commit = setting.Value
			}
		}
	}

	return version, commit
}
