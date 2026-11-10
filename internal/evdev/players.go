package evdev

import "sync"

// Players gives each connected controller a slot, 1, 2, 3...: the first
// controller is player 1, the next takes the lowest free slot. A controller
// keeps its slot while hostd runs, also when it is switched off and on again
// (its ID is the same); the slot of one that is away is given to a newcomer
// only when no free one is lower.
type Players struct {
	mu        sync.Mutex
	slot      map[string]int  // ID -> slot, for every controller seen
	connected map[string]bool // IDs now plugged in
}

// Connect notes a controller and returns its slot.
func (p *Players) Connect(id string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.slot == nil {
		p.slot, p.connected = map[string]int{}, map[string]bool{}
	}
	p.connected[id] = true
	if n, ok := p.slot[id]; ok {
		return n
	}
	for n := 1; ; n++ {
		held := ""
		for other, s := range p.slot {
			if s == n {
				held = other
			}
		}
		if held == "" || !p.connected[held] {
			if held != "" {
				delete(p.slot, held) // its owner is away: the slot is free
			}
			p.slot[id] = n
			return n
		}
	}
}

// Disconnect notes that a controller was unplugged; it keeps its slot.
func (p *Players) Disconnect(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.connected, id)
}

// Slot returns a controller's slot, 0 if it has none.
func (p *Players) Slot(id string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.slot[id]
}

// Assign puts a controller in a slot, moving whoever has it to the
// controller's old one (or taking theirs away if it had none).
func (p *Players) Assign(id string, n int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.slot == nil {
		p.slot, p.connected = map[string]int{}, map[string]bool{}
	}
	old, had := p.slot[id]
	for other, s := range p.slot {
		if s == n && other != id {
			if had {
				p.slot[other] = old
			} else {
				delete(p.slot, other)
			}
		}
	}
	p.slot[id] = n
}
