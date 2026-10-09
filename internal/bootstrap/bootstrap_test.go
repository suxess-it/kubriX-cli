package bootstrap

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/suxess-it/kubrix-cli/internal/platformrepo"
	"github.com/suxess-it/kubrix-cli/internal/testrepo"
)

func TestFromReleasePushesRenderedRepo(t *testing.T) {
	upstream, customer := testrepo.Upstream(t), testrepo.Bare(t)
	err := FromRelease(context.Background(), Options{
		UpstreamURL: upstream,
		Tag:         "v7.0.0",
		CustomerURL: customer,
		Config: platformrepo.ConfigInput{
			ClusterType: "k8s",
			DNSProvider: "cloudflare",
			Domain:      "example.com",
			Repo:        "https://github.com/acme/demo.git",
			GitUser:     "octocat",
		},
		TargetType: testrepo.TargetType,
		Exclude:    []string{"kargo"},
	})
	if err != nil {
		t.Fatal(err)
	}

	dir := testrepo.Clone(t, customer)
	g := testrepo.Git(t, dir)
	if msg := testrepo.Run(t, g, "log", "-1", "--format=%s|%an"); msg != CommitMessage+"|kubrix-installer[kubrix-bot]" {
		t.Errorf("last commit %q", msg)
	}
	if !strings.Contains(testrepo.Run(t, g, "log", "--format=%s"), "release v7.0.0") {
		t.Error("upstream history must be kept so later upgrades can merge")
	}
	if strings.Contains(testrepo.Run(t, g, "log", "--format=%s"), "release v7.1.0") {
		t.Error("must be bootstrapped from the tag, not from main")
	}

	config := testrepo.Read(t, dir, platformrepo.ConfigPath)
	for _, want := range []string{"clusterType: k8s", "domain: example.com", "gitRepoOrg: acme", "gitRepoName: demo", "dnsProvider: cloudflare"} {
		if !strings.Contains(config, want) {
			t.Errorf("customer-config.yaml is missing %q:\n%s", want, config)
		}
	}
	if got := testrepo.Read(t, dir, "platform-apps/charts/grafana/values-customer-generated.yaml"); got != "domain: grafana.example.com\n" {
		t.Errorf("rendered grafana values %q", got)
	}
	apps, err := platformrepo.Apps([]byte(testrepo.Read(t, dir, platformrepo.TargetValuesPath(testrepo.TargetType))))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(apps, []string{"traefik", "external-dns", "backstage"}) {
		t.Errorf("apps %v: want k8s apps without the excluded kargo", apps)
	}
}
