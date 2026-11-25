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
		if role == RoleMemory || role == RoleValidation {
			note, _ := entry["note"].(string)
			if !strings.Contains(note, "尚未接入") {
				t.Fatalf("%s note = %q", role, note)
			}
			if implemented, _ := entry["implemented"].(bool); implemented {
				t.Fatalf("%s must be marked as not wired: %+v", role, entry)
			}
		}
	}
}

func TestLocalModelsRegistryIsCopiedAndClean(t *testing.T) {
	router := NewRouter("local", nil, nil)
	a := &fakeProvider{name: "local-a", model: "qwen-a", available: true}
	b := &fakeProvider{name: "local-b", model: "gemma-b", available: true}

	router.SetLocalModels(map[string]Provider{
		" qwen-a ": a,
		"gemma-b":  b,
		"":         b,
		"gone":     nil,
	})
	models := router.LocalModels()
	if len(models) != 2 {
		t.Fatalf("local models = %d (%v), want 2", len(models), models)
	}
	if models["qwen-a"] != Provider(a) || models["gemma-b"] != Provider(b) {
		t.Fatalf("local models lost entries: %v", models)
	}

	// LocalModels returns a copy.
	delete(models, "qwen-a")
	models["gemma-b"] = a
	if got := router.LocalModels(); len(got) != 2 || got["qwen-a"] != Provider(a) {
		t.Fatalf("LocalModels must return a copy, got %v", got)
	}

	router.SetLocalModels(nil)
	if got := router.LocalModels(); len(got) != 0 {
		t.Fatalf("cleared local models = %v", got)
	}
}

func TestLocalRouteSelectsModelFromLocals(t *testing.T) {
	base := &fakeProvider{name: "default", model: "qwen-default", available: true}
	a := &fakeProvider{name: "local-qwen", model: "qwen-a", available: true}
	b := &fakeProvider{name: "local-gemma", model: "gemma-b", available: true}
	router := NewRouter("local", nil, base)
	router.SetLocalModels(map[string]Provider{"qwen-a": a, "gemma-b": b})
	router.SetRoutes(map[string]RouteConfig{
		RoleStrategy:   {Mode: "local", Model: "gemma-b"},
		RolePerception: {Mode: "local", Model: "QWEN-A"},
		RoleLanguage:   {Mode: "local"},
		RoleAnalysis:   {Mode: "local", Model: "llama-c"},
	})
	ctx := context.Background()

	if resp, err := router.For(RoleStrategy).Complete(ctx, Request{Prompt: "hi"}); err != nil || resp.Provider != "local-gemma" || resp.Model != "gemma-b" {
		t.Fatalf("strategy route = %+v, %v; want local-gemma/gemma-b", resp, err)
	}
	// Model ids match case-insensitively.
	if resp, err := router.For(RolePerception).Complete(ctx, Request{Prompt: "hi"}); err != nil || resp.Provider != "local-qwen" {
		t.Fatalf("perception route = %+v, %v; want local-qwen", resp, err)
	}
	// A route without a model keeps using the default local provider.
	if resp, err := router.For(RoleLanguage).Complete(ctx, Request{Prompt: "hi"}); err != nil || resp.Provider != "default" {
		t.Fatalf("language route = %+v, %v; want the default local provider", resp, err)
	}
	// A model that is not registered stays unavailable; the default local
	// model is not silently substituted in strict local mode.
	missing := router.For(RoleAnalysis)
	if missing.Available(ctx) {
		t.Fatal("a route naming an unregistered local model must be unavailable")
	}
	if _, err := missing.Complete(ctx, Request{Prompt: "hi"}); !errors.Is(err, ErrNoProvider) {
		t.Fatalf("err = %v, want ErrNoProvider", err)
	}

	st := router.RoutesStatus(ctx)
	strategy := st[RoleStrategy].(map[string]any)
	if strategy["provider"] != "local-gemma" || strategy["model"] != "gemma-b" || !strategy["available"].(bool) {
		t.Fatalf("strategy status = %+v", strategy)
	}
	perception := st[RolePerception].(map[string]any)
	if perception["provider"] != "local-qwen" || perception["model"] != "qwen-a" {
		t.Fatalf("perception status = %+v", perception)
	}
	analysis := st[RoleAnalysis].(map[string]any)
	if analysis["available"].(bool) {
		t.Fatalf("analysis status says available: %+v", analysis)
	}
	if note, _ := analysis["note"].(string); !strings.Contains(note, "llama-c") {
		t.Fatalf("analysis note = %q, want the missing model named", note)
	}
}

func TestLocalRouteFallsBackToDefaultLocalModel(t *testing.T) {
	def := &fakeProvider{name: "default-local", model: "qwen-a", available: true}
	other := &fakeProvider{name: "other-local", model: "gemma-b", available: true}
	router := NewRouter("local", nil, def)
	// The map lists another model, so legacy single-provider model checks must
	// still apply for the requested model.
	router.SetLocalModels(map[string]Provider{"gemma-b": other})
	router.SetRoutes(map[string]RouteConfig{RoleStrategy: {Mode: "local", Model: "QWEN-A"}})
	ctx := context.Background()

	if resp, err := router.For(RoleStrategy).Complete(ctx, Request{Prompt: "hi"}); err != nil || resp.Provider != "default-local" {
		t.Fatalf("legacy local model match = %+v, %v", resp, err)
	}
	router.SetRoutes(map[string]RouteConfig{RoleAnalysis: {Mode: "local", Model: "llama-c"}})
	if router.For(RoleAnalysis).Available(ctx) {
		t.Fatal("unknown model must stay unavailable in local mode")
	}
	analysis := router.RoutesStatus(ctx)[RoleAnalysis].(map[string]any)
	if note, _ := analysis["note"].(string); !strings.Contains(note, "llama-c") {
		t.Fatalf("analysis note = %q", note)
	}
}

func TestAutoRouteUsesNamedLocalThenDefaultThenAPI(t *testing.T) {
	api := &fakeProvider{name: "api", model: "gpt-4o", available: true}
	def := &fakeProvider{name: "default-local", model: "qwen-default", available: true}
	named := &fakeProvider{name: "local-gemma", model: "gemma-b", available: false}
	router := NewRouter("auto", api, def)
	router.SetLocalModels(map[string]Provider{"gemma-b": named})
	router.SetRoutes(map[string]RouteConfig{RoleStrategy: {Mode: "auto", Model: "gemma-b"}})
	ctx := context.Background()

	// The named model is registered but not available: auto uses the default
	// local model instead of jumping to the API.
	p := router.For(RoleStrategy)
	if resp, err := p.Complete(ctx, Request{Prompt: "hi"}); err != nil || resp.Provider != "default-local" {
		t.Fatalf("auto fallback = %+v, %v; want default-local", resp, err)
	}
	st := router.RoutesStatus(ctx)[RoleStrategy].(map[string]any)
	if st["provider"] != "default-local" || st["model"] != "qwen-default" || !st["available"].(bool) {
		t.Fatalf("auto fallback status = %+v", st)
	}

	// Once the named model is running it wins over the default local model.
	named.available = true
	if resp, err := p.Complete(ctx, Request{Prompt: "hi"}); err != nil || resp.Provider != "local-gemma" || resp.Model != "gemma-b" {
		t.Fatalf("auto named model = %+v, %v", resp, err)
	}
	st = router.RoutesStatus(ctx)[RoleStrategy].(map[string]any)
	if st["provider"] != "local-gemma" || st["model"] != "gemma-b" {
		t.Fatalf("auto named status = %+v", st)
	}

	// A model nobody runs degrades to the default local model with a note.
	router.SetRoutes(map[string]RouteConfig{RoleAnalysis: {Mode: "auto", Model: "llama-c"}})
	pa := router.For(RoleAnalysis)
	if resp, err := pa.Complete(ctx, Request{Prompt: "hi"}); err != nil || resp.Provider != "default-local" {
		t.Fatalf("auto unknown model = %+v, %v", resp, err)
	}
	sa := router.RoutesStatus(ctx)[RoleAnalysis].(map[string]any)
	note, _ := sa["note"].(string)
	if !strings.Contains(note, "llama-c") || !strings.Contains(note, "auto is using the default local model") {
		t.Fatalf("auto unknown model note = %q", note)
	}

	// With no local model available, auto lands on the API.
	def.available = false
	if resp, err := pa.Complete(ctx, Request{Prompt: "hi"}); err != nil || resp.Provider != "api" {
		t.Fatalf("auto api fallback = %+v, %v", resp, err)
	}
	sa = router.RoutesStatus(ctx)[RoleAnalysis].(map[string]any)
	if sa["provider"] != "api" || !sa["available"].(bool) {
		t.Fatalf("auto api status = %+v", sa)
	}
}
