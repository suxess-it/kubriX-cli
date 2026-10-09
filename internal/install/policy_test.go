package install

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/suxess-it/kubrix-cli/internal/platformrepo"
	"github.com/suxess-it/kubrix-cli/internal/testrepo"
)

func TestMissingDependencies(t *testing.T) {
	available := []string{"crossplane", "cnpg", "keycloak", "backstage"}
	cases := []struct {
		selected []string
		want     []string
	}{
		{[]string{"crossplane", "cnpg", "keycloak"}, nil},
		{[]string{"keycloak", "cnpg"}, []string{"keycloak needs crossplane"}},
		{[]string{"keycloak"}, []string{"keycloak needs crossplane, cnpg"}},
		{[]string{"backstage"}, nil},
		{[]string{}, nil},
	}
	for _, c := range cases {
		if got := missingDependencies(c.selected, available); !slices.Equal(got, c.want) {
			t.Errorf("missingDependencies(%v) = %v, want %v", c.selected, got, c.want)
		}
	}
	if got := missingDependencies([]string{"keycloak"}, []string{"keycloak"}); got != nil {
		t.Errorf("dependencies outside the target must be ignored, got %v", got)
	}
}

func TestAddDependencies(t *testing.T) {
	available := []string{"crossplane", "cnpg", "keycloak", "backstage"}
	got := addDependencies([]string{"backstage", "keycloak"}, available)
	if !slices.Equal(got, []string{"backstage", "keycloak", "crossplane", "cnpg"}) {
		t.Fatalf("got %v", got)
	}
	if got := addDependencies([]string{"keycloak"}, []string{"keycloak", "cnpg"}); !slices.Equal(got, []string{"keycloak", "cnpg"}) {
		t.Fatalf("unavailable dependencies must not be added, got %v", got)
	}
}

func TestDependenciesExistInOSSStack(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(testrepo.KubrixRepo(t), "platform-apps/target-chart/values-kubrix-oss-stack.yaml.tmpl"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := platformrepo.ParseConfig(platformrepo.CustomerConfig(platformrepo.ConfigInput{ClusterType: "kind"}))
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := platformrepo.Template("oss", data, cfg)
	if err != nil {
		t.Fatal(err)
	}
	apps, err := platformrepo.Apps(rendered)
	if err != nil {
		t.Fatal(err)
	}
	for app, deps := range appDependencies {
		for _, name := range append([]string{app}, deps...) {
			if !slices.Contains(apps, name) {
				t.Errorf("appDependencies references %q, which kubrix-oss-stack does not install on kind", name)
			}
		}
	}
}
