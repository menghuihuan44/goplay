package bus

import (
	"sync"
	"time"
)

type Event struct {
	Type       string         `json:"type"`
	InstanceID string         `json:"instance_id,omitempty"`
	Message    string         `json:"message,omitempty"`
	At         time.Time      `json:"at"`
	Data       map[string]any `json:"data,omitempty"`
}

type Bus struct {
	mu     sync.Mutex
	nextID int
	subs   map[int]chan Event
	ring   []Event
	cap    int
}

func New(ringSize int) *Bus {
	if ringSize < 16 {
		ringSize = 16
	}
	return &Bus{subs: map[int]chan Event{}, cap: ringSize}
}

func (b *Bus) Publish(e Event) {
	if e.At.IsZero() {
		e.At = time.Now()
	}
	b.mu.Lock()
	b.ring = append(b.ring, e)
	if len(b.ring) > b.cap {
		b.ring = b.ring[len(b.ring)-b.cap:]
	}
	for _, ch := range b.subs {
		select {
		case ch <- e:
		default:
		}
	}
	b.mu.Unlock()
}

// Subscribe returns a buffered channel and an unsubscribe function.
func (b *Bus) Subscribe(buffer int) (<-chan Event, func()) {
	if buffer < 8 {
		buffer = 8
	}
	ch := make(chan Event, buffer)
	b.mu.Lock()
	id := b.nextID
	b.nextID++
	b.subs[id] = ch
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		if c, ok := b.subs[id]; ok {
			delete(b.subs, id)
			close(c)
		}
		b.mu.Unlock()
	}
}

func (b *Bus) Recent(n int) []Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	if n <= 0 || n > len(b.ring) {
		n = len(b.ring)
	}
	out := make([]Event, n)
	copy(out, b.ring[len(b.ring)-n:])
	return out
}
