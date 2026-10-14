package audio

import (
	"bytes"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// graphCache is the audio graph kept from the pw-dump --monitor stream,
// so reading it needs no pw-dump of its own.
//
// What the stream sends: the first batch is the whole graph. After that a
// batch has every object that changed, each in full (its props and all its
// params), and {"id": N, "info": null} for one that went away. The one
// exception is a metadata object, which carries only the keys that
// changed, so those are merged by key.
//
// Only what parseGraph reads is kept: nodes, devices and the default sink.
type graphCache struct {
	mu      sync.Mutex
	ready   bool                    // the first full dump has arrived
	types   map[int]string          // object ID -> type, to judge removals
	objs    map[int]json.RawMessage // nodes and devices, as pw-dump printed them
	defSink map[int]json.RawMessage // metadata ID -> value of default.audio.sink
	graph   *Graph                  // parsed objs, until the next change
}

// reset forgets everything: the monitor is gone, so what was kept may be
// stale. Reads fall back to a pw-dump of their own until it is back.
func (c *graphCache) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ready, c.types, c.objs, c.defSink, c.graph = false, nil, nil, nil, nil
}

// apply takes one batch of the stream: raw are its objects, batch the same
// objects decoded.
func (c *graphCache) apply(raw []json.RawMessage, batch []pwObject) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.types == nil {
		c.types, c.objs, c.defSink = map[int]string{}, map[int]json.RawMessage{}, map[int]json.RawMessage{}
	}
	for i, o := range batch {
		typ := o.Type
		if typ != "" {
			c.types[o.ID] = typ
		} else {
			typ = c.types[o.ID]
		}
		if string(o.Info) == "null" { // gone
			delete(c.types, o.ID)
			delete(c.objs, o.ID)
			delete(c.defSink, o.ID)
			continue
		}
		switch {
		case strings.HasSuffix(typ, ":Node"), strings.HasSuffix(typ, ":Device"):
			c.objs[o.ID] = slices.Clone(raw[i])
		case strings.HasSuffix(typ, ":Metadata"):
			c.mergeMetadata(o.ID, raw[i])
		}
	}
	c.ready, c.graph = true, nil
}

// mergeMetadata notes the default sink out of a metadata object's entries.
func (c *graphCache) mergeMetadata(id int, raw json.RawMessage) {
	var m struct {
		Metadata []struct {
			Key   string          `json:"key"`
			Value json.RawMessage `json:"value"`
		} `json:"metadata"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return
	}
	for _, e := range m.Metadata {
		if e.Key != "default.audio.sink" {
			continue
		}
		if len(e.Value) == 0 || string(e.Value) == "null" {
			delete(c.defSink, id)
		} else {
			c.defSink[id] = slices.Clone(e.Value)
		}
	}
}

// read returns the graph, or false when the monitor has not delivered one.
func (c *graphCache) read() (Graph, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.ready {
		return Graph{}, false
	}
	if c.graph == nil {
		var b bytes.Buffer
		b.WriteByte('[')
		ids := make([]int, 0, len(c.objs))
		for id := range c.objs {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		for _, id := range ids {
			b.Write(c.objs[id])
			b.WriteByte(',')
		}
		for id, v := range c.defSink {
			b.WriteString(`{"id":` + strconv.Itoa(id) + `,"type":"PipeWire:Interface:Metadata","metadata":[{"key":"default.audio.sink","value":`)
			b.Write(v)
			b.WriteString(`}]},`)
		}
		out := bytes.TrimSuffix(b.Bytes(), []byte(","))
		g, err := parseGraph(append(out, ']'))
		if err != nil {
			return Graph{}, false // never trust a cache that does not parse
		}
		c.graph = &g
	}
	// The caller edits what it gets (it adds each stream's instance).
	g := *c.graph
	g.Outputs, g.Streams = slices.Clone(g.Outputs), slices.Clone(g.Streams)
	return g, true
}
