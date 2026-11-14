package evdev

import (
	"encoding/json"
	"os"
	"strings"
	"sync"
	"unicode"

	"github.com/davitizhgenti/hostd/internal/fsx"
)

// Colours are the names of the colours a device's pointer and mark can
// have; the menu gives each its own shade (its tokens).
var Colours = []string{"blue", "orange", "green", "pink", "yellow", "teal", "red", "violet"}

// MaxName is the longest name a device can be given.
const MaxName = 24

// Identity is what the user chose for one device, and what hostd measured:
// a name, a colour, and the calibration of its pointer (zero: the default).
type Identity struct {
	Name   string       `json:"name,omitempty"`
	Colour string       `json:"colour,omitempty"`
	Cal    *Calibration `json:"calibration,omitempty"`
}

// Identities keep every device's Identity across restarts, by device ID
// (see DeviceID), so a remote is the same blue remote when it comes back.
type Identities struct {
	// File, if set, keeps them: read by Load, written when they change.
	File string

	mu sync.Mutex
	by map[string]Identity
}

// Load reads File (none: nothing to read). Call it before use.
func (s *Identities) Load() error {
	if s.File == "" {
		return nil
	}
	b, err := os.ReadFile(s.File)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var m map[string]Identity
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	s.mu.Lock()
	s.by = m
	s.mu.Unlock()
	return nil
}

// save writes them; s.mu is held.
func (s *Identities) save() {
	if s.File == "" {
		return
	}
	b, _ := json.MarshalIndent(s.by, "", "  ")
	_ = fsx.WriteFile(s.File, b, 0o644)
}

// Get is a device's identity; its colour is chosen (the first one no device
// has) and kept the first time it is asked for.
func (s *Identities) Get(id string) Identity {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.by == nil {
		s.by = map[string]Identity{}
	}
	x := s.by[id]
	if x.Colour == "" {
		used := map[string]bool{}
		for _, o := range s.by {
			used[o.Colour] = true
		}
		x.Colour = Colours[len(s.by)%len(Colours)]
		for _, c := range Colours {
			if !used[c] {
				x.Colour = c
				break
			}
		}
		s.by[id] = x
		s.save()
	}
	return x
}

// SetName names a device ("" gives it its default name again). It returns
// the cleaned name.
func (s *Identities) SetName(id, name string) string {
	name = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, name))
	if r := []rune(name); len(r) > MaxName {
		name = strings.TrimSpace(string(r[:MaxName]))
	}
	x := s.Get(id)
	s.mu.Lock()
	defer s.mu.Unlock()
	x.Name = name
	s.by[id] = x
	s.save()
	return name
}

// SetColour gives a device a colour from Colours; false if it is not one.
func (s *Identities) SetColour(id, colour string) bool {
	ok := false
	for _, c := range Colours {
		ok = ok || c == colour
	}
	if !ok {
		return false
	}
	x := s.Get(id)
	s.mu.Lock()
	defer s.mu.Unlock()
	x.Colour = colour
	s.by[id] = x
	s.save()
	return true
}

// SetCalibration keeps a device's calibration (nil: back to the default).
func (s *Identities) SetCalibration(id string, c *Calibration) {
	x := s.Get(id)
	s.mu.Lock()
	defer s.mu.Unlock()
	x.Cal = c
	s.by[id] = x
	s.save()
}

// Calibration is a device's, the zero value when it has none.
func (s *Identities) Calibration(id string) Calibration {
	if s == nil {
		return Calibration{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.by[id].Cal; c != nil {
		return *c
	}
	return Calibration{}
}
