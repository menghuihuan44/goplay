package scheduler

import (
	"sync"
	"time"
)

type Message struct {
	ID         string    `json:"id"`
	InstanceID string    `json:"instance_id,omitempty"`
	Text       string    `json:"text"`
	Source     string    `json:"source"` // user_text, user_voice, system
	At         time.Time `json:"at"`
}

// Buffer is the independent instruction/preference buffer. It is filled at
// any time and drained only by the scheduler at an event boundary.
type Buffer struct {
	mu   sync.Mutex
	msgs []Message
	max  int
}

func NewBuffer(max int) *Buffer {
	if max <= 0 {
		max = 128
	}
	return &Buffer{max: max}
}

func (b *Buffer) Submit(m Message) {
	if m.At.IsZero() {
		m.At = time.Now()
	}
	b.mu.Lock()
	b.msgs = append(b.msgs, m)
	if len(b.msgs) > b.max {
		// Keep newest; old undelivered instructions are stale by definition.
		b.msgs = b.msgs[len(b.msgs)-b.max:]
	}
	b.mu.Unlock()
}

func (b *Buffer) Drain() []Message {
	b.mu.Lock()
	out := b.msgs
	b.msgs = nil
	b.mu.Unlock()
	return out
}

func (b *Buffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.msgs)
}
