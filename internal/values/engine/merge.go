package engine

import (
	"fmt"
	"os"
	"reflect"
	"sort"

	"github.com/suxess-it/kubrix-cli/internal/values/yamlpath"
	"go.yaml.in/yaml/v3"
	"helm.sh/helm/v4/pkg/strvals"
)

type Options struct{ Target, Application, Chart string }

// ReadLayers reads values files from disk into layers named by their path.
func ReadLayers(files []string) ([]Layer, error) {
	layers := make([]Layer, len(files))
	for i, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("read values file %q: %w", file, err)
		}
		layers[i] = Layer{Name: file, Data: data}
	}
	return layers, nil
}

// SetSources turns user --set expressions into set sources labelled --set:N.
func SetSources(expressions []string) []SetSource {
	sets := make([]SetSource, len(expressions))
	for i, expression := range expressions {
		sets[i] = SetSource{Expression: expression, Source: SourceLocation{File: "--set", Line: i + 1, Column: 1}}
	}
	return sets
}

// Merge applies values layers in order, followed by set sources in order.
// Set sources therefore have higher precedence than every layer.
func Merge(layers []Layer, setSources []SetSource, opts Options) (*Result, error) {
	r := &Result{Target: opts.Target, Application: opts.Application, Chart: opts.Chart, Values: map[string]any{}, Records: map[string]*ValueRecord{}}
	for _, layer := range layers {
		doc, err := yamlpath.Parse(layer.Name, layer.Data)
		if err != nil {
			return nil, err
		}
		before := cloneMap(r.Values)
		if err := r.applyMapping(r.Values, doc.Root, nil, layer.Name, nil); err != nil {
			return nil, err
		}
		r.Files = append(r.Files, FileResult{File: layer.Name, Changed: !reflect.DeepEqual(before, r.Values)})
	}
	for i, setSource := range setSources {
		values, err := ParseSet(setSource.Expression, setSource.StringValues)
		if err != nil {
			return nil, fmt.Errorf("parse set source %s:%d %q: %w", setSource.Source.File, setSource.Source.Line, setSource.Expression, err)
		}
		var node yaml.Node
		if err := node.Encode(values); err != nil {
			return nil, fmt.Errorf("encode --set argument %d: %w", i+1, err)
		}
		source := setSource.Source
		if source.File == "" {
			source = SourceLocation{File: "--set", Line: i + 1, Column: 1}
		}
		if err := r.applyMapping(r.Values, &node, nil, source.File, &source); err != nil {
			return nil, err
		}
	}
	r.finalize()
	return r, nil
}

// ParseSet parses a helm --set expression, or a --set-string expression when stringValues is true.
func ParseSet(expression string, stringValues bool) (map[string]any, error) {
	if stringValues {
		return strvals.ParseString(expression)
	}
	return strvals.Parse(expression)
}

func (r *Result) applyMapping(dst map[string]any, node *yaml.Node, prefix []string, sourceName string, fixedSource *SourceLocation) error {
	for i := 0; i < len(node.Content); i += 2 {
		key, val := node.Content[i], node.Content[i+1]
		if key.Kind != yaml.ScalarNode {
			return fmt.Errorf("%s:%d: mapping key must be scalar", sourceName, key.Line)
		}
		pathParts := appendCopy(prefix, key.Value)
		decoded, err := yamlpath.Decode(val)
		if err != nil {
			return fmt.Errorf("%s:%d: decode value: %w", sourceName, val.Line, err)
		}
		old, exists := dst[key.Value]
		source := SourceLocation{File: sourceName, Line: val.Line, Column: val.Column}
		if fixedSource != nil {
			source = *fixedSource
		}
		r.record(yamlpath.JoinPath(pathParts...), decoded, kind(val), source, exists, old)
		if val.Kind == yaml.MappingNode {
			var child map[string]any
			if existing, ok := old.(map[string]any); ok {
				child = existing
			} else {
				child = map[string]any{}
			}
			dst[key.Value] = child
			if err := r.applyMapping(child, val, pathParts, sourceName, fixedSource); err != nil {
				return err
			}
		} else {
			dst[key.Value] = decoded
		}
	}
	return nil
}

func (r *Result) record(path string, value any, k ValueKind, source SourceLocation, exists bool, old any) {
	rec := r.Records[path]
	if rec == nil {
		rec = &ValueRecord{ID: ValueID{Target: r.Target, Application: r.Application, Chart: r.Chart, Path: path}}
		r.Records[path] = rec
	}
	action := ActionDefined
	if exists {
		oldKind := kindValue(old)
		switch {
		case oldKind != k:
			action = ActionTypeChanged
		case reflect.DeepEqual(old, value):
			action = ActionIdenticalRedefinition
		default:
			action = ActionOverridden
		}
	}
	rec.History = append(rec.History, Assignment{Source: source, Value: value, Kind: k, Action: action})
}

func (r *Result) finalize() {
	for path, rec := range r.Records {
		parts, _ := yamlpath.SplitPath(path)
		value, ok := lookup(r.Values, parts)
		if !ok {
			delete(r.Records, path)
			continue
		}
		rec.EffectiveValue = value
		if len(rec.History) > 0 {
			rec.History[len(rec.History)-1].Effective = true
			rec.EffectiveSource = rec.History[len(rec.History)-1].Source
		}
	}
}
func lookup(root map[string]any, parts []string) (any, bool) {
	var cur any = root
	for _, p := range parts {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[p]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}
func kind(n *yaml.Node) ValueKind {
	if n.Tag == "!!null" {
		return KindNull
	}
	switch n.Kind {
	case yaml.MappingNode:
		return KindMap
	case yaml.SequenceNode:
		return KindSequence
	default:
		return KindScalar
	}
}
func kindValue(v any) ValueKind {
	if v == nil {
		return KindNull
	}
	switch v.(type) {
	case map[string]any:
		return KindMap
	case []any:
		return KindSequence
	default:
		return KindScalar
	}
}
func appendCopy(in []string, s string) []string {
	out := append([]string{}, in...)
	return append(out, s)
}
func cloneMap(in map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range in {
		switch x := v.(type) {
		case map[string]any:
			out[k] = cloneMap(x)
		case []any:
			y := make([]any, len(x))
			copy(y, x)
			out[k] = y
		default:
			out[k] = x
		}
	}
	return out
}

// Overrides returns the leaf paths (not maps) that were assigned more than once, sorted.
// A map is assigned once per layer that touches any of its keys, so listing maps would only repeat their children.
func Overrides(r *Result) []string {
	var out []string
	for _, path := range SortedPaths(r.Records) {
		rec := r.Records[path]
		if len(rec.History) > 1 && kindValue(rec.EffectiveValue) != KindMap {
			out = append(out, path)
		}
	}
	return out
}

func SortedPaths(records map[string]*ValueRecord) []string {
	out := make([]string, 0, len(records))
	for p := range records {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}
