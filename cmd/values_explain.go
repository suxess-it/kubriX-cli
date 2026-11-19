package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/suxess-it/kubrix-cli/internal/values/engine"
	"github.com/suxess-it/kubrix-cli/internal/values/output"
)

type explainJSON struct {
	Application     string                `json:"application"`
	Chart           string                `json:"chart"`
	Path            string                `json:"path"`
	EffectiveValue  any                   `json:"effectiveValue"`
	EffectiveSource engine.SourceLocation `json:"effectiveSource"`
	History         []engine.Assignment   `json:"history"`
}

func newValuesExplainCmd() *cobra.Command {
	var f valuesFlags
	var format string
	cmd := &cobra.Command{
		Use:     "explain PATH",
		Short:   "Explain a value: its effective value and every assignment, in order",
		Args:    cobra.ExactArgs(1),
		PreRunE: func(*cobra.Command, []string) error { return enumFlag("format", &format, "text", "json") },
		RunE: func(cmd *cobra.Command, args []string) error {
			r, _, err := f.merge(cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			path := args[0]
			rec, ok := r.Records[path]
			if !ok {
				return fmt.Errorf("path %q does not exist in application %q", path, r.Application)
			}
			if format == "json" {
				return writeJSON(cmd.OutOrStdout(), explainJSON{r.Application, r.Chart, path, rec.EffectiveValue, rec.EffectiveSource, rec.History})
			}
			w := newTable(cmd.OutOrStdout())
			_, _ = fmt.Fprintf(w, "Application:\t%s\nChart:\t%s\nPath:\t%s\nEffective value:\t%s\n\nHistory:\n", r.Application, r.Chart, path, output.Scalar(rec.EffectiveValue))
			for i, a := range rec.History {
				status := string(a.Action)
				if a.Effective {
					status = "effective"
				}
				_, _ = fmt.Fprintf(w, "  %d\t%s\t%s\t%s\n", i+1, source(a.Source), output.Scalar(a.Value), status)
			}
			return w.Flush()
		},
	}
	f.addApp(cmd)
	cmd.Flags().StringVar(&format, "format", "text", "output format: text|json")
	return cmd
}
