package install

import (
	"strings"
	"testing"
)

func TestCAImportCommands(t *testing.T) {
	cases := map[string]string{
		"darwin":  "security add-trusted-cert",
		"windows": "certutil -addstore",
		"linux":   "update-ca-certificates",
	}
	for goos, want := range cases {
		lines := caImportCommands(goos, "/tmp/kind-ca.crt")
		joined := strings.Join(lines, "\n")
		if !strings.Contains(joined, want) {
			t.Errorf("%s: expected %q in %q", goos, want, joined)
		}
		if !strings.Contains(joined, "/tmp/kind-ca.crt") {
			t.Errorf("%s: CA path missing from %q", goos, joined)
		}
	}
}
