package localruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"goplay/internal/config"
	"goplay/internal/llm"
	"goplay/internal/localmodel"
)

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestOfflineRuntimeDoesNotStartServer(t *testing.T) {
	cfg := config.Default().Local
	cfg.ModelsDir = t.TempDir()
	rt := New(cfg, "offline", testLogger())
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	st := rt.Status()
	if st.Enabled || st.Server.Running {
		t.Fatalf("offline runtime started something: %+v", st)
	}
	if st.Mode != "offline" {
		t.Fatalf("mode=%q", st.Mode)
	}
	if rt.Provider() != nil {
		t.Fatal("provider should be nil in offline mode")
	}
	if len(st.Models) == 0 {
		t.Fatal("tier plan should still be visible in offline mode")
	}
}

func TestAutoModeFallsBackWhenModelOrRuntimeMissing(t *testing.T) {
	cfg := config.Default().Local
	cfg.ModelsDir = t.TempDir()
	cfg.AutoDownload = false
	cfg.RuntimePath = filepath.Join(t.TempDir(), "missing-llama-server.exe")
	rt := New(cfg, "auto", testLogger())
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("auto mode must not be fatal: %v", err)
	}
	st := rt.Status()
	if st.Enabled || st.Server.Running {
		t.Fatalf("auto mode unexpectedly started: %+v", st)
	}
	if st.Error == "" {
		t.Fatal("auto fallback should record why the local model is unavailable")
	}
	if rt.Provider() != nil {
		t.Fatal("provider must be nil after a failed auto start")
	}
}

func TestLocalModeIsFatalWhenUnavailable(t *testing.T) {
	cfg := config.Default().Local
	cfg.ModelsDir = t.TempDir()
	cfg.AutoDownload = false
	cfg.RuntimePath = filepath.Join(t.TempDir(), "missing-llama-server.exe")
	rt := New(cfg, "local", testLogger())
	if err := rt.Start(context.Background()); err == nil {
		t.Fatal("local mode should fail when the model is missing")
	}
}

func TestStatusModelPathsStayInsideModelsDir(t *testing.T) {
	cfg := config.Default().Local
	cfg.ModelsDir = t.TempDir()
	rt := New(cfg, "offline", testLogger())
	for _, m := range rt.Status().Models {
		rel, err := filepath.Rel(cfg.ModelsDir, m.Path)
		if err != nil || rel == "." || filepath.IsAbs(rel) || rel == ".." {
			t.Fatalf("model path %q escapes %q", m.Path, cfg.ModelsDir)
		}
		if _, err := os.Stat(m.Path); err == nil && !m.Present {
			t.Fatalf("existing model not marked present: %+v", m)
		}
	}
}
func TestStatusListsPlanAndCatalogModels(t *testing.T) {
	cfg := config.Default().Local
	cfg.ModelsDir = t.TempDir()
	rt := New(cfg, "offline", testLogger())
	st := rt.Status()
	cat := localmodel.DefaultCatalog()
	if len(st.Catalog) != len(cat.Models) {
		t.Fatalf("catalog models = %d, want %d", len(st.Catalog), len(cat.Models))
	}
	if len(st.Models) == 0 {
		t.Fatal("plan models should be listed")
	}
	planIDs := make(map[string]bool, len(st.Models))
	for _, m := range st.Models {
		planIDs[m.ID] = true
		if m.ID == "" || m.File == "" || m.Path == "" {
			t.Fatalf("incomplete model entry: %+v", m)
		}
	}
	catalogIDs := make(map[string]bool, len(st.Catalog))
	for _, m := range st.Catalog {
		if catalogIDs[m.ID] {
			t.Fatalf("duplicate catalog model %s", m.ID)
		}
		catalogIDs[m.ID] = true
	}
	for id := range planIDs {
		if !catalogIDs[id] {
			t.Fatalf("plan model %s missing from catalog", id)
		}
	}
	if st.Selected != "" {
		t.Fatalf("selected = %q, want empty initially", st.Selected)
	}
}

func TestDeleteModelGuards(t *testing.T) {
	cfg := config.Default().Local
	cfg.ModelsDir = t.TempDir()
	rt := New(cfg, "offline", testLogger())
	spec := localmodel.DefaultCatalog().Models[0]

	if err := rt.DeleteModel("not-a-model"); !errors.Is(err, ErrUnknownModel) {
		t.Fatalf("unknown delete err = %v, want ErrUnknownModel", err)
	}

	rt.mu.Lock()
	rt.servers[spec.ID] = &runningModel{modelID: spec.ID, srv: &localmodel.Server{}, port: 8421}
	rt.mu.Unlock()
	if err := rt.DeleteModel(spec.ID); !errors.Is(err, ErrModelInUse) {
		t.Fatalf("in-use delete err = %v, want ErrModelInUse", err)
	}
	rt.mu.Lock()
	delete(rt.servers, spec.ID)
	rt.mu.Unlock()

	// The file is absent; deletion is idempotent and must not touch other files.
	if err := rt.DeleteModel(spec.ID); err != nil {
		t.Fatalf("delete absent model: %v", err)
	}
	if st := rt.Status(); st.Download.Error != "" || st.Download.Active {
		t.Fatalf("download state should be reset after deletion: %+v", st.Download)
	}

	outside := filepath.Join(cfg.ModelsDir, "..", "evil.gguf")
	if pathInside(cfg.ModelsDir, outside) {
		t.Fatal("pathInside accepted traversal")
	}
	if pathInside(cfg.ModelsDir, cfg.ModelsDir) {
		t.Fatal("pathInside accepted the models dir itself")
	}
	if !pathInside(cfg.ModelsDir, filepath.Join(cfg.ModelsDir, "a.gguf")) {
		t.Fatal("pathInside rejected a direct child")
	}
}

func TestSelectModelRequiresDownloadedFile(t *testing.T) {
	cfg := config.Default().Local
	cfg.ModelsDir = t.TempDir()
	rt := New(cfg, "offline", testLogger())
	spec := localmodel.DefaultCatalog().Models[0]

	if err := rt.SelectModel(spec.ID); !errors.Is(err, ErrModelNotReady) {
		t.Fatalf("select missing model err = %v, want ErrModelNotReady", err)
	}
	if err := rt.DownloadModelID("not-a-model"); !errors.Is(err, ErrUnknownModel) {
		t.Fatalf("download unknown err = %v, want ErrUnknownModel", err)
	}
	if st := rt.Status(); st.Download.Active {
		t.Fatal("unknown model must not start a download")
	}
}

// injectRunning registers a fake running model without launching llama-server.
func injectRunning(rt *Runtime, spec localmodel.ModelSpec, port int, started time.Time) *llm.OpenAI {
	prov := llm.NewOpenAI(llm.OpenAIConfig{
		BaseURL: fmt.Sprintf("http://127.0.0.1:%d", port),
		Model:   spec.ID,
	})
	rt.mu.Lock()
	rt.servers[spec.ID] = &runningModel{
		modelID: spec.ID, srv: &localmodel.Server{}, provider: prov,
		port: port, ctxTokens: 4096, threads: 4, started: started,
	}
	rt.refreshStartedLocked()
	rt.refreshModelsLocked()
	rt.mu.Unlock()
	return prov
}

func TestPickServerPortSkipsUsedAndBusyPorts(t *testing.T) {
	always := func(int) bool { return true }
	got, err := pickServerPort(8421, nil, always)
	if err != nil || got != 8421 {
		t.Fatalf("first port = %d, %v; want 8421", got, err)
	}
	got, err = pickServerPort(8421, map[int]bool{8421: true, 8422: true}, always)
	if err != nil || got != 8423 {
		t.Fatalf("after two used ports = %d, %v; want 8423", got, err)
	}
	// 8421 is used by another model, 8422 is busy at the OS level.
	osBusy := func(port int) bool { return port != 8422 }
	got, err = pickServerPort(8421, map[int]bool{8421: true}, osBusy)
	if err != nil || got != 8423 {
		t.Fatalf("after skipping a busy port = %d, %v; want 8423", got, err)
	}
	if _, err := pickServerPort(8421, nil, func(int) bool { return false }); err == nil {
		t.Fatal("exhausted port search should fail")
	}
	got, err = pickServerPort(0, nil, always)
	if err != nil || got != 8421 {
		t.Fatalf("invalid start port = %d, %v; want 8421", got, err)
	}
}

func TestStartModelIDAlreadyRunningIsNoOp(t *testing.T) {
	cfg := config.Default().Local
	cfg.ModelsDir = t.TempDir()
	cfg.AutoDownload = false
	cfg.RuntimePath = filepath.Join(t.TempDir(), "missing-llama-server.exe")
	rt := New(cfg, "local", testLogger())

	if err := rt.StartModelID(context.Background(), "not-a-model", true); !errors.Is(err, ErrUnknownModel) {
		t.Fatalf("unknown start err = %v, want ErrUnknownModel", err)
	}

	spec := localmodel.DefaultCatalog().Models[0]
	injectRunning(rt, spec, 8421, time.Now())
	// Already running: no download, no port allocation, no llama-server.
	if err := rt.StartModelID(context.Background(), spec.ID, true); err != nil {
		t.Fatalf("already running start = %v, want nil", err)
	}
	if err := rt.StartModelID(context.Background(), strings.ToUpper(spec.ID), true); err != nil {
		t.Fatalf("case-insensitive start = %v, want nil", err)
	}
	if n := len(rt.Providers()); n != 1 {
		t.Fatalf("providers = %d, want 1", n)
	}
}

func TestProvidersAndStatusForMultipleRunningModels(t *testing.T) {
	cfg := config.Default().Local
	cfg.ModelsDir = t.TempDir()
	rt := New(cfg, "auto", testLogger())
	cat := localmodel.DefaultCatalog()
	if len(cat.Models) < 2 {
		t.Fatal("catalog needs two models for this test")
	}
	a, b := cat.Models[0], cat.Models[1]
	now := time.Now()
	pa := injectRunning(rt, a, 8421, now)
	injectRunning(rt, b, 8422, now)

	if n := len(rt.Providers()); n != 2 {
		t.Fatalf("providers = %d, want 2", n)
	}
	// The map is a copy: mutating it must not affect the runtime.
	provs := rt.Providers()
	delete(provs, a.ID)
	if len(rt.Providers()) != 2 {
		t.Fatal("Providers must return a copy")
	}

	rt.mu.Lock()
	rt.selected = b.ID
	rt.mu.Unlock()
	if got := rt.Provider(); got != rt.Providers()[b.ID] {
		t.Fatalf("default provider should be the selected running model, got %v", got)
	}

	st := rt.Status()
	if !st.Enabled || st.Providers != 2 {
		t.Fatalf("enabled=%v providers=%d, want true/2", st.Enabled, st.Providers)
	}
	if !st.Server.Running || st.Server.Model != b.ID || st.Server.Port != 8422 {
		t.Fatalf("legacy server field = %+v, want the selected model %s:8422", st.Server, b.ID)
	}
	if st.Provider != "llama.cpp" || st.Model != b.ID {
		t.Fatalf("provider/model = %q/%q, want llama.cpp/%s", st.Provider, st.Model, b.ID)
	}
	if len(st.Servers) != 2 || st.Servers[0].Model != b.ID {
		t.Fatalf("servers = %+v, want the default model first", st.Servers)
	}
	if len(st.RunningModels) != 2 || st.RunningModels[0] != b.ID {
		t.Fatalf("running models = %v, want default first", st.RunningModels)
	}
	if st.Servers[0].BaseURL != "http://127.0.0.1:8422/v1" {
		t.Fatalf("base url = %q", st.Servers[0].BaseURL)
	}
	if st.Server.PID != 0 {
		t.Fatalf("zero server should not report a pid: %d", st.Server.PID)
	}
	if !st.StartedAt.Equal(now) {
		t.Fatalf("started_at = %v, want %v", st.StartedAt, now)
	}

	ports := map[string]int{}
	for _, m := range st.Catalog {
		if m.Running {
			ports[m.ID] = m.Port
		}
	}
	if ports[a.ID] != 8421 || ports[b.ID] != 8422 {
		t.Fatalf("catalog running ports = %v, want %s:8421 %s:8422", ports, a.ID, b.ID)
	}
	// pa is a distinct provider from the default one.
	if pa == nil || rt.Providers()[a.ID] != llm.Provider(pa) {
		t.Fatal("providers map lost the first model")
	}
}

func TestStopModelKeepsOtherModelsRunning(t *testing.T) {
	cfg := config.Default().Local
	cfg.ModelsDir = t.TempDir()
	rt := New(cfg, "auto", testLogger())
	cat := localmodel.DefaultCatalog()
	a, b := cat.Models[0], cat.Models[1]
	injectRunning(rt, a, 8421, time.Now())
	injectRunning(rt, b, 8422, time.Now())

	if err := rt.StopModel(context.Background(), strings.ToUpper(a.ID)); err != nil {
		t.Fatalf("stop model: %v", err)
	}
	st := rt.Status()
	if st.Providers != 1 || len(st.Servers) != 1 || st.Servers[0].Model != b.ID {
		t.Fatalf("after stop: providers=%d servers=%+v", st.Providers, st.Servers)
	}
	if !strings.Contains(st.Note, "1 local model(s) still running") {
		t.Fatalf("note = %q, want remaining-model hint", st.Note)
	}
	if err := rt.StopModel(context.Background(), a.ID); err != nil {
		t.Fatalf("stopping a stopped model should be a no-op: %v", err)
	}
	if err := rt.StopModel(context.Background(), "not-a-model"); !errors.Is(err, ErrUnknownModel) {
		t.Fatalf("unknown stop err = %v, want ErrUnknownModel", err)
	}

	if err := rt.StopServer(context.Background()); err != nil {
		t.Fatalf("stop all: %v", err)
	}
	st = rt.Status()
	if st.Enabled || st.Providers != 0 || len(st.Servers) != 0 || len(st.RunningModels) != 0 || st.Server.Running {
		t.Fatalf("stop all left state: %+v", st)
	}
	if st.Note != "local model server stopped" {
		t.Fatalf("note = %q", st.Note)
	}
	if rt.Provider() != nil {
		t.Fatal("provider must be nil after stop all")
	}
}

func TestDeleteModelOnlyBlocksThatModel(t *testing.T) {
	cfg := config.Default().Local
	cfg.ModelsDir = t.TempDir()
	rt := New(cfg, "offline", testLogger())
	cat := localmodel.DefaultCatalog()
	a, b := cat.Models[0], cat.Models[1]
	injectRunning(rt, a, 8421, time.Now())

	if err := rt.DeleteModel(strings.ToUpper(b.ID)); err != nil {
		t.Fatalf("deleting an idle model while another runs: %v", err)
	}
	if err := rt.DeleteModel(a.ID); !errors.Is(err, ErrModelInUse) {
		t.Fatalf("running model delete err = %v, want ErrModelInUse", err)
	}
	for _, m := range rt.Status().Catalog {
		if m.ID == b.ID && m.Present {
			t.Fatalf("deleted model still marked present: %+v", m)
		}
	}
}
