package engine

import (
	"bytes"
	"testing"
	"time"
)

func TestRunnerPublishesMockJPEGFrame(t *testing.T) {
	h := newHarness(t, "frame-1", 7)
	h.runner.Start()
	defer h.runner.Stop()

	var jpg []byte
	waitFor(t, 15*time.Second, "encoded frame", func() bool {
		var at time.Time
		jpg, at = h.runner.LastFrameJPEG()
		return len(jpg) > 0 && !at.IsZero()
	})
	if !bytes.HasPrefix(jpg, []byte{0xff, 0xd8, 0xff}) {
		t.Fatalf("frame is not a JPEG: %x", jpg[:3])
	}

	info := h.runner.FrameInfo()
	if available, _ := info["available"].(bool); !available {
		t.Fatalf("FrameInfo.available = %v", info["available"])
	}
	if width, _ := info["width"].(int); width <= 0 || width > frameMaxEdge {
		t.Fatalf("FrameInfo width = %v", info["width"])
	}
	if height, _ := info["height"].(int); height <= 0 || height > frameMaxEdge {
		t.Fatalf("FrameInfo height = %v", info["height"])
	}
	if size, _ := info["bytes"].(int); size <= 0 {
		t.Fatalf("FrameInfo bytes = %v", info["bytes"])
	}

	snap := h.runner.Snapshot()
	if snap.FrameAt.IsZero() || snap.FrameWidth <= 0 || snap.FrameHeight <= 0 {
		t.Fatalf("snapshot frame metadata = at:%v %dx%d", snap.FrameAt, snap.FrameWidth, snap.FrameHeight)
	}
	if len(snap.FrameJPEG) == 0 || !bytes.HasPrefix(snap.FrameJPEG, []byte{0xff, 0xd8, 0xff}) {
		t.Fatalf("snapshot frame is not a JPEG (%d bytes)", len(snap.FrameJPEG))
	}
}
