package llm

import (
	"context"
	"errors"
	"strings"
	"sync"
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

// Router is the single decision point for API-vs-local providers. It is
// mutable at runtime so the model manager can attach a llama.cpp provider after
// a download finishes; every field is guarded by mu.
type Router struct {
	mu    sync.RWMutex
	mode  string
	api   Provider
	local Provider
}

func NewRouter(mode string, api, local Provider) *Router {
	if mode == "" {
		mode = "auto"
	}
	return &Router{mode: mode, api: api, local: local}
}

// Set replaces the mode and both providers atomically.
func (r *Router) Set(mode string, api, local Provider) {
	if mode == "" {
		mode = "auto"
	}
	r.mu.Lock()
	r.mode, r.api, r.local = mode, api, local
	r.mu.Unlock()
}

// SetMode switches api/local/auto/offline without touching providers.
func (r *Router) SetMode(mode string) {
	if mode == "" {
		mode = "auto"
	}
	r.mu.Lock()
	r.mode = mode
	r.mu.Unlock()
}

// SetAPI swaps the remote provider.
func (r *Router) SetAPI(p Provider) {
	r.mu.Lock()
	r.api = p
	r.mu.Unlock()
}

// SetLocal swaps the on-device provider.
func (r *Router) SetLocal(p Provider) {
	r.mu.Lock()
	r.local = p
	r.mu.Unlock()
}

// Mode returns the current routing mode.
func (r *Router) Mode() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.mode
}

// Providers returns the current provider pair.
func (r *Router) Providers() (Provider, Provider) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.api, r.local
}

func (r *Router) snapshot() (string, Provider, Provider) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.mode, r.api, r.local
}

func (r *Router) Name() string { return "router:" + r.Mode() }

// Available implements Provider so a Router can be injected anywhere a single
// provider is expected (the policy layer uses this).
func (r *Router) Available(ctx context.Context) bool {
	mode, api, local := r.snapshot()
	switch mode {
	case "offline":
		return false
	case "api":
		return api != nil && api.Available(ctx)
	case "local":
		return local != nil && local.Available(ctx)
	default:
		if local != nil && local.Available(ctx) {
			return true
		}
		return api != nil && api.Available(ctx)
	}
}

func (r *Router) Status(ctx context.Context) map[string]any {
	mode, api, local := r.snapshot()
	out := map[string]any{"mode": mode}
	if api != nil {
		out["api"] = api.Name()
		out["api_available"] = api.Available(ctx)
	}
	if local != nil {
		out["local"] = local.Name()
		out["local_available"] = local.Available(ctx)
	}
	return out
}

func (r *Router) Complete(ctx context.Context, req Request) (Response, error) {
	mode, api, local := r.snapshot()
	switch mode {
	case "offline":
		return Response{}, ErrNoProvider
	case "api":
		if api == nil {
			return Response{}, ErrNoProvider
		}
		return api.Complete(ctx, req)
	case "local":
		if local == nil {
			return Response{}, ErrNoProvider
		}
		return local.Complete(ctx, req)
	default: // auto: prefer local (privacy, no API key), fall back to API
		if local != nil && local.Available(ctx) {
			if resp, err := local.Complete(ctx, req); err == nil {
				return resp, nil
			}
		}
		if api != nil && api.Available(ctx) {
			return api.Complete(ctx, req)
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
