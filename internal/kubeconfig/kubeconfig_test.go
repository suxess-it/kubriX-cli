package kubeconfig

import (
	"os"
	"path/filepath"
	"testing"
)

const testConfig = `apiVersion: v1
kind: Config
current-context: prod
clusters:
- name: prod-cluster
  cluster: {server: "https://prod.example.com:6443"}
- name: kind-kubrix-demo
  cluster: {server: "https://127.0.0.1:41000"}
contexts:
- name: prod
  context: {cluster: prod-cluster, user: admin}
- name: kind-kubrix-demo
  context: {cluster: kind-kubrix-demo, user: admin}
users:
- name: admin
  user: {token: dummy}
`

func TestContexts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte(testConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", path)
	contexts, current, err := Contexts()
	if err != nil {
		t.Fatal(err)
	}
	if current != "prod" || len(contexts) != 2 {
		t.Fatalf("current %q contexts %+v", current, contexts)
	}
	if contexts[0].Name != "kind-kubrix-demo" || !contexts[0].IsKind() || contexts[1].IsKind() {
		t.Errorf("unexpected kind detection: %+v", contexts)
	}
	if contexts[1].Server != "https://prod.example.com:6443" {
		t.Errorf("server %q", contexts[1].Server)
	}
	cfg, err := RESTConfig("prod")
	if err != nil || cfg.Host != "https://prod.example.com:6443" {
		t.Fatalf("rest config %+v err %v", cfg, err)
	}
}
