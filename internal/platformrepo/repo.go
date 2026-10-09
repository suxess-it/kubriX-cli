// Package platformrepo knows the shape of a platform repository, the repository kubriX is installed from: where
// the customer config lives, which files are templates and what they render to, how the target values file lists
// the applications, and how apps are left out of it and found again.
//
// It reproduces install-platform.sh's bootstrap templating (bootstrap_template_downstream_repo): write
// bootstrap/customer-config.yaml, render every *.yaml.tmpl with it as `.kubriX`, and drop excluded apps.
package platformrepo

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const (
	// TemplateSuffix marks a file that is rendered with the customer config; the output has no ".tmpl".
	TemplateSuffix = ".yaml.tmpl"
	// TemplateGlob is the git pathspec matching templates.
	TemplateGlob = "*" + TemplateSuffix
)

// IsTemplate reports whether the path is a template that renders to OutputPath(path).
func IsTemplate(path string) bool { return strings.HasSuffix(path, TemplateSuffix) }

// TemplatePath is the template an output is rendered from.
func TemplatePath(output string) string { return output + ".tmpl" }

// TargetValuesPath is the values file of a target type; it lists the applications to install.
func TargetValuesPath(targetType string) string {
	return "platform-apps/target-chart/values-" + targetType + ".yaml"
}

// TemplateApps renders a target values template as the bootstrap would for the given config and returns the
// applications it lists.
func TemplateApps(name string, tmpl []byte, in ConfigInput) ([]string, error) {
	cfg, err := ParseConfig(CustomerConfig(in))
	if err != nil {
		return nil, err
	}
	rendered, err := Template(name, tmpl, cfg)
	if err != nil {
		return nil, err
	}
	return Apps(rendered)
}

// Repo is a platform repository checked out in a directory.
type Repo struct{ dir string }

// At returns the platform repository checked out in dir.
func At(dir string) *Repo { return &Repo{dir: dir} }

// WriteConfig writes the customer config the templates are rendered with.
func (r *Repo) WriteConfig(in ConfigInput) error {
	path := filepath.Join(r.dir, ConfigPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, CustomerConfig(in), 0o644)
}

// Config reads the customer config.
func (r *Repo) Config() (map[string]any, error) {
	data, err := os.ReadFile(filepath.Join(r.dir, ConfigPath))
	if err != nil {
		return nil, fmt.Errorf("%s not found: was the repository bootstrapped by kubriX? %w", ConfigPath, err)
	}
	return ParseConfig(data)
}

// RenderAll renders every template of the repository, like the two gomplate calls in install-platform.sh.
func (r *Repo) RenderAll() error {
	cfg, err := r.Config()
	if err != nil {
		return err
	}
	return renderTree(r.dir, cfg)
}

// Render renders the given templates (paths relative to the repository) and returns the files written.
func (r *Repo) Render(tmpls []string) ([]string, error) {
	cfg, err := r.Config()
	if err != nil {
		return nil, err
	}
	if err := renderFiles(r.dir, tmpls, cfg); err != nil {
		return nil, err
	}
	outputs := make([]string, len(tmpls))
	for i, tmpl := range tmpls {
		outputs[i] = OutputPath(tmpl)
	}
	return outputs, nil
}

// Exclude leaves the apps out of the target values file. Without apps the file stays as it is.
func (r *Repo) Exclude(targetType string, apps []string) error {
	if len(apps) == 0 {
		return nil
	}
	path := filepath.Join(r.dir, TargetValuesPath(targetType))
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return os.WriteFile(path, ApplyExcludes(data, apps), 0o644)
}

// ExcludedApps finds the apps that were left out of the target values file, by comparing the file with what its
// template renders to. It is the inverse of Exclude. A target type without a template has nothing to re-apply.
func (r *Repo) ExcludedApps(targetType string) ([]string, error) {
	out := TargetValuesPath(targetType)
	tmpl, err := os.ReadFile(filepath.Join(r.dir, TemplatePath(out)))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	cfg, err := r.Config()
	if err != nil {
		return nil, err
	}
	full, err := Template(out, tmpl, cfg)
	if err != nil {
		return nil, err
	}
	all, err := Apps(full)
	if err != nil {
		return nil, err
	}
	rendered, err := os.ReadFile(filepath.Join(r.dir, out))
	if err != nil {
		return nil, err
	}
	present, err := Apps(rendered)
	if err != nil {
		return nil, err
	}
	var excluded []string
	for _, app := range all {
		if !slices.Contains(present, app) {
			excluded = append(excluded, app)
		}
	}
	return excluded, nil
}
