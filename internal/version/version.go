// Package version handles kubriX release tags (vX.Y.Z) and CHANGELOG.md sections.
package version

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/mod/semver"
)

var releaseTag = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)

// IsRelease reports whether ref is a kubriX release tag; anything else is treated as a branch.
func IsRelease(ref string) bool { return releaseTag.MatchString(ref) }

// MinSupported is the oldest release the CLI bootstraps or upgrades to: its rendering mirrors
// install-platform.sh as of v7.0.0 (v6 also rendered docs/*.md.tmpl, v5 only selected values files).
const MinSupported = "v7.0.0"

// Supported reports whether the CLI can bootstrap or upgrade to release r.
func Supported(r string) bool { return IsRelease(r) && semver.Compare(r, MinSupported) >= 0 }

// Releases filters release tags and sorts them newest first.
func Releases(tags []string) []string {
	var out []string
	for _, t := range tags {
		if IsRelease(t) && !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	slices.SortFunc(out, func(a, b string) int { return semver.Compare(b, a) })
	return out
}

// Normalize turns "7.0.0" (release-please manifest) into "v7.0.0".
func Normalize(v string) string {
	if v != "" && !strings.HasPrefix(v, "v") {
		return "v" + v
	}
	return v
}

// UpgradeTargets returns releases newer than installed, limited to the next major version, newest first.
func UpgradeTargets(installed string, releases []string) []string {
	installed = Normalize(installed)
	major, err := strconv.Atoi(strings.TrimPrefix(semver.Major(installed), "v"))
	if err != nil {
		return nil
	}
	nextMajor := fmt.Sprintf("v%d", major+1)
	var out []string
	for _, r := range Releases(releases) {
		if Supported(r) && semver.Compare(r, installed) > 0 && semver.Compare(semver.Major(r), nextMajor) <= 0 {
			out = append(out, r)
		}
	}
	return out
}

var changelogHeading = regexp.MustCompile(`^## \[?(\d+\.\d+\.\d+)\]?`)

// ChangelogBetween returns the CHANGELOG.md sections for versions in (from, to], newest first.
// to may be empty to include everything newer than from (e.g. for an upgrade to main).
func ChangelogBetween(changelog, from, to string) string {
	from, to = Normalize(from), Normalize(to)
	var out []string
	keep := false
	for _, line := range strings.Split(changelog, "\n") {
		if m := changelogHeading.FindStringSubmatch(line); m != nil {
			v := "v" + m[1]
			keep = semver.Compare(v, from) > 0 && (to == "" || semver.Compare(v, to) <= 0)
		}
		if keep {
			out = append(out, line)
		}
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// HasBreakingChanges reports whether a changelog excerpt contains a breaking-changes section.
func HasBreakingChanges(notes string) bool {
	return strings.Contains(notes, "BREAKING CHANGES")
}

// BreakingChanges extracts the "⚠ BREAKING CHANGES" subsections of a changelog excerpt, with their release headings.
func BreakingChanges(notes string) string {
	var out []string
	release, inBreaking := "", false
	for _, line := range strings.Split(notes, "\n") {
		switch {
		case strings.HasPrefix(line, "## "):
			release, inBreaking = line, false
		case strings.HasPrefix(line, "### "):
			inBreaking = strings.Contains(line, "BREAKING CHANGES")
			if inBreaking {
				out = append(out, release, line)
			}
			continue
		}
		if inBreaking {
			out = append(out, line)
		}
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}
