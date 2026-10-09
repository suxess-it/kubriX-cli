package install

import (
	"context"
	"fmt"
)

const (
	// DNSManual is the DNS provider of a platform whose wildcard record the user creates.
	DNSManual     = "none"
	externalDNS   = "external-dns"
	externalDNSNs = "external-dns"
	traefikNs     = "traefik"
)

// ClusterKube is what an existing cluster is asked besides running the platform installer.
type ClusterKube interface {
	Cluster
	ApplySecretData(ctx context.Context, ns, name string, data map[string][]byte) error
	LoadBalancerAddress(ctx context.Context, ns string) (string, error)
}

// ClusterTarget is an existing cluster chosen from a kubeconfig context.
type ClusterTarget struct {
	Kube        ClusterKube
	Domain      string
	DNSProvider string
	DNS         DNSCredentials
}

// Profile: without a DNS provider nothing manages records, so external-dns would only fail.
func (t *ClusterTarget) Profile() Profile {
	p := Profile{ClusterType: "k8s"}
	if t.DNSProvider == DNSManual {
		p.Excludes = map[string]string{externalDNS: "DNS is managed manually"}
	}
	return p
}

// Prepare creates the DNS provider's credentials for external-dns. With manual DNS it also watches for the
// LoadBalancer address while the installer runs, to tell the user which wildcard record to create.
func (t *ClusterTarget) Prepare(ctx context.Context, p Progress) (Prepared, error) {
	prepared := Prepared{Cluster: t.Kube}
	if t.DNSProvider == DNSManual {
		prepared.Watch = func(ctx context.Context) {
			if addr, err := t.Kube.LoadBalancerAddress(ctx, traefikNs); err == nil {
				p.Warn(fmt.Sprintf("create a wildcard DNS record *.%s pointing to %s (traefik's LoadBalancer)", t.Domain, addr))
			}
		}
		return prepared, nil
	}
	err := p.Step(ctx, "Creating the "+t.DNSProvider+" DNS credentials", func(ctx context.Context) error {
		name, data, err := DNSSecret(t.DNSProvider, t.DNS)
		if err != nil {
			return err
		}
		if err := t.Kube.EnsureNamespace(ctx, externalDNSNs); err != nil {
			return err
		}
		return t.Kube.ApplySecretData(ctx, externalDNSNs, name, data)
	})
	return prepared, err
}

func (t *ClusterTarget) Access(context.Context, Cluster, Progress) error { return nil }
