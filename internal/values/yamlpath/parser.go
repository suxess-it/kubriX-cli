package yamlpath

import (
	"fmt"

	"go.yaml.in/yaml/v3"
)

type Document struct {
	File string
	Root *yaml.Node
}

// Parse parses a values document named path, keeping line and column of every node.
func Parse(path string, b []byte) (*Document, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("parse values file %q: %w", path, err)
	}
	if len(doc.Content) == 0 {
		doc.Content = []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("values file %q: document root must be a mapping", path)
	}
	return &Document{File: path, Root: root}, nil
}

func Decode(node *yaml.Node) (any, error) {
	var v any
	if err := node.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}
