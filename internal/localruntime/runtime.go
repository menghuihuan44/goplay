// Package localruntime wires the local-model catalog, downloader and llama.cpp
// server into a provider the LLM router can use. Models and the runtime binary
// are never bundled: they are detected on the device, downloaded on first use
// and served over loopback.
package localruntime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"goplay/internal/config"
	"goplay/internal/llm"
	"goplay/internal/localmodel"
)

// ModelState is one model file from the recommended tier plan.
type ModelState struct {
	ID        string  `json:"id"`
	Role      string  `json:"role,omitempty"`
	File      string  `json:"file"`
	Path      string  `json:"path"`
	URL       string  `json:"url"`
	Present   bool    `json:"present"`
	SizeBytes int64   `json:"size_bytes"`
	SizeGB    float64 `json:"size_gb"`
	License   string  `json:"license"`
}

// ServerStatus describes the llama-server child process.
type ServerStatus struct {
	Running       bool   `json:"running"`
	BaseURL       string `json:"base_url,omitempty"`
	Port          int    `json:"port,omitempty"`
	PID           int    `json:"pid,omitempty"`
	Model         string `json:"model,omitempty"`
	ContextTokens int    `json:"context_tokens,omitempty"`
	Threads       int    `json:"threads,omitempty"`
	Error         string `json:"error,omitempty"`
}

// DownloadStatus is the live state of the on-device model download. It is
// separate from ServerStatus so the UI can show progress before llama-server
// exists.
type DownloadStatus struct {
	Active         bool    `json:"active"`
	Done           bool    `json:"done"`
	ModelID        string  `json:"model_id,omitempty"`
	Downloaded     int64   `json:"downloaded"`
	Total          int64   `json:"total"`
	Percent        float64 `json:"percent"`
	BytesPerSecond float64 `json:"bytes_per_second"`
	Error          string  `json:"error,omitempty"`
}

// Status is the JSON view exposed at /api/local/status.
type Status struct {
	Mode           string                     `json:"mode"`
	Enabled        bool                       `json:"enabled"`
	Hardware       localmodel.HardwareInfo    `json:"hardware"`
	Tier           string                     `json:"tier,omitempty"`
	Recommendation *localmodel.Recommendation `json:"recommendation,omitempty"`
	Plan           *localmodel.TierPlan       `json:"plan,omitempty"`
	Models         []ModelState               `json:"models"`
	RuntimePath    string                     `json:"runtime_path,omitempty"`
	RuntimeFound   bool                       `json:"runtime_found"`
	Server         ServerStatus               `json:"server"`
	Provider       string                     `json:"provider,omitempty"`
	Model          string                     `json:"model,omitempty"`
	Error          string                     `json:"error,omitempty"`
	Note           string                     `json:"note"`
	StartedAt      time.Time                  `json:"started_at,omitempty"`
	Download       DownloadStatus             `json:"download"`
}

// Runtime owns the optional local model server. It is safe for concurrent
// Status calls from HTTP handlers.
type Runtime struct {
	cfg     config.LocalConfig
	mode    string
	log     *slog.Logger
	catalog localmodel.Catalog

	mu       sync.Mutex
	startMu  sync.Mutex // serializes StartModel so two UI taps cannot launch two servers
	hw       localmodel.HardwareInfo
	rec      *localmodel.Recommendation
	plan     *localmodel.TierPlan
	models   []ModelState
	runtime  string
	srv      *localmodel.Server
	provider *llm.OpenAI
	modelID  string
	err      error
	note     string
	started  time.Time

	download DownloadStatus
	dlCancel context.CancelFunc
}

// New detects hardware and resolves the recommended tier. Detection failures are
// recorded rather than fatal so the daemon still starts in API mode.
func New(cfg config.LocalConfig, mode string, log *slog.Logger) *Runtime {
	if log == nil {
		log = slog.Default()
	}
	if strings.TrimSpace(mode) == "" {
		mode = "auto"
	}
	r := &Runtime{cfg: cfg, mode: mode, log: log, catalog: localmodel.DefaultCatalog()}
	hw, err := localmodel.Detect()
	if err != nil {
		r.note = "hardware detection failed: " + err.Error()
		r.log.Warn("local model hardware detection failed", "err", err)
		return r
	}
	r.hw = hw
	rec, err := localmodel.Recommend(hw, cfg.PreferredTier)
	if err != nil {
		r.note = "tier recommendation failed: " + err.Error()
		return r
	}
	r.rec = &rec
	if plan, ok := r.catalog.Plan(rec.Tier); ok {
		r.plan = &plan
	}
	r.refreshModelsLocked()
	return r
}

func (r *Runtime) refreshModelsLocked() {
	if r.plan == nil {
		return
	}
	roles := map[string]string{}
	for _, a := range r.plan.Roles {
		if a.ModelID != "" {
			roles[a.ModelID] = string(a.Role)
		}
	}
	out := make([]ModelState, 0, len(r.plan.Models))
	for _, m := range r.plan.Models {
		path := filepath.Join(r.cfg.ModelsDir, m.File)
		st, err := os.Stat(path)
		out = append(out, ModelState{
			ID: m.ID, Role: roles[m.ID], File: m.File, Path: path,
			URL: r.catalog.URL(m), Present: err == nil && st.Size() == m.SizeBytes,
			SizeBytes: m.SizeBytes, SizeGB: m.SizeGB(), License: m.License,
		})
	}
	r.models = out
}

// StrategyModel returns the model assigned to the strategy role, if the tier has
// one. The runtime only serves this one; perception stays rules-first.
func (r *Runtime) strategyModel() (localmodel.ModelSpec, bool) {
	if r.plan == nil {
		return localmodel.ModelSpec{}, false
	}
	a, ok := r.plan.Role(localmodel.RoleStrategy)
	if !ok || a.ModelID == "" {
		return localmodel.ModelSpec{}, false
	}
	return r.catalog.Model(a.ModelID)
}

// SetMode changes the routing mode without touching a running server.
func (r *Runtime) SetMode(mode string) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = "auto"
	}
	r.mu.Lock()
	r.mode = mode
	r.mu.Unlock()
}

// Mode returns the current routing mode.
func (r *Runtime) Mode() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.mode
}

// Start downloads the strategy model when permitted and launches llama-server.
// In "auto" mode every failure is non-fatal and recorded; in "local" mode a
// failure is returned so the caller can refuse to start with no working AI.
func (r *Runtime) Start(ctx context.Context) error { return r.start(ctx, false) }

// StartModel is the user-initiated variant: it downloads the strategy model
// even when local.auto_download is false, then starts llama-server. The UI uses
// this after the user explicitly taps "enable local AI".
func (r *Runtime) StartModel(ctx context.Context) error { return r.start(ctx, true) }

func (r *Runtime) start(ctx context.Context, force bool) error {
	r.startMu.Lock()
	defer r.startMu.Unlock()
	r.mu.Lock()
	if r.provider != nil || r.srv != nil {
		r.mu.Unlock()
		return nil
	}
	mode := strings.ToLower(strings.TrimSpace(r.mode))
	if !force && (mode == "api" || mode == "offline") {
		r.note = "local runtime is idle because llm mode is " + mode
		r.mu.Unlock()
		return nil
	}
	if r.rec == nil || r.plan == nil {
		err := fmt.Errorf("localruntime: no tier plan: %s", r.note)
		r.mu.Unlock()
		return r.fail(err, mode)
	}
	r.mu.Unlock()

	strategy, ok := r.strategyModel()
	if !ok {
		err := fmt.Errorf("localruntime: tier %s has no strategy model", r.rec.Tier)
		return r.fail(err, mode)
	}
	modelPath := filepath.Join(r.cfg.ModelsDir, strategy.File)
	needDownload := false
	if st, err := os.Stat(modelPath); err != nil || st.Size() != strategy.SizeBytes {
		needDownload = true
	}
	if needDownload {
		if !force && !r.cfg.AutoDownload {
			err := fmt.Errorf("localruntime: model %s is not downloaded (set local.auto_download=true or run goplay-model download %s)", strategy.ID, strategy.ID)
			r.mu.Lock()
			r.note = "strategy model missing; first-run download is disabled"
			r.err = err
			r.mu.Unlock()
			if mode == "auto" {
				r.log.Warn("local model unavailable; falling back to API", "err", err)
				return nil
			}
			return err
		}
		if err := os.MkdirAll(r.cfg.ModelsDir, 0o755); err != nil {
			return r.fail(fmt.Errorf("create models dir: %w", err), mode)
		}
		d := localmodel.NewDownloader(r.cfg.ModelsDir, r.catalog.Endpoint)
		d.Progress = func(p localmodel.Progress) {
			if p.Done {
				r.log.Info("local model download complete", "model", p.ModelID, "bytes", p.Downloaded, "seconds", p.ElapsedMS/1000)
			} else if p.Percent >= 10 {
				r.log.Info("local model download", "model", p.ModelID, "percent", int(p.Percent))
			}
		}
		r.log.Info("downloading local model", "model", strategy.ID, "size_gb", strategy.SizeGB(), "url", r.catalog.URL(strategy))
		res, err := d.Download(ctx, strategy)
		if err != nil {
			return r.fail(fmt.Errorf("download %s: %w", strategy.ID, err), mode)
		}
		modelPath = res.Path
		r.log.Info("local model ready", "path", modelPath, "cached", res.Cached)
	}

	runtimePath, err := localmodel.ResolveRuntime(r.cfg.RuntimePath)
	if err != nil {
		return r.fail(fmt.Errorf("%w (set local.runtime_path or GOPLAY_LLAMA_SERVER)", err), mode)
	}

	ctxTokens := r.cfg.ContextTokens
	if r.plan.ContextTokens > ctxTokens {
		ctxTokens = r.plan.ContextTokens
	}
	if ctxTokens < 512 {
		ctxTokens = 2048
	}
	threads := r.cfg.Threads
	if threads <= 0 {
		threads = r.plan.Threads
	}
	port := r.cfg.ServerPort
	if port <= 0 {
		port = 8421
	}
	srv, err := localmodel.StartServer(ctx, localmodel.ServerConfig{
		RuntimePath: runtimePath, ModelPath: modelPath, Host: "127.0.0.1", Port: port,
		ContextTokens: ctxTokens, Threads: threads, StartupTimeout: 180 * time.Second,
	})
	if err != nil {
		return r.fail(fmt.Errorf("start llama-server: %w", err), mode)
	}
	provider := llm.NewOpenAI(llm.OpenAIConfig{
		BaseURL: srv.BaseURL(), Model: strategy.ID, Timeout: 90 * time.Second,
		Temperature: 0.2, MaxTokens: 384, Provider: "llama.cpp",
	})

	r.mu.Lock()
	r.srv = srv
	r.provider = provider
	r.modelID = strategy.ID
	r.runtime = runtimePath
	r.err = nil
	r.note = "local model server is running; models are downloaded per device tier and never bundled"
	r.started = time.Now()
	r.refreshModelsLocked()
	r.mu.Unlock()
	r.log.Info("local model server ready", "base_url", srv.BaseURL(), "model", strategy.ID, "pid", srv.PID(), "tier", r.rec.Tier)
	return nil
}

// DownloadModel starts the strategy-model download in the background. Progress
// is visible through Status(). It is idempotent when the file is already present.
func (r *Runtime) DownloadModel() error {
	r.mu.Lock()
	if r.dlCancel != nil || r.download.Active {
		r.mu.Unlock()
		return errors.New("local model download already running")
	}
	if r.plan == nil {
		r.mu.Unlock()
		return fmt.Errorf("localruntime: no tier plan: %s", r.note)
	}
	strategy, ok := r.strategyModel()
	if !ok {
		r.mu.Unlock()
		return fmt.Errorf("localruntime: tier %s has no strategy model", r.rec.Tier)
	}
	modelPath := filepath.Join(r.cfg.ModelsDir, strategy.File)
	if st, err := os.Stat(modelPath); err == nil && st.Size() == strategy.SizeBytes {
		r.download = DownloadStatus{Done: true, ModelID: strategy.ID, Downloaded: strategy.SizeBytes, Total: strategy.SizeBytes, Percent: 100}
		r.note = "model already downloaded"
		r.refreshModelsLocked()
		r.mu.Unlock()
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.dlCancel = cancel
	r.download = DownloadStatus{Active: true, ModelID: strategy.ID, Total: strategy.SizeBytes}
	r.err = nil
	r.note = "downloading " + strategy.ID
	r.mu.Unlock()
	go r.downloadModel(ctx, strategy)
	return nil
}

func (r *Runtime) downloadModel(ctx context.Context, strategy localmodel.ModelSpec) {
	defer func() {
		r.mu.Lock()
		r.dlCancel = nil
		r.download.Active = false
		r.download.BytesPerSecond = 0
		r.mu.Unlock()
	}()
	if err := os.MkdirAll(r.cfg.ModelsDir, 0o755); err != nil {
		r.setDownloadError(fmt.Errorf("create models dir: %w", err))
		return
	}
	d := localmodel.NewDownloader(r.cfg.ModelsDir, r.catalog.Endpoint)
	d.Progress = func(p localmodel.Progress) {
		r.mu.Lock()
		r.download.ModelID = p.ModelID
		r.download.Downloaded = p.Downloaded
		r.download.Total = p.Total
		r.download.Percent = p.Percent
		r.download.BytesPerSecond = p.BytesPerSecond
		if p.Done {
			r.download.Percent = 100
			r.download.Done = true
		}
		r.mu.Unlock()
	}
	r.log.Info("downloading local model", "model", strategy.ID, "size_gb", strategy.SizeGB())
	res, err := d.Download(ctx, strategy)
	if err != nil {
		r.setDownloadError(fmt.Errorf("download %s: %w", strategy.ID, err))
		return
	}
	r.mu.Lock()
	r.download.Active = false
	r.download.Done = true
	r.download.Percent = 100
	r.download.Downloaded = strategy.SizeBytes
	r.download.Total = strategy.SizeBytes
	r.note = "model downloaded; tap start to launch the local AI"
	r.err = nil
	r.refreshModelsLocked()
	r.mu.Unlock()
	r.log.Info("local model ready", "path", res.Path, "cached", res.Cached)
}

func (r *Runtime) setDownloadError(err error) {
	r.mu.Lock()
	r.download.Active = false
	r.download.Error = err.Error()
	r.download.BytesPerSecond = 0
	r.err = err
	r.note = err.Error()
	r.mu.Unlock()
	r.log.Warn("local model download failed", "err", err)
}

// CancelDownload stops an in-flight download. The partial file is kept for
// resuming on the next attempt.
func (r *Runtime) CancelDownload() {
	r.mu.Lock()
	cancel := r.dlCancel
	if cancel != nil {
		cancel()
		r.note = "download cancelled"
	}
	r.mu.Unlock()
}

// StopServer stops the llama-server child and detaches its provider. It is safe
// when nothing is running.
func (r *Runtime) StopServer(ctx context.Context) error {
	r.mu.Lock()
	srv := r.srv
	r.srv = nil
	r.provider = nil
	r.modelID = ""
	r.started = time.Time{}
	if srv != nil {
		r.note = "local model server stopped"
	}
	r.mu.Unlock()
	if srv == nil {
		return nil
	}
	return srv.Stop(ctx)
}

func (r *Runtime) fail(err error, mode string) error {
	r.mu.Lock()
	r.err = err
	r.note = err.Error()
	r.mu.Unlock()
	r.log.Warn("local model runtime unavailable", "mode", mode, "err", err)
	if mode == "auto" {
		return nil
	}
	return err
}

// Provider returns the llama.cpp provider, or nil when the local runtime is not
// running.
func (r *Runtime) Provider() llm.Provider {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.provider == nil {
		return nil
	}
	return r.provider
}

// Status returns a consistent snapshot for the HTTP API.
func (r *Runtime) Status() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	st := Status{
		Mode: r.mode, Enabled: r.provider != nil, Hardware: r.hw,
		Models:      append([]ModelState(nil), r.models...),
		RuntimePath: r.runtime, RuntimeFound: r.runtime != "",
		Error: errString(r.err), Note: r.note, StartedAt: r.started,
		Download: r.download,
	}
	if r.rec != nil {
		st.Tier = string(r.rec.Tier)
		rec := *r.rec
		st.Recommendation = &rec
	}
	if r.plan != nil {
		plan := *r.plan
		st.Plan = &plan
	}
	if r.provider != nil {
		st.Provider = "llama.cpp"
		st.Model = r.modelID
	}
	if r.srv != nil {
		st.Server = ServerStatus{
			Running: true, BaseURL: r.srv.BaseURL(), Port: r.cfg.ServerPort, PID: r.srv.PID(),
			Model: filepath.Base(r.srv.ModelPath()), ContextTokens: r.planValue(func() int { return r.plan.ContextTokens }),
		}
	}
	return st
}

func (r *Runtime) planValue(f func() int) int {
	if r.plan == nil {
		return 0
	}
	return f()
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// Close stops the child process. It is safe to call when the runtime never
// started.
func (r *Runtime) Close(ctx context.Context) error {
	return r.StopServer(ctx)
}