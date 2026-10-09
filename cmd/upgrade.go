package cmd

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/suxess-it/kubrix-cli/internal/gitinfo"
	"github.com/suxess-it/kubrix-cli/internal/gitops"
	"github.com/suxess-it/kubrix-cli/internal/state"
	"github.com/suxess-it/kubrix-cli/internal/ui"
	"github.com/suxess-it/kubrix-cli/internal/upgrade"
)

const otherRepo = "\x00other"

func newUpgradeCmd(f Features) *cobra.Command {
	return &cobra.Command{
		Use:    "upgrade",
		Short:  "Upgrade a kubriX installation by opening a pull request in its repository (experimental)",
		Args:   cobra.NoArgs,
		Hidden: !f.Experimental,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := f.requireExperimental("upgrade"); err != nil {
				return err
			}
			return withUI(cmd, "upgrade", func(ctx context.Context, u ui.UI) error { return runUpgrade(ctx, u, productionServices(f)) })
		},
	}
}

type upgradeInput struct {
	org, repo, upstream, targetType string
}

func runUpgrade(ctx context.Context, u ui.UI, svc services) error {
	u.Heading("Upgrade kubriX")
	if err := gitops.Available(); err != nil {
		return err
	}
	in, err := pickUpgradeRepo(u, openCatalog(u))
	if err != nil {
		return err
	}
	gh, login, err := svc.github(ctx, u)
	if err != nil {
		return err
	}
	u.OK("GitHub: authenticated as " + login)

	proposal, err := upgrade.Begin(ctx,
		upgrade.Deps{Host: gh, AllowMain: contributorMode(), UpstreamURL: svc.upstreamGitURL},
		upgrade.Repo{Org: in.org, Name: in.repo, Upstream: in.upstream, TargetType: in.targetType},
		u)
	if err != nil {
		return err
	}
	defer proposal.Close()

	if err := askInstalledVersion(u, proposal); err != nil {
		return err
	}
	target, err := pickTarget(u, proposal)
	if err != nil {
		return err
	}
	review, err := proposal.Choose(ctx, target)
	if err != nil {
		return err
	}
	if err := confirmNotes(u, review); err != nil {
		return err
	}
	res, err := proposal.Propose(ctx, u)
	if err != nil {
		return err
	}
	u.OK("pull request: " + res.URL)
	u.Info("Argo CD applies the upgrade after the pull request is merged.")
	return nil
}

// upgradeOptions is what the pickers need from an upgrade.Proposal.
type upgradeOptions interface {
	Installed() (string, bool)
	Releases() []string
	SetInstalled(release string) error
	Targets() ([]string, error)
}

func pickUpgradeRepo(u ui.UI, catalog *state.Catalog) (upgradeInput, error) {
	in := upgradeInput{upstream: defaultUpstream, targetType: defaultTarget}
	var options []ui.Option
	for _, name := range catalog.Keys() {
		d, _ := catalog.Find(name)
		if d.Org != "" && d.Repo != "" {
			options = append(options, ui.Option{Label: fmt.Sprintf("%s/%s  (%s)", d.Org, d.Repo, name), Value: name})
		}
	}
	options = append(options, ui.Option{Label: "Other repository…", Value: otherRepo})
	choice := options[0].Value
	slug := ""
	err := u.Ask(ui.Form{Pages: []ui.Page{
		{Fields: []ui.Field{ui.Choice{Key: "installation", Title: "Which installation?", Description: "The upgrade is opened as a pull request in its repository.", Options: options, Value: &choice}}},
		{Hidden: func() bool { return choice != otherRepo }, Fields: []ui.Field{
			ui.Input{Key: "repository", Title: "Repository", Description: "owner/repo of a kubriX platform repository.", Validate: matches(repoSlugRe, "owner/repo"), Value: &slug},
			ui.Choice{Key: "target-type", Title: "Target type", Description: "Used to keep the apps that were excluded at install.", Options: targetOptions(untestedTargets), Value: &in.targetType},
		}},
	}})
	if err != nil {
		return in, abortErr(err)
	}
	if choice == otherRepo {
		in.org, in.repo, _ = strings.Cut(slug, "/")
		return in, nil
	}
	d, _ := catalog.Find(choice)
	in.org, in.repo = d.Org, d.Repo
	in.upstream = cmp.Or(d.UpstreamRepo, defaultUpstream)
	in.targetType = cmp.Or(d.TargetType, defaultTarget)
	return in, nil
}

// askInstalledVersion asks which release is installed when the repository does not say.
func askInstalledVersion(u ui.UI, p upgradeOptions) error {
	if _, ok := p.Installed(); ok {
		return nil
	}
	releases := p.Releases()
	choice := releases[len(releases)-1]
	if err := ui.Select(u, "Which kubriX version is installed?", "The repository has no .release-please-manifest.json to read it from.", ui.Options(releases...), &choice); err != nil {
		return err
	}
	return p.SetInstalled(choice)
}

func pickTarget(u ui.UI, p upgradeOptions) (string, error) {
	targets, err := p.Targets()
	if errors.Is(err, upgrade.ErrUpToDate) {
		installed, _ := p.Installed()
		u.OK("already on the latest release " + installed)
		return "", ui.ErrQuit
	}
	if err != nil {
		return "", err
	}
	installed, _ := p.Installed()
	var options []ui.Option
	for _, t := range targets {
		label := t
		if t == upgrade.MainTarget {
			label = "main (unreleased, contributor mode)"
		}
		options = append(options, ui.Option{Label: label, Value: t})
	}
	choice := targets[0]
	err = ui.Select(u, "Upgrade "+installed+" to", "At most one major version per upgrade; repeat for the next one.", options, &choice)
	return choice, err
}

func contributorMode() bool {
	cwd, err := os.Getwd()
	if err != nil {
		return false
	}
	info, err := gitinfo.Detect(cwd)
	return err == nil && info != nil
}

func confirmNotes(u ui.UI, r upgrade.Review) error {
	if r.Notes != "" {
		u.Heading("Release notes " + r.Installed + " → " + r.Target)
		u.Info(r.Notes)
	}
	description := "The full release notes are in the output and go into the pull request."
	if r.Unreleased > 0 {
		description += fmt.Sprintf("\nThe repository already contains %d unreleased upstream commits after %s (it was bootstrapped from a branch).", r.Unreleased, r.Installed)
	}
	title := "Open a pull request upgrading " + r.Installed + " to " + r.Target + "?"
	if r.Breaking != "" {
		title = "This upgrade has BREAKING CHANGES. Open the pull request?"
		description = r.Breaking + "\n\n" + description
	}
	ok, err := ui.Confirm(u, title, description, true)
	if err != nil || !ok {
		return abortErr(err)
	}
	return nil
}
