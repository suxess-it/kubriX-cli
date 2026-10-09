package platforminstaller

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suxess-it/kubrix-cli/internal/testrepo"
)

func TestBranchTag(t *testing.T) {
	cases := map[string]string{
		"main":                       "main",
		"feat/switch_keycloak_chart": "feat-switch_keycloak_chart",
		"fix/a/b":                    "fix-a-b",
	}
	for in, want := range cases {
		if got := BranchTag(in); got != want {
			t.Errorf("BranchTag(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSetImageTagOnRepoManifest(t *testing.T) {
	manifest, err := os.ReadFile(filepath.Join(testrepo.KubrixRepo(t), "install-manifests.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	out, err := SetImageTag(manifest, "feat-x")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "image: "+ImageRepo+":feat-x") {
		t.Fatal("tag was not replaced")
	}
	if strings.Contains(string(out), ImageRepo+":latest") {
		t.Fatal("latest tag still present")
	}
}

func TestSetImageTagMissingImage(t *testing.T) {
	if _, err := SetImageTag([]byte("kind: Job\n"), "x"); err == nil {
		t.Fatal("expected error when the installer image is not referenced")
	}
}
