package gitopsdiff

import (
	"bytes"
	"log"
	"testing"
)

func TestHelmWarningsArePrintedOnceWithoutTimestamp(t *testing.T) {
	var buf bytes.Buffer
	restore := CaptureHelmWarnings(&buf)
	log.Printf("warning: a")
	log.Printf("warning: a")
	log.Printf("warning: b")
	restore()
	if buf.String() != "warning: a\nwarning: b\n" {
		t.Fatalf("output = %q", buf.String())
	}
	if log.Flags() == 0 {
		t.Fatal("restore did not bring back the standard logger's flags")
	}
}
