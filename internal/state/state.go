// Package state keeps the Installations a user has made: the catalog of saved selections, stored in one file.
package state

import (
	"os"
	"path/filepath"
	"runtime"
)

const (
	KindDemo = "kind"
	Cluster  = "cluster"
)

// Installation holds one installation's selections. It deliberately has no token or DNS credential field.
type Installation struct {
	Kind           string   `json:"kind,omitempty"`
	GitHost        string   `json:"gitHost,omitempty"`
	Org            string   `json:"org,omitempty"`
	Repo           string   `json:"repo,omitempty"`
	GitUserName    string   `json:"gitUserName,omitempty"`
	TargetType     string   `json:"targetType,omitempty"`
	UpstreamRepo   string   `json:"upstreamRepo,omitempty"`
	UpstreamBranch string   `json:"upstreamBranch,omitempty"`
	ClusterName    string   `json:"clusterName,omitempty"`
	Context        string   `json:"context,omitempty"`
	Domain         string   `json:"domain,omitempty"`
	DNSProvider    string   `json:"dnsProvider,omitempty"`
	CloudProvider  string   `json:"cloudProvider,omitempty"`
	ExcludedApps   []string `json:"excludedApps,omitempty"`
}

func (st Installation) IsCluster() bool { return st.Kind == Cluster }

// Key identifies an installation: the kind cluster name for demos, the kube context otherwise.
func (st Installation) Key() string {
	if st.IsCluster() {
		return st.Context
	}
	return st.ClusterName
}

// Where says where the platform runs, e.g. "kind cluster kubrix-demo" or "context prod".
func (st Installation) Where() string {
	if st.IsCluster() {
		return "context " + st.Context
	}
	return "kind cluster " + st.ClusterName
}

// Dir is $XDG_CONFIG_HOME/kubrix, else ~/.config/kubrix (also on macOS, like gh), and %AppData%\kubrix on Windows.
func Dir() (string, error) {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "kubrix"), nil
	}
	if runtime.GOOS == "windows" {
		base, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(base, "kubrix"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "kubrix"), nil
}
