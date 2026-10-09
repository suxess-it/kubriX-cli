package platformrepo

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const targetTemplate = `default:
  valueFiles:
  - values-cluster-{{ .kubriX.clusterType }}.yaml

applications:

  - name: traefik

  - name: kargo
    annotations:
      argocd.argoproj.io/sync-wave: "1"

  {{ if ne .kubriX.clusterType "kind" -}}
  - name: external-dns
  {{ end -}}

  - name: backstage
`

var testInput = ConfigInput{ClusterType: "k8s", DNSProvider: "none", Domain: "example.com", Repo: "https://github.com/acme/demo.git", GitUser: "octocat"}

// checkout writes a platform repository with templates in the template dirs and one elsewhere.
func checkout(t *testing.T, files map[string]string) *Repo {
	t.Helper()
	dir := t.TempDir()
	for rel, content := range files {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return At(dir)
}

func read(t *testing.T, r *Repo, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(r.dir, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func exists(r *Repo, rel string) bool {
	_, err := os.Stat(filepath.Join(r.dir, rel))
	return err == nil
}

func TestLayoutHelpers(t *testing.T) {
	if !IsTemplate("platform-apps/a/values.yaml.tmpl") || IsTemplate("platform-apps/a/values.yaml") || IsTemplate("README.md.tmpl") {
		t.Error("only *.yaml.tmpl files are templates")
	}
	if got := TemplatePath("platform-apps/a/values.yaml"); got != "platform-apps/a/values.yaml.tmpl" {
		t.Errorf("template path %q", got)
	}
	if got := OutputPath(TemplatePath("x/y.yaml")); got != "x/y.yaml" {
		t.Errorf("a template renders back to its output: %q", got)
	}
	if got := TargetValuesPath("kubrix-oss-stack"); got != "platform-apps/target-chart/values-kubrix-oss-stack.yaml" {
		t.Errorf("target values %q", got)
	}
	if TemplateGlob != "*.yaml.tmpl" {
		t.Errorf("glob %q", TemplateGlob)
	}
}

func TestWriteConfigAndConfigRoundTrip(t *testing.T) {
	r := checkout(t, nil)
	if _, err := r.Config(); err == nil || !strings.Contains(err.Error(), "was the repository bootstrapped by kubriX?") {
		t.Errorf("a repository without config: %v", err)
	}
	if err := r.WriteConfig(testInput); err != nil {
		t.Fatal(err)
	}
	cfg, err := r.Config()
	if err != nil {
		t.Fatal(err)
	}
	if cfg["domain"] != "example.com" || cfg["clusterType"] != "k8s" {
		t.Errorf("config %v", cfg)
	}
	if !exists(r, ConfigPath) {
		t.Error("the config is written to its fixed place")
	}
}

func TestRenderAllRendersTemplatesInTheTemplateDirsOnly(t *testing.T) {
	r := checkout(t, map[string]string{
		"platform-apps/charts/grafana/values.yaml.tmpl":  "host: grafana.{{ .kubriX.domain }}\n",
		"backstage-resources/catalog.yaml.tmpl":          "domain: {{ .kubriX.domain }}\n",
		"docs/example.yaml.tmpl":                         "domain: {{ .kubriX.domain }}\n",
		"platform-apps/charts/grafana/plain.yaml":        "keep: me\n",
		"platform-apps/charts/grafana/not-a-template.md": "x\n",
	})
	if err := r.WriteConfig(testInput); err != nil {
		t.Fatal(err)
	}
	if err := r.RenderAll(); err != nil {
		t.Fatal(err)
	}
	if got := read(t, r, "platform-apps/charts/grafana/values.yaml"); got != "host: grafana.example.com\n" {
		t.Errorf("grafana %q", got)
	}
	if got := read(t, r, "backstage-resources/catalog.yaml"); got != "domain: example.com\n" {
		t.Errorf("backstage %q", got)
	}
	if exists(r, "docs/example.yaml") {
		t.Error("templates outside the template dirs are not rendered")
	}
}

func TestRenderAllNeedsTheConfig(t *testing.T) {
	r := checkout(t, map[string]string{"platform-apps/a.yaml.tmpl": "x: 1\n"})
	if err := r.RenderAll(); err == nil {
		t.Error("rendering without the customer config must fail")
	}
}

func TestRenderRendersOnlyTheGivenTemplates(t *testing.T) {
	r := checkout(t, map[string]string{
		"platform-apps/a/values.yaml.tmpl": "a: {{ .kubriX.domain }}\n",
		"platform-apps/b/values.yaml.tmpl": "b: {{ .kubriX.domain }}\n",
	})
	if err := r.WriteConfig(testInput); err != nil {
		t.Fatal(err)
	}
	out, err := r.Render([]string{"platform-apps/a/values.yaml.tmpl"})
	if err != nil || !slices.Equal(out, []string{"platform-apps/a/values.yaml"}) {
		t.Fatalf("%v %v", out, err)
	}
	if !exists(r, "platform-apps/a/values.yaml") || exists(r, "platform-apps/b/values.yaml") {
		t.Error("only the given template is rendered")
	}
	if _, err := r.Render([]string{"platform-apps/missing.yaml.tmpl"}); err == nil {
		t.Error("a missing template must fail")
	}
}

// bootstrapped is a repository whose target values were rendered from the template and had apps excluded.
func bootstrapped(t *testing.T, exclude []string) *Repo {
	t.Helper()
	target := TargetValuesPath("kubrix-oss-stack")
	r := checkout(t, map[string]string{TemplatePath(target): targetTemplate})
	if err := r.WriteConfig(testInput); err != nil {
		t.Fatal(err)
	}
	if err := r.RenderAll(); err != nil {
		t.Fatal(err)
	}
	if err := r.Exclude("kubrix-oss-stack", exclude); err != nil {
		t.Fatal(err)
	}
	return r
}

// ExcludedApps is the inverse of Exclude: upgrades must find again what the bootstrap left out.
func TestExcludedAppsFindsWhatExcludeLeftOut(t *testing.T) {
	for name, exclude := range map[string][]string{
		"nothing":            nil,
		"one app":            {"kargo"},
		"two apps":           {"kargo", "backstage"},
		"app of this type":   {"external-dns"},
		"all but one":        {"kargo", "external-dns", "backstage"},
		"unknown app":        {"does-not-exist"},
		"the first and last": {"traefik", "backstage"},
	} {
		t.Run(name, func(t *testing.T) {
			r := bootstrapped(t, exclude)
			got, err := r.ExcludedApps("kubrix-oss-stack")
			if err != nil {
				t.Fatal(err)
			}
			want := slices.DeleteFunc(slices.Clone(exclude), func(a string) bool { return a == "does-not-exist" })
			slices.Sort(got)
			slices.Sort(want)
			if len(got)+len(want) > 0 && !slices.Equal(got, want) {
				t.Errorf("got %v, want %v", got, want)
			}
		})
	}
}

func TestExcludeLeavesTheRestOfTheFileUntouched(t *testing.T) {
	full := bootstrapped(t, nil)
	cut := bootstrapped(t, []string{"kargo"})
	before, after := read(t, full, TargetValuesPath("kubrix-oss-stack")), read(t, cut, TargetValuesPath("kubrix-oss-stack"))
	if strings.Contains(after, "kargo") || strings.Contains(after, "sync-wave") {
		t.Errorf("the app and its settings must go:\n%s", after)
	}
	for _, keep := range []string{"values-cluster-k8s.yaml", "traefik", "external-dns", "backstage"} {
		if !strings.Contains(after, keep) {
			t.Errorf("%q must stay:\n%s", keep, after)
		}
	}
	if len(after) >= len(before) {
		t.Error("excluding must shorten the file")
	}
}

func TestExcludeWithoutAppsDoesNotTouchTheFile(t *testing.T) {
	r := checkout(t, nil)
	if err := r.Exclude("kubrix-oss-stack", nil); err != nil {
		t.Errorf("nothing to exclude, nothing to read: %v", err)
	}
	if err := r.Exclude("kubrix-oss-stack", []string{"kargo"}); err == nil {
		t.Error("excluding from a file that does not exist must fail")
	}
}

func TestExcludedAppsWithoutATemplateHasNothingToReapply(t *testing.T) {
	r := checkout(t, map[string]string{TargetValuesPath("kind"): "applications:\n  - name: traefik\n"})
	if err := r.WriteConfig(testInput); err != nil {
		t.Fatal(err)
	}
	if got, err := r.ExcludedApps("kind"); err != nil || got != nil {
		t.Errorf("%v %v", got, err)
	}
}

func TestExcludedAppsNeedsTheConfig(t *testing.T) {
	target := TargetValuesPath("kubrix-oss-stack")
	r := checkout(t, map[string]string{TemplatePath(target): targetTemplate, target: "applications: []\n"})
	if _, err := r.ExcludedApps("kubrix-oss-stack"); err == nil {
		t.Error("the template cannot be rendered without the customer config")
	}
}

func TestTemplateApps(t *testing.T) {
	kind, err := TemplateApps("values.yaml.tmpl", []byte(targetTemplate), ConfigInput{ClusterType: "kind", DNSProvider: "none"})
	if err != nil || !slices.Equal(kind, []string{"traefik", "kargo", "backstage"}) {
		t.Errorf("kind: %v %v", kind, err)
	}
	k8s, err := TemplateApps("values.yaml.tmpl", []byte(targetTemplate), ConfigInput{ClusterType: "k8s", DNSProvider: "none"})
	if err != nil || !slices.Equal(k8s, []string{"traefik", "kargo", "external-dns", "backstage"}) {
		t.Errorf("k8s: %v %v", k8s, err)
	}
	if _, err := TemplateApps("broken.yaml.tmpl", []byte("{{ .kubriX.nope"), ConfigInput{}); err == nil {
		t.Error("a template that does not parse must fail")
	}
}
