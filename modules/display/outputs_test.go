package display

import (
	"context"
	"strings"
	"testing"
)

func TestOutputsAreSetOnAttach(t *testing.T) {
	b := newFakeBackend()
	m := New(Options{Outputs: map[string]OutputConfig{"HDMI-A-1": {Mode: "1920x1080@60"}, "*": {Mode: "1280x800"}}})
	m.applyOutputs(context.Background(), b)
	got := strings.Join(b.commands(), "\n")
	// HDMI-A-1 has its own entry (1920x1080@60); DP-1 takes "*" but lists no
	// modes, so the request is taken as it is.
	if !strings.Contains(got, "output HDMI-A-1 mode 1920x1080@60.000Hz") {
		t.Errorf("commands:\n%s", got)
	}
	if !strings.Contains(got, "output DP-1 mode 1280x800") {
		t.Errorf("commands:\n%s", got)
	}
	// Nothing configured: nothing set.
	b2 := newFakeBackend()
	New(Options{}).applyOutputs(context.Background(), b2)
	if c := b2.commands(); len(c) != 0 {
		t.Errorf("commands: %v", c)
	}
	// A mode the screen has already, given without a rate, is left alone.
	b3 := newFakeBackend()
	New(Options{Outputs: map[string]OutputConfig{"DP-1": {Mode: "1920x1080"}}}).applyOutputs(context.Background(), b3)
	if c := b3.commands(); len(c) != 0 {
		t.Errorf("DP-1 is at 1920x1080 already: %v", c)
	}
}

func TestOutputsConfigCheck(t *testing.T) {
	c := Config{Outputs: map[string]OutputConfig{"HDMI-A-1": {Mode: "big"}}}
	if err := c.Check(); err == nil || !strings.Contains(err.Error(), "outputs.HDMI-A-1") {
		t.Errorf("check: %v", err)
	}
	c.Outputs["HDMI-A-1"] = OutputConfig{Mode: "1920x1080@60"}
	if err := c.Check(); err != nil {
		t.Errorf("check: %v", err)
	}
}
