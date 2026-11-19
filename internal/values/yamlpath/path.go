// Package yamlpath parses Helm values YAML while retaining source positions, and joins and splits the dotted paths
// that name a value.
package yamlpath

import (
	"fmt"
	"strings"
)

// JoinPath creates an unambiguous dotted path. Literal dots and backslashes in
// YAML keys are escaped with a backslash (for example, metadata\.name).
func JoinPath(parts ...string) string {
	escaped := make([]string, len(parts))
	for i, part := range parts {
		part = strings.ReplaceAll(part, `\`, `\\`)
		escaped[i] = strings.ReplaceAll(part, `.`, `\.`)
	}
	return strings.Join(escaped, ".")
}

// SplitPath is the inverse of JoinPath.
func SplitPath(path string) ([]string, error) {
	if path == "" {
		return nil, nil
	}
	var out []string
	var b strings.Builder
	escaped := false
	for _, r := range path {
		if escaped {
			b.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		if r == '.' {
			if b.Len() == 0 {
				return nil, fmt.Errorf("invalid empty path segment in %q", path)
			}
			out = append(out, b.String())
			b.Reset()
			continue
		}
		b.WriteRune(r)
	}
	if escaped {
		return nil, fmt.Errorf("invalid trailing escape in path %q", path)
	}
	if b.Len() == 0 {
		return nil, fmt.Errorf("invalid empty path segment in %q", path)
	}
	return append(out, b.String()), nil
}
