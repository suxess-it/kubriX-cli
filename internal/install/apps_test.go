package install

import (
	"slices"
	"strings"
	"testing"
)

func TestNewAppsSplitsRequiredAndOptional(t *testing.T) {
	a := newApps([]string{"traefik", "argocd", "grafana", "kargo"}, nil)
	if !slices.Equal(a.Required, []string{"traefik", "argocd"}) || !slices.Equal(a.Optional, []string{"grafana", "kargo"}) {
		t.Errorf("required %v optional %v", a.Required, a.Optional)
	}
	if !slices.Equal(a.Selected, []string{"grafana", "kargo"}) {
		t.Errorf("everything optional starts selected: %v", a.Selected)
	}
}

func TestNewAppsHonoursSavedExclusions(t *testing.T) {
	a := newApps([]string{"grafana", "kargo"}, []string{"kargo"})
	if !slices.Equal(a.Selected, []string{"grafana"}) {
		t.Errorf("selected %v", a.Selected)
	}
}

// A saved exclusion must not break a kept app, so dependencies of a selected app start selected.
func TestNewAppsSelectsDependenciesOfKeptApps(t *testing.T) {
	a := newApps([]string{"keycloak", "crossplane", "cnpg"}, []string{"crossplane", "cnpg"})
	if !slices.Contains(a.Selected, "crossplane") || !slices.Contains(a.Selected, "cnpg") {
		t.Errorf("selected %v", a.Selected)
	}
	if !slices.Equal(a.Dependencies["keycloak"], []string{"crossplane", "cnpg"}) {
		t.Errorf("dependencies %v", a.Dependencies)
	}
}

func TestAppsValidate(t *testing.T) {
	a := newApps([]string{"keycloak", "crossplane", "cnpg", "kargo"}, nil)
	if err := a.Validate([]string{"keycloak", "crossplane", "cnpg"}); err != nil {
		t.Errorf("complete selection: %v", err)
	}
	if err := a.Validate([]string{"kargo"}); err != nil {
		t.Errorf("dropping keycloak with its dependencies is fine: %v", err)
	}
	err := a.Validate([]string{"keycloak", "cnpg"})
	if err == nil || !strings.Contains(err.Error(), "keycloak needs crossplane") {
		t.Errorf("got %v", err)
	}
}

func TestAppsExcludedIsEveryUnselectedOptionalApp(t *testing.T) {
	a := newApps([]string{"traefik", "grafana", "kargo", "backstage"}, nil)
	if got := a.excluded([]string{"kargo"}); !slices.Equal(got, []string{"grafana", "backstage"}) {
		t.Errorf("excluded %v", got)
	}
	if got := a.excluded(a.Optional); got != nil {
		t.Errorf("nothing excluded, got %v", got)
	}
}
