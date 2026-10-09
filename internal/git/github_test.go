package git

import (
	"slices"
	"testing"
)

func TestMissingScopes(t *testing.T) {
	cases := []struct {
		header  string
		present bool
		want    []string
	}{
		{"", false, nil},
		{"repo, read:org, workflow", true, nil},
		{"repo, admin:org, gist, workflow", true, nil},
		{"read:org, workflow", true, []string{"repo"}},
		{"repo, workflow", true, []string{"read:org"}},
		{"repo, read:org, gist", true, []string{"workflow"}},
		{"", true, []string{"repo", "read:org", "workflow"}},
	}
	for _, c := range cases {
		if got := MissingScopes(c.header, c.present); !slices.Equal(got, c.want) {
			t.Errorf("MissingScopes(%q, %v) = %v, want %v", c.header, c.present, got, c.want)
		}
	}
}
