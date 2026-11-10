package display

import (
	"time"

	"github.com/davitizhgenti/hostd/internal/evdev"
	"github.com/davitizhgenti/hostd/sdk"
)

// DeviceView is an input device as GET /v1/devices shows it.
type DeviceView struct {
	evdev.Device
	// LastInput is when it last did something; absent if not since hostd
	// started (a keyboard nobody touched, or one hostd cannot read).
	LastInput *time.Time `json:"last_input,omitempty"`
}

// readDevices lists the input devices with when each was last used.
func (m *Module) readDevices() []DeviceView {
	devs := evdev.ScanDevices(m.opts.SysRoot, m.opts.Profiles, m.opts.Players)
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]DeviceView, 0, len(devs))
	for _, d := range devs {
		v := DeviceView{Device: d}
		if t, ok := m.lastBy[d.ID]; ok {
			t = t.UTC()
			v.LastInput = &t
		}
		out = append(out, v)
	}
	return out
}

// handleAssign gives a controller a player slot.
func (m *Module) handleAssign(a sdk.Action) (sdk.Result, error) {
	var args struct {
		ID     string
		Player int
	}
	if err := a.DecodeArgs(&args); err != nil {
		return sdk.Result{}, err
	}
	if m.opts.Players == nil {
		return sdk.Result{}, sdk.Errorf(sdk.CodeModuleUnavailable, "controllers have no player slots here")
	}
	var found *evdev.Device
	for _, d := range m.readDevices() {
		if d.ID == args.ID {
			d := d.Device
			found = &d
		}
	}
	switch {
	case found == nil:
		return sdk.Result{}, sdk.Errorf(sdk.CodeNotFound, "no input device %q (hostctl devices)", args.ID)
	case found.Kind != evdev.KindGamepad:
		return sdk.Result{}, sdk.Errorf(sdk.CodeInvalidArgs, "%s is a %s: only controllers have player slots", found.Name, found.Kind)
	}
	m.opts.Players.Assign(args.ID, args.Player)
	m.mu.Lock()
	core := m.core
	m.mu.Unlock()
	if core != nil {
		core.Emit(sdk.Event{Type: EventDeviceAssigned, Action: a.ID,
			Data: sdk.MustJSON(map[string]any{"id": args.ID, "player": args.Player})})
	}
	return sdk.Result{Data: sdk.MustJSON(map[string]any{"id": args.ID, "player": args.Player})}, nil
}

// announceDevices announces devices as they come and go.
func (m *Module) announceDevices(known, now map[string]evdev.Device) {
	m.mu.Lock()
	core := m.core
	m.mu.Unlock()
	if core == nil {
		return
	}
	src := &sdk.Source{Kind: sdk.SourceExternal, Name: "input"}
	for id, d := range now {
		if _, ok := known[id]; !ok {
			core.Emit(sdk.Event{Type: EventDeviceConnected, Data: sdk.MustJSON(d), Source: src})
		}
	}
	for id, d := range known {
		if _, ok := now[id]; !ok {
			core.Emit(sdk.Event{Type: EventDeviceDisconnected, Data: sdk.MustJSON(d), Source: src})
		}
	}
}
