package gitopsdiff

import (
	"fmt"
	"strings"
	"testing"

	"github.com/suxess-it/kubrix-cli/internal/values/engine"
)

const runURL = "https://github.com/suxess-it/kubriX/actions/runs/1"

// fileDiff is a rendered file diff like diffFile produces, with the given added and removed lines.
func fileDiff(chart, testCase, path string, added, removed []string, status Status) FileDiff {
	lines := []string{
		fmt.Sprintf("diff -U 4 -rN target/%s/%s/%s pr/%[1]s/%[2]s/%[3]s", chart, testCase, path),
		fmt.Sprintf("--- target/%s/%s/%s", chart, testCase, path),
		fmt.Sprintf("+++ pr/%s/%s/%s", chart, testCase, path),
		fmt.Sprintf("@@ -1,%d +1,%d @@", len(removed), len(added)),
		" unchanged: line",
	}
	for _, l := range removed {
		lines = append(lines, "-"+l)
	}
	for _, l := range added {
		lines = append(lines, "+"+l)
	}
	return FileDiff{Chart: chart, TestCase: testCase, Path: path, Status: status, Additions: len(added), Deletions: len(removed), Text: strings.Join(lines, "\n") + "\n"}
}

func comments(r *Report) map[string]string {
	out := map[string]string{}
	for _, c := range Comments(r, runURL) {
		out[c.Name] = c.Body
	}
	return out
}

func names(cs []Comment) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Name)
	}
	return out
}

func TestTheSummaryComesFirstFollowedByOneCommentPerChart(t *testing.T) {
	r := &Report{
		Files: []FileDiff{
			fileDiff("loki", "default-aks-kind", "a.yaml", []string{"x"}, nil, Modified),
			fileDiff("loki", "default-kind", "a.yaml", []string{"x"}, nil, Modified),
			fileDiff("mimir", "default-kind", "b.yaml", nil, []string{"y"}, Modified),
		},
		DefaultValuesDiff: "-old: value\n+new: value\n",
	}
	cs := Comments(r, runURL)
	if got := names(cs); strings.Join(got, ",") != "summary,chart-loki,chart-mimir" {
		t.Fatalf("comments = %v", got)
	}
	summary := cs[0].Body
	for _, want := range []string{
		"| `loki` | default-aks-kind | 1 (0 added, 0 removed, 1 modified) | +1 / -0 |",
		"| `mimir` | default-kind | 1 (0 added, 0 removed, 1 modified) | +0 / -1 |",
		"The rendered diff of each chart is posted as a separate comment.",
		runURL,
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary lacks %q:\n%s", want, summary)
		}
	}
	if !strings.HasPrefix(summary, "<!-- gitops-diff:summary -->\n") || strings.Count(summary, "+new: value") != 1 {
		t.Errorf("summary must start with its marker and show the default values once:\n%s", summary)
	}
	loki := cs[1].Body
	if !strings.HasPrefix(loki, "<!-- gitops-diff:chart-loki -->\n") || !strings.Contains(loki, "<summary>default-aks-kind:") ||
		!strings.Contains(loki, "<summary>default-kind:") || strings.Contains(loki, "mimir") {
		t.Errorf("loki comment:\n%s", loki)
	}
}

func TestAChartCommentLeavesOutFilesThatDoNotFit(t *testing.T) {
	big := make([]string, 300)
	for i := range big {
		big[i] = strings.Repeat("x", 100)
	}
	var files []FileDiff
	for i := range 10 {
		files = append(files, fileDiff("loki", "default-kind", fmt.Sprintf("file-%d.yaml", i), big, nil, Modified))
	}
	c := comments(&Report{Files: files})["chart-loki"]
	if len(c) > MaxCommentLength {
		t.Fatalf("comment is %d characters, limit %d", len(c), MaxCommentLength)
	}
	for _, want := range []string{"file-0.yaml", "files are not shown", runURL, "default-kind: 0 added, 0 removed, 10 modified files"} {
		if !strings.Contains(c, want) {
			t.Errorf("comment lacks %q", want)
		}
	}
	if strings.Contains(c, "file-9.yaml") {
		t.Error("file-9.yaml does not fit and must be left out")
	}
}

func TestMoreThanMaxChartsOnlyGetASummary(t *testing.T) {
	var files []FileDiff
	for i := range MaxChartComments + 1 {
		files = append(files, fileDiff(fmt.Sprintf("chart%02d", i), "default-kind", "a.yaml", []string{"x"}, nil, Modified))
	}
	cs := Comments(&Report{Files: files}, runURL)
	if len(cs) != 1 || !strings.Contains(cs[0].Body, "not posted as separate comments") || !strings.Contains(cs[0].Body, "`chart10`") {
		t.Fatalf("comments = %v\n%s", names(cs), cs[0].Body)
	}
}

func TestNoChangesIsOneSummarySayingSo(t *testing.T) {
	cs := Comments(&Report{}, runURL)
	if len(cs) != 1 || !strings.Contains(cs[0].Body, "No changes in the rendered manifests or the default values.") || strings.Contains(cs[0].Body, runURL) {
		t.Fatalf("comments = %+v", cs)
	}
}

func TestOnlyChangedDefaultValuesAreShownInTheSummary(t *testing.T) {
	cs := Comments(&Report{DefaultValuesDiff: "+new: value\n"}, runURL)
	if len(cs) != 1 || !strings.Contains(cs[0].Body, "No changes in the rendered manifests.") || !strings.Contains(cs[0].Body, "+new: value") {
		t.Fatalf("comments = %+v", cs)
	}
}

func TestATooLargeDefaultValuesDiffIsLeftOut(t *testing.T) {
	r := &Report{
		Files:             []FileDiff{fileDiff("loki", "default-kind", "a.yaml", []string{"x"}, nil, Modified)},
		DefaultValuesDiff: strings.Repeat("+"+strings.Repeat("x", 100)+"\n", 1000),
	}
	s := comments(r)["summary"]
	if len(s) > MaxCommentLength || !strings.Contains(s, "too large for a comment") || strings.Contains(s, strings.Repeat("x", 100)) {
		t.Fatalf("summary of %d characters:\n%.500s", len(s), s)
	}
}

func TestChangedValuesAreListedWithWhereTheyAreSet(t *testing.T) {
	r := &Report{ValueChanges: []ValueChange{
		{Chart: "loki", TestCase: "kind", Path: "a.b", Kind: Modified, Before: 1, After: "x|y", Source: engine.SourceLocation{File: "v.yaml", Line: 3}},
		{Chart: "loki", TestCase: "kind", Path: "gone", Kind: Removed, Before: true},
		{Chart: "loki", TestCase: "kind", Path: "new", Kind: Added, After: []any{"a"}, Source: engine.SourceLocation{File: "w.yaml", Line: 1}},
	}}
	s := comments(r)["summary"]
	for _, want := range []string{
		"<summary>Changed values (3)</summary>",
		"| `loki` | kind | `a.b` | `1` | `x\\|y` | `v.yaml:3` |",
		"| `loki` | kind | `gone` | `true` | – | – |",
		"| `loki` | kind | `new` | – | `[\"a\"]` | `w.yaml:1` |",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("summary lacks %q:\n%s", want, s)
		}
	}
}

func TestTheCodeFenceIsLongerThanAnyBacktickRunInTheDiff(t *testing.T) {
	block := codeBlock("+```yaml\n+key: value\n+```\n")
	if !strings.HasPrefix(block, "````diff\n") || !strings.HasSuffix(block, "\n````\n") {
		t.Fatalf("block = %q", block)
	}
}

func TestMarkerNameFindsOnlyALeadingMarker(t *testing.T) {
	cases := map[string]string{Marker("chart-loki") + "body": "chart-loki", "text " + Marker("summary"): "", "no marker": ""}
	for body, want := range cases {
		if got, ok := MarkerName(body); got != want || ok != (want != "") {
			t.Errorf("MarkerName(%q) = %q, %v", body, got, ok)
		}
	}
}
