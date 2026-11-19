package ui

import (
	"bytes"
	"testing"
)

// Output that is not a terminal, such as a CI log, gets no color codes.
func TestPlainWritesNoColorCodesToANonTerminal(t *testing.T) {
	var out bytes.Buffer
	p := NewPlain(&out)
	p.Heading("Install")
	p.OK("done")
	p.Warn("careful")
	p.Info("plain")
	if want := "Install\n✓ done\n! careful\nplain\n"; out.String() != want {
		t.Fatalf("output = %q, want %q", out.String(), want)
	}
}
