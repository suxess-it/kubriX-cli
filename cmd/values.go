package cmd

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/suxess-it/kubrix-cli/internal/values/engine"
	"github.com/suxess-it/kubrix-cli/internal/values/output"
	"github.com/suxess-it/kubrix-cli/internal/values/overlay"
	"github.com/suxess-it/kubrix-cli/internal/values/projectconfig"
	"github.com/suxess-it/kubrix-cli/internal/values/targetchart"
)

func newValuesCmd() *cobra.Command {
	values := &cobra.Command{
		Use:   "values",
		Short: "Explain how a target type's Helm values overlays combine",
		Long: `Explain how the ordered Helm values files of an application combine: the final values, which file and line set
each value, every earlier assignment, paths across applications, and suspicious overlays.

The values files come from, in this order of preference:
  --target-values FILE        a target values file of the target chart
  --file FILE ... / --set ... files and expressions given on the command line
  --target-type TYPE          .kubrix-values.yaml in --repo if it exists, otherwise the kubriX target chart:
                              bootstrap-app-TYPE.yaml lists its values files, and the rendered Argo CD
                              Applications list each application's values files and parameters`,
		Args: cobra.NoArgs,
	}
	values.AddCommand(newValuesMergeCmd(), newValuesExplainCmd(), newValuesOverridesCmd(), newValuesLintCmd(), newValuesAppsCmd(), newValuesSearchCmd())
	return values
}

// valuesFlags select the values overlay of a target type, or the files and expressions given on the command line.
type valuesFlags struct {
	repo, targetType, targetValues, customerConfig, valuesConfig string
	app, chart                                                   string
	files, sets                                                  []string
}

func (f *valuesFlags) addSource(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.repo, "repo", ".", "platform repository root")
	cmd.Flags().StringVar(&f.targetType, "target-type", "", "target type, e.g. kubrix-oss-stack")
	cmd.Flags().StringVar(&f.targetValues, "target-values", "", "target values file of the target chart, used instead of --target-type")
	cmd.Flags().StringVar(&f.customerConfig, "customer-config", "", "customer config to render *.yaml.tmpl with (default: bootstrap/customer-config.yaml, or a plain k8s cluster)")
	cmd.Flags().StringVar(&f.valuesConfig, "values-config", "", "project config listing values files per target type (default: .kubrix-values.yaml in --repo)")
	cmd.Flags().StringArrayVar(&f.sets, "set", nil, "Helm key=value applied after all values files (repeatable)")
}

func (f *valuesFlags) addApp(cmd *cobra.Command) {
	f.addSource(cmd)
	cmd.Flags().StringVar(&f.app, "app", "", "application name")
	cmd.Flags().StringVar(&f.chart, "chart", "", "chart name with --file (default: the application name)")
	cmd.Flags().StringArrayVar(&f.files, "file", nil, "values file in precedence order, instead of a target type (repeatable)")
}

// overlay resolves the selected values overlay and prints what was skipped while resolving it.
func (f *valuesFlags) overlay(stderr io.Writer) (*overlay.Overlay, error) {
	o, err := f.resolve()
	if err != nil {
		return nil, err
	}
	for _, w := range o.Warnings {
		_, _ = fmt.Fprintln(stderr, "warning:", w)
	}
	return o, nil
}

func (f *valuesFlags) resolve() (*overlay.Overlay, error) {
	switch {
	case f.targetValues != "":
		if len(f.files) != 0 {
			return nil, errors.New("--target-values and --file cannot be used together")
		}
		cfg, err := f.config(filepath.Dir(filepath.Dir(filepath.Dir(f.targetValues))))
		if err != nil {
			return nil, err
		}
		return targetchart.Load(targetchart.Options{TargetValues: f.targetValues, TargetType: f.targetType, Config: cfg})
	case len(f.files) != 0 || f.targetType == "" && len(f.sets) != 0 && f.app != "":
		return f.explicit()
	case f.targetType == "":
		return nil, errors.New("--target-type, --target-values or --file is required")
	}
	path, explicit := f.valuesConfig, f.valuesConfig != ""
	if !explicit {
		path = filepath.Join(f.repo, projectconfig.DefaultPath)
	}
	if _, err := os.Stat(path); err == nil || explicit {
		c, err := projectconfig.Load(path)
		if err != nil {
			return nil, err
		}
		return c.Overlay(f.targetType)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	cfg, err := f.config(f.repo)
	if err != nil {
		return nil, err
	}
	return targetchart.Load(targetchart.Options{Repo: f.repo, TargetType: f.targetType, Config: cfg})
}

func (f *valuesFlags) config(repo string) (map[string]any, error) {
	return targetchart.LoadConfig(repo, f.customerConfig)
}

// explicit is the one application made of the --file arguments; the commands that take --file require --app.
func (f *valuesFlags) explicit() (*overlay.Overlay, error) {
	layers, err := engine.ReadLayers(f.files)
	if err != nil {
		return nil, err
	}
	chart := f.chart
	if chart == "" {
		chart = f.app
	}
	return &overlay.Overlay{TargetType: f.targetType, Apps: []overlay.App{{Name: f.app, Chart: chart, Layers: layers}}}, nil
}

// merge merges the selected application with the user's --set expressions.
func (f *valuesFlags) merge(stderr io.Writer) (*engine.Result, overlay.App, error) {
	if f.app == "" {
		return nil, overlay.App{}, errors.New("--app is required")
	}
	o, err := f.overlay(stderr)
	if err != nil {
		return nil, overlay.App{}, err
	}
	app, err := o.App(f.app)
	if err != nil {
		return nil, overlay.App{}, err
	}
	r, err := o.Merge(app, engine.SetSources(f.sets))
	return r, app, err
}

// enumFlag validates a flag that takes one of a few values.
func enumFlag(name string, value *string, valid ...string) error {
	for _, v := range valid {
		if *value == v {
			return nil
		}
	}
	return fmt.Errorf("invalid --%s %q (expected one of %v)", name, *value, valid)
}

func writeJSON(w io.Writer, v any) error {
	b, err := output.JSON(v)
	if err == nil {
		_, err = w.Write(b)
	}
	return err
}

func newTable(w io.Writer) *tabwriter.Writer { return tabwriter.NewWriter(w, 0, 4, 2, ' ', 0) }

func source(s engine.SourceLocation) string { return fmt.Sprintf("%s:%d", s.File, s.Line) }
