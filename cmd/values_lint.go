package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/suxess-it/kubrix-cli/internal/values/lint"
)

func newValuesLintCmd() *cobra.Command {
	var f valuesFlags
	var format, failOn string
	cmd := &cobra.Command{
		Use:   "lint",
		Short: "Report suspicious or redundant values overlays",
		Long:  "Report suspicious or redundant values overlays. With --fail-on, exit with code 2 when a finding reaches that severity.",
		Args:  cobra.NoArgs,
		PreRunE: func(*cobra.Command, []string) error {
			if err := enumFlag("format", &format, "text", "json"); err != nil {
				return err
			}
			return enumFlag("fail-on", &failOn, "info", "warning", "error", "never")
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			r, app, err := f.merge(cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			findings := lint.Analyze(r, app.LayerNames())
			if format == "json" {
				if findings == nil {
					findings = []lint.Finding{}
				}
				err = writeJSON(cmd.OutOrStdout(), findings)
			} else {
				w := newTable(cmd.OutOrStdout())
				_, _ = fmt.Fprintln(w, "SEVERITY\tRULE\tPATH\tSOURCE\tMESSAGE")
				for _, x := range findings {
					_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", x.Severity, x.Rule, x.Path, source(x.Source), x.Message)
				}
				err = w.Flush()
			}
			if err != nil {
				return err
			}
			if lint.Reaches(findings, failOn) {
				return &exitError{code: 2, err: fmt.Errorf("lint findings reached --fail-on %s", failOn)}
			}
			return nil
		},
	}
	f.addApp(cmd)
	cmd.Flags().StringVar(&format, "format", "text", "output format: text|json")
	cmd.Flags().StringVar(&failOn, "fail-on", "never", "exit with code 2 at this severity: info|warning|error|never")
	return cmd
}
