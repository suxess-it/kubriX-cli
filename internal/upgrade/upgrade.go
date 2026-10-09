package upgrade

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/suxess-it/kubrix-cli/internal/gitops"
	"github.com/suxess-it/kubrix-cli/internal/platformrepo"
	"github.com/suxess-it/kubrix-cli/internal/version"
)

const (
	botName  = "kubrix-cli[kubrix-bot]"
	botEmail = "kubrix-cli[kubrix-bot]@users.noreply.github.com"
	// MainTarget upgrades to upstream's unreleased main branch (contributor mode only).
	MainTarget = "main"
)

var ErrUnrelatedHistory = errors.New("the repository shares no history with kubriX upstream (it was probably bootstrapped with KUBRIX_BOOTSTRAP_KEEP_HISTORY=false), so it cannot be upgraded by merging")

// ConflictError lists conflicting files that are not generated, so they need a human.
type ConflictError struct{ Files []string }

func (e *ConflictError) Error() string {
	return "merge conflicts in files the CLI cannot regenerate: " + strings.Join(e.Files, ", ")
}

// workspace is a temporary clone of the customer repo with kubriX upstream as a second remote.
type workspace struct {
	Dir string
	git *gitops.Git
}

// openWorkspace clones the customer repo and fetches upstream's tags and main.
func openWorkspace(ctx context.Context, customerURL, upstreamURL, token string) (*workspace, error) {
	if err := gitops.Available(); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "kubrix-upgrade-")
	if err != nil {
		return nil, err
	}
	w := &workspace{Dir: dir, git: gitops.New(dir, token, botName, botEmail)}
	for _, args := range [][]string{
		{"clone", "-q", customerURL, "."},
		{"remote", "add", "upstream", upstreamURL},
		{"fetch", "-q", "--tags", "upstream", "+refs/heads/main:refs/remotes/upstream/main"},
	} {
		if _, err := w.git.Run(ctx, args...); err != nil {
			w.close()
			return nil, err
		}
	}
	return w, nil
}

func (w *workspace) close() { _ = os.RemoveAll(w.Dir) }

// installedVersion reads the release-please manifest the customer repo inherited from upstream.
func (w *workspace) installedVersion() (string, bool) {
	data, err := os.ReadFile(filepath.Join(w.Dir, ".release-please-manifest.json"))
	if err != nil {
		return "", false
	}
	var manifest map[string]string
	if json.Unmarshal(data, &manifest) != nil || manifest["."] == "" {
		return "", false
	}
	v := version.Normalize(manifest["."])
	return v, version.IsRelease(v)
}

// releases lists the release tags of the repository, newest first.
func (w *workspace) releases(ctx context.Context) ([]string, error) {
	tags, err := w.git.Lines(ctx, "tag", "-l", "v*")
	return version.Releases(tags), err
}

func ref(target string) string {
	if target == MainTarget {
		return "refs/remotes/upstream/main"
	}
	return "refs/tags/" + target
}

// mergeBase returns the common ancestor with target, or ErrUnrelatedHistory.
func (w *workspace) mergeBase(ctx context.Context, target string) (string, error) {
	base, err := w.git.Run(ctx, "merge-base", "HEAD", ref(target))
	if err != nil || base == "" {
		return "", ErrUnrelatedHistory
	}
	return base, nil
}

// unreleasedCommits counts upstream commits in the repo that are newer than the installed release,
// i.e. the repo was bootstrapped from a branch and the manifest version is only approximate.
func (w *workspace) unreleasedCommits(ctx context.Context, installed string) int {
	base, err := w.git.Run(ctx, "merge-base", "HEAD", ref(MainTarget))
	if err != nil {
		return 0
	}
	count, err := w.git.Run(ctx, "rev-list", "--count", ref(installed)+".."+base)
	if err != nil {
		return 0
	}
	n := 0
	_, _ = fmt.Sscanf(count, "%d", &n)
	return n
}

// notes returns the target's CHANGELOG sections newer than installed.
func (w *workspace) notes(ctx context.Context, installed, target string) (string, error) {
	changelog, err := w.git.Run(ctx, "show", ref(target)+":CHANGELOG.md")
	if err != nil {
		return "", err
	}
	to := target
	if target == MainTarget {
		to = ""
	}
	return version.ChangelogBetween(changelog, installed, to), nil
}

// excludedApps finds the apps that were excluded at bootstrap. Call it before merge.
func (w *workspace) excludedApps(targetType string) ([]string, error) {
	return platformrepo.At(w.Dir).ExcludedApps(targetType)
}

func BranchName(target string) string { return "kubrix-upgrade-" + target }

// mergeResult lists what the CLI decided on its own, for the pull request description.
type mergeResult struct {
	Resolved []string // conflicts resolved automatically
	Removed  []string // rendered outputs whose template the target no longer has
}

// merge merges target into a new upgrade branch without committing. Generated files follow their
// template: outputs of templates the target still has keep the repo's version (they are re-rendered
// later), outputs of templates the target dropped are removed (or replaced by upstream's plain file),
// and customer-config.yaml is kept. Any other conflict aborts the merge.
func (w *workspace) merge(ctx context.Context, target string) (mergeResult, error) {
	var result mergeResult
	if _, err := w.git.Run(ctx, "checkout", "-q", "-B", BranchName(target)); err != nil {
		return result, err
	}
	// Directory-rename detection would move rendered outputs into renamed upstream dirs
	// (v7.0.0 renamed charts/vault to charts/openbao) and conflict there.
	_, mergeErr := w.git.Run(ctx, "-c", "merge.directoryRenames=false", "merge", "-q", "--no-ff", "--no-commit", ref(target))
	conflicts, err := w.git.Lines(ctx, "diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return result, err
	}
	if mergeErr != nil && len(conflicts) == 0 {
		return result, mergeErr
	}
	ours, err := w.outputs(ctx, "HEAD")
	if err != nil {
		return result, err
	}
	theirs, err := w.outputs(ctx, ref(target))
	if err != nil {
		return result, err
	}
	theirFiles, err := w.files(ctx, ref(target))
	if err != nil {
		return result, err
	}

	var manual []string
	for _, file := range conflicts {
		if file != platformrepo.ConfigPath && !theirs[file] && !ours[file] {
			manual = append(manual, file)
		}
	}
	if len(manual) > 0 {
		_, _ = w.git.Run(ctx, "merge", "--abort")
		return result, &ConflictError{Files: manual}
	}
	for _, file := range conflicts {
		switch {
		case file == platformrepo.ConfigPath || theirs[file]:
			err = w.keep(ctx, file, "--ours")
		case theirFiles[file]:
			err = w.keep(ctx, file, "--theirs")
		default:
			err = w.remove(ctx, file)
			result.Removed = append(result.Removed, file)
		}
		if err != nil {
			return result, err
		}
		result.Resolved = append(result.Resolved, file)
	}

	// Outputs of dropped templates that merged cleanly are stale too.
	for out := range ours {
		if theirs[out] || theirFiles[out] || slices.Contains(conflicts, out) {
			continue
		}
		if _, err := os.Stat(filepath.Join(w.Dir, out)); err != nil {
			continue
		}
		if err := w.remove(ctx, out); err != nil {
			return result, err
		}
		result.Removed = append(result.Removed, out)
	}
	slices.Sort(result.Removed)
	return result, nil
}

func (w *workspace) keep(ctx context.Context, file, side string) error {
	if _, err := w.git.Run(ctx, "checkout", side, "--", file); err != nil {
		return w.remove(ctx, file)
	}
	_, err := w.git.Run(ctx, "add", "--", file)
	return err
}

func (w *workspace) remove(ctx context.Context, file string) error {
	_, err := w.git.Run(ctx, "rm", "-q", "-f", "--ignore-unmatch", "--", file)
	return err
}

// outputs maps the render output of every template in tree to true.
func (w *workspace) outputs(ctx context.Context, tree string) (map[string]bool, error) {
	files, err := w.git.Lines(ctx, "ls-tree", "-r", "--name-only", tree)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, f := range files {
		if platformrepo.IsTemplate(f) {
			out[platformrepo.OutputPath(f)] = true
		}
	}
	return out, nil
}

func (w *workspace) files(ctx context.Context, tree string) (map[string]bool, error) {
	files, err := w.git.Lines(ctx, "ls-tree", "-r", "--name-only", tree)
	if err != nil {
		return nil, err
	}
	set := map[string]bool{}
	for _, f := range files {
		set[f] = true
	}
	return set, nil
}

// rerender renders the templates that changed between base and target (including new ones) with the
// repo's customer-config.yaml, re-applies the excluded apps, and stages the results.
func (w *workspace) rerender(ctx context.Context, base, target, targetType string, excluded []string) ([]string, error) {
	// --no-renames: a moved template is a new one that must be rendered at its new place.
	changed, err := w.git.Lines(ctx, "diff", "--name-only", "--no-renames", "--diff-filter=AM", base, ref(target), "--", platformrepo.TemplateGlob)
	if err != nil {
		return nil, err
	}
	if len(changed) == 0 {
		return nil, nil
	}
	repo := platformrepo.At(w.Dir)
	outputs, err := repo.Render(changed)
	if err != nil {
		return nil, err
	}
	// The target values are only rendered again when their template changed, and then lose the excluded apps again.
	if slices.Contains(outputs, platformrepo.TargetValuesPath(targetType)) {
		if err := repo.Exclude(targetType, excluded); err != nil {
			return nil, err
		}
	}
	for _, out := range outputs {
		if _, err := w.git.Run(ctx, "add", "--", out); err != nil {
			return nil, err
		}
	}
	return outputs, nil
}

// commitAndPush commits the merge and force-pushes the CLI-owned upgrade branch.
func (w *workspace) commitAndPush(ctx context.Context, target, message string) (string, error) {
	if _, err := w.git.Run(ctx, "commit", "-q", "-m", message); err != nil {
		return "", err
	}
	branch := BranchName(target)
	_, err := w.git.Run(ctx, "push", "-q", "--force", "origin", "HEAD:refs/heads/"+branch)
	return branch, err
}
