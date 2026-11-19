package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/suxess-it/kubrix-cli/internal/values/overlay"
)

// appJSON is the contract kubrix diff and CI rely on: each application's values files and parameters in merge order.
type appJSON struct {
	Name       string    `json:"name"`
	Chart      string    `json:"chart"`
	ValueFiles []string  `json:"valueFiles"`
	Sets       []setJSON `json:"sets"`
}

type setJSON struct {
	Expression string `json:"expression"`
	String     bool   `json:"string,omitempty"`
	Source     string `json:"source"`
}

func newValuesAppsCmd() *cobra.Command {
	var f valuesFlags
	var format string
	cmd := &cobra.Command{
		Use:     "apps",
		Short:   "List a target type's applications and their values files",
		Args:    cobra.NoArgs,
		PreRunE: func(*cobra.Command, []string) error { return enumFlag("format", &format, "text", "json") },
		RunE: func(cmd *cobra.Command, _ []string) error {
			o, err := f.overlay(cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			if format == "json" {
				apps := make([]appJSON, len(o.Apps))
				for i, a := range o.Apps {
					apps[i] = toAppJSON(a)
				}
				return writeJSON(cmd.OutOrStdout(), apps)
			}
			w := newTable(cmd.OutOrStdout())
			_, _ = fmt.Fprintln(w, "APPLICATION\tCHART\tFILES")
			for _, a := range o.Apps {
				_, _ = fmt.Fprintf(w, "%s\t%s\t%d\n", a.Name, a.Chart, len(a.Layers))
			}
			return w.Flush()
		},
	}
	f.addSource(cmd)
	cmd.Flags().StringVar(&format, "format", "text", "output format: text|json")
	return cmd
}

func toAppJSON(a overlay.App) appJSON {
	out := appJSON{Name: a.Name, Chart: a.Chart, ValueFiles: a.LayerNames(), Sets: []setJSON{}}
	for _, s := range a.Sets {
		out.Sets = append(out.Sets, setJSON{Expression: s.Expression, String: s.StringValues, Source: source(s.Source)})
	}
	return out
}
