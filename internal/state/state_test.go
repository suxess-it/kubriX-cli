package state

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestDir(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/xdg")
	if dir, _ := Dir(); dir != filepath.Join("/xdg", "kubrix") {
		t.Errorf("with XDG_CONFIG_HOME: %s", dir)
	}
	if runtime.GOOS == "windows" {
		return
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "/home/someone")
	if dir, _ := Dir(); dir != "/home/someone/.config/kubrix" {
		t.Errorf("default: %s", dir)
	}
}
