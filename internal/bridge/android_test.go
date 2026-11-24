package bridge

import (
	"context"
	"testing"
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
