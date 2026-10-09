package cmd

import (
	"runtime/debug"
	"strings"
)

// BuildInfo says which build of the CLI this is; the release build fills it in through linker flags.
type BuildInfo struct {
	Version string
	Commit  string
	Date    string
}

// Unset values of a build that was not stamped.
const (
	devVersion = "dev"
	noCommit   = "none"
	noDate     = "unknown"
)

// WithRuntime fills what the linker flags left unset from the build info embedded by the Go toolchain, so a
// binary from `go install` or `go build` inside a checkout still knows its module version and commit.
func (b BuildInfo) WithRuntime(bi *debug.BuildInfo, ok bool) BuildInfo {
	if !ok || bi == nil {
		return b
	}
	if (b.Version == "" || b.Version == devVersion) && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		b.Version = strings.TrimPrefix(bi.Main.Version, "v")
	}
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			if b.Commit == "" || b.Commit == noCommit {
				b.Commit = s.Value
			}
		case "vcs.time":
			if b.Date == "" || b.Date == noDate {
				b.Date = s.Value
			}
		}
	}
	return b
}

// String is the version line: "0.1.0 (commit 1a2b3c4, built 2026-10-09T12:42:20Z)", leaving out what is unknown.
func (b BuildInfo) String() string {
	version := b.Version
	if version == "" {
		version = devVersion
	}
	var details []string
	if b.Commit != "" && b.Commit != noCommit {
		details = append(details, "commit "+shortCommit(b.Commit))
	}
	if b.Date != "" && b.Date != noDate {
		details = append(details, "built "+b.Date)
	}
	if len(details) == 0 {
		return version
	}
	return version + " (" + strings.Join(details, ", ") + ")"
}

func shortCommit(c string) string {
	if len(c) > 7 {
		return c[:7]
	}
	return c
}
