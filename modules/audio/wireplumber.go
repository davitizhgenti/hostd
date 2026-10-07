package audio

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/davitizhgenti/hostd/sdk"
)

// Master is the default output's volume and mute state.
type Master struct {
	Percent     int    `json:"percent"` // 0-150
	Muted       bool   `json:"muted"`
	Sink        string `json:"sink"`        // node.name, e.g. "alsa_output.pci-0000_01_00.1.hdmi-stereo"
	Description string `json:"description"` // node.description, e.g. "GP107GL HDMI"
}

// Backend is the audio system. WirePlumber (wpctl, pw-dump) is the first
// implementation; a native PipeWire client can replace it without changing
// the module.
type Backend interface {
	Master(ctx context.Context) (Master, error)
	SetVolume(ctx context.Context, percent int) error
	SetMute(ctx context.Context, muted bool) error
	// Watch calls fn whenever something in the audio graph may have changed,
	// until ctx ends or the monitor stops (then it returns an error).
	Watch(ctx context.Context, fn func()) error
}

// WirePlumber drives PipeWire through wpctl, and notices outside changes
// with pw-dump --monitor.
type WirePlumber struct {
	// Env is added to the commands' environment (XDG_RUNTIME_DIR, so they
	// find the user's PipeWire).
	Env []string
}

const defaultSink = "@DEFAULT_AUDIO_SINK@"

// wpctl runs wpctl with args.
func (w *WirePlumber) wpctl(ctx context.Context, args ...string) ([]byte, error) {
	const name = "wpctl"
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(cmd.Environ(), w.Env...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.Error
		if errors.As(err, &ee) {
			return nil, sdk.Errorf(sdk.CodeModuleUnavailable, "%s is not installed", name)
		}
		msg := strings.TrimSpace(lastLine(stderr.String()))
		if msg == "" {
			msg = err.Error()
		}
		return nil, sdk.Errorf(sdk.CodeModuleUnavailable, "%s %s: %s", name, strings.Join(args, " "), msg)
	}
	return out, nil
}

// lastLine skips wpctl's warnings (e.g. RTKit) and keeps the actual error.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

func (w *WirePlumber) Master(ctx context.Context) (Master, error) {
	out, err := w.wpctl(ctx, "get-volume", defaultSink)
	if err != nil {
		return Master{}, err
	}
	m, err := parseVolume(out)
	if err != nil {
		return Master{}, err
	}
	if info, err := w.wpctl(ctx, "inspect", defaultSink); err == nil {
		props := parseInspect(info)
		m.Sink, m.Description = props["node.name"], props["node.description"]
	}
	return m, nil
}

func (w *WirePlumber) SetVolume(ctx context.Context, percent int) error {
	_, err := w.wpctl(ctx, "set-volume", defaultSink, fmt.Sprintf("%.2f", float64(percent)/100))
	return err
}

func (w *WirePlumber) SetMute(ctx context.Context, muted bool) error {
	v := "0"
	if muted {
		v = "1"
	}
	_, err := w.wpctl(ctx, "set-mute", defaultSink, v)
	return err
}

// Watch runs pw-dump --monitor, which prints a JSON array for every batch
// of changes; each one is a cue to read the state again.
func (w *WirePlumber) Watch(ctx context.Context, fn func()) error {
	cmd := exec.CommandContext(ctx, "pw-dump", "--monitor", "--no-colors")
	cmd.Env = append(cmd.Environ(), w.Env...)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 64*1024), 16<<20)
	for sc.Scan() {
		if sc.Text() == "[" { // a new batch begins
			fn()
		}
	}
	err = cmd.Wait()
	select {
	case <-ctx.Done(): // stopped on purpose
		return nil
	default:
	}
	if err == nil {
		err = errors.New("pw-dump exited")
	}
	return err
}

var reVolume = regexp.MustCompile(`^Volume: ([0-9]+(?:\.[0-9]+)?)( \[MUTED\])?$`)

// parseVolume reads `wpctl get-volume`: "Volume: 0.40" or "Volume: 0.40 [MUTED]".
func parseVolume(out []byte) (Master, error) {
	for _, line := range strings.Split(string(out), "\n") {
		m := reVolume.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		v, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			return Master{}, err
		}
		return Master{Percent: int(math.Round(v * 100)), Muted: m[2] != ""}, nil
	}
	return Master{}, fmt.Errorf("unexpected wpctl output: %q", strings.TrimSpace(string(out)))
}

// parseInspect reads `wpctl inspect` property lines: `  * node.name = "x"`.
func parseInspect(out []byte) map[string]string {
	props := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "*"))
		key, val, ok := strings.Cut(line, " = ")
		if !ok {
			continue
		}
		props[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(val), `"`)
	}
	return props
}
