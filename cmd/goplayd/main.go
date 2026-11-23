// Command goplayd runs the GoPlay daemon: instance manager, REST/WebSocket API,
// embedded dashboard and floating-island client.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"goplay/internal/api"
	"goplay/internal/app"
	"goplay/internal/config"
	"goplay/internal/llm"
	"goplay/internal/localruntime"
	"goplay/internal/metrics"
)

func main() {
	var (
		addr    = flag.String("addr", "", "listen address (default from config: 127.0.0.1:8420)")
		data    = flag.String("data", "", "data directory (default %LOCALAPPDATA%\\GoPlay)")
		cfgPath = flag.String("config", "", "config file path (default <data>/config.json)")
		mode    = flag.String("llm", "", "LLM mode: auto, api, local, offline")
		verbose = flag.Bool("v", false, "debug logging")
	)
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fatal("load config", err)
	}
	if *addr != "" {
		cfg.Addr = *addr
	}
	if *data != "" {
		cfg.DataDir = *data
	}
	if *mode != "" {
		cfg.LLM.Mode = *mode
	}
	if err := validate(&cfg); err != nil {
		fatal("invalid config", err)
	}
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		fatal("create data dir", err)
	}

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	local := localruntime.New(cfg.Local, cfg.LLM.Mode, log)
	if err := local.Start(ctx); err != nil {
		if cfg.LLM.Mode == "local" {
			fatal("start local model runtime", err)
		}
		log.Warn("local model runtime unavailable", "err", err)
	}
	defer func() {
		c, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if err := local.Close(c); err != nil {
			log.Warn("stop local model runtime", "err", err)
		}
	}()

	apiProvider := llm.NewOpenAI(llm.OpenAIConfig{
		BaseURL:     cfg.LLM.BaseURL,
		APIKey:      cfg.LLM.APIKey,
		Model:       cfg.LLM.Model,
		Timeout:     time.Duration(cfg.LLM.TimeoutMS) * time.Millisecond,
		Temperature: cfg.LLM.Temperature,
		MaxTokens:   cfg.LLM.MaxTokens,
		Provider:    "openai-compatible",
	})
	router := llm.NewRouter(cfg.LLM.Mode, apiProvider, local.Provider())

	reg := metrics.New()
	mgr, err := app.New(cfg, reg, router, log)
	if err != nil {
		fatal("start manager", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := mgr.Close(ctx); err != nil {
			log.Error("shutdown", "err", err)
		}
	}()

	mgr.AttachLocal(local)
	srv := api.New(mgr, cfg)

	log.Info("goplayd listening",
		"addr", cfg.Addr, "data", cfg.DataDir, "llm", cfg.LLM.Mode,
		"instances", len(mgr.List()), "dashboard", "http://"+cfg.Addr+"/",
		"island", "http://"+cfg.Addr+"/overlay")
	if cfg.LLM.APIKey == "" && (cfg.LLM.Mode == "api") {
		log.Warn("LLM mode is api but no API key is configured; decisions will fall back to heuristics")
	}
	if err := srv.Serve(ctx); err != nil {
		fatal("serve", err)
	}
	log.Info("goplayd stopped")
}

func validate(cfg *config.Config) error {
	s := strings.TrimSpace(cfg.Addr)
	if s == "" {
		return fmt.Errorf("empty listen address")
	}
	switch cfg.LLM.Mode {
	case "auto", "api", "local", "offline":
	default:
		return fmt.Errorf("unknown llm mode %q (want auto, api, local or offline)", cfg.LLM.Mode)
	}
	return nil
}

func fatal(what string, err error) {
	fmt.Fprintf(os.Stderr, "goplayd: %s: %v\n", what, err)
	os.Exit(1)
}
