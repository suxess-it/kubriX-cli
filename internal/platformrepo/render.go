package platformrepo

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"

	"sigs.k8s.io/yaml"
)

const ConfigPath = "bootstrap/customer-config.yaml"

// TemplateDirs are the directories install-platform.sh runs gomplate over.
var TemplateDirs = []string{"platform-apps", "backstage-resources"}

// ConfigInput holds the KUBRIX_* values that end up in customer-config.yaml.
type ConfigInput struct {
	ClusterType    string
	CloudProvider  string
	DNSProvider    string
	Domain         string
	Repo           string // KUBRIX_REPO, e.g. https://github.com/org/name.git
	GitUser        string
	MetalLBIP      string
	TShirtSize     string
	SecurityStrict bool
	HAEnabled      bool
}

// CustomerConfig renders customer-config.yaml exactly like the installer's heredoc,
// including its defaults for values the OSS CLI does not ask for.
var scheme = regexp.MustCompile(`^[a-z]+://`)

func CustomerConfig(in ConfigInput) []byte {
	url := scheme.ReplaceAllString(in.Repo, "")
	parts := strings.Split(url, "/")
	org, name := "", ""
	if len(parts) > 1 {
		org = parts[1]
	}
	if len(parts) > 2 {
		name = strings.TrimSuffix(parts[2], ".git")
	}
	return fmt.Appendf(nil, `clusterType: %s
cloudProvider: %s
dnsProvider: %s
certManagerDnsProvider: none
tShirtSize: %s
securityStrict: %t
haEnabled: %t
domain: %s
gitRepo: %s
gitRepoOrg: %s
gitRepoName: %s
gitUser: %s
metalLbIp: %s
`, in.ClusterType, or(in.CloudProvider, "on-prem"), in.DNSProvider, or(in.TShirtSize, "small"), in.SecurityStrict, in.HAEnabled,
		in.Domain, in.Repo, org, name, in.GitUser, or(in.MetalLBIP, " "))
}

func or(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// ParseConfig parses customer-config.yaml the way gomplate's YAML context does.
func ParseConfig(data []byte) (map[string]any, error) {
	cfg := map[string]any{}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", ConfigPath, err)
	}
	return cfg, nil
}

// gomplateStrings covers the one gomplate function older releases' templates use
// (`{{ .kubriX.gitRepo | strings.TrimSuffix ".git" }}`, v5/v6). Arguments follow gomplate's order.
type gomplateStrings struct{}

func (gomplateStrings) TrimSuffix(suffix string, s any) string {
	return strings.TrimSuffix(fmt.Sprint(s), suffix)
}

var funcs = template.FuncMap{"strings": func() gomplateStrings { return gomplateStrings{} }}

// Template renders one template. missingkey=error matches gomplate v4's default.
func Template(name string, tmpl []byte, cfg map[string]any) ([]byte, error) {
	t, err := template.New(name).Option("missingkey=error").Funcs(funcs).Parse(string(tmpl))
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := t.Execute(&out, map[string]any{"kubriX": cfg}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// OutputPath maps a template path to the file it renders to.
func OutputPath(tmplPath string) string { return strings.TrimSuffix(tmplPath, ".tmpl") }

// templates lists all *.yaml.tmpl files below the template dirs, relative to root.
func templates(root string) ([]string, error) {
	var out []string
	for _, dir := range TemplateDirs {
		if _, err := os.Stat(filepath.Join(root, dir)); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() && IsTemplate(path) {
				rel, err := filepath.Rel(root, path)
				if err != nil {
					return err
				}
				out = append(out, filepath.ToSlash(rel))
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// renderFiles renders the given templates (paths relative to root) into their output files.
func renderFiles(root string, tmpls []string, cfg map[string]any) error {
	for _, rel := range tmpls {
		data, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			return err
		}
		out, err := Template(rel, data, cfg)
		if err != nil {
			return fmt.Errorf("rendering %s: %w", rel, err)
		}
		if err := os.WriteFile(filepath.Join(root, OutputPath(rel)), out, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// renderTree renders every template below root, like the two gomplate calls in install-platform.sh.
func renderTree(root string, cfg map[string]any) error {
	tmpls, err := templates(root)
	if err != nil {
		return err
	}
	return renderFiles(root, tmpls, cfg)
}

var appStart = regexp.MustCompile(`^  - name:\s*["']?([A-Za-z0-9._-]+)`)

// ApplyExcludes removes the named entries from `.applications` of a rendered target values file.
// It edits line-wise to keep the rest of the file byte-identical.
func ApplyExcludes(data []byte, exclude []string) []byte {
	if len(exclude) == 0 {
		return data
	}
	excluded := map[string]bool{}
	for _, e := range exclude {
		excluded[e] = true
	}
	var out []string
	inApps, dropping := false, false
	for _, line := range strings.SplitAfter(string(data), "\n") {
		trimmed := strings.TrimRight(line, "\n")
		switch {
		case trimmed == "applications:":
			inApps, dropping = true, false
		case inApps && trimmed != "" && !strings.HasPrefix(trimmed, " "):
			inApps, dropping = false, false
		case inApps:
			if m := appStart.FindStringSubmatch(trimmed); m != nil {
				dropping = excluded[m[1]]
			}
		}
		if !dropping {
			out = append(out, line)
		}
	}
	return []byte(strings.Join(out, ""))
}

// Apps returns `.applications[].name` of a rendered target values file.
func Apps(rendered []byte) ([]string, error) {
	var doc struct {
		Applications []struct {
			Name string `json:"name"`
		} `json:"applications"`
	}
	if err := yaml.Unmarshal(rendered, &doc); err != nil {
		return nil, err
	}
	var names []string
	for _, a := range doc.Applications {
		names = append(names, a.Name)
	}
	return names, nil
}
