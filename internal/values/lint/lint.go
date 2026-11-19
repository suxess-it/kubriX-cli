package lint

import (
	"fmt"
	"path/filepath"
	"sort"

	"github.com/suxess-it/kubrix-cli/internal/values/engine"
)

func Analyze(r *engine.Result, files []string) []Finding {
	var out []Finding
	seen := map[string]engine.SourceLocation{}
	for _, f := range files {
		clean := filepath.Clean(f)
		src := engine.SourceLocation{File: f}
		if prev, ok := seen[clean]; ok {
			p := prev
			out = append(out, Finding{"duplicate-file", "warning", r.Application, "", fmt.Sprintf("file %s appears more than once", f), src, &p})
		} else {
			seen[clean] = src
		}
	}
	for _, fr := range r.Files {
		if !fr.Changed {
			out = append(out, Finding{"no-op-file", "info", r.Application, "", fmt.Sprintf("file %s makes no effective change", fr.File), engine.SourceLocation{File: fr.File}, nil})
		}
	}
	for _, path := range engine.SortedPaths(r.Records) {
		h := r.Records[path].History
		for i, a := range h {
			if i == 0 {
				continue
			}
			prev := h[i-1].Source
			switch a.Action {
			case engine.ActionIdenticalRedefinition:
				// A map is repeated whenever a later file touches any of its keys; its leaves report the repetition.
				if a.Kind == engine.KindMap {
					continue
				}
				out = append(out, Finding{"identical-redefinition", "info", r.Application, path, "later assignment repeats the already effective value", a.Source, &prev})
			case engine.ActionTypeChanged:
				out = append(out, Finding{"type-change", "warning", r.Application, path, fmt.Sprintf("value kind changed from %s to %s", h[i-1].Kind, a.Kind), a.Source, &prev})
			}
			// An identical list is already reported as identical-redefinition; nothing is lost by replacing it.
			if a.Kind == engine.KindSequence && h[i-1].Kind == engine.KindSequence && a.Action != engine.ActionIdenticalRedefinition {
				out = append(out, Finding{"list-replacement", "warning", r.Application, path, "Helm values merging replaces the complete list; list items are not merged", a.Source, &prev})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Source.File != b.Source.File {
			return a.Source.File < b.Source.File
		}
		if a.Source.Line != b.Source.Line {
			return a.Source.Line < b.Source.Line
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return a.Rule < b.Rule
	})
	return out
}
func Reaches(findings []Finding, threshold string) bool {
	if threshold == "never" {
		return false
	}
	rank := map[string]int{"info": 1, "warning": 2, "error": 3}
	for _, f := range findings {
		if rank[f.Severity] >= rank[threshold] {
			return true
		}
	}
	return false
}
