package ui

import (
	"bytes"
	"context"
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

// A step in a CI log runs without a spinner, which would need a terminal, and prints its title as a line.
func TestPlainStepRunsWithoutATerminal(t *testing.T) {
	var out bytes.Buffer
	ran := false
	err := NewPlain(&out).Step(context.Background(), "Bootstrap Argo CD", func(context.Context) error {
		ran = true
		return nil
	})
	if err != nil {
		t.Fatalf("Step: %v", err)
	}
	if !ran {
		t.Fatal("Step did not run the step")
	}
	if want := "Bootstrap Argo CD\n"; out.String() != want {
		t.Fatalf("output = %q, want %q", out.String(), want)
	}
}
