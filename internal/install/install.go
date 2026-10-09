// Package install is the Install run: taking a chosen target to a running kubriX platform.
//
// An Install run has two phases. Inspect reads the repository, the kubriX source and the installer image and
// reports what the user still has to decide (apps to leave out, falling back to the latest installer image).
// Plan.Run then readies the platform repository and the target, runs the platform installer and reports access.
// Prompts stay with the caller; the decisions come in as data.
package install

import (
	"context"
	"fmt"
	"slices"

	"github.com/suxess-it/kubrix-cli/internal/bootstrap"
	"github.com/suxess-it/kubrix-cli/internal/git"
	"github.com/suxess-it/kubrix-cli/internal/gitops"
	"github.com/suxess-it/kubrix-cli/internal/platforminstaller"
	"github.com/suxess-it/kubrix-cli/internal/platformrepo"
	"github.com/suxess-it/kubrix-cli/internal/state"
	"github.com/suxess-it/kubrix-cli/internal/version"
)

const defaultBranch = "main"

// Upstream is the kubriX source repository and the installer image registry.
// platforminstaller.Source is the production adapter.
type Upstream interface {
	// Apps lists the applications the target type installs on the cluster type.
	Apps(ctx context.Context, repo, ref, targetType, clusterType string) ([]string, error)
	// Manifest downloads the platform installer's manifests.
	Manifest(ctx context.Context, repo, ref string) ([]byte, error)
	ImageTagExists(ctx context.Context, tag string) (bool, error)
	// GitURL is the clone URL of the source repository.
	GitURL(repo string) string
}

// Progress is how an Install run reports; ui.UI satisfies it.
type Progress interface {
	platforminstaller.Progress
	Heading(msg string)
	Warn(msg string)
}

// Cluster is a running cluster the platform installer can work in.
type Cluster interface {
	platforminstaller.Kube
	SecretValue(ctx context.Context, ns, name, key string) (string, error)
	IngressHosts(ctx context.Context) ([]string, error)
}

// Prepared is a cluster that is ready for the platform installer.
type Prepared struct {
	Cluster Cluster
	// Reused means the cluster already ran a platform installer, whose Job has to be replaced.
	Reused bool
	// Watch, if set, runs while the platform installer runs, until ctx ends.
	Watch func(ctx context.Context)
}

// Profile is what a target tells about itself before anything runs.
type Profile struct {
	// ClusterType is the platform installer's cluster type: kind or k8s.
	ClusterType string
	// Excludes are applications this target never installs, with the reason.
	Excludes map[string]string
}

// Target is where an Install run executes: a kind cluster created by the CLI or an existing cluster.
type Target interface {
	Profile() Profile
	// Prepare readies the cluster and whatever the platform needs in it before the installer starts.
	Prepare(ctx context.Context, p Progress) (Prepared, error)
	// Access reports what is specific to reaching a platform on this target.
	Access(ctx context.Context, c Cluster, p Progress) error
}

// Deps are the services an Install run talks to.
type Deps struct {
	Host     git.Host
	Upstream Upstream
}

// Plan is an inspected Install run.
type Plan struct {
	// Fresh means the platform repository has no content yet and is bootstrapped.
	Fresh bool
	// Apps is the choice of applications; nil unless Fresh and there is something to choose.
	Apps *Apps
	// ImageFallback is set when the installer image of the version is missing: the run can only continue
	// with the latest image, and the message says why.
	ImageFallback string

	deps     Deps
	target   Target
	sel      state.Installation
	repo     git.RepoState
	manifest []byte
	excluded []string
}

// Inspect looks at what the run depends on. sel holds the selections to install; its ExcludedApps are the
// initially deselected apps.
func Inspect(ctx context.Context, deps Deps, target Target, sel state.Installation, p Progress) (*Plan, error) {
	pl := &Plan{deps: deps, target: target, sel: sel}
	if err := p.Step(ctx, "Checking repository "+sel.Org+"/"+sel.Repo, func(ctx context.Context) (err error) {
		pl.repo, err = deps.Host.RepoState(ctx, sel.Org, sel.Repo)
		return err
	}); err != nil {
		return nil, err
	}
	pl.Fresh = pl.repo != git.RepoNonEmpty
	if pl.cliBootstraps() {
		if err := gitops.Available(); err != nil {
			return nil, err
		}
	}

	if pl.Fresh {
		var apps []string
		if err := p.Step(ctx, "Loading applications of "+sel.TargetType, func(ctx context.Context) (err error) {
			apps, err = deps.Upstream.Apps(ctx, sel.UpstreamRepo, sel.UpstreamBranch, sel.TargetType, target.Profile().ClusterType)
			return err
		}); err != nil {
			return nil, err
		}
		if a := newApps(apps, sel.ExcludedApps); len(a.Optional) > 0 {
			pl.Apps = a
			pl.excluded = a.excluded(a.Selected)
		}
	} else {
		pl.excluded = sel.ExcludedApps
	}

	tag, err := pl.imageTag(ctx, p)
	if err != nil {
		return nil, err
	}
	if err := p.Step(ctx, "Downloading install manifests", func(ctx context.Context) error {
		manifest, err := deps.Upstream.Manifest(ctx, sel.UpstreamRepo, sel.UpstreamBranch)
		if err != nil {
			return err
		}
		pl.manifest, err = platforminstaller.SetImageTag(manifest, tag)
		return err
	}); err != nil {
		return nil, err
	}
	return pl, nil
}

// imageTag picks the installer image of the ref, or latest with ImageFallback explaining why.
func (pl *Plan) imageTag(ctx context.Context, p Progress) (string, error) {
	ref := pl.sel.UpstreamBranch
	if ref == defaultBranch {
		return "latest", nil
	}
	tag := platforminstaller.BranchTag(ref)
	var exists bool
	if err := p.Step(ctx, "Looking for installer image "+tag, func(ctx context.Context) (err error) {
		exists, err = pl.deps.Upstream.ImageTagExists(ctx, tag)
		return err
	}); err != nil {
		return "", err
	}
	if exists {
		p.OK("using installer image " + tag)
		return tag, nil
	}
	pl.ImageFallback = fmt.Sprintf("No installer image %s:%s.", platforminstaller.ImageRepo, tag)
	if !version.IsRelease(ref) {
		pl.ImageFallback += " Branch images are only built by running the 'create kubrix-installer image' workflow on the branch."
	}
	return "latest", nil
}

// Select sets which of Apps.Optional are installed; every other optional app is left out.
func (pl *Plan) Select(selected []string) {
	if pl.Apps != nil {
		pl.excluded = pl.Apps.excluded(selected)
	}
}

// Excluded are the applications left out by the user's selection.
func (pl *Plan) Excluded() []string { return slices.Clone(pl.excluded) }

// cliBootstraps: install-platform.sh cannot bootstrap from a tag (it pushes the tag, not its commit),
// so releases are bootstrapped by the CLI and the installer only installs.
func (pl *Plan) cliBootstraps() bool { return pl.Fresh && version.IsRelease(pl.sel.UpstreamBranch) }

// finalExcludes adds what the target never installs to the user's selection; only a fresh bootstrap excludes.
func (pl *Plan) finalExcludes(p Progress) []string {
	out := slices.Clone(pl.excluded)
	if !pl.Fresh {
		return out
	}
	for app, reason := range pl.target.Profile().Excludes {
		if !slices.Contains(out, app) {
			out = append(out, app)
			p.Info(app + " is not installed because " + reason)
		}
	}
	return out
}

func (pl *Plan) repoURL() string { return pl.deps.Host.CloneURL(pl.sel.Org, pl.sel.Repo) }

func (pl *Plan) spec(excluded []string) platforminstaller.Spec {
	s := pl.sel
	spec := platforminstaller.Spec{
		RepoURL:        pl.repoURL(),
		RepoToken:      pl.deps.Host.Token(),
		GitUserName:    s.GitUserName,
		Domain:         s.Domain,
		DNSProvider:    s.DNSProvider,
		CloudProvider:  s.CloudProvider,
		TargetType:     s.TargetType,
		ClusterType:    pl.target.Profile().ClusterType,
		UpstreamRepo:   s.UpstreamRepo,
		UpstreamBranch: s.UpstreamBranch,
		Bootstrap:      pl.Fresh && !pl.cliBootstraps(),
	}
	if pl.Fresh {
		spec.ExcludedApps = excluded
	}
	return spec
}

// Run installs the platform. record receives the Installation as soon as the target is ready, so a failed
// run can still be found and cleaned up.
func (pl *Plan) Run(ctx context.Context, p Progress, record func(state.Installation)) error {
	excluded := pl.finalExcludes(p)
	if err := pl.prepareRepo(ctx, p, excluded); err != nil {
		return err
	}
	prepared, err := pl.target.Prepare(ctx, p)
	if err != nil {
		return err
	}
	if pl.Fresh {
		pl.sel.ExcludedApps = excluded
	}
	record(pl.sel)

	watchCtx, stop := context.WithCancel(ctx)
	watched := make(chan struct{})
	go func() {
		defer close(watched)
		if prepared.Watch != nil {
			prepared.Watch(watchCtx)
		}
	}()
	err = platforminstaller.Run(ctx, prepared.Cluster, p, platforminstaller.Options{
		Spec:       pl.spec(excluded),
		Manifest:   pl.manifest,
		ReplaceJob: prepared.Reused,
	})
	stop()
	<-watched
	if err != nil {
		return err
	}
	return pl.access(ctx, p, prepared.Cluster)
}

// prepareRepo creates a missing repository and, for releases, bootstraps it from the CLI.
func (pl *Plan) prepareRepo(ctx context.Context, p Progress, excluded []string) error {
	s := pl.sel
	if pl.repo == git.RepoMissing {
		if err := p.Step(ctx, "Creating repository "+s.Org+"/"+s.Repo, func(ctx context.Context) error {
			return pl.deps.Host.CreatePrivateRepo(ctx, s.Org, s.Repo)
		}); err != nil {
			return err
		}
	}
	if !pl.cliBootstraps() {
		return nil
	}
	return p.Step(ctx, "Bootstrapping "+s.Repo+" from "+s.UpstreamBranch, func(ctx context.Context) error {
		return bootstrap.FromRelease(ctx, bootstrap.Options{
			UpstreamURL: pl.deps.Upstream.GitURL(s.UpstreamRepo),
			Tag:         s.UpstreamBranch,
			CustomerURL: pl.repoURL(),
			Token:       pl.deps.Host.Token(),
			Config: platformrepo.ConfigInput{
				ClusterType:   pl.target.Profile().ClusterType,
				CloudProvider: s.CloudProvider,
				DNSProvider:   s.DNSProvider,
				Domain:        s.Domain,
				Repo:          pl.repoURL(),
				GitUser:       s.GitUserName,
			},
			TargetType: s.TargetType,
			Exclude:    excluded,
		})
	})
}

func (pl *Plan) access(ctx context.Context, p Progress, c Cluster) error {
	if err := pl.target.Access(ctx, c, p); err != nil {
		return err
	}
	p.Heading("Platform")
	if pw, err := c.SecretValue(ctx, "argocd", "argocd-initial-admin-secret", "password"); err == nil {
		p.Info("Argo CD login: admin / " + pw)
	}
	hosts, err := c.IngressHosts(ctx)
	if err != nil {
		return err
	}
	for _, h := range hosts {
		p.Info("  https://" + h)
	}
	return nil
}
