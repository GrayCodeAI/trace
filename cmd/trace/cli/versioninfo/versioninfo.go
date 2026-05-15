// Package versioninfo exposes the trace CLI version metadata.
//
// The Version and Commit variables are set at build time via ldflags by
// goreleaser:
//
//	-X github.com/GrayCodeAI/trace/cmd/trace/cli/versioninfo.Version={{.Version}}
//	-X github.com/GrayCodeAI/trace/cmd/trace/cli/versioninfo.Commit={{.ShortCommit}}
//
// The VERSION file at the repo root is the canonical source of truth used by
// release tooling (release-please) and CI; goreleaser derives the version
// from the matching git tag at release time.
//
// The default "dev" value applies only to local builds without ldflags, so
// developers can immediately see when they're running an unreleased binary.
package versioninfo

var (
	// Version is the semantic version. Set via ldflags at release time.
	Version = "dev"

	// Commit is the git commit short SHA. Set via ldflags at release time.
	Commit = "none"
)
