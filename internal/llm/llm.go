package llm

import (
	"context"
	"errors"
	"strings"
	"time"
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Request struct {
	System      string
	Prompt      string
	Images      []string // base64 PNG/JPEG, no data: prefix
	Temperature float64
	MaxTokens   int
	JSON        bool
}

type Response struct {
	Text             string
	PromptTokens     int
	CompletionTokens int
	Model            string
	Provider         string
	Latency          time.Duration
	TTFT             time.Duration
}

type Provider interface {
	Name() string
	Complete(ctx context.Context, req Request) (Response, error)
	Available(ctx context.Context) bool
}

type StreamProvider interface {
	Provider
	// CompleteStream calls onToken for each delta; ttft is measured from the
	// request start to the first non-empty delta.
	CompleteStream(ctx context.Context, req Request, onToken func(delta string, ttft time.Duration)) (Response, error)
}

var ErrNoProvider = errors.New("no LLM provider available")

type Router struct {
	Mode  string
	API   Provider
	Local Provider
}

func NewRouter(mode string, api, local Provider) *Router {
	if mode == "" {
		mode = "auto"
	}
	return &Router{Mode: mode, API: api, Local: local}
}

func (r *Router) Name() string { return "router:" + r.Mode }

// Available implements Provider so a Router can be injected anywhere a single
// provider is expected (the policy layer uses this).
func (r *Router) Available(ctx context.Context) bool {
	switch r.Mode {
	case "offline":
		return false
	case "api":
		return r.API != nil && r.API.Available(ctx)
	case "local":
		return r.Local != nil && r.Local.Available(ctx)
	default:
		if r.Local != nil && r.Local.Available(ctx) {
			return true
		}
		return r.API != nil && r.API.Available(ctx)
	}
}

func (r *Router) Status(ctx context.Context) map[string]any {
	out := map[string]any{"mode": r.Mode}
	if r.API != nil {
		out["api"] = r.API.Name()
		out["api_available"] = r.API.Available(ctx)
	}
	if r.Local != nil {
		out["local"] = r.Local.Name()
		out["local_available"] = r.Local.Available(ctx)
	}
	return out
}

func (r *Router) Complete(ctx context.Context, req Request) (Response, error) {
	switch r.Mode {
	case "offline":
		return Response{}, ErrNoProvider
	case "api":
		if r.API == nil {
			return Response{}, ErrNoProvider
		}
		return r.API.Complete(ctx, req)
	case "local":
		if r.Local == nil {
			return Response{}, ErrNoProvider
		}
		return r.Local.Complete(ctx, req)
	default: // auto: prefer local (privacy, no API key), fall back to API
		if r.Local != nil && r.Local.Available(ctx) {
			if resp, err := r.Local.Complete(ctx, req); err == nil {
				return resp, nil
			}
		}
		if r.API != nil && r.API.Available(ctx) {
			return r.API.Complete(ctx, req)
		}
		return Response{}, ErrNoProvider
	}
}

// ExtractJSON returns the first balanced JSON object in text, tolerating
// markdown fences and leading prose from small models.
func ExtractJSON(text string) (string, bool) {
	s := strings.TrimSpace(text)
	if i := strings.Index(s, "```"); i >= 0 {
		s = s[i+3:]
		s = strings.TrimPrefix(s, "json")
		s = strings.TrimPrefix(s, "JSON")
		s = strings.TrimSpace(s)
		if j := strings.Index(s, "```"); j >= 0 {
			s = s[:j]
		}
	}
	start := strings.Index(s, "{")
	if start < 0 {
		start = strings.Index(s, "[")
		if start < 0 {
			return "", false
		}
	}
	if s[start] == '[' {
		depth := 0
		inStr := false
		esc := false
		for i := start; i < len(s); i++ {
			c := s[i]
			if inStr {
				if esc {
					esc = false
				} else if c == '\\' {
					esc = true
				} else if c == '"' {
					inStr = false
				}
				continue
			}
			switch c {
			case '"':
				inStr = true
			case '[':
				depth++
			case ']':
				depth--
				if depth == 0 {
					return s[start : i+1], true
				}
			}
		}
		return "", false
	}
	depth := 0
	inStr := false
	esc := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inStr {
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == '"' {
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1], true
			}
		}
	}
	return "", false
}
