package cmd

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/suxess-it/kubrix-cli/internal/values/engine"
	"github.com/suxess-it/kubrix-cli/internal/values/output"
)

type searchRow struct {
	Application     string                `json:"application"`
	Chart           string                `json:"chart"`
	Path            string                `json:"path"`
	EffectiveValue  any                   `json:"effectiveValue"`
	EffectiveSource engine.SourceLocation `json:"effectiveSource"`
	rank            int
}

func newValuesSearchCmd() *cobra.Command {
	var f valuesFlags
	var format string
	cmd := &cobra.Command{
		Use:     "search QUERY",
		Short:   "Search value paths across all applications of a target type",
		Long:    "Search value paths across all applications of a target type. Exact matches come first, then paths ending in the query, then paths containing it.",
		Args:    cobra.ExactArgs(1),
		PreRunE: func(*cobra.Command, []string) error { return enumFlag("format", &format, "text", "json") },
		RunE: func(cmd *cobra.Command, args []string) error {
			o, err := f.overlay(cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			rows := []searchRow{}
			for _, app := range o.Apps {
				r, err := o.Merge(app, engine.SetSources(f.sets))
				if err != nil {
					return err
				}
				rows = appendSearchRows(rows, r, args[0])
			}
			sortSearchRows(rows)
			if format == "json" {
				return writeJSON(cmd.OutOrStdout(), rows)
			}
			w := newTable(cmd.OutOrStdout())
			_, _ = fmt.Fprintln(w, "APPLICATION\tCHART\tPATH\tEFFECTIVE VALUE\tEFFECTIVE SOURCE")
			for _, row := range rows {
				_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", row.Application, row.Chart, row.Path, output.Scalar(row.EffectiveValue), source(row.EffectiveSource))
			}
			return w.Flush()
		},
	}
	f.addSource(cmd)
	cmd.Flags().StringVar(&format, "format", "text", "output format: text|json")
	return cmd
}

func appendSearchRows(rows []searchRow, r *engine.Result, query string) []searchRow {
	for _, path := range engine.SortedPaths(r.Records) {
		if rank := matchRank(path, query); rank > 0 {
			rec := r.Records[path]
			rows = append(rows, searchRow{r.Application, r.Chart, path, rec.EffectiveValue, rec.EffectiveSource, rank})
		}
	}
	return rows
}

func sortSearchRows(rows []searchRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].rank != rows[j].rank {
			return rows[i].rank > rows[j].rank
		}
		if rows[i].Application != rows[j].Application {
			return rows[i].Application < rows[j].Application
		}
		return rows[i].Path < rows[j].Path
	})
}

func matchRank(path, query string) int {
	switch {
	case path == query:
		return 3
	case strings.HasSuffix(path, "."+query):
		return 2
	case strings.Contains(path, query):
		return 1
	}
	return 0
}
