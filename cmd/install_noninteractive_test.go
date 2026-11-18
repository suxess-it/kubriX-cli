package cmd

import (
	"context"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/suxess-it/kubrix-cli/internal/git"
	"github.com/suxess-it/kubrix-cli/internal/kubeconfig"
	"github.com/suxess-it/kubrix-cli/internal/state"
	"github.com/suxess-it/kubrix-cli/internal/ui"
	"github.com/suxess-it/kubrix-cli/internal/ui/uitest"
)

// ciConfig is how kubriX CI installs into the kind cluster it created, into a repository it created.
func ciConfig() installConfig {
	return installConfig{
		Context: "kind-kubrix-cluster", Org: "acme", Repo: "demo", GitUserName: "kubrix-ci",
		TargetType: "kind-delivery", UpstreamRepo: defaultUpstream, UpstreamBranch: "feat/x",
		InstallerTags: []string{"pr-12", "main"}, MaxWaitSeconds: 3600, RepoToken: "repo-tok", UpstreamToken: "up-tok",
		ClusterType: "kind", Extra: map[string]string{"KUBRIX_SYNC_DEBUG": "true"},
	}
}

// noPromptsUI fails the test on any question: a non-interactive install never asks.
func noPromptsUI() (noPrompts, *uitest.UI) {
	u := uitest.New(nil)
	return noPrompts{u}, u
}

func TestInstallFromConfigOnAnExistingKindCluster(t *testing.T) {
	w := newWorld(t)
	w.contexts = append(w.contexts, kubeconfig.Context{Name: "kind-kubrix-cluster"})
	w.kind.Existing = true
	w.emptyRepoAt("demo")
	w.host.Repos["acme/demo"] = git.RepoEmpty
	w.upstream.Images = []string{"main"}
	u, rec := noPromptsUI()

	if err := runInstallFromConfig(context.Background(), u, w.svc, ciConfig()); err != nil {
		t.Fatal(err)
	}

	if len(w.kind.Calls) != 0 || len(w.host.Created) != 0 {
		t.Errorf("the kind cluster and the repository must be reused: kind %v created %v", w.kind.Calls, w.host.Created)
	}
	s := w.cluster.Secret
	for key, want := range map[string]string{
		"KUBRIX_CLUSTER_TYPE": "kind", "KUBRIX_DOMAIN": kindDomain, "KUBRIX_DNS_PROVIDER": "none",
		"KUBRIX_TARGET_TYPE": "kind-delivery", "KUBRIX_BOOTSTRAP": "true", "KUBRIX_UPSTREAM_BRANCH": "feat/x",
		"KUBRIX_GIT_USER_NAME": "kubrix-ci", "KUBRIX_UPSTREAM_REPO_PASSWORD": "up-tok",
		"KUBRIX_BOOTSTRAP_MAX_WAIT_TIME": "3600", "KUBRIX_SYNC_DEBUG": "true",
	} {
		if s[key] != want {
			t.Errorf("%s = %q, want %q", key, s[key], want)
		}
	}
	if !strings.Contains(w.cluster.Manifest, ":main") || !slices.Contains(w.cluster.Calls, "delete job") {
		t.Errorf("manifest %q calls %v", w.cluster.Manifest, w.cluster.Calls)
	}
	if !slices.Equal(w.githubTokens, []string{"repo-tok"}) || !slices.Equal(w.upstreamTokens, []string{"up-tok"}) {
		t.Errorf("github tokens %v upstream tokens %v", w.githubTokens, w.upstreamTokens)
	}
	if slices.Contains(rec.Infos(), "Argo CD login: admin / pw") {
		t.Errorf("the password must not be printed: %v", rec.Infos())
	}
	if saved, ok := w.catalog().Find("kubrix-cluster"); !ok || saved.Kind != state.KindDemo || saved.TargetType != "kind-delivery" {
		t.Errorf("saved %+v", saved)
	}
}

func TestInstallFromConfigOnAnExistingCluster(t *testing.T) {
	w := newWorld(t)
	w.repoAt("prod-platform", "v8.0.0")
	u, rec := noPromptsUI()
	cfg := installConfig{
		Context: "prod", Org: "acme", Repo: "prod-platform", TargetType: defaultTarget,
		UpstreamRepo: defaultUpstream, UpstreamBranch: "v8.0.0", Domain: "platform.example.com",
		DNSProvider: "cloudflare", RepoToken: "repo-tok", ExcludedApps: []string{"kargo"},
	}
	cfg.DNS.Token = "cf-tok"

	if err := runInstallFromConfig(context.Background(), u, w.svc, cfg); err != nil {
		t.Fatal(err)
	}

	s := w.cluster.Secret
	if s["KUBRIX_CLUSTER_TYPE"] != "k8s" || s["KUBRIX_CLOUD_PROVIDER"] != defaultProvider || s["KUBRIX_GIT_USER_NAME"] != "octocat" || s["KUBRIX_BOOTSTRAP"] != "false" {
		t.Errorf("secret %v", s)
	}
	if _, ok := s["KUBRIX_UPSTREAM_REPO_PASSWORD"]; ok {
		t.Error("no upstream token was given")
	}
	if got := string(w.cluster.DNSData["external-dns/cloudflare-api-key"]["apiKey"]); got != "cf-tok" {
		t.Errorf("dns data %v", w.cluster.DNSData)
	}
	if !slices.Equal(w.upstreamTokens, []string{w.host.Token()}) {
		t.Errorf("without an upstream token the GitHub token reads the kubriX source: %v", w.upstreamTokens)
	}
	if !slices.ContainsFunc(rec.Warns(), func(m string) bool { return strings.Contains(m, "app-exclude has no effect") }) {
		t.Errorf("an exclusion on a bootstrapped repository is reported: %v", rec.Warns())
	}
	if saved, ok := w.catalog().Find("prod"); !ok || saved.Kind != state.Cluster || saved.Domain != "platform.example.com" {
		t.Errorf("saved %+v", saved)
	}
}

func TestInstallFromConfigReportsExclusionsThatDoNotApply(t *testing.T) {
	w := newWorld(t)
	w.contexts = append(w.contexts, kubeconfig.Context{Name: "kind-kubrix-cluster"})
	w.kind.Existing = true
	w.emptyRepoAt("demo")
	u, rec := noPromptsUI()
	cfg := ciConfig()
	cfg.ExcludedApps = []string{"kargo", "traefik", "nope"}

	if err := runInstallFromConfig(context.Background(), u, w.svc, cfg); err != nil {
		t.Fatal(err)
	}
	if got := w.cluster.Secret["KUBRIX_APP_EXCLUDE"]; got != "kargo" {
		t.Errorf("excluded %q", got)
	}
	for _, app := range []string{"traefik", "nope"} {
		if !slices.ContainsFunc(rec.Warns(), func(m string) bool { return strings.HasPrefix(m, "app-exclude "+app+" has no effect") }) {
			t.Errorf("%s: warns %v", app, rec.Warns())
		}
	}
}

func TestInstallFromConfigRejectsWhatItCannotDo(t *testing.T) {
	missingFile := filepath.Join(t.TempDir(), "missing.json")
	for name, tc := range map[string]struct {
		change func(*world, *installConfig)
		want   string
	}{
		"unknown context":       {func(_ *world, c *installConfig) { c.Context = "nope" }, `no kubeconfig context "nope"`},
		"kind cluster missing":  {func(w *world, _ *installConfig) { w.kind.Existing = false }, `has no kind cluster "kubrix-cluster"`},
		"cluster type mismatch": {func(_ *world, c *installConfig) { c.ClusterType = "k8s" }, "KUBRIX_CLUSTER_TYPE is k8s"},
		"domain on kind":        {func(_ *world, c *installConfig) { c.Domain = "platform.example.com" }, "domain on kind"},
		"cloud on kind":         {func(_ *world, c *installConfig) { c.CloudProvider = "aks" }, "cloud-provider does not apply to kind"},
		"no domain":             {func(_ *world, c *installConfig) { prod(c) }, "domain is required"},
		"bad domain":            {func(_ *world, c *installConfig) { prod(c); c.Domain = "x.kubrix.cloud" }, "kubriX's own demo zone"},
		"unknown dns provider":  {func(_ *world, c *installConfig) { prod(c); c.Domain, c.DNSProvider = "p.example.com", "manual" }, "dns-provider must be one of"},
		"dns token missing":     {func(_ *world, c *installConfig) { prod(c); c.Domain, c.DNSProvider = "p.example.com", "ionos" }, "ionos needs an API token"},
		"stackit project": {func(_ *world, c *installConfig) {
			prod(c)
			c.Domain, c.DNSProvider, c.DNS.Token = "p.example.com", "stackit", "t"
		}, "dns-project-id"},
		"credentials file": {func(_ *world, c *installConfig) {
			prod(c)
			c.Domain, c.DNSProvider, c.DNS.File = "p.example.com", "aws", missingFile
		}, "dns-credentials-file"},
		"cloud provider": {func(_ *world, c *installConfig) {
			prod(c)
			c.Domain, c.DNSProvider, c.CloudProvider = "p.example.com", "none", "gke"
		}, "cloud-provider must be"},
		"not cluster-admin": {func(w *world, c *installConfig) {
			prod(c)
			c.Domain, c.DNSProvider = "p.example.com", "none"
			w.cluster.NotAdmin = true
		}, "not cluster-admin"},
		"no storage class": {func(w *world, c *installConfig) {
			prod(c)
			c.Domain, c.DNSProvider = "p.example.com", "none"
			w.cluster.NoStorageClass = true
		}, "no default StorageClass"},
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			w.contexts = append(w.contexts, kubeconfig.Context{Name: "kind-kubrix-cluster"})
			w.kind.Existing = true
			cfg := ciConfig()
			tc.change(w, &cfg)
			u, _ := noPromptsUI()
			err := runInstallFromConfig(context.Background(), u, w.svc, cfg)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("got %v, want %q", err, tc.want)
			}
			if w.cluster.Secret != nil {
				t.Error("nothing may be installed")
			}
		})
	}
}

// prod points a config at the existing cluster of the world's prod context.
func prod(c *installConfig) { c.Context, c.ClusterType = "prod", "" }

func TestNoPromptsNeverAsks(t *testing.T) {
	u, rec := noPromptsUI()
	if err := u.Ask(ui.Form{}); err == nil || rec.Forms() != 0 {
		t.Errorf("err %v forms %d", err, rec.Forms())
	}
}

func TestInstallCommandModes(t *testing.T) {
	clearKubrixEnv(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	on := Features{Experimental: true}
	for name, tc := range map[string]struct {
		features Features
		args     []string
		want     string
	}{
		"settings are required":            {on, []string{"install"}, "context is required"},
		"interactive takes no settings":    {on, []string{"install", "-i", "--context", "prod"}, "--context cannot be combined with --interactive"},
		"interactive takes no config file": {on, []string{"install", "--interactive", "--config", "x.yaml"}, "--config cannot be combined"},
		"the config file must exist":       {on, []string{"install", "--config", filepath.Join(t.TempDir(), "x.yaml")}, "reading"},
		"still experimental":               {Features{}, []string{"install", "--context", "prod"}, "is experimental"},
		"interactive still experimental":   {Features{}, []string{"install", "-i"}, "is experimental"},
	} {
		t.Run(name, func(t *testing.T) {
			root := NewRoot(BuildInfo{}, tc.features)
			root.SetOut(io.Discard)
			root.SetErr(io.Discard)
			root.SetArgs(tc.args)
			if err := root.Execute(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("got %v, want %q", err, tc.want)
			}
		})
	}
}
