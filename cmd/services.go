package cmd

import (
	"context"
	"fmt"

	"github.com/suxess-it/kubrix-cli/internal/git"
	"github.com/suxess-it/kubrix-cli/internal/install"
	"github.com/suxess-it/kubrix-cli/internal/kindcluster"
	"github.com/suxess-it/kubrix-cli/internal/kube"
	"github.com/suxess-it/kubrix-cli/internal/kubeconfig"
	"github.com/suxess-it/kubrix-cli/internal/platforminstaller"
	"github.com/suxess-it/kubrix-cli/internal/ui"
)

// clusterAccess is an existing cluster: what the platform installer needs, plus the checks made before installing.
type clusterAccess interface {
	install.ClusterKube
	IsClusterAdmin(ctx context.Context) (bool, error)
	HasDefaultStorageClass(ctx context.Context) (bool, error)
}

// services are the outside world the flows act on: GitHub, the kubriX source, kind, Docker and kubeconfig.
// productionServices wires the real ones; tests wire fakes and drive the flows end to end.
type services struct {
	// github connects to GitHub, asking for a token when the gh CLI has none, and returns the login.
	github func(ctx context.Context, u ui.UI) (githubAPI, string, error)
	// githubToken connects to GitHub with a token, without asking; an empty token means the gh CLI's.
	githubToken func(token string) (githubAPI, error)
	// upstream reads the kubriX source repository and the installer image on behalf of the token.
	upstream func(token string) install.Upstream
	// upstreamGitURL is the clone URL of a kubriX source repository (owner/repo); GitHub's when nil.
	upstreamGitURL func(repo string) string
	// kind creates and reaches the demo clusters.
	kind install.KindClusters
	// docker reports what Docker can give a kind cluster.
	docker func() (kindcluster.Resources, error)
	// contexts lists the kubeconfig contexts and names the current one.
	contexts func() ([]kubeconfig.Context, string, error)
	// cluster connects to the cluster of a kubeconfig context.
	cluster func(contextName string) (clusterAccess, error)
	// experimental says the experimental commands are enabled, so the saved installations on existing
	// clusters are shown too, not only the demos.
	experimental bool
}

func productionServices(f Features) services {
	return services{
		experimental: f.Experimental,
		github: func(ctx context.Context, u ui.UI) (githubAPI, string, error) {
			gh, login, err := githubClient(ctx, u)
			if err != nil {
				return nil, "", err
			}
			return gh, login, nil
		},
		githubToken: func(token string) (githubAPI, error) {
			if token == "" {
				var err error
				if token, err = git.GitHubTokenFromCLI(); err != nil {
					return nil, fmt.Errorf("no GitHub token: set %s (or GITHUB_TOKEN), or log in with gh: %w", repoTokenEnv, err)
				}
			}
			return git.NewGitHub(token)
		},
		upstream: func(token string) install.Upstream { return platforminstaller.Source{Token: token} },
		kind:     install.NewKindClusters(),
		docker:   kindcluster.DockerResources,
		contexts: kubeconfig.Contexts,
		cluster: func(contextName string) (clusterAccess, error) {
			cfg, err := kubeconfig.RESTConfig(contextName)
			if err != nil {
				return nil, err
			}
			return kube.New(cfg)
		},
	}
}

// installDeps are the services an Install run talks to.
func (s services) installDeps(host git.Host) install.Deps {
	return install.Deps{Host: host, Upstream: s.upstream(host.Token())}
}
