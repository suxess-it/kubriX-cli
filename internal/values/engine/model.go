// Package engine implements ordered Helm-values-style merging and provenance.
package engine

type SourceLocation struct {
	File   string `json:"file" yaml:"file"`
	Line   int    `json:"line" yaml:"line"`
	Column int    `json:"column" yaml:"column"`
}
type ValueID struct {
	Target      string `json:"target"`
	Application string `json:"application"`
	Chart       string `json:"chart"`
	Path        string `json:"path"`
}
type ValueKind string

const (
	KindMap      ValueKind = "map"
	KindSequence ValueKind = "sequence"
	KindScalar   ValueKind = "scalar"
	KindNull     ValueKind = "null"
)

type AssignmentAction string

const (
	ActionDefined               AssignmentAction = "defined"
	ActionOverridden            AssignmentAction = "overridden"
	ActionIdenticalRedefinition AssignmentAction = "identical-redefinition"
	ActionTypeChanged           AssignmentAction = "type-change"
)

type Assignment struct {
	Source    SourceLocation   `json:"source"`
	Value     any              `json:"value"`
	Kind      ValueKind        `json:"kind"`
	Action    AssignmentAction `json:"action"`
	Effective bool             `json:"effective"`
}
type ValueRecord struct {
	ID              ValueID        `json:"id"`
	EffectiveValue  any            `json:"effectiveValue"`
	EffectiveSource SourceLocation `json:"effectiveSource"`
	History         []Assignment   `json:"history"`
}

// Layer is one values file in merge order. Name is how sources refer to it: a path, or a template marked as rendered.
type Layer struct {
	Name string
	Data []byte
}

type FileResult struct {
	File    string
	Changed bool
}

// SetSource is a Helm strvals expression with an explicit provenance label.
// StringValues matches Helm parameters, whose values are always strings.
type SetSource struct {
	Expression   string
	Source       SourceLocation
	StringValues bool
}
type Result struct {
	Target      string
	Application string
	Chart       string
	Values      map[string]any
	Records     map[string]*ValueRecord
	Files       []FileResult
}
