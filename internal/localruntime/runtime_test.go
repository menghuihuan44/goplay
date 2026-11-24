package localruntime

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"goplay/internal/config"
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
	rt.srv = &localmodel.Server{}
	rt.modelID = spec.ID
	rt.mu.Unlock()
	if err := rt.DeleteModel(spec.ID); !errors.Is(err, ErrModelInUse) {
		t.Fatalf("in-use delete err = %v, want ErrModelInUse", err)
	}
	rt.mu.Lock()
	rt.srv = nil
	rt.modelID = ""
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
