package git

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"slices"
	"strings"

	gh "github.com/google/go-github/v92/github"

	"github.com/suxess-it/kubrix-cli/internal/version"
)

// GitHubPATHelp explains the token a user needs when the gh CLI is unavailable.
const GitHubPATHelp = `No authenticated gh CLI found, so a GitHub personal access token is needed.

Classic PAT (recommended), scopes:
  repo         create the demo repo and let the installer push to it
  read:org     list your organizations
  workflow     the bootstrap pushes kubriX's .github/workflows files
  delete_repo  optional, only for 'kubrix demo delete' to remove the repo

Fine-grained PAT, resource owner = your organization, permissions:
  Administration: Read and write  (create/delete repositories)
  Contents:       Read and write  (installer pushes to the repo)
  Workflows:      Read and write  (the push includes .github/workflows files)
  Fine-grained tokens can only reach a repo created after the token if
  "All repositories" is selected for that organization.

Create one at https://github.com/settings/tokens`

// GitHubTokenFromCLI returns the token of an authenticated gh CLI, or an error if gh is missing or logged out.
func GitHubTokenFromCLI() (string, error) {
	if _, err := exec.LookPath("gh"); err != nil {
		return "", errors.New("gh CLI not installed")
	}
	out, err := exec.Command("gh", "auth", "token").Output()
	if err != nil {
		return "", errors.New("gh CLI is not logged in")
	}
	return strings.TrimSpace(string(out)), nil
}

// GitHub is the Host adapter for github.com.
type GitHub struct {
	c     *gh.Client
	token string
}

var _ Host = (*GitHub)(nil)

// NewGitHub returns a client authenticated with token.
func NewGitHub(token string) (*GitHub, error) {
	c, err := gh.NewClient(gh.WithAuthToken(token))
	if err != nil {
		return nil, err
	}
	return &GitHub{c: c, token: token}, nil
}

// Validate returns the token owner's login. Classic tokens missing required scopes are rejected;
// fine-grained tokens carry no scope header and are checked by the API calls themselves.
func (c *GitHub) Validate(ctx context.Context) (string, error) {
	user, resp, err := c.c.Users.Get(ctx, "")
	if err != nil {
		return "", fmt.Errorf("GitHub token rejected: %w", err)
	}
	_, present := resp.Header[http.CanonicalHeaderKey("X-OAuth-Scopes")]
	if missing := MissingScopes(resp.Header.Get("X-OAuth-Scopes"), present); len(missing) > 0 {
		return "", fmt.Errorf("GitHub token is missing scopes: %s\n  gh:  gh auth refresh -h github.com -s %s\n  PAT: add the scopes to the token", strings.Join(missing, ", "), strings.Join(missing, ","))
	}
	return user.GetLogin(), nil
}

// MissingScopes checks a classic token's X-OAuth-Scopes header. present=false means a fine-grained token.
func MissingScopes(header string, present bool) []string {
	if !present {
		return nil
	}
	have := map[string]bool{}
	for _, s := range strings.Split(header, ",") {
		have[strings.TrimSpace(s)] = true
	}
	var missing []string
	if !have["repo"] {
		missing = append(missing, "repo")
	}
	if !have["read:org"] && !have["admin:org"] && !have["write:org"] {
		missing = append(missing, "read:org")
	}
	if !have["workflow"] {
		missing = append(missing, "workflow")
	}
	return missing
}

func (c *GitHub) Orgs(ctx context.Context) ([]string, error) {
	var orgs []string
	opts := &gh.ListOptions{PerPage: 100}
	for {
		page, resp, err := c.c.Organizations.List(ctx, "", opts)
		if err != nil {
			return nil, err
		}
		for _, o := range page {
			orgs = append(orgs, o.GetLogin())
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	slices.Sort(orgs)
	return orgs, nil
}

func (c *GitHub) RepoState(ctx context.Context, org, name string) (RepoState, error) {
	_, resp, err := c.c.Repositories.Get(ctx, org, name)
	if resp != nil && resp.StatusCode == http.StatusNotFound {
		return RepoMissing, nil
	}
	if err != nil {
		return 0, err
	}
	_, resp, err = c.c.Repositories.ListCommits(ctx, org, name, &gh.CommitsListOptions{ListOptions: gh.ListOptions{PerPage: 1}})
	if resp != nil && resp.StatusCode == http.StatusConflict {
		return RepoEmpty, nil
	}
	if err != nil {
		return 0, err
	}
	return RepoNonEmpty, nil
}

func (c *GitHub) CreatePrivateRepo(ctx context.Context, org, name string) error {
	_, _, err := c.c.Repositories.Create(ctx, org, &gh.Repository{
		Name:        new(name),
		Private:     new(true),
		Description: new("kubriX demo platform repository (created by kubrix demo)"),
	})
	return err
}

func (c *GitHub) DeleteRepo(ctx context.Context, org, name string) error {
	resp, err := c.c.Repositories.Delete(ctx, org, name)
	if resp != nil && resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("%w\nthe token needs the delete_repo scope (gh: 'gh auth refresh -s delete_repo')", err)
	}
	return err
}

func (c *GitHub) CloneURL(org, name string) string {
	return fmt.Sprintf("https://github.com/%s/%s.git", org, name)
}

func (c *GitHub) Token() string { return c.token }

// ReleaseTags lists the release tags (vX.Y.Z) of owner/repo, newest first.
func (c *GitHub) ReleaseTags(ctx context.Context, owner, repo string) ([]string, error) {
	var tags []string
	opts := &gh.ListOptions{PerPage: 100}
	for {
		page, resp, err := c.c.Repositories.ListTags(ctx, owner, repo, opts)
		if err != nil {
			return nil, err
		}
		for _, t := range page {
			tags = append(tags, t.GetName())
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return version.Releases(tags), nil
}

// OpenPullRequest creates a PR from head into base, or returns the URL of the one already open.
func (c *GitHub) OpenPullRequest(ctx context.Context, owner, repo, head, base, title, body string) (string, error) {
	existing, _, err := c.c.PullRequests.List(ctx, owner, repo, &gh.PullRequestListOptions{State: "open", Head: owner + ":" + head, Base: base})
	if err != nil {
		return "", err
	}
	if len(existing) > 0 {
		pr, _, err := c.c.PullRequests.Edit(ctx, owner, repo, existing[0].GetNumber(), &gh.PullRequest{Title: new(title), Body: new(body)})
		if err != nil {
			return "", err
		}
		return pr.GetHTMLURL(), nil
	}
	pr, _, err := c.c.PullRequests.Create(ctx, owner, repo, gh.CreatePullRequest{Title: new(title), Head: head, Base: base, Body: new(body)})
	if err != nil {
		return "", err
	}
	return pr.GetHTMLURL(), nil
}
