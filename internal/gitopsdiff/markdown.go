package gitopsdiff

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/suxess-it/kubrix-cli/internal/values/output"
)

const (
	// MaxCommentLength keeps some headroom below GitHub's 65536 characters per comment.
	MaxCommentLength = 60000
	// MaxChartComments is how many charts get a comment of their own; with more, only the summary is posted.
	MaxChartComments = 10
	// truncationNoteReserve is kept free for the note that lists how many files were left out of a comment.
	truncationNoteReserve = 500
	// maxValueChanges is how many changed values the summary lists before pointing at the full report.
	maxValueChanges = 50
)

// Comment is one PR comment: Name identifies it across runs through its marker, Body is its Markdown.
type Comment struct {
	Name string
	Body string
}

// Marker starts every comment body, so a later run finds and updates or deletes its own comments.
func Marker(name string) string { return "<!-- gitops-diff:" + name + " -->\n" }

var markerPattern = regexp.MustCompile(`^<!-- gitops-diff:(\S+) -->`)

// MarkerName returns the comment name of a body that starts with a marker.
func MarkerName(body string) (string, bool) {
	m := markerPattern.FindStringSubmatch(body)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// Comments renders the report as PR comments: the summary first, then one comment per changed chart unless more
// than MaxChartComments charts changed. runURL links the full diffs.
func Comments(r *Report, runURL string) []Comment {
	charts := r.Charts()
	comments := []Comment{{Name: "summary", Body: renderSummary(r, charts, runURL)}}
	if len(charts) <= MaxChartComments {
		for _, chart := range charts {
			comments = append(comments, Comment{Name: "chart-" + chart, Body: renderChartComment(chart, r.chartFiles(chart), runURL)})
		}
	}
	return comments
}

func describe(files []FileDiff) string {
	count := map[Status]int{}
	for _, f := range files {
		count[f.Status]++
	}
	return fmt.Sprintf("%d added, %d removed, %d modified", count[Added], count[Removed], count[Modified])
}

func describeLines(files []FileDiff) string {
	additions, deletions := 0, 0
	for _, f := range files {
		additions += f.Additions
		deletions += f.Deletions
	}
	return fmt.Sprintf("+%d / -%d", additions, deletions)
}

var backticks = regexp.MustCompile("`+")

// codeFence returns a fence longer than any backtick run in the text.
func codeFence(text string) string {
	longest := 0
	for _, run := range backticks.FindAllString(text, -1) {
		longest = max(longest, len(run))
	}
	return strings.Repeat("`", max(3, longest+1))
}

func codeBlock(text string) string {
	fence := codeFence(text)
	return fence + "diff\n" + strings.TrimRight(text, "\n") + "\n" + fence + "\n"
}

func details(summary, body string) string {
	return "<details><summary>" + summary + "</summary>\n\n" + body + "\n</details>\n\n"
}

func fullDiffLink(runURL string) string {
	if runURL == "" {
		return "The full diffs are in the `kubrix diff` output.\n"
	}
	return "The full diffs are attached as artifacts to the [workflow run](" + runURL + ").\n"
}

// groups splits files sorted by key into runs of equal keys.
func groups(files []FileDiff, key func(FileDiff) string) [][]FileDiff {
	var out [][]FileDiff
	for i, f := range files {
		if i == 0 || key(files[i-1]) != key(f) {
			out = append(out, nil)
		}
		out[len(out)-1] = append(out[len(out)-1], f)
	}
	return out
}

func byChartAndTestCase(f FileDiff) string { return f.Chart + "\x00" + f.TestCase }

func renderSummaryTable(files []FileDiff) string {
	if len(files) == 0 {
		return "No changes in the rendered manifests.\n\n"
	}
	lines := []string{"| Chart | Test case | Files | Lines |", "|-------|-----------|-------|-------|"}
	for _, group := range groups(files, byChartAndTestCase) {
		lines = append(lines, fmt.Sprintf("| `%s` | %s | %d (%s) | %s |", group[0].Chart, group[0].TestCase, len(group), describe(group), describeLines(group)))
	}
	return strings.Join(lines, "\n") + "\n\n"
}

// FailureLabel describes a failure's status for people.
func FailureLabel(s FailureStatus) string {
	switch s {
	case Broken:
		return "broken by this change"
	case Fixed:
		return "fixed by this change"
	default:
		return "already broken on the base"
	}
}

// FailureSubject names what failed: a chart with a test case, or a whole test case.
func FailureSubject(f Failure) string {
	if f.Chart == "" {
		return "test case " + f.TestCase
	}
	return f.Chart + " · " + f.TestCase
}

// renderFailures lists what could not be rendered, the failures the change introduces first.
func renderFailures(failures []Failure) string {
	if len(failures) == 0 {
		return ""
	}
	icons := map[FailureStatus]string{Broken: "❌", StillBroken: "⚠️", Fixed: "✅"}
	var b strings.Builder
	b.WriteString("#### Rendering failures\n\n")
	for _, status := range []FailureStatus{Broken, StillBroken, Fixed} {
		for _, f := range failures {
			if f.Status != status {
				continue
			}
			fmt.Fprintf(&b, "- %s **%s**: `%s`\n", icons[status], FailureLabel(status), FailureSubject(f))
			text := f.Error
			if len(text) > 2000 {
				text = text[:2000] + "…"
			}
			fence := codeFence(text)
			fmt.Fprintf(&b, "  <details><summary>error</summary>\n\n  %s\n  %s\n  %s\n\n  </details>\n", fence, strings.ReplaceAll(text, "\n", "\n  "), fence)
		}
	}
	return b.String() + "\n"
}

// renderValueChanges lists the changed effective values with the file and line that now set them (new in kubrix diff).
func renderValueChanges(changes []ValueChange) string {
	if len(changes) == 0 {
		return ""
	}
	lines := []string{"| Chart | Test case | Path | Before | After | Set by |", "|-------|-----------|------|--------|-------|--------|"}
	for i, c := range changes {
		if i == maxValueChanges {
			lines = append(lines, fmt.Sprintf("\n%d more changed values are in the `kubrix diff` output.", len(changes)-maxValueChanges))
			break
		}
		lines = append(lines, fmt.Sprintf("| `%s` | %s | `%s` | %s | %s | %s |", c.Chart, c.TestCase, c.Path, cell(c.Before, c.Kind == Added), cell(c.After, c.Kind == Removed), cellSource(c)))
	}
	return details(fmt.Sprintf("Changed values (%d)", len(changes)), strings.Join(lines, "\n")+"\n")
}

func cell(v any, absent bool) string {
	if absent {
		return "–"
	}
	return "`" + strings.ReplaceAll(output.Scalar(v), "|", `\|`) + "`"
}

func cellSource(c ValueChange) string {
	if c.Kind == Removed {
		return "–"
	}
	return fmt.Sprintf("`%s:%d`", c.Source.File, c.Source.Line)
}

func renderSummary(r *Report, charts []string, runURL string) string {
	var parts []string
	parts = append(parts, Marker("summary"), "### GitOps diff\n\n")
	if r.Empty() {
		parts = append(parts, "No changes in the rendered manifests or the default values.\n")
		return strings.Join(parts, "")
	}
	parts = append(parts, renderFailures(r.Failures))
	parts = append(parts, renderSummaryTable(r.Files))
	if len(charts) > MaxChartComments {
		parts = append(parts, fmt.Sprintf("%d charts changed, so their rendered diffs are not posted as separate comments (limit: %d).\n\n", len(charts), MaxChartComments))
	} else if len(charts) > 0 {
		parts = append(parts, "The rendered diff of each chart is posted as a separate comment.\n\n")
	}
	footer := fullDiffLink(runURL)
	if section := renderValueChanges(r.ValueChanges); section != "" {
		if len(strings.Join(parts, ""))+len(section)+len(footer) > MaxCommentLength {
			section = "The changed values are too many for a comment.\n\n"
		}
		parts = append(parts, section)
	}
	if r.DefaultValuesDiff != "" {
		// Subchart default values don't depend on the test case, so show them once.
		section := details("Changes Default Values", codeBlock(r.DefaultValuesDiff))
		if len(strings.Join(parts, ""))+len(section)+len(footer) > MaxCommentLength {
			section = "The default values diff is too large for a comment.\n\n"
		}
		parts = append(parts, section)
	}
	parts = append(parts, footer)
	return strings.Join(parts, "")
}

func renderChartComment(chart string, files []FileDiff, runURL string) string {
	parts := []string{Marker("chart-" + chart), fmt.Sprintf("### GitOps diff: `%s`\n\n", chart)}
	length := len(strings.Join(parts, ""))
	omitted := 0
	for _, group := range groups(files, func(f FileDiff) string { return f.TestCase }) {
		var all strings.Builder
		for _, f := range group {
			all.WriteString(f.Text)
		}
		fence := codeFence(all.String())
		opening := fmt.Sprintf("<details><summary>%s: %s files, %s lines</summary>\n\n%sdiff\n", group[0].TestCase, describe(group), describeLines(group), fence)
		closing := fence + "\n</details>\n\n"
		var shown []string
		shownLength := len(opening) + len(closing)
		for _, f := range group {
			if length+shownLength+len(f.Text)+truncationNoteReserve > MaxCommentLength {
				omitted++
				continue
			}
			shown = append(shown, f.Text)
			shownLength += len(f.Text)
		}
		if len(shown) > 0 {
			parts = append(parts, opening)
			parts = append(parts, shown...)
			parts = append(parts, closing)
			length += shownLength
		}
	}
	if omitted > 0 {
		parts = append(parts, fmt.Sprintf("%d changed files are not shown because the diff is too large for a comment. %s", omitted, fullDiffLink(runURL)))
	}
	return strings.Join(parts, "")
}

// Charts are the charts with rendered changes, sorted.
func (r *Report) Charts() []string {
	var charts []string
	for _, f := range r.Files {
		if !slices.Contains(charts, f.Chart) {
			charts = append(charts, f.Chart)
		}
	}
	slices.Sort(charts)
	return charts
}

func (r *Report) chartFiles(chart string) []FileDiff {
	var out []FileDiff
	for _, f := range r.Files {
		if f.Chart == chart {
			out = append(out, f)
		}
	}
	return out
}
