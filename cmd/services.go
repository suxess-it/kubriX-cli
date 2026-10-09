package cmd

import (
	"context"

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
	// upstream reads the kubriX source repository and the installer image on behalf of the host's token.
	upstream func(host git.Host) install.Upstream
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
}

func productionServices() services {
	return services{
		github: func(ctx context.Context, u ui.UI) (githubAPI, string, error) {
			gh, login, err := githubClient(ctx, u)
			if err != nil {
				return nil, "", err
			}
			return gh, login, nil
		},
		upstream: func(host git.Host) install.Upstream { return platforminstaller.Source{Token: host.Token()} },
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
	return install.Deps{Host: host, Upstream: s.upstream(host)}
}
