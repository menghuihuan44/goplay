package perception

import (
	"testing"

	"goplay/internal/domain"
)

func solidFrame(w, h int, r, g, b byte) domain.Frame {
	buf := make([]byte, w*h*4)
	for i := 0; i < w*h; i++ {
		buf[i*4], buf[i*4+1], buf[i*4+2], buf[i*4+3] = r, g, b, 255
	}
	return domain.Frame{Width: w, Height: h, RGBA: buf}
}

func TestGenericAnalyzerSeesSceneChange(t *testing.T) {
	a := NewGenericAnalyzer()
	st1, _, err := a.Analyze(solidFrame(96, 64, 8, 8, 8))
	if err != nil {
		t.Fatal(err)
	}
	if st1.Values["edge_ratio"] != 0 {
		t.Fatalf("uniform frame must have no edges, got %v", st1.Values["edge_ratio"])
	}
	if hasTag(st1.Tags, "static") == false {
		t.Fatalf("first uniform frame should be static, got %v", st1.Tags)
	}

	f := solidFrame(96, 64, 240, 240, 240)
	for y := 0; y < 64; y++ {
		for x := 0; x < 48; x++ {
			i := (y*96 + x) * 4
			f.RGBA[i], f.RGBA[i+1], f.RGBA[i+2] = 0, 0, 0
		}
	}
	st2, _, err := a.Analyze(f)
	if err != nil {
		t.Fatal(err)
	}
	if st2.Values["scene_changed"] != 1 {
		t.Fatalf("expected scene change, values=%v", st2.Values)
	}
	if st2.Values["edge_ratio"] <= 0.01 {
		t.Fatalf("expected edges on split frame, got %v", st2.Values["edge_ratio"])
	}
	if !hasTag(st2.Tags, "new_scene") {
		t.Fatalf("expected new_scene tag, got %v", st2.Tags)
	}
	if st2.Values["width"] != 96 || st2.Values["height"] != 64 {
		t.Fatalf("frame size missing: %v", st2.Values)
	}
}

func hasTag(tags []string, want string) bool {
	for _, t := range tags {
		if t == want {
			return true
		}
	}
	return false
}

func TestGenericAnalyzerUsesOCRText(t *testing.T) {
	a := NewGenericAnalyzer()
	f := solidFrame(64, 48, 10, 10, 10)
	f.Meta = map[string]string{
		"ocr": `[{"text":"继续游戏","x":0.72,"y":0.35,"w":0.2,"h":0.05},{"text":"开始游戏","x":0.5,"y":0.6,"w":0.2,"h":0.05}]`,
	}
	st, world, err := a.Analyze(f)
	if err != nil {
		t.Fatal(err)
	}
	if st.Values["text_count"] != 2 {
		t.Fatalf("expected 2 OCR labels, got %v", st.Values["text_count"])
	}
	if st.Text["ui_text"] != "继续游戏 · 开始游戏" {
		t.Fatalf("ui_text not exposed: %q", st.Text["ui_text"])
	}
	if !hasTag(st.Tags, "ui_text") {
		t.Fatalf("expected ui_text tag, got %v", st.Tags)
	}
	regions, ok := world["ui_regions"].([]any)
	if !ok || len(regions) != 2 {
		t.Fatalf("ui_regions missing: %#v", world["ui_regions"])
	}
}
