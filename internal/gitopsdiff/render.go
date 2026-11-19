package gitopsdiff

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"helm.sh/helm/v4/pkg/chart/common"
	"helm.sh/helm/v4/pkg/chart/common/util"
	chart "helm.sh/helm/v4/pkg/chart/v2"
	"helm.sh/helm/v4/pkg/chart/v2/loader"
	chartutil "helm.sh/helm/v4/pkg/chart/v2/util"
	"helm.sh/helm/v4/pkg/downloader"
	helmengine "helm.sh/helm/v4/pkg/engine"
	"helm.sh/helm/v4/pkg/getter"
	"helm.sh/helm/v4/pkg/registry"
)

// DependencyBuilder fetches the dependencies of the chart in dir into its charts/ directory.
type DependencyBuilder func(ctx context.Context, dir string) error

// HelmDependencies builds dependencies like `helm dependency build`, falling back to `helm dependency update` when
// the chart has no Chart.lock or it is out of date. Repositories are used by URL; helm's own repository config and
// cache are not touched, so state lives in home.
func HelmDependencies(home string) DependencyBuilder {
	return func(_ context.Context, dir string) error {
		rc, err := registry.NewClient(registry.ClientOptWriter(io.Discard))
		if err != nil {
			return err
		}
		m := &downloader.Manager{
			Out:              io.Discard,
			ChartPath:        dir,
			Getters:          getter.Getters(),
			RegistryClient:   rc,
			RepositoryConfig: filepath.Join(home, "repositories.yaml"),
			RepositoryCache:  filepath.Join(home, "repository"),
			ContentCache:     filepath.Join(home, "content"),
		}
		if err := m.Build(); err != nil {
			if err := m.Update(); err != nil {
				return fmt.Errorf("helm dependency update %s: %w", filepath.Base(dir), err)
			}
		}
		return nil
	}
}

// renderer prepares charts of a revision in scratch copies, so building dependencies never touches the repository,
// and shares fetched remote dependencies between revisions whose Chart.lock (or Chart.yaml) is the same.
type renderer struct {
	scratch string
	deps    DependencyBuilder

	mu       sync.Mutex
	prepared map[string]*preparation // per revision and chart
	fetched  map[string]*sync.Mutex  // per dependency digest, so one digest is fetched once
}

type preparation struct {
	once sync.Once
	dir  string
	err  error
}

func newRenderer(scratch string, deps DependencyBuilder) *renderer {
	return &renderer{scratch: scratch, deps: deps, prepared: map[string]*preparation{}, fetched: map[string]*sync.Mutex{}}
}

// load returns the chart in dir of revision rev with its dependencies. Each call loads a fresh copy, because
// rendering prunes the disabled dependencies of the chart it is given.
func (r *renderer) load(ctx context.Context, rev, dir string) (*chart.Chart, error) {
	key := rev + "/" + filepath.Base(dir)
	r.mu.Lock()
	p, ok := r.prepared[key]
	if !ok {
		p = &preparation{}
		r.prepared[key] = p
	}
	r.mu.Unlock()
	p.once.Do(func() { p.dir, p.err = r.prepare(ctx, rev, dir) })
	if p.err != nil {
		return nil, p.err
	}
	ch, err := loader.LoadDir(p.dir)
	if err != nil {
		return nil, fmt.Errorf("load chart %s: %w", filepath.Base(dir), err)
	}
	return ch, nil
}

// prepare copies the chart to scratch and builds its dependencies there.
func (r *renderer) prepare(ctx context.Context, rev, dir string) (string, error) {
	work := filepath.Join(r.scratch, "charts", rev, filepath.Base(dir))
	if err := os.CopyFS(work, os.DirFS(dir)); err != nil {
		return "", fmt.Errorf("copy chart %s: %w", filepath.Base(dir), err)
	}
	ch, err := loader.LoadDir(work)
	if err != nil {
		return "", fmt.Errorf("load chart %s: %w", filepath.Base(dir), err)
	}
	if len(ch.Metadata.Dependencies) == 0 {
		return work, nil
	}
	local, err := pinLocalDependencies(work, dir, ch.Metadata.Dependencies)
	if err != nil {
		return "", err
	}
	if local {
		// A local chart can change without a version bump, so its archive is never shared between revisions.
		return work, r.deps(ctx, work)
	}
	return work, r.buildDependencies(ctx, work)
}

// pinLocalDependencies points relative file:// dependencies of the copy in work at the original chart's directory,
// which they are relative to, and reports whether the chart has local dependencies at all.
func pinLocalDependencies(work, dir string, deps []*chart.Dependency) (bool, error) {
	path := filepath.Join(work, "Chart.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	text, local := string(data), false
	for _, d := range deps {
		rel, ok := strings.CutPrefix(d.Repository, "file://")
		if !ok {
			continue
		}
		local = true
		if filepath.IsAbs(rel) {
			continue
		}
		abs, err := filepath.Abs(filepath.Join(dir, rel))
		if err != nil {
			return false, err
		}
		text = strings.ReplaceAll(text, d.Repository, "file://"+abs)
	}
	return local, os.WriteFile(path, []byte(text), 0o644)
}

func (r *renderer) buildDependencies(ctx context.Context, work string) error {
	key, err := dependencyDigest(work)
	if err != nil {
		return err
	}
	r.mu.Lock()
	lock, ok := r.fetched[key]
	if !ok {
		lock = &sync.Mutex{}
		r.fetched[key] = lock
	}
	r.mu.Unlock()
	lock.Lock()
	defer lock.Unlock()

	cache := filepath.Join(r.scratch, "dependencies", key)
	if _, err := os.Stat(cache); err == nil {
		return copyArchives(cache, filepath.Join(work, "charts"))
	}
	if err := r.deps(ctx, work); err != nil {
		return err
	}
	return copyArchives(filepath.Join(work, "charts"), cache)
}

// dependencyDigest identifies a chart's dependencies: its Chart.lock, or its Chart.yaml when it has no lock.
func dependencyDigest(dir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, "Chart.lock"))
	if os.IsNotExist(err) {
		data, err = os.ReadFile(filepath.Join(dir, "Chart.yaml"))
	}
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// copyArchives copies the fetched chart archives from one charts/ directory to another.
func copyArchives(from, to string) error {
	if err := os.MkdirAll(to, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(from)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".tgz") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(from, e.Name()))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(to, e.Name()), data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// render renders a chart like `helm template <release> <chart> --include-crds`: one entry per template or CRD file,
// keyed by its path below the chart, each document headed by "# Source:".
func render(ch *chart.Chart, release string, vals map[string]any) (map[string]string, error) {
	if err := chartutil.ProcessDependencies(ch, vals); err != nil {
		return nil, fmt.Errorf("chart %s dependencies: %w", ch.Name(), err)
	}
	renderVals, err := util.ToRenderValues(ch, vals, common.ReleaseOptions{Name: release, Namespace: "default", IsInstall: true}, nil)
	if err != nil {
		return nil, fmt.Errorf("chart %s values: %w", ch.Name(), err)
	}
	files, err := helmengine.Render(ch, renderVals)
	if err != nil {
		return nil, fmt.Errorf("render chart %s: %w", ch.Name(), err)
	}
	out := map[string]string{}
	for name, content := range files {
		base := path.Base(name)
		if strings.HasPrefix(base, "_") || base == "NOTES.txt" {
			continue
		}
		if doc := documents(name, content); doc != "" {
			out[name] = doc
		}
	}
	for _, crd := range allCRDs(ch) {
		out[crd.Filename] = documents(crd.Filename, string(crd.File.Data))
	}
	return out, nil
}

// allCRDs are the CRD files of the chart and its enabled subcharts, like helm template --include-crds.
func allCRDs(ch *chart.Chart) []chart.CRD {
	crds := ch.CRDObjects()
	for _, dep := range ch.Dependencies() {
		crds = append(crds, allCRDs(dep)...)
	}
	return crds
}

// documents puts a "# Source:" header on each non-empty YAML document, as helm template prints them.
func documents(name, content string) string {
	var b strings.Builder
	for doc := range strings.SplitSeq(content, "\n---") {
		doc = strings.TrimSpace(strings.TrimPrefix(doc, "---"))
		if doc == "" {
			continue
		}
		fmt.Fprintf(&b, "---\n# Source: %s\n%s\n", name, doc)
	}
	return b.String()
}

// defaultValues are the raw default values of each direct subchart, like `helm show values` of its archive.
func defaultValues(ch *chart.Chart) map[string]string {
	out := map[string]string{}
	for _, dep := range ch.Dependencies() {
		for _, f := range dep.Raw {
			if f.Name == "values.yaml" {
				out[ch.Name()+"_"+dep.Name()+"_default-values.out"] = string(f.Data)
			}
		}
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
