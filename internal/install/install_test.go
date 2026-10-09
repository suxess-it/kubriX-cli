package install_test

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/suxess-it/kubrix-cli/internal/git"
	"github.com/suxess-it/kubrix-cli/internal/git/gittest"
	"github.com/suxess-it/kubrix-cli/internal/install"
	"github.com/suxess-it/kubrix-cli/internal/install/installtest"
	"github.com/suxess-it/kubrix-cli/internal/platforminstaller"
	"github.com/suxess-it/kubrix-cli/internal/state"
	"github.com/suxess-it/kubrix-cli/internal/testrepo"
	"github.com/suxess-it/kubrix-cli/internal/ui/uitest"
)

type fixture struct {
	host     *gittest.Host
	upstream installtest.Upstream
	customer string
	cluster  *installtest.Cluster
	progress *uitest.UI
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	customer := testrepo.Bare(t)
	host := gittest.New("octocat", "acme")
	host.URLs["acme/demo"] = customer
	return &fixture{
		host:     host,
		upstream: installtest.Upstream{URL: testrepo.Upstream(t), AppList: []string{"traefik", "external-dns", "backstage", "kargo"}, Images: []string{"v7.0.0", "feat-x"}},
		customer: customer,
		cluster:  &installtest.Cluster{Hosts: []string{"argocd.example.com"}, Secrets: map[string]string{"argocd/argocd-initial-admin-secret/password": "pw"}},
		progress: uitest.New(nil),
	}
}

func (f *fixture) deps() install.Deps { return install.Deps{Host: f.host, Upstream: f.upstream} }

func (f *fixture) selection(ref string, excluded ...string) state.Installation {
	return state.Installation{
		Kind: state.KindDemo, Org: "acme", Repo: "demo", GitUserName: "octocat", TargetType: testrepo.TargetType,
		UpstreamRepo: platforminstaller.DefaultUpstream, UpstreamBranch: ref, ClusterName: "kubrix-demo",
		Domain: "127-0-0-1.nip.io", DNSProvider: "none", ExcludedApps: excluded,
	}
}

func (f *fixture) kind(existing, reuse bool) (*install.KindTarget, *installtest.KindClusters) {
	clusters := &installtest.KindClusters{Existing: existing, Cluster: f.cluster}
	return &install.KindTarget{Clusters: clusters, Name: "kubrix-demo", Reuse: reuse, CADir: ""}, clusters
}

func TestReleaseOnFreshRepoIsBootstrappedByTheCLI(t *testing.T) {
	f := newFixture(t)
	target, clusters := f.kind(false, false)
	target.CADir = t.TempDir()
	f.cluster.Secrets["cert-manager/kind-kubrix-ca-key-pair/tls.crt"] = "CERT"

	plan, err := install.Inspect(context.Background(), f.deps(), target, f.selection("v7.0.0"), f.progress)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Fresh || plan.Apps == nil || plan.ImageFallback != "" {
		t.Fatalf("plan %+v", plan)
	}
	if !slices.Equal(plan.Apps.Required, []string{"traefik"}) || !slices.Equal(plan.Apps.Optional, []string{"external-dns", "backstage", "kargo"}) {
		t.Errorf("apps %+v", plan.Apps)
	}
	plan.Select([]string{"external-dns", "backstage"})

	var saved []state.Installation
	if err := plan.Run(context.Background(), f.progress, func(s state.Installation) { saved = append(saved, s) }); err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(f.host.Created, []string{"acme/demo"}) {
		t.Errorf("created repos %v", f.host.Created)
	}
	if !slices.Equal(clusters.Calls, []string{"create kubrix-demo"}) {
		t.Errorf("cluster calls %v", clusters.Calls)
	}
	dir := testrepo.Clone(t, f.customer)
	if !strings.Contains(testrepo.Read(t, dir, "bootstrap/customer-config.yaml"), "clusterType: kind") {
		t.Error("the CLI must have pushed the rendered repository")
	}
	s := f.cluster.Secret
	if s["KUBRIX_BOOTSTRAP"] != "false" || s["KUBRIX_APP_EXCLUDE"] != "kargo" || s["KUBRIX_CLUSTER_TYPE"] != "kind" {
		t.Errorf("installer must only install and skip waiting for excluded apps: %v", s)
	}
	if len(saved) != 1 || !slices.Equal(saved[0].ExcludedApps, []string{"kargo"}) {
		t.Errorf("saved %+v", saved)
	}
	if !strings.Contains(f.cluster.Manifest, ":v7.0.0") {
		t.Errorf("manifest %q must use the release's installer image", f.cluster.Manifest)
	}
	ca, err := os.ReadFile(filepath.Join(target.CADir, "kind-ca.crt"))
	if err != nil || string(ca) != "CERT" {
		t.Errorf("kind CA %q %v", ca, err)
	}
	if !slices.Contains(f.progress.Infos(), "Argo CD login: admin / pw") || !slices.Contains(f.progress.Infos(), "  https://argocd.example.com") {
		t.Errorf("access info %v", f.progress.Infos())
	}
}

func TestBranchOnFreshRepoIsBootstrappedByTheInstaller(t *testing.T) {
	f := newFixture(t)
	target, _ := f.kind(false, false)
	plan, err := install.Inspect(context.Background(), f.deps(), target, f.selection("feat/x"), f.progress)
	if err != nil {
		t.Fatal(err)
	}
	plan.Select([]string{"external-dns", "backstage", "kargo"})
	if err := plan.Run(context.Background(), f.progress, func(state.Installation) {}); err != nil {
		t.Fatal(err)
	}
	if s := f.cluster.Secret; s["KUBRIX_BOOTSTRAP"] != "true" || s["KUBRIX_UPSTREAM_BRANCH"] != "feat/x" {
		t.Errorf("secret %v", s)
	}
	if !slices.Equal(f.host.Created, []string{"acme/demo"}) {
		t.Errorf("created repos %v", f.host.Created)
	}
	if _, err := os.Stat(filepath.Join(f.customer, "HEAD")); err != nil {
		t.Fatal(err)
	}
	if out := testrepo.Run(t, testrepo.Git(t, f.customer), "branch", "--list"); out != "" {
		t.Errorf("the CLI must not push for branches, got %q", out)
	}
	if !strings.Contains(f.cluster.Manifest, ":feat-x") {
		t.Errorf("manifest %q", f.cluster.Manifest)
	}
}

func TestRepoWithContentIsNeverBootstrapped(t *testing.T) {
	f := newFixture(t)
	f.host.Repos["acme/demo"] = git.RepoNonEmpty
	target, _ := f.kind(false, false)
	plan, err := install.Inspect(context.Background(), f.deps(), target, f.selection("v7.0.0", "kargo"), f.progress)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Fresh || plan.Apps != nil {
		t.Fatalf("plan %+v", plan)
	}
	var saved []state.Installation
	if err := plan.Run(context.Background(), f.progress, func(s state.Installation) { saved = append(saved, s) }); err != nil {
		t.Fatal(err)
	}
	s := f.cluster.Secret
	if s["KUBRIX_BOOTSTRAP"] != "false" {
		t.Errorf("secret %v", s)
	}
	if _, ok := s["KUBRIX_APP_EXCLUDE"]; ok {
		t.Error("excludes only apply to a fresh bootstrap")
	}
	if len(f.host.Created) != 0 || saved[0].ExcludedApps[0] != "kargo" {
		t.Errorf("created %v saved %+v", f.host.Created, saved)
	}
}

func TestMissingInstallerImageFallsBackToLatest(t *testing.T) {
	f := newFixture(t)
	f.host.Repos["acme/demo"] = git.RepoNonEmpty
	target, _ := f.kind(false, false)
	plan, err := install.Inspect(context.Background(), f.deps(), target, f.selection("fix/y"), f.progress)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.ImageFallback, "fix-y") || !strings.Contains(plan.ImageFallback, "workflow") {
		t.Errorf("fallback %q", plan.ImageFallback)
	}
	if err := plan.Run(context.Background(), f.progress, func(state.Installation) {}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.cluster.Manifest, ":latest") {
		t.Errorf("manifest %q", f.cluster.Manifest)
	}
}

func TestReusedKindClusterReplacesTheOldJob(t *testing.T) {
	f := newFixture(t)
	f.host.Repos["acme/demo"] = git.RepoNonEmpty
	target, clusters := f.kind(true, true)
	plan, err := install.Inspect(context.Background(), f.deps(), target, f.selection("main"), f.progress)
	if err != nil {
		t.Fatal(err)
	}
	if err := plan.Run(context.Background(), f.progress, func(state.Installation) {}); err != nil {
		t.Fatal(err)
	}
	if len(clusters.Calls) != 0 || !slices.Contains(f.cluster.Calls, "delete job") {
		t.Errorf("cluster calls %v, kube calls %v", clusters.Calls, f.cluster.Calls)
	}

	f = newFixture(t)
	f.host.Repos["acme/demo"] = git.RepoNonEmpty
	target, clusters = f.kind(true, false)
	plan, err = install.Inspect(context.Background(), f.deps(), target, f.selection("main"), f.progress)
	if err != nil {
		t.Fatal(err)
	}
	if err := plan.Run(context.Background(), f.progress, func(state.Installation) {}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(clusters.Calls, []string{"delete kubrix-demo", "create kubrix-demo"}) {
		t.Errorf("a cluster that is not reused is recreated: %v", clusters.Calls)
	}
}

func TestExistingClusterWithManualDNSLeavesOutExternalDNS(t *testing.T) {
	f := newFixture(t)
	f.cluster.LBAddr = "203.0.113.7"
	target := &install.ClusterTarget{Kube: f.cluster, Domain: "platform.example.com", DNSProvider: install.DNSManual}
	sel := f.selection("v7.0.0")
	sel.Kind, sel.Context, sel.Domain, sel.CloudProvider = state.Cluster, "prod", "platform.example.com", "on-prem"

	plan, err := install.Inspect(context.Background(), f.deps(), target, sel, f.progress)
	if err != nil {
		t.Fatal(err)
	}
	plan.Select([]string{"external-dns", "backstage", "kargo"})
	var saved []state.Installation
	if err := plan.Run(context.Background(), f.progress, func(s state.Installation) { saved = append(saved, s) }); err != nil {
		t.Fatal(err)
	}
	s := f.cluster.Secret
	if s["KUBRIX_CLUSTER_TYPE"] != "k8s" || s["KUBRIX_APP_EXCLUDE"] != "external-dns" || s["KUBRIX_CLOUD_PROVIDER"] != "on-prem" {
		t.Errorf("secret %v", s)
	}
	if !slices.Equal(saved[0].ExcludedApps, []string{"external-dns"}) {
		t.Errorf("saved %+v", saved)
	}
	if !slices.ContainsFunc(f.progress.Infos(), func(m string) bool { return strings.HasPrefix(m, "external-dns is not installed") }) {
		t.Errorf("infos %v", f.progress.Infos())
	}
	if len(f.cluster.DNSData) != 0 {
		t.Errorf("no DNS credentials without a provider: %v", f.cluster.DNSData)
	}
	if !slices.ContainsFunc(f.progress.Warns(), func(m string) bool { return strings.Contains(m, "*.platform.example.com pointing to 203.0.113.7") }) {
		t.Errorf("warns %v", f.progress.Warns())
	}
	dir := testrepo.Clone(t, f.customer)
	if !strings.Contains(testrepo.Read(t, dir, "bootstrap/customer-config.yaml"), "clusterType: k8s") {
		t.Error("the repository must be rendered for k8s")
	}
}

func TestExistingClusterWithDNSProviderCreatesCredentials(t *testing.T) {
	f := newFixture(t)
	f.host.Repos["acme/demo"] = git.RepoNonEmpty
	target := &install.ClusterTarget{Kube: f.cluster, Domain: "platform.example.com", DNSProvider: "cloudflare", DNS: install.DNSCredentials{Token: "t0k"}}
	sel := f.selection("v7.0.0")
	sel.DNSProvider = "cloudflare"
	plan, err := install.Inspect(context.Background(), f.deps(), target, sel, f.progress)
	if err != nil {
		t.Fatal(err)
	}
	if err := plan.Run(context.Background(), f.progress, func(state.Installation) {}); err != nil {
		t.Fatal(err)
	}
	if got := string(f.cluster.DNSData["external-dns/cloudflare-api-key"]["apiKey"]); got != "t0k" {
		t.Errorf("dns data %v", f.cluster.DNSData)
	}
	if !slices.Contains(f.cluster.Calls, "namespace external-dns") {
		t.Errorf("calls %v", f.cluster.Calls)
	}
}

func TestExcludesAreSavedAndSent(t *testing.T) {
	f := newFixture(t)
	target, _ := f.kind(false, false)
	plan, err := install.Inspect(context.Background(), f.deps(), target, f.selection("main", "kargo"), f.progress)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(plan.Excluded(), []string{"kargo"}) || slices.Contains(plan.Apps.Selected, "kargo") {
		t.Errorf("saved exclusions must start deselected: %v %v", plan.Excluded(), plan.Apps.Selected)
	}
	want := map[string]string{"KUBRIX_APP_EXCLUDE": "kargo"}
	if err := plan.Run(context.Background(), f.progress, func(state.Installation) {}); err != nil {
		t.Fatal(err)
	}
	for k, v := range want {
		if f.cluster.Secret[k] != v {
			t.Errorf("%s = %q, want %q (secret %v)", k, f.cluster.Secret[k], v, maps.Clone(f.cluster.Secret))
		}
	}
}
