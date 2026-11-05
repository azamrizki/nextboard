package server

import (
	"encoding/json"
	"strings"
	"sync"
	"time"
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

// cookieConnPrefix marks the sockets of a browser login in connRegistry, next to
// those of paired devices. loginCheckEvery is how late they notice a login ended
// from outside: `nextboard reset-password` is another process.
const (
	cookieConnPrefix = "cookie:"
	loginCheckEvery  = 2 * time.Second
)

// connRegistry tracks live WebSockets per paired device and per browser login,
// so the end of either can cut them immediately. A socket must not outlive its
// login: a stolen session would keep its shell after a password reset.
type connRegistry struct {
	mu       sync.Mutex
	next     int
	conns    map[string]map[int]func()
	onChange func(deviceID string)
	// cookieAlive answers for browser logins; watching says loginWatch runs,
	// which it does only while such sockets are open.
	cookieAlive func(cookie string) bool
	watching    bool
}

func newConnRegistry() *connRegistry {
	return &connRegistry{conns: map[string]map[int]func(){}}
}

// loginWatch closes the sockets of browser logins that ended.
func (c *connRegistry) loginWatch() {
	t := time.NewTicker(loginCheckEvery)
	defer t.Stop()
	for range t.C {
		var cookies []string
		c.mu.Lock()
		for key := range c.conns {
			if cookie, ok := strings.CutPrefix(key, cookieConnPrefix); ok {
				cookies = append(cookies, cookie)
			}
		}
		if len(cookies) == 0 {
			c.watching = false
			c.mu.Unlock()
			return
		}
		alive := c.cookieAlive
		c.mu.Unlock()
		for _, cookie := range cookies {
			if alive != nil && !alive(cookie) {
				c.kill(cookieConnPrefix + cookie)
			}
		}
	}
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
	if strings.HasPrefix(deviceID, cookieConnPrefix) && !c.watching {
		c.watching = true
		go c.loginWatch()
	}
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
