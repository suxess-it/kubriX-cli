package platformrepo

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/suxess-it/kubrix-cli/internal/testrepo"
)

func testConfig(t *testing.T, clusterType string) map[string]any {
	t.Helper()
	cfg, err := ParseConfig(CustomerConfig(ConfigInput{
		ClusterType: clusterType,
		DNSProvider: "none",
		Domain:      "127-0-0-1.nip.io",
		Repo:        "https://github.com/my-org/kubrix-demo.git",
		GitUser:     "octocat",
	}))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// TestCustomerConfigMatchesInstaller fails when install-platform.sh's customer-config heredoc changes.
func TestCustomerConfigMatchesInstaller(t *testing.T) {
	script, err := os.ReadFile(filepath.Join(testrepo.KubrixRepo(t), "install-platform.sh"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?s)cat << EOF > bootstrap/customer-config.yaml\n(.*?)\nEOF`).FindSubmatch(script)
	if m == nil {
		t.Fatal("customer-config heredoc not found in install-platform.sh")
	}
	keys := func(s string) []string {
		var out []string
		for _, line := range strings.Split(strings.TrimSpace(s), "\n") {
			k, _, _ := strings.Cut(line, ":")
			out = append(out, k)
		}
		return out
	}
	want := keys(string(m[1]))
	got := keys(string(CustomerConfig(ConfigInput{})))
	if !slices.Equal(got, want) {
		t.Fatalf("CustomerConfig keys %v, installer writes %v", got, want)
	}
}

func TestCustomerConfigRepoParts(t *testing.T) {
	cfg := testConfig(t, "kind")
	if cfg["gitRepoOrg"] != "my-org" || cfg["gitRepoName"] != "kubrix-demo" {
		t.Fatalf("got org=%v name=%v", cfg["gitRepoOrg"], cfg["gitRepoName"])
	}
	if cfg["securityStrict"] != false || cfg["metalLbIp"] != nil {
		t.Fatalf("installer defaults not reproduced: %v", cfg)
	}
}

// TestAllTemplatesRender fails when a template uses a gomplate function the renderer does not provide, or an unknown key.
func TestAllTemplatesRender(t *testing.T) {
	repoRoot := testrepo.KubrixRepo(t)
	tmpls, err := templates(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(tmpls) < 20 {
		t.Fatalf("expected the repo's templates, found %d", len(tmpls))
	}
	for _, clusterType := range []string{"kind", "k8s"} {
		cfg := testConfig(t, clusterType)
		for _, rel := range tmpls {
			data, err := os.ReadFile(filepath.Join(repoRoot, rel))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Template(rel, data, cfg); err != nil {
				t.Errorf("%s (%s): %v", rel, clusterType, err)
			}
		}
	}
}

func ossApps(t *testing.T, clusterType string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(testrepo.KubrixRepo(t), "platform-apps/target-chart/values-kubrix-oss-stack.yaml.tmpl"))
	if err != nil {
		t.Fatal(err)
	}
	out, err := Template("oss", data, testConfig(t, clusterType))
	if err != nil {
		t.Fatal(err)
	}
	apps, err := Apps(out)
	if err != nil {
		t.Fatal(err)
	}
	return apps
}

func TestOSSStackAppsPerClusterType(t *testing.T) {
	kind, k8s := ossApps(t, "kind"), ossApps(t, "k8s")
	for _, app := range []string{"external-dns", "falco", "kyverno", "velero", "minio"} {
		if slices.Contains(kind, app) {
			t.Errorf("%s must not be installed on kind", app)
		}
		if !slices.Contains(k8s, app) {
			t.Errorf("%s must be installed on k8s", app)
		}
	}
	for _, app := range []string{"traefik", "argocd", "keycloak", "backstage"} {
		if !slices.Contains(kind, app) || !slices.Contains(k8s, app) {
			t.Errorf("%s must be installed everywhere", app)
		}
	}
}

func TestApplyExcludes(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(testrepo.KubrixRepo(t), "platform-apps/target-chart/values-kubrix-oss-stack.yaml.tmpl"))
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := Template("oss", data, testConfig(t, "k8s"))
	if err != nil {
		t.Fatal(err)
	}
	before, _ := Apps(rendered)
	out := ApplyExcludes(rendered, []string{"kargo", "external-dns", "team-onboarding"})
	after, err := Apps(out)
	if err != nil {
		t.Fatalf("result is not valid YAML: %v", err)
	}
	want := slices.DeleteFunc(slices.Clone(before), func(a string) bool {
		return a == "kargo" || a == "external-dns" || a == "team-onboarding"
	})
	if !slices.Equal(after, want) {
		t.Fatalf("got %v, want %v", after, want)
	}
	if !strings.HasPrefix(string(out), string(rendered[:strings.Index(string(rendered), "applications:")])) {
		t.Error("content before applications must stay untouched")
	}
	if got := ApplyExcludes(rendered, nil); string(got) != string(rendered) {
		t.Error("no excludes must not change the file")
	}
}

func TestRenderTree(t *testing.T) {
	root := t.TempDir()
	tmpl := filepath.Join(root, "platform-apps", "x", "values-customer-generated.yaml.tmpl")
	if err := os.MkdirAll(filepath.Dir(tmpl), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tmpl, []byte("domain: {{ .kubriX.domain }}\nraw: {{`{{ .Values.x }}`}}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := renderTree(root, testConfig(t, "kind")); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(OutputPath(tmpl))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "domain: 127-0-0-1.nip.io\nraw: {{ .Values.x }}\n" {
		t.Fatalf("got %q", got)
	}
}

func TestGomplateStringsTrimSuffix(t *testing.T) {
	out, err := Template("v6", []byte(`url: {{ .kubriX.gitRepo | strings.TrimSuffix ".git" }}/blob/main`), map[string]any{"gitRepo": "https://github.com/acme/demo.git"})
	if err != nil || string(out) != "url: https://github.com/acme/demo/blob/main" {
		t.Fatalf("got %q %v", out, err)
	}
}
