package audio

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func TestGraphFromCore(t *testing.T) {
	data, err := os.ReadFile("testdata/pw-dump-core.json")
	if err != nil {
		t.Fatal(err)
	}
	g, err := parseGraph(data)
	if err != nil {
		t.Fatal(err)
	}
	want := []Output{{Name: "hdmi", Description: "GP107GL High Definition Audio Controller Digital Stereo (HDMI)",
		Node: "alsa_output.pci-0000_01_00.1.hdmi-stereo", ID: 54, Default: true, Percent: 50}}
	if !reflect.DeepEqual(g.Outputs, want) || len(g.Streams) != 0 {
		t.Fatalf("graph %+v", g)
	}
}

// A machine with speakers, a Bluetooth headset, a USB headset and two HDMI
// ports, and two apps playing.
const richDump = `[
 {"id": 10, "type": "PipeWire:Interface:Device", "info": {"props": {"media.class": "Audio/Device", "device.api": "alsa", "device.bus": "pci", "device.description": "Built-in Audio"}}},
 {"id": 11, "type": "PipeWire:Interface:Device", "info": {"props": {"media.class": "Audio/Device", "device.api": "bluez5", "device.description": "WH-1000XM4"}}},
 {"id": 12, "type": "PipeWire:Interface:Device", "info": {"props": {"media.class": "Audio/Device", "device.api": "alsa", "device.bus": "usb", "device.description": "Arctis 7"}}},
 {"id": 13, "type": "PipeWire:Interface:Device", "info": {"props": {"media.class": "Audio/Device", "device.api": "alsa", "device.bus": "pci", "device.description": "HDA NVidia"}}},
 {"id": 20, "type": "PipeWire:Interface:Node", "info": {"props": {"media.class": "Audio/Sink", "node.name": "alsa_output.pci-0000_00_1f.3.analog-stereo", "node.description": "Built-in Audio Analog Stereo", "device.id": 10, "device.profile.name": "analog-stereo"},
   "params": {"Props": [{"channelVolumes": [1.0, 1.0], "mute": false}]}}},
 {"id": 21, "type": "PipeWire:Interface:Node", "info": {"props": {"media.class": "Audio/Sink", "node.name": "bluez_output.AA_BB.1", "node.description": "WH-1000XM4", "device.id": 11, "device.api": "bluez5"},
   "params": {"Props": [{"channelVolumes": [0.216], "mute": true}]}}},
 {"id": 22, "type": "PipeWire:Interface:Node", "info": {"props": {"media.class": "Audio/Sink", "node.name": "alsa_output.usb-SteelSeries_Arctis_7.analog-stereo", "node.description": "Arctis 7 Analog Stereo", "device.id": 12}}},
 {"id": 23, "type": "PipeWire:Interface:Node", "info": {"props": {"media.class": "Audio/Sink", "node.name": "alsa_output.pci-0000_01_00.1.hdmi-stereo", "device.id": 13, "device.profile.name": "hdmi-stereo"}}},
 {"id": 24, "type": "PipeWire:Interface:Node", "info": {"props": {"media.class": "Audio/Sink", "node.name": "alsa_output.pci-0000_01_00.1.hdmi-stereo-extra1", "device.id": 13, "device.profile.name": "hdmi-stereo-extra1"}}},
 {"id": 30, "type": "PipeWire:Interface:Node", "info": {"props": {"media.class": "Stream/Output/Audio", "application.process.id": 4242, "application.name": "Chromium"},
   "params": {"Props": [{"channelVolumes": [0.125, 0.125], "mute": false}]}}},
 {"id": 31, "type": "PipeWire:Interface:Node", "info": {"props": {"media.class": "Stream/Output/Audio", "application.process.id": "666", "pipewire.sec.pid": 5151, "application.name": "game.exe"}}},
 {"id": 40, "type": "PipeWire:Interface:Metadata", "props": {"metadata.name": "default"}, "metadata": [{"subject": 0, "key": "default.audio.sink", "type": "Spa:String:JSON", "value": {"name": "bluez_output.AA_BB.1"}}]}
]`

func TestGraphNames(t *testing.T) {
	g, err := parseGraph([]byte(richDump))
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]Output{}
	for _, o := range g.Outputs {
		names[o.Name] = o
	}
	for _, n := range []string{"speakers", "bt-wh-1000xm4", "usb-arctis-7", "hdmi-1", "hdmi-2"} {
		if _, ok := names[n]; !ok {
			t.Errorf("no output %q in %v", n, g.Outputs)
		}
	}
	if o := names["bt-wh-1000xm4"]; !o.Default || !o.Muted || o.Percent != 60 {
		t.Errorf("headset %+v", o)
	}
	if names["hdmi-1"].Node != "alsa_output.pci-0000_01_00.1.hdmi-stereo" {
		t.Errorf("HDMI order: %+v", names["hdmi-1"])
	}
	if names["speakers"].Percent != 100 {
		t.Errorf("speakers %+v", names["speakers"])
	}
	want := []Stream{{ID: 30, PID: 4242, App: "Chromium", Percent: 50}, {ID: 31, PID: 5151, App: "game.exe"}}
	if !reflect.DeepEqual(g.Streams, want) {
		t.Errorf("streams %+v", g.Streams)
	}
	// Same devices, different PipeWire IDs (a reconnect): same names.
	again, _ := parseGraph([]byte(richDump))
	for i := range again.Outputs {
		if again.Outputs[i].Name != g.Outputs[i].Name {
			t.Errorf("names changed: %v / %v", again.Outputs, g.Outputs)
		}
	}
}

// A native PipeWire client (pw-cat, an SDL game) names its process only on
// its client object; the stream node points there with client.id. A node
// that carries a process itself (through the PulseAudio layer) keeps it.
func TestStreamPIDFromItsClient(t *testing.T) {
	g, err := parseGraph([]byte(`[
 {"id": 55, "type": "PipeWire:Interface:Client", "info": {"props": {"pipewire.sec.pid": 23828, "application.process.id": 23828, "application.name": "pw-cat"}}},
 {"id": 56, "type": "PipeWire:Interface:Node", "info": {"props": {"media.class": "Stream/Output/Audio", "client.id": 55, "application.name": "pw-cat"}}},
 {"id": 57, "type": "PipeWire:Interface:Client", "info": {"props": {"pipewire.sec.pid": 1}}},
 {"id": 58, "type": "PipeWire:Interface:Node", "info": {"props": {"media.class": "Stream/Output/Audio", "client.id": 57, "pipewire.sec.pid": 4242, "application.name": "Chromium"}}},
 {"id": 59, "type": "PipeWire:Interface:Node", "info": {"props": {"media.class": "Stream/Output/Audio", "client.id": 99, "application.name": "orphan"}}}
]`))
	if err != nil {
		t.Fatal(err)
	}
	want := []Stream{{ID: 56, PID: 23828, App: "pw-cat"}, {ID: 58, PID: 4242, App: "Chromium"}, {ID: 59, App: "orphan"}}
	if !reflect.DeepEqual(g.Streams, want) {
		t.Fatalf("streams %+v", g.Streams)
	}
}

// A stream names the output its link goes to. An output whose card has
// more than one connector for it names the one in use; the name stays.
func TestStreamOutputAndPort(t *testing.T) {
	dump := `[
 {"id": 48, "type": "PipeWire:Interface:Device", "info": {"props": {"device.description": "GP107GL"}}},
 {"id": 49, "type": "PipeWire:Interface:Device", "info": {"props": {"device.description": "Ryzen HD Audio Controller"},
  "params": {"Route": [{"direction": "Input", "description": "Front Microphone", "devices": [6]},
                       {"direction": "Output", "description": "Headphones", "devices": [4]}]}}},
 {"id": 54, "type": "PipeWire:Interface:Node", "info": {"props": {"media.class": "Audio/Sink", "device.id": 48,
  "node.name": "alsa_output.pci-0000_01_00.1.hdmi-stereo", "device.profile.name": "hdmi-stereo", "card.profile.device": 4, "device.routes": 1}}},
 {"id": 60, "type": "PipeWire:Interface:Node", "info": {"props": {"media.class": "Audio/Sink", "device.id": 49,
  "node.name": "alsa_output.pci-0000_04_00.6.analog-stereo", "device.profile.name": "analog-stereo", "card.profile.device": 4, "device.routes": 2}}},
 {"id": 70, "type": "PipeWire:Interface:Node", "info": {"props": {"media.class": "Stream/Output/Audio", "application.name": "pw-cat"}}},
 {"id": 71, "type": "PipeWire:Interface:Node", "info": {"props": {"media.class": "Stream/Output/Audio", "application.name": "quiet"}}},
 {"id": 80, "type": "PipeWire:Interface:Link", "info": {"output-node-id": 70, "input-node-id": 60}},
 {"id": 81, "type": "PipeWire:Interface:Link", "info": {"output-node-id": 70, "input-node-id": 60}}
]`
	g, err := parseGraph([]byte(dump))
	if err != nil {
		t.Fatal(err)
	}
	hdmi, _ := g.Output("hdmi")
	speakers, _ := g.Output("speakers")
	if hdmi.Port != "" || speakers.Port != "Headphones" {
		t.Fatalf("ports: hdmi %q, speakers %q", hdmi.Port, speakers.Port)
	}
	if len(g.Streams) != 2 || g.Streams[0].Output != "speakers" || g.Streams[1].Output != "" {
		t.Fatalf("streams %+v", g.Streams)
	}

	// The cache that follows pw-dump --monitor keeps the links too.
	var c graphCache
	var raw []json.RawMessage
	var objs []pwObject
	_ = json.Unmarshal([]byte(dump), &raw)
	_ = json.Unmarshal([]byte(dump), &objs)
	c.apply(raw, objs)
	cached, ok := c.read()
	if !ok || cached.Streams[0].Output != "speakers" {
		t.Fatalf("cached streams %+v", cached.Streams)
	}
}
