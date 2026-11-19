package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/suxess-it/kubrix-cli/internal/git"
	"github.com/suxess-it/kubrix-cli/internal/gitinfo"
	"github.com/suxess-it/kubrix-cli/internal/gitopsdiff"
	"github.com/suxess-it/kubrix-cli/internal/ui"
	"github.com/suxess-it/kubrix-cli/internal/values/engine"
)

// diffServices are what a diff reaches outside the repository: chart repositories and GitHub.
type diffServices struct {
	// dependencies fetches chart dependencies; nil uses helm's.
	dependencies gitopsdiff.DependencyBuilder
	// comments connects to GitHub for --pr and returns the login whose comments are the diff's own.
	comments func(ctx context.Context) (gitopsdiff.CommentClient, string, error)
	// origin is the GitHub repository (owner/name) of the platform repository in dir.
	origin func(dir string) (string, error)
}

func productionDiffServices() diffServices {
	return diffServices{
		comments: func(ctx context.Context) (gitopsdiff.CommentClient, string, error) {
			token := os.Getenv("GITHUB_TOKEN")
			if token == "" {
				var err error
				if token, err = git.GitHubTokenFromCLI(); err != nil {
					return nil, "", fmt.Errorf("--pr needs GITHUB_TOKEN or a logged-in gh CLI: %w", err)
				}
			}
			gh, err := git.NewGitHub(token)
			if err != nil {
				return nil, "", err
			}
			// GitHub Actions' GITHUB_TOKEN may not read the user; its comments are always github-actions[bot]'s.
			if os.Getenv("GITHUB_ACTIONS") == "true" {
				return gh, "github-actions[bot]", nil
			}
			login, err := gh.Login(ctx)
			if err != nil {
				return nil, "", fmt.Errorf("find the GitHub login of the token: %w", err)
			}
			return gh, login, nil
		},
		origin: func(dir string) (string, error) {
			info, err := gitinfo.Detect(dir)
			if err != nil {
				return "", err
			}
			return info.Origin.Slug(), nil
		},
	}
}

type diffFlags struct {
	repo, base, head, charts, customerConfig, format, outputDir, githubRepo, runURL, author string
	targetTypes, testCases                                                                  []string
	pr, parallel                                                                            int
	dryRun                                                                                  bool
}

func newDiffCmd() *cobra.Command {
	var f diffFlags
	cmd := &cobra.Command{
		Use:   "diff [--base REF]",
		Short: "Show how a change alters the rendered charts and their values",
		Long: `Render the charts a change touches on the base revision and on the change, with every test case, and show what
differs: the rendered manifests, the subcharts' default values, and the effective values with the file and line
that set them.

In a terminal the result opens in a viewer to browse charts, test cases and files, after asking for the base when
--base is not given. --plain, --format, --output-dir and --pr print instead.

A test case is a target type (--target-type, by default every one in either revision), whose Argo CD Applications
give each chart its values files and parameters, or a hand-made set of chart-local values files and helm arguments:
  --test-case 'default-kind=values-kubrix-default.yaml,values-cluster-kind.yaml;--set a=b --set-string c=d'

With --pr the result is posted to that GitHub pull request as a summary comment and one comment per changed chart;
a later run updates or deletes them. Comments of other authors and without a gitops-diff marker are never touched.`,
		Args: cobra.NoArgs,
		PreRunE: func(*cobra.Command, []string) error {
			if f.pr != 0 {
				f.format = "markdown"
			}
			return enumFlag("format", &f.format, "text", "markdown", "json")
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			plain, _ := cmd.Flags().GetBool("plain")
			if diffInteractive(f, plain || cmd.Flags().Changed("format"), isInteractive()) {
				return runDiffViewer(cmd.Context(), f, productionDiffServices())
			}
			if f.base == "" {
				return errors.New("--base is required when the output is not a terminal")
			}
			return runDiff(cmd.Context(), f, productionDiffServices(), cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.repo, "repo", ".", "platform repository root (a git checkout)")
	fl.StringVar(&f.base, "base", "", "revision to compare against, e.g. origin/main (asked for in a terminal)")
	fl.StringVar(&f.head, "head", "", "revision to compare (default: the working tree)")
	fl.StringArrayVar(&f.targetTypes, "target-type", nil, "target type to render with (repeatable; default: every target type)")
	fl.StringArrayVar(&f.testCases, "test-case", nil, "hand-made test case NAME=FILE,FILE[;HELM ARGS] (repeatable)")
	fl.StringVar(&f.charts, "charts", "changed", "charts to compare: changed, all, or a comma-separated list")
	fl.StringVar(&f.customerConfig, "customer-config", "", "customer config to render *.yaml.tmpl with (default: each revision's bootstrap/customer-config.yaml)")
	fl.StringVar(&f.format, "format", "text", "output format: text|markdown|json")
	fl.StringVar(&f.outputDir, "output-dir", "", "write the Markdown comments and the full diff into this directory")
	fl.IntVar(&f.pr, "pr", 0, "post the result as comments on this GitHub pull request")
	fl.StringVar(&f.githubRepo, "github-repo", "", "GitHub repository owner/name of --pr (default: the repository's origin)")
	fl.StringVar(&f.author, "comment-author", "", "login whose comments are the diff's own (default: the token's, github-actions[bot] in GitHub Actions)")
	fl.StringVar(&f.runURL, "run-url", "", "link the comments point to for the full diff (default: the GitHub Actions run, if any)")
	fl.BoolVar(&f.dryRun, "dry-run", false, "with --pr, only print which comments would be created, updated or deleted")
	fl.IntVar(&f.parallel, "parallel", 0, "charts rendered at once (default: number of CPUs)")
	return cmd
}

// diffInteractive says whether diff opens the viewer: in a terminal, unless the result is meant for a program, files
// or a pull request, or plain output was asked for (explicitly as --plain or by naming a --format).
func diffInteractive(f diffFlags, plain, terminal bool) bool {
	return terminal && !plain && f.outputDir == "" && f.pr == 0
}

// runDiffViewer browses the diff full-screen, asking for the base first when --base is not given.
func runDiffViewer(ctx context.Context, f diffFlags, svc diffServices) error {
	opts, err := diffOptions(f, svc)
	if err != nil {
		return err
	}
	bases, err := gitopsdiff.BaseCandidates(ctx, f.repo)
	if err != nil {
		return err
	}
	head := f.head
	if head == "" {
		head = "working tree"
	}
	var warnings bytes.Buffer
	report, err := ui.RunDiffViewer(ctx, ui.DiffSession{
		Base:  f.base,
		Bases: bases,
		Head:  head,
		Choices: func(ctx context.Context, base string) (ui.DiffChoices, error) {
			c, err := gitopsdiff.ListChoices(ctx, f.repo, base, f.head)
			if err != nil {
				return ui.DiffChoices{}, err
			}
			return diffChoices(opts, c), nil
		},
		Run: func(ctx context.Context, sel ui.DiffSelection, progress func(gitopsdiff.Progress)) (*gitopsdiff.Report, error) {
			o := selectedOptions(opts, sel)
			o.Progress = progress
			// Helm's warnings would scribble over the full-screen view; they are shown in it instead.
			warnings.Reset()
			restore := gitopsdiff.CaptureHelmWarnings(&warnings)
			defer restore()
			return gitopsdiff.Run(ctx, o)
		},
		Warnings: func() string { return strings.TrimSpace(warnings.String()) },
	})
	if err != nil || report == nil {
		return err
	}
	return brokenError(report)
}

// diffChoices offers every target type and chart, plus the hand-made test cases of the flags. Selected are the
// test cases and charts the flags name, or by default every target type and the charts the change touches.
func diffChoices(opts gitopsdiff.Options, c gitopsdiff.Choices) ui.DiffChoices {
	var out ui.DiffChoices
	named := map[string]bool{}
	for _, tc := range opts.TestCases {
		named[tc.Name] = true
		out.TestCases = append(out.TestCases, ui.Option{Label: tc.Name, Value: tc.Name, Selected: true})
	}
	for _, t := range c.TargetTypes {
		if !named[t] {
			out.TestCases = append(out.TestCases, ui.Option{Label: t, Value: t, Selected: len(opts.TestCases) == 0})
		}
	}
	selected := c.Changed
	if len(opts.Charts) != 0 {
		selected = opts.Charts
	}
	for _, chart := range c.Charts {
		label := chart
		if slices.Contains(c.Changed, chart) && len(c.Changed) < len(c.Charts) {
			label += " (changed)"
		}
		out.Charts = append(out.Charts, ui.Option{Label: label, Value: chart, Selected: opts.AllCharts || slices.Contains(selected, chart)})
	}
	return out
}

// selectedOptions narrows the options to a selection: a selected name is the flags' hand-made test case of that
// name, or else a target type.
func selectedOptions(opts gitopsdiff.Options, sel ui.DiffSelection) gitopsdiff.Options {
	o := opts
	o.Base = sel.Base
	if len(sel.TestCases) != 0 {
		o.TestCases = nil
		for _, name := range sel.TestCases {
			i := slices.IndexFunc(opts.TestCases, func(tc gitopsdiff.TestCase) bool { return tc.Name == name })
			if i >= 0 {
				o.TestCases = append(o.TestCases, opts.TestCases[i])
			} else {
				o.TestCases = append(o.TestCases, gitopsdiff.TestCase{Name: name, TargetType: name})
			}
		}
	}
	if len(sel.Charts) != 0 {
		o.Charts, o.AllCharts = sel.Charts, false
	}
	return o
}

// diffOptions turns the flags into the options of a diff.
func diffOptions(f diffFlags, svc diffServices) (gitopsdiff.Options, error) {
	opts := gitopsdiff.Options{Repo: f.repo, Base: f.base, Head: f.head, CustomerConfig: f.customerConfig, Dependencies: svc.dependencies, Parallel: f.parallel}
	switch f.charts {
	case "changed":
	case "all":
		opts.AllCharts = true
	default:
		opts.Charts = strings.Split(f.charts, ",")
	}
	for _, t := range f.targetTypes {
		opts.TestCases = append(opts.TestCases, gitopsdiff.TestCase{Name: t, TargetType: t})
	}
	for _, s := range f.testCases {
		tc, err := parseTestCase(s)
		if err != nil {
			return gitopsdiff.Options{}, err
		}
		opts.TestCases = append(opts.TestCases, tc)
	}
	return opts, nil
}

func runDiff(ctx context.Context, f diffFlags, svc diffServices, stdout, stderr io.Writer) error {
	opts, err := diffOptions(f, svc)
	if err != nil {
		return err
	}
	restore := gitopsdiff.CaptureHelmWarnings(stderr)
	report, err := gitopsdiff.Run(ctx, opts)
	restore()
	if err != nil {
		return err
	}

	runURL := f.runURL
	if runURL == "" && os.Getenv("GITHUB_RUN_ID") != "" {
		runURL = os.Getenv("GITHUB_SERVER_URL") + "/" + os.Getenv("GITHUB_REPOSITORY") + "/actions/runs/" + os.Getenv("GITHUB_RUN_ID")
	}
	comments := gitopsdiff.Comments(report, runURL)
	if f.outputDir != "" {
		if err := writeDiffFiles(f.outputDir, report, comments); err != nil {
			return err
		}
	}
	switch f.format {
	case "json":
		if err := writeJSON(stdout, report); err != nil {
			return err
		}
	case "markdown":
		if f.outputDir == "" && f.pr == 0 {
			for _, c := range comments {
				_, _ = fmt.Fprintln(stdout, c.Body)
			}
		}
	default:
		gitopsdiff.WriteText(stdout, report)
	}
	if f.pr != 0 {
		if err := postComments(ctx, f, svc, comments, stderr); err != nil {
			return err
		}
	}
	return brokenError(report)
}

// brokenError makes the command exit with code 2 when the change breaks rendering, after the result is written.
func brokenError(report *gitopsdiff.Report) error {
	broken := report.Broken()
	if len(broken) == 0 {
		return nil
	}
	subjects := make([]string, len(broken))
	for i, f := range broken {
		subjects[i] = gitopsdiff.FailureSubject(f)
	}
	return &exitError{code: 2, err: fmt.Errorf("the change breaks rendering of %s", strings.Join(subjects, ", "))}
}

func postComments(ctx context.Context, f diffFlags, svc diffServices, comments []gitopsdiff.Comment, log io.Writer) error {
	slug := f.githubRepo
	if slug == "" {
		var err error
		if slug, err = svc.origin(f.repo); err != nil {
			return fmt.Errorf("find the GitHub repository of --pr (set --github-repo): %w", err)
		}
	}
	owner, name, ok := strings.Cut(slug, "/")
	if !ok || owner == "" || name == "" {
		return fmt.Errorf("invalid --github-repo %q (expected owner/name)", slug)
	}
	client, author, err := svc.comments(ctx)
	if err != nil {
		return err
	}
	if f.author != "" {
		author = f.author
	}
	pr := gitopsdiff.PullRequest{Owner: owner, Repo: name, Number: f.pr, Author: author}
	return gitopsdiff.SyncComments(ctx, client, pr, comments, f.dryRun, log)
}

// writeDiffFiles writes each comment as NAME.md and the complete diff, which comments may truncate, as full.diff.
func writeDiffFiles(dir string, report *gitopsdiff.Report, comments []gitopsdiff.Comment) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, c := range comments {
		if err := os.WriteFile(filepath.Join(dir, c.Name+".md"), []byte(c.Body), 0o644); err != nil {
			return err
		}
	}
	var full strings.Builder
	for _, fd := range report.Files {
		full.WriteString(fd.Text)
	}
	full.WriteString(report.DefaultValuesDiff)
	return os.WriteFile(filepath.Join(dir, "full.diff"), []byte(full.String()), 0o644)
}

// parseTestCase parses NAME=FILE,FILE[;HELM ARGS], where HELM ARGS are --set and --set-string flags with their values.
func parseTestCase(s string) (gitopsdiff.TestCase, error) {
	spec, helmArgs, _ := strings.Cut(s, ";")
	name, files, ok := strings.Cut(spec, "=")
	if !ok || name == "" || files == "" {
		return gitopsdiff.TestCase{}, fmt.Errorf("invalid --test-case %q (expected NAME=FILE,FILE[;HELM ARGS])", s)
	}
	tc := gitopsdiff.TestCase{Name: name, Files: strings.Split(files, ",")}
	args := strings.Fields(helmArgs)
	for i := 0; i < len(args); i++ {
		flagName, value, hasValue := strings.Cut(args[i], "=")
		if flagName != "--set" && flagName != "--set-string" {
			return gitopsdiff.TestCase{}, fmt.Errorf("--test-case %s: only --set and --set-string are supported, not %q", name, args[i])
		}
		if !hasValue {
			if i+1 == len(args) {
				return gitopsdiff.TestCase{}, fmt.Errorf("--test-case %s: %s needs a value", name, flagName)
			}
			i++
			value = args[i]
		}
		tc.Sets = append(tc.Sets, engine.SetSource{Expression: value, StringValues: flagName == "--set-string",
			Source: engine.SourceLocation{File: "--test-case " + name, Line: len(tc.Sets) + 1, Column: 1}})
	}
	return tc, nil
}
