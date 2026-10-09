package version

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/suxess-it/kubrix-cli/internal/testrepo"
)

func TestReleasesAndTargets(t *testing.T) {
	tags := []string{"v6.0.0", "v7.1.0", "v5.0.0", "main", "v7.0.0", "v8.0.0", "v8.1.0", "v9.0.0", "v7.0.0-rc1", "v10.0.0"}
	if got := Releases(tags); !slices.Equal(got, []string{"v10.0.0", "v9.0.0", "v8.1.0", "v8.0.0", "v7.1.0", "v7.0.0", "v6.0.0", "v5.0.0"}) {
		t.Fatalf("Releases = %v", got)
	}
	cases := map[string][]string{
		"7.0.0":   {"v8.1.0", "v8.0.0", "v7.1.0"},
		"v7.1.0":  {"v8.1.0", "v8.0.0"},
		"v9.0.0":  {"v10.0.0"},
		"v10.0.0": nil,
		"garbage": nil,
	}
	for installed, want := range cases {
		if got := UpgradeTargets(installed, tags); !slices.Equal(got, want) {
			t.Errorf("UpgradeTargets(%s) = %v, want %v", installed, got, want)
		}
	}
	if !IsRelease("v7.0.0") || IsRelease("main") || IsRelease("feat/v7.0.0") || IsRelease("7.0.0") {
		t.Error("IsRelease misclassifies refs")
	}
}

func TestChangelogBetweenOnRepoChangelog(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(testrepo.KubrixRepo(t), "CHANGELOG.md"))
	if err != nil {
		t.Fatal(err)
	}
	notes := ChangelogBetween(string(data), "5.0.0", "v7.0.0")
	if !strings.HasPrefix(notes, "## [7.0.0]") {
		t.Fatalf("notes must start with the 7.0.0 section, got %q", notes[:min(60, len(notes))])
	}
	if !strings.Contains(notes, "## [6.0.0]") || strings.Contains(notes, "## [5.0.0]") {
		t.Error("expected 7.0.0 and 6.0.0 but not 5.0.0")
	}
	if !HasBreakingChanges(notes) {
		t.Error("7.0.0 has breaking changes")
	}
	if got := ChangelogBetween(string(data), "7.0.0", "7.0.0"); got != "" {
		t.Errorf("same version must give no notes, got %d bytes", len(got))
	}
	if got := ChangelogBetween(string(data), "6.0.0", ""); !strings.HasPrefix(got, "## [7.0.0]") {
		t.Error("open-ended range must include newer sections")
	}
}

func TestBreakingChanges(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(testrepo.KubrixRepo(t), "CHANGELOG.md"))
	if err != nil {
		t.Fatal(err)
	}
	notes := ChangelogBetween(string(data), "6.0.0", "7.0.0")
	breaking := BreakingChanges(notes)
	if !strings.HasPrefix(breaking, "## [7.0.0]") || !strings.Contains(breaking, "BREAKING CHANGES") {
		t.Fatalf("got %q", breaking[:min(80, len(breaking))])
	}
	if strings.Contains(breaking, "### Features") || len(breaking) > len(notes)/3 {
		t.Errorf("only breaking sections expected (%d of %d bytes)", len(breaking), len(notes))
	}
	if BreakingChanges("## [1.0.0]\n\n### Features\n\n* x\n") != "" {
		t.Error("no breaking section must give an empty result")
	}
}

func TestSupported(t *testing.T) {
	if Supported("v6.0.0") || !Supported("v7.0.0") || !Supported("v8.2.1") || Supported("main") {
		t.Error("only releases from " + MinSupported + " on are supported")
	}
}
