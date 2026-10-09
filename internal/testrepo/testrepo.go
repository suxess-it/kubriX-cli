// Package testrepo builds a small fake kubriX upstream git repository for tests.
package testrepo

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/suxess-it/kubrix-cli/internal/gitops"
)

const TargetType = "kubrix-oss-stack"

var v1 = map[string]string{
	"bootstrap/customer-config.yaml": "clusterType: kind\n",
	".release-please-manifest.json":  `{".":"7.0.0"}` + "\n",
	"CHANGELOG.md":                   "# Changelog\n\n## [7.0.0](https://example.com) (2026-01-01)\n\n### Features\n\n* first\n",
	"platform-apps/target-chart/values-kubrix-oss-stack.yaml.tmpl": `default:
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
`,
	"platform-apps/charts/grafana/Chart.yaml":                          "name: grafana\n",
	"platform-apps/charts/grafana/values-customer-generated.yaml.tmpl": "domain: grafana.{{ .kubriX.domain }}\n",
	"platform-apps/charts/keycloak/values-kubrix-default.yaml":         "replicas: 1\n",
}

var v11 = map[string]string{
	"bootstrap/customer-config.yaml": "clusterType: kind\ndomain: example.org\n",
	".release-please-manifest.json":  `{".":"7.1.0"}` + "\n",
	"CHANGELOG.md":                   "# Changelog\n\n## [7.1.0](https://example.com) (2026-02-01)\n\n### Features\n\n* grafana host\n\n## [7.0.0](https://example.com) (2026-01-01)\n\n### Features\n\n* first\n",
	"platform-apps/charts/grafana/values-customer-generated.yaml.tmpl": "host: grafana.{{ .kubriX.domain }}\n",
	"platform-apps/charts/kargo/values-customer-generated.yaml.tmpl":   "api: kargo.{{ .kubriX.domain }}\n",
}

var v2 = map[string]string{
	".release-please-manifest.json": `{".":"8.0.0"}` + "\n",
	"CHANGELOG.md":                  "# Changelog\n\n## [8.0.0](https://example.com) (2026-03-01)\n\n### ⚠ BREAKING CHANGES\n\n* team-onboarding is new\n\n## [7.1.0](https://example.com) (2026-02-01)\n\n### Features\n\n* grafana host\n\n## [7.0.0](https://example.com) (2026-01-01)\n\n### Features\n\n* first\n",
	"platform-apps/target-chart/values-kubrix-oss-stack.yaml.tmpl": `default:
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

  - name: team-onboarding
`,
	"platform-apps/charts/keycloak/values-kubrix-default.yaml": "replicas: 2\n",
	// grafana/ is renamed to grafana-v2/, like v7.0.0 renamed charts/vault to charts/openbao.
	"platform-apps/charts/grafana-v2/Chart.yaml":                          "name: grafana\n",
	"platform-apps/charts/grafana-v2/values-customer-generated.yaml.tmpl": "host: grafana.{{ .kubriX.domain }}\n",
}

var v2Removed = []string{"platform-apps/charts/grafana/Chart.yaml", "platform-apps/charts/grafana/values-customer-generated.yaml.tmpl"}

// Upstream creates the fake upstream with annotated tags v7.0.0, v7.1.0 and v8.0.0 on main.
func Upstream(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	g := Git(t, dir)
	Run(t, g, "init", "-q", "-b", "main")
	for _, release := range []struct {
		tag     string
		files   map[string]string
		removed []string
	}{{"v7.0.0", v1, nil}, {"v7.1.0", v11, nil}, {"v8.0.0", v2, v2Removed}} {
		Write(t, dir, release.files)
		for _, rel := range release.removed {
			Run(t, g, "rm", "-q", rel)
		}
		Run(t, g, "add", "-A")
		Run(t, g, "commit", "-q", "-m", "release "+release.tag)
		Run(t, g, "tag", "-a", release.tag, "-m", release.tag)
	}
	return dir
}

// Bare creates an empty bare repository to push to.
func Bare(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	Run(t, Git(t, dir), "init", "-q", "--bare", "-b", "main")
	return dir
}

// KubrixRepo returns the checkout of the kubriX platform repository named by $KUBRIX_REPO.
// Tests that check this CLI against the real repository are skipped when it is not set.
func KubrixRepo(t *testing.T) string {
	t.Helper()
	dir := os.Getenv("KUBRIX_REPO")
	if dir == "" {
		t.Skip("KUBRIX_REPO not set; skipping check against the kubriX repository")
	}
	return dir
}

// Git binds git to dir, ignoring the developer's global git config (e.g. commit signing).
func Git(t *testing.T, dir string) *gitops.Git {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	return gitops.New(dir, "", "test", "test@example.com")
}

func Run(t *testing.T, g *gitops.Git, args ...string) string {
	t.Helper()
	out, err := g.Run(context.Background(), args...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func Write(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func Read(t *testing.T, dir, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// Clone checks out a (bare) repository into a fresh directory.
func Clone(t *testing.T, url string) string {
	t.Helper()
	dir := t.TempDir()
	Run(t, Git(t, dir), "clone", "-q", url, ".")
	return dir
}
