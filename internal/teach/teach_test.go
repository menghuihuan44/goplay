package teach

import (
	"testing"
	"time"

	"goplay/internal/action"
	"goplay/internal/domain"
)

func solidFrame(w, h int, r, g, b byte) domain.Frame {
	px := make([]byte, w*h*4)
	for i := 0; i < len(px); i += 4 {
		px[i], px[i+1], px[i+2], px[i+3] = r, g, b, 255
	}
	return domain.Frame{Width: w, Height: h, RGBA: px}
}

func TestSignatureAndSimilarity(t *testing.T) {
	a := solidFrame(64, 64, 10, 20, 30)
	b := solidFrame(64, 64, 10, 20, 30)
	c := solidFrame(64, 64, 200, 100, 5)
	sa, sb, sc := Signature(a), Signature(b), Signature(c)
	if len(sa) != sigW*sigH*3 {
		t.Fatalf("signature length = %d", len(sa))
	}
	if got := Similarity(sa, sb); got != 1 {
		t.Fatalf("identical similarity = %v", got)
	}
	if got := Similarity(sa, sc); got >= 0.9 {
		t.Fatalf("different similarity = %v", got)
	}
	if Signature(domain.Frame{}) != nil {
		t.Fatal("empty frame should have no signature")
	}
}

func TestActionFromEvent(t *testing.T) {
	a, ok := ActionFromEvent(action.InputEvent{Type: "key", Key: "Z"})
	if !ok || a.Kind != "demo_key_z" || a.Input == nil || a.Input.Key != "z" {
		t.Fatalf("key event -> %#v ok=%v", a, ok)
	}
	a, ok = ActionFromEvent(action.InputEvent{Type: "mouse_down", Button: "left", X: 0.25, Y: 0.75})
	if !ok || a.Kind != "demo_click" || a.Input.X != 0.25 || a.Input.Y != 0.75 {
		t.Fatalf("mouse event -> %#v ok=%v", a, ok)
	}
	if _, ok := ActionFromEvent(action.InputEvent{Type: "mouse_move"}); ok {
		t.Fatal("mouse move should not become an action")
	}
}

func TestRecorderLearnsAndReuses(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	rec := NewRecorder(store, "inst-1")
	now := time.Now()
	before := solidFrame(80, 60, 20, 30, 40)
	after := solidFrame(80, 60, 60, 30, 20)

	rec.Observe(before, nil, true, now)
	rec.Observe(after, []action.InputEvent{{Type: "key", Key: "z", At: now.Add(time.Millisecond)}}, true, now.Add(10*time.Millisecond))
	rec.Observe(after, nil, true, now.Add(20*time.Millisecond))
	rec.End(now.Add(30 * time.Millisecond))

	st := rec.Status()
	if st.Steps != 1 || st.Demos != 1 || st.Recording {
		t.Fatalf("status = %#v", st)
	}
	demos := store.Snapshot()
	if len(demos) != 1 || len(demos[0].Steps) != 1 {
		t.Fatalf("demos = %#v", demos)
	}
	step := demos[0].Steps[0]
	if step.Action.Kind != "demo_key_z" || step.BeforeImage == "" || step.AfterImage == "" {
		t.Fatalf("step = %#v", step)
	}
	if _, err := store.ImagePath(step.BeforeImage); err != nil {
		t.Fatal(err)
	}

	h, ok := rec.Hint(before)
	if !ok || h.Action.Kind != "demo_key_z" {
		t.Fatalf("hint = %#v ok=%v", h, ok)
	}
	rec.MarkUsed()
	if got := rec.Status(); got.Hints != 1 || got.Uses != 1 {
		t.Fatalf("hint/use counters = %#v", got)
	}
	if _, ok := rec.Hint(solidFrame(80, 60, 240, 240, 240)); ok {
		t.Fatal("unrelated screen must not match a demo")
	}

	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(reopened.Snapshot()); got != 1 {
		t.Fatalf("reloaded demos = %d", got)
	}
	rec2 := NewRecorder(reopened, "inst-1")
	if h, ok := rec2.Hint(before); !ok || h.Action.Kind != "demo_key_z" {
		t.Fatalf("reloaded hint = %#v ok=%v", h, ok)
	}
	if err := rec2.Clear(); err != nil {
		t.Fatal(err)
	}
	if len(reopened.Snapshot()) != 0 {
		t.Fatal("clear did not remove demos")
	}
	if st := rec2.Status(); st.Steps != 0 || st.Demos != 0 {
		t.Fatalf("status after clear = %#v", st)
	}
	if _, ok := rec2.Hint(before); ok {
		t.Fatal("cleared in-memory index still matched")
	}
}

func TestRecorderDisabledRecordsNothing(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	rec := NewRecorder(store, "inst-2")
	rec.SetEnabled(false)
	rec.Observe(solidFrame(32, 32, 1, 2, 3), []action.InputEvent{{Type: "key", Key: "z"}}, true, time.Now())
	rec.End(time.Now())
	if got := rec.Status(); got.Steps != 0 || got.Demos != 0 {
		t.Fatalf("disabled recorder status = %#v", got)
	}
}
