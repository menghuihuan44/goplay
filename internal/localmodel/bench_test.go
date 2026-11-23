package localmodel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"goplay/internal/metrics"
)

func sseEvent(w http.ResponseWriter, f http.Flusher, v any) {
	b, _ := json.Marshal(v)
	_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
	f.Flush()
}

func TestBenchMeasuresTTFTAndTPS(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode payload: %v", err)
			return
		}
		stream, _ := payload["stream"].(bool)
		opts, _ := payload["stream_options"].(map[string]any)
		includeUsage, _ := opts["include_usage"].(bool)
		maxTokens, _ := payload["max_tokens"].(float64)
		messages, _ := payload["messages"].([]any)
		if !stream || !includeUsage || maxTokens != 8 {
			t.Errorf("payload = %+v", payload)
		}
		if len(messages) != 1 {
			t.Errorf("messages = %+v", messages)
		} else {
			msg, _ := messages[0].(map[string]any)
			if msg["role"] != "user" || msg["content"] == "" {
				t.Errorf("message = %+v", msg)
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		f, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("httptest ResponseWriter is not a Flusher")
		}
		sseEvent(w, f, map[string]any{"choices": []any{
			map[string]any{"delta": map[string]any{"content": "Hel"}},
		}})
		time.Sleep(25 * time.Millisecond)
		sseEvent(w, f, map[string]any{"choices": []any{
			map[string]any{"delta": map[string]any{"content": "lo"}},
		}})
		time.Sleep(10 * time.Millisecond)
		sseEvent(w, f, map[string]any{
			"choices": []any{},
			"usage": map[string]any{
				"prompt_tokens":     7,
				"completion_tokens": 2,
				"total_tokens":      9,
			},
		})
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		f.Flush()
	}))
	defer srv.Close()

	reg := metrics.New()
	res, err := Bench(context.Background(), BenchConfig{
		BaseURL:   srv.URL + "/v1/",
		MaxTokens: 8,
		Metrics:   reg,
	})
	if err != nil {
		t.Fatalf("Bench: %v", err)
	}
	if gotPath != "/v1/chat/completions" {
		t.Errorf("path = %q", gotPath)
	}
	if res.BaseURL != srv.URL+"/v1" {
		t.Errorf("BaseURL = %q", res.BaseURL)
	}
	if res.TTFTMS < 0 || res.TTFTMS > res.TotalMS+1 {
		t.Errorf("TTFT = %.3fms, total = %.3fms", res.TTFTMS, res.TotalMS)
	}
	if res.TPS <= 0 {
		t.Errorf("TPS = %.3f", res.TPS)
	}
	if res.CompletionTokens != 2 || res.PromptTokens != 7 {
		t.Errorf("tokens = prompt %d completion %d", res.PromptTokens, res.CompletionTokens)
	}
	if res.TotalMS < res.TTFTMS {
		t.Errorf("TotalMS %.3f < TTFT %.3f", res.TotalMS, res.TTFTMS)
	}
	if got := reg.Gauge(metrics.LocalTTFTMS); got != res.TTFTMS {
		t.Errorf("recorded TTFT = %v, want %v", got, res.TTFTMS)
	}
	if got := reg.Gauge(metrics.LocalTPS); got != res.TPS {
		t.Errorf("recorded TPS = %v, want %v", got, res.TPS)
	}
}

func TestBenchHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "model loading", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	_, err := Bench(context.Background(), BenchConfig{BaseURL: srv.URL + "/v1", Metrics: metrics.New()})
	if err == nil || !strings.Contains(err.Error(), "503") || !strings.Contains(err.Error(), "model loading") {
		t.Fatalf("error = %v", err)
	}
}

func TestBenchStreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"error\":{\"message\":\"boom\"}}\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	defer srv.Close()
	_, err := Bench(context.Background(), BenchConfig{BaseURL: srv.URL + "/v1", Metrics: metrics.New()})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("error = %v", err)
	}
}

func TestBenchNoContentTokens(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":0}}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	_, err := Bench(context.Background(), BenchConfig{BaseURL: srv.URL + "/v1", Metrics: metrics.New()})
	if err == nil || !strings.Contains(err.Error(), "no content tokens") {
		t.Fatalf("error = %v", err)
	}
}

func TestBenchIgnoresNonDataLines(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, ": keep-alive\n\n")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"completion_tokens\":1}}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	res, err := Bench(context.Background(), BenchConfig{BaseURL: srv.URL + "/v1", Metrics: metrics.New()})
	if err != nil {
		t.Fatalf("Bench: %v", err)
	}
	if res.CompletionTokens != 1 || res.TPS <= 0 {
		t.Fatalf("result = %+v", res)
	}
}

func TestEvaluateTierLowBudget(t *testing.T) {
	low, ok := NewCatalog("").Tier(TierLow)
	if !ok {
		t.Fatal("low tier missing")
	}
	pass := BenchResult{TTFTMS: 120, TPS: 30}
	checks := EvaluateTier(low, pass)
	if len(checks) != 2 || !AllChecksOK(checks) {
		t.Fatalf("checks = %+v", checks)
	}
	fail := BenchResult{TTFTMS: float64(low.TTFTBudgetMS) + 1, TPS: low.TPSFloor - 1}
	checks = EvaluateTier(low, fail)
	if len(checks) != 2 || AllChecksOK(checks) {
		t.Fatalf("checks = %+v", checks)
	}
	for _, c := range checks {
		if c.OK {
			t.Errorf("%s unexpectedly passed", c.Name)
		}
	}
	// Exact boundary values pass.
	boundary := BenchResult{TTFTMS: float64(low.TTFTBudgetMS), TPS: low.TPSFloor}
	if !AllChecksOK(EvaluateTier(low, boundary)) {
		t.Errorf("boundary values should pass")
	}
}
