package gittest

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/suxess-it/kubrix-cli/internal/git"
)

func TestHostTracksRepositories(t *testing.T) {
	ctx := context.Background()
	h := New("octocat", "acme", "beta")
	if st, _ := h.RepoState(ctx, "acme", "demo"); st != git.RepoMissing {
		t.Fatalf("unknown repository: %v", st)
	}
	if err := h.CreatePrivateRepo(ctx, "acme", "demo"); err != nil {
		t.Fatal(err)
	}
	if st, _ := h.RepoState(ctx, "acme", "demo"); st != git.RepoEmpty {
		t.Errorf("created repository: %v", st)
	}
	if err := h.CreatePrivateRepo(ctx, "acme", "demo"); err == nil {
		t.Error("creating twice must fail like the real host")
	}
	if !slices.Equal(h.Created, []string{"acme/demo"}) {
		t.Errorf("created %v", h.Created)
	}
	if err := h.DeleteRepo(ctx, "acme", "demo"); err != nil {
		t.Fatal(err)
	}
	if st, _ := h.RepoState(ctx, "acme", "demo"); st != git.RepoMissing || !slices.Equal(h.Deleted, []string{"acme/demo"}) {
		t.Errorf("deleted repository: %v %v", st, h.Deleted)
	}
}

func TestHostIdentityAndOrgs(t *testing.T) {
	ctx := context.Background()
	h := New("octocat", "acme", "beta")
	if login, err := h.Validate(ctx); err != nil || login != "octocat" {
		t.Errorf("login %q %v", login, err)
	}
	orgs, _ := h.Orgs(ctx)
	orgs[0] = "changed"
	if again, _ := h.Orgs(ctx); !slices.Equal(again, []string{"acme", "beta"}) {
		t.Errorf("callers must not alter the host's orgs: %v", again)
	}
	if h.Token() == "" {
		t.Error("a token is needed to push")
	}
}

func TestHostCloneURL(t *testing.T) {
	h := New("octocat", "acme")
	if got := h.CloneURL("acme", "demo"); !strings.HasPrefix(got, "https://") {
		t.Errorf("unmapped repository: %q", got)
	}
	h.URLs["acme/demo"] = "/tmp/bare"
	if got := h.CloneURL("acme", "demo"); got != "/tmp/bare" {
		t.Errorf("mapped repository: %q", got)
	}
}

func TestHostRecordsPullRequests(t *testing.T) {
	h := New("octocat", "acme")
	url, err := h.OpenPullRequest(context.Background(), "acme", "demo", "head", "main", "title", "body")
	if err != nil || !strings.HasSuffix(url, "/acme/demo/pull/1") {
		t.Fatalf("%q %v", url, err)
	}
	if len(h.PRs) != 1 || h.PRs[0] != (PullRequest{"acme", "demo", "head", "main", "title", "body"}) {
		t.Errorf("PRs %+v", h.PRs)
	}
}
