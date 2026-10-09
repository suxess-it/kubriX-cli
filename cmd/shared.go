package cmd

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/suxess-it/kubrix-cli/internal/git"
	"github.com/suxess-it/kubrix-cli/internal/gitinfo"
	"github.com/suxess-it/kubrix-cli/internal/install"
	"github.com/suxess-it/kubrix-cli/internal/platforminstaller"
	"github.com/suxess-it/kubrix-cli/internal/state"
	"github.com/suxess-it/kubrix-cli/internal/ui"
	"github.com/suxess-it/kubrix-cli/internal/version"
)

const (
	defaultUpstream = platforminstaller.DefaultUpstream
	defaultBranch   = "main"
	defaultTarget   = "kubrix-oss-stack"
	otherBranch     = "\x00other"
)

var (
	repoNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	repoSlugRe = regexp.MustCompile(`^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$`)
)

// versionChoice is the "kubriX version" select: releases, main, the checked-out branch, or another branch.
type versionChoice struct {
	choice, other string
	options       []ui.Option
}

// newVersionChoice defaults to current if it is offered, else to the checked-out branch when preferBranch,
// else to the latest release.
func newVersionChoice(releases []string, contributor *gitinfo.Info, current string, preferBranch bool) *versionChoice {
	vc := &versionChoice{}
	for i, r := range releases {
		label := r
		if i == 0 {
			label += " (latest release)"
		}
		vc.options = append(vc.options, ui.Option{Label: label, Value: r})
	}
	vc.options = append(vc.options, ui.Option{Label: "main (unreleased)", Value: defaultBranch})
	if contributor != nil && contributor.Branch != defaultBranch {
		vc.options = append(vc.options, ui.Option{Label: contributor.Branch + " (checked out)", Value: contributor.Branch})
	}
	vc.options = append(vc.options, ui.Option{Label: "Other branch…", Value: otherBranch})

	offered := func(ref string) bool {
		return slices.ContainsFunc(vc.options, func(o ui.Option) bool { return o.Value == ref })
	}
	switch {
	case preferBranch && contributor != nil:
		vc.choice = contributor.Branch
	case current != "" && offered(current):
		vc.choice = current
	case current != "":
		vc.choice, vc.other = otherBranch, current
	case len(releases) > 0:
		vc.choice = releases[0]
	default:
		vc.choice = defaultBranch
	}
	return vc
}

func (vc *versionChoice) ref() string {
	if vc.choice == otherBranch {
		return vc.other
	}
	return vc.choice
}

// fields are the version select and the page asking for a branch when "Other branch…" is chosen.
func (vc *versionChoice) fields(hint string) (ui.Choice, ui.Page) {
	sel := ui.Choice{Key: "version", Title: "kubriX version", Description: hint, Options: vc.options, Value: &vc.choice}
	other := ui.Page{
		Hidden: func() bool { return vc.choice != otherBranch },
		Fields: []ui.Field{ui.Input{Key: "branch", Title: "Branch", Description: "A branch of the kubriX source repository.", Validate: nonEmpty("branch"), Value: &vc.other}},
	}
	return sel, other
}

// githubAPI is what the flows need from GitHub: the Git host, plus the releases of the kubriX source repository.
type githubAPI interface {
	git.Host
	ReleaseTags(ctx context.Context, owner, repo string) ([]string, error)
}

func loadReleases(ctx context.Context, u ui.UI, gh githubAPI, upstream string) []string {
	owner, repo, _ := strings.Cut(upstream, "/")
	var releases []string
	err := u.Step(ctx, "Loading kubriX releases", func(ctx context.Context) (err error) {
		releases, err = gh.ReleaseTags(ctx, owner, repo)
		return err
	})
	if err != nil {
		u.Warn("could not load releases of " + upstream + ", only branches are offered: " + err.Error())
	}
	return slices.DeleteFunc(releases, func(r string) bool { return !version.Supported(r) })
}

func loadOrgs(ctx context.Context, u ui.UI, host git.Host, login string) ([]string, error) {
	var orgs []string
	if err := u.Step(ctx, "Loading GitHub organizations", func(ctx context.Context) (err error) {
		orgs, err = host.Orgs(ctx)
		return err
	}); err != nil {
		return nil, err
	}
	if len(orgs) == 0 {
		return nil, fmt.Errorf("%s is not a member of any GitHub organization; the kubriX bootstrap requires an organization-owned repository", login)
	}
	return orgs, nil
}

func checkContributorBranch(u ui.UI, c *gitinfo.Info, sel state.Installation) error {
	if c == nil || sel.UpstreamRepo != c.Origin.Slug() || sel.UpstreamBranch != c.Branch {
		return nil
	}
	status, err := c.PushStatus()
	if err != nil {
		return err
	}
	warnings := status.Warnings(c.Branch)
	if len(warnings) == 0 {
		return nil
	}
	for _, w := range warnings {
		u.Warn(w)
	}
	if ok, err := ui.Confirm(u, "Continue with what is on origin?", strings.Join(warnings, "\n"), false); err != nil || !ok {
		return abortErr(err)
	}
	return nil
}

// inspect runs the Install run's read-only phase and asks the user for what it cannot decide itself.
func inspect(ctx context.Context, u ui.UI, deps install.Deps, target install.Target, sel state.Installation) (*install.Plan, error) {
	plan, err := install.Inspect(ctx, deps, target, sel, u)
	if err != nil {
		return nil, err
	}
	if !plan.Fresh {
		ok, err := ui.Confirm(u,
			fmt.Sprintf("%s/%s already has content. Reinstall from it?", sel.Org, sel.Repo),
			"The platform is installed from the repository's existing content, without bootstrapping.\nChoose No and use a different repository name to bootstrap a fresh one.",
			true)
		if err != nil || !ok {
			return nil, abortErr(err)
		}
	}
	if plan.Apps != nil {
		if err := selectApps(u, plan); err != nil {
			return nil, err
		}
	}
	if plan.ImageFallback != "" {
		u.Warn(plan.ImageFallback)
		if ok, err := ui.Confirm(u, "Continue with the 'latest' installer image?", plan.ImageFallback, false); err != nil || !ok {
			return nil, abortErr(err)
		}
	}
	return plan, nil
}

// selectApps only runs for a fresh bootstrap: the excludes are applied while templating the new repo.
func selectApps(u ui.UI, plan *install.Plan) error {
	apps := plan.Apps
	selected := slices.Clone(apps.Selected)
	var options []ui.Option
	for _, app := range apps.Optional {
		options = append(options, ui.Option{Label: app, Value: app, Selected: slices.Contains(selected, app)})
	}
	description := "Always installed: " + strings.Join(apps.Required, ", ") + "."
	for _, app := range apps.Optional {
		if deps := apps.Dependencies[app]; len(deps) > 0 {
			description += "\n" + app + " needs " + strings.Join(deps, ", ") + "."
		}
	}
	description += "\nRemoving other apps is untested; apps that depend on a removed one may break."
	err := u.Ask(ui.Form{Pages: []ui.Page{{Fields: []ui.Field{ui.Choices{
		Key:         "apps",
		Title:       "Applications to install",
		Description: description,
		Options:     options,
		Height:      min(len(apps.Optional)+4, 20),
		Validate:    apps.Validate,
		Value:       &selected,
	}}}}})
	if err != nil {
		return abortErr(err)
	}
	plan.Select(selected)
	if excluded := plan.Excluded(); len(excluded) > 0 {
		u.Warn("not installing: " + strings.Join(excluded, ", "))
	}
	return nil
}

// openCatalog opens the saved installations. An unreadable catalog is reported once and then used empty:
// it can still be read as "nothing saved", but it refuses to be written over.
func openCatalog(u ui.UI) *state.Catalog {
	dir, err := state.Dir()
	if err != nil {
		u.Warn("ignoring saved installations: " + err.Error())
		return state.Open("")
	}
	c := state.Open(dir)
	if err := c.Problem(); err != nil {
		u.Warn("ignoring unreadable saved installations: " + err.Error())
	}
	return c
}

// recordInstallation saves the Installation as soon as the target is ready, so 'kubrix demo delete' also finds failed installs.
func recordInstallation(u ui.UI, c *state.Catalog) func(state.Installation) {
	return func(st state.Installation) {
		if err := c.Record(st); err != nil {
			u.Warn("could not save this installation: " + err.Error())
		}
	}
}

func targetOptions(untested []string) []ui.Option {
	options := []ui.Option{{Label: defaultTarget + " (default, tested with bootstrap)", Value: defaultTarget}}
	for _, t := range untested {
		options = append(options, ui.Option{Label: t + " (untested with bootstrap)", Value: t})
	}
	return options
}

func matches(re *regexp.Regexp, hint string) func(string) error {
	return func(s string) error {
		if !re.MatchString(s) {
			return fmt.Errorf("allowed: %s", hint)
		}
		return nil
	}
}
