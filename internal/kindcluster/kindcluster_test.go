package kindcluster

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/suxess-it/kubrix-cli/internal/testrepo"
	"sigs.k8s.io/kind/pkg/apis/config/v1alpha4"
	"sigs.k8s.io/yaml"
)

// TestConfigMatchesCI fails when .github/kind-config.yaml changes without Config() following.
func TestConfigMatchesCI(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(testrepo.KubrixRepo(t), ".github/kind-config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var ci v1alpha4.Cluster
	if err := yaml.UnmarshalStrict(data, &ci); err != nil {
		t.Fatal(err)
	}
	if got := Config(); !reflect.DeepEqual(*got, ci) {
		t.Fatalf("Config() drifted from .github/kind-config.yaml\n got: %+v\nwant: %+v", *got, ci)
	}
}

func TestParseDockerInfo(t *testing.T) {
	r, err := parseDockerInfo("4 16765325312\n")
	if err != nil {
		t.Fatal(err)
	}
	if r.CPUs != 4 || r.Memory != 16765325312 || !r.Sufficient() {
		t.Fatalf("unexpected %+v", r)
	}
	if (Resources{CPUs: 2, Memory: 32 << 30}).Sufficient() {
		t.Error("2 CPUs must be insufficient")
	}
	if (Resources{CPUs: 8, Memory: 8 << 30}).Sufficient() {
		t.Error("8 GiB must be insufficient")
	}
	if _, err := parseDockerInfo("garbage"); err == nil {
		t.Error("expected parse error")
	}
}
