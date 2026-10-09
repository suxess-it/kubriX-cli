// Package kubeconfig lists kubeconfig contexts and builds REST configs for them.
package kubeconfig

import (
	"slices"
	"strings"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

type Context struct {
	Name   string
	Server string
}

// IsKind reports whether the context belongs to a kind cluster (kind names them kind-<cluster>).
func (c Context) IsKind() bool { return strings.HasPrefix(c.Name, "kind-") }

// Contexts returns all contexts sorted by name and the current context, honoring $KUBECONFIG.
func Contexts() ([]Context, string, error) {
	cfg, err := clientcmd.NewDefaultClientConfigLoadingRules().Load()
	if err != nil {
		return nil, "", err
	}
	var out []Context
	for name, ctx := range cfg.Contexts {
		server := ""
		if cluster, ok := cfg.Clusters[ctx.Cluster]; ok {
			server = cluster.Server
		}
		out = append(out, Context{Name: name, Server: server})
	}
	slices.SortFunc(out, func(a, b Context) int { return strings.Compare(a.Name, b.Name) })
	return out, cfg.CurrentContext, nil
}

func RESTConfig(context string) (*rest.Config, error) {
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(),
		&clientcmd.ConfigOverrides{CurrentContext: context},
	).ClientConfig()
}
