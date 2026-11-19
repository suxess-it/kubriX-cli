package gitopsdiff

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/suxess-it/kubrix-cli/internal/git"
)

// fakeComments records the calls made to it on top of a fixed list of existing comments.
type fakeComments struct {
	existing []git.IssueComment
	calls    []string
}

func (f *fakeComments) IssueComments(context.Context, string, string, int) ([]git.IssueComment, error) {
	return f.existing, nil
}

func (f *fakeComments) CreateIssueComment(_ context.Context, _, _ string, _ int, body string) error {
	name, _ := MarkerName(body)
	f.calls = append(f.calls, "create "+name)
	return nil
}

func (f *fakeComments) EditIssueComment(_ context.Context, _, _ string, id int64, _ string) error {
	f.calls = append(f.calls, fmt.Sprintf("edit %d", id))
	return nil
}

func (f *fakeComments) DeleteIssueComment(_ context.Context, _, _ string, id int64) error {
	f.calls = append(f.calls, fmt.Sprintf("delete %d", id))
	return nil
}

func TestSyncCommentsMakesTheDiffsOwnCommentsMatch(t *testing.T) {
	bot := "github-actions[bot]"
	wanted := []Comment{{"summary", Marker("summary") + "new summary"}, {"chart-loki", Marker("chart-loki") + "same"}, {"chart-new", Marker("chart-new") + "x"}}
	existing := []git.IssueComment{
		{ID: 1, Author: bot, Body: Marker("summary") + "old summary"},     // changed: edited
		{ID: 2, Author: bot, Body: Marker("chart-loki") + "same"},         // unchanged: left alone
		{ID: 3, Author: bot, Body: Marker("chart-gone") + "stale"},        // no longer wanted: deleted
		{ID: 4, Author: "someone", Body: Marker("chart-gone") + "theirs"}, // another author: untouched
		{ID: 5, Author: bot, Body: "a comment without marker"},            // no marker: untouched
		{ID: 6, Author: bot, Body: "text before " + Marker("chart-gone")}, // marker not at the start: untouched
	}
	cases := []struct {
		name     string
		existing []git.IssueComment
		dryRun   bool
		want     []string
	}{
		{"existing comments are edited, kept, deleted or created", existing, false, []string{"edit 1", "delete 3", "create chart-new"}},
		{"without comments the summary is created first", nil, false, []string{"create summary", "create chart-loki", "create chart-new"}},
		{"a dry run calls nothing", existing, true, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fake := &fakeComments{existing: c.existing}
			var log bytes.Buffer
			if err := SyncComments(context.Background(), fake, PullRequest{Owner: "o", Repo: "r", Number: 7, Author: bot}, wanted, c.dryRun, &log); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(fake.calls, c.want) {
				t.Fatalf("calls = %v, want %v", fake.calls, c.want)
			}
			if c.dryRun && log.String() != "update comment summary\ndelete comment chart-gone\ncreate comment chart-new\n" {
				t.Fatalf("dry run log = %q", log.String())
			}
		})
	}
}
