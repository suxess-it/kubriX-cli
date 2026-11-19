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

// BaseCandidates lists the revisions a change is usually compared against, the most likely first: the remote's
// default branch, then local branches and the other remote branches.
func BaseCandidates(ctx context.Context, repo string) ([]string, error) {
	g := gitops.New(repo, "", "kubrix", "kubrix@localhost")
	refs, err := g.Lines(ctx, "for-each-ref", "--format=%(refname:short)", "refs/heads", "refs/remotes")
	if err != nil {
		return nil, err
	}
	var out []string
	if def, err := g.Run(ctx, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); err == nil && def != "" {
		out = append(out, def)
	}
	for _, r := range refs {
		// "origin" is how for-each-ref shortens refs/remotes/origin/HEAD.
		if r == "origin" || strings.HasSuffix(r, "/HEAD") || slices.Contains(out, r) {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

// Choices is what a diff of a change can render: the target types and charts of either revision, and the charts the
// change touches (all of them when it touches the target chart, a bootstrap Application or no chart).
type Choices struct {
	TargetTypes []string
	Charts      []string
	Changed     []string
}

// ListChoices finds the choices of comparing head (empty: the working tree) against base, reading git trees and the
// working tree without checking anything out.
func ListChoices(ctx context.Context, repo, base, head string) (Choices, error) {
	g := gitops.New(repo, "", "kubrix", "kubrix@localhost")
	var paths []string
	for _, rev := range []string{base, head} {
		if rev == "" {
			continue
		}
		// ls-tree takes paths, not patterns: list the root for the bootstrap Applications.
		root, err := g.Lines(ctx, "ls-tree", "--name-only", rev)
		if err != nil {
			return Choices{}, fmt.Errorf("list %s: %w", rev, err)
		}
		files, err := g.Lines(ctx, "ls-tree", "-r", "--name-only", rev, "--", targetchart.Dir, ChartsDir)
		if err != nil {
			return Choices{}, fmt.Errorf("list %s: %w", rev, err)
		}
		paths = append(append(paths, root...), files...)
	}
	targetTypes := targetchart.TargetTypesOf(paths)
	charts := chartsOf(paths)
	if head == "" {
		types, err := targetchart.TargetTypes(repo)
		if err != nil {
			return Choices{}, err
		}
		names, err := chartNames(repo)
		if err != nil {
			return Choices{}, err
		}
		targetTypes = union(targetTypes, types)
		charts = union(charts, names)
	}
	files, err := changedFiles(ctx, g, base, head)
	if err != nil {
		return Choices{}, err
	}
	return Choices{TargetTypes: targetTypes, Charts: charts, Changed: changedCharts(files, charts)}, nil
}

// chartsOf names the chart directories that paths lie in.
func chartsOf(paths []string) []string {
	var charts []string
	for _, p := range paths {
		if rest, ok := strings.CutPrefix(p, ChartsDir+"/"); ok {
			if chart, _, ok := strings.Cut(rest, "/"); ok && !slices.Contains(charts, chart) {
				charts = append(charts, chart)
			}
		}
	}
	slices.Sort(charts)
	return charts
}

func union(a, b []string) []string {
	out := slices.Clone(a)
	for _, s := range b {
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	slices.Sort(out)
	return out
}
