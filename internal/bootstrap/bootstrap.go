// Package bootstrap prepares an empty customer repo from a kubriX release tag.
// install-platform.sh pushes `${KUBRIX_UPSTREAM_BRANCH}:main`, which for a tag drops the bootstrap
// commit, so the CLI bootstraps releases itself and runs the installer with KUBRIX_BOOTSTRAP=false.
package bootstrap

import (
	"context"
	"os"

	"github.com/suxess-it/kubrix-cli/internal/gitops"
	"github.com/suxess-it/kubrix-cli/internal/platformrepo"
)

// Same identity and message as install-platform.sh's bootstrap_clone_from_upstream / bootstrap_push_to_downstream.
const (
	botName       = "kubrix-installer[kubrix-bot]"
	botEmail      = "kubrix-installer[kubrix-bot]@users.noreply.github.com"
	CommitMessage = "add customer specific modifications during bootstrap"
)

type Options struct {
	UpstreamURL string
	Tag         string
	CustomerURL string
	Token       string
	Config      platformrepo.ConfigInput
	TargetType  string
	Exclude     []string
}

func FromRelease(ctx context.Context, o Options) error {
	if err := gitops.Available(); err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "kubrix-bootstrap-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	g := gitops.New(dir, o.Token, botName, botEmail)

	if _, err := g.Run(ctx, "clone", "-q", o.UpstreamURL, "."); err != nil {
		return err
	}
	if _, err := g.Run(ctx, "checkout", "-q", "refs/tags/"+o.Tag); err != nil {
		return err
	}

	repo := platformrepo.At(dir)
	if err := repo.WriteConfig(o.Config); err != nil {
		return err
	}
	if err := repo.RenderAll(); err != nil {
		return err
	}
	if err := repo.Exclude(o.TargetType, o.Exclude); err != nil {
		return err
	}

	if _, err := g.Run(ctx, "add", "-A"); err != nil {
		return err
	}
	if _, err := g.Run(ctx, "commit", "-q", "-m", CommitMessage); err != nil {
		return err
	}
	_, err = g.Run(ctx, "push", "-q", o.CustomerURL, "HEAD:refs/heads/main")
	return err
}
