package display

import (
	"context"
	"errors"
	"strconv"

	"github.com/davitizhgenti/hostd/internal/gpu"
	"github.com/davitizhgenti/hostd/sdk"
)

// Video memory thresholds: full from 90%, fine again under 80%.
const (
	gpuFullAt = 0.90
	gpuOKAt   = 0.80
)

// followGPU reads video memory every GPUEvery, keeps the figures for the
// display read, and announces when it fills up and when it is fine again.
func (m *Module) followGPU(ctx context.Context, g gpu.Reader) {
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
		} else if errors.Is(err, gpu.ErrNoInfo) {
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
