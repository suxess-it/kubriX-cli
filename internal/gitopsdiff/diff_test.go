package gitopsdiff

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"helm.sh/helm/v4/pkg/chart/v2/loader"
	chartutil "helm.sh/helm/v4/pkg/chart/v2/util"

	"github.com/suxess-it/kubrix-cli/internal/testrepo"
	"github.com/suxess-it/kubrix-cli/internal/values/engine"
)

// platformRepo is a git repository with testrepo.TargetChart, templates for grafana, and loki depending on a local
// subchart with a CRD, committed once. It needs no network: the dependency is a file:// chart.
func platformRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	testrepo.TargetChart(t, dir)
	testrepo.Write(t, dir, map[string]string{
		"platform-apps/charts/grafana/templates/config.yaml":  "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: {{ .Release.Name }}\ndata:\n  replicas: {{ .Values.grafana.replicas | quote }}\n",
		"platform-apps/charts/grafana/templates/ingress.yaml": "{{- if .Values.grafana.ingress.enabled }}\napiVersion: networking.k8s.io/v1\nkind: Ingress\nmetadata:\n  name: grafana\n{{- end }}\n",
		"platform-apps/charts/grafana/templates/_helpers.tpl": "{{- define \"x\" }}x{{ end }}\n",
		"platform-apps/charts/loki/Chart.yaml":                "apiVersion: v2\nname: loki\nversion: 0.1.0\ndependencies:\n- name: sub\n  version: 0.1.0\n  repository: file://../../../shared/sub\n",
		"platform-apps/charts/loki/templates/cm.yaml":         "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: loki\ndata:\n  retention: {{ .Values.loki.retention }}\n",
		"shared/sub/Chart.yaml":                               "apiVersion: v2\nname: sub\nversion: 0.1.0\n",
		"shared/sub/values.yaml":                              "size: 1\n",
		"shared/sub/templates/cm.yaml":                        "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: sub\ndata:\n  size: {{ .Values.size | quote }}\n",
		"shared/sub/crds/crd.yaml":                            "apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\nmetadata:\n  name: subs.example.com\n",
	})
	g := testrepo.Git(t, dir)
	testrepo.Run(t, g, "init", "-q", "-b", "main")
	commit(t, dir, "base")
	return dir
}

func commit(t *testing.T, dir, msg string) {
	t.Helper()
	g := testrepo.Git(t, dir)
	testrepo.Run(t, g, "add", "-A")
	testrepo.Run(t, g, "commit", "-q", "-m", msg)
}

// countingDependencies builds dependencies with helm and counts how often it is asked to.
func countingDependencies(t *testing.T, calls *atomic.Int32) DependencyBuilder {
	deps := HelmDependencies(t.TempDir())
	return func(ctx context.Context, dir string) error {
		calls.Add(1)
		return deps(ctx, dir)
	}
}

func run(t *testing.T, opts Options) *Report {
	t.Helper()
	var calls atomic.Int32
	if opts.Dependencies == nil {
		opts.Dependencies = countingDependencies(t, &calls)
	}
	r, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func paths(files []FileDiff) []string {
	var out []string
	for _, f := range files {
		out = append(out, string(f.Status)+" "+f.Chart+"/"+f.TestCase+"/"+f.Path)
	}
	return out
}

func TestAValuesChangeShowsInTheRenderedManifestsAndTheValuesWithItsSource(t *testing.T) {
	dir := platformRepo(t)
	testrepo.Write(t, dir, map[string]string{"platform-apps/charts/grafana/values-size-small.yaml": "grafana:\n  replicas: 3\n  ingress:\n    enabled: false\n"})
	commit(t, dir, "change")

	r := run(t, Options{Repo: dir, Base: "HEAD~1", Head: "HEAD"})
	want := []string{"modified grafana/small/sx-grafana/templates/config.yaml", "removed grafana/small/sx-grafana/templates/ingress.yaml"}
	if got := paths(r.Files); !reflect.DeepEqual(got, want) {
		t.Fatalf("files = %v, want %v (only the changed chart, no helpers)", got, want)
	}
	if !strings.Contains(r.Files[0].Text, `-  replicas: "2"`) || !strings.Contains(r.Files[0].Text, `+  replicas: "3"`) || r.Files[0].Additions != 1 || r.Files[0].Deletions != 1 {
		t.Fatalf("config diff:\n%s", r.Files[0].Text)
	}
	src := engine.SourceLocation{File: "platform-apps/charts/grafana/values-size-small.yaml", Line: 2, Column: 13}
	wantValues := []ValueChange{
		{Chart: "grafana", TestCase: "small", Path: "grafana.ingress.enabled", Kind: Modified, Before: true, After: false, Source: engine.SourceLocation{File: src.File, Line: 4, Column: 14}},
		{Chart: "grafana", TestCase: "small", Path: "grafana.replicas", Kind: Modified, Before: 2, After: 3, Source: src},
	}
	if !reflect.DeepEqual(r.ValueChanges, wantValues) {
		t.Fatalf("value changes = %+v\nwant %+v", r.ValueChanges, wantValues)
	}
}

func TestTheWorkingTreeIsComparedWhenThereIsNoHead(t *testing.T) {
	dir := platformRepo(t)
	testrepo.Write(t, dir, map[string]string{"platform-apps/charts/loki/values-kubrix-default.yaml": "loki:\n  retention: 30d\n"})
	r := run(t, Options{Repo: dir, Base: "HEAD"})
	if got := paths(r.Files); !reflect.DeepEqual(got, []string{"modified loki/small/loki/templates/cm.yaml"}) {
		t.Fatalf("files = %v", got)
	}
	if out := testrepo.Run(t, testrepo.Git(t, dir), "worktree", "list"); strings.Count(out, "\n") != 0 {
		t.Fatalf("worktrees left behind:\n%s", out)
	}
	if status := testrepo.Run(t, testrepo.Git(t, dir), "status", "--porcelain"); status != "M platform-apps/charts/loki/values-kubrix-default.yaml" {
		t.Fatalf("the working tree was changed by the diff:\n%s", status)
	}
}

func TestALocalDependencyIsBuiltPerRevisionSoItsChangesShow(t *testing.T) {
	dir := platformRepo(t)
	testrepo.Write(t, dir, map[string]string{
		"shared/sub/values.yaml":   "size: 2\n",
		"shared/sub/crds/crd.yaml": "apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\nmetadata:\n  name: subs.example.com\n  labels:\n    v: \"2\"\n",
	})
	commit(t, dir, "subchart changes")

	var calls atomic.Int32
	r := run(t, Options{Repo: dir, Base: "HEAD~1", Head: "HEAD", Charts: []string{"loki"}, Dependencies: countingDependencies(t, &calls)})
	if calls.Load() != 2 {
		t.Fatalf("dependencies built %d times, want once per revision", calls.Load())
	}
	want := []string{"modified loki/small/loki/charts/sub/crds/crd.yaml", "modified loki/small/loki/charts/sub/templates/cm.yaml"}
	if got := paths(r.Files); !reflect.DeepEqual(got, want) {
		t.Fatalf("files = %v, want the subchart's CRD and template", got)
	}
	if !strings.HasPrefix(r.DefaultValuesDiff, "diff -U 4 -rN target/loki_sub_default-values.out pr/loki_sub_default-values.out\n") ||
		!strings.Contains(r.DefaultValuesDiff, "-size: 1\n+size: 2\n") {
		t.Fatalf("default values diff:\n%s", r.DefaultValuesDiff)
	}
}

func TestARemoteDependencyIsFetchedOnceForBothRevisions(t *testing.T) {
	dir := platformRepo(t)
	testrepo.Write(t, dir, map[string]string{
		"platform-apps/charts/loki/Chart.yaml":                 "apiVersion: v2\nname: loki\nversion: 0.1.0\ndependencies:\n- name: sub\n  version: 0.1.0\n  repository: https://charts.example.com\n",
		"platform-apps/charts/loki/values-kubrix-default.yaml": "loki:\n  retention: 1d\n",
	})
	commit(t, dir, "remote dependency")
	testrepo.Write(t, dir, map[string]string{"platform-apps/charts/loki/values-kubrix-default.yaml": "loki:\n  retention: 2d\n"})
	commit(t, dir, "change")

	// The fake repository serves the local subchart as an archive.
	sub, err := loader.LoadDir(filepath.Join(dir, "shared", "sub"))
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	fetch := func(_ context.Context, chartDir string) error {
		calls.Add(1)
		if err := os.MkdirAll(filepath.Join(chartDir, "charts"), 0o755); err != nil {
			return err
		}
		_, err := chartutil.Save(sub, filepath.Join(chartDir, "charts"))
		return err
	}
	r := run(t, Options{Repo: dir, Base: "HEAD~1", Head: "HEAD", Charts: []string{"loki"}, Dependencies: fetch})
	if calls.Load() != 1 {
		t.Fatalf("dependencies fetched %d times, want once: base and head have the same Chart.yaml", calls.Load())
	}
	if got := paths(r.Files); !reflect.DeepEqual(got, []string{"modified loki/small/loki/templates/cm.yaml"}) {
		t.Fatalf("files = %v", got)
	}
}

func TestChangedChartsFollowWhatTheChangeTouches(t *testing.T) {
	all := []string{"grafana", "loki", "tempo"}
	cases := []struct {
		name  string
		files []string
		want  []string
	}{
		{"files of two charts", []string{"platform-apps/charts/loki/values.yaml", "platform-apps/charts/grafana/templates/a.yaml", "platform-apps/charts/loki/Chart.yaml"}, []string{"grafana", "loki"}},
		{"the target chart affects every chart", []string{"platform-apps/charts/loki/values.yaml", "platform-apps/target-chart/values-kind.yaml"}, all},
		{"a bootstrap Application affects every chart", []string{"bootstrap-app-kind.yaml"}, all},
		{"no chart changed: every chart, like the CI script", []string{".github/workflows/gitops-diff.yaml"}, all},
		{"a file directly in the charts directory is no chart", []string{"platform-apps/charts/README.md"}, all},
	}
	for _, c := range cases {
		if got := changedCharts(c.files, all); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestAHandMadeTestCaseUsesChartLocalFilesAndHelmSets(t *testing.T) {
	dir := platformRepo(t)
	testrepo.Write(t, dir, map[string]string{"platform-apps/charts/grafana/values-size-small.yaml": "grafana:\n  replicas: 4\n"})
	commit(t, dir, "change")
	tc := TestCase{Name: "manual", Files: []string{"values-kubrix-default.yaml", "values-size-small.yaml", "values-missing.yaml"},
		Sets: []engine.SetSource{{Expression: "grafana.ingress.enabled=false", Source: engine.SourceLocation{File: "--test-case manual", Line: 1}}}}
	r := run(t, Options{Repo: dir, Base: "HEAD~1", Head: "HEAD", TestCases: []TestCase{tc}})
	if got := paths(r.Files); !reflect.DeepEqual(got, []string{"modified grafana/manual/sx-grafana/templates/config.yaml"}) {
		t.Fatalf("files = %v (the --set disables the ingress on both sides)", got)
	}
}

func TestATargetTypeThatInstallsAChartOnlyOnOneSideAddsOrRemovesIt(t *testing.T) {
	dir := platformRepo(t)
	testrepo.Write(t, dir, map[string]string{"platform-apps/target-chart/values-small.yaml": "default:\n  valueFiles: [values-kubrix-default.yaml]\napplications:\n  grafana:\n    enabled: false\n"})
	commit(t, dir, "drop grafana")
	r := run(t, Options{Repo: dir, Base: "HEAD~1", Head: "HEAD", TestCases: []TestCase{{Name: "small", TargetType: "small"}}, Charts: []string{"grafana"}})
	want := []string{"removed grafana/small/sx-grafana/templates/config.yaml", "removed grafana/small/sx-grafana/templates/ingress.yaml"}
	if got := paths(r.Files); !reflect.DeepEqual(got, want) {
		t.Fatalf("files = %v, want %v", got, want)
	}
	for _, v := range r.ValueChanges {
		if v.Kind != Removed {
			t.Fatalf("value change %+v, want only removed values", v)
		}
	}
}

func TestAnUnknownBaseIsAnError(t *testing.T) {
	_, err := Run(context.Background(), Options{Repo: platformRepo(t), Base: "nope"})
	if err == nil || !strings.Contains(err.Error(), "check out nope") {
		t.Fatalf("error = %v", err)
	}
}

func TestDiffFileClassifiesAndCountsLikeDiffN(t *testing.T) {
	cases := []struct {
		old, new           string
		oldExists, exists  bool
		status             Status
		additions, removed int
	}{
		{"a\nb\n", "a\nc\nd\n", true, true, Modified, 2, 1},
		{"", "x\n--- y\n", false, true, Added, 2, 0},
		{"x\n", "", true, false, Removed, 0, 1},
	}
	for _, c := range cases {
		f, changed := diffFile("c", "t", "p.yaml", c.old, c.new, c.oldExists, c.exists)
		if !changed || f.Status != c.status || f.Additions != c.additions || f.Deletions != c.removed || !strings.HasPrefix(f.Text, "diff -U 4 -rN target/c/t/p.yaml pr/c/t/p.yaml\n--- target/c/t/p.yaml\n+++ pr/c/t/p.yaml\n") {
			t.Errorf("diffFile(%q, %q) = %+v", c.old, c.new, f)
		}
	}
	if _, changed := diffFile("c", "t", "p", "same", "same", true, true); changed {
		t.Error("an unchanged file is reported")
	}
	if _, changed := diffFile("c", "t", "p", "", "", false, true); !changed {
		t.Error("an added empty file is not reported")
	}
}

func TestRenderedDocumentsAreHeadedLikeHelmTemplate(t *testing.T) {
	got := documents("c/templates/a.yaml", "\n---\na: 1\n---\n\n---\nb: 2\n")
	want := "---\n# Source: c/templates/a.yaml\na: 1\n---\n# Source: c/templates/a.yaml\nb: 2\n"
	if got != want {
		t.Fatalf("documents =\n%q\nwant\n%q", got, want)
	}
	if filepath.Base(baseName) != "target" || headName != "pr" {
		t.Fatal("revision names are part of the comment contract")
	}
}
