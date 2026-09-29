package main

import "fmt"

// Version, Commit, and BuildDate identify a build. Release builds set them
// with -ldflags "-X main.Version=... -X main.Commit=... -X main.BuildDate=...";
// the values below are what a plain `go build` reports.
var (
	Version   = "dev"
	Commit    = "none"
	BuildDate = "unknown"
)

func versionString() string {
	return fmt.Sprintf("trace %s (commit %s, built %s)", Version, Commit, BuildDate)
}
