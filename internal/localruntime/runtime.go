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
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
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
	Role      string  `json:"role"`
	File      string  `json:"file"`
	Path      string  `json:"path"`
	URL       string  `json:"url"`
	Present   bool    `json:"present"`
	Running   bool    `json:"running"`
	Port      int     `json:"port,omitempty"`
	Size      string  `json:"size"`
	SizeBytes int64   `json:"size_bytes"`
	SizeGB    float64 `json:"size_gb"`
	License   string  `json:"license"`
}

// ServerStatus describes one llama-server child process.
type ServerStatus struct {
	Running       bool   `json:"running"`
	BaseURL       string `json:"base_url,omitempty"`
	Port          int    `json:"port,omitempty"`
	PID           int    `json:"pid,omitempty"`
	Model         string `json:"model,omitempty"`
	ContextTokens int    `json:"context_tokens,omitempty"`
	Threads       int    `json:"threads,omitempty"`
	Error         string `json:"error,omitempty"`
	// LogTail is the tail of llama-server stdout/stderr. It makes a hung or
	// misconfigured local runtime diagnosable from the UI instead of a bare
	// "context deadline exceeded".
	LogTail string `json:"log_tail,omitempty"`
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
//
// Server/Provider/Model mirror the default running model for older UI code;
// Servers, RunningModels and Providers describe every model running at once.
type Status struct {
	Mode           string                     `json:"mode"`
	Enabled        bool                       `json:"enabled"`
	Hardware       localmodel.HardwareInfo    `json:"hardware"`
	Tier           string                     `json:"tier,omitempty"`
	Recommendation *localmodel.Recommendation `json:"recommendation,omitempty"`
	Plan           *localmodel.TierPlan       `json:"plan,omitempty"`
	Models         []ModelState               `json:"models"`
	Catalog        []ModelState               `json:"catalog"`
	Selected       string                     `json:"selected"`
	RuntimePath    string                     `json:"runtime_path,omitempty"`
	RuntimeFound   bool                       `json:"runtime_found"`
	Server         ServerStatus               `json:"server"`
	Servers        []ServerStatus             `json:"servers"`
	Provider       string                     `json:"provider,omitempty"`
	Providers      int                        `json:"providers"`
	Model          string                     `json:"model,omitempty"`
	RunningModels  []string                   `json:"running_models"`
	Error          string                     `json:"error,omitempty"`
	Note           string                     `json:"note"`
	StartedAt      time.Time                  `json:"started_at,omitempty"`
	Download       DownloadStatus             `json:"download"`
}

// runningModel is one llama-server process and the provider bound to it.
type runningModel struct {
	modelID   string
	srv       *localmodel.Server
	provider  *llm.OpenAI
	port      int
	ctxTokens int
	threads   int
	started   time.Time
}

// Runtime owns the optional local model servers. Several models may run at the
// same time, each on its own loopback port. It is safe for concurrent Status
// calls from HTTP handlers.
type Runtime struct {
	cfg     config.LocalConfig
	mode    string
	log     *slog.Logger
	catalog localmodel.Catalog

	mu            sync.Mutex
	startMu       sync.Mutex // serializes model start/stop so two UI taps cannot launch the same server twice
	hw            localmodel.HardwareInfo
	rec           *localmodel.Recommendation
	plan          *localmodel.TierPlan
	models        []ModelState
	catalogModels []ModelState
	selected      string
	runtime       string
	servers       map[string]*runningModel
	err           error
	note          string
	started       time.Time

	download DownloadStatus
	dlCancel context.CancelFunc
	dlDone   chan struct{}
}

// Sentinel errors let the HTTP layer map model operations to useful status
// codes without parsing messages.
var (
	ErrUnknownModel    = errors.New("unknown model id")
	ErrModelNotReady   = errors.New("model is not fully downloaded")
	ErrModelInUse      = errors.New("model is in use by the running local server")
	ErrModelPathUnsafe = errors.New("model path escapes the models directory")
	ErrDownloadRunning = errors.New("local model download already running")
)

// New detects hardware and resolves the recommended tier. Detection failures are
// recorded rather than fatal so the daemon still starts in API mode.
func New(cfg config.LocalConfig, mode string, log *slog.Logger) *Runtime {
	if log == nil {
		log = slog.Default()
	}
	if strings.TrimSpace(mode) == "" {
		mode = "auto"
	}
	r := &Runtime{
		cfg: cfg, mode: mode, log: log, catalog: localmodel.DefaultCatalog(),
		servers: make(map[string]*runningModel),
	}
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
	// Role comes from the plan first and from any tier assignment as fallback,
	// so optional catalog models still show which pipeline stage they serve.
	roles := map[string]string{}
	for _, ts := range r.catalog.Tiers {
		for _, a := range ts.Roles {
			if a.ModelID != "" && roles[a.ModelID] == "" {
				roles[a.ModelID] = string(a.Role)
			}
		}
	}
	for _, a := range r.plan.Roles {
		if a.ModelID != "" {
			roles[a.ModelID] = string(a.Role)
		}
	}

	planned := make([]localmodel.ModelSpec, 0, len(r.plan.Models)+len(r.plan.OptionalModels))
	seen := map[string]bool{}
	for _, m := range r.plan.Models {
		if !seen[m.ID] {
			seen[m.ID] = true
			planned = append(planned, m)
		}
	}
	for _, m := range r.plan.OptionalModels {
		if !seen[m.ID] {
			seen[m.ID] = true
			planned = append(planned, m)
		}
	}
	out := make([]ModelState, 0, len(planned))
	for _, m := range planned {
		out = append(out, r.modelStateLocked(m, roles[m.ID]))
	}
	r.models = out

	all := make([]ModelState, 0, len(r.catalog.Models))
	for _, m := range r.catalog.Models {
		all = append(all, r.modelStateLocked(m, roles[m.ID]))
	}
	r.catalogModels = all
}

func (r *Runtime) modelStateLocked(m localmodel.ModelSpec, role string) ModelState {
	path := filepath.Join(r.cfg.ModelsDir, m.File)
	present := false
	if st, err := os.Stat(path); err == nil && st.Size() == m.SizeBytes {
		present = true
	}
	state := ModelState{
		ID: m.ID, Role: role, File: m.File, Path: path,
		URL: r.catalog.URL(m), Present: present,
		Size:      localmodel.HumanBytes(m.SizeBytes),
		SizeBytes: m.SizeBytes, SizeGB: m.SizeGB(), License: m.License,
	}
	if rm := r.runningLocked(m.ID); rm != nil {
		state.Running = true
		state.Port = rm.port
	}
	return state
}

// catalogModel resolves one catalog model by id, case-insensitively so model
// ids coming from route configs keep working.
func (r *Runtime) catalogModel(id string) (localmodel.ModelSpec, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return localmodel.ModelSpec{}, false
	}
	if spec, ok := r.catalog.Model(id); ok {
		return spec, true
	}
	for _, m := range r.catalog.Models {
		if strings.EqualFold(m.ID, id) {
			return m, true
		}
	}
	return localmodel.ModelSpec{}, false
}

func (r *Runtime) modelPresentLocked(spec localmodel.ModelSpec) bool {
	st, err := os.Stat(filepath.Join(r.cfg.ModelsDir, spec.File))
	return err == nil && st.Size() == spec.SizeBytes
}

// runningLocked returns the running server for a model id, matched
// case-insensitively. The caller must hold r.mu.
func (r *Runtime) runningLocked(id string) *runningModel {
	id = strings.TrimSpace(id)
	if id == "" || len(r.servers) == 0 {
		return nil
	}
	if rm := r.servers[id]; rm != nil {
		return rm
	}
	for key, rm := range r.servers {
		if rm != nil && strings.EqualFold(key, id) {
			return rm
		}
	}
	return nil
}

// defaultRunningLocked returns the model exposed through the legacy
// single-provider API: the selected model when it runs, else the tier strategy
// model, else the first model id in sorted order.
func (r *Runtime) defaultRunningLocked() *runningModel {
	if len(r.servers) == 0 {
		return nil
	}
	if rm := r.runningLocked(r.selected); rm != nil {
		return rm
	}
	if spec, ok := r.strategyModel(); ok {
		if rm := r.runningLocked(spec.ID); rm != nil {
			return rm
		}
	}
	ids := make([]string, 0, len(r.servers))
	for id, rm := range r.servers {
		if rm != nil {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	sort.Strings(ids)
	return r.servers[ids[0]]
}

// refreshStartedLocked re-derives StartedAt from the running models.
func (r *Runtime) refreshStartedLocked() {
	earliest := time.Time{}
	for _, rm := range r.servers {
		if rm == nil || rm.started.IsZero() {
			continue
		}
		if earliest.IsZero() || rm.started.Before(earliest) {
			earliest = rm.started
		}
	}
	r.started = earliest
}

// preferredModel returns the selected model when it is valid, otherwise the
// tier's strategy model. The caller must not hold r.mu.
func (r *Runtime) preferredModel() (localmodel.ModelSpec, bool) {
	r.mu.Lock()
	selected := r.selected
	r.mu.Unlock()
	if selected != "" {
		if spec, ok := r.catalog.Model(selected); ok {
			return spec, true
		}
	}
	return r.strategyModel()
}

// strategyModel returns the model assigned to the strategy role, if the tier has
// one. This is the default when the user has not selected another model.
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
func (r *Runtime) Start(ctx context.Context) error { return r.startDefault(ctx, false) }

// StartModel is the user-initiated variant: it downloads the strategy model
// even when local.auto_download is false, then starts llama-server. The UI uses
// this after the user explicitly taps "enable local AI".
func (r *Runtime) StartModel(ctx context.Context) error { return r.startDefault(ctx, true) }

func (r *Runtime) startDefault(ctx context.Context, force bool) error {
	r.mu.Lock()
	already := len(r.servers) > 0
	mode := strings.ToLower(strings.TrimSpace(r.mode))
	hasPlan := r.rec != nil && r.plan != nil
	rec := r.rec
	note := r.note
	r.mu.Unlock()
	if already {
		return nil
	}
	if !force && (mode == "api" || mode == "offline") {
		r.mu.Lock()
		r.note = "local runtime is idle because llm mode is " + mode
		r.mu.Unlock()
		return nil
	}
	if !hasPlan {
		return r.fail(fmt.Errorf("localruntime: no tier plan: %s", note), mode)
	}
	chosen, ok := r.preferredModel()
	if !ok {
		return r.fail(fmt.Errorf("localruntime: tier %s has no strategy model; select a model first", rec.Tier), mode)
	}
	return r.startModel(ctx, chosen, force)
}

// StartModelID starts one catalog model on its own loopback port. It is a no-op
// when that model is already running. force bypasses local.auto_download for
// the first-run download, exactly like StartModel.
func (r *Runtime) StartModelID(ctx context.Context, id string, force bool) error {
	spec, ok := r.catalogModel(id)
	if !ok {
		return fmt.Errorf("%w: %q (use GET /api/local/catalog to list model ids)", ErrUnknownModel, strings.TrimSpace(id))
	}
	return r.startModel(ctx, spec, force)
}

func (r *Runtime) startModel(ctx context.Context, spec localmodel.ModelSpec, force bool) error {
	r.startMu.Lock()
	defer r.startMu.Unlock()

	r.mu.Lock()
	mode := strings.ToLower(strings.TrimSpace(r.mode))
	if r.runningLocked(spec.ID) != nil {
		r.mu.Unlock()
		return nil
	}
	if r.rec == nil || r.plan == nil {
		note := r.note
		r.mu.Unlock()
		return r.fail(fmt.Errorf("localruntime: no tier plan: %s", note), mode)
	}
	r.mu.Unlock()

	modelPath := filepath.Join(r.cfg.ModelsDir, spec.File)
	needDownload := false
	if st, err := os.Stat(modelPath); err != nil || st.Size() != spec.SizeBytes {
		needDownload = true
	}
	if needDownload {
		if !force && !r.cfg.AutoDownload {
			err := fmt.Errorf("localruntime: model %s is not downloaded (download it in local AI settings or set local.auto_download=true)", spec.ID)
			r.mu.Lock()
			r.note = "selected model missing; first-run download is disabled"
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
		r.log.Info("downloading local model", "model", spec.ID, "size_gb", spec.SizeGB(), "url", r.catalog.URL(spec))
		res, err := d.Download(ctx, spec)
		if err != nil {
			return r.fail(fmt.Errorf("download %s: %w", spec.ID, err), mode)
		}
		modelPath = res.Path
		r.log.Info("local model ready", "path", modelPath, "cached", res.Cached)
	}

	runtimePath, err := localmodel.ResolveRuntime(r.cfg.RuntimePath)
	if err != nil {
		return r.fail(fmt.Errorf("%w (set local.runtime_path or GOPLAY_LLAMA_SERVER)", err), mode)
	}

	r.mu.Lock()
	ctxTokens := r.cfg.ContextTokens
	if r.plan.ContextTokens > ctxTokens {
		ctxTokens = r.plan.ContextTokens
	}
	threads := r.cfg.Threads
	if threads <= 0 {
		threads = r.plan.Threads
	}
	r.mu.Unlock()
	if ctxTokens < 512 {
		ctxTokens = 2048
	}
	if threads <= 0 {
		threads = 4
	}

	r.mu.Lock()
	port, portErr := r.allocatePortLocked()
	r.mu.Unlock()
	if portErr != nil {
		return r.fail(portErr, mode)
	}

	srv, err := localmodel.StartServer(ctx, localmodel.ServerConfig{
		RuntimePath: runtimePath, ModelPath: modelPath, Host: "127.0.0.1", Port: port,
		ContextTokens: ctxTokens, Threads: threads, StartupTimeout: 180 * time.Second,
	})
	if err != nil {
		return r.fail(fmt.Errorf("start llama-server: %w", err), mode)
	}
	provider := llm.NewOpenAI(llm.OpenAIConfig{
		BaseURL: srv.BaseURL(), Model: spec.ID, Timeout: 90 * time.Second,
		Temperature: 0.2, MaxTokens: 384, Provider: "llama.cpp",
	})

	r.mu.Lock()
	if r.servers == nil {
		r.servers = make(map[string]*runningModel)
	}
	r.servers[spec.ID] = &runningModel{
		modelID: spec.ID, srv: srv, provider: provider, port: port,
		ctxTokens: ctxTokens, threads: threads, started: time.Now(),
	}
	r.runtime = runtimePath
	r.err = nil
	r.note = "local model server is running; models are downloaded per device tier and never bundled"
	r.refreshStartedLocked()
	r.refreshModelsLocked()
	running := len(r.servers)
	r.mu.Unlock()
	r.log.Info("local model server ready",
		"base_url", srv.BaseURL(), "model", spec.ID, "pid", srv.PID(),
		"port", port, "running_models", running)
	return nil
}

// allocatePortLocked picks the next free loopback port for a new model server.
// The caller must hold r.mu (it reads the running model ports).
func (r *Runtime) allocatePortLocked() (int, error) {
	used := make(map[int]bool, len(r.servers))
	for _, rm := range r.servers {
		if rm != nil {
			used[rm.port] = true
		}
	}
	start := r.cfg.ServerPort
	if start <= 0 {
		start = 8421
	}
	return pickServerPort(start, used, portAvailable)
}

// pickServerPort returns the first port at or above start that no other model
// of this runtime uses and that the OS lets us bind on 127.0.0.1. probe reports
// whether one port is bindable; it is injected so port allocation can be tested
// without starting llama-server.
func pickServerPort(start int, used map[int]bool, probe func(port int) bool) (int, error) {
	if start < 1 {
		start = 8421
	}
	if start > 65535 {
		start = 65535
	}
	const maxAttempts = 100
	limit := start + maxAttempts
	if limit > 65536 {
		limit = 65536
	}
	for port := start; port < limit; port++ {
		if used[port] {
			continue
		}
		if probe(port) {
			return port, nil
		}
	}
	return 0, fmt.Errorf("localruntime: no free loopback port between %d and %d (set local.server_port)", start, limit-1)
}

// portAvailable reports whether 127.0.0.1:port can be bound right now.
func portAvailable(port int) bool {
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

// DownloadModel starts the strategy-model download in the background. Progress
// is visible through Status(). It is idempotent when the file is already present.
func (r *Runtime) DownloadModel() error {
	r.mu.Lock()
	plan, rec, note := r.plan, r.rec, r.note
	r.mu.Unlock()
	if plan == nil {
		return fmt.Errorf("localruntime: no tier plan: %s", note)
	}
	strategy, ok := r.strategyModel()
	if !ok {
		return fmt.Errorf("localruntime: tier %s has no strategy model", rec.Tier)
	}
	return r.downloadModelByID(strategy)
}

// DownloadModelID starts a background download for any catalog model. Only one
// download runs at a time.
func (r *Runtime) DownloadModelID(id string) error {
	spec, ok := r.catalogModel(id)
	if !ok {
		return fmt.Errorf("%w: %q (use GET /api/local/catalog to list model ids)", ErrUnknownModel, strings.TrimSpace(id))
	}
	return r.downloadModelByID(spec)
}

func (r *Runtime) downloadModelByID(spec localmodel.ModelSpec) error {
	r.mu.Lock()
	if r.dlCancel != nil || r.download.Active {
		r.mu.Unlock()
		return ErrDownloadRunning
	}
	if r.modelPresentLocked(spec) {
		r.download = DownloadStatus{Done: true, ModelID: spec.ID, Downloaded: spec.SizeBytes, Total: spec.SizeBytes, Percent: 100}
		r.note = "model already downloaded; select it and start the local AI"
		r.err = nil
		r.refreshModelsLocked()
		r.mu.Unlock()
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	r.dlCancel = cancel
	r.dlDone = done
	r.download = DownloadStatus{Active: true, ModelID: spec.ID, Total: spec.SizeBytes}
	r.err = nil
	r.note = "downloading " + spec.ID
	r.mu.Unlock()
	go r.runDownload(ctx, spec, done)
	return nil
}

func (r *Runtime) runDownload(ctx context.Context, spec localmodel.ModelSpec, done chan struct{}) {
	defer close(done)
	defer func() {
		r.mu.Lock()
		r.dlCancel = nil
		r.dlDone = nil
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
	r.log.Info("downloading local model", "model", spec.ID, "size_gb", spec.SizeGB())
	res, err := d.Download(ctx, spec)
	if err != nil {
		r.setDownloadError(fmt.Errorf("download %s: %w", spec.ID, err))
		return
	}
	r.mu.Lock()
	r.download.Active = false
	r.download.Done = true
	r.download.Percent = 100
	r.download.Downloaded = spec.SizeBytes
	r.download.Total = spec.SizeBytes
	r.note = "model downloaded; select it and start the local AI"
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

// DeleteModel removes one catalog model file from the models directory. It
// refuses to delete a model that is currently running (other running models do
// not block it) and rejects paths outside the directory.
func (r *Runtime) DeleteModel(id string) error {
	id = strings.TrimSpace(id)
	spec, ok := r.catalogModel(id)
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownModel, id)
	}
	path := filepath.Join(r.cfg.ModelsDir, spec.File)
	if !pathInside(r.cfg.ModelsDir, path) {
		return fmt.Errorf("%w: %q", ErrModelPathUnsafe, id)
	}

	r.startMu.Lock()
	defer r.startMu.Unlock()

	r.mu.Lock()
	if rm := r.runningLocked(spec.ID); rm != nil {
		r.mu.Unlock()
		return fmt.Errorf("%w: %s (stop this model first; other models keep running)", ErrModelInUse, spec.ID)
	}
	var done chan struct{}
	if r.download.Active && strings.EqualFold(r.download.ModelID, spec.ID) {
		if r.dlCancel != nil {
			r.dlCancel()
		}
		done = r.dlDone
	}
	r.mu.Unlock()

	if done != nil {
		<-done
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete model %s: %w (close any app using the file and retry)", spec.ID, err)
	}
	_ = os.Remove(path + ".part")

	r.mu.Lock()
	if strings.EqualFold(r.selected, spec.ID) {
		r.selected = ""
	}
	if strings.EqualFold(r.download.ModelID, spec.ID) && !r.download.Active {
		r.download = DownloadStatus{}
	}
	r.err = nil
	r.note = "deleted model " + spec.ID + "; download it again to restore"
	r.refreshModelsLocked()
	r.mu.Unlock()
	return nil
}

// SelectModel records the preferred model and starts it when it is not running
// yet. Other running models keep serving their roles.
func (r *Runtime) SelectModel(id string) error {
	id = strings.TrimSpace(id)
	spec, ok := r.catalogModel(id)
	if !ok {
		return fmt.Errorf("%w: %q (use GET /api/local/catalog to list model ids)", ErrUnknownModel, id)
	}
	r.mu.Lock()
	present := r.modelPresentLocked(spec)
	if !present {
		r.mu.Unlock()
		return fmt.Errorf("%w: download %s before selecting it", ErrModelNotReady, spec.ID)
	}
	r.selected = spec.ID
	running := r.runningLocked(spec.ID) != nil
	r.mu.Unlock()

	if running {
		return nil
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		if err := r.StartModelID(ctx, spec.ID, true); err != nil {
			r.log.Warn("start selected local model failed", "model", spec.ID, "err", err)
		}
	}()
	return nil
}

// pathInside reports whether path is a child of dir (not dir itself, not ..).
func pathInside(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil || rel == "." || filepath.IsAbs(rel) {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// StopModel stops one model server and removes its provider. Other models keep
// running. It is a no-op when that model is not running.
func (r *Runtime) StopModel(ctx context.Context, id string) error {
	spec, ok := r.catalogModel(id)
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownModel, strings.TrimSpace(id))
	}
	r.startMu.Lock()
	defer r.startMu.Unlock()
	return r.stopModelLocked(ctx, spec.ID)
}

func (r *Runtime) stopModelLocked(ctx context.Context, id string) error {
	r.mu.Lock()
	rm := r.runningLocked(id)
	if rm == nil {
		r.mu.Unlock()
		return nil
	}
	delete(r.servers, rm.modelID)
	remaining := len(r.servers)
	if remaining == 0 {
		r.note = "local model server stopped"
	} else {
		r.note = fmt.Sprintf("stopped %s; %d local model(s) still running", rm.modelID, remaining)
	}
	r.refreshStartedLocked()
	r.refreshModelsLocked()
	r.mu.Unlock()
	if rm.srv == nil {
		return nil
	}
	return rm.srv.Stop(ctx)
}

// StopServer stops every llama-server child and detaches their providers. It is
// safe when nothing is running.
func (r *Runtime) StopServer(ctx context.Context) error {
	r.startMu.Lock()
	defer r.startMu.Unlock()

	r.mu.Lock()
	running := make([]*runningModel, 0, len(r.servers))
	for _, rm := range r.servers {
		if rm != nil {
			running = append(running, rm)
		}
	}
	r.servers = make(map[string]*runningModel)
	if len(running) > 0 {
		r.note = "local model server stopped"
	}
	r.refreshStartedLocked()
	r.refreshModelsLocked()
	r.mu.Unlock()

	var errs []error
	for _, rm := range running {
		if rm.srv == nil {
			continue
		}
		if err := rm.srv.Stop(ctx); err != nil {
			errs = append(errs, fmt.Errorf("stop %s: %w", rm.modelID, err))
		}
	}
	return errors.Join(errs...)
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

// Provider returns the default llama.cpp provider (selected/strategy model, or
// any running model), or nil when the local runtime is not running.
func (r *Runtime) Provider() llm.Provider {
	r.mu.Lock()
	defer r.mu.Unlock()
	if rm := r.defaultRunningLocked(); rm != nil && rm.provider != nil {
		return rm.provider
	}
	return nil
}

// Providers returns every running local model provider keyed by model id. The
// app layer attaches the map to the LLM router with SetLocalModels.
func (r *Runtime) Providers() map[string]llm.Provider {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]llm.Provider, len(r.servers))
	for id, rm := range r.servers {
		if rm != nil && rm.provider != nil {
			out[id] = rm.provider
		}
	}
	return out
}

// Status returns a consistent snapshot for the HTTP API.
func (r *Runtime) Status() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	st := Status{
		Mode: r.mode, Enabled: len(r.servers) > 0, Hardware: r.hw,
		Models:      append([]ModelState(nil), r.models...),
		Catalog:     append([]ModelState(nil), r.catalogModels...),
		Selected:    r.selected,
		RuntimePath: r.runtime, RuntimeFound: r.runtime != "",
		Error: errString(r.err), Note: r.note, StartedAt: r.started,
		Download:  r.download,
		Providers: len(r.servers),
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
	servers := r.serverStatusesLocked()
	st.Servers = servers
	st.RunningModels = make([]string, 0, len(servers))
	for _, s := range servers {
		st.RunningModels = append(st.RunningModels, s.Model)
	}
	if len(servers) > 0 {
		st.Server = servers[0]
		st.Provider = "llama.cpp"
		st.Model = servers[0].Model
	}
	return st
}

// serverStatusesLocked lists the running servers with the default model first,
// then the remaining model ids in sorted order.
func (r *Runtime) serverStatusesLocked() []ServerStatus {
	out := make([]ServerStatus, 0, len(r.servers))
	if len(r.servers) == 0 {
		return out
	}
	def := r.defaultRunningLocked()
	appendOne := func(rm *runningModel) {
		if rm == nil {
			return
		}
		ss := ServerStatus{
			Running: true, Port: rm.port, Model: rm.modelID,
			ContextTokens: rm.ctxTokens, Threads: rm.threads,
		}
		if rm.srv != nil {
			if rm.port > 0 {
				ss.BaseURL = fmt.Sprintf("http://127.0.0.1:%d/v1", rm.port)
			}
			ss.PID = rm.srv.PID()
			if tail := rm.srv.LogTail(); tail != "" {
				if len(tail) > 4000 {
					tail = tail[len(tail)-4000:]
				}
				ss.LogTail = tail
			}
		}
		out = append(out, ss)
	}
	appendOne(def)
	ids := make([]string, 0, len(r.servers))
	for id := range r.servers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		rm := r.servers[id]
		if rm == nil || rm == def {
			continue
		}
		appendOne(rm)
	}
	return out
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// Close stops every child process. It is safe to call when the runtime never
// started.
func (r *Runtime) Close(ctx context.Context) error {
	return r.StopServer(ctx)
}
