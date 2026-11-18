package cmd

import (
	"cmp"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"

	"github.com/suxess-it/kubrix-cli/internal/install"
	"github.com/suxess-it/kubrix-cli/internal/platforminstaller"
)

// envPrefix makes every config key an environment variable too: upstream-branch is KUBRIX_UPSTREAM_BRANCH.
const envPrefix = "KUBRIX"

// installKeys are the settings of a non-interactive install, as flags, KUBRIX_* variables and config file keys.
var installKeys = []struct{ name, usage string }{
	{"context", "kubeconfig context to install into; kind-<name> contexts reuse that kind cluster (required)"},
	{"repo", "platform repository, org/repo or its GitHub URL (required)"},
	{"git-user-name", "git user name for the platform's commits (default: the token's GitHub login)"},
	{"target-type", "kubriX target type (default " + defaultTarget + ")"},
	{"upstream-repo", "kubriX source repository, owner/repo or its GitHub URL (default " + defaultUpstream + ")"},
	{"upstream-branch", "kubriX version: a release tag or a branch (default " + defaultBranch + ")"},
	{"domain", "platform domain; required on existing clusters, " + kindDomain + " on kind"},
	{"dns-provider", "cloudflare, ionos, stackit, aws, azure or none; required on existing clusters, none on kind"},
	{"dns-project-id", "STACKIT project ID"},
	{"dns-credentials-file", "credentials file for aws or azure"},
	{"cloud-provider", "on-prem or aks on existing clusters (default " + defaultProvider + ")"},
	{"app-exclude", "applications to leave out of a fresh repository, comma or space separated"},
	{"installer-tag", "installer image tags to try in order, comma separated; the first that exists is used, else latest"},
	{"bootstrap-max-wait-time", "seconds the installer waits for the platform to become healthy"},
}

// extraEnvKey is the config file map of further installer variables.
const extraEnvKey = "extra-env"

// Secrets are only read from the environment, never from flags or the config file.
const (
	repoTokenEnv     = "KUBRIX_REPO_PASSWORD"
	upstreamTokenEnv = "KUBRIX_UPSTREAM_REPO_PASSWORD"
	dnsTokenEnv      = "KUBRIX_DNS_TOKEN"
	clusterTypeEnv   = "KUBRIX_CLUSTER_TYPE"
)

var extraEnvNameRe = regexp.MustCompile(`^KUBRIX_[A-Z0-9_]+$`)

// installConfig is a non-interactive install: everything the interactive installer would ask.
type installConfig struct {
	Context        string
	Org, Repo      string
	GitUserName    string
	TargetType     string
	UpstreamRepo   string
	UpstreamBranch string
	Domain         string
	DNSProvider    string
	DNS            install.DNSCredentials
	CloudProvider  string
	ExcludedApps   []string
	InstallerTags  []string
	MaxWaitSeconds int
	// RepoToken is the GitHub token; empty means the gh CLI's.
	RepoToken     string
	UpstreamToken string
	// ClusterType is what KUBRIX_CLUSTER_TYPE claims, checked against the context.
	ClusterType string
	// Extra are the further installer variables: the config file's extra-env and unknown KUBRIX_* variables.
	Extra map[string]string
}

// addInstallFlags defines one flag per installKeys entry.
func addInstallFlags(flags *pflag.FlagSet) {
	for _, k := range installKeys {
		flags.String(k.name, "", k.usage)
	}
}

// installFlagNames are the names of the installKeys.
func installFlagNames() []string {
	var names []string
	for _, k := range installKeys {
		names = append(names, k.name)
	}
	return names
}

// envName is the environment variable of a config key.
func envName(key string) string {
	return envPrefix + "_" + strings.ToUpper(strings.ReplaceAll(key, "-", "_"))
}

// cliEnv are the KUBRIX_* variables the CLI reads itself; every other one is passed on to the installer.
func cliEnv() []string {
	names := []string{experimentalEnv, repoTokenEnv, upstreamTokenEnv, dnsTokenEnv, clusterTypeEnv}
	for _, name := range installFlagNames() {
		names = append(names, envName(name))
	}
	return names
}

// loadInstallConfig reads the settings: flags over KUBRIX_* variables over the config file.
func loadInstallConfig(flags *pflag.FlagSet, configFile string) (installConfig, error) {
	v := viper.New()
	v.SetEnvPrefix(envPrefix)
	v.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))
	v.AutomaticEnv()
	for _, name := range installFlagNames() {
		if err := v.BindPFlag(name, flags.Lookup(name)); err != nil {
			return installConfig{}, err
		}
	}
	if configFile != "" {
		v.SetConfigFile(configFile)
		if err := v.ReadInConfig(); err != nil {
			return installConfig{}, fmt.Errorf("reading %s: %w", configFile, err)
		}
		for key := range v.AllSettings() {
			if key != extraEnvKey && !slices.Contains(installFlagNames(), key) {
				return installConfig{}, fmt.Errorf("%s: unknown key %q (secrets are only read from environment variables)", configFile, key)
			}
		}
	}

	for _, key := range []string{"context", "repo"} {
		if v.GetString(key) == "" {
			return installConfig{}, fmt.Errorf("%s is required: set --%s or %s", key, key, envName(key))
		}
	}
	var c installConfig
	var err error
	c.Context = v.GetString("context")
	if c.Org, c.Repo, err = githubSlug(v.GetString("repo")); err != nil {
		return c, fmt.Errorf("repo: %w", err)
	}
	if c.UpstreamRepo, err = githubRepo(cmp.Or(v.GetString("upstream-repo"), defaultUpstream)); err != nil {
		return c, fmt.Errorf("upstream-repo: %w", err)
	}
	c.GitUserName = v.GetString("git-user-name")
	c.TargetType = cmp.Or(v.GetString("target-type"), defaultTarget)
	c.UpstreamBranch = cmp.Or(v.GetString("upstream-branch"), defaultBranch)
	c.Domain = v.GetString("domain")
	c.DNSProvider = v.GetString("dns-provider")
	c.DNS = install.DNSCredentials{Token: os.Getenv(dnsTokenEnv), ProjectID: v.GetString("dns-project-id"), File: expandHome(v.GetString("dns-credentials-file"))}
	c.CloudProvider = v.GetString("cloud-provider")
	if c.ExcludedApps, err = listSetting(v, "app-exclude"); err != nil {
		return c, err
	}
	if c.InstallerTags, err = listSetting(v, "installer-tag"); err != nil {
		return c, err
	}
	if wait := v.GetString("bootstrap-max-wait-time"); wait != "" {
		if c.MaxWaitSeconds, err = strconv.Atoi(wait); err != nil || c.MaxWaitSeconds <= 0 {
			return c, fmt.Errorf("bootstrap-max-wait-time: %q is not a positive number of seconds", wait)
		}
	}
	c.RepoToken = cmp.Or(os.Getenv(repoTokenEnv), os.Getenv("GITHUB_TOKEN"), os.Getenv("GH_TOKEN"))
	c.UpstreamToken = os.Getenv(upstreamTokenEnv)
	c.ClusterType = os.Getenv(clusterTypeEnv)
	if c.Extra, err = extraEnv(v.Get(extraEnvKey)); err != nil {
		return c, err
	}
	return c, nil
}

// listSetting reads a list given as a comma or space separated string, or as a list in the config file.
func listSetting(v *viper.Viper, key string) ([]string, error) {
	var items []string
	switch raw := v.Get(key).(type) {
	case nil:
	case string:
		items = strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' })
	case []any:
		for _, item := range raw {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("%s: %v is not a string", key, item)
			}
			items = append(items, s)
		}
	default:
		return nil, fmt.Errorf("%s: expected a list, got %v", key, raw)
	}
	return items, nil
}

// extraEnv merges the config file's extra-env with the KUBRIX_* variables the CLI does not read itself, which win.
func extraEnv(fromFile any) (map[string]string, error) {
	extra := map[string]string{}
	if fromFile != nil {
		entries, ok := fromFile.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s: expected a map of variables", extraEnvKey)
		}
		for key, value := range entries {
			// The config file's keys arrive lower-cased; installer variables are upper case.
			name := strings.ToUpper(key)
			switch value.(type) {
			case string, bool, int, float64:
			default:
				return nil, fmt.Errorf("%s.%s: expected a single value", extraEnvKey, name)
			}
			extra[name] = fmt.Sprint(value)
		}
	}
	for _, kv := range os.Environ() {
		name, value, _ := strings.Cut(kv, "=")
		// Like the settings, an empty variable counts as unset.
		if strings.HasPrefix(name, envPrefix+"_") && value != "" && !slices.Contains(cliEnv(), name) {
			extra[name] = value
		}
	}
	for name := range extra {
		switch {
		case !extraEnvNameRe.MatchString(name):
			return nil, fmt.Errorf("%s: %s is not a KUBRIX_ variable", extraEnvKey, name)
		case slices.Contains(cliEnv(), name):
			return nil, fmt.Errorf("%s: %s is a setting of kubrix install; set it as one", extraEnvKey, name)
		case platforminstaller.IsSpecKey(name):
			return nil, fmt.Errorf("%s is decided by kubrix install; remove it", name)
		}
	}
	return extra, nil
}

// githubSlug splits an org/repo slug or a GitHub repository URL.
func githubSlug(repo string) (string, string, error) {
	slug, err := githubRepo(repo)
	if err != nil {
		return "", "", err
	}
	org, name, _ := strings.Cut(slug, "/")
	return org, name, nil
}

// githubRepo turns an owner/repo slug or a GitHub repository URL into the slug.
func githubRepo(repo string) (string, error) {
	slug := repo
	if strings.Contains(repo, "://") {
		u, err := url.Parse(repo)
		if err != nil || u.Host != "github.com" {
			return "", fmt.Errorf("%q is not a github.com repository", repo)
		}
		slug = strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git")
	}
	if !repoSlugRe.MatchString(slug) {
		return "", fmt.Errorf("%q is not owner/repo", repo)
	}
	return slug, nil
}
