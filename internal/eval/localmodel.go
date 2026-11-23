package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"time"

	"goplay/internal/llm"
	"goplay/internal/metrics"
)

func boolMetric(v bool) float64 {
	if v {
		return 1
	}
	return 0
}

// runLocalModel proves the local-model metric chain (TTFT and tokens/sec)
// without requiring a real model. A local SSE server mimics an OpenAI-
// compatible /chat/completions stream, then llm.OpenAI parses it through the
// same code path used for a real local endpoint. llm.Mock covers the offline
// provider fallback.
func runLocalModel() (s Suite) {
	s = Suite{ID: "localmodel", Title: "Local model TTFT/TPS metric chain"}
	start := time.Now()
	defer func() { s.DurationMS = time.Since(start).Milliseconds() }()

	tokens := []string{"alpha ", "beta ", "gamma ", "delta ", "alpha ", "beta ", "gamma ", "delta "}
	want := strings.Join(tokens, "")
	const tokenDelay = 10 * time.Millisecond

	var authSeen atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "" {
			authSeen.Store(true)
		}
		// A non-stream request is used only to warm the loopback connection:
		// on some Windows hosts the first TCP connect to a fresh local listener
		// can take seconds (firewall/EDR inspection), which would otherwise be
		// misattributed to model TTFT.
		var body struct {
			Stream bool `json:"stream"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if !body.Stream {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"model":"mock-local","choices":[{"message":{"content":"warm"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		flusher, _ := w.(http.Flusher)
		for _, tok := range tokens {
			b, _ := json.Marshal(tok)
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%s}}]}\n\n", b)
			if flusher != nil {
				flusher.Flush()
			}
			time.Sleep(tokenDelay)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer srv.Close()

	client := llm.NewOpenAI(llm.OpenAIConfig{
		BaseURL: srv.URL, Model: "mock-local", Provider: "mock-local",
		Timeout: 10 * time.Second, APIKey: "test-key",
	})
	var provider llm.StreamProvider = client
	if !client.Available(context.Background()) {
		s.Error = "local client is not available"
		s.Finalize()
		return s
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Warm the TCP/TLS path (and the HTTP keep-alive connection) before
	// timing the stream, so TTFT measures the model, not first-connect cost.
	warmStart := time.Now()
	warm, err := client.Complete(ctx, llm.Request{Prompt: "warmup", MaxTokens: 1})
	warmupMS := float64(time.Since(warmStart).Milliseconds())
	if err == nil && !strings.Contains(warm.Text, "warm") {
		err = fmt.Errorf("unexpected warmup reply %q", warm.Text)
	}
	if err != nil {
		s.Error = fmt.Sprintf("stream warmup: %v", err)
		s.Finalize()
		return s
	}
	req := llm.Request{System: "return text", Prompt: "say alpha beta gamma delta twice", MaxTokens: 32}
	resp, err := provider.CompleteStream(ctx, req, nil)
	if err != nil {
		s.Error = fmt.Sprintf("stream: %v", err)
		s.Finalize()
		return s
	}
	textOK := resp.Text == want

	elapsed := resp.Latency - resp.TTFT
	if elapsed <= 0 {
		elapsed = resp.Latency
	}
	tps := 0.0
	if elapsed > 0 && resp.CompletionTokens > 0 {
		tps = float64(resp.CompletionTokens) / elapsed.Seconds()
	}

	// Wire the metrics exactly like the product metric names.
	reg := metrics.New()
	reg.Observe(metrics.LocalTTFTMS, float64(resp.TTFT.Milliseconds()))
	reg.Set(metrics.LocalTPS, tps)
	ttftSamples, _, _, _, _ := reg.Hist(metrics.LocalTTFTMS)
	tpsGauge := reg.Gauge(metrics.LocalTPS)

	mock := &llm.Mock{Reply: `{"kind":"attack","reason":"mock"}`, Delay: 15 * time.Millisecond}
	mockStart := time.Now()
	mockResp, err := mock.Complete(ctx, llm.Request{Prompt: "ACTIONS: [{\"kind\":\"attack\"}]"})
	mockLatency := float64(time.Since(mockStart).Milliseconds())
	mockOK := err == nil && strings.Contains(mockResp.Text, "attack")

	s.Add("localmodel.stream_ttft_ms", "SSE first-token latency", float64(resp.TTFT.Milliseconds()), "ms", "measured by llm.OpenAI.CompleteStream")
	s.Add("localmodel.stream_latency_ms", "SSE full stream latency", float64(resp.Latency.Milliseconds()), "ms", "")
	s.Add("localmodel.stream_tokens", "streamed completion tokens", float64(resp.CompletionTokens), "tokens", "")
	s.Add("localmodel.stream_tps", "decode tokens per second after first token", tps, "tps", "tokens / (latency - TTFT)")
	s.Add("localmodel.metrics_ttft_samples", "LocalTTFTMS histogram samples", float64(ttftSamples), "count", "")
	s.Add("localmodel.metrics_tps_gauge", "LocalTPS gauge value", tpsGauge, "tps", "")
	s.Add("localmodel.mock_latency_ms", "llm.Mock latency", mockLatency, "ms", "")
	s.Add("localmodel.warmup_ms", "preflight request including first loopback connect", warmupMS, "ms", "absorbed before TTFT timing")

	s.Check("localmodel.ttft", "first token under 500ms", float64(resp.TTFT.Milliseconds()), "<=", 500, "ms", "")
	s.Check("localmodel.tps", "decode speed at least 20 tokens/sec", tps, ">=", 20, "tps", "8 tokens at 10ms/token")
	s.Check("localmodel.tokens", "all 8 stream deltas parsed", float64(resp.CompletionTokens), "==", 8, "tokens", "")
	s.Check("localmodel.text", "streamed text matches the mock", boolMetric(textOK), "==", 1, "count", "")
	s.Check("localmodel.metric_chain", "TTFT histogram and TPS gauge are wired", boolMetric(ttftSamples == 1 && tpsGauge > 0), "==", 1, "count", "")
	s.Check("localmodel.mock_provider", "llm.Mock completes offline", boolMetric(mockOK), "==", 1, "count", "no network")

	s.Details = map[string]any{
		"auth_header_seen":  authSeen.Load(),
		"want_text":         want,
		"got_text":          resp.Text,
		"ttft_ms":           resp.TTFT.Milliseconds(),
		"stream_latency_ms": resp.Latency.Milliseconds(),
		"completion_tokens": resp.CompletionTokens,
		"tps":               tps,
		"warmup_ms":         warmupMS,
		"mock_text":         mockResp.Text,
		"mock_latency_ms":   mockLatency,
	}
	s.Notes = append(s.Notes,
		"The SSE server is local httptest; no external network or real model is required.",
		"TTFT is measured by the production llm.OpenAI.CompleteStream parser, not by the test harness.",
		"A non-stream preflight request absorbs host-specific first-connect latency; its cost is reported separately as warmup_ms and is not counted as model TTFT.",
	)
	s.Finalize()
	return s
}
