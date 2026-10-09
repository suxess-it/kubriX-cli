package upgrade

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/suxess-it/kubrix-cli/internal/bootstrap"
	"github.com/suxess-it/kubrix-cli/internal/git/gittest"
	"github.com/suxess-it/kubrix-cli/internal/platformrepo"
	"github.com/suxess-it/kubrix-cli/internal/testrepo"
	"github.com/suxess-it/kubrix-cli/internal/version"
)

const targetType = testrepo.TargetType

// steps runs the steps and records their titles.
type steps struct{ titles []string }

func (s *steps) Step(ctx context.Context, title string, fn func(context.Context) error) error {
	s.titles = append(s.titles, title)
	return fn(ctx)
}

// fixture is a customer repository bootstrapped from upstream v7.0.0 for k8s with kargo excluded.
type fixture struct {
	upstream string
	customer string
	host     *gittest.Host
	steps    *steps
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	upstream, customer := testrepo.Upstream(t), testrepo.Bare(t)
	err := bootstrap.FromRelease(context.Background(), bootstrap.Options{
		UpstreamURL: upstream,
		Tag:         "v7.0.0",
		CustomerURL: customer,
		Config:      platformrepo.ConfigInput{ClusterType: "k8s", DNSProvider: "none", Domain: "example.com", Repo: "https://github.com/acme/demo.git", GitUser: "octocat"},
		TargetType:  targetType,
		Exclude:     []string{"kargo"},
	})
	if err != nil {
		t.Fatal(err)
	}
	host := gittest.New("octocat", "acme")
	host.URLs["acme/demo"] = customer
	return &fixture{upstream: upstream, customer: customer, host: host, steps: &steps{}}
}

func (f *fixture) repo() Repo {
	return Repo{Org: "acme", Name: "demo", Upstream: "suxess-it/kubriX", TargetType: targetType}
}

func (f *fixture) deps(allowMain bool) Deps {
	return Deps{Host: f.host, UpstreamURL: func(string) string { return f.upstream }, AllowMain: allowMain}
}

func (f *fixture) begin(t *testing.T) *Proposal {
	t.Helper()
	p, err := Begin(context.Background(), f.deps(false), f.repo(), f.steps)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}

// propose runs the whole staged flow to the pull request.
func (f *fixture) propose(t *testing.T, target string) (Review, Result) {
	t.Helper()
	p := f.begin(t)
	review, err := p.Choose(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.Propose(context.Background(), f.steps)
	if err != nil {
		t.Fatal(err)
	}
	return review, res
}

// branch checks out the pushed upgrade branch of the customer repository.
func (f *fixture) branch(t *testing.T, name string) string {
	t.Helper()
	dir := testrepo.Clone(t, f.customer)
	testrepo.Run(t, testrepo.Git(t, dir), "checkout", "-q", name)
	return dir
}

func apps(t *testing.T, dir string) []string {
	t.Helper()
	a, err := platformrepo.Apps([]byte(testrepo.Read(t, dir, platformrepo.TargetValuesPath(targetType))))
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestProposeMinorUpgrade(t *testing.T) {
	f := newFixture(t)
	p := f.begin(t)

	if installed, ok := p.Installed(); !ok || installed != "v7.0.0" {
		t.Fatalf("installed %q %v", installed, ok)
	}
	targets, err := p.Targets()
	if err != nil || !slices.Equal(targets, []string{"v8.0.0", "v7.1.0"}) {
		t.Fatalf("targets %v %v", targets, err)
	}
	review, err := p.Choose(context.Background(), "v7.1.0")
	if err != nil {
		t.Fatal(err)
	}
	if review.Installed != "v7.0.0" || review.Target != "v7.1.0" || review.Unreleased != 0 || review.Breaking != "" {
		t.Errorf("review %+v: bootstrapped from a tag, so no unreleased commits", review)
	}
	if !strings.HasPrefix(review.Notes, "## [7.1.0]") || strings.Contains(review.Notes, "7.0.0]") {
		t.Errorf("notes %q", review.Notes)
	}
	res, err := p.Propose(context.Background(), f.steps)
	if err != nil {
		t.Fatal(err)
	}

	if res.Branch != "kubrix-upgrade-v7.1.0" || !strings.HasSuffix(res.URL, "/acme/demo/pull/1") {
		t.Errorf("result %+v", res)
	}
	if !slices.Equal(res.Excluded, []string{"kargo"}) || !slices.Equal(res.Resolved, []string{platformrepo.ConfigPath}) || res.Removed != nil {
		t.Errorf("excluded %v resolved %v removed %v: only the customer-config conflict is resolved", res.Excluded, res.Resolved, res.Removed)
	}
	regenerated := slices.Sorted(slices.Values(res.Regenerated))
	if !slices.Equal(regenerated, []string{
		"platform-apps/charts/grafana/values-customer-generated.yaml",
		"platform-apps/charts/kargo/values-customer-generated.yaml",
	}) {
		t.Errorf("regenerated %v: only changed or new templates", regenerated)
	}
	if want := []string{"Cloning acme/demo", "Merging v7.1.0", "Re-rendering changed templates", "Pushing kubrix-upgrade-v7.1.0", "Opening the pull request"}; !slices.Equal(f.steps.titles, want) {
		t.Errorf("steps %v", f.steps.titles)
	}

	pushed := f.branch(t, res.Branch)
	if v := testrepo.Read(t, pushed, ".release-please-manifest.json"); !strings.Contains(v, "7.1.0") {
		t.Errorf("pushed branch must contain the release, manifest %q", v)
	}
	if got := testrepo.Read(t, pushed, "platform-apps/charts/grafana/values-customer-generated.yaml"); got != "host: grafana.example.com\n" {
		t.Errorf("grafana %q", got)
	}
	if got := apps(t, pushed); slices.Contains(got, "kargo") {
		t.Errorf("unchanged target values must stay as they were, got %v", got)
	}
	if cfg := testrepo.Read(t, pushed, platformrepo.ConfigPath); !strings.Contains(cfg, "domain: example.com") {
		t.Errorf("customer-config.yaml must keep the customer's values:\n%s", cfg)
	}
	if main := testrepo.Run(t, testrepo.Git(t, pushed), "log", "-1", "--format=%s", "origin/main"); main != bootstrap.CommitMessage {
		t.Errorf("main must stay untouched until the PR is merged, last commit %q", main)
	}

	if len(f.host.PRs) != 1 {
		t.Fatalf("PRs %+v", f.host.PRs)
	}
	pr := f.host.PRs[0]
	if pr.Owner != "acme" || pr.Repo != "demo" || pr.Head != "kubrix-upgrade-v7.1.0" || pr.Base != "main" || pr.Title != "chore(kubrix): upgrade kubriX v7.0.0 → v7.1.0" {
		t.Errorf("PR %+v", pr)
	}
	for _, want := range []string{"**v7.0.0** to **v7.1.0**", "`platform-apps/charts/kargo/values-customer-generated.yaml`", "`bootstrap/customer-config.yaml`", "Apps kept excluded", "`kargo`", "## [7.1.0]"} {
		if !strings.Contains(pr.Body, want) {
			t.Errorf("PR body is missing %q:\n%s", want, pr.Body)
		}
	}
	if strings.Contains(pr.Body, "WARNING") {
		t.Error("no warning without breaking changes")
	}
}

func TestProposeMajorUpgradeKeepsExclusionsAndWarnsAboutBreakingChanges(t *testing.T) {
	f := newFixture(t)
	review, res := f.propose(t, "v8.0.0")
	if !version.HasBreakingChanges(review.Notes) || review.Breaking == "" {
		t.Errorf("v8.0.0 notes must flag breaking changes: %+v", review)
	}
	pushed := f.branch(t, res.Branch)
	if got := apps(t, pushed); !slices.Equal(got, []string{"traefik", "external-dns", "backstage", "team-onboarding"}) {
		t.Errorf("apps %v: new app added, kargo still excluded, k8s-only app kept", got)
	}
	if body := f.host.PRs[0].Body; !strings.Contains(body, "[!WARNING]") || !strings.Contains(body, "vault is now openbao") && !strings.Contains(body, "BREAKING") {
		t.Errorf("PR body must carry the breaking changes:\n%s", body)
	}
}

// v8.0.0 renames charts/grafana to charts/grafana-v2. Git's directory-rename detection would move the
// customer's rendered grafana output along and conflict; instead generated files follow their template.
func TestProposeAcrossRenamedTemplateDir(t *testing.T) {
	f := newFixture(t)
	_, res := f.propose(t, "v8.0.0")
	old := "platform-apps/charts/grafana/values-customer-generated.yaml"
	moved := "platform-apps/charts/grafana-v2/values-customer-generated.yaml"
	if !slices.Contains(res.Removed, old) {
		t.Errorf("removed %v: the output of the moved template must go", res.Removed)
	}
	if !slices.Contains(res.Regenerated, moved) {
		t.Errorf("regenerated %v: the moved template must be rendered at its new place", res.Regenerated)
	}
	pushed := f.branch(t, res.Branch)
	if got := testrepo.Read(t, pushed, moved); got != "host: grafana.example.com\n" {
		t.Errorf("%s = %q", moved, got)
	}
	if out := testrepo.Run(t, testrepo.Git(t, pushed), "ls-files", old); out != "" {
		t.Errorf("%s must be deleted", old)
	}
	if !strings.Contains(f.host.PRs[0].Body, "Removed: rendered from templates this release no longer has") {
		t.Errorf("PR body must list the removed output:\n%s", f.host.PRs[0].Body)
	}
}

func TestProposeStopsOnManualConflictWithoutPushing(t *testing.T) {
	f := newFixture(t)
	edit := testrepo.Clone(t, f.customer)
	testrepo.Write(t, edit, map[string]string{"platform-apps/charts/keycloak/values-kubrix-default.yaml": "replicas: 3\n"})
	g := testrepo.Git(t, edit)
	testrepo.Run(t, g, "commit", "-q", "-am", "customer tuning")
	testrepo.Run(t, g, "push", "-q", "origin", "main")

	p := f.begin(t)
	if _, err := p.Choose(context.Background(), "v8.0.0"); err != nil {
		t.Fatal(err)
	}
	_, err := p.Propose(context.Background(), f.steps)
	var conflict *ConflictError
	if !errors.As(err, &conflict) || !slices.Equal(conflict.Files, []string{"platform-apps/charts/keycloak/values-kubrix-default.yaml"}) {
		t.Fatalf("expected a manual conflict on keycloak values, got %v", err)
	}
	for _, want := range []string{"acme/demo", "kubriX v8.0.0", "by hand", "nothing was pushed"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error must tell what to do, missing %q: %v", want, err)
		}
	}
	if branches := testrepo.Run(t, testrepo.Git(t, f.customer), "branch", "--list", BranchName("v8.0.0")); branches != "" {
		t.Error("nothing may be pushed when the upgrade stops")
	}
	if len(f.host.PRs) != 0 {
		t.Errorf("PRs %+v", f.host.PRs)
	}
}

// orphanCustomer publishes a repository that shares no history with kubriX.
func orphanCustomer(t *testing.T, f *fixture) {
	t.Helper()
	f.customer = testrepo.Bare(t)
	f.host.URLs["acme/demo"] = f.customer
	orphan := t.TempDir()
	g := testrepo.Git(t, orphan)
	testrepo.Run(t, g, "init", "-q", "-b", "main")
	testrepo.Write(t, orphan, map[string]string{"README.md": "published without history\n"})
	testrepo.Run(t, g, "add", "-A")
	testrepo.Run(t, g, "commit", "-q", "-m", "orphan")
	testrepo.Run(t, g, "push", "-q", f.customer, "main")
}

func TestUnrelatedHistoryIsRefusedBeforeAnythingIsChanged(t *testing.T) {
	f := newFixture(t)
	orphanCustomer(t, f)
	p := f.begin(t)
	if _, ok := p.Installed(); ok {
		t.Error("a repo without manifest has no known version")
	}
	if err := p.SetInstalled("v7.0.0"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Choose(context.Background(), "v7.1.0"); !errors.Is(err, ErrUnrelatedHistory) {
		t.Fatalf("got %v, want ErrUnrelatedHistory", err)
	}
	if _, err := p.Propose(context.Background(), f.steps); err == nil {
		t.Error("nothing was chosen, so there is nothing to propose")
	}
}

func TestInstalledVersionMustBeSetWhenTheRepositoryDoesNotSay(t *testing.T) {
	f := newFixture(t)
	orphanCustomer(t, f)
	p := f.begin(t)
	if _, err := p.Targets(); err == nil || !strings.Contains(err.Error(), "SetInstalled") {
		t.Errorf("targets before the installed version is known: %v", err)
	}
	if got := p.Releases(); !slices.Equal(got, []string{"v8.0.0", "v7.1.0", "v7.0.0"}) {
		t.Errorf("releases %v: the user picks the installed one from these", got)
	}
	for _, bad := range []string{"main", "7.0.0", "feat/x", ""} {
		if err := p.SetInstalled(bad); err == nil {
			t.Errorf("%q is not a release", bad)
		}
	}
}

func TestTargetsRules(t *testing.T) {
	f := newFixture(t)
	p := f.begin(t)
	set := func(v string) {
		t.Helper()
		if err := p.SetInstalled(v); err != nil {
			t.Fatal(err)
		}
	}

	set("v7.1.0")
	if got, err := p.Targets(); err != nil || !slices.Equal(got, []string{"v8.0.0"}) {
		t.Errorf("v7.1.0: %v %v", got, err)
	}
	set("v8.0.0")
	if _, err := p.Targets(); !errors.Is(err, ErrUpToDate) {
		t.Errorf("v8.0.0 is the latest: %v", err)
	}
	set("v6.0.0")
	if got, err := p.Targets(); err != nil || !slices.Equal(got, []string{"v7.1.0", "v7.0.0"}) {
		t.Errorf("v6.0.0 may still upgrade one major version at a time: %v %v", got, err)
	}
	set("v5.0.0")
	var tooOld *TooOldError
	if _, err := p.Targets(); !errors.As(err, &tooOld) || !strings.Contains(err.Error(), "v7.0.0") {
		t.Errorf("v5.0.0 has no supported release one major version ahead: %v", err)
	}

	withMain, err := Begin(context.Background(), f.deps(true), f.repo(), f.steps)
	if err != nil {
		t.Fatal(err)
	}
	defer withMain.Close()
	if err := withMain.SetInstalled("v8.0.0"); err != nil {
		t.Fatal(err)
	}
	if got, err := withMain.Targets(); err != nil || !slices.Equal(got, []string{MainTarget}) {
		t.Errorf("contributors can upgrade to main even on the latest release: %v %v", got, err)
	}
	review, err := withMain.Choose(context.Background(), MainTarget)
	if err != nil || review.Target != MainTarget {
		t.Errorf("main: %+v %v", review, err)
	}
}

func TestStagesMustBeFollowedInOrder(t *testing.T) {
	f := newFixture(t)
	p := f.begin(t)
	if _, err := p.Propose(context.Background(), f.steps); err == nil || !strings.Contains(err.Error(), "choose a target") {
		t.Errorf("propose before choose: %v", err)
	}
	if _, err := p.Choose(context.Background(), "v9.9.9"); err == nil || !strings.Contains(err.Error(), "not one of the targets") {
		t.Errorf("a target that was not offered: %v", err)
	}
	if _, err := p.Choose(context.Background(), MainTarget); err == nil {
		t.Error("main is only offered to contributors")
	}
	if _, err := p.Choose(context.Background(), "v7.1.0"); err != nil {
		t.Fatal(err)
	}
	if err := p.SetInstalled("v7.1.0"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Propose(context.Background(), f.steps); err == nil {
		t.Error("changing the installed version invalidates the chosen target")
	}
}

func TestBeginFailures(t *testing.T) {
	f := newFixture(t)
	f.host.URLs["acme/demo"] = "/does/not/exist"
	if _, err := Begin(context.Background(), f.deps(false), f.repo(), f.steps); err == nil {
		t.Error("an unreachable repository must fail")
	}

	// Neither a manifest nor releases to choose the installed version from.
	f = newFixture(t)
	orphanCustomer(t, f)
	empty := t.TempDir()
	testrepo.Run(t, testrepo.Git(t, empty), "init", "-q", "-b", "main")
	f.upstream = empty
	if _, err := Begin(context.Background(), f.deps(false), f.repo(), f.steps); err == nil {
		t.Error("fetching an upstream without main must fail")
	}
}

func TestFailedPullRequestKeepsTheBranchAndCanBeRetried(t *testing.T) {
	f := newFixture(t)
	f.host.PRErr = errors.New("rate limited")
	p := f.begin(t)
	if _, err := p.Choose(context.Background(), "v7.1.0"); err != nil {
		t.Fatal(err)
	}
	res, err := p.Propose(context.Background(), f.steps)
	if err == nil || !errors.Is(err, f.host.PRErr) {
		t.Fatalf("got %v", err)
	}
	for _, want := range []string{"pushed branch kubrix-upgrade-v7.1.0", "opening the pull request failed", "rate limited", "run the upgrade again"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error must say what happened, missing %q: %v", want, err)
		}
	}
	if res.Branch != "kubrix-upgrade-v7.1.0" || res.URL != "" {
		t.Errorf("result %+v", res)
	}
	if branches := testrepo.Run(t, testrepo.Git(t, f.customer), "branch", "--list", res.Branch); branches == "" {
		t.Error("the branch stays pushed")
	}

	// Run it again: the same branch is force-pushed and the pull request goes through.
	f.host.PRErr = nil
	retry := f.begin(t)
	if _, err := retry.Choose(context.Background(), "v7.1.0"); err != nil {
		t.Fatal(err)
	}
	again, err := retry.Propose(context.Background(), f.steps)
	if err != nil {
		t.Fatal(err)
	}
	if again.Branch != res.Branch || len(f.host.PRs) != 1 || f.host.PRs[0].Head != res.Branch {
		t.Errorf("retry %+v PRs %+v", again, f.host.PRs)
	}
}

func TestPRBody(t *testing.T) {
	notes := "## [7.0.0](x) (2026-04-14)\n\n### ⚠ BREAKING CHANGES\n\n* vault is now openbao\n"
	merged := mergeResult{Resolved: []string{"bootstrap/customer-config.yaml"}, Removed: []string{"platform-apps/charts/vault/values-customer-generated.yaml"}}
	review := Review{Installed: "v6.0.0", Target: "v7.0.0", Notes: notes, Breaking: version.BreakingChanges(notes), Unreleased: 3}
	body := prBody(review, merged, []string{"platform-apps/charts/grafana/values-customer-generated.yaml"}, []string{"kargo"})
	for _, want := range []string{"**v6.0.0** to **v7.0.0**", "3 unreleased upstream commits", "[!WARNING]", "`bootstrap/customer-config.yaml`", "`platform-apps/charts/vault/values-customer-generated.yaml`", "`platform-apps/charts/grafana/values-customer-generated.yaml`", "`kargo`", "vault is now openbao"} {
		if !strings.Contains(body, want) {
			t.Errorf("PR body is missing %q:\n%s", want, body)
		}
	}
	plain := prBody(Review{Installed: "v7.0.0", Target: "v7.1.0", Notes: "## [7.1.0]\n\n### Features\n"}, mergeResult{}, nil, nil)
	if strings.Contains(plain, "WARNING") {
		t.Error("no warning without breaking changes")
	}
}
