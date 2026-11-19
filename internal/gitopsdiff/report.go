// Package gitopsdiff renders the charts a change touches on two revisions of a platform repository and reports what
// changed: the rendered manifests per chart and test case, the subcharts' default values, and the effective values
// with the file and line that set them. It replaces kubriX's create-pr-comment-file.sh and gitops-diff-comments.py.
package gitopsdiff

import (
	"fmt"
	"strings"

	"github.com/aymanbagabas/go-udiff"

	"github.com/suxess-it/kubrix-cli/internal/values/engine"
)

// Revision names used in diff headers, as in the CI script: the base branch is "target", the change is "pr".
const (
	baseName = "target"
	headName = "pr"
	// contextLines matches diff -U 4.
	contextLines = 4
)

// Status of a file or value between the two revisions.
type Status string

const (
	Added    Status = "added"
	Removed  Status = "removed"
	Modified Status = "modified"
)

// FileDiff is the change of one rendered file of a chart in one test case.
type FileDiff struct {
	Chart     string `json:"chart"`
	TestCase  string `json:"testCase"`
	Path      string `json:"path"`
	Status    Status `json:"status"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	// Text is the unified diff, starting with a "diff -U 4 -rN" header line like diff -r prints.
	Text string `json:"diff"`
}

// ValueChange is an effective value of an application that differs between the revisions.
type ValueChange struct {
	Chart    string `json:"chart"`
	TestCase string `json:"testCase"`
	Path     string `json:"path"`
	Kind     Status `json:"kind"`
	Before   any    `json:"before,omitempty"`
	After    any    `json:"after,omitempty"`
	// Source is where the head revision sets the value, or where the base revision set a removed one.
	Source engine.SourceLocation `json:"source"`
}

// Failure is a chart that could not be rendered with a test case on one or both revisions, or, with an empty
// Chart, a test case whose applications could not be resolved. Its rendered files are not compared.
type Failure struct {
	Chart    string        `json:"chart,omitempty"`
	TestCase string        `json:"testCase"`
	Status   FailureStatus `json:"status"`
	// Error is the head revision's error, or the base revision's for a failure the change fixes.
	Error string `json:"error"`
}

// FailureStatus says which revision a failure is on.
type FailureStatus string

const (
	// Broken fails on the change only: the change breaks rendering.
	Broken FailureStatus = "broken"
	// StillBroken fails on both revisions: the change neither caused nor fixed it.
	StillBroken FailureStatus = "still-broken"
	// Fixed fails on the base revision only: the change fixes it.
	Fixed FailureStatus = "fixed"
)

// Report is everything that changed, sorted by chart, test case and path.
type Report struct {
	Files             []FileDiff    `json:"files"`
	DefaultValuesDiff string        `json:"defaultValuesDiff"`
	ValueChanges      []ValueChange `json:"valueChanges"`
	Failures          []Failure     `json:"failures"`
}

// Empty reports whether nothing changed.
func (r *Report) Empty() bool {
	return len(r.Files) == 0 && r.DefaultValuesDiff == "" && len(r.ValueChanges) == 0 && len(r.Failures) == 0
}

// Broken are the failures the change introduces.
func (r *Report) Broken() []Failure {
	var out []Failure
	for _, f := range r.Failures {
		if f.Status == Broken {
			out = append(out, f)
		}
	}
	return out
}

// diffFile returns the diff of one file, or false when it did not change. A file that exists on one side only is
// compared with an empty one, like diff -N.
func diffFile(chart, testCase, path string, old, new string, oldExists, newExists bool) (FileDiff, bool) {
	if old == new && oldExists == newExists {
		return FileDiff{}, false
	}
	oldLabel := strings.Join([]string{baseName, chart, testCase, path}, "/")
	newLabel := strings.Join([]string{headName, chart, testCase, path}, "/")
	text, additions, deletions := unified(oldLabel, newLabel, old, new)
	status := Modified
	switch {
	case !oldExists:
		status = Added
	case !newExists:
		status = Removed
	}
	return FileDiff{Chart: chart, TestCase: testCase, Path: path, Status: status, Additions: additions, Deletions: deletions,
		Text: fmt.Sprintf("diff -U %d -rN %s %s\n%s", contextLines, oldLabel, newLabel, text)}, true
}

// unified diffs two texts with diff -U 4 context and counts the added and removed lines.
func unified(oldLabel, newLabel, old, new string) (string, int, int) {
	text, err := udiff.ToUnified(oldLabel, newLabel, old, udiff.Strings(old, new), contextLines)
	if err != nil {
		// ToUnified only fails on edits that do not fit the text, which udiff.Strings never produces.
		panic(err)
	}
	additions, deletions := 0, 0
	for i, line := range strings.Split(text, "\n") {
		if i < 2 {
			continue // the --- and +++ header lines
		}
		switch {
		case strings.HasPrefix(line, "+"):
			additions++
		case strings.HasPrefix(line, "-"):
			deletions++
		}
	}
	return text, additions, deletions
}
