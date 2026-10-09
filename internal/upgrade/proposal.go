// Package upgrade proposes upgrading a platform repository to a newer kubriX release: it merges the release into
// a branch, re-renders what the release changed and opens a pull request.
//
// A Proposal is staged. Begin clones the repository; the caller then settles the installed version (SetInstalled
// when the repository does not say), picks one of Targets, confirms the Review of the chosen target, and finally
// calls Propose. The order of the git steps behind Propose is the module's business.
package upgrade

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"golang.org/x/mod/semver"

	"github.com/suxess-it/kubrix-cli/internal/git"
	"github.com/suxess-it/kubrix-cli/internal/gitops"
	"github.com/suxess-it/kubrix-cli/internal/version"
)

// baseBranch is the branch the pull request targets.
const baseBranch = "main"

// ErrUpToDate means the installed release is the latest one there is to upgrade to.
var ErrUpToDate = errors.New("already on the latest release")

// TooOldError means the installed version is older than any upgrade path the CLI supports.
type TooOldError struct{ Installed string }

func (e *TooOldError) Error() string {
	return fmt.Sprintf("%s is too old for 'kubrix upgrade', which supports upgrades to %s and newer, one major version at a time; upgrade to the next major version by hand first", e.Installed, version.MinSupported)
}

// Deps are the services a Proposal talks to.
type Deps struct {
	Host git.Host
	// UpstreamURL is the clone URL of a kubriX source repository (owner/repo); GitHub's when nil.
	UpstreamURL func(repo string) string
	// AllowMain offers upstream's unreleased main as a target; only contributors testing kubriX want that.
	AllowMain bool
}

// Repo is the platform repository to upgrade.
type Repo struct {
	Org, Name string
	// Upstream is the kubriX source repository (owner/repo) the platform was created from.
	Upstream string
	// TargetType is the platform's target type; its excluded apps are kept excluded.
	TargetType string
}

// Progress reports the slow steps; ui.UI satisfies it.
type Progress interface {
	Step(ctx context.Context, title string, fn func(context.Context) error) error
}

// Review is what the user confirms before anything is changed.
type Review struct {
	Installed, Target string
	// Notes are the release notes between the installed version and the target.
	Notes string
	// Breaking are the breaking-change entries of Notes, if any.
	Breaking string
	// Unreleased counts upstream commits after the installed release that the repository already contains:
	// it was bootstrapped from a branch, so the installed version is only approximate.
	Unreleased int
}

// Result is the pull request that proposes the upgrade.
type Result struct {
	URL, Branch string
	// Regenerated are the files rendered again from changed templates.
	Regenerated []string
	// Resolved are conflicts in generated files that were resolved automatically; Removed are outputs of
	// templates the target no longer has.
	Resolved, Removed []string
	// Excluded are the apps that stay excluded.
	Excluded []string
}

// Proposal is an upgrade in preparation. Close it to remove the temporary clone.
type Proposal struct {
	deps Deps
	repo Repo
	ws   *workspace

	releases  []string
	installed string
	target    string
	review    *Review
}

// Begin clones the repository and reads the installed version and the releases to upgrade to.
func Begin(ctx context.Context, deps Deps, repo Repo, p Progress) (*Proposal, error) {
	if err := gitops.Available(); err != nil {
		return nil, err
	}
	upstreamURL := gitops.GitHubURL
	if deps.UpstreamURL != nil {
		upstreamURL = deps.UpstreamURL
	}
	pr := &Proposal{deps: deps, repo: repo}
	if err := p.Step(ctx, "Cloning "+repo.Org+"/"+repo.Name, func(ctx context.Context) (err error) {
		pr.ws, err = openWorkspace(ctx, deps.Host.CloneURL(repo.Org, repo.Name), upstreamURL(repo.Upstream), deps.Host.Token())
		return err
	}); err != nil {
		return nil, err
	}
	var err error
	if pr.releases, err = pr.ws.releases(ctx); err != nil {
		pr.Close()
		return nil, err
	}
	pr.installed, _ = pr.ws.installedVersion()
	if pr.installed == "" && len(pr.releases) == 0 {
		pr.Close()
		return nil, errors.New("the repository has no .release-please-manifest.json and upstream has no releases to compare with")
	}
	return pr, nil
}

// Close removes the temporary clone.
func (p *Proposal) Close() {
	if p.ws != nil {
		p.ws.close()
	}
}

// Installed is the installed kubriX release; ok is false when the repository does not say, and the caller has
// to ask the user and call SetInstalled.
func (p *Proposal) Installed() (release string, ok bool) { return p.installed, p.installed != "" }

// Releases are the kubriX releases there are, newest first.
func (p *Proposal) Releases() []string { return slices.Clone(p.releases) }

// SetInstalled sets the installed release when the repository does not say.
func (p *Proposal) SetInstalled(release string) error {
	if !version.IsRelease(release) {
		return fmt.Errorf("%q is not a kubriX release", release)
	}
	p.installed, p.target, p.review = release, "", nil
	return nil
}

// Targets lists what the installed release can be upgraded to, newest first: at most one major version ahead.
// It returns ErrUpToDate when there is nothing newer, and a *TooOldError when the installed release is below
// the oldest one the CLI upgrades from.
func (p *Proposal) Targets() ([]string, error) {
	if p.installed == "" {
		return nil, errors.New("upgrade: the installed version is not known yet; call SetInstalled")
	}
	targets := version.UpgradeTargets(p.installed, p.releases)
	if p.deps.AllowMain {
		targets = append(targets, MainTarget)
	}
	if len(targets) > 0 {
		return targets, nil
	}
	if !version.Supported(p.installed) && slices.ContainsFunc(p.releases, func(r string) bool { return semver.Compare(r, p.installed) > 0 }) {
		return nil, &TooOldError{Installed: p.installed}
	}
	return nil, ErrUpToDate
}

// Choose picks the target and returns what the user has to confirm. It fails with ErrUnrelatedHistory when the
// repository cannot be upgraded by merging.
func (p *Proposal) Choose(ctx context.Context, target string) (Review, error) {
	targets, err := p.Targets()
	if err != nil {
		return Review{}, err
	}
	if !slices.Contains(targets, target) {
		return Review{}, fmt.Errorf("%s is not one of the targets %s can be upgraded to: %s", target, p.installed, strings.Join(targets, ", "))
	}
	if _, err := p.ws.mergeBase(ctx, target); err != nil {
		return Review{}, err
	}
	notes, err := p.ws.notes(ctx, p.installed, target)
	if err != nil {
		return Review{}, err
	}
	r := Review{
		Installed:  p.installed,
		Target:     target,
		Notes:      notes,
		Breaking:   version.BreakingChanges(notes),
		Unreleased: p.ws.unreleasedCommits(ctx, p.installed),
	}
	p.target, p.review = target, &r
	return r, nil
}

// Propose merges the chosen target into a branch, re-renders the templates it changed, pushes the branch and
// opens a pull request. A merge conflict in files the CLI cannot regenerate stops it before anything is pushed.
func (p *Proposal) Propose(ctx context.Context, pr Progress) (Result, error) {
	var res Result
	if p.review == nil {
		return res, errors.New("upgrade: choose a target before proposing")
	}
	target, installed := p.target, p.installed
	base, err := p.ws.mergeBase(ctx, target)
	if err != nil {
		return res, err
	}
	// Before merging: the excluded apps are found by comparing the repository's current files.
	if res.Excluded, err = p.ws.excludedApps(p.repo.TargetType); err != nil {
		return res, err
	}
	var merged mergeResult
	if err := pr.Step(ctx, "Merging "+target, func(ctx context.Context) (err error) {
		merged, err = p.ws.merge(ctx, target)
		return err
	}); err != nil {
		var conflict *ConflictError
		if errors.As(err, &conflict) {
			return res, fmt.Errorf("%w\nThese files were changed both in %s/%s and in kubriX %s. Resolve them by merging %s into the repository by hand; nothing was pushed", err, p.repo.Org, p.repo.Name, target, target)
		}
		return res, err
	}
	res.Resolved, res.Removed = merged.Resolved, merged.Removed
	if err := pr.Step(ctx, "Re-rendering changed templates", func(ctx context.Context) (err error) {
		res.Regenerated, err = p.ws.rerender(ctx, base, target, p.repo.TargetType, res.Excluded)
		return err
	}); err != nil {
		return res, err
	}

	title := "chore(kubrix): upgrade kubriX " + installed + " → " + target
	if err := pr.Step(ctx, "Pushing "+BranchName(target), func(ctx context.Context) (err error) {
		res.Branch, err = p.ws.commitAndPush(ctx, target, title)
		return err
	}); err != nil {
		return res, err
	}
	if err := pr.Step(ctx, "Opening the pull request", func(ctx context.Context) (err error) {
		res.URL, err = p.deps.Host.OpenPullRequest(ctx, p.repo.Org, p.repo.Name, res.Branch, baseBranch, title, prBody(*p.review, merged, res.Regenerated, res.Excluded))
		return err
	}); err != nil {
		return res, fmt.Errorf("pushed branch %s, but opening the pull request failed: %w; run the upgrade again to retry", res.Branch, err)
	}
	return res, nil
}

func prBody(r Review, merged mergeResult, regenerated, excluded []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Upgrades kubriX from **%s** to **%s**. Argo CD applies it after this pull request is merged.\n\n", r.Installed, r.Target)
	if r.Unreleased > 0 {
		fmt.Fprintf(&b, "Note: the repository already contained %d unreleased upstream commits after %s, so the installed version was only approximately %s.\n\n", r.Unreleased, r.Installed, r.Installed)
	}
	if r.Breaking != "" {
		b.WriteString("> [!WARNING]\n> This upgrade contains breaking changes that may need manual steps. Read them before merging.\n\n")
	}
	list := func(title string, items []string) {
		if len(items) == 0 {
			return
		}
		b.WriteString("### " + title + "\n\n")
		for _, item := range items {
			b.WriteString("- `" + item + "`\n")
		}
		b.WriteString("\n")
	}
	list("Re-rendered from changed templates", regenerated)
	list("Merge conflicts in generated files, resolved automatically", merged.Resolved)
	list("Removed: rendered from templates this release no longer has", merged.Removed)
	list("Apps kept excluded", excluded)
	if r.Notes != "" {
		b.WriteString("### Release notes\n\n" + r.Notes + "\n\n")
	}
	b.WriteString("---\nOpened by `kubrix upgrade`.\n")
	return b.String()
}
