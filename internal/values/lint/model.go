// Package lint reports suspicious or redundant values overlays.
package lint

import "github.com/suxess-it/kubrix-cli/internal/values/engine"

type Finding struct {
	Rule        string                 `json:"rule"`
	Severity    string                 `json:"severity"`
	Application string                 `json:"application"`
	Path        string                 `json:"path"`
	Message     string                 `json:"message"`
	Source      engine.SourceLocation  `json:"source"`
	Previous    *engine.SourceLocation `json:"previous,omitempty"`
}
