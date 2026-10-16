package gpu

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSystemFromAMDSysfs(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "sys/class/drm/card0/device")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "mem_info_vram_total"), []byte("4294967296\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "mem_info_vram_used"), []byte("1073741824\n"), 0o644)
	used, total, err := System{SysRoot: root, NvidiaSMI: "-"}.Memory(context.Background())
	if err != nil || used != 1<<30 || total != 4<<30 {
		t.Fatalf("%d %d %v", used, total, err)
	}
	if _, _, err := (System{SysRoot: t.TempDir(), NvidiaSMI: "-"}).Memory(context.Background()); !errors.Is(err, ErrNoInfo) {
		t.Fatalf("no card: %v", err)
	}
}

func TestSystemFromNvidiaSMI(t *testing.T) {
	smi := func(t *testing.T, script string) string {
		p := filepath.Join(t.TempDir(), "nvidia-smi")
		_ = os.WriteFile(p, []byte("#!/bin/sh\n"+script+"\n"), 0o755)
		return p
	}
	// MiB, the first card's line.
	used, total, err := System{NvidiaSMI: smi(t, `echo "1024, 4096"; echo "1, 2"`)}.Memory(context.Background())
	if err != nil || used != 1<<30 || total != 4<<30 {
		t.Fatalf("%d %d %v", used, total, err)
	}
	if _, _, err := (System{NvidiaSMI: smi(t, `echo "No devices were found"`)}).Memory(context.Background()); err == nil {
		t.Fatal("read figures from a message")
	}
	if _, _, err := (System{NvidiaSMI: smi(t, "exit 9")}).Memory(context.Background()); err == nil {
		t.Fatal("a failing nvidia-smi")
	}
}

// A card whose figures cannot be read is skipped for the next one.
func TestSystemSkipsUnreadableCards(t *testing.T) {
	root := t.TempDir()
	for card, files := range map[string][2]string{"card0": {"junk", "1"}, "card1": {"8589934592", "0"}} {
		dir := filepath.Join(root, "sys/class/drm", card, "device")
		_ = os.MkdirAll(dir, 0o755)
		_ = os.WriteFile(filepath.Join(dir, "mem_info_vram_total"), []byte(files[0]), 0o644)
		_ = os.WriteFile(filepath.Join(dir, "mem_info_vram_used"), []byte(files[1]), 0o644)
	}
	used, total, err := System{SysRoot: root, NvidiaSMI: "-"}.Memory(context.Background())
	if err != nil || used != 0 || total != 8<<30 {
		t.Fatalf("%d %d %v", used, total, err)
	}
}
