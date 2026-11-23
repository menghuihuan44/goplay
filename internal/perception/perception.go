// Package perception turns frames into observations. It is the only place the
// agent is allowed to learn about a game world: no process memory, no graphics
// API hooks, no injected code.
package perception

import (
	"context"
	"fmt"

	"goplay/internal/domain"
)

// Source produces frames. Implementations may use a screen-capture API, an
// emulator bridge, or a replay file.
type Source interface {
	Grab(ctx context.Context) (domain.Frame, error)
	Describe() string
	Close() error
}

// Analyzer converts pixels into a typed observation plus a raw world map for
// the Lua game profile.
type Analyzer interface {
	Analyze(frame domain.Frame) (domain.State, map[string]any, error)
	Name() string
}

func ValidateFrame(f domain.Frame) error {
	if f.Width <= 0 || f.Height <= 0 {
		return fmt.Errorf("invalid frame size %dx%d", f.Width, f.Height)
	}
	if len(f.RGBA) != f.Width*f.Height*4 {
		return fmt.Errorf("frame buffer is %d bytes, want %d", len(f.RGBA), f.Width*f.Height*4)
	}
	return nil
}

func pix(f domain.Frame, x, y int) (r, g, b uint8) {
	i := (y*f.Width + x) * 4
	return f.RGBA[i], f.RGBA[i+1], f.RGBA[i+2]
}
