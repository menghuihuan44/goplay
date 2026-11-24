package perception

import (
	"testing"

	"goplay/internal/domain"
)

func shmupFrame() domain.Frame {
	w, h := 640, 480
	rgba := make([]byte, w*h*4)
	for i := 0; i < len(rgba); i += 4 {
		rgba[i], rgba[i+1], rgba[i+2], rgba[i+3] = 30, 30, 45, 255
	}
	rect := func(x, y, rw, rh int, r, g, b byte) {
		for yy := y; yy < y+rh; yy++ {
			for xx := x; xx < x+rw; xx++ {
				i := yy*w*4 + xx*4
				rgba[i], rgba[i+1], rgba[i+2], rgba[i+3] = r, g, b, 255
			}
		}
	}
	// Player: bright red sprite low in the playfield.
	rect(96, 432, 14, 20, 230, 40, 40)
	// Dark-blue HUD strip on the right, as in Touhou gameplay.
	rect(440, 0, 200, 480, 41, 41, 57)
	// Two white bullets well above/right of the player.
	rect(135, 430, 6, 4, 250, 250, 250)
	rect(300, 120, 6, 4, 250, 250, 250)
	return domain.Frame{Width: w, Height: h, RGBA: rgba}
}

func TestShmupAnalyzerFindsPlayerAndBullets(t *testing.T) {
	a := NewShmupAnalyzer()
	st, world, err := a.Analyze(shmupFrame())
	if err != nil {
		t.Fatal(err)
	}
	if st.Values["player_x"] <= 0.1 || st.Values["player_x"] >= 0.4 {
		t.Fatalf("player_x = %v, want near 0.25", st.Values["player_x"])
	}
	if st.Values["bullets"] != 2 {
		t.Fatalf("bullets = %v, want 2", st.Values["bullets"])
	}
	if st.Values["safe_left"] <= st.Values["safe_stay"] {
		t.Fatalf("left should be safer than stay: %v vs %v", st.Values["safe_left"], st.Values["safe_stay"])
	}
	if st.Text["phase"] != "playing" {
		t.Fatalf("phase = %q", st.Text["phase"])
	}
	if world["bullets"] != 2 {
		t.Fatalf("world bullets = %v", world["bullets"])
	}
}

func TestShmupAnalyzerMenuFallback(t *testing.T) {
	w, h := 640, 480
	rgba := make([]byte, w*h*4)
	for i := 0; i < len(rgba); i += 4 {
		rgba[i], rgba[i+1], rgba[i+2], rgba[i+3] = 20, 20, 30, 255
	}
	st, _, err := NewShmupAnalyzer().Analyze(domain.Frame{Width: w, Height: h, RGBA: rgba})
	if err != nil {
		t.Fatal(err)
	}
	if st.Text["phase"] != "menu" {
		t.Fatalf("phase = %q, want menu", st.Text["phase"])
	}
}
func TestShmupRejectsOffFieldMovesAtEdges(t *testing.T) {
	w, h := 640, 480
	rgba := make([]byte, w*h*4)
	for i := 0; i < len(rgba); i += 4 {
		rgba[i], rgba[i+1], rgba[i+2], rgba[i+3] = 30, 30, 45, 255
	}
	fill := func(x, y, rw, rh int, r, g, b byte) {
		for yy := y; yy < y+rh; yy++ {
			for xx := x; xx < x+rw; xx++ {
				i := yy*w*4 + xx*4
				rgba[i], rgba[i+1], rgba[i+2], rgba[i+3] = r, g, b, 255
			}
		}
	}
	// Player hugging the bottom-right corner of the playfield.
	fill(415, 448, 14, 20, 230, 40, 40)
	fill(440, 0, 200, 480, 41, 41, 57)
	st, _, err := NewShmupAnalyzer().Analyze(domain.Frame{Width: w, Height: h, RGBA: rgba})
	if err != nil {
		t.Fatal(err)
	}
	if st.Values["player_x"] < 0.85 {
		t.Fatalf("player_x = %v, want near right edge", st.Values["player_x"])
	}
	if st.Values["safe_right"] != 0 {
		t.Fatalf("safe_right = %v, want 0 for an off-field move", st.Values["safe_right"])
	}
	if st.Values["safe_down"] != 0 {
		t.Fatalf("safe_down = %v, want 0 for an off-field move", st.Values["safe_down"])
	}
}
