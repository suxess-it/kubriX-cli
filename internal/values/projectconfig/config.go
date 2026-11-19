// Package projectconfig loads .kubrix-values.yaml, a hand-written list of the values files of each application per
// target type, for repositories that do not follow the kubriX target-chart layout.
package projectconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/suxess-it/kubrix-cli/internal/values/engine"
	"github.com/suxess-it/kubrix-cli/internal/values/overlay"
	"go.yaml.in/yaml/v3"
)

// DefaultPath is where the project config is looked for when --values-config is not given.
const DefaultPath = ".kubrix-values.yaml"

type FileRef struct {
	Path     string `json:"path"`
	Optional bool   `json:"optional,omitempty"`
}

func (f *FileRef) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		f.Path = n.Value
		return nil
	}
	type plain FileRef
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("file must be a path string or object")
	}
	if err := n.Decode((*plain)(f)); err != nil {
		return err
	}
	if f.Path == "" {
		return fmt.Errorf("file path must not be empty")
	}
	return nil
}

type Application struct {
	Chart string    `yaml:"chart" json:"chart"`
	Files []FileRef `yaml:"files" json:"files"`
}
type Target struct {
	Applications map[string]Application `yaml:"applications" json:"applications"`
}
type Config struct {
	Version string            `yaml:"version" json:"version"`
	Targets map[string]Target `yaml:"targets" json:"targets"`
	baseDir string
}

func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %q: %w", path, err)
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("parse config %q: %w", path, err)
	}
	if c.Version != "v1" {
		return nil, fmt.Errorf("config %q: unsupported version %q (expected v1)", path, c.Version)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	c.baseDir = filepath.Dir(abs)
	return &c, nil
}

// Overlay resolves every application of a target type. Missing optional files are skipped; a missing required file,
// an empty file list or an application without any existing file is an error.
func (c *Config) Overlay(targetType string) (*overlay.Overlay, error) {
	t, ok := c.Targets[targetType]
	if !ok {
		return nil, fmt.Errorf("target type %q does not exist in the project config", targetType)
	}
	o := &overlay.Overlay{TargetType: targetType}
	names := make([]string, 0, len(t.Applications))
	for name := range t.Applications {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		app, warnings, err := c.resolve(name, t.Applications[name])
		if err != nil {
			return nil, err
		}
		o.Apps = append(o.Apps, app)
		o.Warnings = append(o.Warnings, warnings...)
	}
	return o, nil
}

func (c *Config) resolve(name string, a Application) (overlay.App, []string, error) {
	if len(a.Files) == 0 {
		return overlay.App{}, nil, fmt.Errorf("application %q has an empty file list", name)
	}
	app := overlay.App{Name: name, Chart: a.Chart}
	if app.Chart == "" {
		app.Chart = name
	}
	var warnings []string
	seen := map[string]bool{}
	for _, ref := range a.Files {
		p := ref.Path
		if !filepath.IsAbs(p) {
			p = filepath.Join(c.baseDir, p)
		}
		p = filepath.Clean(p)
		if seen[p] {
			warnings = append(warnings, fmt.Sprintf("%s: duplicate file %s", name, p))
		}
		seen[p] = true
		data, err := os.ReadFile(p)
		if os.IsNotExist(err) && ref.Optional {
			continue
		}
		if err != nil {
			return overlay.App{}, nil, fmt.Errorf("values file %q: %w", p, err)
		}
		app.Layers = append(app.Layers, engine.Layer{Name: p, Data: data})
	}
	if len(app.Layers) == 0 {
		return overlay.App{}, nil, fmt.Errorf("application %q has no existing values files", name)
	}
	return app, warnings, nil
}
