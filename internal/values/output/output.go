// Package output provides deterministic machine-readable and human renderers.
package output

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/suxess-it/kubrix-cli/internal/values/engine"
	"github.com/suxess-it/kubrix-cli/internal/values/yamlpath"
	"go.yaml.in/yaml/v3"
)

func JSON(v any) ([]byte, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
func YAML(r *engine.Result, showSources bool) ([]byte, error) {
	var n yaml.Node
	if err := n.Encode(r.Values); err != nil {
		return nil, err
	}
	root := &n
	if n.Kind == yaml.DocumentNode && len(n.Content) > 0 {
		root = n.Content[0]
	}
	if showSources {
		annotate(root, nil, r.Records)
	}
	sortNode(root)
	var b bytes.Buffer
	e := yaml.NewEncoder(&b)
	e.SetIndent(2)
	if err := e.Encode(root); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
func annotate(n *yaml.Node, prefix []string, recs map[string]*engine.ValueRecord) {
	if n.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		parts := append(append([]string{}, prefix...), k.Value)
		if v.Kind == yaml.MappingNode {
			annotate(v, parts, recs)
		} else if rec := recs[yamlpath.JoinPath(parts...)]; rec != nil {
			// Mapping comments belong to the key node. Attaching them to the
			// value makes yaml.v3 emit the comment after the preceding scalar.
			k.HeadComment = fmt.Sprintf("source: %s:%d", rec.EffectiveSource.File, rec.EffectiveSource.Line)
		}
	}
}
func sortNode(n *yaml.Node) {
	if n == nil {
		return
	}
	if n.Kind == yaml.MappingNode {
		type pair struct{ k, v *yaml.Node }
		ps := make([]pair, 0, len(n.Content)/2)
		for i := 0; i < len(n.Content); i += 2 {
			ps = append(ps, pair{n.Content[i], n.Content[i+1]})
		}
		sort.SliceStable(ps, func(i, j int) bool { return ps[i].k.Value < ps[j].k.Value })
		n.Content = n.Content[:0]
		for _, p := range ps {
			sortNode(p.v)
			n.Content = append(n.Content, p.k, p.v)
		}
	} else {
		for _, c := range n.Content {
			sortNode(c)
		}
	}
}
func Scalar(v any) string {
	switch v.(type) {
	case map[string]any, []any:
		b, _ := json.Marshal(v)
		return string(b)
	default:
		return fmt.Sprint(v)
	}
}
func BaseFile(path string) string {
	if path == "" {
		return "-"
	}
	parts := strings.Split(strings.ReplaceAll(path, "\\", "/"), "/")
	return parts[len(parts)-1]
}
