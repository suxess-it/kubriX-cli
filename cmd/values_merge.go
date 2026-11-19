package cmd

import (
	"github.com/spf13/cobra"

	"github.com/suxess-it/kubrix-cli/internal/values/output"
)

func newValuesMergeCmd() *cobra.Command {
	var f valuesFlags
	var format string
	var sources bool
	cmd := &cobra.Command{
		Use:     "merge",
		Short:   "Print an application's final merged values",
		Args:    cobra.NoArgs,
		PreRunE: func(*cobra.Command, []string) error { return enumFlag("format", &format, "yaml", "json") },
		RunE: func(cmd *cobra.Command, _ []string) error {
			r, _, err := f.merge(cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			if format == "json" {
				return writeJSON(cmd.OutOrStdout(), r.Values)
			}
			b, err := output.YAML(r, sources)
			if err == nil {
				_, err = cmd.OutOrStdout().Write(b)
			}
			return err
		},
	}
	f.addApp(cmd)
	cmd.Flags().StringVar(&format, "format", "yaml", "output format: yaml|json")
	cmd.Flags().BoolVar(&sources, "show-sources", false, "annotate each YAML value with the file and line that set it")
	return cmd
}
