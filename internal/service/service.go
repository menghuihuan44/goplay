// Package service assembles the daemon once so the CLI and the desktop GUI
// share the exact same startup, configuration and shutdown path.
package service

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"goplay/internal/api"
	"goplay/internal/app"
	"goplay/internal/config"
	"goplay/internal/llm"
	"goplay/internal/localruntime"
	"goplay/internal/metrics"
	"goplay/internal/version"
)

// Options are command-line overrides applied on top of config.Load.
type Options struct {
	ConfigPath string
	Addr       string
	DataDir    string
	LLMMode    string
	Verbose    bool
	Logger     *slog.Logger
}

// Service owns the listener, model runtime, instance manager and HTTP API.
type Service struct {
	cfg   config.Config
	log   *slog.Logger
	local *localruntime.Runtime
	mgr   *app.Manager
	srv   *api.Server
	ln    net.Listener
}

// Start loads configuration, binds the listener and starts the optional local
// model runtime. It does not serve HTTP yet; call Serve for that.
func Start(ctx context.Context, opts Options) (*Service, error) {
	cfgPath := opts.ConfigPath
	if cfgPath == "" && opts.DataDir != "" {
		// A -data override must not silently load config.json from the default
		// data directory. Load the override directory's file when it exists.
		cfgPath = filepath.Join(opts.DataDir, "config.json")
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	if opts.Addr != "" {
		cfg.Addr = opts.Addr
	}
	if opts.DataDir != "" {
		cfg.DataDir = opts.DataDir
		if cfg.Local.ModelsDir == "" {
			cfg.Local.ModelsDir = filepath.Join(cfg.DataDir, "models")
		}
	}
	if opts.LLMMode != "" {
		cfg.LLM.Mode = opts.LLMMode
	}
	if err := validate(cfg); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	cfg.Version = version.Version

	log := opts.Logger
	if log == nil {
		level := slog.LevelInfo
		if opts.Verbose {
			level = slog.LevelDebug
		}
		log = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	}

	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", cfg.Addr, err)
	}
	// Publish the real address (useful for :0 in the desktop app).
	cfg.Addr = ln.Addr().String()
	if cfg.OverlayURL == "" || strings.Contains(cfg.OverlayURL, "127.0.0.1:8420") {
		cfg.OverlayURL = "http://" + cfg.Addr + "/overlay"
	}

	s := &Service{cfg: cfg, log: log, ln: ln}

	local := localruntime.New(cfg.Local, cfg.LLM.Mode, log)
	if err := local.Start(ctx); err != nil {
		if cfg.LLM.Mode == "local" {
			_ = ln.Close()
			return nil, fmt.Errorf("start local model runtime: %w", err)
		}
		log.Warn("local model runtime unavailable", "err", err)
	}
	s.local = local

	apiProvider := llm.NewOpenAI(llm.OpenAIConfig{
		BaseURL:     cfg.LLM.BaseURL,
		APIKey:      cfg.LLM.APIKey,
		Model:       cfg.LLM.Model,
		Timeout:     time.Duration(cfg.LLM.TimeoutMS) * time.Millisecond,
		Temperature: cfg.LLM.Temperature,
		MaxTokens:   cfg.LLM.MaxTokens,
		Provider:    "openai-compatible",
		Vision:      cfg.LLM.Vision,
	})
	router := llm.NewRouter(cfg.LLM.Mode, apiProvider, local.Provider())

	reg := metrics.New()
	mgr, err := app.New(cfg, reg, router, log)
	if err != nil {
		_ = ln.Close()
		closeLocal(local, log)
		return nil, fmt.Errorf("start manager: %w", err)
	}
	mgr.AttachLocal(local)
	s.mgr = mgr
	s.srv = api.New(mgr, cfg)

	if cfg.LLM.APIKey == "" && cfg.LLM.Mode == "api" {
		log.Warn("LLM mode is api but no API key is configured; decisions will fall back to heuristics")
	}
	log.Info("goplay service ready",
		"addr", cfg.Addr, "data", cfg.DataDir, "llm", cfg.LLM.Mode,
		"instances", len(mgr.List()))
	return s, nil
}

// Serve blocks until ctx is cancelled or the listener fails.
func (s *Service) Serve(ctx context.Context) error {
	if s.ln == nil {
		return fmt.Errorf("service: already served or closed")
	}
	ln := s.ln
	s.ln = nil
	return s.srv.ServeListener(ctx, ln)
}

// URL is the dashboard URL. It is valid after Start.
func (s *Service) URL() string { return "http://" + s.cfg.Addr + "/" }

// Addr is the actual bound address.
func (s *Service) Addr() string { return s.cfg.Addr }

// Config returns a copy of the effective configuration.
func (s *Service) Config() config.Config { return s.cfg }

// Manager exposes the instance manager for tests and desktop-only actions.
func (s *Service) Manager() *app.Manager { return s.mgr }

// Close stops the API listener, game runners, memory persistence and the local
// model server. It is safe to call after Serve has returned.
func (s *Service) Close(ctx context.Context) error {
	if s.ln != nil {
		_ = s.ln.Close()
		s.ln = nil
	}
	var first error
	if s.mgr != nil {
		if err := s.mgr.Close(ctx); err != nil {
			first = err
		}
		s.mgr = nil
	}
	if s.local != nil {
		if err := s.local.Close(ctx); err != nil && first == nil {
			first = err
		}
		s.local = nil
	}
	return first
}

func closeLocal(local *localruntime.Runtime, log *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := local.Close(ctx); err != nil {
		log.Warn("stop local model runtime", "err", err)
	}
}

func validate(cfg config.Config) error {
	if strings.TrimSpace(cfg.Addr) == "" {
		return fmt.Errorf("empty listen address")
	}
	switch cfg.LLM.Mode {
	case "auto", "api", "local", "offline":
	default:
		return fmt.Errorf("unknown llm mode %q (want auto, api, local or offline)", cfg.LLM.Mode)
	}
	return nil
}
