package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/suxess-it/kubrix-cli/internal/git"
	"github.com/suxess-it/kubrix-cli/internal/git/gittest"
	"github.com/suxess-it/kubrix-cli/internal/install"
	"github.com/suxess-it/kubrix-cli/internal/platforminstaller"
	"github.com/suxess-it/kubrix-cli/internal/state"
	"github.com/suxess-it/kubrix-cli/internal/ui"
	"github.com/suxess-it/kubrix-cli/internal/ui/uitest"
)

type stubUpstream struct {
	apps   []string
	images []string
}

func (u stubUpstream) Apps(context.Context, string, string, string, string) ([]string, error) {
	return u.apps, nil
}

func (u stubUpstream) Manifest(context.Context, string, string) ([]byte, error) {
	return []byte("image: " + platforminstaller.ImageRepo + ":latest\n"), nil
}

func (u stubUpstream) ImageTagExists(_ context.Context, tag string) (bool, error) {
	return slices.Contains(u.images, tag), nil
}
func (u stubUpstream) GitURL(string) string { return "" }

// inertTarget is a target the tests inspect against but never run.
type inertTarget struct{}

func (inertTarget) Profile() install.Profile { return install.Profile{ClusterType: "kind"} }
func (inertTarget) Prepare(context.Context, install.Progress) (install.Prepared, error) {
	return install.Prepared{}, errors.New("not run in this test")
}
func (inertTarget) Access(context.Context, install.Cluster, install.Progress) error { return nil }

func selection(ref string, excluded ...string) state.Installation {
	return state.Installation{Org: "acme", Repo: "demo", TargetType: defaultTarget, UpstreamRepo: defaultUpstream, UpstreamBranch: ref, ExcludedApps: excluded}
}

func deps(host *gittest.Host, up stubUpstream) install.Deps {
	return install.Deps{Host: host, Upstream: up}
}

const (
	reinstallKey = "acme/demo already has content. Reinstall from it?"
	fallbackKey  = "Continue with the 'latest' installer image?"
)

func TestInspectKeepsDefaultsForAFreshRepo(t *testing.T) {
	host := gittest.New("octocat", "acme")
	u := uitest.New(nil)
	up := stubUpstream{apps: []string{"traefik", "grafana", "kargo"}, images: []string{"v7.0.0"}}
	plan, err := inspect(context.Background(), u, deps(host, up), inertTarget{}, selection("v7.0.0", "kargo"))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(u.Asked(), []string{"apps"}) {
		t.Errorf("a fresh repository only asks which apps to install, got %v", u.Asked())
	}
	if !slices.Equal(plan.Excluded(), []string{"kargo"}) {
		t.Errorf("saved exclusions survive an untouched selection: %v", plan.Excluded())
	}
	if !slices.Equal(u.Warns(), []string{"not installing: kargo"}) {
		t.Errorf("warns %v", u.Warns())
	}
}

func TestInspectLeavesOutTheAppsTheUserDeselects(t *testing.T) {
	host := gittest.New("octocat", "acme")
	u := uitest.New(map[string]any{"apps": []string{"grafana"}})
	up := stubUpstream{apps: []string{"traefik", "grafana", "kargo", "backstage"}}
	plan, err := inspect(context.Background(), u, deps(host, up), inertTarget{}, selection("main"))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(plan.Excluded(), []string{"kargo", "backstage"}) {
		t.Errorf("excluded %v", plan.Excluded())
	}
}

func TestInspectRejectsAnAppSelectionThatBreaksADependency(t *testing.T) {
	host := gittest.New("octocat", "acme")
	u := uitest.New(map[string]any{"apps": []string{"keycloak", "cnpg"}})
	up := stubUpstream{apps: []string{"traefik", "keycloak", "crossplane", "cnpg"}}
	_, err := inspect(context.Background(), u, deps(host, up), inertTarget{}, selection("main"))
	var rejected *uitest.AnswerError
	if !errors.As(err, &rejected) || !strings.Contains(rejected.Error(), "keycloak needs crossplane") {
		t.Errorf("got %v", err)
	}
}

func TestInspectAbortsWhenTheAppSelectionIsCancelled(t *testing.T) {
	host := gittest.New("octocat", "acme")
	u := uitest.New(map[string]any{"apps": uitest.Abort})
	_, err := inspect(context.Background(), u, deps(host, stubUpstream{apps: []string{"traefik", "grafana"}}), inertTarget{}, selection("main"))
	if !errors.Is(err, ui.ErrAborted) {
		t.Errorf("got %v", err)
	}
}

func TestInspectAsksBeforeReinstallingFromARepoWithContent(t *testing.T) {
	host := gittest.New("octocat", "acme")
	host.Repos["acme/demo"] = git.RepoNonEmpty

	u := uitest.New(nil)
	plan, err := inspect(context.Background(), u, deps(host, stubUpstream{}), inertTarget{}, selection("main"))
	if err != nil || plan.Fresh {
		t.Fatalf("plan %+v, %v", plan, err)
	}
	if !slices.Equal(u.Asked(), []string{reinstallKey}) {
		t.Errorf("asked %v", u.Asked())
	}

	declined := uitest.New(map[string]any{reinstallKey: false})
	if _, err := inspect(context.Background(), declined, deps(host, stubUpstream{}), inertTarget{}, selection("main")); !errors.Is(err, ui.ErrAborted) {
		t.Errorf("declining must abort, got %v", err)
	}
}

// The fallback to the latest installer image defaults to "no": the user has to opt in.
func TestInspectOnlyFallsBackToLatestWhenTheUserAgrees(t *testing.T) {
	host := gittest.New("octocat", "acme")
	host.Repos["acme/demo"] = git.RepoNonEmpty

	refused := uitest.New(nil)
	if _, err := inspect(context.Background(), refused, deps(host, stubUpstream{}), inertTarget{}, selection("fix/y")); !errors.Is(err, ui.ErrAborted) {
		t.Fatalf("got %v", err)
	}
	if warns := refused.Warns(); len(warns) != 1 || !strings.Contains(warns[0], "fix-y") {
		t.Errorf("the user must be told which image is missing: %v", warns)
	}

	agreed := uitest.New(map[string]any{fallbackKey: true})
	plan, err := inspect(context.Background(), agreed, deps(host, stubUpstream{}), inertTarget{}, selection("fix/y"))
	if err != nil || plan.ImageFallback == "" {
		t.Errorf("plan %+v, %v", plan, err)
	}
}

func TestInspectNeedsNoPromptWhenTheImageExists(t *testing.T) {
	host := gittest.New("octocat", "acme")
	u := uitest.New(nil)
	up := stubUpstream{apps: []string{"traefik"}, images: []string{"feat-x"}}
	if _, err := inspect(context.Background(), u, deps(host, up), inertTarget{}, selection("feat/x")); err != nil {
		t.Fatal(err)
	}
	if u.Forms() != 0 || len(u.Warns()) != 0 {
		t.Errorf("forms %d warns %v", u.Forms(), u.Warns())
	}
}

func TestRecordInstallationSavesAndKeysByName(t *testing.T) {
	dir := t.TempDir()
	u := uitest.New(nil)
	record := recordInstallation(u, state.Open(dir))
	record(state.Installation{Kind: state.KindDemo, ClusterName: "kubrix-demo", Org: "acme", Repo: "demo"})
	record(state.Installation{Kind: state.Cluster, Context: "prod", Org: "acme", Repo: "platform"})

	if names := slices.Sorted(slices.Values(state.Open(dir).Keys())); !slices.Equal(names, []string{"kubrix-demo", "prod"}) {
		t.Errorf("names %v", names)
	}
	if len(u.Warns()) != 0 {
		t.Errorf("warns %v", u.Warns())
	}
}

func TestRecordInstallationWarnsInsteadOfFailing(t *testing.T) {
	file := t.TempDir() + "/file"
	if err := writeFile(file); err != nil {
		t.Fatal(err)
	}
	u := uitest.New(nil)
	recordInstallation(u, state.Open(file))(state.Installation{ClusterName: "x"}) // a file where a directory is needed
	if warns := u.Warns(); len(warns) != 1 || !strings.Contains(warns[0], "could not save this installation") {
		t.Errorf("warns %v", warns)
	}
}

func TestOpenCatalogWarnsOnceAboutAnUnreadableFileAndKeepsIt(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	path := filepath.Join(xdg, "kubrix", "installations.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	u := uitest.New(nil)
	catalog := openCatalog(u)
	if catalog.Len() != 0 || len(u.Warns()) != 1 || !strings.Contains(u.Warns()[0], "unreadable saved installations") {
		t.Errorf("len %d warns %v", catalog.Len(), u.Warns())
	}
	recordInstallation(u, catalog)(state.Installation{ClusterName: "x"})
	if data, _ := os.ReadFile(path); string(data) != "{broken" {
		t.Errorf("the unreadable file must stay as it was: %q", data)
	}
	if len(u.Warns()) != 2 {
		t.Errorf("recording must warn that nothing was saved: %v", u.Warns())
	}
}

func TestOpenCatalogReadsTheDefaultLocation(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	u := uitest.New(nil)
	recordInstallation(u, openCatalog(u))(state.Installation{Kind: state.KindDemo, ClusterName: "kubrix-demo", Org: "acme", Repo: "demo"})
	if openCatalog(u).Len() != 1 || len(u.Warns()) != 0 {
		t.Errorf("warns %v", u.Warns())
	}
}

type stubClusters struct{ exists bool }

func (s stubClusters) Exists(string) (bool, error)             { return s.exists, nil }
func (s stubClusters) Create(string) error                     { return nil }
func (s stubClusters) Delete(string) error                     { return nil }
func (s stubClusters) Connect(string) (install.Cluster, error) { return nil, nil }

func TestAskReuseClusterOnlyAsksForAnExistingCluster(t *testing.T) {
	const key = `kind cluster "kubrix-demo" already exists`
	u := uitest.New(nil)
	if reuse, err := askReuseCluster(u, stubClusters{exists: false}, "kubrix-demo"); err != nil || reuse || u.Forms() != 0 {
		t.Errorf("new cluster: reuse %v err %v forms %d", reuse, err, u.Forms())
	}
	// An existing cluster defaults to being recreated.
	if reuse, err := askReuseCluster(u, stubClusters{exists: true}, "kubrix-demo"); err != nil || reuse || !slices.Equal(u.Asked(), []string{key}) {
		t.Errorf("existing cluster: reuse %v err %v asked %v", reuse, err, u.Asked())
	}
	reusing := uitest.New(map[string]any{key: "reuse"})
	if reuse, err := askReuseCluster(reusing, stubClusters{exists: true}, "kubrix-demo"); err != nil || !reuse {
		t.Errorf("reuse %v err %v", reuse, err)
	}
	cancelled := uitest.New(map[string]any{key: uitest.Abort})
	if _, err := askReuseCluster(cancelled, stubClusters{exists: true}, "kubrix-demo"); !errors.Is(err, ui.ErrAborted) {
		t.Errorf("got %v", err)
	}
}

func TestCheckContributorBranchIgnoresNonContributors(t *testing.T) {
	if err := checkContributorBranch(uitest.New(nil), nil, selection("main")); err != nil {
		t.Errorf("got %v", err)
	}
}

func writeFile(path string) error { return os.WriteFile(path, []byte("x"), 0o600) }
