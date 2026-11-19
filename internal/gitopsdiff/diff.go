package gitopsdiff

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/suxess-it/kubrix-cli/internal/gitops"
	"github.com/suxess-it/kubrix-cli/internal/values/engine"
	"github.com/suxess-it/kubrix-cli/internal/values/overlay"
	"github.com/suxess-it/kubrix-cli/internal/values/targetchart"
)

// TestCase is one set of values every changed chart is rendered with: the files and parameters a target type's
// Application passes to the chart, or chart-local values files and --set expressions given by hand.
type TestCase struct {
	Name string
	// TargetType resolves each chart's values through the target chart, as Argo CD would.
	TargetType string
	// Files are values files looked up in each chart's directory; missing ones are skipped.
	Files []string
	// Sets are helm --set expressions applied after Files.
	Sets []engine.SetSource
}

// Options of a diff.
type Options struct {
	// Repo is the platform repository; its git history provides Base and Head.
	Repo string
	Base string
	// Head is the revision to compare; empty compares the working tree.
	Head string
	// TestCases default to every target type of either revision.
	TestCases []TestCase
	// Charts to compare; empty compares the charts the change touches, or all charts with AllCharts.
	Charts    []string
	AllCharts bool
	// CustomerConfig renders templates; empty uses each revision's bootstrap/customer-config.yaml.
	CustomerConfig string
	// Dependencies fetches chart dependencies; nil uses HelmDependencies.
	Dependencies DependencyBuilder
	// Parallel renders this many charts at once; 0 uses the number of CPUs.
	Parallel int
}

// Run checks out both revisions, renders the selected charts with every test case and reports what changed.
func Run(ctx context.Context, opts Options) (*Report, error) {
	scratch, err := os.MkdirTemp("", "kubrix-diff-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(scratch) }()
	g := gitops.New(opts.Repo, "", "kubrix", "kubrix@localhost")

	revs := map[string]string{baseName: filepath.Join(scratch, baseName), headName: opts.Repo}
	removeBase, err := worktree(ctx, g, opts.Base, revs[baseName])
	if err != nil {
		return nil, err
	}
	defer removeBase()
	if opts.Head != "" {
		revs[headName] = filepath.Join(scratch, headName)
		removeHead, err := worktree(ctx, g, opts.Head, revs[headName])
		if err != nil {
			return nil, err
		}
		defer removeHead()
	}

	charts, err := selectCharts(ctx, g, opts, revs)
	if err != nil {
		return nil, err
	}
	testCases := opts.TestCases
	if len(testCases) == 0 {
		if testCases, err = targetTypeTestCases(revs); err != nil {
			return nil, err
		}
	}
	deps := opts.Dependencies
	if deps == nil {
		deps = HelmDependencies(filepath.Join(scratch, "helm"))
	}
	d := &differ{opts: opts, revs: revs, renderer: newRenderer(scratch, deps)}
	return d.run(ctx, charts, testCases)
}

func selectCharts(ctx context.Context, g *gitops.Git, opts Options, revs map[string]string) ([]string, error) {
	if len(opts.Charts) != 0 {
		return opts.Charts, nil
	}
	all, err := chartNames(revs[baseName], revs[headName])
	if err != nil || opts.AllCharts {
		return all, err
	}
	files, err := changedFiles(ctx, g, opts.Base, opts.Head)
	if err != nil {
		return nil, err
	}
	return changedCharts(files, all), nil
}

// targetTypeTestCases has one test case per target type of either revision.
func targetTypeTestCases(revs map[string]string) ([]TestCase, error) {
	var names []string
	for _, dir := range revs {
		types, err := targetchart.TargetTypes(dir)
		if err != nil {
			return nil, err
		}
		for _, t := range types {
			if !slices.Contains(names, t) {
				names = append(names, t)
			}
		}
	}
	if len(names) == 0 {
		return nil, errors.New("no target types found; name a test case")
	}
	slices.Sort(names)
	cases := make([]TestCase, len(names))
	for i, n := range names {
		cases[i] = TestCase{Name: n, TargetType: n}
	}
	return cases, nil
}

type differ struct {
	opts     Options
	revs     map[string]string
	renderer *renderer
}

// rendering is one chart of one revision rendered with one test case; it is empty when the test case does not
// install the chart in that revision.
type rendering struct {
	files  map[string]string
	values *engine.Result
}

func (d *differ) run(ctx context.Context, charts []string, testCases []TestCase) (*Report, error) {
	apps := map[string]map[string]map[string]*overlay.App{} // revision → test case → chart → app
	for rev, dir := range d.revs {
		apps[rev] = map[string]map[string]*overlay.App{}
		for _, tc := range testCases {
			byChart, err := d.resolve(dir, tc, charts)
			if err != nil {
				return nil, fmt.Errorf("%s revision, test case %s: %w", rev, tc.Name, err)
			}
			apps[rev][tc.Name] = byChart
		}
	}

	var mu sync.Mutex
	results := map[string]rendering{} // revision/chart/test case
	defaults := map[string]map[string]string{}
	group, gctx := errgroup.WithContext(ctx)
	group.SetLimit(cmp.Or(d.opts.Parallel, runtime.NumCPU()))
	for rev, dir := range d.revs {
		for _, chart := range charts {
			chartDir := filepath.Join(dir, ChartsDir, chart)
			if _, err := os.Stat(chartDir); err != nil {
				continue
			}
			group.Go(func() error {
				ch, err := d.renderer.load(gctx, rev, chartDir)
				if err != nil {
					return err
				}
				mu.Lock()
				defaults[rev] = mergeInto(defaults[rev], defaultValues(ch))
				mu.Unlock()
				return nil
			})
			for _, tc := range testCases {
				app := apps[rev][tc.Name][chart]
				if app == nil {
					continue
				}
				group.Go(func() error {
					r, err := d.render(gctx, rev, chartDir, chart, tc, app)
					if err != nil {
						return fmt.Errorf("%s revision, chart %s, test case %s: %w", rev, chart, tc.Name, err)
					}
					mu.Lock()
					results[rev+"/"+chart+"/"+tc.Name] = r
					mu.Unlock()
					return nil
				})
			}
		}
	}
	if err := group.Wait(); err != nil {
		return nil, err
	}

	report := &Report{Files: []FileDiff{}, ValueChanges: []ValueChange{}}
	for _, chart := range charts {
		for _, tc := range testCases {
			base, head := results[baseName+"/"+chart+"/"+tc.Name], results[headName+"/"+chart+"/"+tc.Name]
			for _, path := range sortedKeys(mergeInto(mergeInto(nil, base.files), head.files)) {
				oldText, inBase := base.files[path]
				newText, inHead := head.files[path]
				if f, changed := diffFile(chart, tc.Name, path, oldText, newText, inBase, inHead); changed {
					report.Files = append(report.Files, f)
				}
			}
			report.ValueChanges = append(report.ValueChanges, valueChanges(chart, tc.Name, base.values, head.values)...)
		}
	}
	report.DefaultValuesDiff = defaultValuesDiff(defaults[baseName], defaults[headName])
	return report, nil
}

// resolve finds the application that installs each chart in a revision for a test case.
func (d *differ) resolve(dir string, tc TestCase, charts []string) (map[string]*overlay.App, error) {
	byChart := map[string]*overlay.App{}
	if tc.TargetType == "" {
		for _, chart := range charts {
			app := overlay.App{Name: chart, Chart: chart, Dir: ChartsDir + "/" + chart, Sets: tc.Sets}
			for _, f := range tc.Files {
				data, err := os.ReadFile(filepath.Join(dir, ChartsDir, chart, f))
				if os.IsNotExist(err) {
					continue
				}
				if err != nil {
					return nil, err
				}
				app.Layers = append(app.Layers, engine.Layer{Name: ChartsDir + "/" + chart + "/" + f, Data: data})
			}
			byChart[chart] = &app
		}
		return byChart, nil
	}
	types, err := targetchart.TargetTypes(dir)
	if err != nil || !slices.Contains(types, tc.TargetType) {
		return byChart, err // the target type does not exist in this revision: it installs nothing
	}
	cfg, err := targetchart.LoadConfig(dir, d.opts.CustomerConfig)
	if err != nil {
		return nil, err
	}
	o, err := targetchart.Load(targetchart.Options{Repo: dir, TargetType: tc.TargetType, Config: cfg})
	if err != nil {
		return nil, err
	}
	for i := range o.Apps {
		app := &o.Apps[i]
		if chart, ok := cutChartDir(app.Dir); ok && slices.Contains(charts, chart) {
			byChart[chart] = app
		}
	}
	return byChart, nil
}

// cutChartDir returns the chart of an application's directory when it is one of the repository's charts.
func cutChartDir(dir string) (string, bool) {
	chart, ok := strings.CutPrefix(dir, ChartsDir+"/")
	return chart, ok && chart != "" && !strings.Contains(chart, "/")
}

func (d *differ) render(ctx context.Context, rev, chartDir, chart string, tc TestCase, app *overlay.App) (rendering, error) {
	ch, err := d.renderer.load(ctx, rev, chartDir)
	if err != nil {
		return rendering{}, err
	}
	vals, err := overlay.HelmValues(app.Layers, app.Sets)
	if err != nil {
		return rendering{}, err
	}
	files, err := render(ch, "sx-"+chart, vals)
	if err != nil {
		return rendering{}, err
	}
	values, err := (&overlay.Overlay{TargetType: tc.Name}).Merge(*app, nil)
	if err != nil {
		return rendering{}, err
	}
	return rendering{files: files, values: values}, nil
}

// defaultValuesDiff diffs the subcharts' default values of both revisions, once for all test cases.
func defaultValuesDiff(base, head map[string]string) string {
	var out string
	for _, name := range sortedKeys(mergeInto(mergeInto(nil, base), head)) {
		if base[name] == head[name] {
			continue
		}
		text, _, _ := unified(baseName+"/"+name, headName+"/"+name, base[name], head[name])
		out += fmt.Sprintf("diff -U %d -rN %s/%s %s/%s\n%s", contextLines, baseName, name, headName, name, text)
	}
	return out
}

func mergeInto(dst, src map[string]string) map[string]string {
	if dst == nil {
		dst = map[string]string{}
	}
	for k, v := range src {
		dst[k] = v
	}
	return dst
}
