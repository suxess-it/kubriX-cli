package gitinfo

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/suxess-it/kubrix-cli/internal/gitops"
	"github.com/suxess-it/kubrix-cli/internal/testrepo"
)

const githubOrigin = "https://github.com/acme/kubriX.git"

// checkout creates a kubriX checkout on main whose origin is originURL; the files are committed but not pushed.
func checkout(t *testing.T, originURL string) string {
	t.Helper()
	dir := t.TempDir()
	g := testrepo.Git(t, dir)
	testrepo.Run(t, g, "init", "-q", "-b", "main")
	testrepo.Write(t, dir, map[string]string{"install-platform.sh": "#!/bin/sh\n", "install-manifests.yaml": "kind: Job\n"})
	testrepo.Run(t, g, "add", "-A")
	testrepo.Run(t, g, "commit", "-q", "-m", "initial")
	testrepo.Run(t, g, "remote", "add", "origin", originURL)
	return dir
}

// pushedCheckout is a checkout whose origin is a local bare repository that holds the pushed main. The origin
// is no github.com URL, so it is described by hand instead of through Detect.
func pushedCheckout(t *testing.T) (*Info, *testrepoGit) {
	t.Helper()
	remote := testrepo.Bare(t)
	dir := checkout(t, remote)
	g := testrepo.Git(t, dir)
	testrepo.Run(t, g, "push", "-q", "origin", "main")
	return &Info{Root: dir, Origin: Remote{Owner: "acme", Repo: "kubriX"}, Branch: "main"}, &testrepoGit{g: g, dir: dir, remote: remote}
}

type testrepoGit struct {
	g      *gitops.Git
	dir    string
	remote string
}

func real(t *testing.T, path string) string {
	t.Helper()
	p, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDetectFindsTheCheckout(t *testing.T) {
	dir := checkout(t, githubOrigin)
	info, err := Detect(dir)
	if err != nil || info == nil {
		t.Fatalf("%+v %v", info, err)
	}
	if info.Root != real(t, dir) || info.Origin.Slug() != "acme/kubriX" || info.Branch != "main" {
		t.Errorf("%+v", info)
	}
	if !info.IsFork() {
		t.Error("acme is not the upstream owner")
	}

	sub := filepath.Join(dir, "platform-apps", "charts")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if again, err := Detect(sub); err != nil || again == nil || again.Root != info.Root {
		t.Errorf("a subdirectory belongs to the same checkout: %+v %v", again, err)
	}
}

func TestDetectIgnoresWhatIsNoKubrixCheckout(t *testing.T) {
	if info, err := Detect(t.TempDir()); info != nil || err != nil {
		t.Errorf("outside any repository: %+v %v", info, err)
	}
	if info, err := Detect(filepath.Join(t.TempDir(), "missing")); info != nil || err != nil {
		t.Errorf("a directory that does not exist: %+v %v", info, err)
	}
	other := t.TempDir()
	g := testrepo.Git(t, other)
	testrepo.Run(t, g, "init", "-q", "-b", "main")
	if info, err := Detect(other); info != nil || err != nil {
		t.Errorf("a repository without kubriX files: %+v %v", info, err)
	}
	dir := checkout(t, githubOrigin)
	if err := os.Remove(filepath.Join(dir, "install-manifests.yaml")); err != nil {
		t.Fatal(err)
	}
	if info, err := Detect(dir); info != nil || err != nil {
		t.Errorf("both marker files are needed: %+v %v", info, err)
	}
}

func TestDetectRejectsUnusableCheckouts(t *testing.T) {
	dir := checkout(t, githubOrigin)
	g := testrepo.Git(t, dir)
	testrepo.Run(t, g, "checkout", "-q", "--detach")
	if _, err := Detect(dir); err == nil || !strings.Contains(err.Error(), "detached HEAD") {
		t.Errorf("detached HEAD: %v", err)
	}

	noOrigin := t.TempDir()
	g = testrepo.Git(t, noOrigin)
	testrepo.Run(t, g, "init", "-q", "-b", "main")
	testrepo.Write(t, noOrigin, map[string]string{"install-platform.sh": "x", "install-manifests.yaml": "x"})
	if _, err := Detect(noOrigin); err == nil || !strings.Contains(err.Error(), "no 'origin' remote") {
		t.Errorf("no origin: %v", err)
	}

	elsewhere := checkout(t, "https://gitlab.com/acme/kubriX.git")
	if _, err := Detect(elsewhere); err == nil || !strings.Contains(err.Error(), "not a github.com remote") {
		t.Errorf("origin elsewhere: %v", err)
	}
}

func TestPushStatusOfACheckoutThatIsInSync(t *testing.T) {
	info, _ := pushedCheckout(t)
	status, err := info.PushStatus()
	if err != nil || status != (PushStatus{}) || len(status.Warnings("main")) != 0 {
		t.Errorf("%+v %v", status, err)
	}
}

func TestPushStatusWarnsAboutWhatTheInstallerWillNotSee(t *testing.T) {
	info, c := pushedCheckout(t)

	testrepo.Write(t, c.dir, map[string]string{"notes.txt": "uncommitted\n"})
	if status, err := info.PushStatus(); err != nil || !status.Dirty || status.Diverged || status.RemoteMissing {
		t.Errorf("dirty: %+v %v", status, err)
	}

	testrepo.Run(t, c.g, "add", "-A")
	testrepo.Run(t, c.g, "commit", "-q", "-m", "local only")
	if status, err := info.PushStatus(); err != nil || status.Dirty || !status.Diverged {
		t.Errorf("a local commit that was not pushed: %+v %v", status, err)
	}

	testrepo.Run(t, c.g, "checkout", "-q", "-b", "feat/new")
	branch := &Info{Root: c.dir, Origin: info.Origin, Branch: "feat/new"}
	status, err := branch.PushStatus()
	if err != nil || !status.RemoteMissing {
		t.Fatalf("a branch that was never pushed: %+v %v", status, err)
	}
	if w := status.Warnings("feat/new"); !slices.ContainsFunc(w, func(s string) bool { return strings.Contains(s, `"feat/new" does not exist on origin`) }) {
		t.Errorf("warnings %v", w)
	}
}

func TestPushStatusFailsWhenOriginCannotBeReached(t *testing.T) {
	info, c := pushedCheckout(t)
	if err := os.RemoveAll(c.remote); err != nil {
		t.Fatal(err)
	}
	if _, err := info.PushStatus(); err == nil || !strings.Contains(err.Error(), "ls-remote") {
		t.Errorf("got %v", err)
	}
}
