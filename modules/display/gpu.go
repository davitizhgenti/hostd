// Video memory: a game that fills it stutters, then shows garbage or
// crashes, and on a 4 GB card one game can. hostd watches it and tells
// the person at the screen before it is full, while there is still time
// to lower the game's settings.
package display

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/davitizhgenti/hostd/sdk"
)

// GPUMemory reports the graphics card's memory use, in bytes.
type GPUMemory interface {
	Memory(ctx context.Context) (used, total int64, err error)
}

// errNoGPUInfo: no source of video memory figures on this machine.
var errNoGPUInfo = errors.New("no video memory figures (nvidia-smi or amdgpu sysfs)")

// SystemGPU reads video memory from nvidia-smi (NVIDIA's driver) or from
// the amdgpu driver's sysfs files.
type SystemGPU struct {
	SysRoot   string // "/" outside tests
	NvidiaSMI string // default: from PATH; "-": do not use it
}

func (g SystemGPU) Memory(ctx context.Context) (int64, int64, error) {
	smi := g.NvidiaSMI
	if smi == "" {
		smi, _ = exec.LookPath("nvidia-smi")
	}
	if path := smi; path != "" && path != "-" {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, path, "--query-gpu=memory.used,memory.total", "--format=csv,noheader,nounits").Output()
		if err != nil {
			return 0, 0, err
		}
		// MiB, one line per card: the first card is the screen's.
		line, _, _ := bytes.Cut(bytes.TrimSpace(out), []byte("\n"))
		used, total, ok := strings.Cut(string(line), ",")
		u, err1 := strconv.ParseInt(strings.TrimSpace(used), 10, 64)
		t, err2 := strconv.ParseInt(strings.TrimSpace(total), 10, 64)
		if !ok || err1 != nil || err2 != nil {
			return 0, 0, errors.New("nvidia-smi: unexpected output " + strconv.Quote(string(line)))
		}
		return u << 20, t << 20, nil
	}
	root := g.SysRoot
	if root == "" {
		root = "/"
	}
	cards, _ := filepath.Glob(filepath.Join(root, "sys/class/drm/card[0-9]*/device/mem_info_vram_total"))
	for _, totalFile := range cards {
		dir := filepath.Dir(totalFile)
		t, err1 := readInt(totalFile)
		u, err2 := readInt(filepath.Join(dir, "mem_info_vram_used"))
		if err1 == nil && err2 == nil && t > 0 {
			return u, t, nil
		}
	}
	return 0, 0, errNoGPUInfo
}

func readInt(path string) (int64, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
}

// Video memory thresholds: full from 90%, fine again under 80%.
const (
	gpuFullAt = 0.90
	gpuOKAt   = 0.80
)

// followGPU reads video memory every GPUEvery, keeps the figures for the
// display read, and announces when it fills up and when it is fine again.
func (m *Module) followGPU(ctx context.Context, g GPUMemory) {
	if g == nil {
		return
	}
	full := false
	t := m.opts.Clock.NewTicker(m.opts.GPUEvery)
	defer t.Stop()
	for {
		used, total, err := g.Memory(ctx)
		if err == nil && total > 0 {
			m.mu.Lock()
			m.gpuUsed, m.gpuTotal = used, total
			core := m.core
			m.mu.Unlock()
			share := float64(used) / float64(total)
			data := sdk.MustJSON(map[string]int64{"used": used, "total": total})
			switch {
			case !full && share >= gpuFullAt:
				full = true
				if core != nil {
					core.Emit(sdk.Event{Type: EventGPUFull, Data: data})
				}
				m.notice("", "Video memory nearly full: "+gib(used)+" of "+gib(total)+
					" GB in use. Lower the game's texture quality or resolution.")
			case full && share < gpuOKAt:
				full = false
				if core != nil {
					core.Emit(sdk.Event{Type: EventGPUOK, Data: data})
				}
			}
		} else if errors.Is(err, errNoGPUInfo) {
			return // nothing to watch here
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C():
		}
	}
}

// gib formats bytes as gigabytes with one decimal.
func gib(b int64) string {
	return strconv.FormatFloat(float64(b)/(1<<30), 'f', 1, 64)
}
