package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/suxess-it/kubrix-cli/internal/bootstrap"
	"github.com/suxess-it/kubrix-cli/internal/git"
	"github.com/suxess-it/kubrix-cli/internal/git/gittest"
	"github.com/suxess-it/kubrix-cli/internal/install"
	"github.com/suxess-it/kubrix-cli/internal/install/installtest"
	"github.com/suxess-it/kubrix-cli/internal/kindcluster"
	"github.com/suxess-it/kubrix-cli/internal/kubeconfig"
	"github.com/suxess-it/kubrix-cli/internal/platformrepo"
	"github.com/suxess-it/kubrix-cli/internal/state"
	"github.com/suxess-it/kubrix-cli/internal/testrepo"
	"github.com/suxess-it/kubrix-cli/internal/ui"
	"github.com/suxess-it/kubrix-cli/internal/ui/uitest"
	"github.com/suxess-it/kubrix-cli/internal/upgrade"
)

// world is everything outside the CLI, faked: GitHub, the kubriX source, kind, Docker, kubeconfig and a cluster.
// The user's config directory and working directory are temporary, so the user is no kubriX contributor.
type world struct {
	t         *testing.T
	host      *gittest.Host
	upstream  installtest.Upstream
	kind      *installtest.KindClusters
	cluster   *installtest.Cluster
	docker    kindcluster.Resources
	contexts  []kubeconfig.Context
	current   string
	githubErr error
	svc       services
}

func newWorld(t *testing.T) *world {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Chdir(t.TempDir())
	w := &world{
		t:    t,
		host: gittest.New("octocat", "acme", "beta"),
		upstream: installtest.Upstream{
			URL:     testrepo.Upstream(t),
			AppList: []string{"traefik", "external-dns", "backstage", "kargo"},
			Images:  []string{"v7.0.0", "v7.1.0", "v8.0.0"},
		},
		cluster: &installtest.Cluster{
			Hosts:   []string{"argocd.example.com"},
			Secrets: map[string]string{"argocd/argocd-initial-admin-secret/password": "pw"},
		},
		docker:   kindcluster.Resources{CPUs: 8, Memory: 32 << 30},
		contexts: []kubeconfig.Context{{Name: "prod", Server: "https://prod.example.com"}},
		current:  "prod",
	}
	w.host.Releases = []string{"v8.0.0", "v7.1.0", "v7.0.0"}
	w.kind = &installtest.KindClusters{Cluster: w.cluster}
	w.svc = services{
		github: func(context.Context, ui.UI) (githubAPI, string, error) {
			if w.githubErr != nil {
				return nil, "", w.githubErr
			}
			return w.host, "octocat", nil
		},
		upstream:       func(git.Host) install.Upstream { return w.upstream },
		upstreamGitURL: func(string) string { return w.upstream.URL },
		kind:           w.kind,
		docker:         func() (kindcluster.Resources, error) { return w.docker, nil },
		contexts:       func() ([]kubeconfig.Context, string, error) { return w.contexts, w.current, nil },
		cluster:        func(string) (clusterAccess, error) { return w.cluster, nil },
	}
	return w
}

// catalog reads the saved installations.
func (w *world) catalog() *state.Catalog {
	w.t.Helper()
	dir, err := state.Dir()
	if err != nil {
		w.t.Fatal(err)
	}
	return state.Open(dir)
}

// repoAt makes a platform repository that GitHub hosts at acme/<name>, bootstrapped from the release.
func (w *world) repoAt(name, release string, exclude ...string) string {
	w.t.Helper()
	bare := testrepo.Bare(w.t)
	err := bootstrap.FromRelease(context.Background(), bootstrap.Options{
		UpstreamURL: w.upstream.URL, Tag: release, CustomerURL: bare,
		Config:     platformrepo.ConfigInput{ClusterType: "k8s", DNSProvider: "none", Domain: "example.com", Repo: "https://github.com/acme/" + name + ".git", GitUser: "octocat"},
		TargetType: testrepo.TargetType, Exclude: exclude,
	})
	if err != nil {
		w.t.Fatal(err)
	}
	w.host.Repos["acme/"+name] = git.RepoNonEmpty
	w.host.URLs["acme/"+name] = bare
	return bare
}

// emptyRepoAt makes an empty bare repository hosted at acme/<name>, to be created and bootstrapped.
func (w *world) emptyRepoAt(name string) string {
	w.t.Helper()
	bare := testrepo.Bare(w.t)
	w.host.URLs["acme/"+name] = bare
	return bare
}

func demoAnswers(extra map[string]any) map[string]any {
	a := map[string]any{"repo": "demo"}
	for k, v := range extra {
		a[k] = v
	}
	return a
}

func TestRunDemoBootstrapsAReleaseOnAFreshKindCluster(t *testing.T) {
	w := newWorld(t)
	bare := w.emptyRepoAt("demo")
	w.cluster.Secrets["cert-manager/kind-kubrix-ca-key-pair/tls.crt"] = "CERT"
	u := uitest.New(demoAnswers(nil))

	if err := runDemo(context.Background(), u, w.svc); err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(w.host.Created, []string{"acme/demo"}) || !slices.Equal(w.kind.Calls, []string{"create kubrix-demo"}) {
		t.Errorf("created %v kind %v", w.host.Created, w.kind.Calls)
	}
	if s := w.cluster.Secret; s["KUBRIX_CLUSTER_TYPE"] != "kind" || s["KUBRIX_BOOTSTRAP"] != "false" || s["KUBRIX_UPSTREAM_BRANCH"] != "v8.0.0" || s["KUBRIX_DOMAIN"] != kindDomain {
		t.Errorf("installer secret %v", s)
	}
	if cfg := testrepo.Read(t, testrepo.Clone(t, bare), platformrepo.ConfigPath); !strings.Contains(cfg, "clusterType: kind") {
		t.Errorf("the repository was not bootstrapped for kind:\n%s", cfg)
	}
	saved, ok := w.catalog().Find("kubrix-demo")
	if !ok || saved.Org != "acme" || saved.Repo != "demo" || saved.Kind != state.KindDemo || saved.GitHost != git.GitHubName {
		t.Errorf("saved %+v", saved)
	}
	dir, _ := state.Dir()
	if ca, err := os.ReadFile(filepath.Join(dir, "kind-ca.crt")); err != nil || string(ca) != "CERT" {
		t.Errorf("kind CA %q %v", ca, err)
	}
	if !slices.Contains(u.OKs(), "GitHub: authenticated as octocat") || !slices.Contains(u.OKs(), "kubriX installed") || !slices.Contains(u.Infos(), "Argo CD login: admin / pw") {
		t.Errorf("oks %v infos %v", u.OKs(), u.Infos())
	}
	if unused := u.Unused(); len(unused) != 0 {
		t.Errorf("unused answers %v", unused)
	}
}

func TestRunDemoOmitsTheAppsTheUserDeselects(t *testing.T) {
	w := newWorld(t)
	w.emptyRepoAt("demo")
	u := uitest.New(demoAnswers(map[string]any{"apps": []string{"external-dns", "backstage"}}))
	if err := runDemo(context.Background(), u, w.svc); err != nil {
		t.Fatal(err)
	}
	if got := w.cluster.Secret["KUBRIX_APP_EXCLUDE"]; got != "kargo" {
		t.Errorf("excluded %q", got)
	}
	if saved, _ := w.catalog().Find("kubrix-demo"); !slices.Equal(saved.ExcludedApps, []string{"kargo"}) {
		t.Errorf("saved %+v", saved)
	}
}

func TestRunDemoFromABranchLeavesBootstrappingToTheInstaller(t *testing.T) {
	w := newWorld(t)
	w.emptyRepoAt("demo")
	w.upstream.Images = []string{"feat-x"}
	u := uitest.New(demoAnswers(map[string]any{"version": otherBranch, "branch": "feat/x"}))
	if err := runDemo(context.Background(), u, w.svc); err != nil {
		t.Fatal(err)
	}
	if s := w.cluster.Secret; s["KUBRIX_BOOTSTRAP"] != "true" || s["KUBRIX_UPSTREAM_BRANCH"] != "feat/x" {
		t.Errorf("secret %v", s)
	}
	if !strings.Contains(w.cluster.Manifest, ":feat-x") {
		t.Errorf("manifest %q", w.cluster.Manifest)
	}
}

func TestRunDemoReusesOrRecreatesAnExistingKindCluster(t *testing.T) {
	const ask = `kind cluster "kubrix-demo" already exists`
	w := newWorld(t)
	w.emptyRepoAt("demo")
	w.kind.Existing = true
	reuse := uitest.New(demoAnswers(map[string]any{ask: "reuse"}))
	if err := runDemo(context.Background(), reuse, w.svc); err != nil {
		t.Fatal(err)
	}
	if len(w.kind.Calls) != 0 || !slices.Contains(w.cluster.Calls, "delete job") {
		t.Errorf("reusing keeps the cluster and replaces the old Job: kind %v cluster %v", w.kind.Calls, w.cluster.Calls)
	}

	w = newWorld(t)
	w.emptyRepoAt("demo")
	w.kind.Existing = true
	if err := runDemo(context.Background(), uitest.New(demoAnswers(nil)), w.svc); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(w.kind.Calls, []string{"delete kubrix-demo", "create kubrix-demo"}) {
		t.Errorf("recreating is the default: %v", w.kind.Calls)
	}
}

func TestRunDemoRerunsASavedDemoWithoutBootstrapping(t *testing.T) {
	w := newWorld(t)
	w.repoAt("old", "v7.0.0")
	saved := state.Installation{Kind: state.KindDemo, ClusterName: "kubrix-demo", Org: "acme", Repo: "old", GitUserName: "octocat", TargetType: defaultTarget, UpstreamRepo: defaultUpstream, UpstreamBranch: "v7.0.0", Domain: kindDomain, DNSProvider: "none"}
	if err := w.catalog().Record(saved); err != nil {
		t.Fatal(err)
	}
	w.kind.Existing = true
	u := uitest.New(map[string]any{"Which demo?": "kubrix-demo", `kind cluster "kubrix-demo" already exists`: "reuse"})
	if err := runDemo(context.Background(), u, w.svc); err != nil {
		t.Fatal(err)
	}
	if len(w.host.Created) != 0 || w.cluster.Secret["KUBRIX_BOOTSTRAP"] != "false" {
		t.Errorf("created %v secret %v", w.host.Created, w.cluster.Secret)
	}
	if _, ok := w.cluster.Secret["KUBRIX_APP_EXCLUDE"]; ok {
		t.Error("a repository with content is not bootstrapped, so nothing is excluded")
	}
	if !slices.Contains(u.Asked(), "acme/old already has content. Reinstall from it?") {
		t.Errorf("asked %v", u.Asked())
	}
}

func TestRunDemoStopsWhenDockerHasTooLittle(t *testing.T) {
	w := newWorld(t)
	w.emptyRepoAt("demo")
	w.docker = kindcluster.Resources{CPUs: 2, Memory: 4 << 30}
	u := uitest.New(demoAnswers(nil))
	if err := runDemo(context.Background(), u, w.svc); !errors.Is(err, ui.ErrAborted) {
		t.Fatalf("got %v", err)
	}
	if len(w.host.Created) != 0 || len(w.kind.Calls) != 0 || w.cluster.Secret != nil {
		t.Errorf("nothing may happen before the user agrees: created %v kind %v", w.host.Created, w.kind.Calls)
	}
	if warns := u.Warns(); len(warns) != 1 || !strings.Contains(warns[0], "2 CPUs") {
		t.Errorf("warns %v", warns)
	}

	w = newWorld(t)
	w.emptyRepoAt("demo")
	w.docker = kindcluster.Resources{CPUs: 2, Memory: 4 << 30}
	agreed := uitest.New(demoAnswers(map[string]any{"Continue anyway?": true}))
	if err := runDemo(context.Background(), agreed, w.svc); err != nil {
		t.Fatal(err)
	}
	if len(w.kind.Calls) != 1 {
		t.Errorf("kind %v", w.kind.Calls)
	}
}

func TestRunDemoFailures(t *testing.T) {
	t.Run("github", func(t *testing.T) {
		w := newWorld(t)
		w.githubErr = errors.New("bad token")
		if err := runDemo(context.Background(), uitest.New(nil), w.svc); err == nil || !strings.Contains(err.Error(), "bad token") {
			t.Errorf("got %v", err)
		}
		if len(w.kind.Calls) != 0 {
			t.Error("nothing may happen without GitHub")
		}
	})
	t.Run("docker", func(t *testing.T) {
		w := newWorld(t)
		w.svc.docker = func() (kindcluster.Resources, error) {
			return kindcluster.Resources{}, errors.New("docker is not reachable")
		}
		if err := runDemo(context.Background(), uitest.New(nil), w.svc); err == nil || !strings.Contains(err.Error(), "docker is not reachable") {
			t.Errorf("got %v", err)
		}
	})
	t.Run("cancelled form", func(t *testing.T) {
		w := newWorld(t)
		if err := runDemo(context.Background(), uitest.New(map[string]any{"repo": uitest.Abort}), w.svc); !errors.Is(err, ui.ErrAborted) {
			t.Errorf("got %v", err)
		}
		if len(w.host.Created) != 0 || len(w.kind.Calls) != 0 {
			t.Error("a cancelled form changes nothing")
		}
	})
	t.Run("invalid repository name", func(t *testing.T) {
		w := newWorld(t)
		var rejected *uitest.AnswerError
		if err := runDemo(context.Background(), uitest.New(demoAnswers(map[string]any{"repo": "not a name"})), w.svc); !errors.As(err, &rejected) {
			t.Errorf("got %v", err)
		}
	})
	t.Run("installer job fails", func(t *testing.T) {
		w := newWorld(t)
		w.emptyRepoAt("demo")
		w.cluster.JobFails = true
		u := uitest.New(demoAnswers(nil))
		if err := runDemo(context.Background(), u, w.svc); err == nil || !strings.Contains(err.Error(), "install job failed") {
			t.Fatalf("got %v", err)
		}
		if _, ok := w.catalog().Find("kubrix-demo"); !ok {
			t.Error("a failed install is still saved, so 'kubrix demo delete' can clean it up")
		}
		if slices.Contains(u.Infos(), "Argo CD login: admin / pw") {
			t.Error("no access info for a failed install")
		}
	})
	t.Run("unreadable saved installations", func(t *testing.T) {
		w := newWorld(t)
		w.emptyRepoAt("demo")
		dir, _ := state.Dir()
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(dir, "installations.json")
		if err := os.WriteFile(file, []byte("{broken"), 0o600); err != nil {
			t.Fatal(err)
		}
		u := uitest.New(demoAnswers(nil))
		if err := runDemo(context.Background(), u, w.svc); err != nil {
			t.Fatalf("the demo still runs: %v", err)
		}
		if data, _ := os.ReadFile(file); string(data) != "{broken" {
			t.Errorf("the unreadable file must not be overwritten: %q", data)
		}
		if len(u.Warns()) < 2 {
			t.Errorf("the user is told it was ignored and that nothing was saved: %v", u.Warns())
		}
	})
}

func installAnswers(extra map[string]any) map[string]any {
	a := map[string]any{"repo": "platform", "domain": "platform.example.com", "dns-token": "t0k", "confirm-context": "prod"}
	for k, v := range extra {
		a[k] = v
	}
	return a
}

func TestRunInstallBootstrapsAReleaseOntoAnExistingCluster(t *testing.T) {
	w := newWorld(t)
	bare := w.emptyRepoAt("platform")
	u := uitest.New(installAnswers(nil))
	if err := runInstall(context.Background(), u, w.svc); err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(w.host.Created, []string{"acme/platform"}) {
		t.Errorf("created %v", w.host.Created)
	}
	if s := w.cluster.Secret; s["KUBRIX_CLUSTER_TYPE"] != "k8s" || s["KUBRIX_DOMAIN"] != "platform.example.com" || s["KUBRIX_DNS_PROVIDER"] != "cloudflare" || s["KUBRIX_CLOUD_PROVIDER"] != "on-prem" || s["KUBRIX_UPSTREAM_BRANCH"] != "v8.0.0" {
		t.Errorf("installer secret %v", s)
	}
	if got := string(w.cluster.DNSData["external-dns/cloudflare-api-key"]["apiKey"]); got != "t0k" {
		t.Errorf("DNS credentials %v", w.cluster.DNSData)
	}
	if cfg := testrepo.Read(t, testrepo.Clone(t, bare), platformrepo.ConfigPath); !strings.Contains(cfg, "clusterType: k8s") || !strings.Contains(cfg, "domain: platform.example.com") {
		t.Errorf("the repository was not bootstrapped for this cluster:\n%s", cfg)
	}
	saved, ok := w.catalog().Find("prod")
	if !ok || !saved.IsCluster() || saved.Repo != "platform" || saved.Domain != "platform.example.com" || saved.DNSProvider != "cloudflare" {
		t.Errorf("saved %+v", saved)
	}
	if !slices.Contains(u.Infos(), "  https://argocd.example.com") {
		t.Errorf("infos %v", u.Infos())
	}
	if unused := u.Unused(); len(unused) != 0 {
		t.Errorf("unused answers %v", unused)
	}
}

func TestRunInstallWithManualDNSLeavesOutExternalDNSAndTellsTheRecord(t *testing.T) {
	w := newWorld(t)
	w.emptyRepoAt("platform")
	w.cluster.LBAddr = "203.0.113.7"
	answers := installAnswers(map[string]any{"dns": dnsManual})
	delete(answers, "dns-token")
	u := uitest.New(answers)
	if err := runInstall(context.Background(), u, w.svc); err != nil {
		t.Fatal(err)
	}
	if s := w.cluster.Secret; s["KUBRIX_DNS_PROVIDER"] != "none" || !strings.Contains(s["KUBRIX_APP_EXCLUDE"], "external-dns") {
		t.Errorf("secret %v", s)
	}
	if len(w.cluster.DNSData) != 0 {
		t.Errorf("no DNS credentials without a provider: %v", w.cluster.DNSData)
	}
	if !slices.ContainsFunc(u.Warns(), func(m string) bool { return strings.Contains(m, "*.platform.example.com pointing to 203.0.113.7") }) {
		t.Errorf("warns %v", u.Warns())
	}
}

func TestRunInstallNeedsTheContextToBeConfirmed(t *testing.T) {
	w := newWorld(t)
	w.emptyRepoAt("platform")
	var rejected *uitest.AnswerError
	if err := runInstall(context.Background(), uitest.New(installAnswers(map[string]any{"confirm-context": "staging"})), w.svc); !errors.As(err, &rejected) {
		t.Errorf("got %v", err)
	}
	if len(w.host.Created) != 0 || w.cluster.Secret != nil {
		t.Error("nothing may be created before the context is confirmed")
	}
	if err := runInstall(context.Background(), uitest.New(installAnswers(map[string]any{"confirm-context": uitest.Abort})), w.svc); !errors.Is(err, ui.ErrAborted) {
		t.Errorf("cancelling: %v", err)
	}
}

func TestRunInstallPicksTheContext(t *testing.T) {
	w := newWorld(t)
	w.emptyRepoAt("platform")
	w.contexts = []kubeconfig.Context{{Name: "prod", Server: "https://prod"}, {Name: "staging", Server: "https://staging"}}
	u := uitest.New(installAnswers(map[string]any{"Which cluster?": "staging", "confirm-context": "staging"}))
	if err := runInstall(context.Background(), u, w.svc); err != nil {
		t.Fatal(err)
	}
	if _, ok := w.catalog().Find("staging"); !ok {
		t.Error("staging was installed")
	}
}

func TestRunInstallRefusesWhatCannotBeInstalledInto(t *testing.T) {
	t.Run("no contexts", func(t *testing.T) {
		w := newWorld(t)
		w.contexts = nil
		if err := runInstall(context.Background(), uitest.New(nil), w.svc); err == nil || !strings.Contains(err.Error(), "no kubeconfig contexts") {
			t.Errorf("got %v", err)
		}
	})
	t.Run("kind cluster", func(t *testing.T) {
		w := newWorld(t)
		w.contexts = []kubeconfig.Context{{Name: "kind-demo", Server: "https://127.0.0.1:1"}}
		w.current = "kind-demo"
		if err := runInstall(context.Background(), uitest.New(nil), w.svc); err == nil || !strings.Contains(err.Error(), "use 'kubrix demo'") {
			t.Errorf("got %v", err)
		}
	})
	t.Run("not cluster-admin", func(t *testing.T) {
		w := newWorld(t)
		w.cluster.NotAdmin = true
		if err := runInstall(context.Background(), uitest.New(nil), w.svc); err == nil || !strings.Contains(err.Error(), "cluster-admin") {
			t.Errorf("got %v", err)
		}
		if len(w.host.Created) != 0 {
			t.Error("nothing is created on a cluster the user cannot administer")
		}
	})
	t.Run("cluster cannot be reached", func(t *testing.T) {
		w := newWorld(t)
		w.svc.cluster = func(string) (clusterAccess, error) { return nil, errors.New("no route to host") }
		if err := runInstall(context.Background(), uitest.New(nil), w.svc); err == nil || !strings.Contains(err.Error(), "no route to host") {
			t.Errorf("got %v", err)
		}
	})
	t.Run("github", func(t *testing.T) {
		w := newWorld(t)
		w.githubErr = errors.New("bad token")
		if err := runInstall(context.Background(), uitest.New(nil), w.svc); err == nil || !strings.Contains(err.Error(), "bad token") {
			t.Errorf("got %v", err)
		}
	})
}

func TestRunInstallAsksBeforeInstallingWithoutAStorageClass(t *testing.T) {
	w := newWorld(t)
	w.emptyRepoAt("platform")
	w.cluster.NoStorageClass = true
	if err := runInstall(context.Background(), uitest.New(installAnswers(nil)), w.svc); !errors.Is(err, ui.ErrAborted) {
		t.Fatalf("declining is the default: %v", err)
	}
	if w.cluster.Secret != nil {
		t.Error("nothing is installed")
	}
	agreed := installAnswers(map[string]any{"Continue without a default StorageClass?": true})
	if err := runInstall(context.Background(), uitest.New(agreed), w.svc); err != nil {
		t.Fatal(err)
	}
}

func TestRunInstallRerunsTheSavedInstallationOfTheContext(t *testing.T) {
	w := newWorld(t)
	w.repoAt("platform", "v7.0.0")
	saved := state.Installation{Kind: state.Cluster, Context: "prod", Org: "acme", Repo: "platform", GitUserName: "octocat", TargetType: defaultTarget, UpstreamRepo: defaultUpstream, UpstreamBranch: "v7.0.0", Domain: "platform.example.com", DNSProvider: "none", CloudProvider: "aks"}
	if err := w.catalog().Record(saved); err != nil {
		t.Fatal(err)
	}
	u := uitest.New(map[string]any{"confirm-context": "prod"})
	if err := runInstall(context.Background(), u, w.svc); err != nil {
		t.Fatal(err)
	}
	if s := w.cluster.Secret; s["KUBRIX_BOOTSTRAP"] != "false" || s["KUBRIX_CLOUD_PROVIDER"] != "aks" || s["KUBRIX_DOMAIN"] != "platform.example.com" || s["KUBRIX_UPSTREAM_BRANCH"] != "v7.0.0" {
		t.Errorf("secret %v", s)
	}
	if len(w.host.Created) != 0 {
		t.Errorf("created %v", w.host.Created)
	}
}

// upgradeWorld has a platform repository acme/demo bootstrapped from v7.0.0 and saved as a demo.
func upgradeWorld(t *testing.T, release string) (*world, string) {
	t.Helper()
	w := newWorld(t)
	bare := w.repoAt("demo", release, "kargo")
	saved := state.Installation{Kind: state.KindDemo, ClusterName: "kubrix-demo", Org: "acme", Repo: "demo", TargetType: testrepo.TargetType, UpstreamRepo: defaultUpstream}
	if err := w.catalog().Record(saved); err != nil {
		t.Fatal(err)
	}
	return w, bare
}

func TestRunUpgradeOpensAPullRequest(t *testing.T) {
	w, bare := upgradeWorld(t, "v7.0.0")
	u := uitest.New(map[string]any{"Upgrade v7.0.0 to": "v7.1.0"})
	if err := runUpgrade(context.Background(), u, w.svc); err != nil {
		t.Fatal(err)
	}
	if len(w.host.PRs) != 1 {
		t.Fatalf("PRs %+v", w.host.PRs)
	}
	pr := w.host.PRs[0]
	if pr.Head != "kubrix-upgrade-v7.1.0" || pr.Base != "main" || pr.Owner != "acme" || pr.Repo != "demo" || !strings.Contains(pr.Title, "v7.0.0 → v7.1.0") {
		t.Errorf("PR %+v", pr)
	}
	if branches := testrepo.Run(t, testrepo.Git(t, bare), "branch", "--list", pr.Head); branches == "" {
		t.Error("the branch must be pushed")
	}
	if oks := u.OKs(); !slices.ContainsFunc(oks, func(m string) bool { return strings.HasPrefix(m, "pull request: ") }) {
		t.Errorf("oks %v", oks)
	}
	if !slices.Contains(u.Infos(), "Argo CD applies the upgrade after the pull request is merged.") {
		t.Errorf("infos %v", u.Infos())
	}
}

func TestRunUpgradeWarnsAboutBreakingChangesAndNeedsConfirmation(t *testing.T) {
	w, bare := upgradeWorld(t, "v7.0.0")
	const breaking = "This upgrade has BREAKING CHANGES. Open the pull request?"
	declined := uitest.New(map[string]any{breaking: false})
	if err := runUpgrade(context.Background(), declined, w.svc); !errors.Is(err, ui.ErrAborted) {
		t.Fatalf("got %v", err)
	}
	if len(w.host.PRs) != 0 || testrepo.Run(t, testrepo.Git(t, bare), "branch", "--list", "kubrix-upgrade-*") != "" {
		t.Error("declining changes nothing")
	}
	if !slices.Contains(declined.Asked(), "Upgrade v7.0.0 to") {
		t.Errorf("asked %v", declined.Asked())
	}

	if err := runUpgrade(context.Background(), uitest.New(nil), w.svc); err != nil {
		t.Fatal(err)
	}
	if len(w.host.PRs) != 1 || w.host.PRs[0].Head != "kubrix-upgrade-v8.0.0" {
		t.Errorf("the newest release is the default: %+v", w.host.PRs)
	}
}

func TestRunUpgradeEndsQuietlyOnTheLatestRelease(t *testing.T) {
	w, _ := upgradeWorld(t, "v8.0.0")
	u := uitest.New(nil)
	err := runUpgrade(context.Background(), u, w.svc)
	if !errors.Is(err, ui.ErrQuit) || !slices.Contains(u.OKs(), "already on the latest release v8.0.0") || len(w.host.PRs) != 0 {
		t.Errorf("%v oks %v PRs %v", err, u.OKs(), w.host.PRs)
	}
}

func TestRunUpgradeStopsOnConflictsItCannotResolve(t *testing.T) {
	w, bare := upgradeWorld(t, "v7.0.0")
	edit := testrepo.Clone(t, bare)
	testrepo.Write(t, edit, map[string]string{"platform-apps/charts/keycloak/values-kubrix-default.yaml": "replicas: 3\n"})
	g := testrepo.Git(t, edit)
	testrepo.Run(t, g, "commit", "-q", "-am", "customer tuning")
	testrepo.Run(t, g, "push", "-q", "origin", "main")

	err := runUpgrade(context.Background(), uitest.New(nil), w.svc)
	var conflict *upgrade.ConflictError
	if !errors.As(err, &conflict) || !strings.Contains(err.Error(), "nothing was pushed") {
		t.Fatalf("got %v", err)
	}
	if len(w.host.PRs) != 0 {
		t.Errorf("PRs %v", w.host.PRs)
	}
}

// A repository that was published without kubriX's history cannot say which version it has, nor be merged.
func TestRunUpgradeAsksForTheVersionAndRefusesUnrelatedHistory(t *testing.T) {
	w := newWorld(t)
	orphan := t.TempDir()
	g := testrepo.Git(t, orphan)
	testrepo.Run(t, g, "init", "-q", "-b", "main")
	testrepo.Write(t, orphan, map[string]string{"README.md": "published without history\n"})
	testrepo.Run(t, g, "add", "-A")
	testrepo.Run(t, g, "commit", "-q", "-m", "orphan")
	bare := testrepo.Bare(t)
	testrepo.Run(t, g, "push", "-q", bare, "main")
	w.host.URLs["acme/demo"] = bare
	if err := w.catalog().Record(state.Installation{Kind: state.KindDemo, ClusterName: "kubrix-demo", Org: "acme", Repo: "demo", TargetType: testrepo.TargetType}); err != nil {
		t.Fatal(err)
	}

	u := uitest.New(map[string]any{"Which kubriX version is installed?": "v7.0.0", "Upgrade v7.0.0 to": "v7.1.0"})
	if err := runUpgrade(context.Background(), u, w.svc); !errors.Is(err, upgrade.ErrUnrelatedHistory) {
		t.Fatalf("got %v", err)
	}
	if !slices.Contains(u.Asked(), "Which kubriX version is installed?") {
		t.Errorf("asked %v", u.Asked())
	}
	if slices.ContainsFunc(u.Asked(), func(k string) bool { return strings.HasPrefix(k, "Open a pull request") }) || len(w.host.PRs) != 0 {
		t.Error("the problem is found before the user is asked to confirm anything")
	}
}

func TestRunUpgradeWithAnotherRepository(t *testing.T) {
	w, _ := upgradeWorld(t, "v7.0.0")
	w.repoAt("other", "v7.0.0")
	u := uitest.New(map[string]any{"installation": otherRepo, "repository": "acme/other", "target-type": testrepo.TargetType, "Upgrade v7.0.0 to": "v7.1.0"})
	if err := runUpgrade(context.Background(), u, w.svc); err != nil {
		t.Fatal(err)
	}
	if len(w.host.PRs) != 1 || w.host.PRs[0].Repo != "other" {
		t.Errorf("PRs %+v", w.host.PRs)
	}
}

func TestRunUpgradeFailures(t *testing.T) {
	t.Run("github", func(t *testing.T) {
		w, _ := upgradeWorld(t, "v7.0.0")
		w.githubErr = errors.New("bad token")
		if err := runUpgrade(context.Background(), uitest.New(nil), w.svc); err == nil || !strings.Contains(err.Error(), "bad token") {
			t.Errorf("got %v", err)
		}
	})
	t.Run("pull request", func(t *testing.T) {
		w, _ := upgradeWorld(t, "v7.0.0")
		w.host.PRErr = errors.New("rate limited")
		err := runUpgrade(context.Background(), uitest.New(map[string]any{"Upgrade v7.0.0 to": "v7.1.0"}), w.svc)
		if err == nil || !strings.Contains(err.Error(), "run the upgrade again") {
			t.Errorf("got %v", err)
		}
	})
	t.Run("unreachable repository", func(t *testing.T) {
		w, _ := upgradeWorld(t, "v7.0.0")
		w.host.URLs["acme/demo"] = "/does/not/exist"
		if err := runUpgrade(context.Background(), uitest.New(nil), w.svc); err == nil {
			t.Error("an unreachable repository must fail")
		}
	})
}

func TestRunDemoDeleteRemovesTheClusterAndForgetsTheDemo(t *testing.T) {
	w := newWorld(t)
	w.kind.Existing = true
	if err := w.catalog().Record(state.Installation{Kind: state.KindDemo, ClusterName: "kubrix-demo", Org: "acme", Repo: "demo"}); err != nil {
		t.Fatal(err)
	}
	u := uitest.New(nil)
	if err := runDemoDelete(context.Background(), u, w.svc); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(w.kind.Calls, []string{"delete kubrix-demo"}) {
		t.Errorf("kind %v", w.kind.Calls)
	}
	if len(w.host.Deleted) != 0 {
		t.Errorf("the repository is only deleted when the user says so: %v", w.host.Deleted)
	}
	if w.catalog().Len() != 0 {
		t.Error("the demo is forgotten once its cluster is gone")
	}
	if !slices.Contains(u.OKs(), `deleted kind cluster kubrix-demo`) {
		t.Errorf("oks %v", u.OKs())
	}
}

func TestRunDemoDeleteCanAlsoDeleteTheRepository(t *testing.T) {
	w := newWorld(t)
	w.kind.Existing = true
	w.host.Repos["acme/demo"] = git.RepoNonEmpty
	if err := w.catalog().Record(state.Installation{Kind: state.KindDemo, ClusterName: "kubrix-demo", Org: "acme", Repo: "demo"}); err != nil {
		t.Fatal(err)
	}
	u := uitest.New(map[string]any{"Also delete GitHub repository acme/demo?": true})
	if err := runDemoDelete(context.Background(), u, w.svc); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(w.host.Deleted, []string{"acme/demo"}) {
		t.Errorf("deleted %v", w.host.Deleted)
	}
}

func TestRunDemoDeleteKeepsWhatTheUserDoesNotConfirm(t *testing.T) {
	w := newWorld(t)
	w.kind.Existing = true
	if err := w.catalog().Record(state.Installation{Kind: state.KindDemo, ClusterName: "kubrix-demo", Org: "acme", Repo: "demo"}); err != nil {
		t.Fatal(err)
	}
	u := uitest.New(map[string]any{`Delete kind cluster "kubrix-demo"?`: false})
	if err := runDemoDelete(context.Background(), u, w.svc); err != nil {
		t.Fatal(err)
	}
	if len(w.kind.Calls) != 0 || w.catalog().Len() != 1 {
		t.Errorf("a cluster that stays keeps its Installation: kind %v saved %d", w.kind.Calls, w.catalog().Len())
	}
}

func TestRunDemoDeleteForgetsAMissingClusterAndAnInstallOnAnExistingOne(t *testing.T) {
	w := newWorld(t)
	rec := w.catalog()
	for _, st := range []state.Installation{
		{Kind: state.KindDemo, ClusterName: "gone", Org: "acme", Repo: "demo-gone"},
		{Kind: state.Cluster, Context: "prod", Org: "acme", Repo: "platform"},
	} {
		if err := rec.Record(st); err != nil {
			t.Fatal(err)
		}
	}
	// Two installations: the user chooses. A cluster install is only forgotten; kubriX keeps running there.
	u := uitest.New(map[string]any{"Which installation?": "prod"})
	if err := runDemoDelete(context.Background(), u, w.svc); err != nil {
		t.Fatal(err)
	}
	if _, ok := w.catalog().Find("prod"); ok || w.catalog().Len() != 1 || len(w.kind.Calls) != 0 {
		t.Errorf("saved %v kind %v", w.catalog().Keys(), w.kind.Calls)
	}
	// What is left is a demo whose kind cluster does not exist (any more).
	u = uitest.New(nil)
	if err := runDemoDelete(context.Background(), u, w.svc); err != nil {
		t.Fatal(err)
	}
	if w.catalog().Len() != 0 || !slices.ContainsFunc(u.Infos(), func(m string) bool { return strings.Contains(m, `"gone" does not exist`) }) {
		t.Errorf("saved %v infos %v", w.catalog().Keys(), u.Infos())
	}
}

func TestRunDemoDeleteFailures(t *testing.T) {
	w := newWorld(t)
	if err := runDemoDelete(context.Background(), uitest.New(nil), w.svc); err == nil || !strings.Contains(err.Error(), "nothing to delete") {
		t.Errorf("nothing saved: %v", err)
	}

	dir, _ := state.Dir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "installations.json"), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runDemoDelete(context.Background(), uitest.New(nil), w.svc); err == nil || !strings.Contains(err.Error(), "cannot delete") {
		t.Errorf("an unreadable catalog is not an empty one: %v", err)
	}
}
