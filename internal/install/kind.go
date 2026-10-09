package install

import (
	"context"
	"os"
	"path/filepath"
	"runtime"

	"github.com/suxess-it/kubrix-cli/internal/kindcluster"
	"github.com/suxess-it/kubrix-cli/internal/kube"
)

// KindClusters creates and reaches local kind clusters; NewKindClusters is the production adapter.
type KindClusters interface {
	Exists(name string) (bool, error)
	Create(name string) error
	Delete(name string) error
	Connect(name string) (Cluster, error)
}

type kindClusters struct{ p *kindcluster.Provider }

// NewKindClusters returns the kind adapter.
func NewKindClusters() KindClusters { return kindClusters{p: kindcluster.NewProvider()} }

func (k kindClusters) Exists(name string) (bool, error) { return k.p.Exists(name) }
func (k kindClusters) Create(name string) error         { return k.p.Create(name) }
func (k kindClusters) Delete(name string) error         { return k.p.Delete(name) }

func (k kindClusters) Connect(name string) (Cluster, error) {
	cfg, err := k.p.RESTConfig(name)
	if err != nil {
		return nil, err
	}
	return kube.New(cfg)
}

// KindTarget is a demo: a kind cluster created by the CLI.
type KindTarget struct {
	Clusters KindClusters
	Name     string
	// Reuse keeps an existing cluster of that name instead of recreating it.
	Reuse bool
	// CADir receives the kind root CA for the user to trust.
	CADir string
}

func (t *KindTarget) Profile() Profile { return Profile{ClusterType: "kind"} }

func (t *KindTarget) Prepare(ctx context.Context, p Progress) (Prepared, error) {
	exists, err := t.Clusters.Exists(t.Name)
	if err != nil {
		return Prepared{}, err
	}
	reused := exists && t.Reuse
	if exists && !t.Reuse {
		if err := p.Step(ctx, "Deleting kind cluster "+t.Name, func(context.Context) error { return t.Clusters.Delete(t.Name) }); err != nil {
			return Prepared{}, err
		}
	}
	if !reused {
		if err := p.Step(ctx, "Creating kind cluster "+t.Name, func(context.Context) error { return t.Clusters.Create(t.Name) }); err != nil {
			return Prepared{}, err
		}
	}
	c, err := t.Clusters.Connect(t.Name)
	if err != nil {
		return Prepared{}, err
	}
	return Prepared{Cluster: c, Reused: reused}, nil
}

// Access saves the kind root CA, which the platform's certificates are issued from.
func (t *KindTarget) Access(ctx context.Context, c Cluster, p Progress) error {
	ca, err := c.SecretValue(ctx, "cert-manager", "kind-kubrix-ca-key-pair", "tls.crt")
	if err != nil {
		p.Warn("could not read the kind root CA: " + err.Error())
		return nil
	}
	if err := os.MkdirAll(t.CADir, 0o700); err != nil {
		return err
	}
	caPath := filepath.Join(t.CADir, "kind-ca.crt")
	if err := os.WriteFile(caPath, []byte(ca), 0o644); err != nil {
		return err
	}
	p.Heading("Browser trust")
	p.Info("The platform uses certificates from the kubriX kind root CA, saved to:\n  " + caPath)
	p.Warn("this CA's private key ships in the public installer image; anyone can issue certificates your machine will trust. Only import it on a throwaway/demo machine, and remove it afterwards.")
	p.Info("To trust it:")
	for _, line := range caImportCommands(runtime.GOOS, caPath) {
		p.Info("  " + line)
	}
	return nil
}
