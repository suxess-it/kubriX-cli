package projectconfig

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func put(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

func load(t *testing.T, dir, config string) *Config {
	t.Helper()
	put(t, filepath.Join(dir, "conf", DefaultPath), config)
	c, err := Load(filepath.Join(dir, "conf", DefaultPath))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestOverlayResolvesFilesRelativeToTheConfigAndSkipsMissingOptionalOnes(t *testing.T) {
	d := t.TempDir()
	put(t, filepath.Join(d, "conf", "b", "one.yaml"), "x: 1\n")
	put(t, filepath.Join(d, "abs.yaml"), "y: 1\n")
	c := load(t, d, "version: v1\ntargets:\n  dev:\n    applications:\n      zeta:\n        files: [b/one.yaml]\n      app:\n        chart: other\n        files:\n          - b/one.yaml\n          - path: absent.yaml\n            optional: true\n          - "+filepath.Join(d, "abs.yaml")+"\n")
	o, err := c.Overlay("dev")
	if err != nil {
		t.Fatal(err)
	}
	if o.TargetType != "dev" || len(o.Apps) != 2 || o.Apps[0].Name != "app" || o.Apps[1].Chart != "zeta" {
		t.Fatalf("overlay = %+v, want apps sorted and chart defaulting to the name", o)
	}
	want := []string{filepath.Join(d, "conf", "b", "one.yaml"), filepath.Join(d, "abs.yaml")}
	if got := o.Apps[0].LayerNames(); o.Apps[0].Chart != "other" || !reflect.DeepEqual(got, want) {
		t.Fatalf("app = %s %v, want other %v", o.Apps[0].Chart, got, want)
	}
}

func TestOverlayRejectsUnusableApplications(t *testing.T) {
	cases := []struct{ name, apps, target, want string }{
		{"unknown target type", "a:\n        files: [x.yaml]\n", "nope", `target type "nope" does not exist`},
		{"empty file list", "a:\n        files: []\n", "dev", "empty file list"},
		{"missing required file", "a:\n        files: [missing.yaml]\n", "dev", "values file"},
		{"only missing optional files", "a:\n        files:\n          - path: missing.yaml\n            optional: true\n", "dev", "no existing values files"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := load(t, t.TempDir(), "version: v1\ntargets:\n  dev:\n    applications:\n      "+tc.apps)
			if _, err := c.Overlay(tc.target); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestADuplicateFileIsMergedTwiceWithAWarning(t *testing.T) {
	d := t.TempDir()
	put(t, filepath.Join(d, "conf", "one.yaml"), "x: 1\n")
	o, err := load(t, d, "version: v1\ntargets:\n  t:\n    applications:\n      a:\n        files: [one.yaml, ./one.yaml]\n").Overlay("t")
	if err != nil || len(o.Warnings) != 1 || len(o.Apps[0].Layers) != 2 {
		t.Fatalf("overlay = %+v, %v; want two layers and one warning", o, err)
	}
}

func TestLoadRejectsBadConfigs(t *testing.T) {
	cases := map[string]string{
		"version: v2\n":             "unsupported version",
		"version: v1\ntargets: [\n": "parse config",
		"version: v1\ntargets:\n  t:\n    applications:\n      a:\n        files: [{optional: true}]\n": "file path must not be empty",
	}
	for body, want := range cases {
		p := filepath.Join(t.TempDir(), DefaultPath)
		put(t, p, body)
		if _, err := Load(p); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Load(%q) error = %v, want %q", body, err, want)
		}
	}
	if _, err := Load(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Error("Load of a missing file succeeded")
	}
}
