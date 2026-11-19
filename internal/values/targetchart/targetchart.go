// Package targetchart resolves the applications of a kubriX target type the way Argo CD does: it reads the
// bootstrap Application of the target type, renders the target chart with the values files it lists, and takes
// each rendered Application's chart path, value files, inline values and parameters.
//
// Templates (*.yaml.tmpl) that a platform repository has not been bootstrapped with yet are rendered in memory with
// a customer config, so an upstream kubriX checkout can be analysed as well.
package targetchart

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/suxess-it/kubrix-cli/internal/platformrepo"
	"github.com/suxess-it/kubrix-cli/internal/values/engine"
	"github.com/suxess-it/kubrix-cli/internal/values/overlay"
	"go.yaml.in/yaml/v3"
	"helm.sh/helm/v4/pkg/chart/common"
	"helm.sh/helm/v4/pkg/chart/common/util"
	"helm.sh/helm/v4/pkg/chart/v2/loader"
	helmengine "helm.sh/helm/v4/pkg/engine"
)

// Dir is the target chart inside a platform repository.
const Dir = "platform-apps/target-chart"

// RenderedSuffix marks a layer that was rendered from a template, e.g. "values-x.yaml.tmpl (rendered)".
const RenderedSuffix = " (rendered)"

// Options select what to resolve. Either TargetType (looked up in Repo) or TargetValues (an explicit profile file).
type Options struct {
	// Repo is the platform repository root.
	Repo string
	// TargetType names bootstrap-app-<type>.yaml, or values-<type>.yaml in the target chart when there is no bootstrap Application.
	TargetType string
	// TargetValues is a target values file used instead of TargetType, layered over the target chart's defaults.
	TargetValues string
	// Config is the customer config templates are rendered with.
	Config map[string]any
}

// DefaultConfig is the customer config used when a repository has none: a plain Kubernetes cluster without DNS,
// as the platform installer renders it for an existing cluster.
func DefaultConfig() map[string]any {
	cfg, _ := platformrepo.ParseConfig(platformrepo.CustomerConfig(platformrepo.ConfigInput{ClusterType: "k8s", DNSProvider: "none"}))
	return cfg
}

// LoadConfig reads the customer config at path, or the repository's bootstrap/customer-config.yaml when path is
// empty, over the defaults of DefaultConfig: the installer always writes every key, while a repository's sample
// config may hold only a few.
func LoadConfig(repo, path string) (map[string]any, error) {
	cfg := DefaultConfig()
	if path == "" {
		path = filepath.Join(repo, platformrepo.ConfigPath)
		if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
			return cfg, nil
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read customer config: %w", err)
	}
	given, err := platformrepo.ParseConfig(data)
	if err != nil {
		return nil, err
	}
	maps.Copy(cfg, given)
	return cfg, nil
}

// TargetTypes lists the target types of a repository: those with a bootstrap Application, and those with only a
// target values file.
func TargetTypes(repo string) ([]string, error) {
	var paths []string
	for _, pattern := range []string{"bootstrap-app-*", filepath.Join(Dir, "values-*")} {
		matches, err := filepath.Glob(filepath.Join(repo, pattern))
		if err != nil {
			return nil, err
		}
		for _, m := range matches {
			rel, err := filepath.Rel(repo, m)
			if err != nil {
				return nil, err
			}
			paths = append(paths, filepath.ToSlash(rel))
		}
	}
	return TargetTypesOf(paths), nil
}

// TargetTypesOf lists the target types named by repository paths, sorted: bootstrap-app-<type>.yaml at the root and
// platform-apps/target-chart/values-<type>.yaml, either possibly a template. Other paths are ignored.
func TargetTypesOf(paths []string) []string {
	seen := map[string]bool{}
	for _, p := range paths {
		dir, base := path.Split(p)
		var prefix string
		switch {
		case dir == "" && strings.HasPrefix(base, "bootstrap-app-"):
			prefix = "bootstrap-app-"
		case dir == Dir+"/" && strings.HasPrefix(base, "values-"):
			prefix = "values-"
		default:
			continue
		}
		name, ok := strings.CutSuffix(strings.TrimSuffix(base, ".tmpl"), ".yaml")
		name = strings.TrimPrefix(name, prefix)
		if ok && name != "kubrix-default" && name != "customer" && name != "customer-generated" {
			seen[name] = true
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Load resolves every application the target chart renders for the selected target type.
func Load(opts Options) (*overlay.Overlay, error) {
	if opts.Config == nil {
		opts.Config = DefaultConfig()
	}
	r := resolver{opts: opts, repo: opts.Repo}
	targetType, layers, params, err := r.targetLayers()
	if err != nil {
		return nil, err
	}
	apps, err := r.renderApplications(layers, params)
	if err != nil {
		return nil, err
	}
	o := &overlay.Overlay{TargetType: targetType}
	for _, a := range apps {
		app, err := r.app(a)
		if err != nil {
			return nil, err
		}
		o.Apps = append(o.Apps, app)
	}
	o.Warnings = r.warnings
	return o, nil
}

type resolver struct {
	opts     Options
	repo     string
	warnings []string
}

// targetLayers returns the target type's name and the values layers and parameters the target chart is rendered with.
func (r *resolver) targetLayers() (string, []engine.Layer, []engine.SetSource, error) {
	if r.opts.TargetValues != "" {
		profile, err := filepath.Abs(r.opts.TargetValues)
		if err != nil {
			return "", nil, nil, err
		}
		// An explicit file lives in <repo>/platform-apps/target-chart.
		r.repo = filepath.Dir(filepath.Dir(filepath.Dir(profile)))
		name := strings.TrimSuffix(strings.TrimSuffix(strings.TrimPrefix(filepath.Base(profile), "values-"), ".tmpl"), ".yaml")
		if r.opts.TargetType != "" {
			name = r.opts.TargetType
		}
		layers, err := r.chartLayers([]string{filepath.Base(platformrepo.DefaultValuesPath), filepath.Base(profile)})
		return name, layers, nil, err
	}
	if r.opts.TargetType == "" {
		return "", nil, nil, errors.New("a target type is required")
	}
	bootstrap, found, err := r.readMaybeTemplate("bootstrap-app-" + r.opts.TargetType + ".yaml")
	if err != nil {
		return "", nil, nil, err
	}
	if !found {
		// Without a bootstrap Application the profile is layered over the defaults, as platformrepo.AppsLayered does.
		profile := filepath.Base(platformrepo.TargetValuesPath(r.opts.TargetType))
		if _, ok, err := r.readMaybeTemplate(filepath.Join(Dir, profile)); err != nil || !ok {
			if err == nil {
				err = fmt.Errorf("target type %q: neither bootstrap-app-%[1]s.yaml nor %s exists in %s", r.opts.TargetType, filepath.Join(Dir, profile), r.repo)
			}
			return "", nil, nil, err
		}
		layers, err := r.chartLayers([]string{filepath.Base(platformrepo.DefaultValuesPath), profile})
		return r.opts.TargetType, layers, nil, err
	}
	var app application
	if err := yaml.Unmarshal(bootstrap, &app); err != nil {
		return "", nil, nil, fmt.Errorf("parse bootstrap-app-%s.yaml: %w", r.opts.TargetType, err)
	}
	if app.Spec.Source.Path != "" && filepath.Clean(app.Spec.Source.Path) != Dir {
		return "", nil, nil, fmt.Errorf("bootstrap-app-%s.yaml points at %s, not %s", r.opts.TargetType, app.Spec.Source.Path, Dir)
	}
	layers, err := r.chartLayers(app.Spec.Source.Helm.ValueFiles)
	if err != nil {
		return "", nil, nil, err
	}
	// Argo CD substitutes its build environment into parameters; use the bootstrap Application's own source.
	env := strings.NewReplacer("$ARGOCD_APP_SOURCE_REPO_URL", app.Spec.Source.RepoURL, "$ARGOCD_APP_SOURCE_TARGET_REVISION", app.Spec.Source.TargetRevision)
	params := make([]engine.SetSource, len(app.Spec.Source.Helm.Parameters))
	for i, p := range app.Spec.Source.Helm.Parameters {
		p.Value = env.Replace(p.Value)
		params[i] = p.setSource("bootstrap-app-"+r.opts.TargetType+".yaml parameters", i)
	}
	return r.opts.TargetType, layers, params, nil
}

// chartLayers reads target chart values files, skipping missing ones like ignoreMissingValueFiles.
func (r *resolver) chartLayers(files []string) ([]engine.Layer, error) {
	var layers []engine.Layer
	for _, f := range files {
		data, name, ok, err := r.readLayer(filepath.Join(Dir, f))
		if err != nil {
			return nil, err
		}
		if ok {
			layers = append(layers, engine.Layer{Name: name, Data: data})
		}
	}
	return layers, nil
}

// readMaybeTemplate reads a repository file, or renders its template when only the template exists.
func (r *resolver) readMaybeTemplate(rel string) ([]byte, bool, error) {
	data, _, ok, err := r.readLayer(rel)
	return data, ok, err
}

// readLayer is readMaybeTemplate that also returns the name sources refer to: the path, or the template marked as rendered.
func (r *resolver) readLayer(rel string) ([]byte, string, bool, error) {
	data, err := os.ReadFile(filepath.Join(r.repo, rel))
	if err == nil {
		return data, filepath.ToSlash(rel), true, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, "", false, err
	}
	tmplPath := platformrepo.TemplatePath(rel)
	tmpl, err := os.ReadFile(filepath.Join(r.repo, tmplPath))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, "", false, nil
	}
	if err != nil {
		return nil, "", false, err
	}
	rendered, err := platformrepo.Template(tmplPath, tmpl, r.opts.Config)
	if err != nil {
		return nil, "", false, fmt.Errorf("render %s: %w", tmplPath, err)
	}
	return rendered, filepath.ToSlash(tmplPath) + RenderedSuffix, true, nil
}

// renderApplications renders the target chart and returns its Argo CD Applications sorted by name.
func (r *resolver) renderApplications(layers []engine.Layer, params []engine.SetSource) ([]application, error) {
	chartDir := filepath.Join(r.repo, Dir)
	ch, err := loader.LoadDir(chartDir)
	if err != nil {
		return nil, fmt.Errorf("load target chart: %w", err)
	}
	vals, err := overlay.HelmValues(layers, params)
	if err != nil {
		return nil, fmt.Errorf("target values: %w", err)
	}
	renderVals, err := util.ToRenderValues(ch, vals, common.ReleaseOptions{Name: "sx-bootstrap-app", Namespace: "argocd", IsInstall: true}, nil)
	if err != nil {
		return nil, fmt.Errorf("target chart values: %w", err)
	}
	files, err := helmengine.Render(ch, renderVals)
	if err != nil {
		return nil, fmt.Errorf("render target chart: %w", err)
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	var apps []application
	for _, name := range names {
		dec := yaml.NewDecoder(bytes.NewReader([]byte(files[name])))
		for {
			var a application
			err := dec.Decode(&a)
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return nil, fmt.Errorf("parse rendered %s: %w", name, err)
			}
			if a.Kind == "Application" {
				apps = append(apps, a)
			}
		}
	}
	slices.SortFunc(apps, func(a, b application) int { return strings.Compare(a.Metadata.Name, b.Metadata.Name) })
	return apps, nil
}

// app turns a rendered Application into the application's values layers and set sources, in Argo CD's precedence:
// value files, then inline values, then parameters.
func (r *resolver) app(a application) (overlay.App, error) {
	name := strings.TrimPrefix(a.Metadata.Name, "sx-")
	src := a.Spec.Source
	chartDir := filepath.Clean(src.Path)
	app := overlay.App{Name: name, Chart: filepath.Base(chartDir), Dir: filepath.ToSlash(chartDir)}
	if meta, ok, err := r.readMaybeTemplate(filepath.Join(chartDir, "Chart.yaml")); err != nil {
		return overlay.App{}, err
	} else if ok {
		var c struct {
			Name string `yaml:"name"`
		}
		if err := yaml.Unmarshal(meta, &c); err == nil && c.Name != "" {
			app.Chart = c.Name
		}
	} else {
		r.warnings = append(r.warnings, fmt.Sprintf("%s: chart %s does not exist", name, chartDir))
	}
	for _, f := range src.Helm.ValueFiles {
		data, layerName, ok, err := r.readLayer(filepath.Join(chartDir, f))
		if err != nil {
			return overlay.App{}, err
		}
		if !ok {
			if !src.Helm.IgnoreMissingValueFiles {
				return overlay.App{}, fmt.Errorf("%s: values file %s does not exist", name, filepath.Join(chartDir, f))
			}
			continue
		}
		app.Layers = append(app.Layers, engine.Layer{Name: layerName, Data: data})
	}
	inline := fmt.Sprintf("Application sx-%s", name)
	if src.Helm.Values != "" {
		app.Layers = append(app.Layers, engine.Layer{Name: inline + " helm.values", Data: []byte(src.Helm.Values)})
	}
	if src.Helm.ValuesObject.Kind != 0 {
		data, err := yaml.Marshal(&src.Helm.ValuesObject)
		if err != nil {
			return overlay.App{}, err
		}
		app.Layers = append(app.Layers, engine.Layer{Name: inline + " helm.valuesObject", Data: data})
	}
	for i, p := range src.Helm.Parameters {
		app.Sets = append(app.Sets, p.setSource(inline+" parameters", i))
	}
	return app, nil
}

// escapeSet escapes a parameter for strvals like Argo CD does before passing it to helm --set.
func escapeSet(s string) string {
	return strings.NewReplacer(`\`, `\\`, `,`, `\,`).Replace(s)
}

type parameter struct {
	Name        string `yaml:"name"`
	Value       string `yaml:"value"`
	ForceString bool   `yaml:"forceString"`
}

// setSource is the parameter as Argo CD passes it to helm: --set, or --set-string with forceString.
func (p parameter) setSource(file string, i int) engine.SetSource {
	return engine.SetSource{
		Expression:   escapeSet(p.Name) + "=" + escapeSet(p.Value),
		Source:       engine.SourceLocation{File: file, Line: i + 1, Column: 1},
		StringValues: p.ForceString,
	}
}

type application struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	Spec struct {
		Source struct {
			RepoURL        string `yaml:"repoURL"`
			TargetRevision string `yaml:"targetRevision"`
			Path           string `yaml:"path"`
			Helm           struct {
				ValueFiles              []string    `yaml:"valueFiles"`
				IgnoreMissingValueFiles bool        `yaml:"ignoreMissingValueFiles"`
				Values                  string      `yaml:"values"`
				ValuesObject            yaml.Node   `yaml:"valuesObject"`
				Parameters              []parameter `yaml:"parameters"`
			} `yaml:"helm"`
		} `yaml:"source"`
	} `yaml:"spec"`
}
