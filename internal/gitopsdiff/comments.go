package gitopsdiff

import (
	"context"
	"fmt"
	"io"

	"github.com/suxess-it/kubrix-cli/internal/git"
)

// CommentClient is the part of a git host's API that syncing PR comments needs.
type CommentClient interface {
	IssueComments(ctx context.Context, owner, repo string, number int) ([]git.IssueComment, error)
	CreateIssueComment(ctx context.Context, owner, repo string, number int, body string) error
	EditIssueComment(ctx context.Context, owner, repo string, id int64, body string) error
	DeleteIssueComment(ctx context.Context, owner, repo string, id int64) error
}

// PullRequest names the pull request to comment on and who the diff's own comments are written by.
type PullRequest struct {
	Owner, Repo string
	Number      int
	// Author is the login whose marked comments are this diff's; comments of anyone else are never touched.
	Author string
}

// SyncComments makes the pull request's diff comments match comments: a comment whose body changed is updated,
// an unchanged one is left alone, one that is no longer wanted is deleted, and new ones are created in order
// (the summary first). With dryRun it only prints what it would do.
func SyncComments(ctx context.Context, c CommentClient, pr PullRequest, comments []Comment, dryRun bool, log io.Writer) error {
	wanted := map[string]string{}
	for _, cm := range comments {
		wanted[cm.Name] = cm.Body
	}
	existing, err := c.IssueComments(ctx, pr.Owner, pr.Repo, pr.Number)
	if err != nil {
		return fmt.Errorf("list comments of pull request %d: %w", pr.Number, err)
	}
	do := func(action, name string, call func() error) error {
		_, _ = fmt.Fprintf(log, "%s comment %s\n", action, name)
		if dryRun {
			return nil
		}
		if err := call(); err != nil {
			return fmt.Errorf("%s comment %s: %w", action, name, err)
		}
		return nil
	}
	for _, ec := range existing {
		name, ok := MarkerName(ec.Body)
		if !ok || ec.Author != pr.Author {
			continue
		}
		body, keep := wanted[name]
		delete(wanted, name)
		switch {
		case !keep:
			err = do("delete", name, func() error { return c.DeleteIssueComment(ctx, pr.Owner, pr.Repo, ec.ID) })
		case body != ec.Body:
			err = do("update", name, func() error { return c.EditIssueComment(ctx, pr.Owner, pr.Repo, ec.ID, body) })
		}
		if err != nil {
			return err
		}
	}
	for _, cm := range comments {
		if body, ok := wanted[cm.Name]; ok {
			if err := do("create", cm.Name, func() error { return c.CreateIssueComment(ctx, pr.Owner, pr.Repo, pr.Number, body) }); err != nil {
				return err
			}
		}
	}
	return nil
}
