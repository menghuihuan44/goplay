package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type OpenAIConfig struct {
	BaseURL     string
	APIKey      string
	Model       string
	Timeout     time.Duration
	Temperature float64
	MaxTokens   int
	Provider    string
}

type OpenAI struct {
	cfg  OpenAIConfig
	http *http.Client
}

func NewOpenAI(cfg OpenAIConfig) *OpenAI {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.openai.com/v1"
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	if cfg.Timeout <= 0 {
		cfg.Timeout = 45 * time.Second
	}
	if cfg.Temperature <= 0 {
		cfg.Temperature = 0.2
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = 512
	}
	return &OpenAI{cfg: cfg, http: &http.Client{Timeout: cfg.Timeout}}
}

func (o *OpenAI) Name() string {
	if o.cfg.Provider != "" {
		return o.cfg.Provider
	}
	return "openai-compatible"
}

func (o *OpenAI) Available(ctx context.Context) bool {
	if o.cfg.APIKey == "" {
		u := strings.ToLower(o.cfg.BaseURL)
		return strings.Contains(u, "127.0.0.1") || strings.Contains(u, "localhost")
	}
	return true
}

type chatMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

func (o *OpenAI) Complete(ctx context.Context, req Request) (Response, error) {
	start := time.Now()
	body, err := o.buildBody(req, false)
	if err != nil {
		return Response{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, o.cfg.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return Response{}, err
	}
	o.headers(httpReq)
	resp, err := o.http.Do(httpReq)
	if err != nil {
		return Response{}, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return Response{}, err
	}
	if resp.StatusCode/100 != 2 {
		return Response{}, fmt.Errorf("llm %s: http %d: %s", o.Name(), resp.StatusCode, trim(string(b), 500))
	}
	var parsed struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(b, &parsed); err != nil {
		return Response{}, fmt.Errorf("decode llm response: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return Response{}, fmt.Errorf("llm %s: no choices", o.Name())
	}
	return Response{
		Text:             parsed.Choices[0].Message.Content,
		PromptTokens:     parsed.Usage.PromptTokens,
		CompletionTokens: parsed.Usage.CompletionTokens,
		Model:            firstNonEmpty(parsed.Model, o.cfg.Model),
		Provider:         o.Name(),
		Latency:          time.Since(start),
	}, nil
}

func (o *OpenAI) CompleteStream(ctx context.Context, req Request, onToken func(delta string, ttft time.Duration)) (Response, error) {
	start := time.Now()
	req.JSON = false
	body, err := o.buildBody(req, true)
	if err != nil {
		return Response{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, o.cfg.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return Response{}, err
	}
	o.headers(httpReq)
	httpReq.Header.Set("Accept", "text/event-stream")
	resp, err := o.http.Do(httpReq)
	if err != nil {
		return Response{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return Response{}, fmt.Errorf("llm %s: http %d: %s", o.Name(), resp.StatusCode, trim(string(b), 500))
	}
	var text strings.Builder
	var ttft time.Duration
	tokens := 0
	scanner := newSSEScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		for _, c := range chunk.Choices {
			if c.Delta.Content == "" {
				continue
			}
			if ttft == 0 {
				ttft = time.Since(start)
			}
			tokens++
			text.WriteString(c.Delta.Content)
			if onToken != nil {
				onToken(c.Delta.Content, ttft)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return Response{}, err
	}
	return Response{
		Text:             text.String(),
		CompletionTokens: tokens,
		Model:            o.cfg.Model,
		Provider:         o.Name(),
		Latency:          time.Since(start),
		TTFT:             ttft,
	}, nil
}

func (o *OpenAI) buildBody(req Request, stream bool) ([]byte, error) {
	system := req.System
	if system == "" {
		system = "You are a precise game-playing agent. Reply with a single JSON object when asked."
	}
	userContent := any(req.Prompt)
	if len(req.Images) > 0 {
		parts := []map[string]any{{"type": "text", "text": req.Prompt}}
		for _, img := range req.Images {
			parts = append(parts, map[string]any{
				"type":      "image_url",
				"image_url": map[string]any{"url": "data:image/png;base64," + img},
			})
		}
		userContent = parts
	}
	payload := map[string]any{
		"model": o.cfg.Model,
		"messages": []chatMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: userContent},
		},
		"temperature": req.Temperature,
		"max_tokens":  req.MaxTokens,
		"stream":      stream,
	}
	if req.Temperature <= 0 {
		payload["temperature"] = o.cfg.Temperature
	}
	if req.MaxTokens <= 0 {
		payload["max_tokens"] = o.cfg.MaxTokens
	}
	if stream {
		payload["stream_options"] = map[string]any{"include_usage": true}
	}
	return json.Marshal(payload)
}

func (o *OpenAI) headers(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	if o.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+o.cfg.APIKey)
	}
}

func trim(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}

// sseScanner is a minimal line scanner kept separate to avoid importing
// bufio in tests.
type sseScanner struct {
	r    io.Reader
	buf  []byte
	line string
	err  error
}

func newSSEScanner(r io.Reader) *sseScanner {
	return &sseScanner{r: r}
}

func (s *sseScanner) Scan() bool {
	if s.err != nil {
		return false
	}
	for {
		if i := bytes.IndexByte(s.buf, '\n'); i >= 0 {
			s.line = strings.TrimRight(string(s.buf[:i]), "\r")
			s.buf = s.buf[i+1:]
			return true
		}
		tmp := make([]byte, 4096)
		n, err := s.r.Read(tmp)
		if n > 0 {
			s.buf = append(s.buf, tmp[:n]...)
		}
		if err != nil {
			if err == io.EOF {
				if len(s.buf) > 0 {
					s.line = strings.TrimRight(string(s.buf), "\r\n")
					s.buf = nil
					return true
				}
				s.err = io.EOF
				return false
			}
			s.err = err
			return false
		}
	}
}

func (s *sseScanner) Text() string { return s.line }
func (s *sseScanner) Err() error {
	if s.err == io.EOF {
		return nil
	}
	return s.err
}
