// Package gpu reads video memory: a game that fills it stutters, then shows garbage or
// crashes, and on a 4 GB card one game can. hostd watches it and tells
// the person at the screen before it is full, while there is still time
// to lower the game's settings.
package gpu

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
)

// Reader reports the graphics card's memory use, in bytes.
type Reader interface {
	Memory(ctx context.Context) (used, total int64, err error)
}

// ErrNoInfo: no source of video memory figures on this machine.
var ErrNoInfo = errors.New("no video memory figures (nvidia-smi or amdgpu sysfs)")

// System reads video memory from nvidia-smi (NVIDIA's driver) or from
// the amdgpu driver's sysfs files.
type System struct {
	SysRoot   string // "/" outside tests
	NvidiaSMI string // default: from PATH; "-": do not use it
}

func (g System) Memory(ctx context.Context) (int64, int64, error) {
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
	return 0, 0, ErrNoInfo
}

func readInt(path string) (int64, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
}
