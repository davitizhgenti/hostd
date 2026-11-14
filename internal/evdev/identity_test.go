package evdev

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestIdentitiesNameColourAndKeep(t *testing.T) {
	f := filepath.Join(t.TempDir(), "devices.json")
	s := &Identities{File: f}
	if err := s.Load(); err != nil {
		t.Fatal(err)
	}
	a, b := s.Get("a"), s.Get("b")
	if a.Colour != "blue" || b.Colour != "orange" {
		t.Errorf("each device has its own colour: %q %q", a.Colour, b.Colour)
	}
	if !s.SetColour("a", "teal") || s.SetColour("a", "mauve") {
		t.Error("colours are from the palette")
	}
	if got := s.SetName("a", "  Dad\x07's remote "+strings.Repeat("x", 40)); len([]rune(got)) > MaxName || strings.ContainsRune(got, 7) || !strings.HasPrefix(got, "Dad's remote") {
		t.Errorf("name cleaned: %q", got)
	}
	s.SetName("b", "Mia")
	s.SetCalibration("b", &Calibration{CX: 0.1, W: 4, H: 2.5})
	// A new colour is the first one nobody has.
	if c := s.Get("c").Colour; c != "blue" {
		t.Errorf("c gets the freed colour: %q", c)
	}
	// Kept across restarts.
	r := &Identities{File: f}
	if err := r.Load(); err != nil {
		t.Fatal(err)
	}
	if x := r.Get("b"); x.Name != "Mia" || x.Colour != "orange" || r.Calibration("b").W != 4 {
		t.Errorf("kept: %+v %+v", x, r.Calibration("b"))
	}
	if r.Get("a").Colour != "teal" {
		t.Error("colour kept")
	}
	r.SetName("b", "")
	r.SetCalibration("b", nil)
	if r.Get("b").Name != "" || r.Calibration("b").W != 0 {
		t.Error("reset")
	}
	if (*Identities)(nil).Calibration("x").W != 0 {
		t.Error("nil store")
	}
}
