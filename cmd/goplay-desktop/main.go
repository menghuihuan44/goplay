// Command goplay-desktop is the lightweight cross-platform desktop shell for
// GoPlay. It reuses the embedded dashboard in a native WebView2/WebKit/Cocoa
// window and keeps the daemon in the same process.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	webview "github.com/webview/webview_go"

	"goplay/internal/config"
	"goplay/internal/service"
	"goplay/internal/version"
)

type health struct {
	OK      bool   `json:"ok"`
	Service string `json:"service"`
}

func main() {
	var (
		addr    = flag.String("addr", "", "listen address (default from config)")
		data    = flag.String("data", "", "data directory (default %LOCALAPPDATA%\\GoPlay)")
		cfgPath = flag.String("config", "", "config file path")
		mode    = flag.String("llm", "", "LLM mode: auto, api, local, offline")
		browser = flag.Bool("browser", false, "use the system browser instead of the native WebView")
		verbose = flag.Bool("v", false, "debug logging")
		width   = flag.Int("width", 1180, "window width")
		height  = flag.Int("height", 780, "window height")
		showVer = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()
	if *showVer {
		fmt.Println("goplay-desktop " + version.String())
		return
	}

	preview, _ := previewConfig(*cfgPath, *addr, *data, *mode)
	log := newLogger(preview.DataDir, *verbose)
	opts := service.Options{
		ConfigPath: *cfgPath, Addr: *addr, DataDir: *data,
		LLMMode: *mode, Verbose: *verbose, Logger: log,
	}
	if *browser {
		if err := runBrowser(opts, preview.Addr, log); err != nil {
			log.Error("desktop", "err", err)
			os.Exit(1)
		}
		return
	}
	if err := runGUI(opts, preview.Addr, *width, *height, log); err != nil {
		log.Error("desktop", "err", err)
		os.Exit(1)
	}
}

func previewConfig(cfgPath, addr, data, mode string) (config.Config, error) {
	p := cfgPath
	if p == "" && data != "" {
		p = filepath.Join(data, "config.json")
	}
	cfg, err := config.Load(p)
	if err != nil {
		cfg = config.Default()
	}
	if addr != "" {
		cfg.Addr = addr
	}
	if data != "" {
		cfg.DataDir = data
	}
	if mode != "" {
		cfg.LLM.Mode = mode
	}
	return cfg, err
}

func newLogger(dataDir string, verbose bool) *slog.Logger {
	level := slog.LevelInfo
	if verbose {
		level = slog.LevelDebug
	}
	w := io.Writer(os.Stderr)
	if dataDir != "" {
		if err := os.MkdirAll(dataDir, 0o755); err == nil {
			if f, err := os.OpenFile(filepath.Join(dataDir, "desktop.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
				w = io.MultiWriter(os.Stderr, f)
			}
		}
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: level}))
}

func runGUI(opts service.Options, previewAddr string, width, height int, log *slog.Logger) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := webview.New(false)
	if w == nil {
		return errors.New("could not create a native WebView (is WebView2 installed?)")
	}
	defer w.Destroy()
	w.SetTitle("GoPlay " + version.Version)
	w.SetSize(clamp(width, 900, 3840), clamp(height, 600, 2160), webview.HintNone)
	w.SetSize(880, 580, webview.HintMin)

	var (
		svcMu   sync.Mutex
		svc     *service.Service
		uiAlive = true
		uiMu    sync.Mutex
	)
	dispatch := func(f func()) {
		uiMu.Lock()
		alive := uiAlive
		uiMu.Unlock()
		if alive {
			w.Dispatch(f)
		}
	}
	_ = w.Bind("goplayDesktop", func(action string) (string, error) {
		switch action {
		case "version":
			return version.String(), nil
		case "data":
			dir := opts.DataDir
			if dir == "" {
				dir = config.DefaultDataDir()
			}
			return "", openPath(dir)
		case "overlay":
			baseURL := "http://" + previewAddr
			svcMu.Lock()
			if svc != nil {
				baseURL = strings.TrimSuffix(svc.URL(), "/")
			}
			svcMu.Unlock()
			if err := launchNativeOverlay(baseURL); err != nil {
				// goplay-overlay.exe is shipped with the Windows bundle. Fall
				// back to the web overlay if the shell runs from a bare build.
				return "", openBrowser(baseURL + "/overlay")
			}
			return "ok", nil
		case "quit":
			dispatch(func() { w.Terminate() })
			return "ok", nil
		default:
			return "", fmt.Errorf("unknown desktop action %q", action)
		}
	})

	w.SetHtml(splashHTML)
	startDone := make(chan struct{})
	go func() {
		defer close(startDone)
		target := "http://" + previewAddr + "/"
		if exists, _ := probeServer(target); exists {
			dispatch(func() { w.Navigate(target) })
			return
		}
		s, err := startCore(ctx, opts, log)
		if err != nil {
			dispatch(func() { w.SetHtml(errorHTML(err.Error())) })
			return
		}
		svcMu.Lock()
		svc = s
		svcMu.Unlock()
		go func() {
			if err := s.Serve(ctx); err != nil && ctx.Err() == nil {
				log.Error("serve", "err", err)
				dispatch(func() { w.SetHtml(errorHTML(err.Error())) })
			}
		}()
		if !waitServer(s.URL(), 25*time.Second) {
			dispatch(func() { w.SetHtml(errorHTML("本地面板启动超时，请查看数据目录中的 desktop.log。")) })
			return
		}
		dispatch(func() { w.Navigate(s.URL()) })
	}()

	// The initial native window is created before Windows associates it with a
	// monitor, so webview_go cannot scale the requested size for the real DPI.
	// Re-apply it once the window is on screen; with raw-pixel bounds this gives
	// the dashboard its intended CSS viewport on 125%/150%/200% displays.
	go func() {
		time.Sleep(700 * time.Millisecond)
		dispatch(func() {
			w.SetSize(clamp(width, 900, 3840), clamp(height, 600, 2160), webview.HintNone)
			centerOnScreen(w.Window())
		})
	}()

	w.Run()
	uiMu.Lock()
	uiAlive = false
	uiMu.Unlock()
	cancel()
	select {
	case <-startDone:
	case <-time.After(10 * time.Second):
		log.Warn("startup did not finish before window close")
	}
	svcMu.Lock()
	s := svc
	svcMu.Unlock()
	if s != nil {
		stop, stopCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer stopCancel()
		if err := s.Close(stop); err != nil {
			log.Error("shutdown", "err", err)
		}
	}
	return nil
}

func startCore(ctx context.Context, opts service.Options, log *slog.Logger) (*service.Service, error) {
	s, err := service.Start(ctx, opts)
	if err != nil && opts.Addr == "" && strings.Contains(err.Error(), "listen on") {
		log.Warn("preferred port unavailable; using a free local port", "err", err)
		opts.Addr = "127.0.0.1:0"
		s, err = service.Start(ctx, opts)
	}
	if err != nil {
		return nil, err
	}
	return s, nil
}

func runBrowser(opts service.Options, previewAddr string, log *slog.Logger) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	target := "http://" + previewAddr + "/"
	if exists, _ := probeServer(target); exists {
		log.Info("opening existing GoPlay dashboard", "url", target)
		if err := openBrowser(target); err != nil {
			return err
		}
		<-ctx.Done()
		return nil
	}
	s, err := startCore(ctx, opts, log)
	if err != nil {
		return err
	}
	defer func() {
		stop, stopCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer stopCancel()
		_ = s.Close(stop)
	}()
	go func() {
		if err := s.Serve(ctx); err != nil && ctx.Err() == nil {
			log.Error("serve", "err", err)
		}
	}()
	if !waitServer(s.URL(), 25*time.Second) {
		return errors.New("dashboard did not become ready")
	}
	log.Info("opening GoPlay dashboard", "url", s.URL())
	if err := openBrowser(s.URL()); err != nil {
		return err
	}
	<-ctx.Done()
	return nil
}

func probeServer(base string) (bool, error) {
	client := &http.Client{Timeout: 700 * time.Millisecond}
	resp, err := client.Get(strings.TrimRight(base, "/") + "/api/health")
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	var h health
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<10)).Decode(&h); err != nil {
		return false, err
	}
	return h.OK && h.Service == "goplay", nil
}

func waitServer(base string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ok, _ := probeServer(base); ok {
			return true
		}
		time.Sleep(120 * time.Millisecond)
	}
	return false
}

// launchNativeOverlay starts the borderless always-on-top Windows overlay
// shipped next to GoPlay.exe. It returns an error on other platforms or when
// the helper is absent so the caller can fall back to the browser.
func launchNativeOverlay(baseURL string) error {
	if runtime.GOOS != "windows" {
		return fmt.Errorf("native overlay is Windows-only")
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	helper := filepath.Join(filepath.Dir(exe), "goplay-overlay.exe")
	if _, err := os.Stat(helper); err != nil {
		return err
	}
	cmd := exec.Command(helper, "--base-url", baseURL)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

func openBrowser(url string) error {
	switch runtime.GOOS {
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		return exec.Command("open", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}

func openPath(path string) error {
	if path == "" {
		path = "."
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		return err
	}
	switch runtime.GOOS {
	case "windows":
		return exec.Command("explorer.exe", path).Start()
	case "darwin":
		return exec.Command("open", path).Start()
	default:
		return exec.Command("xdg-open", path).Start()
	}
}

func errorHTML(message string) string {
	return `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><title>GoPlay</title>
<style>body{margin:0;min-height:100vh;display:grid;place-items:center;background:#070a0c;color:#e9f7df;font-family:ui-sans-serif,system-ui,sans-serif}.box{width:min(620px,88vw)}h1{font-size:28px;margin:0 0 12px}p{color:#ff8f8f;line-height:1.7;white-space:pre-wrap;background:#ff5a5a12;border:1px solid #ff5a5a33;border-radius:14px;padding:18px}button{margin-top:18px;background:#b8ff5a;color:#0a1006;border:0;border-radius:10px;padding:12px 20px;font-weight:700;cursor:pointer}</style></head>
<body><div class="box"><h1>GoPlay 启动失败</h1><p>` + html.EscapeString(message) + `</p><button onclick="window.goplayDesktop('quit')">退出</button></div></body></html>`
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
