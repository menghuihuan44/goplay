package localmodel

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"goplay/internal/metrics"
)

// BenchMetrics receives the latest TTFT/TPS sample. Callers may pass their own
// Registry in BenchConfig; otherwise this package-level registry is updated.
var BenchMetrics = metrics.New()

// BenchConfig controls one streaming benchmark request against an
// OpenAI-compatible endpoint (llama-server exposes /v1/chat/completions).
type BenchConfig struct {
	BaseURL        string
	Model          string
	Prompt         string
	MaxTokens      int
	Temperature    float64
	RequestTimeout time.Duration
	Client         *http.Client
	Metrics        *metrics.Registry
}

func (c BenchConfig) withDefaults() BenchConfig {
	if strings.TrimSpace(c.BaseURL) == "" {
		c.BaseURL = "http://127.0.0.1:8421/v1"
	}
	c.BaseURL = strings.TrimRight(c.BaseURL, "/")
	if strings.TrimSpace(c.Prompt) == "" {
		c.Prompt = "Reply with one short sentence: why are deterministic rule checks useful for game validation?"
	}
	if c.MaxTokens <= 0 {
		c.MaxTokens = 32
	}
	if c.Temperature < 0 {
		c.Temperature = 0
	}
	if c.RequestTimeout <= 0 {
		c.RequestTimeout = 120 * time.Second
	}
	if c.Client == nil {
		c.Client = &http.Client{}
	}
	return c
}

// BenchResult is the measured latency/throughput. TTFTMS is measured from just
// before the HTTP request to the first non-empty content delta; TPS uses
// completion_tokens (or a delta count fallback) divided by the time from the
// first delta to the end of the stream.
type BenchResult struct {
	BaseURL          string  `json:"base_url"`
	Model            string  `json:"model,omitempty"`
	TTFTMS           float64 `json:"ttft_ms"`
	TPS              float64 `json:"tokens_per_second"`
	PromptTokens     int     `json:"prompt_tokens,omitempty"`
	CompletionTokens int     `json:"completion_tokens,omitempty"`
	TotalMS          float64 `json:"total_ms"`
	FirstTokenMS     float64 `json:"first_token_ms"`
}

type sseUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type sseChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
		Text string `json:"text"`
	} `json:"choices"`
	Usage *sseUsage `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Bench runs one streaming chat completion and records TTFT/TPS into
// metrics.LocalTTFTMS and metrics.LocalTPS.
func Bench(ctx context.Context, cfg BenchConfig) (BenchResult, error) {
	c := cfg.withDefaults()
	url := c.BaseURL + "/chat/completions"
	payload := map[string]any{
		"messages": []map[string]string{{"role": "user", "content": c.Prompt}},
		"stream":   true,
		"stream_options": map[string]bool{
			"include_usage": true,
		},
		"max_tokens":  c.MaxTokens,
		"temperature": c.Temperature,
	}
	if c.Model != "" {
		payload["model"] = c.Model
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return BenchResult{}, err
	}

	reqCtx, cancel := context.WithTimeout(ctx, c.RequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return BenchResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	start := time.Now()
	resp, err := c.Client.Do(req)
	if err != nil {
		return BenchResult{}, fmt.Errorf("bench request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return BenchResult{}, fmt.Errorf("bench HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	result := BenchResult{BaseURL: c.BaseURL, Model: c.Model}
	var (
		firstToken time.Time
		usage      *sseUsage
		deltas     int
		events     int
	)
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			break
		}
		events++
		var ch sseChunk
		if err := json.Unmarshal([]byte(data), &ch); err != nil {
			// Some proxies emit keep-alive comments; ignore malformed data only if
			// it is not valid JSON but stream continues.
			continue
		}
		if ch.Error != nil {
			return BenchResult{}, fmt.Errorf("bench stream error: %s", ch.Error.Message)
		}
		if ch.Usage != nil {
			usage = ch.Usage
		}
		text := ""
		if len(ch.Choices) > 0 {
			text = ch.Choices[0].Delta.Content
			if text == "" {
				text = ch.Choices[0].Text
			}
		}
		if text != "" {
			if firstToken.IsZero() {
				firstToken = time.Now()
			}
			deltas++
		}
	}
	if err := scanner.Err(); err != nil {
		return BenchResult{}, fmt.Errorf("read SSE stream: %w", err)
	}
	if events == 0 {
		return BenchResult{}, fmt.Errorf("bench: no SSE data events received from %s", url)
	}
	end := time.Now()
	if firstToken.IsZero() {
		return BenchResult{}, fmt.Errorf("bench: stream produced no content tokens")
	}
	completion := deltas
	if usage != nil {
		result.PromptTokens = usage.PromptTokens
		if usage.CompletionTokens > 0 {
			completion = usage.CompletionTokens
		}
	}
	if completion <= 0 {
		return BenchResult{}, fmt.Errorf("bench: no completion tokens counted")
	}
	elapsed := end.Sub(firstToken).Seconds()
	if elapsed <= 0 {
		// A mock or fully buffered stream can deliver every token in one read.
		// Fall back to request duration so TPS stays finite instead of dividing
		// by zero; real streaming servers always have a positive window.
		elapsed = end.Sub(start).Seconds()
	}
	if elapsed <= 0 {
		return BenchResult{}, fmt.Errorf("bench: non-positive generation duration")
	}
	result.CompletionTokens = completion
	result.TTFTMS = float64(firstToken.Sub(start)) / float64(time.Millisecond)
	result.FirstTokenMS = result.TTFTMS
	result.TotalMS = float64(end.Sub(start)) / float64(time.Millisecond)
	result.TPS = float64(completion) / elapsed

	reg := c.Metrics
	if reg == nil {
		reg = BenchMetrics
	}
	reg.Set(metrics.LocalTTFTMS, result.TTFTMS)
	reg.Set(metrics.LocalTPS, result.TPS)
	return result, nil
}

// BenchCheck is one budget assertion for a tier.
type BenchCheck struct {
	Name   string  `json:"name"`
	OK     bool    `json:"ok"`
	Actual float64 `json:"actual"`
	Target float64 `json:"target"`
	Detail string  `json:"detail"`
}

// EvaluateTier compares a benchmark result with a tier's TTFT budget and TPS
// floor. A low-tier device is expected to satisfy both thresholds; the caller
// decides whether a missed budget should block startup.
func EvaluateTier(t TierSpec, r BenchResult) []BenchCheck {
	ttft := BenchCheck{
		Name:   "ttft_ms",
		OK:     r.TTFTMS <= float64(t.TTFTBudgetMS) && r.TTFTMS > 0,
		Actual: r.TTFTMS,
		Target: float64(t.TTFTBudgetMS),
	}
	if ttft.OK {
		ttft.Detail = fmt.Sprintf("%.1fms <= budget %dms", r.TTFTMS, t.TTFTBudgetMS)
	} else {
		ttft.Detail = fmt.Sprintf("%.1fms > budget %dms", r.TTFTMS, t.TTFTBudgetMS)
	}
	tps := BenchCheck{
		Name:   "tokens_per_second",
		OK:     r.TPS >= t.TPSFloor && r.TPS > 0,
		Actual: r.TPS,
		Target: t.TPSFloor,
	}
	if tps.OK {
		tps.Detail = fmt.Sprintf("%.1f t/s >= floor %.1f t/s", r.TPS, t.TPSFloor)
	} else {
		tps.Detail = fmt.Sprintf("%.1f t/s < floor %.1f t/s", r.TPS, t.TPSFloor)
	}
	return []BenchCheck{ttft, tps}
}

// AllChecksOK reports whether every budget check passed.
func AllChecksOK(checks []BenchCheck) bool {
	for _, c := range checks {
		if !c.OK {
			return false
		}
	}
	return true
}
