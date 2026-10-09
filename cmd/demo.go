package cmd

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"slices"

	"github.com/spf13/cobra"

	"github.com/suxess-it/kubrix-cli/internal/git"
	"github.com/suxess-it/kubrix-cli/internal/gitinfo"
	"github.com/suxess-it/kubrix-cli/internal/install"
	"github.com/suxess-it/kubrix-cli/internal/kindcluster"
	"github.com/suxess-it/kubrix-cli/internal/state"
	"github.com/suxess-it/kubrix-cli/internal/ui"
)

const kindDomain = "127-0-0-1.nip.io"

var untestedTargets = []string{"kind", "kind-base", "kind-delivery", "kind-observability", "kind-portal", "kind-security"}

func newDemoCmd(f Features) *cobra.Command {
	return &cobra.Command{
		Use:   "demo",
		Short: "Bootstrap a kubriX demo platform on a local kind cluster",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withUI(cmd, "kind demo", func(ctx context.Context, u ui.UI) error { return runDemo(ctx, u, productionServices(f)) })
		},
	}
}

func runDemo(ctx context.Context, u ui.UI, svc services) error {
	u.Heading("kubriX kind demo")

	catalog := openCatalog(u)
	base, original, err := pickDemo(u, catalog)
	if err != nil {
		return err
	}

	var resources kindcluster.Resources
	if err := u.Step(ctx, "Checking Docker", func(context.Context) (err error) {
		resources, err = svc.docker()
		return err
	}); err != nil {
		return err
	}
	gh, login, err := svc.github(ctx, u)
	if err != nil {
		return err
	}
	u.OK("GitHub: authenticated as " + login)

	sel, contributor, err := collectDemoInput(ctx, u, gh, login, base, catalog, original)
	if err != nil {
		return err
	}
	if err := checkContributorBranch(u, contributor, sel); err != nil {
		return err
	}

	reuse, err := askReuseCluster(u, svc.kind, sel.ClusterName)
	if err != nil {
		return err
	}
	caDir, err := state.Dir()
	if err != nil {
		return err
	}
	target := &install.KindTarget{Clusters: svc.kind, Name: sel.ClusterName, Reuse: reuse, CADir: caDir}

	plan, err := inspect(ctx, u, svc.installDeps(gh), target, sel)
	if err != nil {
		return err
	}
	if !resources.Sufficient() {
		msg := fmt.Sprintf("Docker has %s; the tested setup is at least %d CPUs and 16 GB. The install may stall.", resources, kindcluster.MinCPUs)
		u.Warn(msg)
		if ok, err := ui.Confirm(u, "Continue anyway?", msg, false); err != nil || !ok {
			return abortErr(err)
		}
	}
	return plan.Run(ctx, u, recordInstallation(u, catalog))
}

// pickDemo returns the selections to start from and, for a saved demo, its cluster name.
func pickDemo(u ui.UI, catalog *state.Catalog) (state.Installation, string, error) {
	keys := catalog.Demos()
	if len(keys) == 0 {
		return catalog.NewDemo(), "", nil
	}
	options := []ui.Option{{Label: "New demo", Value: ""}}
	for _, key := range keys {
		d, _ := catalog.Find(key)
		options = append(options, ui.Option{Label: fmt.Sprintf("%s  (%s/%s, %s)", key, d.Org, d.Repo, d.TargetType), Value: key})
	}
	choice := ""
	if err := ui.Select(u, "Which demo?", "Start a new demo or rerun a saved one.", options, &choice); err != nil {
		return state.Installation{}, "", abortErr(err)
	}
	if choice == "" {
		return catalog.NewDemo(), "", nil
	}
	d, _ := catalog.Find(choice)
	return d, choice, nil
}

func collectDemoInput(ctx context.Context, u ui.UI, gh githubAPI, login string, base state.Installation, catalog *state.Catalog, original string) (state.Installation, *gitinfo.Info, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return state.Installation{}, nil, err
	}
	contributor, err := gitinfo.Detect(cwd)
	if err != nil {
		return state.Installation{}, nil, err
	}

	in := &base
	in.Kind, in.GitHost = state.KindDemo, cmp.Or(base.GitHost, git.GitHubName)
	in.Domain, in.DNSProvider, in.CloudProvider = kindDomain, "none", ""
	in.UpstreamRepo = cmp.Or(base.UpstreamRepo, defaultUpstream)
	sourceHint := "Releases are bootstrapped by the CLI; branches by the installer."
	if contributor != nil {
		// The checkout reflects what the contributor wants to test right now, so it beats saved state.
		in.UpstreamRepo = contributor.Origin.Slug()
		sourceHint = "Contributor mode: defaults to the branch checked out in " + contributor.Root + "."
		u.Info(fmt.Sprintf("Contributor mode: testing %s@%s", in.UpstreamRepo, contributor.Branch))
	}
	in.GitUserName = cmp.Or(base.GitUserName, login)
	in.TargetType = cmp.Or(base.TargetType, defaultTarget)
	in.ClusterName = cmp.Or(base.ClusterName, state.DefaultClusterName)
	if in.Repo == "" {
		in.Repo = "kubrix-demo-" + randomSuffix()
	}
	clusterNameFree := func(name string) error { return catalog.ClusterNameFree(name, original) }

	orgs, err := loadOrgs(ctx, u, gh, login)
	if err != nil {
		return state.Installation{}, nil, err
	}
	if !slices.Contains(orgs, in.Org) {
		in.Org = orgs[0]
	}
	releases := loadReleases(ctx, u, gh, in.UpstreamRepo)
	vc := newVersionChoice(releases, contributor, base.UpstreamBranch, true)
	versionSelect, otherBranchPage := vc.fields(sourceHint)

	form := ui.Form{Pages: []ui.Page{
		{Title: "GitHub", Fields: []ui.Field{
			ui.Choice{Key: "org", Title: "GitHub organization", Description: "The demo repository is created here.", Options: ui.Options(orgs...), Value: &in.Org},
			ui.Input{Key: "repo", Title: "Repository name", Description: "Created as a private repository if it does not exist.", Validate: matches(repoNameRe, "letters, digits, '.', '-' and '_'"), Value: &in.Repo},
			ui.Input{Key: "git-user", Title: "Git user name", Description: "Used for commits made by the platform.", Validate: nonEmpty("git user name"), Value: &in.GitUserName},
		}},
		{Title: "Platform", Fields: []ui.Field{
			ui.Choice{Key: "target-type", Title: "Target type", Options: targetOptions(untestedTargets), Value: &in.TargetType},
			ui.Input{Key: "upstream", Title: "kubriX source repository", Description: "owner/repo the installer clones from.", Validate: matches(repoSlugRe, "owner/repo"), Value: &in.UpstreamRepo},
			versionSelect,
			ui.Input{Key: "cluster-name", Title: "kind cluster name", Validate: clusterNameFree, Value: &in.ClusterName},
		}},
		otherBranchPage,
	}}
	if err := u.Ask(form); err != nil {
		return state.Installation{}, nil, abortErr(err)
	}
	in.UpstreamBranch = vc.ref()
	return *in, contributor, nil
}

// askReuseCluster asks whether an existing kind cluster of that name is reused; a new name is never reused.
func askReuseCluster(u ui.UI, clusters install.KindClusters, name string) (bool, error) {
	exists, err := clusters.Exists(name)
	if err != nil || !exists {
		return false, err
	}
	choice := "recreate"
	if err := ui.Select(u, fmt.Sprintf("kind cluster %q already exists", name),
		"Reusing it removes the previous install job and runs the installer again.",
		[]ui.Option{{Label: "Delete and recreate", Value: "recreate"}, {Label: "Reuse it", Value: "reuse"}},
		&choice); err != nil {
		return false, abortErr(err)
	}
	return choice == "reuse", nil
}

func randomSuffix() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
