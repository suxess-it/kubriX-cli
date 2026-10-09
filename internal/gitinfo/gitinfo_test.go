package gitinfo

import "testing"

func TestParseRemote(t *testing.T) {
	cases := map[string]Remote{
		"https://github.com/suxess-it/kubriX":     {"suxess-it", "kubriX"},
		"https://github.com/suxess-it/kubriX.git": {"suxess-it", "kubriX"},
		"git@github.com:someone/kubriX.git":       {"someone", "kubriX"},
		"ssh://git@github.com/someone/kubriX.git": {"someone", "kubriX"},
		"https://github.com/someone/kubrix-fork/": {"someone", "kubrix-fork"},
	}
	for url, want := range cases {
		got, err := ParseRemote(url)
		if err != nil {
			t.Errorf("%s: %v", url, err)
			continue
		}
		if got != want {
			t.Errorf("%s: got %+v, want %+v", url, got, want)
		}
	}
	if _, err := ParseRemote("https://gitlab.com/a/b.git"); err == nil {
		t.Error("expected error for non-github remote")
	}
}

func TestIsFork(t *testing.T) {
	if (&Info{Origin: Remote{"suxess-it", "kubriX"}}).IsFork() {
		t.Error("upstream must not be a fork")
	}
	if !(&Info{Origin: Remote{"someone", "kubriX"}}).IsFork() {
		t.Error("other owner must be a fork")
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		name                     string
		local, remote, porcelain string
		want                     PushStatus
		warnings                 int
	}{
		{"clean and pushed", "abc", "abc\n", "", PushStatus{}, 0},
		{"not on remote", "abc", "", "", PushStatus{RemoteMissing: true}, 1},
		{"diverged", "abc", "def", "", PushStatus{Diverged: true}, 1},
		{"dirty", "abc", "abc", " M install-platform.sh\n", PushStatus{Dirty: true}, 1},
		{"not on remote and dirty", "abc", "", "?? x\n", PushStatus{RemoteMissing: true, Dirty: true}, 2},
	}
	for _, c := range cases {
		got := Classify(c.local, c.remote, c.porcelain)
		if got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
		if n := len(got.Warnings("feat/x")); n != c.warnings {
			t.Errorf("%s: got %d warnings, want %d", c.name, n, c.warnings)
		}
	}
}
