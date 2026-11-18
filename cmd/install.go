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

const installLong = `Install kubriX on the cluster of a kubeconfig context.

Without -i the install runs without questions. Its settings come from flags, from KUBRIX_* environment
variables (--upstream-branch is KUBRIX_UPSTREAM_BRANCH) and from a YAML --config file, in that order of
precedence. A kind-<name> context reuses the kind cluster <name>, like 'kubrix demo'.

Secrets are only read from the environment:
  KUBRIX_REPO_PASSWORD           GitHub token (else GITHUB_TOKEN, GH_TOKEN or the gh CLI's)
  KUBRIX_UPSTREAM_REPO_PASSWORD  token for a private kubriX source repository
  KUBRIX_DNS_TOKEN               API token of cloudflare, ionos or stackit

Every other KUBRIX_* variable, and the config file's extra-env map, is passed on to the platform installer.

With -i the installer asks for the settings instead.`

func newInstallCmd(f Features) *cobra.Command {
	cmd := &cobra.Command{
		Use:    "install",
		Short:  "Install kubriX on an existing cluster (a kubeconfig context) (experimental)",
		Long:   installLong,
		Args:   cobra.NoArgs,
		Hidden: !f.Experimental,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := f.requireExperimental("install"); err != nil {
				return err
			}
			if interactive, _ := cmd.Flags().GetBool("interactive"); interactive {
				for _, name := range append([]string{"config"}, installFlagNames()...) {
					if cmd.Flags().Changed(name) {
						return fmt.Errorf("--%s cannot be combined with --interactive, which asks for the settings", name)
					}
				}
				return withUI(cmd, "install", func(ctx context.Context, u ui.UI) error { return runInstall(ctx, u, productionServices(f)) })
			}
			configFile, _ := cmd.Flags().GetString("config")
			cfg, err := loadInstallConfig(cmd.Flags(), configFile)
			if err != nil {
				return err
			}
			return runInstallFromConfig(cmd.Context(), noPrompts{ui.NewPlain(cmd.OutOrStdout())}, productionServices(f), cfg)
		},
	}
	cmd.Flags().BoolP("interactive", "i", false, "ask for the settings")
	cmd.Flags().String("config", "", "YAML file with the settings; flags and KUBRIX_* variables take precedence")
	addInstallFlags(cmd.Flags())
	return cmd
}

// noPrompts is the UI of a non-interactive install, which never asks.
type noPrompts struct{ ui.UI }

func (noPrompts) Ask(ui.Form) error {
	return errors.New("kubrix install does not ask without --interactive; this is a bug")
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

const noStorageClassMsg = "The cluster has no default StorageClass. Grafana, Mimir, MinIO and the Postgres databases need persistent volumes."

func preflight(ctx context.Context, u ui.UI, kc clusterAccess, kctx kubeconfig.Context) error {
	storage, err := checkCluster(ctx, u, kc, kctx)
	if err != nil {
		return err
	}
	if !storage {
		u.Warn(noStorageClassMsg)
		if ok, err := ui.Confirm(u, "Continue without a default StorageClass?", noStorageClassMsg, false); err != nil || !ok {
			return abortErr(err)
		}
	}
	return nil
}

// checkCluster requires cluster-admin rights and reports whether the cluster has a default StorageClass.
func checkCluster(ctx context.Context, u ui.UI, kc clusterAccess, kctx kubeconfig.Context) (bool, error) {
	var admin, storage bool
	if err := u.Step(ctx, "Checking cluster "+kctx.Name, func(ctx context.Context) (err error) {
		if admin, err = kc.IsClusterAdmin(ctx); err != nil {
			return fmt.Errorf("cannot reach %s: %w", kctx.Server, err)
		}
		storage, err = kc.HasDefaultStorageClass(ctx)
		return err
	}); err != nil {
		return false, err
	}
	if !admin {
		return false, errors.New("your kubeconfig user is not cluster-admin; the kubriX installer needs cluster-admin rights")
	}
	return storage, nil
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

	usesToken := func() bool { return dnsUsesToken(dnsChoice) }
	usesFile := func() bool { return dnsUsesFile(dnsChoice) }
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

// dnsProviders are the DNS providers of an existing cluster; install.DNSManual means no provider.
var dnsProviders = []string{"cloudflare", "ionos", "stackit", "aws", "azure", install.DNSManual}

func dnsUsesToken(provider string) bool {
	return slices.Contains([]string{"cloudflare", "ionos", "stackit"}, provider)
}

func dnsUsesFile(provider string) bool { return provider == "aws" || provider == "azure" }

// checkDNSCredentials asks of a non-interactive install what the interactive one asks for the provider.
func checkDNSCredentials(provider string, creds install.DNSCredentials) error {
	switch {
	case dnsUsesToken(provider) && creds.Token == "":
		return fmt.Errorf("%s needs an API token: set %s", provider, dnsTokenEnv)
	case provider == "stackit" && creds.ProjectID == "":
		return errors.New("stackit needs dns-project-id")
	case dnsUsesFile(provider) && creds.File == "":
		return fmt.Errorf("%s needs dns-credentials-file", provider)
	case dnsUsesFile(provider):
		if _, err := os.Stat(creds.File); err != nil {
			return fmt.Errorf("dns-credentials-file: %w", err)
		}
	}
	return nil
}

// runInstallFromConfig is the non-interactive install: cfg answers every question the interactive one asks.
func runInstallFromConfig(ctx context.Context, u ui.UI, svc services, cfg installConfig) error {
	u.Heading("Install kubriX into context " + cfg.Context)

	contexts, _, err := svc.contexts()
	if err != nil {
		return err
	}
	i := slices.IndexFunc(contexts, func(c kubeconfig.Context) bool { return c.Name == cfg.Context })
	if i < 0 {
		return fmt.Errorf("no kubeconfig context %q", cfg.Context)
	}
	sel := state.Installation{
		GitHost: git.GitHubName, Org: cfg.Org, Repo: cfg.Repo, GitUserName: cfg.GitUserName, TargetType: cfg.TargetType,
		UpstreamRepo: cfg.UpstreamRepo, UpstreamBranch: cfg.UpstreamBranch, ExcludedApps: cfg.ExcludedApps,
	}
	target, err := configTarget(ctx, u, svc, cfg, contexts[i], &sel)
	if err != nil {
		return err
	}

	gh, err := svc.githubToken(cfg.RepoToken)
	if err != nil {
		return err
	}
	if sel.GitUserName == "" {
		if err := u.Step(ctx, "Checking GitHub token", func(ctx context.Context) (err error) {
			sel.GitUserName, err = gh.Validate(ctx)
			return err
		}); err != nil {
			return fmt.Errorf("%w (with a token that has no user, set %s)", err, envName("git-user-name"))
		}
	}

	catalog := openCatalog(u)
	deps := install.Deps{Host: gh, Upstream: svc.upstream(cmp.Or(cfg.UpstreamToken, gh.Token()))}
	opts := install.Options{
		InstallerTags:  cfg.InstallerTags,
		UpstreamToken:  cfg.UpstreamToken,
		MaxWaitSeconds: cfg.MaxWaitSeconds,
		Extra:          cfg.Extra,
		HideSecrets:    true,
	}
	plan, err := install.InspectWith(ctx, deps, target, sel, opts, u)
	if err != nil {
		return err
	}
	if !plan.Fresh {
		u.Info(fmt.Sprintf("%s/%s already has content: installing from it without bootstrapping", sel.Org, sel.Repo))
	}
	warnIgnoredExcludes(u, plan, sel)
	if plan.ImageFallback != "" {
		u.Warn(plan.ImageFallback + " Using the 'latest' installer image.")
	}
	return plan.Run(ctx, u, recordInstallation(u, catalog))
}

// configTarget is the target of the context: a kind cluster that is reused, or an existing cluster. It completes
// sel with what the target decides.
func configTarget(ctx context.Context, u ui.UI, svc services, cfg installConfig, kctx kubeconfig.Context, sel *state.Installation) (install.Target, error) {
	clusterType := "k8s"
	if kctx.IsKind() {
		clusterType = "kind"
	}
	if cfg.ClusterType != "" && cfg.ClusterType != clusterType {
		return nil, fmt.Errorf("%s is %s, but context %s is a %s cluster", clusterTypeEnv, cfg.ClusterType, kctx.Name, clusterType)
	}

	if kctx.IsKind() {
		switch {
		case cfg.Domain != "" && cfg.Domain != kindDomain:
			return nil, fmt.Errorf("domain on kind is %s", kindDomain)
		case cfg.DNSProvider != "" && cfg.DNSProvider != install.DNSManual:
			return nil, fmt.Errorf("dns-provider on kind is %s", install.DNSManual)
		case cfg.CloudProvider != "":
			return nil, errors.New("cloud-provider does not apply to kind")
		}
		name := strings.TrimPrefix(kctx.Name, "kind-")
		exists, err := svc.kind.Exists(name)
		if err != nil {
			return nil, err
		}
		if !exists {
			return nil, fmt.Errorf("context %s has no kind cluster %q", kctx.Name, name)
		}
		caDir, err := state.Dir()
		if err != nil {
			return nil, err
		}
		sel.Kind, sel.ClusterName, sel.Domain, sel.DNSProvider = state.KindDemo, name, kindDomain, install.DNSManual
		return &install.KindTarget{Clusters: svc.kind, Name: name, Reuse: true, CADir: caDir}, nil
	}

	if cfg.Domain == "" {
		return nil, fmt.Errorf("domain is required on existing clusters: set --domain or %s", envName("domain"))
	}
	if err := validDomain(cfg.Domain); err != nil {
		return nil, fmt.Errorf("domain: %w", err)
	}
	if !slices.Contains(dnsProviders, cfg.DNSProvider) {
		return nil, fmt.Errorf("dns-provider must be one of %s", strings.Join(dnsProviders, ", "))
	}
	if err := checkDNSCredentials(cfg.DNSProvider, cfg.DNS); err != nil {
		return nil, err
	}
	cloud := cmp.Or(cfg.CloudProvider, defaultProvider)
	if cloud != defaultProvider && cloud != "aks" {
		return nil, fmt.Errorf("cloud-provider must be %s or aks", defaultProvider)
	}
	kc, err := svc.cluster(kctx.Name)
	if err != nil {
		return nil, err
	}
	storage, err := checkCluster(ctx, u, kc, kctx)
	if err != nil {
		return nil, err
	}
	if !storage {
		return nil, errors.New("the cluster has no default StorageClass, which Grafana, Mimir, MinIO and the Postgres databases need; use --interactive to install anyway")
	}
	sel.Kind, sel.Context, sel.Domain, sel.DNSProvider, sel.CloudProvider = state.Cluster, kctx.Name, cfg.Domain, cfg.DNSProvider, cloud
	return &install.ClusterTarget{Kube: kc, Domain: cfg.Domain, DNSProvider: cfg.DNSProvider, DNS: cfg.DNS}, nil
}

// warnIgnoredExcludes reports the requested exclusions the plan does not apply; the interactive install shows them.
func warnIgnoredExcludes(u ui.UI, plan *install.Plan, sel state.Installation) {
	if len(sel.ExcludedApps) == 0 {
		return
	}
	if !plan.Fresh {
		u.Warn("app-exclude has no effect: " + sel.Org + "/" + sel.Repo + " is already bootstrapped")
		return
	}
	for _, app := range sel.ExcludedApps {
		if slices.Contains(plan.Excluded(), app) {
			continue
		}
		reason := "it is not an optional application of " + sel.TargetType
		if plan.Apps != nil && slices.Contains(plan.Apps.Optional, app) {
			reason = "an installed application needs it"
		}
		u.Warn("app-exclude " + app + " has no effect: " + reason)
	}
}
