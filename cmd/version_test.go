package cmd

import (
	"bytes"
	"runtime/debug"
	"testing"
)

func TestBuildInfoString(t *testing.T) {
	for name, tc := range map[string]struct {
		in   BuildInfo
		want string
	}{
		"unstamped build":      {BuildInfo{Version: "dev", Commit: "none", Date: "unknown"}, "dev"},
		"zero value":           {BuildInfo{}, "dev"},
		"release":              {BuildInfo{Version: "0.1.0", Commit: "59db8fafc963a189a292412818e85bab5d729f52", Date: "2026-10-09T12:42:20Z"}, "0.1.0 (commit 59db8fa, built 2026-10-09T12:42:20Z)"},
		"short commit":         {BuildInfo{Version: "0.1.0", Commit: "59db8fa", Date: "unknown"}, "0.1.0 (commit 59db8fa)"},
		"without commit":       {BuildInfo{Version: "0.1.0", Commit: "none", Date: "2026-10-09"}, "0.1.0 (built 2026-10-09)"},
		"container build args": {BuildInfo{Version: "dev-202610091242", Commit: "59db8fa", Date: "2026-10-09_12:42:20"}, "dev-202610091242 (commit 59db8fa, built 2026-10-09_12:42:20)"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := tc.in.String(); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestWithRuntimeFillsOnlyWhatIsUnset(t *testing.T) {
	bi := &debug.BuildInfo{
		Main: debug.Module{Version: "v0.1.0"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "abcdef0123456789"},
			{Key: "vcs.time", Value: "2026-10-09T10:00:00Z"},
			{Key: "GOOS", Value: "linux"},
		},
	}
	got := BuildInfo{Version: "dev", Commit: "none", Date: "unknown"}.WithRuntime(bi, true)
	if got != (BuildInfo{Version: "0.1.0", Commit: "abcdef0123456789", Date: "2026-10-09T10:00:00Z"}) {
		t.Errorf("unstamped: %+v", got)
	}

	stamped := BuildInfo{Version: "0.2.0", Commit: "1111111", Date: "2026-10-10"}
	if got := stamped.WithRuntime(bi, true); got != stamped {
		t.Errorf("linker flags win over the toolchain: %+v", got)
	}

	if got := (BuildInfo{Version: "dev"}).WithRuntime(&debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, true); got.Version != "dev" {
		t.Errorf("a local build has no module version: %+v", got)
	}
	if got := (BuildInfo{Version: "dev"}).WithRuntime(nil, false); got.Version != "dev" {
		t.Errorf("no build info: %+v", got)
	}
}

func TestVersionFlag(t *testing.T) {
	root := NewRoot(BuildInfo{Version: "0.1.0", Commit: "59db8fafc963", Date: "2026-10-09"}, Features{})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"--version"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if want := "kubrix version 0.1.0 (commit 59db8fa, built 2026-10-09)\n"; out.String() != want {
		t.Errorf("got %q, want %q", out.String(), want)
	}
}
