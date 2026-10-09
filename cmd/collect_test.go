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
	"github.com/suxess-it/kubrix-cli/internal/kubeconfig"
	"github.com/suxess-it/kubrix-cli/internal/state"
	"github.com/suxess-it/kubrix-cli/internal/ui"
	"github.com/suxess-it/kubrix-cli/internal/ui/uitest"
	"github.com/suxess-it/kubrix-cli/internal/upgrade"
)

// catalogWith returns a catalog in a temporary directory holding the installations, the last one most recent.
func catalogWith(t *testing.T, saved ...state.Installation) *state.Catalog {
	t.Helper()
	c := state.Open(t.TempDir())
	for _, st := range saved {
		if err := c.Record(st); err != nil {
			t.Fatal(err)
		}
	}
	return c
}

func githubWith(releases ...string) *gittest.Host {
	h := gittest.New("octocat", "acme", "beta")
	h.Releases = releases
	return h
}

// collectDemo runs collectDemoInput outside any checkout, so the user is no kubriX contributor.
func collectDemo(t *testing.T, u *uitest.UI, h *gittest.Host, base state.Installation, store *state.Catalog, original string) (state.Installation, error) {
	t.Helper()
	t.Chdir(t.TempDir())
	got, contributor, err := collectDemoInput(context.Background(), u, h, "octocat", base, store, original)
	if err == nil && contributor != nil {
		t.Fatalf("unexpected contributor %+v", contributor)
	}
	return got, err
}

func TestCollectDemoInputDefaults(t *testing.T) {
	u := uitest.New(nil)
	got, err := collectDemo(t, u, githubWith("v7.1.0", "v7.0.0"), state.Installation{}, catalogWith(t), "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != state.KindDemo || got.GitHost != git.GitHubName || got.Org != "acme" || got.GitUserName != "octocat" {
		t.Errorf("identity %+v", got)
	}
	if !strings.HasPrefix(got.Repo, "kubrix-demo-") || got.ClusterName != state.DefaultClusterName {
		t.Errorf("repo %q cluster %q", got.Repo, got.ClusterName)
	}
	if got.TargetType != defaultTarget || got.UpstreamRepo != defaultUpstream || got.UpstreamBranch != "v7.1.0" {
		t.Errorf("source %+v", got)
	}
	if got.Domain != kindDomain || got.DNSProvider != "none" || got.CloudProvider != "" {
		t.Errorf("kind networking %+v", got)
	}
	if want := []string{"org", "repo", "git-user", "target-type", "upstream", "version", "cluster-name"}; !slices.Equal(u.Asked(), want) {
		t.Errorf("asked %v, want %v", u.Asked(), want)
	}
}

func TestCollectDemoInputTakesTheAnswers(t *testing.T) {
	u := uitest.New(map[string]any{"org": "beta", "repo": "my-demo", "git-user": "me", "version": "v7.0.0", "cluster-name": "other"})
	got, err := collectDemo(t, u, githubWith("v7.1.0", "v7.0.0"), state.Installation{}, catalogWith(t), "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Org != "beta" || got.Repo != "my-demo" || got.GitUserName != "me" || got.UpstreamBranch != "v7.0.0" || got.ClusterName != "other" {
		t.Errorf("got %+v", got)
	}
	if u := u.Unused(); len(u) != 0 {
		t.Errorf("unused answers %v", u)
	}
}

func TestCollectDemoInputAsksForAnyBranch(t *testing.T) {
	u := uitest.New(map[string]any{"version": otherBranch, "branch": "feat/x"})
	got, err := collectDemo(t, u, githubWith("v7.0.0"), state.Installation{}, catalogWith(t), "")
	if err != nil {
		t.Fatal(err)
	}
	if got.UpstreamBranch != "feat/x" || !slices.Contains(u.Asked(), "branch") {
		t.Errorf("branch %q asked %v", got.UpstreamBranch, u.Asked())
	}

	// Without choosing "Other branch…", the branch page stays hidden.
	hidden := uitest.New(nil)
	if _, err := collectDemo(t, hidden, githubWith("v7.0.0"), state.Installation{}, catalogWith(t), ""); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(hidden.Asked(), "branch") {
		t.Errorf("asked %v", hidden.Asked())
	}
	empty := uitest.New(map[string]any{"version": otherBranch, "branch": ""})
	if _, err := collectDemo(t, empty, githubWith("v7.0.0"), state.Installation{}, catalogWith(t), ""); err == nil {
		t.Error("an empty branch must be rejected")
	}
}

func TestCollectDemoInputRejectsBadAnswers(t *testing.T) {
	for name, answers := range map[string]map[string]any{
		"repository name":         {"repo": "not a name"},
		"empty git user":          {"git-user": ""},
		"source repository":       {"upstream": "no-slash"},
		"cluster name uppercase":  {"cluster-name": "Demo"},
		"cluster name with space": {"cluster-name": "my demo"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := collectDemo(t, uitest.New(answers), githubWith("v7.0.0"), state.Installation{}, catalogWith(t), "")
			var rejected *uitest.AnswerError
			if !errors.As(err, &rejected) {
				t.Errorf("got %v", err)
			}
		})
	}
}

func TestCollectDemoInputClusterNamesMustBeFree(t *testing.T) {
	store := catalogWith(t, state.Installation{Kind: state.KindDemo, ClusterName: "taken"})

	_, err := collectDemo(t, uitest.New(map[string]any{"cluster-name": "taken"}), githubWith("v7.0.0"), state.Installation{}, store, "")
	if err == nil || !strings.Contains(err.Error(), "already uses") {
		t.Errorf("a name used by another installation is rejected: %v", err)
	}
	got, err := collectDemo(t, uitest.New(map[string]any{"cluster-name": "taken"}), githubWith("v7.0.0"), state.Installation{ClusterName: "taken"}, store, "taken")
	if err != nil || got.ClusterName != "taken" {
		t.Errorf("rerunning a demo keeps its own name: %+v %v", got, err)
	}
}

func TestCollectDemoInputRerunKeepsSavedSelections(t *testing.T) {
	saved := state.Installation{
		Kind: state.KindDemo, Org: "beta", Repo: "kept", GitUserName: "me", TargetType: "kind-base",
		UpstreamRepo: "someone/kubriX", UpstreamBranch: "v7.0.0", ClusterName: "kubrix-demo",
	}
	got, err := collectDemo(t, uitest.New(nil), githubWith("v7.1.0", "v7.0.0"), saved, catalogWith(t), "kubrix-demo")
	if err != nil {
		t.Fatal(err)
	}
	if got.Org != "beta" || got.Repo != "kept" || got.GitUserName != "me" || got.TargetType != "kind-base" ||
		got.UpstreamRepo != "someone/kubriX" || got.UpstreamBranch != "v7.0.0" {
		t.Errorf("got %+v", got)
	}
}

func TestCollectDemoInputFallsBackToTheFirstOrgWhenTheSavedOneIsGone(t *testing.T) {
	got, err := collectDemo(t, uitest.New(nil), githubWith("v7.0.0"), state.Installation{Org: "left-org"}, catalogWith(t), "")
	if err != nil || got.Org != "acme" {
		t.Errorf("got %+v %v", got, err)
	}
}

func TestCollectDemoInputWarnsWhenReleasesCannotBeLoaded(t *testing.T) {
	h := githubWith()
	h.ReleasesErr = errors.New("rate limited")
	u := uitest.New(nil)
	got, err := collectDemo(t, u, h, state.Installation{}, catalogWith(t), "")
	if err != nil {
		t.Fatal(err)
	}
	if got.UpstreamBranch != defaultBranch {
		t.Errorf("without releases the demo uses main, got %q", got.UpstreamBranch)
	}
	if warns := u.Warns(); len(warns) != 1 || !strings.Contains(warns[0], "rate limited") {
		t.Errorf("warns %v", warns)
	}
}

func TestCollectDemoInputNeedsAnOrganization(t *testing.T) {
	_, err := collectDemo(t, uitest.New(nil), gittest.New("octocat"), state.Installation{}, catalogWith(t), "")
	if err == nil || !strings.Contains(err.Error(), "not a member of any GitHub organization") {
		t.Errorf("got %v", err)
	}
}

func TestCollectDemoInputCancelled(t *testing.T) {
	_, err := collectDemo(t, uitest.New(map[string]any{"repo": uitest.Abort}), githubWith("v7.0.0"), state.Installation{}, catalogWith(t), "")
	if !errors.Is(err, ui.ErrAborted) {
		t.Errorf("got %v", err)
	}
}

func collectInstall(t *testing.T, u *uitest.UI, store *state.Catalog) (state.Installation, install.DNSCredentials, error) {
	t.Helper()
	t.Chdir(t.TempDir())
	kctx := kubeconfig.Context{Name: "prod", Server: "https://prod.example.com"}
	got, creds, _, err := collectInstallInput(context.Background(), u, githubWith("v7.1.0", "v7.0.0"), "octocat", kctx, store)
	return got, creds, err
}

func TestCollectInstallInputDefaults(t *testing.T) {
	u := uitest.New(map[string]any{"domain": "platform.example.com", "dns-token": "t0k"})
	got, creds, err := collectInstall(t, u, catalogWith(t))
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != state.Cluster || got.Context != "prod" || got.GitHost != git.GitHubName || got.TargetType != defaultTarget {
		t.Errorf("identity %+v", got)
	}
	if !strings.HasPrefix(got.Repo, "kubrix-") || got.UpstreamBranch != "v7.1.0" || got.CloudProvider != "on-prem" {
		t.Errorf("defaults %+v", got)
	}
	if got.DNSProvider != "cloudflare" || got.Domain != "platform.example.com" || creds.Token != "t0k" {
		t.Errorf("dns %+v %+v", got, creds)
	}
}

func TestCollectInstallInputOnlyAsksForTheCredentialsOfTheChosenDNSProvider(t *testing.T) {
	file := filepath.Join(t.TempDir(), "credentials")
	if err := os.WriteFile(file, []byte("[default]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		dns     string
		answers map[string]any
		asked   []string
		notAsk  []string
	}{
		"cloudflare": {"cloudflare", map[string]any{"dns-token": "t"}, []string{"dns-token"}, []string{"dns-project", "dns-file"}},
		"ionos":      {"ionos", map[string]any{"dns-token": "t"}, []string{"dns-token"}, []string{"dns-project", "dns-file"}},
		"stackit":    {"stackit", map[string]any{"dns-token": "t", "dns-project": "p"}, []string{"dns-token", "dns-project"}, []string{"dns-file"}},
		"aws":        {"aws", map[string]any{"dns-file": file}, []string{"dns-file"}, []string{"dns-token", "dns-project"}},
		"azure":      {"azure", map[string]any{"dns-file": file}, []string{"dns-file"}, []string{"dns-token", "dns-project"}},
		"manual":     {dnsManual, nil, nil, []string{"dns-token", "dns-project", "dns-file"}},
	} {
		t.Run(name, func(t *testing.T) {
			answers := map[string]any{"domain": "platform.example.com", "dns": tc.dns}
			for k, v := range tc.answers {
				answers[k] = v
			}
			u := uitest.New(answers)
			got, _, err := collectInstall(t, u, catalogWith(t))
			if err != nil {
				t.Fatal(err)
			}
			for _, key := range tc.asked {
				if !slices.Contains(u.Asked(), key) {
					t.Errorf("%s must be asked, asked %v", key, u.Asked())
				}
			}
			for _, key := range tc.notAsk {
				if slices.Contains(u.Asked(), key) {
					t.Errorf("%s must not be asked for %s", key, tc.dns)
				}
			}
			want := tc.dns
			if tc.dns == dnsManual {
				want = "none"
			}
			if got.DNSProvider != want {
				t.Errorf("DNS provider %q, want %q", got.DNSProvider, want)
			}
		})
	}
}

func TestCollectInstallInputExpandsTheCredentialsFilePath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.WriteFile(filepath.Join(home, "creds"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	u := uitest.New(map[string]any{"domain": "platform.example.com", "dns": "aws", "dns-file": "~/creds"})
	_, creds, err := collectInstall(t, u, catalogWith(t))
	if err != nil || creds.File != filepath.Join(home, "creds") {
		t.Errorf("file %q %v", creds.File, err)
	}
}

func TestCollectInstallInputRejectsBadAnswers(t *testing.T) {
	for name, answers := range map[string]map[string]any{
		"not a domain":            {"domain": "localhost"},
		"kubriX demo zone":        {"domain": "demo-abc.kubrix.cloud"},
		"empty token":             {"domain": "platform.example.com", "dns-token": ""},
		"missing credentials":     {"domain": "platform.example.com", "dns": "aws", "dns-file": "/does/not/exist"},
		"stackit without project": {"domain": "platform.example.com", "dns": "stackit", "dns-token": "t", "dns-project": ""},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := collectInstall(t, uitest.New(answers), catalogWith(t))
			var rejected *uitest.AnswerError
			if !errors.As(err, &rejected) {
				t.Errorf("got %v", err)
			}
		})
	}
}

func TestCollectInstallInputRerunKeepsTheSavedInstallation(t *testing.T) {
	store := catalogWith(t, state.Installation{
		Kind: state.Cluster, Context: "prod", Org: "beta", Repo: "platform", Domain: "platform.example.com",
		DNSProvider: "none", UpstreamBranch: "v7.0.0", ExcludedApps: []string{"kargo"}, CloudProvider: "aks",
	})
	u := uitest.New(nil)
	got, _, err := collectInstall(t, u, store)
	if err != nil {
		t.Fatal(err)
	}
	if got.Repo != "platform" || got.Domain != "platform.example.com" || got.UpstreamBranch != "v7.0.0" ||
		got.DNSProvider != "none" || got.CloudProvider != "aks" || !slices.Equal(got.ExcludedApps, []string{"kargo"}) {
		t.Errorf("got %+v", got)
	}
	if slices.Contains(u.Asked(), "dns-token") {
		t.Error("a saved manual DNS choice is offered again as manual")
	}
}

func TestCollectInstallInputStartsFromTheLastDemoButNotItsRepo(t *testing.T) {
	store := catalogWith(t, state.Installation{
		Kind: state.KindDemo, ClusterName: "kubrix-demo", Org: "beta", Repo: "demo-repo", Domain: kindDomain,
		GitUserName: "me", UpstreamRepo: "someone/kubriX", UpstreamBranch: "v7.0.0", ExcludedApps: []string{"kargo"},
	})
	u := uitest.New(map[string]any{"domain": "platform.example.com", "dns-token": "t"})
	got, _, err := collectInstall(t, u, store)
	if err != nil {
		t.Fatal(err)
	}
	if got.Repo == "demo-repo" || got.Domain != "platform.example.com" || len(got.ExcludedApps) != 0 {
		t.Errorf("repo, domain and exclusions belong to the demo: %+v", got)
	}
	if got.Org != "beta" || got.GitUserName != "me" || got.UpstreamRepo != "someone/kubriX" {
		t.Errorf("personal choices carry over: %+v", got)
	}
	if got.UpstreamBranch != "v7.1.0" {
		t.Errorf("a new cluster install defaults to the latest release, got %q", got.UpstreamBranch)
	}
}

func TestConfirmContext(t *testing.T) {
	kctx := kubeconfig.Context{Name: "prod", Server: "https://prod.example.com"}
	if err := confirmContext(uitest.New(map[string]any{"confirm-context": "prod"}), kctx); err != nil {
		t.Errorf("typing the name confirms: %v", err)
	}
	var rejected *uitest.AnswerError
	if err := confirmContext(uitest.New(map[string]any{"confirm-context": "staging"}), kctx); !errors.As(err, &rejected) {
		t.Errorf("another name must be rejected: %v", err)
	}
	if err := confirmContext(uitest.New(nil), kctx); !errors.As(err, &rejected) {
		t.Errorf("pressing enter without typing must be rejected: %v", err)
	}
	if err := confirmContext(uitest.New(map[string]any{"confirm-context": uitest.Abort}), kctx); !errors.Is(err, ui.ErrAborted) {
		t.Errorf("got %v", err)
	}
}

func TestPickDemoOffersNewAndSavedDemos(t *testing.T) {
	store := catalogWith(t,
		state.Installation{Kind: state.Cluster, Context: "prod", Org: "acme", Repo: "platform"},
		state.Installation{Kind: state.KindDemo, ClusterName: "kubrix-demo", Org: "acme", Repo: "first", TargetType: defaultTarget, UpstreamBranch: "v7.0.0"},
	)

	u := uitest.New(nil)
	got, original, err := pickDemo(u, store)
	if err != nil {
		t.Fatal(err)
	}
	if original != "" || got.Repo != "" || got.ClusterName != "kubrix-demo-2" || got.Kind != state.KindDemo {
		t.Errorf("a new demo starts without a repo and with a free cluster name: %+v (%q)", got, original)
	}
	if got.UpstreamBranch != "v7.0.0" {
		t.Errorf("a new demo starts from the last demo's choices: %+v", got)
	}

	rerun, original, err := pickDemo(uitest.New(map[string]any{"Which demo?": "kubrix-demo"}), store)
	if err != nil || original != "kubrix-demo" || rerun.Repo != "first" {
		t.Errorf("rerun %+v %q %v", rerun, original, err)
	}
}

func TestPickDemoWithoutSavedDemosDoesNotAsk(t *testing.T) {
	store := catalogWith(t, state.Installation{Kind: state.Cluster, Context: "prod"})
	u := uitest.New(nil)
	got, original, err := pickDemo(u, store)
	if err != nil || original != "" || got.ClusterName != state.DefaultClusterName || u.Forms() != 0 {
		t.Errorf("%+v %q %v forms %d", got, original, err, u.Forms())
	}
}

func TestPickUpgradeRepo(t *testing.T) {
	store := catalogWith(t, state.Installation{Kind: state.KindDemo, ClusterName: "kubrix-demo", Org: "acme", Repo: "demo", UpstreamRepo: "someone/kubriX", TargetType: "kind-base"})

	got, err := pickUpgradeRepo(uitest.New(map[string]any{"installation": "kubrix-demo"}), store)
	if err != nil {
		t.Fatal(err)
	}
	if got.org != "acme" || got.repo != "demo" || got.upstream != "someone/kubriX" || got.targetType != "kind-base" {
		t.Errorf("saved installation: %+v", got)
	}

	u := uitest.New(map[string]any{"installation": otherRepo, "repository": "beta/platform", "target-type": "kind"})
	got, err = pickUpgradeRepo(u, store)
	if err != nil {
		t.Fatal(err)
	}
	if got.org != "beta" || got.repo != "platform" || got.upstream != defaultUpstream || got.targetType != "kind" {
		t.Errorf("other repository: %+v", got)
	}

	var rejected *uitest.AnswerError
	if _, err := pickUpgradeRepo(uitest.New(map[string]any{"installation": otherRepo, "repository": "nope"}), store); !errors.As(err, &rejected) {
		t.Errorf("a bad repository slug must be rejected: %v", err)
	}

	// Without installations only another repository can be chosen, and it has no default.
	skipped := uitest.New(nil)
	if _, err := pickUpgradeRepo(skipped, catalogWith(t)); !errors.As(err, &rejected) || !slices.Contains(skipped.Asked(), "repository") {
		t.Errorf("asked %v: %v", skipped.Asked(), err)
	}
	typed := uitest.New(map[string]any{"repository": "beta/platform"})
	if got, err := pickUpgradeRepo(typed, catalogWith(t)); err != nil || got.org != "beta" || got.repo != "platform" {
		t.Errorf("%+v %v", got, err)
	}
}

func TestMenuMapsCancelToQuit(t *testing.T) {
	choice, err := menu(uitest.New(nil), catalogWith(t))
	if err != nil || choice != "demo" {
		t.Errorf("default %q %v", choice, err)
	}
	choice, err = menu(uitest.New(map[string]any{"What do you want to do?": "upgrade"}), catalogWith(t))
	if err != nil || choice != "upgrade" {
		t.Errorf("chosen %q %v", choice, err)
	}
	if _, err := menu(uitest.New(map[string]any{"What do you want to do?": uitest.Abort}), catalogWith(t)); !errors.Is(err, ui.ErrQuit) {
		t.Errorf("cancelling the menu quits: %v", err)
	}
}

func TestConfirmNotes(t *testing.T) {
	const key = "Open a pull request upgrading v7.0.0 to v8.0.0?"
	review := upgrade.Review{Installed: "v7.0.0", Target: "v8.0.0", Notes: "## [8.0.0]\n\n### Features\n"}
	u := uitest.New(nil)
	if err := confirmNotes(u, review); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(u.Asked(), []string{key}) || len(u.Headings()) != 1 {
		t.Errorf("asked %v headings %v", u.Asked(), u.Headings())
	}
	if err := confirmNotes(uitest.New(map[string]any{key: false}), upgrade.Review{Installed: "v7.0.0", Target: "v8.0.0"}); !errors.Is(err, ui.ErrAborted) {
		t.Errorf("declining aborts: %v", err)
	}
	breaking := uitest.New(nil)
	review.Breaking = "* vault is now openbao"
	review.Unreleased = 3
	if err := confirmNotes(breaking, review); err != nil {
		t.Fatal(err)
	}
	if want := "This upgrade has BREAKING CHANGES. Open the pull request?"; !slices.Equal(breaking.Asked(), []string{want}) {
		t.Errorf("breaking changes change the question: %v", breaking.Asked())
	}
}

// stubProposal stands in for an upgrade.Proposal in the pickers.
type stubProposal struct {
	installed string
	releases  []string
	targets   []string
	err       error
	set       string
}

func (s *stubProposal) Installed() (string, bool) { return s.installed, s.installed != "" }
func (s *stubProposal) Releases() []string        { return s.releases }
func (s *stubProposal) SetInstalled(r string) error {
	s.set, s.installed = r, r
	return nil
}
func (s *stubProposal) Targets() ([]string, error) { return s.targets, s.err }

func TestPickTarget(t *testing.T) {
	p := &stubProposal{installed: "v7.0.0", targets: []string{"v8.0.0", "v7.1.0", upgrade.MainTarget}}
	if got, err := pickTarget(uitest.New(nil), p); err != nil || got != "v8.0.0" {
		t.Errorf("default is the newest target: %q %v", got, err)
	}
	u := uitest.New(map[string]any{"Upgrade v7.0.0 to": "v7.1.0"})
	if got, err := pickTarget(u, p); err != nil || got != "v7.1.0" {
		t.Errorf("%q %v", got, err)
	}
	if _, err := pickTarget(uitest.New(map[string]any{"Upgrade v7.0.0 to": "v9.9.9"}), p); err == nil {
		t.Error("a target that was not offered must be rejected")
	}
	if _, err := pickTarget(uitest.New(map[string]any{"Upgrade v7.0.0 to": uitest.Abort}), p); !errors.Is(err, ui.ErrAborted) {
		t.Errorf("cancelling aborts: %v", err)
	}
}

func TestPickTargetEndsQuietlyWhenUpToDate(t *testing.T) {
	u := uitest.New(nil)
	_, err := pickTarget(u, &stubProposal{installed: "v8.0.0", err: upgrade.ErrUpToDate})
	if !errors.Is(err, ui.ErrQuit) || !slices.Equal(u.OKs(), []string{"already on the latest release v8.0.0"}) {
		t.Errorf("%v %v", err, u.OKs())
	}
	tooOld := &upgrade.TooOldError{Installed: "v5.0.0"}
	if _, err := pickTarget(uitest.New(nil), &stubProposal{installed: "v5.0.0", err: tooOld}); !errors.Is(err, tooOld) {
		t.Errorf("a too-old installation is an error the user must read: %v", err)
	}
}

func TestAskInstalledVersion(t *testing.T) {
	known := &stubProposal{installed: "v7.0.0", releases: []string{"v8.0.0", "v7.0.0"}}
	u := uitest.New(nil)
	if err := askInstalledVersion(u, known); err != nil || u.Forms() != 0 || known.set != "" {
		t.Errorf("a known version is not asked: %v forms %d", err, u.Forms())
	}

	unknown := &stubProposal{releases: []string{"v8.0.0", "v7.1.0", "v7.0.0"}}
	if err := askInstalledVersion(uitest.New(nil), unknown); err != nil || unknown.set != "v7.0.0" {
		t.Errorf("the default is the oldest release, so the user is offered every upgrade: %q %v", unknown.set, err)
	}
	chosen := &stubProposal{releases: []string{"v8.0.0", "v7.1.0", "v7.0.0"}}
	if err := askInstalledVersion(uitest.New(map[string]any{"Which kubriX version is installed?": "v7.1.0"}), chosen); err != nil || chosen.set != "v7.1.0" {
		t.Errorf("%q %v", chosen.set, err)
	}
	cancelled := &stubProposal{releases: []string{"v7.0.0"}}
	if err := askInstalledVersion(uitest.New(map[string]any{"Which kubriX version is installed?": uitest.Abort}), cancelled); !errors.Is(err, ui.ErrAborted) || cancelled.set != "" {
		t.Errorf("%v %q", err, cancelled.set)
	}
}
