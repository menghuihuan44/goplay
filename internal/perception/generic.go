package perception

import (
	"encoding/json"
	"fmt"

	"goplay/internal/bridge"
	"hash/fnv"
	"math"
	"strings"
	"sync"

	"goplay/internal/domain"
)

// GenericAnalyzer is the fallback for unknown games. It does not pretend to
// recognize game objects; it measures the parts of the frame any game exposes:
// layout, color, edges, how much the picture changed, and whether the agent is
// stuck on a static screen. Those numbers give the policy a real signal even
// when no vision model is configured.
type GenericAnalyzer struct {
	mu    sync.Mutex
	state *visualState
}

type visualState struct {
	frames     int
	prevLuma   []float64
	prevR      []float64
	prevG      []float64
	prevB      []float64
	prevSig    string
	prevChange float64
}

func NewGenericAnalyzer() *GenericAnalyzer { return &GenericAnalyzer{state: &visualState{}} }

func (a *GenericAnalyzer) Name() string { return "generic-visual-v2" }

const visualGridX = 24
const visualGridY = 14

// Analyze samples the frame into a fixed grid. It is deliberately cheap:
// O(pixels/16) and allocation-light, so it can run on both desktop and phone.
func (a *GenericAnalyzer) Analyze(f domain.Frame) (domain.State, map[string]any, error) {
	if err := ValidateFrame(f); err != nil {
		return domain.State{}, nil, err
	}
	run := a
	if run == nil || run.state == nil {
		run = NewGenericAnalyzer()
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	stt := run.state

	n := visualGridX * visualGridY
	luma := make([]float64, n)
	rr := make([]float64, n)
	gg := make([]float64, n)
	bb := make([]float64, n)
	edge := 0
	for gy := 0; gy < visualGridY; gy++ {
		for gx := 0; gx < visualGridX; gx++ {
			x0 := gx * f.Width / visualGridX
			x1 := (gx + 1) * f.Width / visualGridX
			y0 := gy * f.Height / visualGridY
			y1 := (gy + 1) * f.Height / visualGridY
			if x1 <= x0 {
				x1 = x0 + 1
			}
			if y1 <= y0 {
				y1 = y0 + 1
			}
			var sr, sg, sb, cnt float64
			for y := y0; y < y1; y += 3 {
				for x := x0; x < x1; x += 3 {
					r, g, b := pix(f, x, y)
					sr += float64(r)
					sg += float64(g)
					sb += float64(b)
					cnt++
				}
			}
			if cnt == 0 {
				cnt = 1
			}
			i := gy*visualGridX + gx
			rr[i], gg[i], bb[i] = sr/cnt, sg/cnt, sb/cnt
			luma[i] = 0.2126*sr/cnt + 0.7152*sg/cnt + 0.0722*sb/cnt
		}
	}
	// Edge density is a cheap proxy for UI complexity and text density.
	for y := 0; y < visualGridY; y++ {
		for x := 0; x < visualGridX; x++ {
			i := y*visualGridX + x
			if x+1 < visualGridX && math.Abs(luma[i]-luma[i+1]) > 18 {
				edge++
			}
			if y+1 < visualGridY && math.Abs(luma[i]-luma[i+visualGridX]) > 18 {
				edge++
			}
		}
	}
	edgeRatio := float64(edge) / float64(n*2)

	var delta, change float64
	if stt.prevLuma != nil {
		var sum float64
		changed := 0
		for i := range luma {
			d := math.Abs(luma[i] - stt.prevLuma[i])
			sum += d
			if d > 7 {
				changed++
			}
		}
		delta = sum / float64(n) / 255.0
		change = float64(changed) / float64(n)
	}
	sig := visualSignature(rr, gg, bb)
	sceneChanged := stt.prevSig != "" && (change >= 0.15 || (sig != stt.prevSig && delta > 0.035))
	stt.frames++
	stt.prevLuma = luma
	stt.prevR, stt.prevG, stt.prevB = rr, gg, bb
	stt.prevSig = sig
	stt.prevChange = change

	brightness := 0.0
	warm := 0
	dark := 0
	for i := range luma {
		brightness += luma[i]
		if rr[i] > bb[i]+25 {
			warm++
		}
		if luma[i] < 32 {
			dark++
		}
	}
	brightness /= float64(n)
	values := map[string]float64{
		"width": float64(f.Width), "height": float64(f.Height),
		"brightness":   brightness,
		"warm_ratio":   float64(warm) / float64(n),
		"dark_ratio":   float64(dark) / float64(n),
		"edge_ratio":   edgeRatio,
		"frame_delta":  delta,
		"scene_change": change,
	}
	changedFlag := 0.0
	if sceneChanged {
		changedFlag = 1
	}
	values["scene_changed"] = changedFlag

	tags := []string{"frame", "unknown_game"}
	if delta < 0.008 {
		tags = append(tags, "static")
	} else {
		tags = append(tags, "changing")
	}
	if change > 0.02 && !sceneChanged {
		tags = append(tags, "animating")
	}
	if sceneChanged {
		tags = append(tags, "new_scene")
	}
	summary := fmt.Sprintf("画面 %dx%d · 亮度 %.0f · 变化 %.0f%% · 边缘 %.0f%%",
		f.Width, f.Height, brightness, change*100, edgeRatio*100)
	if sceneChanged {
		summary += " · 场景切换"
	} else if delta < 0.008 {
		summary += " · 静态"
	}
	// OCR boxes come from the phone (on-device ML Kit). They are the difference
	// between random 3x3 tapping and actually pressing "开始游戏"/"继续游戏".
	regions := parseOCR(f.Meta["ocr"])
	uiText := make([]string, 0, len(regions))
	uiRegions := make([]any, 0, len(regions))
	for _, r := range regions {
		uiText = append(uiText, r.Text)
		uiRegions = append(uiRegions, map[string]any{
			"text": r.Text, "x": r.X, "y": r.Y, "w": r.W, "h": r.H,
		})
	}
	values["text_count"] = float64(len(regions))
	text := map[string]string{"visual": fmt.Sprintf("画面变化 %.0f%% · 边缘 %.0f%%", change*100, edgeRatio*100)}
	if len(uiText) > 0 {
		joined := strings.Join(uiText, " · ")
		if len([]rune(joined)) > 400 {
			joined = string([]rune(joined)[:400]) + "…"
		}
		text["ui_text"] = joined
		summary += fmt.Sprintf(" · 文字 %d", len(regions))
		tags = append(tags, "ui_text")
	}
	st := domain.State{
		Summary:    summary,
		Values:     values,
		Text:       text,
		Tags:       tags,
		Confidence: 0.55,
		Hash:       sig,
	}
	world := map[string]any{
		"width": f.Width, "height": f.Height, "brightness": brightness,
		"warm_ratio": values["warm_ratio"], "dark_ratio": values["dark_ratio"],
		"edge_ratio": edgeRatio, "frame_delta": delta, "scene_change": change,
		"scene_changed": sceneChanged, "scene_hash": sig,
		"ui_text": strings.Join(uiText, " · "), "ui_regions": uiRegions,
	}
	return st, world, nil
}

// visualSignature quantizes the 24x14 grid into a stable scene fingerprint.
// It is used for diagnostics and for distinguishing menu/gameplay states; it is
// too fine-grained to be a policy state key on its own.
func visualSignature(rr, gg, bb []float64) string {
	h := fnv.New64a()
	for i := range rr {
		q := func(v float64) byte {
			switch {
			case v < 40:
				return 0
			case v < 80:
				return 1
			case v < 130:
				return 2
			case v < 190:
				return 3
			default:
				return 4
			}
		}
		_, _ = h.Write([]byte{q(rr[i]), q(gg[i]), q(bb[i])})
	}
	return fmt.Sprintf("%016x", h.Sum64())
}

// parseOCR decodes the Android client's normalized OCR boxes. A malformed
// payload is ignored: perception must never fail because text recognition did.
func parseOCR(raw string) []bridge.TextRegion {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var out []bridge.TextRegion
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	clean := out[:0]
	for _, r := range out {
		r.Text = strings.TrimSpace(r.Text)
		if r.Text == "" || r.X < 0 || r.X > 1 || r.Y < 0 || r.Y > 1 {
			continue
		}
		clean = append(clean, r)
	}
	return clean
}
