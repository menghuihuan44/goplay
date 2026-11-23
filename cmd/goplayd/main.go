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
	"syscall"
	"time"

	"goplay/internal/service"
	"goplay/internal/version"
)

func main() {
	var (
		addr    = flag.String("addr", "", "listen address (default from config: 127.0.0.1:8420)")
		data    = flag.String("data", "", "data directory (default %LOCALAPPDATA%\\GoPlay)")
		cfgPath = flag.String("config", "", "config file path (default <data>/config.json)")
		mode    = flag.String("llm", "", "LLM mode: auto, api, local, offline")
		verbose = flag.Bool("v", false, "debug logging")
		showVer = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()
	if *showVer {
		fmt.Println("goplayd " + version.String())
		return
	}

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	svc, err := service.Start(ctx, service.Options{
		ConfigPath: *cfgPath,
		Addr:       *addr,
		DataDir:    *data,
		LLMMode:    *mode,
		Verbose:    *verbose,
		Logger:     log,
	})
	if err != nil {
		fatal("start", err)
	}
	defer func() {
		stop, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := svc.Close(stop); err != nil {
			log.Error("shutdown", "err", err)
		}
	}()

	log.Info("goplayd listening",
		"addr", svc.Addr(), "dashboard", svc.URL(), "island", svc.URL()+"overlay",
		"version", version.String())
	if err := svc.Serve(ctx); err != nil {
		fatal("serve", err)
	}
	log.Info("goplayd stopped")
}

func fatal(what string, err error) {
	fmt.Fprintf(os.Stderr, "goplayd: %s: %v\n", what, err)
	os.Exit(1)
}
