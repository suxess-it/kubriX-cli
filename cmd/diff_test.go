package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/suxess-it/kubrix-cli/internal/git"
	"github.com/suxess-it/kubrix-cli/internal/gitopsdiff"
	"github.com/suxess-it/kubrix-cli/internal/testrepo"
	"github.com/suxess-it/kubrix-cli/internal/ui"
	"github.com/suxess-it/kubrix-cli/internal/values/engine"
)

// diffRepo is a committed platform repository whose grafana replicas the working tree changes from 2 to 3.
func diffRepo(t *testing.T) string {
	t.Helper()
	dir := platformRepo(t, map[string]string{
		"platform-apps/charts/grafana/templates/cm.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: grafana\ndata:\n  replicas: {{ .Values.grafana.replicas | quote }}\n",
	})
	g := testrepo.Git(t, dir)
	testrepo.Run(t, g, "init", "-q", "-b", "main")
	testrepo.Run(t, g, "add", "-A")
	testrepo.Run(t, g, "commit", "-q", "-m", "base")
	testrepo.Write(t, dir, map[string]string{"platform-apps/charts/grafana/values-size-small.yaml": "grafana:\n  replicas: 3\n"})
	return dir
}

type fakeCommentClient struct {
	existing []git.IssueComment
	created  []string
}

func (f *fakeCommentClient) IssueComments(context.Context, string, string, int) ([]git.IssueComment, error) {
	return f.existing, nil
}
func (f *fakeCommentClient) CreateIssueComment(_ context.Context, owner, repo string, number int, body string) error {
	name, _ := gitopsdiff.MarkerName(body)
	f.created = append(f.created, fmt.Sprintf("%s/%s#%d %s", owner, repo, number, name))
	return nil
}
func (f *fakeCommentClient) EditIssueComment(context.Context, string, string, int64, string) error {
	return errors.New("unexpected edit")
}
func (f *fakeCommentClient) DeleteIssueComment(context.Context, string, string, int64) error {
	return errors.New("unexpected delete")
}

func fakeDiffServices(client *fakeCommentClient) diffServices {
	return diffServices{
		comments: func(context.Context) (gitopsdiff.CommentClient, string, error) { return client, "bot", nil },
		origin:   func(string) (string, error) { return "suxess-it/kubriX", nil },
	}
}

func diff(t *testing.T, f diffFlags, svc diffServices) (string, string) {
	t.Helper()
	if f.charts == "" {
		f.charts = "changed"
	}
	if f.format == "" {
		f.format = "text"
	}
	var stdout, stderr bytes.Buffer
	if err := runDiff(context.Background(), f, svc, &stdout, &stderr); err != nil {
		t.Fatalf("diff: %v\n%s", err, stderr.String())
	}
	return stdout.String(), stderr.String()
}

func TestDiffPrintsTheChangedManifestsAndValuesOfTheWorkingTree(t *testing.T) {
	dir := diffRepo(t)
	out, _ := diff(t, diffFlags{repo: dir, base: "HEAD"}, fakeDiffServices(&fakeCommentClient{}))
	for _, want := range []string{
		"grafana  small      1 (0 added, 0 removed, 1 modified)  +1 / -1",
		"grafana  small      grafana.replicas  2       3      platform-apps/charts/grafana/values-size-small.yaml:2",
		`+  replicas: "3"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestDiffWritesTheCommentsAndTheFullDiffAndJSON(t *testing.T) {
	dir := diffRepo(t)
	outDir := filepath.Join(t.TempDir(), "comments")
	out, _ := diff(t, diffFlags{repo: dir, base: "HEAD", format: "json", outputDir: outDir, runURL: "https://ci/run/1"}, fakeDiffServices(&fakeCommentClient{}))
	var report gitopsdiff.Report
	if err := json.Unmarshal([]byte(out), &report); err != nil || len(report.Files) != 1 || len(report.ValueChanges) != 1 {
		t.Fatalf("json report = %+v, %v\n%s", report, err, out)
	}
	entries, err := os.ReadDir(outDir)
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, e := range entries {
		files = append(files, e.Name())
	}
	if !reflect.DeepEqual(files, []string{"chart-grafana.md", "full.diff", "summary.md"}) {
		t.Fatalf("output dir = %v", files)
	}
	summary := testrepo.Read(t, outDir, "summary.md")
	if !strings.HasPrefix(summary, gitopsdiff.Marker("summary")) || !strings.Contains(summary, "https://ci/run/1") {
		t.Fatalf("summary.md:\n%s", summary)
	}
	if full := testrepo.Read(t, outDir, "full.diff"); !strings.HasPrefix(full, "diff -U 4 -rN target/grafana/small/") {
		t.Fatalf("full.diff:\n%s", full)
	}
}

func TestDiffPostsCommentsOnlyWithPR(t *testing.T) {
	dir := diffRepo(t)
	client := &fakeCommentClient{}
	out, _ := diff(t, diffFlags{repo: dir, base: "HEAD", format: "markdown", outputDir: t.TempDir()}, fakeDiffServices(client))
	if len(client.created) != 0 || out != "" {
		t.Fatalf("without --pr: comments %v, stdout %q (comments go to --output-dir)", client.created, out)
	}
	_, log := diff(t, diffFlags{repo: dir, base: "HEAD", format: "markdown", pr: 7}, fakeDiffServices(client))
	if want := []string{"suxess-it/kubriX#7 summary", "suxess-it/kubriX#7 chart-grafana"}; !reflect.DeepEqual(client.created, want) {
		t.Fatalf("created = %v, want %v", client.created, want)
	}
	if log != "create comment summary\ncreate comment chart-grafana\n" {
		t.Fatalf("stderr = %q", log)
	}
}

func TestDiffRejectsBadInput(t *testing.T) {
	dir := diffRepo(t)
	svc := fakeDiffServices(&fakeCommentClient{})
	cases := []struct {
		name string
		f    diffFlags
		want string
	}{
		{"an invalid test case", diffFlags{repo: dir, base: "HEAD", testCases: []string{"nofiles"}}, "invalid --test-case"},
		{"an unknown base", diffFlags{repo: dir, base: "nope"}, "check out nope"},
		{"an invalid GitHub repository", diffFlags{repo: dir, base: "HEAD", pr: 1, githubRepo: "nope"}, `invalid --github-repo "nope"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.f.charts, c.f.format = "changed", "text"
			err := runDiff(context.Background(), c.f, svc, &bytes.Buffer{}, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error = %v, want %q", err, c.want)
			}
		})
	}
	if _, _, err := kubrix(t, "diff", "--repo", dir); err == nil || !strings.Contains(err.Error(), "--base is required when the output is not a terminal") {
		t.Fatalf("without --base and a terminal: %v", err)
	}
}

func TestParseTestCaseReadsFilesAndHelmSetFlags(t *testing.T) {
	tc, err := parseTestCase("default-kind=values-kubrix-default.yaml,values-cluster-kind.yaml;--set a=b --set-string c=1 --set=d=2")
	if err != nil {
		t.Fatal(err)
	}
	want := gitopsdiff.TestCase{Name: "default-kind", Files: []string{"values-kubrix-default.yaml", "values-cluster-kind.yaml"}, Sets: []engine.SetSource{
		{Expression: "a=b", Source: engine.SourceLocation{File: "--test-case default-kind", Line: 1, Column: 1}},
		{Expression: "c=1", StringValues: true, Source: engine.SourceLocation{File: "--test-case default-kind", Line: 2, Column: 1}},
		{Expression: "d=2", Source: engine.SourceLocation{File: "--test-case default-kind", Line: 3, Column: 1}},
	}}
	if !reflect.DeepEqual(tc, want) {
		t.Fatalf("test case = %+v\nwant %+v", tc, want)
	}
	for _, bad := range []string{"=a.yaml", "x=", "x=a.yaml;--values y.yaml", "x=a.yaml;--set"} {
		if _, err := parseTestCase(bad); err == nil {
			t.Errorf("parseTestCase(%q) accepted", bad)
		}
	}
}

// The real kubriX repository renders: a chart with remote dependencies builds and renders the same on both sides.
// This needs network access to the chart repositories, as CI has.
func TestDiffRendersAChartOfTheKubrixRepository(t *testing.T) {
	repo := testrepo.KubrixRepo(t)
	report, err := gitopsdiff.Run(context.Background(), gitopsdiff.Options{Repo: repo, Base: "HEAD", Head: "HEAD", Charts: []string{"grafana"}, TestCases: []gitopsdiff.TestCase{{Name: "kind", TargetType: "kind"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Empty() {
		t.Fatalf("HEAD against itself changed: %+v", report)
	}
}

func TestDiffExitsWithTwoWhenTheChangeBreaksRendering(t *testing.T) {
	dir := diffRepo(t)
	testrepo.Write(t, dir, map[string]string{"platform-apps/charts/grafana/templates/broken.yaml": "{{ fail \"nope\" }}\n"})
	var stdout bytes.Buffer
	err := runDiff(context.Background(), diffFlags{repo: dir, base: "HEAD", charts: "changed", format: "text"}, fakeDiffServices(&fakeCommentClient{}), &stdout, &bytes.Buffer{})
	if ExitCode(err) != 2 || !strings.Contains(err.Error(), "the change breaks rendering of grafana · small") {
		t.Fatalf("error = %v (exit %d)", err, ExitCode(err))
	}
	if !strings.Contains(stdout.String(), "grafana · small  broken by this change") {
		t.Fatalf("the result is written before failing:\n%s", stdout.String())
	}
}

func TestDiffOpensTheViewerOnlyInATerminalForAPerson(t *testing.T) {
	cases := []struct {
		name            string
		f               diffFlags
		plain, terminal bool
		want            bool
	}{
		{"a terminal", diffFlags{}, false, true, true},
		{"no terminal", diffFlags{}, false, false, false},
		{"--plain or an explicit --format", diffFlags{}, true, true, false},
		{"--output-dir", diffFlags{outputDir: "out"}, false, true, false},
		{"--pr", diffFlags{pr: 1}, false, true, false},
	}
	for _, c := range cases {
		if got := diffInteractive(c.f, c.plain, c.terminal); got != c.want {
			t.Errorf("%s: interactive = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestDiffChoicesSelectWhatTheFlagsNameOrElseTheDefaults(t *testing.T) {
	c := gitopsdiff.Choices{TargetTypes: []string{"kind", "peak"}, Charts: []string{"grafana", "loki"}, Changed: []string{"grafana"}}
	sel := func(opts []ui.Option) string {
		var out []string
		for _, o := range opts {
			if o.Selected {
				out = append(out, o.Label)
			}
		}
		return strings.Join(out, ",")
	}
	cases := []struct {
		name             string
		opts             gitopsdiff.Options
		testCases, chart string
	}{
		{"no flags: every target type and the changed charts", gitopsdiff.Options{}, "kind,peak", "grafana (changed)"},
		{"--target-type and --charts", gitopsdiff.Options{TestCases: []gitopsdiff.TestCase{{Name: "peak", TargetType: "peak"}}, Charts: []string{"loki"}}, "peak", "loki"},
		{"a hand-made test case and --charts all", gitopsdiff.Options{TestCases: []gitopsdiff.TestCase{{Name: "manual", Files: []string{"v.yaml"}}}, AllCharts: true}, "manual", "grafana (changed),loki"},
	}
	for _, tc := range cases {
		got := diffChoices(tc.opts, c)
		if sel(got.TestCases) != tc.testCases || sel(got.Charts) != tc.chart {
			t.Errorf("%s: selected test cases %q, charts %q; want %q, %q", tc.name, sel(got.TestCases), sel(got.Charts), tc.testCases, tc.chart)
		}
	}
}

func TestSelectedOptionsKeepHandMadeTestCasesAndNameTheCharts(t *testing.T) {
	manual := gitopsdiff.TestCase{Name: "manual", Files: []string{"v.yaml"}}
	opts := gitopsdiff.Options{Repo: "r", TestCases: []gitopsdiff.TestCase{manual}, AllCharts: true}
	got := selectedOptions(opts, ui.DiffSelection{Base: "main", TestCases: []string{"manual", "kind"}, Charts: []string{"loki"}})
	want := []gitopsdiff.TestCase{manual, {Name: "kind", TargetType: "kind"}}
	if got.Base != "main" || got.AllCharts || !reflect.DeepEqual(got.Charts, []string{"loki"}) || !reflect.DeepEqual(got.TestCases, want) {
		t.Fatalf("options = %+v", got)
	}
	if kept := selectedOptions(opts, ui.DiffSelection{Base: "main"}); !kept.AllCharts || len(kept.TestCases) != 1 {
		t.Fatalf("an empty selection keeps the defaults: %+v", kept)
	}
}
