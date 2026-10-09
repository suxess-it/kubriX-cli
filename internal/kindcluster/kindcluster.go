package kindcluster

import (
	"fmt"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/kind/pkg/apis/config/v1alpha4"
	"sigs.k8s.io/kind/pkg/cluster"
	"sigs.k8s.io/kind/pkg/log"
)

// Known-good setup: devcontainer hostRequirements (4 CPUs) and CI's ubuntu-latest runners (16 GB).
// Docker reports slightly less than the configured memory, hence 15 GiB.
const (
	MinCPUs   = 4
	MinMemory = 15 << 30
)

// Config mirrors .github/kind-config.yaml; TestConfigMatchesCI guards against drift.
func Config() *v1alpha4.Cluster {
	return &v1alpha4.Cluster{
		TypeMeta: v1alpha4.TypeMeta{Kind: "Cluster", APIVersion: "kind.x-k8s.io/v1alpha4"},
		Nodes: []v1alpha4.Node{{
			Role: v1alpha4.ControlPlaneRole,
			KubeadmConfigPatches: []string{`kind: InitConfiguration
nodeRegistration:
  kubeletExtraArgs:
    node-labels: "ingress-ready=true"
    max-pods: "300"
`},
			ExtraPortMappings: []v1alpha4.PortMapping{
				{ContainerPort: 80, HostPort: 80, Protocol: v1alpha4.PortMappingProtocolTCP},
				{ContainerPort: 443, HostPort: 443, Protocol: v1alpha4.PortMappingProtocolTCP},
			},
		}},
	}
}

type Provider struct {
	p *cluster.Provider
}

func NewProvider() *Provider {
	return &Provider{p: cluster.NewProvider(cluster.ProviderWithDocker(), cluster.ProviderWithLogger(log.NoopLogger{}))}
}

func (p *Provider) Exists(name string) (bool, error) {
	names, err := p.p.List()
	if err != nil {
		return false, err
	}
	return slices.Contains(names, name), nil
}

func (p *Provider) Create(name string) error {
	return p.p.Create(name,
		cluster.CreateWithV1Alpha4Config(Config()),
		cluster.CreateWithWaitForReady(5*time.Minute),
		cluster.CreateWithDisplayUsage(false),
		cluster.CreateWithDisplaySalutation(false),
	)
}

func (p *Provider) Delete(name string) error {
	return p.p.Delete(name, "")
}

func (p *Provider) RESTConfig(name string) (*rest.Config, error) {
	kubeconfig, err := p.p.KubeConfig(name, false)
	if err != nil {
		return nil, err
	}
	return clientcmd.RESTConfigFromKubeConfig([]byte(kubeconfig))
}

type Resources struct {
	CPUs   int
	Memory int64
}

func (r Resources) Sufficient() bool { return r.CPUs >= MinCPUs && r.Memory >= MinMemory }

func (r Resources) String() string {
	return fmt.Sprintf("%d CPUs, %.1f GiB memory", r.CPUs, float64(r.Memory)/(1<<30))
}

// DockerResources also serves as the "is Docker reachable" check; kind itself drives the docker CLI.
func DockerResources() (Resources, error) {
	out, err := exec.Command("docker", "info", "--format", "{{.NCPU}} {{.MemTotal}}").Output()
	if err != nil {
		return Resources{}, fmt.Errorf("docker is not reachable (is Docker running?): %w", err)
	}
	return parseDockerInfo(string(out))
}

func parseDockerInfo(out string) (Resources, error) {
	fields := strings.Fields(out)
	if len(fields) != 2 {
		return Resources{}, fmt.Errorf("unexpected docker info output %q", out)
	}
	cpus, err := strconv.Atoi(fields[0])
	if err != nil {
		return Resources{}, err
	}
	mem, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return Resources{}, err
	}
	return Resources{CPUs: cpus, Memory: mem}, nil
}
