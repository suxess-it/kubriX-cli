package cmd

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/pflag"
)

// clearKubrixEnv empties the KUBRIX_* variables of the test process, e.g. KUBRIX_REPO of the test setup.
func clearKubrixEnv(t *testing.T) {
	t.Helper()
	for _, kv := range os.Environ() {
		if name, _, _ := strings.Cut(kv, "="); strings.HasPrefix(name, "KUBRIX_") {
			t.Setenv(name, "")
		}
	}
	for _, name := range []string{"GITHUB_TOKEN", "GH_TOKEN"} {
		t.Setenv(name, "")
	}
}

func loadConfig(t *testing.T, file string, args ...string) (installConfig, error) {
	t.Helper()
	flags := pflag.NewFlagSet("install", pflag.ContinueOnError)
	addInstallFlags(flags)
	if err := flags.Parse(args); err != nil {
		t.Fatal(err)
	}
	path := ""
	if file != "" {
		path = filepath.Join(t.TempDir(), "kubrix.yaml")
		if err := os.WriteFile(path, []byte(file), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return loadInstallConfig(flags, path)
}

func TestInstallConfigPrecedenceIsFlagsThenEnvThenFile(t *testing.T) {
	clearKubrixEnv(t)
	t.Setenv("KUBRIX_TARGET_TYPE", "kind-delivery")
	t.Setenv("KUBRIX_UPSTREAM_BRANCH", "feat/env")
	file := "context: kind-file\nrepo: acme/file\ntarget-type: kind-portal\nupstream-branch: feat/file\ngit-user-name: filer\n"
	c, err := loadConfig(t, file, "--upstream-branch", "feat/flag")
	if err != nil {
		t.Fatal(err)
	}
	if c.UpstreamBranch != "feat/flag" || c.TargetType != "kind-delivery" || c.GitUserName != "filer" || c.Context != "kind-file" || c.Org != "acme" || c.Repo != "file" {
		t.Errorf("config %+v", c)
	}
}

func TestInstallConfigDefaults(t *testing.T) {
	clearKubrixEnv(t)
	c, err := loadConfig(t, "", "--context", "prod", "--repo", "acme/demo")
	if err != nil {
		t.Fatal(err)
	}
	if c.TargetType != defaultTarget || c.UpstreamRepo != defaultUpstream || c.UpstreamBranch != defaultBranch || c.MaxWaitSeconds != 0 || len(c.Extra) != 0 {
		t.Errorf("config %+v", c)
	}
}

func TestInstallConfigReadsCIStyleEnvironment(t *testing.T) {
	clearKubrixEnv(t)
	for name, value := range map[string]string{
		"KUBRIX_CONTEXT":                 "kind-kubrix-cluster",
		"KUBRIX_REPO":                    "https://github.com/kubriX-demo/kubrix-cluster-test-1",
		"KUBRIX_UPSTREAM_REPO":           "https://github.com/suxess-it/kubriX-prime.git",
		"KUBRIX_REPO_PASSWORD":           "repo-tok",
		"KUBRIX_UPSTREAM_REPO_PASSWORD":  "up-tok",
		"KUBRIX_BOOTSTRAP_MAX_WAIT_TIME": "3600",
		"KUBRIX_INSTALLER_TAG":           "pr-12,main",
		"KUBRIX_APP_EXCLUDE":             "kargo backstage",
		"KUBRIX_CLUSTER_TYPE":            "kind",
		"KUBRIX_REPO_BRANCH":             "main",
		"KUBRIX_SYNC_DEBUG":              "true",
		"KUBRIX_EXPERIMENTAL":            "1",
		"GITHUB_TOKEN":                   "ignored-when-KUBRIX_REPO_PASSWORD-is-set",
	} {
		t.Setenv(name, value)
	}
	c, err := loadConfig(t, "")
	if err != nil {
		t.Fatal(err)
	}
	if c.Org != "kubriX-demo" || c.Repo != "kubrix-cluster-test-1" || c.UpstreamRepo != "suxess-it/kubriX-prime" {
		t.Errorf("repos %+v", c)
	}
	if c.RepoToken != "repo-tok" || c.UpstreamToken != "up-tok" || c.MaxWaitSeconds != 3600 || c.ClusterType != "kind" {
		t.Errorf("config %+v", c)
	}
	if !slices.Equal(c.InstallerTags, []string{"pr-12", "main"}) || !slices.Equal(c.ExcludedApps, []string{"kargo", "backstage"}) {
		t.Errorf("lists %v %v", c.InstallerTags, c.ExcludedApps)
	}
	if want := map[string]string{"KUBRIX_REPO_BRANCH": "main", "KUBRIX_SYNC_DEBUG": "true"}; !maps.Equal(c.Extra, want) {
		t.Errorf("only the variables the CLI does not read are passed on: %v", c.Extra)
	}
}

func TestInstallConfigTokenFallsBackToGitHubTokens(t *testing.T) {
	clearKubrixEnv(t)
	t.Setenv("GH_TOKEN", "gh-tok")
	c, err := loadConfig(t, "", "--context", "prod", "--repo", "acme/demo")
	if err != nil || c.RepoToken != "gh-tok" {
		t.Fatalf("token %q %v", c.RepoToken, err)
	}
	t.Setenv("GITHUB_TOKEN", "github-tok")
	if c, _ := loadConfig(t, "", "--context", "prod", "--repo", "acme/demo"); c.RepoToken != "github-tok" {
		t.Errorf("GITHUB_TOKEN comes before GH_TOKEN: %q", c.RepoToken)
	}
}

func TestInstallConfigFileListsAndExtraEnv(t *testing.T) {
	clearKubrixEnv(t)
	t.Setenv("KUBRIX_SYNC_DEBUG", "false")
	file := `context: prod
repo: acme/demo
app-exclude: [kargo, backstage]
installer-tag: [pr-1]
extra-env:
  KUBRIX_SYNC_DEBUG: true
  kubrix_install_debug: true
  KUBRIX_REPO_USERNAME: dummy
`
	c, err := loadConfig(t, file)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(c.ExcludedApps, []string{"kargo", "backstage"}) || !slices.Equal(c.InstallerTags, []string{"pr-1"}) {
		t.Errorf("lists %v %v", c.ExcludedApps, c.InstallerTags)
	}
	want := map[string]string{"KUBRIX_SYNC_DEBUG": "false", "KUBRIX_INSTALL_DEBUG": "true", "KUBRIX_REPO_USERNAME": "dummy"}
	if !maps.Equal(c.Extra, want) {
		t.Errorf("extra %v, want %v (environment wins, booleans stay true/false, names are upper case)", c.Extra, want)
	}
}

func TestInstallConfigRejectsMistakes(t *testing.T) {
	for name, tc := range map[string]struct {
		env  map[string]string
		file string
		args []string
		want string
	}{
		"missing context":      {args: []string{"--repo", "acme/demo"}, want: "context is required"},
		"missing repo":         {args: []string{"--context", "prod"}, want: "repo is required"},
		"repo not on GitHub":   {args: []string{"--context", "prod", "--repo", "https://gitlab.com/acme/demo"}, want: "not a github.com repository"},
		"repo not a slug":      {args: []string{"--context", "prod", "--repo", "demo"}, want: "not owner/repo"},
		"bad wait time":        {args: []string{"--context", "prod", "--repo", "acme/demo", "--bootstrap-max-wait-time", "1h"}, want: "bootstrap-max-wait-time"},
		"secret in the file":   {file: "context: prod\nrepo: acme/demo\nrepo-password: x\n", want: `unknown key "repo-password"`},
		"unknown file key":     {file: "context: prod\nrepo: acme/demo\ndomian: x\n", want: `unknown key "domian"`},
		"setting as extra-env": {file: "context: prod\nrepo: acme/demo\nextra-env:\n  KUBRIX_DOMAIN: x\n", want: "KUBRIX_DOMAIN is a setting"},
		"non-KUBRIX extra-env": {file: "context: prod\nrepo: acme/demo\nextra-env:\n  HOME: x\n", want: "not a KUBRIX_ variable"},
		"nested extra-env":     {file: "context: prod\nrepo: acme/demo\nextra-env:\n  KUBRIX_X: [a]\n", want: "expected a single value"},
		"decided by the CLI":   {env: map[string]string{"KUBRIX_BOOTSTRAP": "true"}, args: []string{"--context", "prod", "--repo", "acme/demo"}, want: "KUBRIX_BOOTSTRAP is decided by kubrix install"},
	} {
		t.Run(name, func(t *testing.T) {
			clearKubrixEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			_, err := loadConfig(t, tc.file, tc.args...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("got %v, want %q", err, tc.want)
			}
		})
	}
}
