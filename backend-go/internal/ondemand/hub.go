package ondemand

import (
	"encoding/json"
	"sync"
	"time"
)

// Event is one server-sent event for a test. ID increases within a test so a reconnecting browser can resume.
type Event struct {
	ID   int64
	Name string // state | progress | result | error  (equals the "type" field in Data)
	Data json.RawMessage
}

const (
	replayLimit  = 400              // events kept per test; a whole Ookla run is about 150
	subBuffer    = 64               // events a slow reader may fall behind before it is dropped (it can reconnect and resume)
	keepFinished = 10 * time.Minute // how long a finished test's events stay available to late subscribers
)

type stream struct {
	events   []Event
	next     int64
	subs     map[chan Event]struct{}
	closed   bool
	closedAt time.Time
}

// Hub fans a test's live events out to any number of subscribers.
type Hub struct {
	mu      sync.Mutex
	streams map[string]*stream
	now     func() time.Time
}

func NewHub() *Hub { return &Hub{streams: map[string]*stream{}, now: time.Now} }

func (h *Hub) get(id string) *stream {
	st := h.streams[id]
	if st == nil {
		st = &stream{next: 1, subs: map[chan Event]struct{}{}}
		h.streams[id] = st
	}
	return st
}

// Publish records an event and delivers it to current subscribers. Events for a closed stream are ignored.
func (h *Hub) Publish(id, name string, data any) {
	b, err := json.Marshal(data)
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	st := h.get(id)
	if st.closed {
		return
	}
	ev := Event{ID: st.next, Name: name, Data: b}
	st.next++
	st.events = append(st.events, ev)
	if len(st.events) > replayLimit {
		st.events = st.events[len(st.events)-replayLimit:]
	}
	for ch := range st.subs {
		select {
		case ch <- ev:
		default: // too far behind: drop it; the browser reconnects with Last-Event-ID and catches up from the buffer
			delete(st.subs, ch)
			close(ch)
		}
	}
}

// Close ends a test's stream: subscribers' channels are closed after what they have already been sent.
func (h *Hub) Close(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	st := h.get(id)
	if st.closed {
		return
	}
	st.closed, st.closedAt = true, h.now()
	for ch := range st.subs {
		close(ch)
	}
	st.subs = map[chan Event]struct{}{}
}

// Subscription is what a listener gets: the events it missed, then live ones.
type Subscription struct {
	Replay []Event
	Live   <-chan Event
	Cancel func()
	// Exists is false when this process has no record of the test (it ended before a restart, or never ran here).
	Exists bool
	// Closed means the stream is over: there will be no live events.
	Closed bool
}

// Subscribe returns the events after lastID and a channel for new ones.
func (h *Hub) Subscribe(id string, lastID int64) Subscription {
	h.mu.Lock()
	defer h.mu.Unlock()
	st := h.streams[id]
	if st == nil {
		return Subscription{Cancel: func() {}}
	}
	sub := Subscription{Exists: true, Closed: st.closed, Cancel: func() {}}
	for _, ev := range st.events {
		if ev.ID > lastID {
			sub.Replay = append(sub.Replay, ev)
		}
	}
	if !st.closed {
		ch := make(chan Event, subBuffer)
		st.subs[ch] = struct{}{}
		sub.Live = ch
		sub.Cancel = func() {
			h.mu.Lock()
			defer h.mu.Unlock()
			if _, ok := st.subs[ch]; ok {
				delete(st.subs, ch)
				close(ch)
			}
		}
	}
	return sub
}

// Forget drops streams that finished long ago.
func (h *Hub) Forget() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, st := range h.streams {
		if st.closed && h.now().Sub(st.closedAt) > keepFinished {
			delete(h.streams, id)
		}
	}
}
