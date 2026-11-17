package platformrepo

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
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

var (
	appStart    = regexp.MustCompile(`^  - name:\s*["']?([A-Za-z0-9._-]+)`)
	appKey      = regexp.MustCompile(`^  ["']?([A-Za-z0-9._-]+)["']?:\s*(#.*)?$`)
	enabledLine = regexp.MustCompile(`^    enabled:`)
)

// ApplyExcludes leaves the named apps out of `.applications` of a rendered target values file: list entries are
// removed, map entries get `enabled: false` (like install-platform.sh), so apps enabled by the defaults are disabled too.
// It edits line-wise to keep the rest of the file byte-identical.
func ApplyExcludes(data []byte, exclude []string) []byte {
	if len(exclude) == 0 {
		return data
	}
	excluded := map[string]bool{}
	for _, e := range exclude {
		excluded[e] = true
	}
	if _, m, err := parseApps(data); err == nil && m != nil {
		return disableApps(data, exclude, excluded)
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

// disableApps sets `enabled: false` on the excluded apps of a map-format `.applications`. Apps without an entry
// (enabled by the defaults file) get one appended at the end of the block.
func disableApps(data []byte, exclude []string, excluded map[string]bool) []byte {
	var out []string
	seen := map[string]bool{}
	appendMissing := func() {
		if n := len(out); n > 0 && out[n-1] != "" && !strings.HasSuffix(out[n-1], "\n") {
			out[n-1] += "\n"
		}
		for _, app := range exclude {
			if !seen[app] {
				seen[app] = true
				out = append(out, "  "+app+":\n", "    enabled: false\n")
			}
		}
	}
	inApps, disabling := false, false
	for _, line := range strings.SplitAfter(string(data), "\n") {
		trimmed := strings.TrimRight(line, "\n")
		switch {
		case trimmed == "applications:":
			inApps, disabling = true, false
		case inApps && trimmed != "" && !strings.HasPrefix(trimmed, " "):
			appendMissing()
			inApps, disabling = false, false
		case inApps:
			if m := appKey.FindStringSubmatch(trimmed); m != nil {
				disabling = excluded[m[1]]
				if disabling {
					seen[m[1]] = true
					out = append(out, line, "    enabled: false\n")
					continue
				}
			} else if disabling && enabledLine.MatchString(trimmed) {
				continue
			}
		}
		out = append(out, line)
	}
	if inApps {
		appendMissing()
	}
	return []byte(strings.Join(out, ""))
}

// appMap is `.applications` in the map format: application name to its `enabled` flag (nil when unset).
type appMap map[string]*bool

// parseApps reads `.applications` of a values file. It returns the names of a list, or the entries of a map.
func parseApps(data []byte) (list []string, m appMap, err error) {
	var doc struct {
		Applications json.RawMessage `json:"applications"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, nil, err
	}
	raw := bytes.TrimSpace(doc.Applications)
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil, nil
	}
	if raw[0] == '[' {
		var apps []struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(raw, &apps); err != nil {
			return nil, nil, err
		}
		for _, a := range apps {
			list = append(list, a.Name)
		}
		return list, nil, nil
	}
	var entries map[string]*struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, nil, err
	}
	m = appMap{}
	for name, e := range entries {
		m[name] = nil
		if e != nil {
			m[name] = e.Enabled
		}
	}
	return nil, m, nil
}

// enabled returns the names the target chart installs from a map: those with `enabled: true`, sorted like Helm's range.
func (m appMap) enabled() []string {
	var names []string
	for name, on := range m {
		if on != nil && *on {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

// Apps returns the applications of a rendered target values file: `.applications[].name` of a list, or the
// enabled entries of a map. A map profile only holds overrides; use AppsLayered to include the defaults.
func Apps(rendered []byte) ([]string, error) {
	return AppsLayered(nil, rendered)
}

// AppsLayered returns the applications installed by a target values file layered over the defaults file
// (values-kubrix-default.yaml), as the bootstrap Application passes them to the target chart.
// A list-format profile replaces the defaults' applications, so they are ignored.
func AppsLayered(defaults, profile []byte) ([]string, error) {
	list, m, err := parseApps(profile)
	if err != nil || m == nil {
		return list, err
	}
	_, merged, err := parseApps(defaults)
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", DefaultValuesPath, err)
	}
	if merged == nil {
		merged = appMap{}
	}
	for name, on := range m {
		if on != nil || merged[name] == nil {
			merged[name] = on
		}
	}
	return merged.enabled(), nil
}
