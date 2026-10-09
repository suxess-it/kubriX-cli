package platforminstaller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// rawServer serves files like raw.githubusercontent.com and records the requests it got.
type rawServer struct {
	*httptest.Server
	mu       sync.Mutex
	requests []string // "path|Authorization"
}

func newRawServer(t *testing.T, files map[string]string, tokenOnly map[string]bool) *rawServer {
	t.Helper()
	rs := &rawServer{}
	rs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rs.mu.Lock()
		rs.requests = append(rs.requests, r.URL.Path+"|"+r.Header.Get("Authorization"))
		rs.mu.Unlock()
		body, ok := files[r.URL.Path]
		if !ok || tokenOnly[r.URL.Path] && r.Header.Get("Authorization") == "" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(rs.Close)
	return rs
}

func (rs *rawServer) seen() []string {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return slices.Clone(rs.requests)
}

const valuesTemplate = `applications:
  - name: traefik
  {{ if ne .kubriX.clusterType "kind" -}}
  - name: external-dns
  {{ end -}}
  - name: backstage
`

func TestRawURLRefs(t *testing.T) {
	s := Source{}
	if got := s.rawURL("suxess-it/kubriX", "v7.0.0", "install-manifests.yaml"); got != "https://raw.githubusercontent.com/suxess-it/kubriX/refs/tags/v7.0.0/install-manifests.yaml" {
		t.Errorf("release ref: %s", got)
	}
	if got := s.rawURL("suxess-it/kubriX", "feat/x", "install-manifests.yaml"); got != "https://raw.githubusercontent.com/suxess-it/kubriX/refs/heads/feat/x/install-manifests.yaml" {
		t.Errorf("branch ref: %s", got)
	}
	if got := (Source{RawURL: "http://localhost:1/"}).rawURL("a/b", "main", "f"); got != "http://localhost:1/a/b/refs/heads/main/f" {
		t.Errorf("custom base: %s", got)
	}
}

func TestSourceAppsRendersTheTemplateForTheClusterType(t *testing.T) {
	rs := newRawServer(t, map[string]string{
		"/suxess-it/kubriX/refs/tags/v7.0.0/platform-apps/target-chart/values-kubrix-oss-stack.yaml.tmpl": valuesTemplate,
	}, nil)
	s := Source{RawURL: rs.URL}
	kind, err := s.Apps(context.Background(), "suxess-it/kubriX", "v7.0.0", "kubrix-oss-stack", "kind")
	if err != nil || !slices.Equal(kind, []string{"traefik", "backstage"}) {
		t.Errorf("kind: %v %v", kind, err)
	}
	k8s, err := s.Apps(context.Background(), "suxess-it/kubriX", "v7.0.0", "kubrix-oss-stack", "k8s")
	if err != nil || !slices.Equal(k8s, []string{"traefik", "external-dns", "backstage"}) {
		t.Errorf("k8s: %v %v", k8s, err)
	}
}

func TestSourceAppsFallsBackToAPlainValuesFile(t *testing.T) {
	rs := newRawServer(t, map[string]string{
		"/o/r/refs/heads/main/platform-apps/target-chart/values-kind.yaml": "applications:\n  - name: argocd\n",
	}, nil)
	apps, err := Source{RawURL: rs.URL}.Apps(context.Background(), "o/r", "main", "kind", "kind")
	if err != nil || !slices.Equal(apps, []string{"argocd"}) {
		t.Errorf("%v %v", apps, err)
	}
	if got := rs.seen(); len(got) != 2 || !strings.Contains(got[0], ".yaml.tmpl") {
		t.Errorf("the template is tried first: %v", got)
	}
}

func TestSourceAppsNamesWhatIsMissing(t *testing.T) {
	rs := newRawServer(t, nil, nil)
	_, err := Source{RawURL: rs.URL}.Apps(context.Background(), "o/r", "feat/x", "kind-base", "kind")
	if err == nil || !strings.Contains(err.Error(), `target type "kind-base"`) || !strings.Contains(err.Error(), "o/r@feat/x") {
		t.Errorf("got %v", err)
	}
}

func TestSourceTriesAnonymouslyBeforeUsingTheToken(t *testing.T) {
	const path = "/o/r/refs/heads/main/install-manifests.yaml"
	public := newRawServer(t, map[string]string{path: "image: x"}, nil)
	if data, err := (Source{RawURL: public.URL, Token: "secret"}).Manifest(context.Background(), "o/r", "main"); err != nil || string(data) != "image: x" {
		t.Fatalf("%q %v", data, err)
	}
	if got := public.seen(); !slices.Equal(got, []string{path + "|"}) {
		t.Errorf("a public file must be fetched without the token: %v", got)
	}

	private := newRawServer(t, map[string]string{path: "image: y"}, map[string]bool{path: true})
	if data, err := (Source{RawURL: private.URL, Token: "secret"}).Manifest(context.Background(), "o/r", "main"); err != nil || string(data) != "image: y" {
		t.Fatalf("%q %v", data, err)
	}
	if got := private.seen(); !slices.Equal(got, []string{path + "|", path + "|token secret"}) {
		t.Errorf("a private fork is retried with the token: %v", got)
	}

	if _, err := (Source{RawURL: private.URL}).Manifest(context.Background(), "o/r", "main"); err == nil || !strings.Contains(err.Error(), "HTTP 404") || !strings.Contains(err.Error(), "is the branch pushed?") {
		t.Errorf("without a token the 404 is explained: %v", err)
	}
}

func TestSourceManifestReportsUnreachableServers(t *testing.T) {
	rs := newRawServer(t, nil, nil)
	url := rs.URL
	rs.Close()
	if _, err := (Source{RawURL: url}).Manifest(context.Background(), "o/r", "main"); err == nil {
		t.Error("an unreachable server must fail")
	}
}

func TestSourceImageTagExists(t *testing.T) {
	reg := httptest.NewServer(registry.New())
	t.Cleanup(reg.Close)
	host := strings.TrimPrefix(reg.URL, "http://")
	image := host + "/suxess-it/kubrix-installer"

	img, err := random.Image(16, 1)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := name.NewTag(image+":v7.0.0", name.Insecure)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(ref, img); err != nil {
		t.Fatal(err)
	}

	s := Source{Image: image, InsecureRegistry: true}
	if ok, err := s.ImageTagExists(context.Background(), "v7.0.0"); err != nil || !ok {
		t.Errorf("pushed tag: %v %v", ok, err)
	}
	if ok, err := s.ImageTagExists(context.Background(), "feat-x"); err != nil || ok {
		t.Errorf("missing tag must be reported as missing, not as an error: %v %v", ok, err)
	}
	if _, err := s.ImageTagExists(context.Background(), "bad tag!"); err == nil {
		t.Error("an invalid tag is an error")
	}
	reg.Close()
	if ok, err := s.ImageTagExists(context.Background(), "v7.0.0"); err == nil || ok {
		t.Errorf("an unreachable registry is an error: %v %v", ok, err)
	}
}

func TestSourceGitURL(t *testing.T) {
	if got := (Source{}).GitURL("suxess-it/kubriX"); got != "https://github.com/suxess-it/kubriX.git" {
		t.Errorf("got %s", got)
	}
}
