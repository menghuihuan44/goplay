package perception

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"strings"
	"sync"
	"time"

	"goplay/internal/domain"
	"goplay/internal/llm"
)

// VisionAnalyzer adds a multimodal LLM pass for unknown games. Deterministic
// pixel statistics remain the fallback: the model is asked only to describe what
// is visible, never to invent hidden game state. Calls are throttled because a
// screenshot + generation is far more expensive than a tick.
type VisionAnalyzer struct {
	Provider    llm.Provider
	Fallback    Analyzer
	MinInterval time.Duration
	MaxWidth    int
	Timeout     time.Duration

	mu       sync.Mutex
	lastAt   time.Time
	last     domain.State
	lastErr  error
	calls    int64
	failures int64
}

func NewVisionAnalyzer(p llm.Provider, fallback Analyzer) *VisionAnalyzer {
	if fallback == nil {
		fallback = NewGenericAnalyzer()
	}
	return &VisionAnalyzer{Provider: p, Fallback: fallback, MinInterval: time.Second, MaxWidth: 1280, Timeout: 12 * time.Second}
}

func (v *VisionAnalyzer) Name() string { return "vision+visual-v2" }

func (v *VisionAnalyzer) Stats() map[string]any {
	v.mu.Lock()
	defer v.mu.Unlock()
	return map[string]any{"calls": v.calls, "failures": v.failures, "last_error": fmt.Sprint(v.lastErr)}
}

func (v *VisionAnalyzer) Analyze(f domain.Frame) (domain.State, map[string]any, error) {
	base, world, baseErr := v.Fallback.Analyze(f)
	if v.Provider == nil || !v.Provider.Available(context.Background()) {
		return base, world, baseErr
	}
	v.mu.Lock()
	if !v.lastAt.IsZero() && time.Since(v.lastAt) < v.MinInterval && v.last.Summary != "" {
		st := v.last
		v.mu.Unlock()
		return st, worldFromState(st), baseErr
	}
	v.mu.Unlock()

	img, err := encodeFramePNG(f, v.MaxWidth)
	if err != nil {
		return base, world, baseErr
	}
	ctx, cancel := context.WithTimeout(context.Background(), v.Timeout)
	defer cancel()
	resp, err := v.Provider.Complete(ctx, llm.Request{
		System: "You are the perception stage of an external game-playing agent. Describe only what is visible. Never claim to read memory or internal state.",
		Prompt: "Look at this game screenshot. Return one JSON object with keys: " +
			"summary (short Chinese or English description), tags (array of short lowercase labels), " +
			"values (object of numeric HUD facts you can actually see, e.g. hp, max_hp, score, timer, level), " +
			"text (array of important visible strings), confidence (0..1). " +
			"If this looks like a menu or loading screen include tag menu or loading. Do not invent numbers.",
		Images:      []string{base64.StdEncoding.EncodeToString(img)},
		Temperature: 0.1,
		MaxTokens:   300,
		JSON:        true,
	})
	if err != nil {
		v.mu.Lock()
		v.lastErr = err
		v.failures++
		v.mu.Unlock()
		return base, world, baseErr
	}
	raw, ok := llm.ExtractJSON(resp.Text)
	if !ok {
		v.mu.Lock()
		v.lastErr = fmt.Errorf("vision response had no JSON")
		v.failures++
		v.mu.Unlock()
		return base, world, baseErr
	}
	var parsed struct {
		Summary    string             `json:"summary"`
		Tags       []string           `json:"tags"`
		Values     map[string]float64 `json:"values"`
		Text       []string           `json:"text"`
		Confidence float64            `json:"confidence"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		v.mu.Lock()
		v.lastErr = err
		v.failures++
		v.mu.Unlock()
		return base, world, baseErr
	}
	text := map[string]string{}
	for i, s := range parsed.Text {
		if s = strings.TrimSpace(s); s != "" {
			text[fmt.Sprintf("t%d", i)] = s
		}
	}
	st := domain.State{
		Summary:    strings.TrimSpace(parsed.Summary),
		Values:     parsed.Values,
		Text:       text,
		Tags:       cleanTags(parsed.Tags),
		Confidence: parsed.Confidence,
		At:         time.Now(),
	}
	if st.Summary == "" {
		st.Summary = base.Summary
	}
	if st.Confidence <= 0 {
		st.Confidence = 0.55
	}
	if len(st.Tags) == 0 {
		st.Tags = base.Tags
	}
	v.mu.Lock()
	v.lastAt = time.Now()
	v.last = st
	v.calls++
	v.lastErr = nil
	v.mu.Unlock()
	return st, worldFromState(st), baseErr
}

func cleanTags(tags []string) []string {
	out := make([]string, 0, len(tags))
	seen := map[string]bool{}
	for _, t := range tags {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" || len(t) > 40 || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
		if len(out) >= 16 {
			break
		}
	}
	return out
}

func worldFromState(st domain.State) map[string]any {
	w := map[string]any{"summary": st.Summary, "confidence": st.Confidence}
	for k, v := range st.Values {
		w[k] = v
	}
	if len(st.Tags) > 0 {
		w["tags"] = st.Tags
	}
	if len(st.Text) > 0 {
		w["text"] = st.Text
	}
	return w
}

func encodeFramePNG(f domain.Frame, maxWidth int) ([]byte, error) {
	if err := ValidateFrame(f); err != nil {
		return nil, err
	}
	src := &image.RGBA{Pix: f.RGBA, Stride: f.Width * 4, Rect: image.Rect(0, 0, f.Width, f.Height)}
	var img image.Image = src
	if maxWidth > 0 && f.Width > maxWidth {
		scale := float64(maxWidth) / float64(f.Width)
		dw, dh := maxWidth, int(float64(f.Height)*scale)
		if dh < 1 {
			dh = 1
		}
		dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
		for y := 0; y < dh; y++ {
			sy := y * f.Height / dh
			for x := 0; x < dw; x++ {
				sx := x * f.Width / dw
				si := sy*src.Stride + sx*4
				di := y*dst.Stride + x*4
				copy(dst.Pix[di:di+4], src.Pix[si:si+4])
			}
		}
		img = dst
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
