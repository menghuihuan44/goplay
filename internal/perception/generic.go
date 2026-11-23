package perception

import (
	"fmt"

	"goplay/internal/domain"
)

// GenericAnalyzer is the fallback for unknown games. It extracts cheap global
// statistics and hands the frame to the vision model; it never pretends to
// understand a game it has not learned.
type GenericAnalyzer struct{}

func (GenericAnalyzer) Name() string { return "generic-frame-v1" }

func (GenericAnalyzer) Analyze(f domain.Frame) (domain.State, map[string]any, error) {
	if err := ValidateFrame(f); err != nil {
		return domain.State{}, nil, err
	}
	step := 4
	var sum, warm, dark int
	for y := 0; y < f.Height; y += step {
		for x := 0; x < f.Width; x += step {
			r, g, b := pix(f, x, y)
			sum += int(r) + int(g) + int(b)
			if int(r) > int(b)+30 {
				warm++
			}
			if int(r)+int(g)+int(b) < 90 {
				dark++
			}
		}
	}
	n := (f.Width / step) * (f.Height / step)
	if n == 0 {
		n = 1
	}
	brightness := float64(sum) / float64(n*3)
	st := domain.State{
		Summary: fmt.Sprintf("frame %dx%d brightness %.0f", f.Width, f.Height, brightness),
		Values: map[string]float64{
			"width": float64(f.Width), "height": float64(f.Height),
			"brightness": brightness, "warm_ratio": float64(warm) / float64(n),
			"dark_ratio": float64(dark) / float64(n),
		},
		Tags:       []string{"frame", "unknown_game"},
		Confidence: 0.4,
	}
	world := map[string]any{
		"width": f.Width, "height": f.Height, "brightness": brightness,
		"warm_ratio": float64(warm) / float64(n), "dark_ratio": float64(dark) / float64(n),
	}
	return st, world, nil
}
