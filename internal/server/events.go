package server

import (
	"encoding/json"
	"sync"
)

// Broker fans JSON events out to /api/events subscribers.
type Broker struct {
	mu   sync.Mutex
	subs map[*eventSub]struct{}
}

type eventSub struct {
	C    chan []byte
	once sync.Once
	// topics limits delivery to these event types (nil = all), for
	// /api/events?topics=attention,session (docs/API-agents.md §12).
	topics map[string]bool
}

func (e *eventSub) close() { e.once.Do(func() { close(e.C) }) }

func NewBroker() *Broker { return &Broker{subs: map[*eventSub]struct{}{}} }

// Subscribe registers a subscriber; its channel closes if it falls behind.
func (b *Broker) Subscribe(topics ...string) *eventSub {
	sub := &eventSub{C: make(chan []byte, 256)}
	if len(topics) > 0 {
		sub.topics = map[string]bool{}
		for _, t := range topics {
			sub.topics[t] = true
		}
	}
	b.mu.Lock()
	b.subs[sub] = struct{}{}
	b.mu.Unlock()
	return sub
}

func (b *Broker) Unsubscribe(sub *eventSub) {
	b.mu.Lock()
	delete(b.subs, sub)
	b.mu.Unlock()
	sub.close()
}

// Publish marshals v once and delivers it without blocking. Events are
// maps with a "type" key; subscribers with topics only get those types.
func (b *Broker) Publish(v any) {
	msg, err := json.Marshal(v)
	if err != nil {
		return
	}
	typ := ""
	if m, ok := v.(map[string]any); ok {
		typ, _ = m["type"].(string)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for sub := range b.subs {
		if sub.topics != nil && !sub.topics[typ] {
			continue
		}
		select {
		case sub.C <- msg:
		default:
			delete(b.subs, sub)
			sub.close()
		}
	}
}

// connRegistry tracks live WebSockets per paired device so revoking a
// device can cut them immediately.
type connRegistry struct {
	mu       sync.Mutex
	next     int
	conns    map[string]map[int]func()
	onChange func(deviceID string)
}

func newConnRegistry() *connRegistry {
	return &connRegistry{conns: map[string]map[int]func(){}}
}

// add registers a kill func for deviceID; the returned func unregisters.
func (c *connRegistry) add(deviceID string, kill func()) func() {
	if deviceID == "" {
		return func() {}
	}
	c.mu.Lock()
	c.next++
	id := c.next
	if c.conns[deviceID] == nil {
		c.conns[deviceID] = map[int]func(){}
	}
	c.conns[deviceID][id] = kill
	first := len(c.conns[deviceID]) == 1
	c.mu.Unlock()
	if first && c.onChange != nil {
		c.onChange(deviceID)
	}
	return func() {
		c.mu.Lock()
		delete(c.conns[deviceID], id)
		last := len(c.conns[deviceID]) == 0
		if last {
			delete(c.conns, deviceID)
		}
		c.mu.Unlock()
		if last && c.onChange != nil {
			c.onChange(deviceID)
		}
	}
}

func (c *connRegistry) online(deviceID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.conns[deviceID]) > 0
}

// kill closes every connection of deviceID.
func (c *connRegistry) kill(deviceID string) {
	c.mu.Lock()
	fns := c.conns[deviceID]
	delete(c.conns, deviceID)
	c.mu.Unlock()
	for _, f := range fns {
		f()
	}
}
