package config

import (
	"path/filepath"
	"testing"
)

// TestExampleConfigLoads keeps the documented example parseable and in sync
// with the loader. A stale example is the first thing a new user hits.
func TestExampleConfigLoads(t *testing.T) {
	t.Setenv("GOPLAY_ADDR", "")
	t.Setenv("GOPLAY_DATA", "")
	t.Setenv("GOPLAY_LLM_MODE", "")
	t.Setenv("GOPLAY_API_BASE", "")
	t.Setenv("GOPLAY_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("GOPLAY_MODEL", "")
	t.Setenv("GOPLAY_THREADS", "")

	cfg, err := Load(filepath.Join("..", "..", "config.example.json"))
	if err != nil {
		t.Fatalf("load example config: %v", err)
	}
	if cfg.Addr != "127.0.0.1:8420" || cfg.TickMS != 250 || cfg.HumanResumeMS != 2500 {
		t.Fatalf("unexpected loop defaults: %+v", cfg)
	}
	if cfg.LLM.Mode != "auto" || cfg.LLM.Model != "gpt-4o-mini" {
		t.Fatalf("unexpected llm defaults: %+v", cfg.LLM)
	}
	if cfg.Local.ServerPort != 8421 || cfg.Local.PreferredTier != "auto" {
		t.Fatalf("unexpected local defaults: %+v", cfg.Local)
	}
	if cfg.Local.ModelsDir == "" {
		t.Fatal("models dir should fall back under data dir")
	}
}
