package display

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/davitizhgenti/hostd/internal/evdev"
	"github.com/davitizhgenti/hostd/sdk"
)

// DeviceView is an input device as GET /v1/devices shows it.
type DeviceView struct {
	evdev.Device
	// Label is the name the menu shows: the one it was given, or a default
	// ("Wii Remote 2", "Mouse"). Colour is its pointer's and mark's.
	Label  string `json:"label"`
	Colour string `json:"colour"`
	// Pointer: it points at the screen (a mouse, a Wii Remote).
	Pointer bool `json:"pointer,omitempty"`
	// Calibrated: its pointer was measured (hostctl calibrate).
	Calibrated bool `json:"calibrated,omitempty"`
	// LastInput is when it last did something; absent if not since hostd
	// started (a keyboard nobody touched, or one hostd cannot read).
	LastInput *time.Time `json:"last_input,omitempty"`
}

// hereDevice is a device with no input node, such as a Wii Remote on a
// DolphinBar, that hostd hears from.
type hereDevice struct {
	Family string
	Player int
}

// label is the name a device is shown by.
func (m *Module) label(d evdev.Device, family string) string {
	if n := m.opts.Identities.Get(d.ID).Name; n != "" {
		return n
	}
	switch {
	case family == "wii":
		if d.Player > 0 {
			return "Wii Remote " + strconv.Itoa(d.Player)
		}
		return "Wii Remote"
	case d.Kind == evdev.KindPointer:
		return "Mouse"
	case d.Kind == evdev.KindKeyboard:
		return "Keyboard"
	case d.Kind == evdev.KindGamepad && d.Player > 0:
		return d.Name + " " + strconv.Itoa(d.Player)
	}
	return d.Name
}

// pointerLabel is the name a pointer event carries, with its colour.
func (m *Module) pointerLabel(id, family string, player int) (string, string) {
	d := evdev.Device{ID: id, Player: player, Kind: evdev.KindPointer}
	if family == "wii" {
		d.Kind = evdev.KindGamepad
	}
	return m.label(d, family), m.opts.Identities.Get(id).Colour
}

// readDevices lists the input devices with when each was last used, and the
// Wii Remotes heard from.
func (m *Module) readDevices() []DeviceView {
	devs := evdev.ScanDevices(m.opts.SysRoot, m.opts.Profiles, m.opts.Players)
	m.mu.Lock()
	here := make(map[string]hereDevice, len(m.here))
	for id, h := range m.here {
		here[id] = h
	}
	m.mu.Unlock()
	ids := make([]string, 0, len(here))
	for id := range here {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		devs = append(devs, evdev.Device{ID: id, Name: "Wii Remote", Kind: evdev.KindGamepad, Vendor: "057e", Product: "0306", Player: here[id].Player})
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]DeviceView, 0, len(devs))
	for _, d := range devs {
		v := DeviceView{Device: d}
		fam := here[d.ID].Family
		v.Label, v.Colour = m.label(d, fam), m.opts.Identities.Get(d.ID).Colour
		v.Pointer = d.Kind == evdev.KindPointer || fam == "wii"
		v.Calibrated = m.opts.Identities.Calibration(d.ID).W > 0
		if t, ok := m.lastBy[d.ID]; ok {
			t = t.UTC()
			v.LastInput = &t
		}
		out = append(out, v)
	}
	return out
}

// handleIdentity names a device or gives it a colour.
func (m *Module) handleIdentity(_ context.Context, a sdk.Action) (sdk.Result, error) {
	var args struct {
		ID, Name, Colour string
	}
	if err := a.DecodeArgs(&args); err != nil {
		return sdk.Result{}, err
	}
	var found bool
	for _, d := range m.readDevices() {
		found = found || d.ID == args.ID
	}
	if !found {
		return sdk.Result{}, sdk.Errorf(sdk.CodeNotFound, "no input device %q (hostctl devices)", args.ID)
	}
	if a.Type == "device.colour" {
		if !m.opts.Identities.SetColour(args.ID, args.Colour) {
			return sdk.Result{}, sdk.Errorf(sdk.CodeInvalidArgs, "%q is not a colour: use one of %s", args.Colour, strings.Join(evdev.Colours, ", "))
		}
	} else {
		args.Name = m.opts.Identities.SetName(args.ID, args.Name)
	}
	m.mu.Lock()
	core := m.core
	m.mu.Unlock()
	if core != nil {
		core.Emit(sdk.Event{Type: EventDeviceChanged, Action: a.ID, Data: sdk.MustJSON(m.deviceByID(args.ID))})
	}
	return sdk.Result{Data: sdk.MustJSON(m.deviceByID(args.ID))}, nil
}

func (m *Module) deviceByID(id string) *DeviceView {
	for _, d := range m.readDevices() {
		if d.ID == id {
			return &d
		}
	}
	return nil
}

// handleCalibrate starts a pointer's calibration, or clears it.
func (m *Module) handleCalibrate(_ context.Context, a sdk.Action) (sdk.Result, error) {
	var args struct {
		ID    string
		Reset bool
	}
	if err := a.DecodeArgs(&args); err != nil {
		return sdk.Result{}, err
	}
	d := m.deviceByID(args.ID)
	if d == nil {
		return sdk.Result{}, sdk.Errorf(sdk.CodeNotFound, "no input device %q (hostctl devices)", args.ID)
	}
	c, ok := m.opts.Input.(evdev.Calibrator)
	if !ok || !d.Pointer || d.Kind != evdev.KindGamepad {
		return sdk.Result{}, sdk.Errorf(sdk.CodeInvalidArgs, "%s has no pointer to calibrate (only a Wii Remote's camera needs it)", d.Label)
	}
	if err := c.Calibrate(args.ID, args.Reset); err != nil {
		return sdk.Result{}, sdk.Errorf(sdk.CodeNotFound, "%v", err)
	}
	return sdk.Result{Data: sdk.MustJSON(m.deviceByID(args.ID))}, nil
}

// calibration tells the menu how a calibration goes.
func (m *Module) calibration(ev evdev.InputEvent) {
	m.mu.Lock()
	core := m.core
	m.mu.Unlock()
	c := ev.Calibrate
	if core == nil {
		return
	}
	name, colour := m.pointerLabel(ev.Device, ev.Family, ev.Player)
	data := map[string]any{"device": ev.Device, "state": c.State, "step": c.Step, "name": name, "colour": colour}
	if c.State == "aim" && c.Step >= 0 && c.Step < len(evdev.Corners) {
		data["corner"] = evdev.Corners[c.Step]
	}
	if c.Reason != "" {
		data["reason"] = c.Reason
	}
	if c.Fit != nil {
		data["calibration"] = c.Fit
	}
	core.Emit(sdk.Event{Type: EventCalibrate, Data: sdk.MustJSON(data), Source: &sdk.Source{Kind: sdk.SourceLocal, Name: "controller"}})
	if c.State == "done" || c.State == "reset" {
		core.Emit(sdk.Event{Type: EventDeviceChanged, Data: sdk.MustJSON(m.deviceByID(ev.Device))})
	}
}

// hear notes that a device with no input node is here or gone, and says so.
func (m *Module) hear(ev evdev.InputEvent) {
	m.mu.Lock()
	_, was := m.here[ev.Device]
	if ev.Presence == "here" {
		m.here[ev.Device] = hereDevice{Family: ev.Family, Player: ev.Player}
	} else {
		delete(m.here, ev.Device)
	}
	core := m.core
	m.mu.Unlock()
	if core == nil || was == (ev.Presence == "here") {
		return
	}
	src := &sdk.Source{Kind: sdk.SourceExternal, Name: "input"}
	if ev.Presence == "here" {
		core.Emit(sdk.Event{Type: EventDeviceConnected, Data: sdk.MustJSON(m.deviceByID(ev.Device)), Source: src})
		return
	}
	d := m.opts.Identities.Get(ev.Device)
	core.Emit(sdk.Event{Type: EventDeviceDisconnected, Data: sdk.MustJSON(map[string]any{"id": ev.Device, "label": d.Name, "colour": d.Colour, "player": ev.Player}), Source: src})
}

// handleAssign gives a controller a player slot.
func (m *Module) handleAssign(_ context.Context, a sdk.Action) (sdk.Result, error) {
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
