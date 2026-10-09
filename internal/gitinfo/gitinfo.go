package gitinfo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/suxess-it/kubrix-cli/internal/gitops"
)

const UpstreamOwner = "suxess-it"

type Remote struct {
	Owner string
	Repo  string
}

func (r Remote) Slug() string { return r.Owner + "/" + r.Repo }
func (r Remote) URL() string  { return "https://github.com/" + r.Slug() }

var remotePattern = regexp.MustCompile(`^(?:https://github\.com/|git@github\.com:|ssh://git@github\.com/)([^/]+)/([^/]+?)(?:\.git)?/?$`)

func ParseRemote(url string) (Remote, error) {
	m := remotePattern.FindStringSubmatch(strings.TrimSpace(url))
	if m == nil {
		return Remote{}, fmt.Errorf("origin %q is not a github.com remote", url)
	}
	return Remote{Owner: m[1], Repo: m[2]}, nil
}

type Info struct {
	Root   string
	Origin Remote
	Branch string
}

func (i *Info) IsFork() bool { return !strings.EqualFold(i.Origin.Owner, UpstreamOwner) }

// Detect returns nil, nil when dir is not inside a kubriX checkout.
func Detect(dir string) (*Info, error) {
	root, err := git(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, nil
	}
	for _, marker := range []string{"install-platform.sh", "install-manifests.yaml"} {
		if _, err := os.Stat(filepath.Join(root, marker)); err != nil {
			return nil, nil
		}
	}
	originURL, err := git(root, "remote", "get-url", "origin")
	if err != nil {
		return nil, errors.New("kubriX checkout has no 'origin' remote")
	}
	origin, err := ParseRemote(originURL)
	if err != nil {
		return nil, err
	}
	branch, err := git(root, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return nil, err
	}
	if branch == "HEAD" {
		return nil, errors.New("kubriX checkout is in detached HEAD state; check out a branch first")
	}
	return &Info{Root: root, Origin: origin, Branch: branch}, nil
}

type PushStatus struct {
	RemoteMissing bool
	Diverged      bool
	Dirty         bool
}

// Classify takes `git rev-parse HEAD`, the sha from `git ls-remote` (empty if the
// branch is not on the remote) and `git status --porcelain` output.
func Classify(localHead, remoteHead, porcelain string) PushStatus {
	remoteHead = strings.TrimSpace(remoteHead)
	return PushStatus{
		RemoteMissing: remoteHead == "",
		Diverged:      remoteHead != "" && remoteHead != strings.TrimSpace(localHead),
		Dirty:         strings.TrimSpace(porcelain) != "",
	}
}

func (s PushStatus) Warnings(branch string) []string {
	var w []string
	if s.RemoteMissing {
		w = append(w, fmt.Sprintf("branch %q does not exist on origin; the installer only sees pushed branches", branch))
	}
	if s.Diverged {
		w = append(w, fmt.Sprintf("local HEAD differs from origin/%s; the installer uses what is pushed", branch))
	}
	if s.Dirty {
		w = append(w, "working tree has uncommitted changes; they will not be part of the install")
	}
	return w
}

func (i *Info) PushStatus() (PushStatus, error) {
	local, err := git(i.Root, "rev-parse", "HEAD")
	if err != nil {
		return PushStatus{}, err
	}
	lsRemote, err := git(i.Root, "ls-remote", "origin", "refs/heads/"+i.Branch)
	if err != nil {
		return PushStatus{}, fmt.Errorf("git ls-remote origin: %w", err)
	}
	remote, _, _ := strings.Cut(lsRemote, "\t")
	porcelain, err := git(i.Root, "status", "--porcelain")
	if err != nil {
		return PushStatus{}, err
	}
	return Classify(local, remote, porcelain), nil
}

// git reads from the checkout in dir; reading needs neither a token nor an identity.
func git(dir string, args ...string) (string, error) {
	return gitops.New(dir, "", "", "").Run(context.Background(), args...)
}
