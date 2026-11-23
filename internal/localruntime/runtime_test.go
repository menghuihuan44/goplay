package localruntime

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"goplay/internal/config"
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
