package install

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDNSSecret(t *testing.T) {
	creds := DNSCredentials{Token: "t0k", ProjectID: "p1"}
	for provider, want := range map[string]struct{ name, key, value string }{
		"cloudflare": {"cloudflare-api-key", "apiKey", "t0k"},
		"ionos":      {"ionos-credentials", "api-key", "t0k"},
		"stackit":    {"external-dns-webhook", "PROJECT_ID", "p1"},
	} {
		name, data, err := DNSSecret(provider, creds)
		if err != nil || name != want.name || string(data[want.key]) != want.value {
			t.Errorf("%s: %s %v %v", provider, name, data, err)
		}
	}
	file := filepath.Join(t.TempDir(), "credentials")
	if err := os.WriteFile(file, []byte("[default]\naws_access_key_id=x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	name, data, err := DNSSecret("aws", DNSCredentials{File: file})
	if err != nil || name != "sx-external-dns" || !strings.Contains(string(data["credentials"]), "aws_access_key_id") {
		t.Errorf("aws: %s %v %v", name, data, err)
	}
	if name, data, err := DNSSecret("azure", DNSCredentials{File: file}); err != nil || name != "external-dns-azure" || data["azure.json"] == nil {
		t.Errorf("azure: %s %v %v", name, data, err)
	}
	if _, _, err := DNSSecret("aws", DNSCredentials{File: "/does/not/exist"}); err == nil {
		t.Error("missing credentials file must fail")
	}
	if _, _, err := DNSSecret("route66", creds); err == nil {
		t.Error("unknown provider must fail")
	}
}
