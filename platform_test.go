package main

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPlatformStateDirectories(t *testing.T) {
	for _, tt := range []struct{ os, home, xdg, want string }{
		{"darwin", "/Users/demo", "/tmp/config", "/Users/demo/Library/Application Support/kitty-tunnels"},
		{"linux", "/home/demo", "", "/home/demo/.config/kitty-tunnels"},
		{"linux", "/home/demo", "/tmp/config", "/tmp/config/kitty-tunnels"},
		{"linux", "/home/demo", "relative", "/home/demo/.config/kitty-tunnels"},
	} {
		if got := defaultStateRoot(tt.os, tt.home, tt.xdg); got != tt.want {
			t.Fatalf("%s root=%s want=%s", tt.os, got, tt.want)
		}
	}
}

func TestStateDirectoryOverride(t *testing.T) {
	t.Setenv("TUNNELS_HOME", "/tmp/tunnels-test-home")
	p, err := statePaths()
	if err != nil {
		t.Fatal(err)
	}
	if p.Root != "/tmp/tunnels-test-home" || p.Config != filepath.Join(p.Root, "config.json") {
		t.Fatal(p)
	}
}

func TestLinuxMissingToolsError(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux dependency lookup")
	}
	t.Setenv("PATH", t.TempDir())
	err := requireTools()
	if err == nil || !strings.Contains(err.Error(), "openssh-client procps lsof") {
		t.Fatalf("unexpected dependency result: %v", err)
	}
}
