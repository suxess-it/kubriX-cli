package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/suxess-it/kubrix-cli/internal/values/engine"
	"github.com/suxess-it/kubrix-cli/internal/values/output"
)

type overrideJSON struct {
	Path            string                `json:"path"`
	Assignments     int                   `json:"assignments"`
	EffectiveValue  any                   `json:"effectiveValue"`
	EffectiveSource engine.SourceLocation `json:"effectiveSource"`
}

func newValuesOverridesCmd() *cobra.Command {
	var f valuesFlags
	var format string
	cmd := &cobra.Command{
		Use:     "overrides",
		Short:   "List the values that more than one file or --set assigns",
		Args:    cobra.NoArgs,
		PreRunE: func(*cobra.Command, []string) error { return enumFlag("format", &format, "text", "json") },
		RunE: func(cmd *cobra.Command, _ []string) error {
			r, _, err := f.merge(cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			rows := []overrideJSON{}
			for _, p := range engine.Overrides(r) {
				rec := r.Records[p]
				rows = append(rows, overrideJSON{p, len(rec.History), rec.EffectiveValue, rec.EffectiveSource})
			}
			if format == "json" {
				return writeJSON(cmd.OutOrStdout(), rows)
			}
			w := newTable(cmd.OutOrStdout())
			_, _ = fmt.Fprintln(w, "PATH\tASSIGNMENTS\tEFFECTIVE VALUE\tEFFECTIVE SOURCE")
			for _, x := range rows {
				_, _ = fmt.Fprintf(w, "%s\t%d\t%s\t%s\n", x.Path, x.Assignments, output.Scalar(x.EffectiveValue), source(x.EffectiveSource))
			}
			return w.Flush()
		},
	}
	f.addApp(cmd)
	cmd.Flags().StringVar(&format, "format", "text", "output format: text|json")
	return cmd
}
