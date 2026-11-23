package proc

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestScan(t *testing.T) {
	root := t.TempDir()
	fake(t, root, 10, unit("steam"), []string{"FLATPAK_ID=com.valvesoftware.Steam", "HOME=/h"}, 1)
	fake(t, root, 11, unit("steam"), []string{"FLATPAK_ID=com.valvesoftware.Steam", "SteamAppId=620"}, 10)
	fake(t, root, 12, "", []string{"HOME=/h"}, 1)
	_ = os.WriteFile(filepath.Join(root, "12", "comm"), []byte("dolphin-emu\n"), 0o644)
	_ = os.MkdirAll(filepath.Join(root, "self"), 0o755) // not a process

	if got := WithEnv(root, "FLATPAK_ID=com.valvesoftware.Steam"); !slices.Equal(got, []int{10, 11}) {
		t.Fatalf("with env %v", got)
	}
	got := EnvsRunning(root, []string{"SteamAppId=620", "SteamAppId=550", "HOME=/h"})
	if len(got) != 2 || !got["SteamAppId=620"] || !got["HOME=/h"] {
		t.Fatalf("running %v", got)
	}
	if len(EnvsRunning(root, nil)) != 0 || len(EnvsRunning(filepath.Join(root, "nope"), []string{"A=1"})) != 0 {
		t.Fatal("nothing asked, or no /proc")
	}
	if !Named(root, "dolphin-emu") || Named(root, "steam") {
		t.Fatal("named")
	}
	n := 0
	Each(root, func(int) bool { n++; return false })
	if n != 1 || Root("") != "/proc" {
		t.Fatalf("each stops when asked: %d", n)
	}
}
