package cmd

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/suxess-it/kubrix-cli/internal/git"
	"github.com/suxess-it/kubrix-cli/internal/gitinfo"
	"github.com/suxess-it/kubrix-cli/internal/install"
	"github.com/suxess-it/kubrix-cli/internal/kubeconfig"
	"github.com/suxess-it/kubrix-cli/internal/state"
	"github.com/suxess-it/kubrix-cli/internal/ui"
)

const (
	dnsManual       = "manual"
	kubrixDemoZone  = "kubrix.cloud"
	defaultProvider = "on-prem"
)

var domainRe = regexp.MustCompile(`^([a-z0-9]([-a-z0-9]*[a-z0-9])?\.)+[a-z]{2,}$`)

func newInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Install kubriX on an existing cluster (a kubeconfig context)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withUI(cmd, "install", func(ctx context.Context, u ui.UI) error { return runInstall(ctx, u, productionServices()) })
		},
	}
}

func expandHome(path string) string {
	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		if home, err := os.UserHomeDir(); err == nil {
			return home + "/" + rest
		}
	}
	return path
}

func validDomain(d string) error {
	if !domainRe.MatchString(d) {
		return errors.New("enter a domain like platform.example.com")
	}
	if d == kubrixDemoZone || strings.HasSuffix(d, "."+kubrixDemoZone) {
		return errors.New(kubrixDemoZone + " is kubriX's own demo zone; use a domain you control")
	}
	return nil
}

func runInstall(ctx context.Context, u ui.UI, svc services) error {
	u.Heading("Install kubriX on an existing cluster")

	kctx, err := pickContext(u, svc.contexts)
	if err != nil {
		return err
	}
	kc, err := svc.cluster(kctx.Name)
	if err != nil {
		return err
	}
	if err := preflight(ctx, u, kc, kctx); err != nil {
		return err
	}

	gh, login, err := svc.github(ctx, u)
	if err != nil {
		return err
	}
	u.OK("GitHub: authenticated as " + login)

	catalog := openCatalog(u)
	sel, creds, contributor, err := collectInstallInput(ctx, u, gh, login, kctx, catalog)
	if err != nil {
		return err
	}
	if err := checkContributorBranch(u, contributor, sel); err != nil {
		return err
	}

	target := &install.ClusterTarget{Kube: kc, Domain: sel.Domain, DNSProvider: sel.DNSProvider, DNS: creds}
	plan, err := inspect(ctx, u, svc.installDeps(gh), target, sel)
	if err != nil {
		return err
	}
	if err := confirmContext(u, kctx); err != nil {
		return err
	}
	return plan.Run(ctx, u, recordInstallation(u, catalog))
}

func pickContext(u ui.UI, listContexts func() ([]kubeconfig.Context, string, error)) (kubeconfig.Context, error) {
	contexts, current, err := listContexts()
	if err != nil {
		return kubeconfig.Context{}, err
	}
	if len(contexts) == 0 {
		return kubeconfig.Context{}, errors.New("no kubeconfig contexts found; set up access to your cluster first")
	}
	var options []ui.Option
	for _, c := range contexts {
		label := c.Name + "  (" + c.Server + ")"
		if c.Name == current {
			label += "  ← current"
		}
		options = append(options, ui.Option{Label: label, Value: c.Name})
	}
	choice := cmp.Or(current, contexts[0].Name)
	if err := ui.Select(u, "Which cluster?", "kubriX is installed into the cluster of this kubeconfig context.", options, &choice); err != nil {
		return kubeconfig.Context{}, abortErr(err)
	}
	i := slices.IndexFunc(contexts, func(c kubeconfig.Context) bool { return c.Name == choice })
	if contexts[i].IsKind() {
		return kubeconfig.Context{}, errors.New(choice + " is a kind cluster; use 'kubrix demo' for kind")
	}
	return contexts[i], nil
}

func preflight(ctx context.Context, u ui.UI, kc clusterAccess, kctx kubeconfig.Context) error {
	var admin, storage bool
	if err := u.Step(ctx, "Checking cluster "+kctx.Name, func(ctx context.Context) (err error) {
		if admin, err = kc.IsClusterAdmin(ctx); err != nil {
			return fmt.Errorf("cannot reach %s: %w", kctx.Server, err)
		}
		storage, err = kc.HasDefaultStorageClass(ctx)
		return err
	}); err != nil {
		return err
	}
	if !admin {
		return errors.New("your kubeconfig user is not cluster-admin; the kubriX installer needs cluster-admin rights")
	}
	if !storage {
		msg := "The cluster has no default StorageClass. Grafana, Mimir, MinIO and the Postgres databases need persistent volumes."
		u.Warn(msg)
		if ok, err := ui.Confirm(u, "Continue without a default StorageClass?", msg, false); err != nil || !ok {
			return abortErr(err)
		}
	}
	return nil
}

func confirmContext(u ui.UI, kctx kubeconfig.Context) error {
	typed := ""
	err := u.Ask(ui.Form{Pages: []ui.Page{{Fields: []ui.Field{ui.Input{
		Key:         "confirm-context",
		Title:       "Type the context name to install kubriX into it",
		Description: fmt.Sprintf("Context %s (%s). The installer gets cluster-admin rights there.", kctx.Name, kctx.Server),
		Validate: func(s string) error {
			if s != kctx.Name {
				return fmt.Errorf("type %q to confirm", kctx.Name)
			}
			return nil
		},
		Value: &typed,
	}}}}})
	return err
}

func collectInstallInput(ctx context.Context, u ui.UI, gh githubAPI, login string, kctx kubeconfig.Context, catalog *state.Catalog) (state.Installation, install.DNSCredentials, *gitinfo.Info, error) {
	var creds install.DNSCredentials
	cwd, err := os.Getwd()
	if err != nil {
		return state.Installation{}, creds, nil, err
	}
	contributor, err := gitinfo.Detect(cwd)
	if err != nil {
		return state.Installation{}, creds, nil, err
	}

	base, rerun := catalog.NewInstall(kctx.Name)
	in := &base
	in.Kind, in.Context, in.TargetType = state.Cluster, kctx.Name, defaultTarget
	in.GitHost = cmp.Or(base.GitHost, git.GitHubName)
	in.UpstreamRepo = cmp.Or(base.UpstreamRepo, defaultUpstream)
	if contributor != nil {
		in.UpstreamRepo = contributor.Origin.Slug()
	}
	in.GitUserName = cmp.Or(base.GitUserName, login)
	in.CloudProvider = cmp.Or(base.CloudProvider, defaultProvider)
	dnsChoice := cmp.Or(base.DNSProvider, "cloudflare")
	if dnsChoice == "none" {
		dnsChoice = dnsManual
	}
	if in.Repo == "" {
		in.Repo = "kubrix-" + randomSuffix()
	}

	orgs, err := loadOrgs(ctx, u, gh, login)
	if err != nil {
		return state.Installation{}, creds, nil, err
	}
	if !slices.Contains(orgs, in.Org) {
		in.Org = orgs[0]
	}
	releases := loadReleases(ctx, u, gh, in.UpstreamRepo)
	current := base.UpstreamBranch
	if !rerun {
		current = ""
	}
	vc := newVersionChoice(releases, contributor, current, false)
	versionSelect, otherBranchPage := vc.fields("Installs on existing clusters default to the latest release.")

	usesToken := func() bool { return slices.Contains([]string{"cloudflare", "ionos", "stackit"}, dnsChoice) }
	usesFile := func() bool { return dnsChoice == "aws" || dnsChoice == "azure" }
	fileHint := map[string]string{
		"aws":   "Path to an AWS shared-credentials file (e.g. ~/.aws/credentials) for Route 53; see aws-resources/route53-iam-policy.json.",
		"azure": "Path to the azure.json external-dns uses for Azure DNS.",
	}

	form := ui.Form{Pages: []ui.Page{
		{Title: "GitHub", Fields: []ui.Field{
			ui.Choice{Key: "org", Title: "GitHub organization", Description: "The platform repository is created here.", Options: ui.Options(orgs...), Value: &in.Org},
			ui.Input{Key: "repo", Title: "Repository name", Description: "Created as a private repository if it does not exist.", Validate: matches(repoNameRe, "letters, digits, '.', '-' and '_'"), Value: &in.Repo},
			ui.Input{Key: "git-user", Title: "Git user name", Description: "Used for commits made by the platform.", Validate: nonEmpty("git user name"), Value: &in.GitUserName},
		}},
		{Title: "Platform", Description: "Target type: " + defaultTarget + ". The cluster needs LoadBalancer support and a default StorageClass.", Fields: []ui.Field{
			ui.Input{Key: "upstream", Title: "kubriX source repository", Description: "owner/repo the installer clones from.", Validate: matches(repoSlugRe, "owner/repo"), Value: &in.UpstreamRepo},
			versionSelect,
			ui.Choice{Key: "cloud-provider", Title: "Cloud provider", Options: []ui.Option{{Label: "on-prem / generic Kubernetes", Value: "on-prem"}, {Label: "Azure AKS", Value: "aks"}}, Value: &in.CloudProvider},
		}},
		otherBranchPage,
		{Title: "DNS", Fields: []ui.Field{
			ui.Input{Key: "domain", Title: "Domain", Description: "Platform apps get <app>.<domain>. It must resolve publicly to the cluster's LoadBalancer and port 80 must be reachable from the internet, because certificates come from Let's Encrypt.", Validate: validDomain, Value: &in.Domain},
			ui.Choice{Key: "dns", Title: "DNS", Description: "external-dns manages the records with a provider, or you create one wildcard record yourself.", Options: []ui.Option{
				{Label: "Cloudflare", Value: "cloudflare"},
				{Label: "IONOS", Value: "ionos"},
				{Label: "STACKIT", Value: "stackit"},
				{Label: "AWS Route 53", Value: "aws"},
				{Label: "Azure DNS", Value: "azure"},
				{Label: "Manual (wildcard record, external-dns not installed)", Value: dnsManual},
			}, Value: &dnsChoice},
		}},
		{Hidden: func() bool { return !usesToken() }, Fields: []ui.Field{
			ui.Input{Key: "dns-token", Title: "API token", Description: "Stored only as a Secret in the cluster's external-dns namespace.", Secret: true, Validate: nonEmpty("token"), Value: &creds.Token},
		}},
		{Hidden: func() bool { return dnsChoice != "stackit" }, Fields: []ui.Field{
			ui.Input{Key: "dns-project", Title: "STACKIT project ID", Validate: nonEmpty("project ID"), Value: &creds.ProjectID},
		}},
		{Hidden: func() bool { return !usesFile() }, Fields: []ui.Field{
			ui.Input{Key: "dns-file", Title: "Credentials file", DescriptionFunc: func() string { return fileHint[dnsChoice] }, Watch: &dnsChoice,
				Validate: func(p string) error {
					if _, err := os.Stat(expandHome(p)); err != nil {
						return errors.New("file not found")
					}
					return nil
				}, Value: &creds.File},
		}},
	}}
	if err := u.Ask(form); err != nil {
		return state.Installation{}, creds, nil, abortErr(err)
	}
	in.UpstreamBranch = vc.ref()
	in.DNSProvider = dnsChoice
	if dnsChoice == dnsManual {
		in.DNSProvider = "none"
	}
	creds.File = expandHome(creds.File)
	return *in, creds, contributor, nil
}
