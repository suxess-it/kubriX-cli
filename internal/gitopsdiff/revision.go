package gitopsdiff

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/suxess-it/kubrix-cli/internal/gitops"
	"github.com/suxess-it/kubrix-cli/internal/values/targetchart"
)

// ChartsDir holds the application charts of a platform repository.
const ChartsDir = "platform-apps/charts"

// worktree checks out ref into dir and returns a function that removes the worktree again.
func worktree(ctx context.Context, g *gitops.Git, ref, dir string) (func(), error) {
	if _, err := g.Run(ctx, "worktree", "add", "--detach", dir, ref); err != nil {
		return nil, fmt.Errorf("check out %s: %w", ref, err)
	}
	return func() {
		// The context may already be cancelled; removing the worktree must still happen.
		_, _ = g.Run(context.WithoutCancel(ctx), "worktree", "remove", "--force", dir)
	}, nil
}

// changedFiles lists the files that differ between base and head, or between base and the working tree (including
// untracked files) when head is empty. Like a pull request, it compares against the merge base.
func changedFiles(ctx context.Context, g *gitops.Git, base, head string) ([]string, error) {
	if head != "" {
		return g.Lines(ctx, "diff", "--name-only", base+"..."+head)
	}
	mergeBase, err := g.Run(ctx, "merge-base", base, "HEAD")
	if err != nil {
		return nil, err
	}
	changed, err := g.Lines(ctx, "diff", "--name-only", mergeBase)
	if err != nil {
		return nil, err
	}
	untracked, err := g.Lines(ctx, "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	return append(changed, untracked...), nil
}

// changedCharts are the charts whose directory a change touches. A change of the target chart or a bootstrap
// Application can change how every chart is rendered, so it selects all charts, as does a change of no chart at all
// (the CI workflow itself changed, say).
func changedCharts(files []string, all []string) []string {
	var charts []string
	for _, f := range files {
		if strings.HasPrefix(f, targetchart.Dir+"/") || strings.HasPrefix(f, "bootstrap-app-") {
			return all
		}
		if rest, ok := strings.CutPrefix(f, ChartsDir+"/"); ok {
			if chart, _, ok := strings.Cut(rest, "/"); ok && !slices.Contains(charts, chart) {
				charts = append(charts, chart)
			}
		}
	}
	if len(charts) == 0 {
		return all
	}
	slices.Sort(charts)
	return charts
}

// chartNames lists the chart directories of the given revisions, sorted and without duplicates.
func chartNames(dirs ...string) ([]string, error) {
	var names []string
	for _, dir := range dirs {
		entries, err := os.ReadDir(filepath.Join(dir, ChartsDir))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if e.IsDir() && !slices.Contains(names, e.Name()) {
				names = append(names, e.Name())
			}
		}
	}
	slices.Sort(names)
	return names, nil
}
