// Package gittest provides an in-memory git.Host for tests.
package gittest

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/suxess-it/kubrix-cli/internal/git"
)

// PullRequest is one call to OpenPullRequest.
type PullRequest struct {
	Owner, Repo, Head, Base, Title, Body string
}

// Host is a git.Host that keeps repositories in memory. Set URLs to point clone URLs at local bare repositories.
type Host struct {
	User     string
	Password string
	OrgNames []string
	// Repos maps "org/name" to its state; a repository that is not listed is missing.
	Repos map[string]git.RepoState
	// URLs maps "org/name" to the clone URL; unlisted repositories get an unreachable https URL.
	URLs map[string]string

	// Releases are the release tags of the kubriX source repository, newest first; ReleasesErr makes listing fail.
	Releases    []string
	ReleasesErr error

	// PRErr makes OpenPullRequest fail.
	PRErr error

	Created []string
	Deleted []string
	PRs     []PullRequest
}

var _ git.Host = (*Host)(nil)

func New(login string, orgs ...string) *Host {
	return &Host{User: login, Password: "test-token", OrgNames: orgs, Repos: map[string]git.RepoState{}, URLs: map[string]string{}}
}

func (h *Host) Validate(context.Context) (string, error) { return h.User, nil }
func (h *Host) Token() string                            { return h.Password }
func (h *Host) Orgs(context.Context) ([]string, error)   { return slices.Clone(h.OrgNames), nil }

func (h *Host) RepoState(_ context.Context, org, name string) (git.RepoState, error) {
	return h.Repos[org+"/"+name], nil
}

func (h *Host) CreatePrivateRepo(_ context.Context, org, name string) error {
	slug := org + "/" + name
	if _, ok := h.Repos[slug]; ok {
		return errors.New(slug + " already exists")
	}
	h.Repos[slug] = git.RepoEmpty
	h.Created = append(h.Created, slug)
	return nil
}

func (h *Host) DeleteRepo(_ context.Context, org, name string) error {
	slug := org + "/" + name
	delete(h.Repos, slug)
	h.Deleted = append(h.Deleted, slug)
	return nil
}

func (h *Host) CloneURL(org, name string) string {
	if u, ok := h.URLs[org+"/"+name]; ok {
		return u
	}
	return fmt.Sprintf("https://git.invalid/%s/%s.git", org, name)
}

func (h *Host) OpenPullRequest(_ context.Context, owner, repo, head, base, title, body string) (string, error) {
	if h.PRErr != nil {
		return "", h.PRErr
	}
	h.PRs = append(h.PRs, PullRequest{owner, repo, head, base, title, body})
	return fmt.Sprintf("https://git.invalid/%s/%s/pull/%d", owner, repo, len(h.PRs)), nil
}

// ReleaseTags lists Releases, standing in for the kubriX source repository's tags.
func (h *Host) ReleaseTags(context.Context, string, string) ([]string, error) {
	return slices.Clone(h.Releases), h.ReleasesErr
}
