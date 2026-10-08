package display

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/fsnotify/fsnotify"

	"github.com/davitizhgenti/hostd/sdk"
)

// Controllers are described by profiles: how to recognise a family of
// devices, and what their buttons mean to hostd. hostd ships some
// (controllers/*.toml, embedded); files in ~/.config/hostd/controllers add
// to them or replace one with the same name. A gamepad no profile matches
// gets the generic one. A new kind of controller is a new file, not code.

//go:embed controllers/*.toml
var builtinProfiles embed.FS

// ButtonNames are the buttons hostd can bind (see Bindings): meanings, not
// codes. A profile says which evdev key each one is on its devices.
var ButtonNames = []string{"guide", "start", "select"}

// Profile describes a family of controllers.
type Profile struct {
	ID      string            `toml:"-" json:"id"` // the file name
	Name    string            `toml:"name" json:"name"`
	Match   ProfileMatch      `toml:"match" json:"match"`
	Buttons map[string]string `toml:"buttons" json:"buttons,omitempty"` // button -> evdev key ("BTN_MODE")
	// Raw: the device has no input device, only raw HID that its app
	// reads (App); hostd lists it but cannot see its buttons.
	Raw bool   `toml:"raw" json:"raw,omitempty"`
	App string `toml:"app" json:"app,omitempty"`

	codes map[uint16]string // evdev code -> button
}

// ProfileMatch recognises devices: by USB vendor (and products), by name,
// or both. All that are set must match.
type ProfileMatch struct {
	Vendor   string   `toml:"vendor" json:"vendor,omitempty"`     // hex, e.g. "045e"
	Products []string `toml:"products" json:"products,omitempty"` // hex; empty: any of the vendor
	Name     string   `toml:"name" json:"name,omitempty"`         // glob on the device name
}

// genericID names the profile for gamepads no profile knows: Linux
// drivers put the Guide/Home button on BTN_MODE.
const genericID = "gamepad"

func newGenericProfile() *Profile {
	p := &Profile{ID: genericID, Name: "Game controller",
		Buttons: map[string]string{"guide": "BTN_MODE", "start": "BTN_START", "select": "BTN_SELECT"}}
	if err := p.prepare(); err != nil {
		panic(err) // the definition above is wrong
	}
	return p
}

// keyCodes are the evdev key names a profile may use.
var keyCodes = map[string]uint16{
	"BTN_SOUTH": 0x130, "BTN_EAST": 0x131, "BTN_NORTH": 0x133, "BTN_WEST": 0x134,
	"BTN_TL": 0x136, "BTN_TR": 0x137, "BTN_TL2": 0x138, "BTN_TR2": 0x139,
	"BTN_SELECT": 0x13a, "BTN_START": 0x13b, "BTN_MODE": 0x13c,
	"BTN_THUMBL": 0x13d, "BTN_THUMBR": 0x13e, "KEY_HOMEPAGE": 0xac, "KEY_MENU": 0x8b,
}

func (p *Profile) prepare() error {
	if p.Name == "" {
		return errors.New("name is required")
	}
	if !p.Raw && p.Match.Vendor == "" && p.Match.Name == "" && p.ID != genericID {
		return errors.New("match needs a vendor or a name")
	}
	if p.Match.Name != "" {
		if _, err := path.Match(p.Match.Name, ""); err != nil {
			return fmt.Errorf("match.name %q: %w", p.Match.Name, err)
		}
	}
	p.codes = map[uint16]string{}
	for button, key := range p.Buttons {
		if !slices.Contains(ButtonNames, button) {
			return fmt.Errorf("button %q: use one of %s", button, strings.Join(ButtonNames, ", "))
		}
		code, ok := keyCodes[key]
		if !ok {
			if n, err := strconv.ParseUint(key, 0, 16); err == nil {
				code, ok = uint16(n), true
			}
		}
		if !ok {
			return fmt.Errorf("button %q: unknown key %q (an evdev name such as BTN_MODE, or a number)", button, key)
		}
		p.codes[code] = button
	}
	return nil
}

func (p *Profile) matches(d deviceInfo) bool {
	m := p.Match
	if m.Vendor != "" && !strings.EqualFold(m.Vendor, d.Vendor) {
		return false
	}
	if len(m.Products) > 0 && !slices.ContainsFunc(m.Products, func(x string) bool { return strings.EqualFold(x, d.Product) }) {
		return false
	}
	if m.Name != "" {
		if ok, _ := path.Match(m.Name, d.Name); !ok {
			return false
		}
	}
	return true
}

// Profiles is a set of controller profiles.
type Profiles struct {
	list    []*Profile
	generic *Profile
}

// LoadProfiles reads the built-in profiles, then dir (missing is fine).
// A file with a built-in's name replaces it.
func LoadProfiles(dir string) (*Profiles, error) {
	byID := map[string]*Profile{}
	read := func(fsys fs.FS, name string) error {
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			return err
		}
		p := &Profile{ID: strings.TrimSuffix(path.Base(name), ".toml")}
		md, err := toml.Decode(string(data), p)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if und := md.Undecoded(); len(und) > 0 {
			return fmt.Errorf("%s: unknown key %s", name, und[0])
		}
		if err := p.prepare(); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		byID[p.ID] = p
		return nil
	}
	names, _ := fs.Glob(builtinProfiles, "controllers/*.toml")
	for _, n := range names {
		if err := read(builtinProfiles, n); err != nil {
			return nil, err
		}
	}
	if dir != "" {
		files, _ := filepath.Glob(filepath.Join(dir, "*.toml"))
		for _, f := range files {
			if err := read(os.DirFS(dir), filepath.Base(f)); err != nil {
				return nil, fmt.Errorf("controller profile %w", err)
			}
		}
	}
	ps := &Profiles{generic: newGenericProfile()}
	for _, p := range byID {
		ps.list = append(ps.list, p)
	}
	// Specific before general: product lists, then vendors, then names.
	sort.Slice(ps.list, func(i, j int) bool {
		a, b := ps.list[i], ps.list[j]
		if (len(a.Match.Products) > 0) != (len(b.Match.Products) > 0) {
			return len(a.Match.Products) > 0
		}
		return a.ID < b.ID
	})
	return ps, nil
}

// forInput finds the profile of an input device: a profile that matches,
// or the generic one for a gamepad, or nil (a keyboard, a mouse).
func (ps *Profiles) forInput(d deviceInfo) *Profile {
	for _, p := range ps.list {
		if !p.Raw && p.matches(d) {
			return p
		}
	}
	if d.Gamepad {
		return ps.generic
	}
	return nil
}

// forRaw finds the profile of a raw HID device, or nil.
func (ps *Profiles) forRaw(d deviceInfo) *Profile {
	for _, p := range ps.list {
		if p.Raw && p.matches(d) {
			return p
		}
	}
	return nil
}

// deviceInfo is what sysfs says about an input or hidraw device.
type deviceInfo struct {
	Name            string
	Vendor, Product string // 4 hex digits, lower case
	Gamepad         bool   // has gamepad or joystick buttons
}

// Controller is a connected controller, as GET /v1/controllers lists it.
type Controller struct {
	Device  string   `json:"device"` // /dev/input/event7, /dev/hidraw5
	Name    string   `json:"name"`
	Vendor  string   `json:"vendor"`
	Product string   `json:"product"`
	Profile string   `json:"profile"` // profile ID, "gamepad" for the generic one
	Kind    string   `json:"kind"`    // its profile's name
	Buttons []string `json:"buttons,omitempty"`
	// Raw devices: only their app reads them; hostd sees no buttons.
	Raw bool   `json:"raw,omitempty"`
	App string `json:"app,omitempty"`
}

// sysfs reads device information. root is "/" outside tests.
type sysfs struct{ root string }

func (s sysfs) read(p string) string {
	b, _ := os.ReadFile(filepath.Join(s.root, p))
	return strings.TrimSpace(string(b))
}

// input describes /dev/input/eventN.
func (s sysfs) input(dev string) deviceInfo {
	base := filepath.Join("sys/class/input", filepath.Base(dev), "device")
	d := deviceInfo{Name: s.read(filepath.Join(base, "name")),
		Vendor: strings.ToLower(s.read(filepath.Join(base, "id/vendor"))), Product: strings.ToLower(s.read(filepath.Join(base, "id/product")))}
	d.Gamepad = hasKeyBit(s.read(filepath.Join(base, "capabilities/key")), 0x130) || // BTN_GAMEPAD
		hasKeyBit(s.read(filepath.Join(base, "capabilities/key")), 0x120) // BTN_JOYSTICK
	return d
}

// hidraw describes /dev/hidrawN (HID_ID=0003:0000057E:00000306).
func (s sysfs) hidraw(dev string) deviceInfo {
	var d deviceInfo
	for _, line := range strings.Split(s.read(filepath.Join("sys/class/hidraw", filepath.Base(dev), "device/uevent")), "\n") {
		k, v, _ := strings.Cut(line, "=")
		switch k {
		case "HID_NAME":
			d.Name = v
		case "HID_ID":
			if parts := strings.Split(v, ":"); len(parts) == 3 && len(parts[1]) >= 4 && len(parts[2]) >= 4 {
				d.Vendor = strings.ToLower(parts[1][len(parts[1])-4:])
				d.Product = strings.ToLower(parts[2][len(parts[2])-4:])
			}
		}
	}
	return d
}

// hasKeyBit reads a capabilities/key bitmap: hex words, most significant
// first, 64 bits each.
func hasKeyBit(bitmap string, bit int) bool {
	words := strings.Fields(bitmap)
	i := len(words) - 1 - bit/64
	if i < 0 {
		return false
	}
	w, err := strconv.ParseUint(words[i], 16, 64)
	return err == nil && w&(1<<(bit%64)) != 0
}

// scanControllers lists the connected controllers: input devices that are
// gamepads or match a profile, and raw devices a profile knows.
func scanControllers(s sysfs, ps *Profiles) []Controller {
	var out []Controller
	events, _ := filepath.Glob(filepath.Join(s.root, "dev/input/event*"))
	for _, ev := range events {
		dev := "/dev/input/" + filepath.Base(ev)
		d := s.input(dev)
		p := ps.forInput(d)
		if p == nil || (!d.Gamepad && p.Match.Name == "") {
			continue // a keyboard of a known vendor is no controller
		}
		out = append(out, controllerOf(dev, d, p))
	}
	raws, _ := filepath.Glob(filepath.Join(s.root, "dev/hidraw*"))
	for _, r := range raws {
		dev := "/dev/" + filepath.Base(r)
		d := s.hidraw(dev)
		if p := ps.forRaw(d); p != nil {
			out = append(out, controllerOf(dev, d, p))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Device < out[j].Device })
	return out
}

func controllerOf(dev string, d deviceInfo, p *Profile) Controller {
	c := Controller{Device: dev, Name: d.Name, Vendor: d.Vendor, Product: d.Product,
		Profile: p.ID, Kind: p.Name, Raw: p.Raw, App: p.App}
	for _, b := range ButtonNames {
		if _, ok := p.Buttons[b]; ok {
			c.Buttons = append(c.Buttons, b)
		}
	}
	return c
}

// followControllers announces controllers as they come and go. It watches
// /dev/input and /dev (raw HID devices appear there), and lists again a
// moment after the last change, since one controller brings several
// device nodes.
func (m *Module) followControllers(ctx context.Context) {
	sys := sysfs{root: m.opts.SysRoot}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		m.log.Warn("cannot follow controllers", "err", err)
		return
	}
	defer w.Close()
	for _, d := range []string{"dev/input", "dev"} {
		if err := w.Add(filepath.Join(sys.root, d)); err != nil {
			m.log.Debug("cannot watch for controllers", "dir", d, "err", err)
		}
	}
	known := map[string]Controller{}
	for _, c := range scanControllers(sys, m.opts.Profiles) {
		known[c.Device] = c
	}
	var settle <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-w.Events:
			if !ok {
				return
			}
			base := filepath.Base(ev.Name)
			if strings.HasPrefix(base, "event") || strings.HasPrefix(base, "hidraw") {
				settle = m.opts.Clock.After(300 * time.Millisecond)
			}
		case <-w.Errors:
		case <-settle:
			settle = nil
			now := map[string]Controller{}
			for _, c := range scanControllers(sys, m.opts.Profiles) {
				now[c.Device] = c
				if _, ok := known[c.Device]; !ok {
					m.emitController(EventControllerConnected, c)
				}
			}
			for dev, c := range known {
				if _, ok := now[dev]; !ok {
					m.emitController(EventControllerDisconnected, c)
				}
			}
			known = now
		}
	}
}

func (m *Module) emitController(typ string, c Controller) {
	m.mu.Lock()
	core := m.core
	m.mu.Unlock()
	if core != nil {
		core.Emit(sdk.Event{Type: typ, Data: sdk.MustJSON(c), Source: &sdk.Source{Kind: sdk.SourceExternal, Name: "input"}})
	}
}
