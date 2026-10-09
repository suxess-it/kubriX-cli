package state

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func demo(name, repo string) Installation {
	return Installation{Kind: KindDemo, ClusterName: name, Org: "acme", Repo: repo, GitUserName: "octocat", TargetType: "kubrix-oss-stack", UpstreamRepo: "suxess-it/kubriX", UpstreamBranch: "v7.0.0", Domain: "127-0-0-1.nip.io", DNSProvider: "none", ExcludedApps: []string{"kargo"}}
}

func cluster(context, repo string) Installation {
	return Installation{Kind: Cluster, Context: context, Org: "beta", Repo: repo, GitUserName: "me", UpstreamRepo: "someone/kubriX", UpstreamBranch: "v7.1.0", Domain: "platform.example.com", DNSProvider: "cloudflare", CloudProvider: "aks", ExcludedApps: []string{"external-dns"}}
}

func record(t *testing.T, c *Catalog, states ...Installation) {
	t.Helper()
	for _, st := range states {
		if err := c.Record(st); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMissingFileIsAnEmptyCatalog(t *testing.T) {
	c := Open(t.TempDir())
	if c.Problem() != nil || c.Len() != 0 || len(c.Keys()) != 0 {
		t.Errorf("problem %v len %d", c.Problem(), c.Len())
	}
	if _, ok := c.Last(); ok {
		t.Error("an empty catalog has no last installation")
	}
}

func TestRecordPersistsAcrossOpens(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested")
	c := Open(dir)
	record(t, c, demo("kubrix-demo", "one"), cluster("prod", "platform"))

	again := Open(dir)
	if again.Problem() != nil || again.Len() != 2 {
		t.Fatalf("problem %v len %d", again.Problem(), again.Len())
	}
	got, ok := again.Find("kubrix-demo")
	if !ok || got.Repo != "one" || !slices.Equal(got.ExcludedApps, []string{"kargo"}) {
		t.Errorf("demo %+v", got)
	}
	if got, ok := again.Find("prod"); !ok || !got.IsCluster() || got.Domain != "platform.example.com" {
		t.Errorf("cluster %+v", got)
	}
	data, err := os.ReadFile(filepath.Join(dir, "installations.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(data)), "token") {
		t.Errorf("the catalog must not contain a token field: %s", data)
	}
	info, err := os.Stat(filepath.Join(dir, "installations.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("mode %v %v", info.Mode(), err)
	}
}

func TestKeysAndRecency(t *testing.T) {
	c := Open(t.TempDir())
	record(t, c, demo("b", "1"), demo("c", "2"), cluster("prod", "p"), demo("a", "3"))
	if got := c.Keys(); !slices.Equal(got, []string{"a", "prod", "c", "b"}) {
		t.Errorf("keys %v", got)
	}
	if got := c.Demos(); !slices.Equal(got, []string{"a", "c", "b"}) {
		t.Errorf("demos %v", got)
	}
	if got := c.Clusters(); !slices.Equal(got, []string{"prod"}) {
		t.Errorf("clusters %v", got)
	}
	record(t, c, demo("c", "2"))
	if got := c.Keys(); !slices.Equal(got, []string{"c", "a", "prod", "b"}) {
		t.Errorf("recording again moves it to the front: %v", got)
	}
	if last, ok := c.Last(); !ok || last.ClusterName != "c" {
		t.Errorf("last %+v", last)
	}
}

func TestInstallationsWithoutRecencyAreStillListed(t *testing.T) {
	dir := t.TempDir()
	data := `{"installations": {"zeta": {"kind": "kind", "clusterName": "zeta"}, "alpha": {"kind": "kind", "clusterName": "alpha"}}}`
	if err := os.WriteFile(filepath.Join(dir, "installations.json"), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := Open(dir).Keys(); !slices.Equal(got, []string{"alpha", "zeta"}) {
		t.Errorf("keys %v", got)
	}
}

func TestForget(t *testing.T) {
	dir := t.TempDir()
	c := Open(dir)
	record(t, c, demo("a", "1"), demo("b", "2"))
	if err := c.Forget("b"); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Find("b"); ok || !slices.Equal(c.Keys(), []string{"a"}) {
		t.Errorf("keys %v", c.Keys())
	}
	if got := Open(dir).Keys(); !slices.Equal(got, []string{"a"}) {
		t.Errorf("not persisted: %v", got)
	}
	if last, _ := c.Last(); last.ClusterName != "a" {
		t.Errorf("a forgotten installation is no longer the last one: %+v", last)
	}
	if err := c.Forget("never-existed"); err != nil {
		t.Errorf("forgetting an unknown key is fine: %v", err)
	}
}

// Another run may have saved installations since this catalog was opened; writing must not drop them.
func TestWritesKeepWhatAnotherRunSaved(t *testing.T) {
	dir := t.TempDir()
	first, second := Open(dir), Open(dir)
	record(t, first, demo("one", "1"))
	record(t, second, demo("two", "2"))
	if got := Open(dir).Keys(); !slices.Equal(got, []string{"two", "one"}) {
		t.Errorf("keys %v", got)
	}
}

func TestUnreadableFileIsEmptyAndNeverOverwritten(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "installations.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := Open(dir)
	if c.Problem() == nil || c.Len() != 0 {
		t.Fatalf("problem %v len %d", c.Problem(), c.Len())
	}
	if c.NewDemo().ClusterName != DefaultClusterName {
		t.Error("an unreadable catalog still offers a new demo")
	}
	if err := c.Record(demo("a", "1")); err == nil || !strings.Contains(err.Error(), "unreadable") {
		t.Errorf("Record: %v", err)
	}
	if err := c.Forget("a"); err == nil {
		t.Error("Forget must refuse too")
	}
	if data, _ := os.ReadFile(path); string(data) != "{not json" {
		t.Errorf("the unreadable file must stay as it was: %q", data)
	}
}

func TestUnavailableCatalog(t *testing.T) {
	c := Open("")
	if c.Problem() == nil || c.Len() != 0 {
		t.Errorf("problem %v", c.Problem())
	}
	if err := c.Record(demo("a", "1")); err == nil {
		t.Error("nothing can be saved without a directory")
	}
}

func TestRecordRefusesWhatCannotBeKeyed(t *testing.T) {
	c := Open(t.TempDir())
	if err := c.Record(Installation{Kind: Cluster}); err == nil {
		t.Error("a cluster install without a context has no key")
	}
	record(t, c, demo("shared", "1"))
	if err := c.Record(cluster("shared", "p")); err == nil || !strings.Contains(err.Error(), "already uses") {
		t.Errorf("an install must not replace a demo with the same key: %v", err)
	}
	if got, _ := c.Find("shared"); got.IsCluster() {
		t.Error("the demo must be untouched")
	}
	record(t, c, demo("shared", "2"))
	if got, _ := c.Find("shared"); got.Repo != "2" {
		t.Errorf("recording the same installation again updates it: %+v", got)
	}
}

func TestFreeClusterName(t *testing.T) {
	c := Open(t.TempDir())
	if got := c.FreeClusterName(); got != "kubrix-demo" {
		t.Fatalf("empty catalog: %s", got)
	}
	record(t, c, demo("kubrix-demo", "1"), demo("kubrix-demo-2", "2"))
	if got := c.FreeClusterName(); got != "kubrix-demo-3" {
		t.Fatalf("got %s, want kubrix-demo-3", got)
	}
}

func TestClusterNameFree(t *testing.T) {
	c := Open(t.TempDir())
	record(t, c, demo("taken", "1"), cluster("prod", "p"))
	for name, wantErr := range map[string]string{
		"fresh":    "",
		"a":        "",
		"Taken":    "allowed",
		"my demo":  "allowed",
		"-lead":    "allowed",
		"trail-":   "allowed",
		"":         "allowed",
		"taken":    "already uses",
		"prod":     "already uses",
		"under_sc": "allowed",
	} {
		err := c.ClusterNameFree(name, "")
		if wantErr == "" && err != nil || wantErr != "" && (err == nil || !strings.Contains(err.Error(), wantErr)) {
			t.Errorf("%q: got %v, want %q", name, err, wantErr)
		}
	}
	if err := c.ClusterNameFree("taken", "taken"); err != nil {
		t.Errorf("a rerun keeps its own name: %v", err)
	}
}

func TestNewDemoStartsFromTheLastDemo(t *testing.T) {
	c := Open(t.TempDir())
	if st := c.NewDemo(); st.Kind != KindDemo || st.ClusterName != "kubrix-demo" || st.Repo != "" || st.Org != "" {
		t.Errorf("first demo %+v", st)
	}
	record(t, c, demo("kubrix-demo", "old"), cluster("prod", "platform"))
	st := c.NewDemo()
	if st.Repo != "" || st.ClusterName != "kubrix-demo-2" || st.Kind != KindDemo {
		t.Errorf("a new demo needs its own repository and cluster name: %+v", st)
	}
	if st.Org != "acme" || st.UpstreamBranch != "v7.0.0" || !slices.Equal(st.ExcludedApps, []string{"kargo"}) || st.Context != "" {
		t.Errorf("it keeps the last demo's choices and nothing of the cluster install: %+v", st)
	}
}

func TestNewInstallRerunKeepsEverything(t *testing.T) {
	c := Open(t.TempDir())
	record(t, c, demo("kubrix-demo", "d"), cluster("prod", "platform"))
	st, rerun := c.NewInstall("prod")
	if !rerun || st.Repo != "platform" || st.Domain != "platform.example.com" || !slices.Equal(st.ExcludedApps, []string{"external-dns"}) {
		t.Errorf("rerun %v %+v", rerun, st)
	}
}

func TestNewInstallStartsFromTheLastClusterInstall(t *testing.T) {
	c := Open(t.TempDir())
	record(t, c, cluster("prod", "platform"), demo("kubrix-demo", "d"))
	st, rerun := c.NewInstall("staging")
	if rerun {
		t.Fatal("staging was never installed")
	}
	if st.Repo != "" || st.Domain != "" || st.UpstreamBranch != "" || st.Context != "" || len(st.ExcludedApps) != 0 {
		t.Errorf("what belongs to the other cluster is cleared: %+v", st)
	}
	if st.Org != "beta" || st.GitUserName != "me" || st.UpstreamRepo != "someone/kubriX" || st.DNSProvider != "cloudflare" || st.CloudProvider != "aks" {
		t.Errorf("choices for clusters carry over from the last install, not from the demo: %+v", st)
	}
}

func TestNewInstallFallsBackToThePersonalChoicesOfTheLastDemo(t *testing.T) {
	c := Open(t.TempDir())
	d := demo("kubrix-demo", "d")
	d.GitHost = "github"
	record(t, c, d)
	st, rerun := c.NewInstall("prod")
	if rerun || st.GitHost != "github" || st.Org != "acme" || st.GitUserName != "octocat" || st.UpstreamRepo != "suxess-it/kubriX" {
		t.Errorf("personal choices: %v %+v", rerun, st)
	}
	if st.DNSProvider != "" || st.Domain != "" || st.TargetType != "" || st.Repo != "" || st.Kind != "" {
		t.Errorf("a demo's kind networking must not leak into a cluster install: %+v", st)
	}
	if empty, rerun := Open(t.TempDir()).NewInstall("prod"); rerun || empty.Org != "" {
		t.Errorf("nothing saved: %v %+v", rerun, empty)
	}
}

func TestNewInstallIgnoresADemoWithTheSameKey(t *testing.T) {
	c := Open(t.TempDir())
	record(t, c, demo("prod", "d"))
	if _, rerun := c.NewInstall("prod"); rerun {
		t.Error("a demo is not an earlier install on a cluster")
	}
}

func TestWhere(t *testing.T) {
	if got := demo("kubrix-demo", "r").Where(); got != "kind cluster kubrix-demo" {
		t.Errorf("demo: %q", got)
	}
	if got := cluster("prod", "r").Where(); got != "context prod" {
		t.Errorf("cluster: %q", got)
	}
}

func TestKey(t *testing.T) {
	if demo("a", "r").Key() != "a" || cluster("prod", "r").Key() != "prod" {
		t.Error("demos are keyed by cluster name, installs by context")
	}
}
