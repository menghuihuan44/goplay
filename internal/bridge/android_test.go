package bridge

import (
	"context"
	"sync"
	"testing"
	"time"

	"goplay/internal/domain"
)

func TestClearHumanEndsControlWindow(t *testing.T) {
	s := NewSession("test")
	s.SetHuman(true, "client-touch")
	if !s.State(context.Background()).Active {
		t.Fatal("human state should be active after SetHuman(true)")
	}

	s.ClearHuman("user-released")
	if s.State(context.Background()).Active {
		t.Fatal("human state still active after ClearHuman")
	}

	info := s.Info()
	if info["connected"].(bool) {
		t.Fatal("session with no transport reports connected")
	}
	if info["human"].(bool) {
		t.Fatal("Info reports human after ClearHuman")
	}
	if age, ok := info["last_frame_age_ms"].(int64); !ok || age != -1 {
		t.Fatalf("last_frame_age_ms = %v, want -1", info["last_frame_age_ms"])
	}
}

type recordingTransport struct {
	mu   sync.Mutex
	sent []map[string]any
}

func (r *recordingTransport) SendJSON(v any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if m, ok := v.(map[string]any); ok {
		r.sent = append(r.sent, m)
	}
	return nil
}

func (r *recordingTransport) actions() []map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]map[string]any(nil), r.sent...)
}

// A client that attaches just after Start must still receive the first action.
func TestDoWaitsForLateTransport(t *testing.T) {
	s := NewSession("late")
	tr := &recordingTransport{}
	attachDone := make(chan struct{})
	go func() {
		time.Sleep(150 * time.Millisecond)
		s.Attach(tr)
		close(attachDone)
	}()

	errCh := make(chan error, 1)
	go func() {
		errCh <- s.Do(context.Background(), domain.Action{
			Kind:  "wait",
			Input: &domain.InputSpec{Type: "wait", DurationMS: 50},
		})
	}()

	var id string
	deadline := time.After(2 * time.Second)
	for id == "" {
		select {
		case <-deadline:
			t.Fatal("action was not sent after the transport attached")
		case <-time.After(20 * time.Millisecond):
		}
		for _, m := range tr.actions() {
			if got, ok := m["id"].(string); ok && got != "" {
				id = got
			}
		}
	}
	s.Ack(id, true, "", "", "wait", 10)
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Do returned %v after a late transport attached", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Do did not finish after ack")
	}
	<-attachDone
}

// A frame cached before the socket dropped must not satisfy a later Grab: the
// action would be sent on a transport that no longer exists.
func TestGrabIgnoresFrameWithoutTransport(t *testing.T) {
	s := NewSession("stale-frame")
	s.mu.Lock()
	s.latest = &domain.Frame{Width: 1, Height: 1, RGBA: []byte{0, 0, 0, 255}, At: time.Now()}
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := s.Grab(ctx); err == nil {
		t.Fatal("Grab reused a cached frame while no client was attached")
	}
}

// With a live transport the freshly cached frame is still reused so a slow
// client does not stall the decision loop.
func TestGrabReusesFrameWhenTransportAttached(t *testing.T) {
	s := NewSession("live-frame")
	s.Attach(&recordingTransport{})
	s.mu.Lock()
	s.latest = &domain.Frame{Width: 1, Height: 1, RGBA: []byte{0, 0, 0, 255}, At: time.Now()}
	s.mu.Unlock()

	f, err := s.Grab(context.Background())
	if err != nil {
		t.Fatalf("Grab with attached transport: %v", err)
	}
	if f.Width != 1 || f.Height != 1 {
		t.Fatalf("unexpected frame %dx%d", f.Width, f.Height)
	}
}

// Without a client the action must fail with the historical error text, but
// only after the bounded wait.
func TestDoStillFailsWithoutTransport(t *testing.T) {
	s := NewSession("none")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := s.Do(ctx, domain.Action{
		Kind:  "wait",
		Input: &domain.InputSpec{Type: "wait"},
	})
	if err == nil {
		t.Fatal("Do succeeded without a transport")
	}
}
