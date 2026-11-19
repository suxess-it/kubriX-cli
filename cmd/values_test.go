package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/suxess-it/kubrix-cli/internal/platformrepo"
	"github.com/suxess-it/kubrix-cli/internal/testrepo"
	"github.com/suxess-it/kubrix-cli/internal/values/targetchart"
)

var update = flag.Bool("update", false, "rewrite golden files in testdata")

// kubrix runs the CLI in-process and returns stdout, stderr and the error the command returned.
func kubrix(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	root := NewRoot(BuildInfo{}, Features{})
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(args)
	err := root.Execute()
	return stdout.String(), stderr.String(), err
}

func mustKubrix(t *testing.T, args ...string) string {
	t.Helper()
	out, stderr, err := kubrix(t, args...)
	if err != nil {
		t.Fatalf("kubrix %v: %v\n%s", args, err, stderr)
	}
	return out
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test ./cmd -update to create it)", err)
	}
	if got != string(want) {
		t.Fatalf("%s differs from the output:\n%s\n(run go test ./cmd -update after reviewing the contract change)", path, got)
	}
}

func platformRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	testrepo.TargetChart(t, dir)
	testrepo.Write(t, dir, files)
	return dir
}

// Each way of selecting the values files is reached by its flags; the merged result tells which one was used.
func TestValuesSelectsTheSourceOfValuesFilesByItsFlags(t *testing.T) {
	dir := platformRepo(t, map[string]string{
		".kubrix-values.yaml": "version: v1\ntargets:\n  small:\n    applications:\n      grafana:\n        files: [cfg.yaml]\n",
		"cfg.yaml":            "from: project-config\n",
		"file.yaml":           "from: file\n",
	})
	noConfig := platformRepo(t, nil)
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"a project config in --repo wins over the target chart", []string{"--repo", dir, "--target-type", "small", "--app", "grafana"}, "from: project-config\n"},
		{"an explicit --values-config", []string{"--values-config", filepath.Join(dir, ".kubrix-values.yaml"), "--target-type", "small", "--app", "grafana"}, "from: project-config\n"},
		{"without a project config the target chart is used", []string{"--repo", noConfig, "--target-type", "small", "--app", "grafana"}, "replicas: 2\n"},
		{"--target-values names a target values file", []string{"--target-values", filepath.Join(noConfig, "platform-apps/target-chart/values-small.yaml"), "--app", "grafana"}, "replicas: 2\n"},
		{"--file replaces any target type", []string{"--file", filepath.Join(dir, "file.yaml"), "--app", "x"}, "from: file\n"},
		{"--set alone with an --app", []string{"--set", "from=set", "--app", "x"}, "from: set\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := mustKubrix(t, append([]string{"values", "merge"}, c.args...)...)
			if !strings.Contains(out, c.want) {
				t.Fatalf("merge output:\n%s\nwant it to contain %q", out, c.want)
			}
		})
	}
}

func TestValuesRejectsAmbiguousOrIncompleteSelections(t *testing.T) {
	dir := platformRepo(t, nil)
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"--target-values with --file", []string{"merge", "--target-values", "x.yaml", "--file", "y.yaml", "--app", "a"}, "--target-values and --file cannot be used together"},
		{"nothing selected", []string{"apps"}, "--target-type, --target-values or --file is required"},
		{"no application", []string{"merge", "--repo", dir, "--target-type", "small"}, "--app is required"},
		{"unknown application", []string{"merge", "--repo", dir, "--target-type", "small", "--app", "nope"}, `application "nope" does not exist in target type "small"`},
		{"an explicit project config that is missing", []string{"apps", "--values-config", filepath.Join(dir, "none.yaml"), "--target-type", "small"}, "read config"},
		{"an invalid format", []string{"apps", "--repo", dir, "--target-type", "small", "--format", "xml"}, `invalid --format "xml"`},
		{"an unknown path", []string{"explain", "nope", "--repo", dir, "--target-type", "small", "--app", "loki"}, `path "nope" does not exist in application "loki"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := kubrix(t, append([]string{"values"}, c.args...)...)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error = %v, want %q", err, c.want)
			}
		})
	}
}

func TestValuesFindsTemplateProfilesAndRendersThemWithTheGivenCustomerConfig(t *testing.T) {
	dir := platformRepo(t, map[string]string{
		"platform-apps/target-chart/values-tmpl.yaml.tmpl": "default:\n  valueFiles: [values-kubrix-default.yaml, values-{{ .kubriX.clusterType }}.yaml]\n",
		"platform-apps/charts/loki/values-kind.yaml":       "loki:\n  retention: 1d\n",
		"platform-apps/charts/loki/values-k8s.yaml":        "loki:\n  retention: 30d\n",
		"kind.yaml": "clusterType: kind\n",
	})
	defaults := mustKubrix(t, "values", "merge", "--repo", dir, "--target-type", "tmpl", "--app", "loki")
	kind := mustKubrix(t, "values", "merge", "--repo", dir, "--target-type", "tmpl", "--app", "loki", "--customer-config", filepath.Join(dir, "kind.yaml"))
	if !strings.Contains(defaults, "retention: 30d") || !strings.Contains(kind, "retention: 1d") {
		t.Fatalf("default config:\n%s\nkind config:\n%s", defaults, kind)
	}
}

func TestValuesWarningsGoToStderrAndDataToStdout(t *testing.T) {
	dir := platformRepo(t, map[string]string{"platform-apps/target-chart/values-small.yaml": "applications:\n  ghost:\n    enabled: true\n"})
	out, stderr, err := kubrix(t, "values", "apps", "--repo", dir, "--target-type", "small", "--format", "json")
	if err != nil {
		t.Fatal(err)
	}
	var apps []appJSON
	if err := json.Unmarshal([]byte(out), &apps); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, out)
	}
	if !strings.HasPrefix(stderr, "warning: ghost: chart platform-apps/charts/ghost does not exist") {
		t.Fatalf("stderr = %q", stderr)
	}
}

// The JSON of apps, explain and lint is what scripts and CI consume; changing it is a reviewed contract change.
func TestValuesJSONContracts(t *testing.T) {
	dir := platformRepo(t, map[string]string{"platform-apps/charts/grafana/values-size-small.yaml": "grafana:\n  replicas: 2\n  ingress:\n    enabled: true\n"})
	base := []string{"--repo", dir, "--target-type", "small", "--format", "json"}
	golden(t, "values-apps.json", mustKubrix(t, append([]string{"values", "apps"}, base...)...))
	golden(t, "values-explain.json", mustKubrix(t, append([]string{"values", "explain", "grafana.replicas", "--app", "grafana"}, base...)...))
	golden(t, "values-lint.json", mustKubrix(t, append([]string{"values", "lint", "--app", "grafana"}, base...)...))
	golden(t, "values-overrides.json", mustKubrix(t, append([]string{"values", "overrides", "--app", "grafana"}, base...)...))
}

func TestValuesSearchAppliesSetToEveryApplication(t *testing.T) {
	dir := platformRepo(t, nil)
	out := mustKubrix(t, "values", "search", "team", "--repo", dir, "--target-type", "small", "--set", "team=core", "--format", "json")
	var rows []searchRow
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatal(err)
	}
	var apps []string
	for _, r := range rows {
		if r.Path != "team" || r.EffectiveValue != "core" || r.EffectiveSource.File != "--set" {
			t.Fatalf("row = %+v", r)
		}
		apps = append(apps, r.Application)
	}
	if !reflect.DeepEqual(apps, []string{"grafana", "loki"}) {
		t.Fatalf("apps = %v, want grafana and loki", apps)
	}
}

func TestValuesSearchRanksExactThenSuffixThenSubstringMatches(t *testing.T) {
	rows := []searchRow{{Path: "x.replicas.max", rank: matchRank("x.replicas.max", "replicas")}, {Path: "a.replicas", rank: matchRank("a.replicas", "replicas")}, {Path: "replicas", rank: matchRank("replicas", "replicas")}, {Path: "other", rank: matchRank("other", "replicas")}}
	sortSearchRows(rows)
	var got []string
	for _, r := range rows {
		got = append(got, r.Path)
	}
	if want := []string{"replicas", "a.replicas", "x.replicas.max", "other"}; !reflect.DeepEqual(got, want) || rows[3].rank != 0 {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

func TestValuesLintFailOnReturnsExitCodeTwo(t *testing.T) {
	dir := platformRepo(t, map[string]string{"platform-apps/charts/grafana/values-size-small.yaml": "grafana:\n  replicas: 1\n"})
	args := []string{"values", "lint", "--repo", dir, "--target-type", "small", "--app", "grafana"}
	if _, _, err := kubrix(t, args...); ExitCode(err) != 0 {
		t.Fatalf("without --fail-on: %v", err)
	}
	if _, _, err := kubrix(t, append(args, "--fail-on", "warning")...); ExitCode(err) != 0 {
		t.Fatalf("an info finding must not reach --fail-on warning: %v", err)
	}
	if _, _, err := kubrix(t, append(args, "--fail-on", "info")...); ExitCode(err) != 2 {
		t.Fatalf("an identical redefinition must reach --fail-on info with exit code 2: %v", err)
	}
}

func TestExitCode(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{nil, 0},
		{errors.New("plain"), 1},
		{&exitError{code: 2, err: errors.New("lint")}, 2},
		{errors.Join(errors.New("wrapped"), &exitError{code: 3, err: errors.New("x")}), 3},
	}
	for _, c := range cases {
		if got := ExitCode(c.err); got != c.want {
			t.Errorf("ExitCode(%v) = %d, want %d", c.err, got, c.want)
		}
	}
}

func TestValuesExampleProjectStillWorks(t *testing.T) {
	out := mustKubrix(t, "values", "lint", "--values-config", filepath.Join("..", "examples", "values", ".kubrix-values.yaml"), "--target-type", "kind-hub-prime", "--app", "mimir")
	if !strings.Contains(out, "no-op-file") {
		t.Fatalf("the example's customer file is meant to be a no-op:\n%s", out)
	}
}

// Every target type of the real kubriX repository resolves, installs the applications the bootstrap would, and merges.
func TestValuesResolvesEveryTargetTypeOfTheKubrixRepository(t *testing.T) {
	repo := testrepo.KubrixRepo(t)
	types, err := targetchart.TargetTypes(repo)
	if err != nil || len(types) == 0 {
		t.Fatalf("target types = %v, %v", types, err)
	}
	cfg, err := targetchart.LoadConfig(repo, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range types {
		t.Run(tt, func(t *testing.T) {
			o, err := targetchart.Load(targetchart.Options{Repo: repo, TargetType: tt, Config: cfg})
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, app := range o.Apps {
				got = append(got, app.Name)
				if _, err := o.Merge(app, nil); err != nil {
					t.Errorf("merge %s: %v", app.Name, err)
				}
			}
			if want := bootstrapApps(t, repo, tt, cfg); want != nil && !reflect.DeepEqual(got, want) {
				t.Errorf("apps = %v, want what platformrepo.AppsLayered finds: %v", got, want)
			}
		})
	}
}

// bootstrapApps lists the applications platformrepo finds for a target type whose bootstrap layers defaults and profile.
func bootstrapApps(t *testing.T, repo, targetType string, cfg map[string]any) []string {
	t.Helper()
	read := func(rel string) []byte {
		if b, err := os.ReadFile(filepath.Join(repo, rel)); err == nil {
			return b
		}
		tmpl, err := os.ReadFile(filepath.Join(repo, platformrepo.TemplatePath(rel)))
		if err != nil {
			return nil
		}
		b, err := platformrepo.Template(rel, tmpl, cfg)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	profile := read(platformrepo.TargetValuesPath(targetType))
	if profile == nil {
		return nil
	}
	apps, err := platformrepo.AppsLayered(read(platformrepo.DefaultValuesPath), profile)
	if err != nil {
		t.Fatal(err)
	}
	return apps
}
