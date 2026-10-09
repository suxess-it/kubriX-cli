package install

import (
	"fmt"
	"os"
)

// DNSCredentials are what the external-dns values-dns-*.yaml files expect; they are never saved.
type DNSCredentials struct {
	Token, ProjectID string
	// File is the path of a credentials file (AWS, Azure).
	File string
}

// DNSSecret returns the Secret external-dns expects for the provider (platform-apps/charts/external-dns/values-dns-*.yaml).
func DNSSecret(provider string, creds DNSCredentials) (string, map[string][]byte, error) {
	readFile := func() ([]byte, error) {
		data, err := os.ReadFile(creds.File)
		if err != nil {
			return nil, fmt.Errorf("reading %s credentials: %w", provider, err)
		}
		return data, nil
	}
	switch provider {
	case "cloudflare":
		return "cloudflare-api-key", map[string][]byte{"apiKey": []byte(creds.Token)}, nil
	case "ionos":
		return "ionos-credentials", map[string][]byte{"api-key": []byte(creds.Token)}, nil
	case "stackit":
		return "external-dns-webhook", map[string][]byte{"AUTH_TOKEN": []byte(creds.Token), "PROJECT_ID": []byte(creds.ProjectID)}, nil
	case "aws":
		data, err := readFile()
		return "sx-external-dns", map[string][]byte{"credentials": data}, err
	case "azure":
		data, err := readFile()
		return "external-dns-azure", map[string][]byte{"azure.json": data}, err
	}
	return "", nil, fmt.Errorf("unknown DNS provider %q", provider)
}
