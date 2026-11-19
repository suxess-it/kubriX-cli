package gitopsdiff

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/suxess-it/kubrix-cli/internal/values/output"
)

// WriteText prints the report for a terminal: what changed per chart and test case, the changed values with the
// file that sets them, and the diffs.
func WriteText(w io.Writer, r *Report) {
	if r.Empty() {
		_, _ = fmt.Fprintln(w, "No changes in the rendered manifests, the default values or the effective values.")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	if len(r.Failures) > 0 {
		_, _ = fmt.Fprintln(tw, "FAILED\tSTATUS\tERROR")
		for _, f := range r.Failures {
			msg, _, _ := strings.Cut(f.Error, "\n")
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", FailureSubject(f), FailureLabel(f.Status), msg)
		}
		_, _ = fmt.Fprintln(tw)
	}
	if len(r.Files) > 0 {
		_, _ = fmt.Fprintln(tw, "CHART\tTEST CASE\tFILES\tLINES")
		for _, group := range groups(r.Files, byChartAndTestCase) {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%d (%s)\t%s\n", group[0].Chart, group[0].TestCase, len(group), describe(group), describeLines(group))
		}
		_, _ = fmt.Fprintln(tw)
	}
	if len(r.ValueChanges) > 0 {
		_, _ = fmt.Fprintln(tw, "CHART\tTEST CASE\tPATH\tBEFORE\tAFTER\tSET BY")
		for _, c := range r.ValueChanges {
			before, after, source := output.Scalar(c.Before), output.Scalar(c.After), fmt.Sprintf("%s:%d", c.Source.File, c.Source.Line)
			switch c.Kind {
			case Added:
				before = "-"
			case Removed:
				after, source = "-", "-"
			}
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", c.Chart, c.TestCase, c.Path, before, after, source)
		}
		_, _ = fmt.Fprintln(tw)
	}
	_ = tw.Flush()
	for _, f := range r.Files {
		_, _ = fmt.Fprint(w, f.Text)
	}
	_, _ = fmt.Fprint(w, r.DefaultValuesDiff)
}
