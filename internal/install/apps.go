package install

import (
	"errors"
	"slices"
	"strings"
)

// requiredApps are always installed: other apps depend on them, so they are not offered for exclusion.
var requiredApps = []string{"traefik", "cert-manager", "argocd", "external-secrets", "openbao"}

// appDependencies lists apps that must stay installed while the key app is installed.
var appDependencies = map[string][]string{
	"keycloak": {"crossplane", "cnpg"},
}

// missingDependencies reports selected apps whose dependencies are deselected, e.g. "keycloak needs crossplane, cnpg".
// Dependencies that are not in available (not part of the target, or always installed) are ignored.
func missingDependencies(selected, available []string) []string {
	var problems []string
	for _, app := range selected {
		var missing []string
		for _, dep := range appDependencies[app] {
			if slices.Contains(available, dep) && !slices.Contains(selected, dep) {
				missing = append(missing, dep)
			}
		}
		if len(missing) > 0 {
			problems = append(problems, app+" needs "+strings.Join(missing, ", "))
		}
	}
	return problems
}

// addDependencies returns selected plus every available dependency of a selected app, transitively.
func addDependencies(selected, available []string) []string {
	out := slices.Clone(selected)
	for i := 0; i < len(out); i++ {
		for _, dep := range appDependencies[out[i]] {
			if slices.Contains(available, dep) && !slices.Contains(out, dep) {
				out = append(out, dep)
			}
		}
	}
	return out
}

// Apps is the choice of applications offered while bootstrapping a fresh platform repository.
type Apps struct {
	// Required are always installed because other apps depend on them.
	Required []string
	// Optional can be left out.
	Optional []string
	// Selected is the initial selection of Optional: saved exclusions are honoured, but never break a kept app.
	Selected []string
	// Dependencies maps optional apps to the apps that must stay installed with them.
	Dependencies map[string][]string
}

func newApps(available, excluded []string) *Apps {
	a := &Apps{Dependencies: map[string][]string{}}
	for _, app := range available {
		if slices.Contains(requiredApps, app) {
			a.Required = append(a.Required, app)
			continue
		}
		a.Optional = append(a.Optional, app)
		if !slices.Contains(excluded, app) {
			a.Selected = append(a.Selected, app)
		}
	}
	a.Selected = addDependencies(a.Selected, a.Optional)
	for app, deps := range appDependencies {
		if slices.Contains(a.Optional, app) {
			a.Dependencies[app] = deps
		}
	}
	return a
}

// Validate reports a selection that drops an app another selected app needs.
func (a *Apps) Validate(selected []string) error {
	if problems := missingDependencies(selected, a.Optional); len(problems) > 0 {
		return errors.New(strings.Join(problems, "; ") + ". Select them again, or deselect the app that needs them.")
	}
	return nil
}

// excluded is every optional app that is not selected.
func (a *Apps) excluded(selected []string) []string {
	var out []string
	for _, app := range a.Optional {
		if !slices.Contains(selected, app) {
			out = append(out, app)
		}
	}
	return out
}
