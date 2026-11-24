package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goplay/internal/app"
	"goplay/internal/config"
	"goplay/internal/llm"
	"goplay/internal/localruntime"
)

func newLocalTestServer(t *testing.T) (*httptest.Server, *app.Manager, config.Config) {
	t.Helper()
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.Local.ModelsDir = t.TempDir()
	cfg.LLM.Mode = "offline"
	cfg.MaxInstances = 4
	cfg.TickMS = 20
	cfg.HumanResumeMS = 80
	cfg.MemoryBudgetKB = 256
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := llm.NewRouter("offline", nil, nil)
	mgr, err := app.New(cfg, nil, router, log)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	rt := localruntime.New(cfg.Local, "offline", log)
	mgr.AttachLocal(rt)
	t.Cleanup(func() {
		_ = mgr.Close(t.Context())
	})
	ts := httptest.NewServer(New(mgr, cfg).Handler())
	t.Cleanup(ts.Close)
	return ts, mgr, cfg
}

func TestAIRoutesEndpoints(t *testing.T) {
	ts, mgr := newTestServer(t)

	var got map[string]any
	if code := doJSON(t, http.MethodGet, ts.URL+"/api/ai/routes", nil, &got); code != http.StatusOK {
		t.Fatalf("GET /api/ai/routes = %d", code)
	}
	if got["global_mode"] != "offline" {
		t.Fatalf("global_mode = %v", got["global_mode"])
	}
	available, ok := got["available"].(map[string]any)
	if !ok || len(available) < len(llm.Roles()) {
		t.Fatalf("available = %+v", got["available"])
	}
	for _, role := range llm.Roles() {
		entry, ok := available[role].(map[string]any)
		if !ok {
			t.Fatalf("missing role %s in available", role)
		}
		if _, ok := entry["available"].(bool); !ok {
			t.Fatalf("%s.available is not bool: %+v", role, entry)
		}
	}

	code := doJSON(t, http.MethodPost, ts.URL+"/api/ai/routes", map[string]any{
		"routes": map[string]any{
			"perception": map[string]any{"mode": "api", "model": "gpt-4o-mini"},
			"memory":     map[string]any{"mode": "offline"},
		},
	}, &got)
	if code != http.StatusOK {
		t.Fatalf("POST /api/ai/routes = %d: %v", code, got)
	}
	routes, _ := got["routes"].(map[string]any)
	perception, _ := routes["perception"].(map[string]any)
	if perception["mode"] != "api" || perception["model"] != "gpt-4o-mini" {
		t.Fatalf("perception route not stored: %+v", routes)
	}
	if mgr.Router().Routes()["perception"].Model != "gpt-4o-mini" {
		t.Fatal("router did not receive the saved route")
	}

	if code := doJSON(t, http.MethodPost, ts.URL+"/api/ai/routes", map[string]any{
		"routes": map[string]any{"bogus": map[string]any{"mode": "api"}},
	}, nil); code != http.StatusBadRequest {
		t.Fatalf("unknown role status = %d, want 400", code)
	}
	if code := doJSON(t, http.MethodPost, ts.URL+"/api/ai/routes", map[string]any{
		"routes": map[string]any{"language": map[string]any{"mode": "sometimes"}},
	}, nil); code != http.StatusBadRequest {
		t.Fatalf("bad mode status = %d, want 400", code)
	}

	var llmBody map[string]any
	if code := doJSON(t, http.MethodGet, ts.URL+"/api/llm", nil, &llmBody); code != http.StatusOK {
		t.Fatalf("GET /api/llm = %d", code)
	}
	if _, ok := llmBody["routes"].(map[string]any); !ok {
		t.Fatalf("GET /api/llm misses routes status: %+v", llmBody)
	}
}

func TestLLMUpdatePersistsRoutes(t *testing.T) {
	ts, _, cfg := newLocalTestServer(t)
	var out map[string]any
	code := doJSON(t, http.MethodPost, ts.URL+"/api/llm", map[string]any{
		"routes": map[string]any{
			"analysis": map[string]any{"mode": "api", "model": "gpt-4o-mini"},
		},
	}, &out)
	if code != http.StatusOK {
		t.Fatalf("POST /api/llm = %d: %v", code, out)
	}
	b, err := os.ReadFile(filepath.Join(cfg.DataDir, "config.json"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var saved config.Config
	if err := json.Unmarshal(b, &saved); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	rc, ok := saved.LLM.Routes["analysis"]
	if !ok || rc.Mode != "api" || rc.Model != "gpt-4o-mini" {
		t.Fatalf("saved routes = %+v", saved.LLM.Routes)
	}
	if code := doJSON(t, http.MethodPost, ts.URL+"/api/llm", map[string]any{
		"routes": map[string]any{"analysis": map[string]any{"mode": "nope"}},
	}, nil); code != http.StatusBadRequest {
		t.Fatalf("invalid route mode status = %d, want 400", code)
	}

	// Updating the API model without resending routes must keep the overrides.
	if code := doJSON(t, http.MethodPost, ts.URL+"/api/llm", map[string]any{
		"model": "gpt-4o",
	}, nil); code != http.StatusOK {
		t.Fatalf("POST /api/llm model = %d", code)
	}
	saved = readConfig(t, cfg)
	if rc := saved.LLM.Routes["analysis"]; rc.Mode != "api" || rc.Model != "gpt-4o-mini" {
		t.Fatalf("routes lost after model update: %+v", saved.LLM.Routes)
	}

	// Saving role routes must not revert settings changed through /api/llm.
	if code := doJSON(t, http.MethodPost, ts.URL+"/api/ai/routes", map[string]any{
		"routes": map[string]any{"language": map[string]any{"mode": "auto"}},
	}, nil); code != http.StatusOK {
		t.Fatalf("POST /api/ai/routes = %d", code)
	}
	saved = readConfig(t, cfg)
	if saved.LLM.Model != "gpt-4o" {
		t.Fatalf("ai routes save reverted model: %q", saved.LLM.Model)
	}
}

func TestChatReturnsLanguageFallback(t *testing.T) {
	ts, _ := newTestServer(t)
	createDemo(t, ts)

	var out struct {
		Accepted bool   `json:"accepted"`
		Reply    string `json:"reply"`
		Provider string `json:"provider"`
		Model    string `json:"model"`
		Fallback bool   `json:"fallback"`
	}
	code := doJSON(t, http.MethodPost, ts.URL+"/api/instances/demo/chat", map[string]any{
		"text": "向左走，小心陷阱",
	}, &out)
	if code != http.StatusOK {
		t.Fatalf("POST chat = %d", code)
	}
	if !out.Accepted || !out.Fallback || strings.TrimSpace(out.Reply) == "" {
		t.Fatalf("fallback chat = %+v", out)
	}

	var chat map[string]any
	if code := doJSON(t, http.MethodGet, ts.URL+"/api/instances/demo/chat", nil, &chat); code != http.StatusOK {
		t.Fatalf("GET chat = %d", code)
	}
	msgs, _ := chat["chat"].([]any)
	found := false
	for _, raw := range msgs {
		m, _ := raw.(map[string]any)
		if m["role"] == "assistant" && m["text"] == out.Reply {
			id, _ := m["id"].(string)
			if strings.HasSuffix(id, "-r") {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("assistant reply not stored under <msg>-r: %+v", msgs)
	}

	if code := doJSON(t, http.MethodPost, ts.URL+"/api/instances/demo/chat", map[string]any{
		"text": "   ",
	}, nil); code != http.StatusBadRequest {
		t.Fatalf("empty text status = %d, want 400", code)
	}
	if code := doJSON(t, http.MethodPost, ts.URL+"/api/instances/missing/chat", map[string]any{
		"text": "hi",
	}, nil); code != http.StatusNotFound {
		t.Fatalf("missing instance status = %d, want 404", code)
	}
}

func TestLocalModelEndpoints(t *testing.T) {
	ts, _, _ := newLocalTestServer(t)

	var cat struct {
		Tier     string `json:"tier"`
		Selected string `json:"selected"`
		Models   []any  `json:"models"`
		Catalog  []any  `json:"catalog"`
	}
	if code := doJSON(t, http.MethodGet, ts.URL+"/api/local/catalog", nil, &cat); code != http.StatusOK {
		t.Fatalf("GET catalog = %d", code)
	}
	if len(cat.Models) == 0 || len(cat.Catalog) == 0 || cat.Selected != "" {
		t.Fatalf("catalog = %+v", cat)
	}

	if code := doJSON(t, http.MethodPost, ts.URL+"/api/local/download", map[string]any{
		"model_id": "not-a-model",
	}, nil); code != http.StatusNotFound {
		t.Fatalf("download unknown = %d, want 404", code)
	}
	if code := doJSON(t, http.MethodPost, ts.URL+"/api/local/select", map[string]any{
		"model_id": "not-a-model",
	}, nil); code != http.StatusNotFound {
		t.Fatalf("select unknown = %d, want 404", code)
	}
	if code := doJSON(t, http.MethodDelete, ts.URL+"/api/local/models/not-a-model", nil, nil); code != http.StatusNotFound {
		t.Fatalf("delete unknown = %d, want 404", code)
	}

	var status map[string]any
	if code := doJSON(t, http.MethodGet, ts.URL+"/api/local/status", nil, &status); code != http.StatusOK {
		t.Fatalf("GET local status = %d", code)
	}
	for _, key := range []string{"selected", "catalog", "models"} {
		if _, ok := status[key]; !ok {
			t.Fatalf("local status misses %q: %+v", key, status)
		}
	}
}

func readConfig(t *testing.T, cfg config.Config) config.Config {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(cfg.DataDir, "config.json"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var saved config.Config
	if err := json.Unmarshal(b, &saved); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	return saved
}
