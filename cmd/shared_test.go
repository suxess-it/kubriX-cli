package cmd

import (
	"slices"
	"testing"

	"github.com/suxess-it/kubrix-cli/internal/gitinfo"
)

func TestVersionChoiceDefaults(t *testing.T) {
	releases := []string{"v7.1.0", "v7.0.0"}
	contributor := &gitinfo.Info{Branch: "feat/x"}
	cases := []struct {
		name         string
		contributor  *gitinfo.Info
		current      string
		preferBranch bool
		releases     []string
		want         string
	}{
		{"demo contributor keeps testing the checkout", contributor, "v7.0.0", true, releases, "feat/x"},
		{"demo user gets the latest release", nil, "", true, releases, "v7.1.0"},
		{"saved choice is kept", nil, "v7.0.0", true, releases, "v7.0.0"},
		{"saved unknown branch goes to other", nil, "fix/y", true, releases, "fix/y"},
		{"no releases falls back to main", nil, "", true, nil, "main"},
		{"cluster install prefers the release even for contributors", contributor, "", false, releases, "v7.1.0"},
	}
	for _, c := range cases {
		vc := newVersionChoice(c.releases, c.contributor, c.current, c.preferBranch)
		if got := vc.ref(); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
	vc := newVersionChoice(releases, contributor, "", true)
	var labels []string
	for _, o := range vc.options {
		labels = append(labels, o.Label)
	}
	if !slices.Equal(labels, []string{"v7.1.0 (latest release)", "v7.0.0", "main (unreleased)", "feat/x (checked out)", "Other branch…"}) {
		t.Errorf("options %v", labels)
	}
}

func TestValidDomain(t *testing.T) {
	for _, ok := range []string{"example.com", "platform.example.co.uk", "a-b.example.io"} {
		if err := validDomain(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "localhost", "Example.com", "demo-abc.kubrix.cloud", "kubrix.cloud", "-x.example.com"} {
		if validDomain(bad) == nil {
			t.Errorf("%q must be rejected", bad)
		}
	}
}
