// Package installtest provides fakes for testing Install runs without a cluster, a registry or GitHub.
package installtest

import (
	"context"
	"errors"
	"io"
	"slices"
	"sync"

	"github.com/suxess-it/kubrix-cli/internal/install"
	"github.com/suxess-it/kubrix-cli/internal/platforminstaller"
)

// Manifest is what Upstream serves as the installer manifest.
const Manifest = "kind: Job\nimage: " + platforminstaller.ImageRepo + ":latest\n"

// Cluster is a cluster on which the platform installer succeeds right away. It records what was applied.
type Cluster struct {
	mu sync.Mutex

	// Calls lists namespace and job operations in order.
	Calls []string
	// Secret holds the installer's Secret values, Manifest the applied installer manifests.
	Secret   map[string]string
	Manifest string
	// DNSData holds secrets created with raw data, by "namespace/name".
	DNSData map[string]map[string][]byte

	// Secrets are the values SecretValue answers with, by "namespace/name/key".
	Secrets map[string]string
	// Hosts are the ingress hosts reported once the platform is installed.
	Hosts []string
	// LBAddr is the LoadBalancer address.
	LBAddr string

	// JobFails makes the installer Job fail. NotAdmin and NoStorageClass make the preflight checks fail.
	JobFails       bool
	NotAdmin       bool
	NoStorageClass bool
}

var (
	_ install.Cluster     = (*Cluster)(nil)
	_ install.ClusterKube = (*Cluster)(nil)
)

func (c *Cluster) record(call string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Calls = append(c.Calls, call)
}

func (c *Cluster) EnsureNamespace(_ context.Context, name string) error {
	c.record("namespace " + name)
	return nil
}

func (c *Cluster) ApplySecret(_ context.Context, _, _ string, values map[string]string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Secret = values
	return nil
}

func (c *Cluster) ApplySecretData(_ context.Context, ns, name string, data map[string][]byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.DNSData == nil {
		c.DNSData = map[string]map[string][]byte{}
	}
	c.DNSData[ns+"/"+name] = data
	return nil
}

func (c *Cluster) DeleteJob(context.Context, string, string) error {
	c.record("delete job")
	return nil
}

func (c *Cluster) ApplyManifests(_ context.Context, m []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Manifest = string(m)
	return nil
}

func (c *Cluster) WaitForJobPod(context.Context, string, string) (string, error) { return "pod", nil }
func (c *Cluster) StreamLogs(context.Context, string, string, io.Writer) error   { return nil }
func (c *Cluster) WaitForJob(context.Context, string, string) (bool, error)      { return !c.JobFails, nil }
func (c *Cluster) JobDiagnostics(context.Context, string, string) string         { return "" }
func (c *Cluster) IngressHosts(context.Context) ([]string, error)                { return c.Hosts, nil }

func (c *Cluster) LoadBalancerAddress(context.Context, string) (string, error) { return c.LBAddr, nil }

func (c *Cluster) SecretValue(_ context.Context, ns, name, key string) (string, error) {
	if v, ok := c.Secrets[ns+"/"+name+"/"+key]; ok {
		return v, nil
	}
	return "", errors.New("not found")
}

func (c *Cluster) IsClusterAdmin(context.Context) (bool, error) { return !c.NotAdmin, nil }
func (c *Cluster) HasDefaultStorageClass(context.Context) (bool, error) {
	return !c.NoStorageClass, nil
}

// KindClusters are kind clusters that exist only as a record of what was done to them.
type KindClusters struct {
	// Existing is whether a cluster of the asked name exists.
	Existing bool
	// Calls lists "create <name>" and "delete <name>" in order.
	Calls []string
	// Cluster is what Connect returns.
	Cluster install.Cluster
	// ConnectErr makes Connect fail.
	ConnectErr error
}

var _ install.KindClusters = (*KindClusters)(nil)

func (k *KindClusters) Exists(string) (bool, error) { return k.Existing, nil }

func (k *KindClusters) Create(name string) error {
	k.Calls = append(k.Calls, "create "+name)
	k.Existing = true
	return nil
}

func (k *KindClusters) Delete(name string) error {
	k.Calls = append(k.Calls, "delete "+name)
	k.Existing = false
	return nil
}

func (k *KindClusters) Connect(string) (install.Cluster, error) { return k.Cluster, k.ConnectErr }

// Upstream is a kubriX source that serves a fixed app list and the manifest above. The installer image exists
// for the tags in Images; GitURL points at URL, e.g. a testrepo upstream.
type Upstream struct {
	URL     string
	AppList []string
	Images  []string
}

var _ install.Upstream = Upstream{}

func (u Upstream) Apps(context.Context, string, string, string, string) ([]string, error) {
	return u.AppList, nil
}

func (u Upstream) Manifest(context.Context, string, string) ([]byte, error) {
	return []byte(Manifest), nil
}

func (u Upstream) ImageTagExists(_ context.Context, tag string) (bool, error) {
	return slices.Contains(u.Images, tag), nil
}

func (u Upstream) GitURL(string) string { return u.URL }
