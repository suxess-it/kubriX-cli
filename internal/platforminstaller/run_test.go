package platforminstaller

import (
	"context"
	"errors"
	"io"
	"maps"
	"slices"
	"strings"
	"testing"
)

type fakeKube struct {
	calls     []string
	secret    map[string]string
	manifest  []byte
	succeeded bool
	waitErr   error
	logs      string
}

func (f *fakeKube) EnsureNamespace(_ context.Context, name string) error {
	f.calls = append(f.calls, "namespace "+name)
	return nil
}

func (f *fakeKube) ApplySecret(_ context.Context, ns, name string, values map[string]string) error {
	f.calls = append(f.calls, "secret "+ns+"/"+name)
	f.secret = values
	return nil
}

func (f *fakeKube) DeleteJob(_ context.Context, ns, name string) error {
	f.calls = append(f.calls, "delete job "+ns+"/"+name)
	return nil
}

func (f *fakeKube) ApplyManifests(_ context.Context, m []byte) error {
	f.calls = append(f.calls, "manifests")
	f.manifest = m
	return nil
}

func (f *fakeKube) WaitForJobPod(context.Context, string, string) (string, error) {
	return "pod-1", nil
}

func (f *fakeKube) StreamLogs(_ context.Context, _, _ string, w io.Writer) error {
	_, err := io.WriteString(w, f.logs)
	return err
}

func (f *fakeKube) WaitForJob(context.Context, string, string) (bool, error) {
	return f.succeeded, f.waitErr
}

func (f *fakeKube) JobDiagnostics(context.Context, string, string) string { return "diagnostics" }

type fakeProgress struct {
	infos, oks []string
	logs       strings.Builder
	ended      error
}

func (p *fakeProgress) Step(ctx context.Context, _ string, fn func(context.Context) error) error {
	return fn(ctx)
}
func (p *fakeProgress) Begin(string) func(error) { return func(err error) { p.ended = err } }
func (p *fakeProgress) Logs() io.Writer          { return &p.logs }
func (p *fakeProgress) Info(m string)            { p.infos = append(p.infos, m) }
func (p *fakeProgress) OK(m string)              { p.oks = append(p.oks, m) }

func kindSpec(ref string, bootstrap bool, excluded ...string) Spec {
	return Spec{
		RepoURL: "https://github.com/acme/demo.git", RepoToken: "tok", GitUserName: "octocat",
		Domain: "127-0-0-1.nip.io", DNSProvider: "none", TargetType: "kubrix-oss-stack", ClusterType: "kind",
		UpstreamRepo: DefaultUpstream, UpstreamBranch: ref, Bootstrap: bootstrap, ExcludedApps: excluded,
	}
}

// The kind demo from a branch must keep sending exactly the Secret it sent before releases existed.
func TestKindBranchSecretUnchanged(t *testing.T) {
	got := kindSpec("feat/switch_keycloak_chart", true, "kargo").SecretValues()
	want := map[string]string{
		"KUBRIX_REPO":            "https://github.com/acme/demo.git",
		"KUBRIX_REPO_PASSWORD":   "tok",
		"KUBRIX_GIT_USER_NAME":   "octocat",
		"KUBRIX_DOMAIN":          "127-0-0-1.nip.io",
		"KUBRIX_DNS_PROVIDER":    "none",
		"KUBRIX_TARGET_TYPE":     "kubrix-oss-stack",
		"KUBRIX_CLUSTER_TYPE":    "kind",
		"KUBRIX_BOOTSTRAP":       "true",
		"KUBRIX_INSTALLER":       "true",
		"KUBRIX_UPSTREAM_BRANCH": "feat/switch_keycloak_chart",
		"KUBRIX_APP_EXCLUDE":     "kargo",
	}
	if !maps.Equal(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
}

func TestRunSucceeds(t *testing.T) {
	kc, p := &fakeKube{succeeded: true, logs: "installing\n"}, &fakeProgress{}
	err := Run(context.Background(), kc, p, Options{Spec: kindSpec("v7.0.0", false), Manifest: []byte("m"), ReplaceJob: true})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"namespace " + Namespace, "secret " + Namespace + "/" + SecretName, "delete job " + Namespace + "/" + JobName, "manifests"}
	if !slices.Equal(kc.calls, want) {
		t.Errorf("calls %v, want %v", kc.calls, want)
	}
	if string(kc.manifest) != "m" || kc.secret["KUBRIX_BOOTSTRAP"] != "false" {
		t.Errorf("manifest %q secret %v", kc.manifest, kc.secret)
	}
	if p.logs.String() != "installing\n" || p.ended != nil || !slices.Equal(p.oks, []string{"kubriX installed"}) {
		t.Errorf("logs %q ended %v oks %v", p.logs.String(), p.ended, p.oks)
	}
}

func TestRunKeepsPreviousJobUnlessReplaced(t *testing.T) {
	kc := &fakeKube{succeeded: true}
	if err := Run(context.Background(), kc, &fakeProgress{}, Options{Spec: kindSpec("main", true)}); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(kc.calls, "delete job "+Namespace+"/"+JobName) {
		t.Errorf("job must only be deleted on request: %v", kc.calls)
	}
}

func TestRunReportsFailedJob(t *testing.T) {
	kc, p := &fakeKube{}, &fakeProgress{}
	err := Run(context.Background(), kc, p, Options{Spec: kindSpec("main", true)})
	if err == nil || !strings.Contains(err.Error(), "install job failed") {
		t.Fatalf("got %v", err)
	}
	if !slices.Equal(p.infos, []string{"diagnostics"}) || p.ended == nil || len(p.oks) != 0 {
		t.Errorf("infos %v ended %v oks %v", p.infos, p.ended, p.oks)
	}
}

func TestRunReportsWaitError(t *testing.T) {
	kc := &fakeKube{waitErr: errors.New("timeout")}
	err := Run(context.Background(), kc, &fakeProgress{}, Options{Spec: kindSpec("main", true)})
	if err == nil || !strings.Contains(err.Error(), "the cluster keeps running") {
		t.Fatalf("got %v", err)
	}
}
