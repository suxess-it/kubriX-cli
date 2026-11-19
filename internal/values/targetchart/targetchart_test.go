package targetchart

import (
	"errors"
	"io/fs"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/suxess-it/kubrix-cli/internal/testrepo"
	"github.com/suxess-it/kubrix-cli/internal/values/engine"
	"github.com/suxess-it/kubrix-cli/internal/values/overlay"
)

func repo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	testrepo.TargetChart(t, dir)
	testrepo.Write(t, dir, files)
	return dir
}

func load(t *testing.T, opts Options) *overlay.Overlay {
	t.Helper()
	o, err := Load(opts)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func names(o *overlay.Overlay) []string {
	var out []string
	for _, a := range o.Apps {
		out = append(out, a.Name)
	}
	return out
}

func app(t *testing.T, o *overlay.Overlay, name string) overlay.App {
	t.Helper()
	a, err := o.App(name)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestATargetTypeInstallsTheEnabledApplicationsOfItsBootstrapValuesFiles(t *testing.T) {
	dir := repo(t, map[string]string{
		"platform-apps/target-chart/values-small.yaml": "default:\n  valueFiles:\n  - values-kubrix-default.yaml\napplications:\n  loki:\n    enabled: false\n  tempo:\n    enabled: true\n",
		"platform-apps/charts/tempo/Chart.yaml":        "apiVersion: v2\nname: tempo\nversion: 0.1.0\n",
	})
	o := load(t, Options{Repo: dir, TargetType: "small"})
	if o.TargetType != "small" || !reflect.DeepEqual(names(o), []string{"grafana", "tempo"}) {
		t.Fatalf("overlay %s apps = %v, want grafana and tempo (loki disabled by the profile, tempo enabled)", o.TargetType, names(o))
	}
}

func TestDefaultValueFilesComeBeforeAnApplicationsOwnAndMissingOnesAreSkipped(t *testing.T) {
	dir := repo(t, map[string]string{
		"platform-apps/target-chart/values-small.yaml":   "default:\n  valueFiles:\n  - values-kubrix-default.yaml\n  - values-size-small.yaml\n  - values-missing.yaml\napplications:\n  grafana:\n    valueFiles:\n    - values-extra.yaml\n",
		"platform-apps/charts/grafana/values-extra.yaml": "grafana:\n  replicas: 5\n",
	})
	got := app(t, load(t, Options{Repo: dir, TargetType: "small"}), "grafana")
	want := []string{
		"platform-apps/charts/grafana/values-kubrix-default.yaml",
		"platform-apps/charts/grafana/values-size-small.yaml",
		"platform-apps/charts/grafana/values-extra.yaml",
	}
	if !reflect.DeepEqual(got.LayerNames(), want) || got.Chart != "sx-grafana" {
		t.Fatalf("grafana chart %s layers = %v, want sx-grafana %v", got.Chart, got.LayerNames(), want)
	}
}

func TestParametersBecomeSetSourcesWithArgoCDsBuildEnvironmentSubstituted(t *testing.T) {
	a := app(t, load(t, Options{Repo: repo(t, nil), TargetType: "small"}), "loki")
	var got []string
	for _, s := range a.Sets {
		got = append(got, s.Expression+" @ "+s.Source.File)
		// Argo CD passes parameters with --set, and with --set-string only when forceString is true.
		if s.StringValues {
			t.Errorf("%s is a string set source without forceString", s.Expression)
		}
	}
	want := []string{
		"kubriXPlan=oss @ Application sx-loki parameters",
		// The bootstrap Application passes $ARGOCD_APP_SOURCE_REPO_URL as default.repoURL; Argo CD replaces it with its own source.
		"global.repoURL=https://example.com/bootstrap @ Application sx-loki parameters",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sets = %v, want %v", got, want)
	}
}

func TestInlineValuesComeAfterValueFilesAndParametersAreEscapedForStrvals(t *testing.T) {
	dir := repo(t, map[string]string{
		"platform-apps/target-chart/values-small.yaml": "default:\n  valueFiles: [values-kubrix-default.yaml]\napplications:\n  grafana:\n    helmOptions:\n      values: |\n        grafana:\n          replicas: 7\n      valuesObject:\n        grafana:\n          replicas: 8\n",
	})
	a := app(t, load(t, Options{Repo: dir, TargetType: "small"}), "grafana")
	wantLayers := []string{"platform-apps/charts/grafana/values-kubrix-default.yaml", "Application sx-grafana helm.values", "Application sx-grafana helm.valuesObject"}
	if !reflect.DeepEqual(a.LayerNames(), wantLayers) {
		t.Fatalf("layers = %v, want %v", a.LayerNames(), wantLayers)
	}
	o := &overlay.Overlay{TargetType: "small"}
	r, err := o.Merge(a, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Records["grafana.replicas"].EffectiveValue; got != 8 {
		t.Fatalf("grafana.replicas = %#v, want 8 from valuesObject", got)
	}
	set := parameter{Name: "a,b", Value: `x\y`, ForceString: true}.setSource("p", 0)
	vals, err := overlay.HelmValues(nil, []engine.SetSource{set})
	if err != nil || !reflect.DeepEqual(vals, map[string]any{"a,b": `x\y`}) {
		t.Fatalf("escaped parameter = %#v, %v", vals, err)
	}
}

func TestTemplatesAreRenderedWithTheCustomerConfigAndNamedAsRendered(t *testing.T) {
	dir := repo(t, map[string]string{
		"bootstrap-app-tmpl.yaml":                                    testrepo.BootstrapApp("values-kubrix-default.yaml", "values-tmpl.yaml"),
		"platform-apps/target-chart/values-tmpl.yaml.tmpl":           "default:\n  valueFiles:\n  - values-kubrix-default.yaml\n  - values-cluster-{{ .kubriX.clusterType }}.yaml\n",
		"platform-apps/charts/grafana/values-cluster-kind.yaml.tmpl": "grafana:\n  domain: {{ .kubriX.domain }}\n",
		"platform-apps/charts/grafana/values-cluster-k8s.yaml":       "grafana:\n  domain: wrong\n",
	})
	cfg := DefaultConfig()
	cfg["clusterType"], cfg["domain"] = "kind", "example.org"
	a := app(t, load(t, Options{Repo: dir, TargetType: "tmpl", Config: cfg}), "grafana")
	want := "platform-apps/charts/grafana/values-cluster-kind.yaml.tmpl" + RenderedSuffix
	if names := a.LayerNames(); len(names) != 2 || names[1] != want {
		t.Fatalf("layers = %v, want the kind template rendered last", names)
	}
	r, err := (&overlay.Overlay{}).Merge(a, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rec := r.Records["grafana.domain"]; rec.EffectiveValue != "example.org" || rec.EffectiveSource != (engine.SourceLocation{File: want, Line: 2, Column: 11}) {
		t.Fatalf("grafana.domain = %+v", rec)
	}
}

func TestWithoutABootstrapApplicationTheProfileIsLayeredOverTheDefaults(t *testing.T) {
	dir := repo(t, map[string]string{"platform-apps/target-chart/values-solo.yaml": "applications:\n  loki:\n    enabled: false\n"})
	if got := names(load(t, Options{Repo: dir, TargetType: "solo"})); !reflect.DeepEqual(got, []string{"grafana"}) {
		t.Fatalf("apps = %v, want [grafana]", got)
	}
	explicit := load(t, Options{TargetValues: filepath.Join(dir, "platform-apps/target-chart/values-solo.yaml")})
	if explicit.TargetType != "solo" || !reflect.DeepEqual(names(explicit), []string{"grafana"}) {
		t.Fatalf("--target-values: %s %v, want solo [grafana]", explicit.TargetType, names(explicit))
	}
}

func TestTheLegacyListFormatStillResolves(t *testing.T) {
	dir := repo(t, map[string]string{
		"bootstrap-app-legacy.yaml":                     testrepo.BootstrapApp("values-legacy.yaml"),
		"platform-apps/target-chart/values-legacy.yaml": "default:\n  valueFiles: [values-kubrix-default.yaml]\napplications:\n- name: loki\n  valueFiles: [values-size-small.yaml]\n",
	})
	a := app(t, load(t, Options{Repo: dir, TargetType: "legacy"}), "loki")
	if want := []string{"platform-apps/charts/loki/values-kubrix-default.yaml"}; !reflect.DeepEqual(a.LayerNames(), want) {
		t.Fatalf("loki layers = %v, want %v (values-size-small.yaml does not exist for loki)", a.LayerNames(), want)
	}
}

func TestResolutionErrorsNameTheirCause(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		opts  Options
		want  string
	}{
		{"unknown target type", nil, Options{TargetType: "nope"}, "neither bootstrap-app-nope.yaml nor"},
		{"no target type", nil, Options{}, "a target type is required"},
		{"the chart's template fails", map[string]string{"platform-apps/target-chart/values-small.yaml": "applications:\n- name: loki\n"}, Options{TargetType: "small"}, "applicationMap=true requires applications to be a map"},
		{"a template with a missing key", map[string]string{"platform-apps/target-chart/values-small.yaml.tmpl": "{{ .kubriX.nope }}", "platform-apps/target-chart/values-small.yaml": ""}, Options{TargetType: "small"}, ""},
		{"a bootstrap Application for another chart", map[string]string{"bootstrap-app-small.yaml": "spec:\n  source:\n    path: elsewhere\n"}, Options{TargetType: "small"}, "points at elsewhere"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.opts.Repo = repo(t, c.files)
			_, err := Load(c.opts)
			if c.want == "" {
				if err != nil {
					t.Fatalf("an existing file must win over its template: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error = %v, want %q", err, c.want)
			}
		})
	}
}

func TestAMissingChartIsAWarningNotAnError(t *testing.T) {
	dir := repo(t, map[string]string{"platform-apps/target-chart/values-small.yaml": "applications:\n  ghost:\n    enabled: true\n"})
	o := load(t, Options{Repo: dir, TargetType: "small"})
	if len(o.Warnings) != 1 || !strings.Contains(o.Warnings[0], "ghost: chart platform-apps/charts/ghost does not exist") {
		t.Fatalf("warnings = %v", o.Warnings)
	}
}

func TestTargetTypesListsBootstrapApplicationsAndProfiles(t *testing.T) {
	dir := repo(t, map[string]string{
		"platform-apps/target-chart/values-solo.yaml.tmpl": "",
		"platform-apps/target-chart/values-customer.yaml":  "",
		"bootstrap-app-other.yaml":                         "",
	})
	got, err := TargetTypes(dir)
	if err != nil || !reflect.DeepEqual(got, []string{"other", "small", "solo"}) {
		t.Fatalf("target types = %v, %v; want other, small, solo (not kubrix-default or customer)", got, err)
	}
}

func TestLoadConfigLayersTheRepositoryConfigOverTheDefaults(t *testing.T) {
	dir := t.TempDir()
	if cfg, err := LoadConfig(dir, ""); err != nil || cfg["clusterType"] != "k8s" || cfg["cloudProvider"] != "on-prem" {
		t.Fatalf("without a repository config: %v, %v", cfg, err)
	}
	testrepo.Write(t, dir, map[string]string{"bootstrap/customer-config.yaml": "clusterType: kind\n", "other.yaml": "domain: x.org\n"})
	if cfg, err := LoadConfig(dir, ""); err != nil || cfg["clusterType"] != "kind" || cfg["cloudProvider"] != "on-prem" {
		t.Fatalf("with a partial repository config: %v, %v", cfg, err)
	}
	if cfg, err := LoadConfig(dir, filepath.Join(dir, "other.yaml")); err != nil || cfg["domain"] != "x.org" || cfg["clusterType"] != "k8s" {
		t.Fatalf("with an explicit config: %v, %v", cfg, err)
	}
	if _, err := LoadConfig(dir, filepath.Join(dir, "missing.yaml")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing explicit config: %v", err)
	}
}
