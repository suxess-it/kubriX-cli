// Package overlay describes which values layers make up each application of a target type, independent of where
// that list comes from: a kubriX target chart, a .kubrix-values.yaml project config or files given on the command line.
package overlay

import (
	"fmt"
	"maps"
	"slices"

	"helm.sh/helm/v4/pkg/chart/common"
	"helm.sh/helm/v4/pkg/strvals"

	"github.com/suxess-it/kubrix-cli/internal/values/engine"
)

// App is one application: the chart it installs, its values layers in merge order and the set sources applied after them.
type App struct {
	Name  string
	Chart string
	// Dir is the chart's directory relative to the repository, when the overlay knows it (the target chart does).
	Dir    string
	Layers []engine.Layer
	Sets   []engine.SetSource
}

// Overlay is the applications of one target type, sorted by name, and what was skipped or doubtful while resolving them.
type Overlay struct {
	TargetType string
	Apps       []App
	Warnings   []string
}

// App returns the named application.
func (o *Overlay) App(name string) (App, error) {
	i := slices.IndexFunc(o.Apps, func(a App) bool { return a.Name == name })
	if i < 0 {
		return App{}, fmt.Errorf("application %q does not exist in target type %q", name, o.TargetType)
	}
	return o.Apps[i], nil
}

// Merge merges the application's layers and set sources, then the user's set sources.
func (o *Overlay) Merge(app App, userSets []engine.SetSource) (*engine.Result, error) {
	sets := append(slices.Clone(app.Sets), userSets...)
	return engine.Merge(app.Layers, sets, engine.Options{Target: o.TargetType, Application: app.Name, Chart: app.Chart})
}

// LayerNames are the names of the application's layers in merge order.
func (a App) LayerNames() []string {
	names := make([]string, len(a.Layers))
	for i, l := range a.Layers {
		names[i] = l.Name
	}
	return names
}

// HelmValues are the values helm itself computes from the layers and set sources, as for `helm template -f … --set …`:
// layers merge recursively with later maps winning, then each set expression is parsed into the result, so
// --set servers[0].host=x changes one list item where a values layer would replace the list.
func HelmValues(layers []engine.Layer, sets []engine.SetSource) (map[string]any, error) {
	vals := map[string]any{}
	for _, l := range layers {
		v, err := common.ReadValues(l.Data)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", l.Name, err)
		}
		vals = mergeMaps(vals, v)
	}
	for _, s := range sets {
		parse := strvals.ParseInto
		if s.StringValues {
			parse = strvals.ParseIntoString
		}
		if err := parse(s.Expression, vals); err != nil {
			return nil, fmt.Errorf("parse set source %s:%d %q: %w", s.Source.File, s.Source.Line, s.Expression, err)
		}
	}
	return vals, nil
}

// mergeMaps merges b into a like helm's -f handling: maps merge recursively, everything else is replaced.
func mergeMaps(a, b map[string]any) map[string]any {
	out := make(map[string]any, len(a))
	maps.Copy(out, a)
	for k, v := range b {
		if vm, ok := v.(map[string]any); ok {
			if om, ok := out[k].(map[string]any); ok {
				out[k] = mergeMaps(om, vm)
				continue
			}
		}
		out[k] = v
	}
	return out
}
