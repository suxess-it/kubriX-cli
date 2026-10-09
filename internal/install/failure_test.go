package install_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/suxess-it/kubrix-cli/internal/git"
	"github.com/suxess-it/kubrix-cli/internal/install"
	"github.com/suxess-it/kubrix-cli/internal/install/installtest"
	"github.com/suxess-it/kubrix-cli/internal/state"
)

type failingUpstream struct {
	installtest.Upstream
	apps, manifest, image error
}

func (u failingUpstream) Apps(ctx context.Context, repo, ref, tt, ct string) ([]string, error) {
	if u.apps != nil {
		return nil, u.apps
	}
	return u.Upstream.Apps(ctx, repo, ref, tt, ct)
}

func (u failingUpstream) Manifest(ctx context.Context, repo, ref string) ([]byte, error) {
	if u.manifest != nil {
		return nil, u.manifest
	}
	return u.Upstream.Manifest(ctx, repo, ref)
}

func (u failingUpstream) ImageTagExists(ctx context.Context, tag string) (bool, error) {
	if u.image != nil {
		return false, u.image
	}
	return u.Upstream.ImageTagExists(ctx, tag)
}

type brokenManifestUpstream struct{ installtest.Upstream }

func (brokenManifestUpstream) Manifest(context.Context, string, string) ([]byte, error) {
	return []byte("kind: Job\nimage: other:1\n"), nil
}

func TestInspectFailures(t *testing.T) {
	boom := errors.New("boom")
	for name, tc := range map[string]struct {
		upstream func(installtest.Upstream) install.Upstream
		ref      string
		want     string
	}{
		"apps cannot be loaded":      {func(u installtest.Upstream) install.Upstream { return failingUpstream{Upstream: u, apps: boom} }, "v7.0.0", "boom"},
		"manifest cannot be fetched": {func(u installtest.Upstream) install.Upstream { return failingUpstream{Upstream: u, manifest: boom} }, "main", "boom"},
		"registry cannot be reached": {func(u installtest.Upstream) install.Upstream { return failingUpstream{Upstream: u, image: boom} }, "feat/x", "boom"},
		"manifest has no latest tag": {func(u installtest.Upstream) install.Upstream { return brokenManifestUpstream{u} }, "main", "latest"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			target, _ := f.kind(false, false)
			deps := install.Deps{Host: f.host, Upstream: tc.upstream(f.upstream)}
			_, err := install.Inspect(context.Background(), deps, target, f.selection(tc.ref), f.progress)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want it to mention %q", err, tc.want)
			}
			if len(f.host.Created) != 0 {
				t.Errorf("inspecting must not change anything: created %v", f.host.Created)
			}
		})
	}
}

func TestInspectOfARepoWithContentLoadsNoApps(t *testing.T) {
	f := newFixture(t)
	f.host.Repos["acme/demo"] = git.RepoNonEmpty
	target, _ := f.kind(false, false)
	deps := install.Deps{Host: f.host, Upstream: failingUpstream{Upstream: f.upstream, apps: errors.New("must not be called")}}
	plan, err := install.Inspect(context.Background(), deps, target, f.selection("main"), f.progress)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Fresh || plan.Apps != nil {
		t.Errorf("plan %+v", plan)
	}
}

func TestEmptyExistingRepoCountsAsFresh(t *testing.T) {
	f := newFixture(t)
	f.host.Repos["acme/demo"] = git.RepoEmpty
	target, _ := f.kind(false, false)
	plan, err := install.Inspect(context.Background(), f.deps(), target, f.selection("main"), f.progress)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Fresh {
		t.Fatal("an empty repository is bootstrapped")
	}
	if err := plan.Run(context.Background(), f.progress, func(state.Installation) {}); err != nil {
		t.Fatal(err)
	}
	if len(f.host.Created) != 0 {
		t.Errorf("an existing empty repository is not created again: %v", f.host.Created)
	}
}

func TestNoOptionalAppsMeansNoChoice(t *testing.T) {
	f := newFixture(t)
	f.upstream.AppList = []string{"traefik", "argocd"}
	target, _ := f.kind(false, false)
	plan, err := install.Inspect(context.Background(), f.deps(), target, f.selection("main", "stale"), f.progress)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Apps != nil || len(plan.Excluded()) != 0 {
		t.Errorf("apps %+v excluded %v", plan.Apps, plan.Excluded())
	}
	plan.Select([]string{"anything"}) // must be a no-op without a choice
	if len(plan.Excluded()) != 0 {
		t.Errorf("excluded %v", plan.Excluded())
	}
}

type failingTarget struct {
	install.Target
	prepare  error
	accessed bool
}

func (t *failingTarget) Prepare(ctx context.Context, p install.Progress) (install.Prepared, error) {
	if t.prepare != nil {
		return install.Prepared{}, t.prepare
	}
	return t.Target.Prepare(ctx, p)
}

func (t *failingTarget) Access(ctx context.Context, c install.Cluster, p install.Progress) error {
	t.accessed = true
	return t.Target.Access(ctx, c, p)
}

func TestRunFailures(t *testing.T) {
	boom := errors.New("boom")
	for name, tc := range map[string]struct{ prepare error }{
		"cluster cannot be prepared": {prepare: boom},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.host.Repos["acme/demo"] = git.RepoNonEmpty
			kind, _ := f.kind(false, false)
			target := &failingTarget{Target: kind, prepare: tc.prepare}
			plan, err := install.Inspect(context.Background(), f.deps(), target, f.selection("main"), f.progress)
			if err != nil {
				t.Fatal(err)
			}
			saved := false
			err = plan.Run(context.Background(), f.progress, func(state.Installation) { saved = true })
			if !errors.Is(err, boom) {
				t.Fatalf("got %v", err)
			}
			if saved || f.cluster.Secret != nil || target.accessed {
				t.Errorf("a run that fails before the installer must not save, install or report access (saved %v, secret %v)", saved, f.cluster.Secret)
			}
		})
	}
}

func TestFailedInstallerIsNotReportedAsAccessible(t *testing.T) {
	f := newFixture(t)
	f.host.Repos["acme/demo"] = git.RepoNonEmpty
	f.cluster.JobFails = true
	kind, _ := f.kind(false, false)
	target := &failingTarget{Target: kind}
	plan, err := install.Inspect(context.Background(), f.deps(), target, f.selection("main"), f.progress)
	if err != nil {
		t.Fatal(err)
	}
	saved := false
	err = plan.Run(context.Background(), f.progress, func(state.Installation) { saved = true })
	if err == nil || !strings.Contains(err.Error(), "install job failed") {
		t.Fatalf("got %v", err)
	}
	if !saved {
		t.Error("the Installation is saved before the installer runs, so a failed run can be deleted")
	}
	if target.accessed {
		t.Error("access info must not be printed for a failed install")
	}
}

func TestClusterTargetDNSCredentialsErrors(t *testing.T) {
	f := newFixture(t)
	f.host.Repos["acme/demo"] = git.RepoNonEmpty
	target := &install.ClusterTarget{Kube: f.cluster, DNSProvider: "aws", DNS: install.DNSCredentials{File: "/does/not/exist"}}
	plan, err := install.Inspect(context.Background(), f.deps(), target, f.selection("main"), f.progress)
	if err != nil {
		t.Fatal(err)
	}
	err = plan.Run(context.Background(), f.progress, func(state.Installation) {})
	if err == nil || !strings.Contains(err.Error(), "reading aws credentials") {
		t.Fatalf("got %v", err)
	}
	if f.cluster.Secret != nil {
		t.Error("the installer must not start without its DNS credentials")
	}
}

func TestClusterTargetExcludesExternalDNSOnlyForManualDNS(t *testing.T) {
	if got := (&install.ClusterTarget{DNSProvider: install.DNSManual}).Profile().Excludes; len(got) != 1 || got["external-dns"] == "" {
		t.Errorf("manual DNS: %v", got)
	}
	if got := (&install.ClusterTarget{DNSProvider: "cloudflare"}).Profile().Excludes; len(got) != 0 {
		t.Errorf("provider DNS: %v", got)
	}
}

// Excludes of the target are added once and never duplicate what the user already left out.
func TestTargetExcludeIsNotDuplicated(t *testing.T) {
	f := newFixture(t)
	target := &install.ClusterTarget{Kube: f.cluster, Domain: "platform.example.com", DNSProvider: install.DNSManual}
	sel := f.selection("main", "external-dns")
	plan, err := install.Inspect(context.Background(), f.deps(), target, sel, f.progress)
	if err != nil {
		t.Fatal(err)
	}
	var saved []state.Installation
	if err := plan.Run(context.Background(), f.progress, func(s state.Installation) { saved = append(saved, s) }); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(saved[0].ExcludedApps, []string{"external-dns"}) || f.cluster.Secret["KUBRIX_APP_EXCLUDE"] != "external-dns" {
		t.Errorf("saved %v secret %v", saved[0].ExcludedApps, f.cluster.Secret)
	}
	if slices.ContainsFunc(f.progress.Infos(), func(m string) bool { return strings.HasPrefix(m, "external-dns is not installed") }) {
		t.Error("the user already left it out, so there is nothing to announce")
	}
}

func TestKindTargetWarnsWhenTheRootCAIsMissing(t *testing.T) {
	f := newFixture(t)
	target, _ := f.kind(false, false)
	target.CADir = t.TempDir()
	if err := target.Access(context.Background(), f.cluster, f.progress); err != nil {
		t.Fatal(err)
	}
	if len(f.progress.Warns()) != 1 || !strings.Contains(f.progress.Warns()[0], "kind root CA") {
		t.Errorf("warns %v", f.progress.Warns())
	}
	if entries, _ := os.ReadDir(target.CADir); len(entries) != 0 {
		t.Errorf("nothing to save: %v", entries)
	}
}

func TestKindTargetTellsHowToTrustTheCA(t *testing.T) {
	f := newFixture(t)
	f.cluster.Secrets["cert-manager/kind-kubrix-ca-key-pair/tls.crt"] = "CERT"
	target, _ := f.kind(false, false)
	target.CADir = filepath.Join(t.TempDir(), "nested")
	if err := target.Access(context.Background(), f.cluster, f.progress); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(f.progress.Headings(), "Browser trust") {
		t.Errorf("headings %v", f.progress.Headings())
	}
	caPath := filepath.Join(target.CADir, "kind-ca.crt")
	if !slices.ContainsFunc(f.progress.Infos(), func(m string) bool { return strings.Contains(m, caPath) }) {
		t.Errorf("infos %v", f.progress.Infos())
	}
	if len(f.progress.Warns()) != 1 || !strings.Contains(f.progress.Warns()[0], "private key ships in the public installer image") {
		t.Errorf("the user must be warned about the CA: %v", f.progress.Warns())
	}
}

func TestKindTargetPropagatesClusterErrors(t *testing.T) {
	f := newFixture(t)
	target, _ := f.kind(false, false)
	target.Clusters = &errKindClusters{err: errors.New("docker is down")}
	_, err := target.Prepare(context.Background(), f.progress)
	if err == nil || !strings.Contains(err.Error(), "docker is down") {
		t.Fatalf("got %v", err)
	}
}

type errKindClusters struct{ err error }

func (e *errKindClusters) Exists(string) (bool, error)             { return false, e.err }
func (e *errKindClusters) Create(string) error                     { return e.err }
func (e *errKindClusters) Delete(string) error                     { return e.err }
func (e *errKindClusters) Connect(string) (install.Cluster, error) { return nil, e.err }
