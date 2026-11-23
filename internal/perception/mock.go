package perception

import (
	"context"
	"fmt"
	"math/rand"
	"sync"

	"goplay/internal/domain"
	"goplay/internal/mockgame"
)

// MockSource renders the training dungeon. Noise optionally corrupts pixels to
// keep the analyzer honest about imperfect screen captures.
type MockSource struct {
	game  *mockgame.Game
	noise float64
	mu    sync.Mutex
	rng   *rand.Rand
}

func NewMockSource(g *mockgame.Game, noise float64) *MockSource {
	return &MockSource{game: g, noise: noise, rng: rand.New(rand.NewSource(g.Seed() ^ 0x5eed))}
}

func (s *MockSource) Grab(ctx context.Context) (domain.Frame, error) {
	select {
	case <-ctx.Done():
		return domain.Frame{}, ctx.Err()
	default:
	}
	f := mockgame.Render(s.game)
	if s.noise <= 0 {
		return f, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n := int(float64(f.Width*f.Height) * s.noise)
	for i := 0; i < n; i++ {
		x := s.rng.Intn(f.Width)
		y := s.rng.Intn(f.Height)
		j := (y*f.Width + x) * 4
		if s.rng.Intn(2) == 0 {
			f.RGBA[j], f.RGBA[j+1], f.RGBA[j+2] = 0, 0, 0
		} else {
			f.RGBA[j], f.RGBA[j+1], f.RGBA[j+2] = 255, 255, 255
		}
	}
	return f, nil
}

func (s *MockSource) Describe() string { return "mock-dungeon-renderer" }
func (s *MockSource) Close() error     { return nil }

// MockAnalyzer decodes the rendered HUD. It only ever inspects pixels; the
// simulator's GroundTruth is used by the evaluator, not by this analyzer.
type MockAnalyzer struct{}

func (MockAnalyzer) Name() string { return "mock-hud-v1" }

var digitTemplates = [10][5][3]byte{
	{{1, 1, 1}, {1, 0, 1}, {1, 0, 1}, {1, 0, 1}, {1, 1, 1}},
	{{0, 1, 0}, {1, 1, 0}, {0, 1, 0}, {0, 1, 0}, {1, 1, 1}},
	{{1, 1, 1}, {0, 0, 1}, {1, 1, 1}, {1, 0, 0}, {1, 1, 1}},
	{{1, 1, 1}, {0, 0, 1}, {1, 1, 1}, {0, 0, 1}, {1, 1, 1}},
	{{1, 0, 1}, {1, 0, 1}, {1, 1, 1}, {0, 0, 1}, {0, 0, 1}},
	{{1, 1, 1}, {1, 0, 0}, {1, 1, 1}, {0, 0, 1}, {1, 1, 1}},
	{{1, 1, 1}, {1, 0, 0}, {1, 1, 1}, {1, 0, 1}, {1, 1, 1}},
	{{1, 1, 1}, {0, 0, 1}, {0, 0, 1}, {0, 0, 1}, {0, 0, 1}},
	{{1, 1, 1}, {1, 0, 1}, {1, 1, 1}, {1, 0, 1}, {1, 1, 1}},
	{{1, 1, 1}, {1, 0, 1}, {1, 1, 1}, {0, 0, 1}, {1, 1, 1}},
}

func (a MockAnalyzer) Analyze(f domain.Frame) (domain.State, map[string]any, error) {
	if err := ValidateFrame(f); err != nil {
		return domain.State{}, nil, err
	}
	if f.Width != mockgame.W || f.Height != mockgame.H {
		return domain.State{}, nil, fmt.Errorf("unexpected mock frame %dx%d", f.Width, f.Height)
	}

	hp := barFraction(f, mockgame.HPBarY, mockgame.BarX, mockgame.BarW, func(r, g, b uint8) bool {
		return r > 150 && g < 120 && b < 120
	})
	ehpFrac := barFraction(f, mockgame.EnemyBarY, mockgame.BarX, mockgame.BarW, func(r, g, b uint8) bool {
		return r > 170 && g > 100 && g < 200 && b < 110
	})

	room := decodeRoom(f)
	boss := room >= mockgame.MaxRooms
	emax := 18 + room*5
	if boss {
		emax = 90
	}
	potions := countPotions(f)
	dead, won := statusMarker(f)

	hpVal := hp * 100
	ehpVal := ehpFrac * float64(emax)
	vals := map[string]float64{
		"hp": hpVal, "max_hp": 100,
		"enemy_hp": ehpVal, "enemy_max_hp": float64(emax),
		"potions": float64(potions), "room": float64(room),
		"dead": boolVal(dead), "won": boolVal(won), "boss": boolVal(boss),
	}
	phase := "combat"
	switch {
	case dead:
		phase = "dead"
	case won:
		phase = "won"
	case ehpVal <= 0.5:
		phase = "clear"
	}
	tags := []string{"room" + fmt.Sprint(room), phase}
	if potions > 0 {
		tags = append(tags, "has_potion")
	}
	st := domain.State{
		Summary: fmt.Sprintf("room %d hp %.0f enemy %.0f potions %d", room, hpVal, ehpVal, potions),
		Values:  vals,
		Text: map[string]string{
			"phase": phase,
			"dead":  fmt.Sprint(dead),
			"won":   fmt.Sprint(won),
		},
		Tags:       tags,
		Confidence: 1,
		Raw:        map[string]any{"hp": hpVal, "enemy_hp": ehpVal, "room": room},
	}
	world := map[string]any{
		"hp": hpVal, "max_hp": 100, "enemy_hp": ehpVal, "enemy_max_hp": emax,
		"potions": potions, "room": room, "max_rooms": mockgame.MaxRooms, "dead": dead, "won": won, "boss": boss,
	}
	return st, world, nil
}

func boolVal(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func barFraction(f domain.Frame, y, x, w int, match func(r, g, b uint8) bool) float64 {
	filled := 0
	for i := 0; i < w; i++ {
		hits := 0
		// Sample a vertical stripe and use a majority vote so isolated noisy
		// pixels cannot move a health bar reading.
		for dy := 1; dy <= 6; dy++ {
			r, g, b := pix(f, x+i, y+dy)
			if match(r, g, b) {
				hits++
			}
		}
		if hits >= 4 {
			filled++
		}
	}
	return float64(filled) / float64(w)
}

func decodeRoom(f domain.Frame) int {
	tens := decodeDigit(f, mockgame.RoomDigitsX, mockgame.RoomDigitsY)
	ones := decodeDigit(f, mockgame.RoomDigitsX+mockgame.DigitW*mockgame.DigitScale+2, mockgame.RoomDigitsY)
	if tens < 0 || ones < 0 {
		return 1
	}
	room := tens*10 + ones
	if room < 1 || room > mockgame.MaxRooms {
		return 1
	}
	return room
}

func decodeDigit(f domain.Frame, x, y int) int {
	var bits [5][3]byte
	for row := 0; row < 5; row++ {
		for col := 0; col < 3; col++ {
			sx := x + col*mockgame.DigitScale + 1
			sy := y + row*mockgame.DigitScale + 1
			r, g, b := pix(f, sx, sy)
			if r > 140 && g > 140 && b > 140 {
				bits[row][col] = 1
			}
		}
	}
	best, bestDist := -1, 99
	for d := 0; d < 10; d++ {
		dist := 0
		for row := 0; row < 5; row++ {
			for col := 0; col < 3; col++ {
				if bits[row][col] != digitTemplates[d][row][col] {
					dist++
				}
			}
		}
		if dist < bestDist {
			best, bestDist = d, dist
		}
	}
	if bestDist > 2 {
		return -1
	}
	return best
}

func countPotions(f domain.Frame) int {
	count := 0
	inRun := false
	for x := 8; x < 80; x++ {
		r, g, b := pix(f, x, 171)
		isBlue := b > 150 && r < 140 && g < 180
		if isBlue && !inRun {
			count++
			inRun = true
		} else if !isBlue {
			inRun = false
		}
	}
	return count
}

func statusMarker(f domain.Frame) (dead, won bool) {
	r, g, b := pix(f, 305, 13)
	if r > 150 && g < 100 && b < 100 {
		dead = true
	}
	if g > 150 && r < 120 {
		won = true
	}
	return
}
