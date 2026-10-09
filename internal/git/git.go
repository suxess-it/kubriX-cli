// Package git is the seam to the service that hosts platform repositories and pull requests.
package git

import "context"

// GitHubName names the GitHub Host, as saved on an Installation.
const GitHubName = "github"

// RepoState tells what a platform repository already holds.
type RepoState int

const (
	RepoMissing RepoState = iota
	RepoEmpty
	RepoNonEmpty
)

// Host is a git hosting service. GitHub is the only adapter today; the gittest fake is the second.
type Host interface {
	// Validate checks the token and returns the login it belongs to.
	Validate(ctx context.Context) (login string, err error)
	// Token is the credential used to push to platform repositories.
	Token() string
	// Orgs lists the organizations a platform repository can be created in.
	Orgs(ctx context.Context) ([]string, error)
	RepoState(ctx context.Context, org, name string) (RepoState, error)
	CreatePrivateRepo(ctx context.Context, org, name string) error
	DeleteRepo(ctx context.Context, org, name string) error
	CloneURL(org, name string) string
	// OpenPullRequest creates a PR from head into base, or updates and returns the one already open.
	OpenPullRequest(ctx context.Context, owner, repo, head, base, title, body string) (url string, err error)
}
