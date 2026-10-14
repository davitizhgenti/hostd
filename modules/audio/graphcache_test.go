package audio

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// feed gives the cache a batch as the monitor would.
func feed(t *testing.T, c *graphCache, batch string) {
	t.Helper()
	var raw []json.RawMessage
	if err := json.Unmarshal([]byte(batch), &raw); err != nil {
		t.Fatal(err)
	}
	objs := make([]pwObject, len(raw))
	for i, r := range raw {
		if err := json.Unmarshal(r, &objs[i]); err != nil {
			t.Fatal(err)
		}
	}
	c.apply(raw, objs)
}

func readGraph(t *testing.T, c *graphCache) Graph {
	t.Helper()
	g, ok := c.read()
	if !ok {
		t.Fatal("the cache has no graph")
	}
	return g
}

func TestGraphCacheMatchesADump(t *testing.T) {
	var c graphCache
	if _, ok := c.read(); ok {
		t.Fatal("a graph before any dump")
	}
	feed(t, &c, richDump)
	want, err := parseGraph([]byte(richDump))
	if err != nil {
		t.Fatal(err)
	}
	if got := readGraph(t, &c); !reflect.DeepEqual(got, want) {
		t.Fatalf("cache %+v\nwant  %+v", got, want)
	}
}

func TestGraphCacheFollowsChanges(t *testing.T) {
	var c graphCache
	feed(t, &c, richDump)

	// A node that changed comes whole: the speakers go to 50%.
	feed(t, &c, `[{"id": 20, "type": "PipeWire:Interface:Node", "info": {"props": {"media.class": "Audio/Sink", "node.name": "alsa_output.pci-0000_00_1f.3.analog-stereo", "node.description": "Built-in Audio Analog Stereo", "device.id": 10, "device.profile.name": "analog-stereo"},
	  "params": {"Props": [{"channelVolumes": [0.125, 0.125], "mute": false}]}}}]`)
	if o, _ := readGraph(t, &c).Output("speakers"); o.Percent != 50 {
		t.Errorf("speakers after a volume change: %+v", o)
	}

	// A metadata object carries only the key that changed: another key
	// leaves the default alone, the default's own key moves it.
	feed(t, &c, `[{"id": 40, "type": "PipeWire:Interface:Metadata", "metadata": [{"subject": 0, "key": "something.else", "value": 1}]}]`)
	if o, _ := readGraph(t, &c).Output("bt-wh-1000xm4"); !o.Default {
		t.Errorf("an unrelated key moved the default: %+v", o)
	}
	feed(t, &c, `[{"id": 40, "metadata": [{"subject": 0, "key": "default.audio.sink", "type": "Spa:String:JSON", "value": {"name": "alsa_output.pci-0000_00_1f.3.analog-stereo"}}]}]`)
	g := readGraph(t, &c)
	if o, _ := g.Output("speakers"); !o.Default {
		t.Errorf("the default did not move: %+v", g.Outputs)
	}
	if o, _ := g.Output("bt-wh-1000xm4"); o.Default {
		t.Errorf("two defaults: %+v", g.Outputs)
	}

	// A stream starts, and one ends; an object that was never kept ends too.
	feed(t, &c, `[{"id": 32, "type": "PipeWire:Interface:Node", "info": {"props": {"media.class": "Stream/Output/Audio", "pipewire.sec.pid": 77, "application.name": "mpv"}}},
	  {"id": 99, "type": "PipeWire:Interface:Link", "info": {}}]`)
	feed(t, &c, `[{"id": 30, "info": null}, {"id": 99, "info": null}]`)
	var ids []int
	for _, s := range readGraph(t, &c).Streams {
		ids = append(ids, s.ID)
	}
	if !reflect.DeepEqual(ids, []int{31, 32}) {
		t.Errorf("streams %v", ids)
	}

	// An output goes away (the headset's node is removed by ID alone).
	feed(t, &c, `[{"id": 21, "info": null}]`)
	if _, ok := readGraph(t, &c).Output("bt-wh-1000xm4"); ok {
		t.Error("a removed output is still there")
	}
}

func TestGraphCacheResetAndCopies(t *testing.T) {
	var c graphCache
	feed(t, &c, richDump)
	g := readGraph(t, &c)
	g.Streams[0].Instance = "edited" // as the module does
	g.Outputs[0].Name = "edited"
	again := readGraph(t, &c)
	if again.Streams[0].Instance != "" || again.Outputs[0].Name == "edited" {
		t.Fatal("a caller's edit reached the cache")
	}
	c.reset()
	if _, ok := c.read(); ok {
		t.Fatal("a graph after the monitor went away")
	}
	feed(t, &c, richDump)
	readGraph(t, &c)
}

// With the monitor running the backend answers from memory, and asks
// pw-dump only before the first dump and after the monitor is gone.
func TestWirePlumberReadsFromTheMonitor(t *testing.T) {
	dir := t.TempDir()
	dump := filepath.Join(dir, "dump.json")
	if err := os.WriteFile(dump, []byte(richDump), 0o644); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
if [ "$1" = "--monitor" ]; then
  cat "$AUDIO_DIR/dump.json"
  printf '[{"id": 20, "type": "PipeWire:Interface:Node", "info": {"props": {"media.class": "Audio/Sink", "node.name": "alsa_output.pci-0000_00_1f.3.analog-stereo", "device.id": 10, "device.profile.name": "analog-stereo"}, "params": {"Props": [{"channelVolumes": [0.125], "mute": false}]}}}]\n'
  exec sleep 30
else
  echo run >> "$AUDIO_DIR/forks"
  cat "$AUDIO_DIR/dump.json"
fi
`
	if err := os.WriteFile(filepath.Join(dir, "pw-dump"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	forks := func() int {
		b, _ := os.ReadFile(filepath.Join(dir, "forks"))
		return strings.Count(string(b), "run")
	}
	w := &WirePlumber{Env: []string{"AUDIO_DIR=" + dir}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if _, err := w.Graph(ctx); err != nil || forks() != 1 {
		t.Fatalf("before the monitor: %d forks, %v", forks(), err)
	}

	cues := make(chan struct{}, 8)
	done := make(chan error, 1)
	go func() { done <- w.Watch(ctx, func() { cues <- struct{}{} }) }()
	for i := 0; i < 2; i++ { // the dump and the volume change
		select {
		case <-cues:
		case <-time.After(5 * time.Second):
			t.Fatal("the monitor sent nothing")
		}
	}
	for i := 0; i < 3; i++ {
		g, err := w.Graph(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if o, _ := g.Output("speakers"); o.Percent != 50 {
			t.Fatalf("speakers %+v: the change did not reach the cache", o)
		}
	}
	if forks() != 1 {
		t.Fatalf("%d pw-dump forks with the monitor running, want the 1 from before it", forks())
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Watch did not stop")
	}
	if _, err := w.Graph(context.Background()); err != nil || forks() != 2 {
		t.Fatalf("after the monitor: %d forks, %v", forks(), err)
	}
}

// The cache keeps client objects too: a native stream finds its process
// through one, and a client that goes away takes that with it.
func TestGraphCacheKeepsClients(t *testing.T) {
	var c graphCache
	feed(t, &c, `[{"id": 55, "type": "PipeWire:Interface:Client", "info": {"props": {"pipewire.sec.pid": 23828}}},
	  {"id": 56, "type": "PipeWire:Interface:Node", "info": {"props": {"media.class": "Stream/Output/Audio", "client.id": 55}}}]`)
	if s := readGraph(t, &c).Streams; len(s) != 1 || s[0].PID != 23828 {
		t.Fatalf("streams %+v", s)
	}
	feed(t, &c, `[{"id": 55, "info": null}]`)
	if s := readGraph(t, &c).Streams; len(s) != 1 || s[0].PID != 0 {
		t.Fatalf("after the client left: %+v", s)
	}
}
