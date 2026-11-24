package perception

import (
	"fmt"
	"math"

	"goplay/internal/domain"
)

// ShmupAnalyzer is a deterministic, game-agnostic vertical-shooter analyzer.
// It reads only the screenshot: it finds the player by its bright red sprite,
// finds bullets as small near-white components, and scores five candidate
// positions (stay/left/right/up/down) by nearby bullet density. The Lua profile
// maps those scores onto the game's movement keys, so an offline rule fallback
// can still dodge instead of standing in a bullet stream.
//
// It deliberately does not claim to know lives, score or boss HP: those are
// left to the optional vision model. This keeps the "unknown game" path honest.
type ShmupAnalyzer struct {
	lastX, lastY float64
	haveLast     bool
}

func NewShmupAnalyzer() *ShmupAnalyzer { return &ShmupAnalyzer{} }

func (a *ShmupAnalyzer) Name() string { return "shmup-pixels-v1" }

type blob struct {
	area       int
	minX, minY int
	maxX, maxY int
	cx, cy     float64
}

func (a *ShmupAnalyzer) Analyze(f domain.Frame) (domain.State, map[string]any, error) {
	if err := ValidateFrame(f); err != nil {
		return domain.State{}, nil, err
	}
	playW := playfieldWidth(f.Width)
	// The player is a red/white sprite in the lower part of the playfield.
	// Red spell effects can be large, so cap the component size and prefer
	// components near the bottom.
	player, ok := findPlayer(f, playW, a.lastX*float64(playW), a.lastY*float64(f.Height), a.haveLast)
	if ok {
		a.lastX = player.cx / float64(playW)
		a.lastY = player.cy / float64(f.Height)
		a.haveLast = true
	}
	px, py := a.lastX, a.lastY
	if !a.haveLast {
		px, py = 0.5, 0.9
	}
	bullets := findBullets(f, playW, px, py)
	cx := px * float64(playW)
	cy := py * float64(f.Height)
	// Touhou's gameplay screen has a dark blue HUD on the right; its title and
	// menu screens are mostly red artwork. That colour difference is a more
	// reliable menu signal than "no bullets", because menu text also contains
	// white components that look like bullets.
	hudBlue := hudBlueFraction(f, playW)

	safety := map[string]float64{}
	for _, c := range []struct {
		name string
		x, y float64
	}{
		{"stay", cx, cy},
		{"left", cx - 28, cy},
		{"right", cx + 28, cy},
		{"up", cx, cy - 28},
		{"down", cx, cy + 28},
	} {
		// A move that would leave the playfield does not exist. Clamping it to
		// the edge made "move right" look permanently safe while the player was
		// already touching the right wall, so the AI pressed into the wall for
		// seconds instead of dodging. Treat off-field moves as unusable.
		if c.x < 4 || c.x > float64(playW-4) || c.y < 4 || c.y > float64(f.Height-4) {
			safety[c.name] = 0
			continue
		}
		s := safetyAt(c.x, c.y, bullets)
		// Mild centre bias: sitting exactly in a corner is rarely the best
		// dodge and gives the player nowhere to retreat.
		if c.x < float64(playW)*0.12 || c.x > float64(playW)*0.88 {
			s *= 0.85
		}
		if c.y > float64(f.Height)*0.94 {
			s *= 0.9
		}
		safety[c.name] = s
	}
	danger := 0.0
	if len(bullets) > 0 {
		danger = 1.0 - safety["stay"]
	}
	phase := "playing"
	if hudBlue < 0.15 {
		phase = "menu"
		bullets = nil
	} else if !ok && len(bullets) == 0 {
		phase = "menu"
	}
	tags := []string{phase, "shmup"}
	if danger > 0.55 {
		tags = append(tags, "danger")
	}
	if len(bullets) == 0 {
		tags = append(tags, "no_bullets")
	}
	st := domain.State{
		Summary: fmt.Sprintf("shmup player=(%.2f,%.2f) bullets=%d danger=%.2f safe L/R/U/D=%.2f/%.2f/%.2f/%.2f",
			px, py, len(bullets), danger, safety["left"], safety["right"], safety["up"], safety["down"]),
		Values: map[string]float64{
			"player_x": px, "player_y": py,
			"bullets": float64(len(bullets)), "danger": danger,
			"safe_stay": safety["stay"], "safe_left": safety["left"],
			"safe_right": safety["right"], "safe_up": safety["up"], "safe_down": safety["down"],
			"frame_w": float64(f.Width), "frame_h": float64(f.Height),
			"hud_blue": hudBlue,
		},
		Text:       map[string]string{"phase": phase},
		Tags:       tags,
		Confidence: 0.72,
		At:         f.At,
	}
	world := map[string]any{
		"bullets": len(bullets), "danger": danger, "player_x": px, "player_y": py,
		"safe_stay": safety["stay"], "safe_left": safety["left"],
		"safe_right": safety["right"], "safe_up": safety["up"], "safe_down": safety["down"],
		"summary": st.Summary, "tags": tags, "text": st.Text,
		"width": f.Width, "height": f.Height, "hud_blue": hudBlue,
	}
	return st, world, nil
}

func playfieldWidth(w int) int {
	if w < 200 {
		return w
	}
	// Touhou 6-18 use a 640x480 canvas with a right-side HUD. On other
	// resolutions the HUD is roughly the right 32%; keep a full-width fallback
	// for narrow phone frames.
	if w >= 480 {
		return int(float64(w) * 0.68)
	}
	return w
}

// hudBlueFraction measures the dark-blue HUD strip on the right of a Touhou
// frame. Gameplay has a blue panel (typically 0.7-0.9); menus are red artwork
// (typically <0.05).
func hudBlueFraction(f domain.Frame, playW int) float64 {
	if playW >= f.Width-8 {
		return 0
	}
	n, blue := 0, 0
	for y := 0; y < f.Height; y += 2 {
		for x := playW; x < f.Width; x += 2 {
			r, _, b := pix(f, x, y)
			n++
			if int(b) > int(r)+10 && b > 40 {
				blue++
			}
		}
	}
	if n == 0 {
		return 0
	}
	return float64(blue) / float64(n)
}

func findPlayer(f domain.Frame, playW int, lastX, lastY float64, haveLast bool) (blob, bool) {
	startY := int(float64(f.Height) * 0.55)
	mask := make([]bool, playW*f.Height)
	for y := startY; y < f.Height; y++ {
		for x := 0; x < playW; x++ {
			r, g, b := pix(f, x, y)
			if r >= 170 && g <= 105 && b <= 105 {
				mask[y*playW+x] = true
			}
		}
	}
	comps := components(mask, playW, f.Height, 4)
	var best blob
	found := false
	for _, c := range comps {
		w, h := c.maxX-c.minX+1, c.maxY-c.minY+1
		if c.area < 18 || c.area > 900 || w > 48 || h > 56 {
			continue
		}
		if c.cy < float64(f.Height)*0.6 {
			continue
		}
		// Prefer compact lower components; the player is usually the largest
		// red sprite near the bottom, while items are small squares. Continuity
		// matters more than size: a red bullet or item at the far edge must not
		// steal the "player" position and make every dodge decision wrong.
		score := float64(c.area) + (c.cy-float64(startY))*2
		if haveLast {
			score -= math.Hypot(c.cx-lastX, c.cy-lastY) * 2.5
		}
		bestScore := float64(best.area) + (best.cy-float64(startY))*2
		if haveLast {
			bestScore -= math.Hypot(best.cx-lastX, best.cy-lastY) * 2.5
		}
		if !found || score > bestScore {
			best, found = c, true
		}
	}
	return best, found
}

func findBullets(f domain.Frame, playW int, px, py float64) []blob {
	mask := make([]bool, playW*f.Height)
	for y := 0; y < f.Height; y++ {
		for x := 0; x < playW; x++ {
			r, g, b := pix(f, x, y)
			if r >= 205 && g >= 205 && b >= 205 {
				mask[y*playW+x] = true
			}
		}
	}
	comps := components(mask, playW, f.Height, 2)
	out := make([]blob, 0, len(comps))
	for _, c := range comps {
		w, h := c.maxX-c.minX+1, c.maxY-c.minY+1
		if c.area < 3 || c.area > 160 || w > 26 || h > 26 {
			continue
		}
		if float64(w)/float64(h) < 0.35 || float64(w)/float64(h) > 2.8 {
			continue
		}
		// Ignore the player's own white bow.
		dx := c.cx - px*float64(playW)
		dy := c.cy - py*float64(f.Height)
		if dx*dx+dy*dy < 14*14 {
			continue
		}
		out = append(out, c)
	}
	return out
}

func safetyAt(x, y float64, bullets []blob) float64 {
	threat := 0.0
	for _, b := range bullets {
		dx, dy := x-b.cx, y-b.cy
		d := math.Hypot(dx, dy)
		if d < 44 {
			threat += (44 - d) / 44
		}
	}
	return 1.0 / (1.0 + threat*2.2)
}

func components(mask []bool, w, h, minArea int) []blob {
	seen := make([]bool, len(mask))
	var out []blob
	stack := make([]int, 0, 64)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := y*w + x
			if !mask[i] || seen[i] {
				continue
			}
			seen[i] = true
			stack = stack[:0]
			stack = append(stack, i)
			c := blob{minX: x, maxX: x, minY: y, maxY: y}
			sumX, sumY := 0, 0
			for len(stack) > 0 {
				idx := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				cx, cy := idx%w, idx/w
				c.area++
				sumX += cx
				sumY += cy
				if cx < c.minX {
					c.minX = cx
				}
				if cx > c.maxX {
					c.maxX = cx
				}
				if cy < c.minY {
					c.minY = cy
				}
				if cy > c.maxY {
					c.maxY = cy
				}
				for _, d := range [4][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
					nx, ny := cx+d[0], cy+d[1]
					if nx < 0 || nx >= w || ny < 0 || ny >= h {
						continue
					}
					ni := ny*w + nx
					if mask[ni] && !seen[ni] {
						seen[ni] = true
						stack = append(stack, ni)
					}
				}
			}
			if c.area >= minArea {
				c.cx = float64(sumX) / float64(c.area)
				c.cy = float64(sumY) / float64(c.area)
				out = append(out, c)
			}
		}
	}
	return out
}
