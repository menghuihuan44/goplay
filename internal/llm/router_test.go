package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeProvider struct {
	name      string
	model     string
	available bool
}

func (f *fakeProvider) Name() string                   { return f.name }
func (f *fakeProvider) Available(context.Context) bool { return f.available }
func (f *fakeProvider) Model() string                  { return f.model }
func (f *fakeProvider) WithModel(model string) Provider {
	cp := *f
	cp.model = model
	return &cp
}
func (f *fakeProvider) Complete(context.Context, Request) (Response, error) {
	if !f.available {
		return Response{}, errors.New("fake provider unavailable")
	}
	return Response{Text: "ok", Provider: f.name, Model: f.model}, nil
}

func TestRouteModelOverrideReachesOpenAIRequest(t *testing.T) {
	var gotModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotModel = body.Model
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"model":"`+body.Model+`","choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer srv.Close()

	api := NewOpenAI(OpenAIConfig{BaseURL: srv.URL, APIKey: "test-key", Model: "base-model"})
	router := NewRouter("offline", api, nil)
	router.SetRoutes(map[string]RouteConfig{
		RolePerception: {Mode: "api", Model: "gpt-4o-mini"},
	})
	p := router.For(RolePerception)
	if !p.Available(context.Background()) {
		t.Fatalf("route should be available: %s", p.Name())
	}
	resp, err := p.Complete(context.Background(), Request{Prompt: "hello"})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if gotModel != "gpt-4o-mini" {
		t.Fatalf("request model = %q, want gpt-4o-mini", gotModel)
	}
	if resp.Model != "gpt-4o-mini" {
		t.Fatalf("response model = %q", resp.Model)
	}
}

func TestLocalRouteModelMismatchIsUnavailable(t *testing.T) {
	local := &fakeProvider{name: "local", model: "qwen-a", available: true}
	router := NewRouter("local", nil, local)
	router.SetRoutes(map[string]RouteConfig{
		RoleStrategy: {Mode: "local", Model: "gemma-b"},
	})
	p := router.For(RoleStrategy)
	if p.Available(context.Background()) {
		t.Fatal("a route naming a model the server is not running must be unavailable")
	}
	if _, err := p.Complete(context.Background(), Request{Prompt: "hi"}); !errors.Is(err, ErrNoProvider) {
		t.Fatalf("err = %v, want ErrNoProvider", err)
	}
	st := router.RoutesStatus(context.Background())[RoleStrategy].(map[string]any)
	if st["available"].(bool) {
		t.Fatalf("status says available: %+v", st)
	}
	if note, _ := st["note"].(string); !strings.Contains(note, "gemma-b") {
		t.Fatalf("note should explain the mismatch: %+v", st)
	}
}

func TestRouteModeFallsBackAndInherits(t *testing.T) {
	api := &fakeProvider{name: "api", model: "gpt-4o", available: true}
	local := &fakeProvider{name: "local", model: "qwen", available: false}

	router := NewRouter("api", api, local)
	router.SetRoutes(map[string]RouteConfig{
		RoleStrategy: {Mode: "auto"},
	})
	p := router.For(RoleStrategy)
	if !p.Available(context.Background()) {
		t.Fatal("auto should fall back to the API when local is unavailable")
	}
	resp, err := p.Complete(context.Background(), Request{Prompt: "hi"})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if resp.Provider != "api" {
		t.Fatalf("provider = %q, want api", resp.Provider)
	}

	// Empty mode inherits the global mode.
	r2 := NewRouter("offline", api, local)
	if r2.For(RoleLanguage).Available(context.Background()) {
		t.Fatal("empty route must inherit the offline global mode")
	}
}

func TestRoutesStatusMarksUnwiredLayers(t *testing.T) {
	router := NewRouter("offline", nil, nil)
	st := router.RoutesStatus(context.Background())
	for _, role := range Roles() {
		entry, ok := st[role].(map[string]any)
		if !ok {
			t.Fatalf("missing role %s in status: %+v", role, st)
		}
		if role == RoleAnalysis || role == RoleMemory {
			note, _ := entry["note"].(string)
			if !strings.Contains(note, "规则层暂未调用") {
				t.Fatalf("%s note = %q", role, note)
			}
		}
	}
}
