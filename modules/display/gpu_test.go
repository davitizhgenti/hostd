package display

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type fakeGPU struct {
	mu          sync.Mutex
	used, total int64
}

func (f *fakeGPU) set(usedMiB int64) { f.mu.Lock(); f.used = usedMiB << 20; f.mu.Unlock() }
func (f *fakeGPU) Memory(context.Context) (int64, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.used, f.total, nil
}

func TestVideoMemoryWarning(t *testing.T) {
	r := newDisplayRig(t, true)
	gpu := &fakeGPU{total: 4096 << 20}
	gpu.set(3000)
	full := r.e.Subscribe(t.Context(), EventGPUFull)
	ok := r.e.Subscribe(t.Context(), EventGPUOK)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.m.followGPU(ctx, gpu); close(done) }()
	defer func() { cancel(); <-done }()

	waitFor(t, "first reading", func() bool {
		st, _ := r.m.Read(context.Background(), "display", nil)
		m, _ := st.(map[string]any)["gpu_memory"].(map[string]int64)
		return m["used"] == 3000<<20
	})
	if n := r.notes.all(); len(n) != 0 {
		t.Fatalf("notice at 73%%: %q", n)
	}
	gpu.set(3800) // 93%
	waitFor(t, "warning", func() bool { r.clock.Advance(15 * time.Second); return len(r.notes.all()) == 1 })
	if got := r.notes.all()[0]; got != "Video memory nearly full: 3.7 of 4.0 GB in use. Lower the game's texture quality or resolution." {
		t.Fatalf("notice %q", got)
	}
	<-full
	gpu.set(3900) // still full: no second warning
	r.clock.Advance(15 * time.Second)
	gpu.set(3000)
	waitFor(t, "fine again", func() bool {
		r.clock.Advance(15 * time.Second)
		select {
		case <-ok:
			return true
		default:
			return false
		}
	})
	if n := r.notes.all(); len(n) != 1 {
		t.Fatalf("notices %q", n)
	}
}

func TestSystemGPUFromAMDSysfs(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "sys/class/drm/card0/device")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "mem_info_vram_total"), []byte("4294967296\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "mem_info_vram_used"), []byte("1073741824\n"), 0o644)
	used, total, err := SystemGPU{SysRoot: root, NvidiaSMI: "-"}.Memory(context.Background())
	if err != nil || used != 1<<30 || total != 4<<30 {
		t.Fatalf("%d %d %v", used, total, err)
	}
	if _, _, err := (SystemGPU{SysRoot: t.TempDir(), NvidiaSMI: "-"}).Memory(context.Background()); err != errNoGPUInfo {
		t.Fatalf("no card: %v", err)
	}
}
