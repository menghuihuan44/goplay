// Package api exposes the manager over HTTP and WebSocket. The browser UI and
// the mobile/desktop overlays are thin clients of this API; all game control
// stays inside the engine.
package api

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"goplay/internal/app"
	"goplay/internal/bus"
	"goplay/internal/config"
	"goplay/internal/domain"
	"goplay/internal/llm"
	"goplay/internal/localmodel"
	"goplay/internal/localruntime"
	"goplay/internal/metrics"
)

//go:embed web/*
var webFS embed.FS

type Server struct {
	mgr     *app.Manager
	cfg     config.Config
	started time.Time
	mux     *http.ServeMux
	up      websocket.Upgrader
}

func New(mgr *app.Manager, cfg config.Config) *Server {
	s := &Server{
		mgr: mgr, cfg: cfg, started: time.Now(),
		mux: http.NewServeMux(),
		up: websocket.Upgrader{
			ReadBufferSize: 4096, WriteBufferSize: 4096,
			// The daemon binds to loopback by default. A mobile client on the
			// same LAN needs cross-origin WebSocket; the README explains the
			// threat model and recommends a reverse proxy + token on untrusted
			// networks.
			CheckOrigin: func(*http.Request) bool { return true },
		},
	}
	s.routes()
	return s
}

func (s *Server) Handler() http.Handler {
	return recoverMW(corsMW(s.mux))
}

func (s *Server) routes() {
	mux := s.mux
	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("GET /api/config", s.getConfig)
	mux.HandleFunc("GET /api/instances", s.listInstances)
	mux.HandleFunc("POST /api/instances", s.createInstance)
	mux.HandleFunc("GET /api/instances/{id}", s.getInstance)
	mux.HandleFunc("DELETE /api/instances/{id}", s.deleteInstance)
	mux.HandleFunc("POST /api/instances/{id}/start", s.startInstance)
	mux.HandleFunc("POST /api/instances/{id}/stop", s.stopInstance)
	mux.HandleFunc("POST /api/instances/{id}/pause", s.pauseInstance)
	mux.HandleFunc("POST /api/instances/{id}/resume", s.resumeInstance)
	mux.HandleFunc("POST /api/instances/{id}/message", s.messageInstance)
	mux.HandleFunc("GET /api/instances/{id}/snapshot", s.snapshotInstance)
	mux.HandleFunc("GET /api/instances/{id}/events", s.instanceEvents)
	mux.HandleFunc("GET /api/instances/{id}/chat", s.instanceChat)
	mux.HandleFunc("POST /api/instances/{id}/chat", s.postInstanceChat)
	mux.HandleFunc("GET /api/instances/{id}/constraints", s.instanceConstraints)
	mux.HandleFunc("POST /api/instances/{id}/constraints", s.addConstraint)
	mux.HandleFunc("GET /api/games/windows", s.windowsGames)
	mux.HandleFunc("POST /api/games/windows/launch", s.launchWindowsGame)
	mux.HandleFunc("GET /api/instances/{id}/diagnostics", s.instanceDiagnostics)
	mux.HandleFunc("GET /api/instances/{id}/frame", s.instanceFrame)
	mux.HandleFunc("POST /api/instances/{id}/release", s.releaseInstance)
	mux.HandleFunc("GET /api/instances/{id}/teach", s.instanceTeaching)
	mux.HandleFunc("POST /api/instances/{id}/teach", s.updateTeaching)
	mux.HandleFunc("DELETE /api/instances/{id}/teach", s.clearTeaching)
	mux.HandleFunc("GET /api/instances/{id}/teach/frame", s.teachFrame)
	mux.HandleFunc("GET /api/instances/{id}/memory", s.recallMemory)
	mux.HandleFunc("GET /api/profiles", s.profiles)
	mux.HandleFunc("GET /api/memory/global", s.globalMemory)
	mux.HandleFunc("POST /api/memory/global/preferences", s.addGlobalPreference)
	mux.HandleFunc("DELETE /api/memory/global/preferences/{pid}", s.deleteGlobalPreference)
	mux.HandleFunc("GET /api/metrics", s.metricsJSON)
	mux.HandleFunc("GET /metrics", s.metricsText)
	mux.HandleFunc("GET /api/local/status", s.localStatus)
	mux.HandleFunc("POST /api/local/recommend", s.localRecommend)
	mux.HandleFunc("GET /api/local/catalog", s.localCatalog)
	mux.HandleFunc("POST /api/local/download", s.localDownload)
	mux.HandleFunc("DELETE /api/local/models/{id}", s.localDeleteModel)
	mux.HandleFunc("POST /api/local/select", s.localSelect)
	mux.HandleFunc("POST /api/local/start", s.localStart)
	mux.HandleFunc("POST /api/local/stop", s.localStop)
	mux.HandleFunc("POST /api/local/cancel", s.localCancel)
	mux.HandleFunc("GET /api/ai/routes", s.getAIRoutes)
	mux.HandleFunc("POST /api/ai/routes", s.updateAIRoutes)
	mux.HandleFunc("GET /api/llm", s.getLLM)
	mux.HandleFunc("POST /api/llm", s.updateLLM)
	mux.HandleFunc("POST /api/llm/test", s.testLLM)
	mux.HandleFunc("GET /api/voice", s.getVoice)
	mux.HandleFunc("POST /api/voice", s.updateVoice)
	mux.HandleFunc("POST /api/voice/tts", s.speakVoice)
	// Legacy mobile control surface (android/PROTOCOL.md v1).
	mux.HandleFunc("POST /api/control/pause", s.legacyControl(s.mgr.Pause))
	mux.HandleFunc("POST /api/control/resume", s.legacyControl(s.mgr.Resume))
	mux.HandleFunc("POST /api/message", s.legacyMessage)
	mux.HandleFunc("GET /ws/instance/{id}", s.instanceWS)
	mux.HandleFunc("GET /ws/events", s.eventsWS)

	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		panic(err)
	}
	files := http.FileServer(http.FS(sub))
	mux.Handle("GET /static/", http.StripPrefix("/static/", files))
	mux.HandleFunc("GET /overlay", func(w http.ResponseWriter, r *http.Request) {
		serveEmbedded(w, sub, "overlay.html", "text/html; charset=utf-8")
	})
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/ws/") {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		serveEmbedded(w, sub, "index.html", "text/html; charset=utf-8")
	})
}

func serveEmbedded(w http.ResponseWriter, fsys fs.FS, name, ctype string) {
	b, err := fs.ReadFile(fsys, name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "embedded asset missing")
		return
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(b)
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	list := s.mgr.List()
	running := 0
	for _, in := range list {
		if in.Status == domain.StatusRunning || in.Status == domain.StatusHuman {
			running++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "service": "goplay", "version": s.cfg.Version,
		"uptime_seconds": time.Since(s.started).Seconds(),
		"instances":      len(list), "running": running, "addr": s.cfg.Addr,
	})
}

func (s *Server) getConfig(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg
	writeJSON(w, http.StatusOK, map[string]any{
		"config":   cfg.Redacted(),
		"llm":      s.mgr.Router().Status(r.Context()),
		"profiles": s.mgr.Profiles(),
	})
}

func (s *Server) listInstances(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"instances": s.mgr.List()})
}

type createRequest struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	ProfileID     string `json:"profile_id"`
	SourceKind    string `json:"source_kind"`
	TargetWindow  string `json:"target_window"`
	PackageName   string `json:"package_name"`
	Seed          int64  `json:"seed"`
	MaxEpisodes   int    `json:"max_episodes"`
	TickMS        int    `json:"tick_ms"`
	HumanResumeMS int    `json:"human_resume_ms"`
	AutoStart     *bool  `json:"auto_start"`
}

func (s *Server) createInstance(w http.ResponseWriter, r *http.Request) {
	var req createRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	auto := true
	if req.AutoStart != nil {
		auto = *req.AutoStart
	}
	if req.SourceKind == app.SourceWindows && strings.TrimSpace(req.TargetWindow) == "" {
		writeError(w, http.StatusBadRequest, "Windows 实例必须填写游戏窗口标题（target_window）。只在这个窗口前台时才会发送键鼠，避免误操作到聊天窗口或其它程序。")
		return
	}
	spec := app.Spec{
		ID: req.ID, Name: req.Name, ProfileID: req.ProfileID, SourceKind: req.SourceKind,
		TargetWindow: req.TargetWindow, PackageName: req.PackageName,
		Seed: req.Seed, MaxEpisodes: req.MaxEpisodes, TickMS: req.TickMS,
		HumanResumeMS: req.HumanResumeMS, AutoStart: auto,
	}
	view, err := s.mgr.Create(spec)
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "already exists") {
			status = http.StatusConflict
		}
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, view)
}

func (s *Server) getInstance(w http.ResponseWriter, r *http.Request) {
	view, err := s.mgr.Get(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) deleteInstance(w http.ResponseWriter, r *http.Request) {
	if err := s.mgr.Delete(r.PathValue("id")); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

func (s *Server) startInstance(w http.ResponseWriter, r *http.Request) { s.control(w, r, s.mgr.Start) }
func (s *Server) stopInstance(w http.ResponseWriter, r *http.Request)  { s.control(w, r, s.mgr.Stop) }
func (s *Server) pauseInstance(w http.ResponseWriter, r *http.Request) { s.control(w, r, s.mgr.Pause) }
func (s *Server) resumeInstance(w http.ResponseWriter, r *http.Request) {
	s.control(w, r, s.mgr.Resume)
}

func (s *Server) control(w http.ResponseWriter, r *http.Request, fn func(string) error) {
	id := r.PathValue("id")
	if err := fn(id); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	view, err := s.mgr.Get(id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) messageInstance(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Text string `json:"text"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.mgr.Submit(r.PathValue("id"), req.Text); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"accepted": true,
		"note":     "queued in the instruction buffer; applied at the next event boundary",
	})
}

func (s *Server) snapshotInstance(w http.ResponseWriter, r *http.Request) {
	view, err := s.mgr.Get(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) instanceEvents(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	writeJSON(w, http.StatusOK, map[string]any{"events": s.mgr.Events(r.PathValue("id"), limit)})
}

func (s *Server) instanceChat(w http.ResponseWriter, r *http.Request) {
	view, err := s.mgr.Get(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"chat": view.Chat})
}

// postInstanceChat queues the user's instruction and returns the language
// layer's immediate reply. A missing provider yields a fallback answer, not 500.
func (s *Server) postInstanceChat(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.mgr.Get(id); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	var req struct {
		Text string `json:"text"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(req.Text) == "" {
		writeError(w, http.StatusBadRequest, "text must not be empty")
		return
	}
	res, err := s.mgr.ChatDetailed(r.Context(), id, req.Text)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"accepted": true,
		"reply":    res.Reply,
		"provider": res.Provider,
		"model":    res.Model,
		"fallback": res.Fallback,
	})
}

func (s *Server) instanceConstraints(w http.ResponseWriter, r *http.Request) {
	view, err := s.mgr.Get(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"constraints": view.Control.Hard})
}

func (s *Server) addConstraint(w http.ResponseWriter, r *http.Request) {
	var c domain.Constraint
	if err := readJSON(r, &c); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.mgr.AddConstraint(r.PathValue("id"), c); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"constraint": c})
}

func (s *Server) recallMemory(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	k, _ := strconv.Atoi(r.URL.Query().Get("k"))
	if k <= 0 {
		k = 5
	}
	start := time.Now()
	res, err := s.mgr.Recall(r.PathValue("id"), q, k)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	s.mgr.Metrics().Observe(metrics.MemoryRecallMS, float64(time.Since(start).Microseconds())/1000)
	writeJSON(w, http.StatusOK, map[string]any{"query": q, "results": res, "latency_ms": float64(time.Since(start).Microseconds()) / 1000})
}

func (s *Server) profiles(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"profiles": s.mgr.Profiles()})
}

func (s *Server) globalMemory(w http.ResponseWriter, r *http.Request) {
	g := s.mgr.Memory().Global().SnapshotCopy()
	writeJSON(w, http.StatusOK, map[string]any{
		"preferences": s.mgr.GlobalPreferences(),
		"facts":       len(g.Facts),
		"updated_at":  g.UpdatedAt,
		"note":        "cross-game preferences only; per-game state lives in each instance memory",
	})
}

func (s *Server) addGlobalPreference(w http.ResponseWriter, r *http.Request) {
	var p domain.Preference
	if err := readJSON(r, &p); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.mgr.AddGlobalPreference(p); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

func (s *Server) deleteGlobalPreference(w http.ResponseWriter, r *http.Request) {
	if err := s.mgr.DeleteGlobalPreference(r.PathValue("pid")); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

func (s *Server) metricsJSON(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.mgr.Metrics().Snapshot())
}

func (s *Server) metricsText(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	io.WriteString(w, "# GoPlay metrics\n")
	io.WriteString(w, s.mgr.Metrics().Prometheus())
}

func (s *Server) localDownload(w http.ResponseWriter, r *http.Request) {
	rt := s.mgr.LocalRuntime()
	if rt == nil {
		writeError(w, http.StatusServiceUnavailable, "local runtime is not attached")
		return
	}
	var req struct {
		ModelID string `json:"model_id"`
	}
	if err := readOptionalJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	id := strings.TrimSpace(req.ModelID)
	var err error
	if id == "" {
		err = rt.DownloadModel()
	} else {
		err = rt.DownloadModelID(id)
	}
	if err != nil {
		status := http.StatusConflict
		if errors.Is(err, localruntime.ErrUnknownModel) {
			status = http.StatusNotFound
		}
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true, "model_id": id, "status": rt.Status()})
}

// localCatalog lists every downloadable model plus the selected one.
func (s *Server) localCatalog(w http.ResponseWriter, r *http.Request) {
	st, ok := s.mgr.LocalStatus()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "local runtime is not attached")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"tier":     st.Tier,
		"selected": st.Selected,
		"models":   st.Models,
		"catalog":  st.Catalog,
	})
}

func (s *Server) localDeleteModel(w http.ResponseWriter, r *http.Request) {
	rt := s.mgr.LocalRuntime()
	if rt == nil {
		writeError(w, http.StatusServiceUnavailable, "local runtime is not attached")
		return
	}
	id := r.PathValue("id")
	if err := rt.DeleteModel(id); err != nil {
		status := http.StatusConflict
		if errors.Is(err, localruntime.ErrUnknownModel) {
			status = http.StatusNotFound
		}
		writeError(w, status, err.Error())
		return
	}
	s.mgr.SyncLocalProviders()
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "model_id": id, "status": rt.Status()})
}

func (s *Server) localSelect(w http.ResponseWriter, r *http.Request) {
	rt := s.mgr.LocalRuntime()
	if rt == nil {
		writeError(w, http.StatusServiceUnavailable, "local runtime is not attached")
		return
	}
	var req struct {
		ModelID string `json:"model_id"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := rt.SelectModel(req.ModelID); err != nil {
		status := http.StatusConflict
		if errors.Is(err, localruntime.ErrUnknownModel) {
			status = http.StatusNotFound
		}
		writeError(w, status, err.Error())
		return
	}
	s.mgr.SyncLocalProviders()
	s.syncWhenRunning(req.ModelID)
	writeJSON(w, http.StatusOK, map[string]any{"selected": req.ModelID, "status": rt.Status()})
}

func (s *Server) localStart(w http.ResponseWriter, r *http.Request) {
	rt := s.mgr.LocalRuntime()
	if rt == nil {
		writeError(w, http.StatusServiceUnavailable, "local runtime is not attached")
		return
	}
	var req struct {
		ModelID string `json:"model_id"`
	}
	if err := readOptionalJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	id := strings.TrimSpace(req.ModelID)
	if id == "" {
		cfg := s.cfg
		cfg.LLM.Mode = "local"
		if err := cfg.Save(filepath.Join(cfg.DataDir, "config.json")); err != nil {
			writeError(w, http.StatusInternalServerError, "save config: "+err.Error())
			return
		}
		s.cfg = cfg
		rt.SetMode("local")
		s.mgr.Router().SetMode("local")
		go s.ensureLocalProvider()
	} else {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
			defer cancel()
			if err := rt.StartModelID(ctx, id, true); err != nil {
				return
			}
			s.mgr.SyncLocalProviders()
		}()
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"accepted": true,
		"model_id": id,
		"note":     "downloading if needed, then starting llama-server on loopback",
		"status":   rt.Status(),
	})
}

func (s *Server) localStop(w http.ResponseWriter, r *http.Request) {
	rt := s.mgr.LocalRuntime()
	if rt == nil {
		writeError(w, http.StatusServiceUnavailable, "local runtime is not attached")
		return
	}
	var req struct {
		ModelID string `json:"model_id"`
	}
	if err := readOptionalJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if id := strings.TrimSpace(req.ModelID); id != "" {
		if err := rt.StopModel(r.Context(), id); err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		s.mgr.SyncLocalProviders()
		writeJSON(w, http.StatusOK, map[string]any{"stopped": true, "model_id": id, "status": rt.Status()})
		return
	}
	if err := rt.StopServer(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.mgr.Router().SetLocal(nil)
	s.mgr.Router().SetLocalModels(nil)
	s.mgr.Router().SetMode("auto")
	rt.SetMode("auto")
	cfg := s.cfg
	cfg.LLM.Mode = "auto"
	if err := cfg.Save(filepath.Join(cfg.DataDir, "config.json")); err == nil {
		s.cfg = cfg
	}
	writeJSON(w, http.StatusOK, map[string]any{"stopped": true, "status": rt.Status()})
}

func (s *Server) localCancel(w http.ResponseWriter, r *http.Request) {
	rt := s.mgr.LocalRuntime()
	if rt == nil {
		writeError(w, http.StatusServiceUnavailable, "local runtime is not attached")
		return
	}
	rt.CancelDownload()
	writeJSON(w, http.StatusOK, map[string]any{"cancelled": true, "status": rt.Status()})
}

// ensureLocalProvider downloads (user-initiated) and launches llama-server, then
// attaches the provider to the router. The triggering HTTP request returns
// immediately, so this uses a background context.
func (s *Server) ensureLocalProvider() {
	rt := s.mgr.LocalRuntime()
	if rt == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	if err := rt.StartModel(ctx); err != nil {
		return
	}
	s.mgr.SyncLocalProviders()
}

// syncWhenRunning waits (bounded) for an asynchronously started model and then
// publishes every running model to the router.
func (s *Server) syncWhenRunning(id string) {
	id = strings.ToLower(strings.TrimSpace(id))
	if id == "" {
		return
	}
	go func() {
		for i := 0; i < 120; i++ {
			time.Sleep(500 * time.Millisecond)
			st, ok := s.mgr.LocalStatus()
			if !ok {
				return
			}
			if localModelRunning(st, id) {
				s.mgr.SyncLocalProviders()
				return
			}
		}
	}()
}

func localModelRunning(st localruntime.Status, id string) bool {
	for _, m := range st.Models {
		if strings.EqualFold(m.ID, id) && m.Running {
			return true
		}
	}
	return false
}

// normalizeLLMRoutes validates role names and modes before persisting.
func normalizeLLMRoutes(in map[string]config.RouteConfig) (map[string]config.RouteConfig, error) {
	out := make(map[string]config.RouteConfig, len(in))
	for role, rc := range in {
		name := strings.ToLower(strings.TrimSpace(role))
		if !llm.ValidRole(name) {
			return nil, fmt.Errorf("unknown role %q (want %s)", role, strings.Join(llm.Roles(), ", "))
		}
		mode := strings.ToLower(strings.TrimSpace(rc.Mode))
		switch mode {
		case "", "auto", "api", "local", "offline":
		default:
			return nil, fmt.Errorf("routes[%s].mode must be auto, api, local or offline", name)
		}
		if _, dup := out[name]; dup {
			return nil, fmt.Errorf("duplicate role %q", name)
		}
		out[name] = config.RouteConfig{Mode: mode, Model: strings.TrimSpace(rc.Model)}
	}
	return out, nil
}

func (s *Server) aiRoutesBody(ctx context.Context) map[string]any {
	statuses := s.mgr.Router().RoutesStatus(ctx)
	available := make(map[string]any, len(statuses))
	for role, raw := range statuses {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		available[role] = map[string]any{
			"provider":  entry["provider"],
			"model":     entry["model"],
			"available": entry["available"],
		}
	}
	routes := make(map[string]config.RouteConfig)
	for role, rc := range s.mgr.Router().Routes() {
		routes[role] = config.RouteConfig{Mode: rc.Mode, Model: rc.Model}
	}
	return map[string]any{
		"global_mode": s.mgr.Router().Mode(),
		"routes":      routes,
		"available":   available,
		// Per-role live status: implemented, calls, state, last activity/error.
		// The UI uses this to say who is actually working instead of implying
		// every configured layer runs a separate model.
		"status": statuses,
		"roles":  llm.Roles(),
	}
}

// getAIRoutes reports the per-layer mode/model and whether each layer can run.
func (s *Server) getAIRoutes(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.aiRoutesBody(r.Context()))
}

func (s *Server) updateAIRoutes(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Routes map[string]config.RouteConfig `json:"routes"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Routes == nil {
		writeError(w, http.StatusBadRequest, "routes is required (send {} to clear all overrides)")
		return
	}
	routes, err := normalizeLLMRoutes(req.Routes)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	cfg := s.cfg
	cfg.LLM.Routes = routes
	if err := cfg.Save(filepath.Join(cfg.DataDir, "config.json")); err != nil {
		writeError(w, http.StatusInternalServerError, "save config: "+err.Error())
		return
	}
	s.cfg = cfg
	s.mgr.Router().SetRoutes(app.RoutesFromConfig(routes))
	writeJSON(w, http.StatusOK, s.aiRoutesBody(r.Context()))
}

func (s *Server) getLLM(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg
	body := map[string]any{
		"mode":        s.mgr.Router().Mode(),
		"base_url":    cfg.LLM.BaseURL,
		"model":       cfg.LLM.Model,
		"has_api_key": strings.TrimSpace(cfg.LLM.APIKey) != "",
		"temperature": cfg.LLM.Temperature,
		"max_tokens":  cfg.LLM.MaxTokens,
		"timeout_ms":  cfg.LLM.TimeoutMS,
		"vision":      cfg.LLM.Vision,
		"routes":      s.mgr.Router().RoutesStatus(r.Context()),
		"router":      s.mgr.Router().Status(r.Context()),
		"voice":       s.voiceView(),
	}
	if st, ok := s.mgr.LocalStatus(); ok {
		body["local"] = st
	}
	writeJSON(w, http.StatusOK, body)
}

type llmUpdateRequest struct {
	Mode        string                        `json:"mode"`
	BaseURL     string                        `json:"base_url"`
	APIKey      string                        `json:"api_key"`
	ClearKey    bool                          `json:"clear_key"`
	Model       string                        `json:"model"`
	Temperature *float64                      `json:"temperature"`
	MaxTokens   *int                          `json:"max_tokens"`
	TimeoutMS   *int                          `json:"timeout_ms"`
	Vision      *bool                         `json:"vision"`
	Routes      map[string]config.RouteConfig `json:"routes,omitempty"`
}

func (s *Server) updateLLM(w http.ResponseWriter, r *http.Request) {
	var req llmUpdateRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	mode := strings.ToLower(strings.TrimSpace(req.Mode))
	switch mode {
	case "", "auto", "api", "local", "offline":
	default:
		writeError(w, http.StatusBadRequest, "mode must be auto, api, local or offline")
		return
	}
	cfg := s.cfg
	if mode != "" {
		cfg.LLM.Mode = mode
	}
	if v := strings.TrimSpace(req.BaseURL); v != "" {
		cfg.LLM.BaseURL = strings.TrimRight(v, "/")
	}
	if req.ClearKey {
		cfg.LLM.APIKey = ""
	} else if v := strings.TrimSpace(req.APIKey); v != "" {
		cfg.LLM.APIKey = v
	}
	if v := strings.TrimSpace(req.Model); v != "" {
		cfg.LLM.Model = v
	}
	if req.Temperature != nil {
		cfg.LLM.Temperature = *req.Temperature
	}
	if req.MaxTokens != nil {
		cfg.LLM.MaxTokens = *req.MaxTokens
	}
	if req.TimeoutMS != nil {
		cfg.LLM.TimeoutMS = *req.TimeoutMS
	}
	if req.Vision != nil {
		cfg.LLM.Vision = *req.Vision
	}
	if req.Routes != nil {
		routes, err := normalizeLLMRoutes(req.Routes)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		cfg.LLM.Routes = routes
	}
	var api llm.Provider
	if cfg.LLM.BaseURL != "" && cfg.LLM.Model != "" {
		api = llm.NewOpenAI(llm.OpenAIConfig{
			BaseURL: cfg.LLM.BaseURL, APIKey: cfg.LLM.APIKey, Model: cfg.LLM.Model,
			Timeout:     time.Duration(cfg.LLM.TimeoutMS) * time.Millisecond,
			Temperature: cfg.LLM.Temperature, MaxTokens: cfg.LLM.MaxTokens,
			Provider: "openai-compatible",
		})
	}
	s.mgr.Router().SetAPI(api)
	s.mgr.Router().SetMode(cfg.LLM.Mode)
	s.mgr.Router().SetRoutes(app.RoutesFromConfig(cfg.LLM.Routes))
	if rt := s.mgr.LocalRuntime(); rt != nil {
		rt.SetMode(cfg.LLM.Mode)
		if cfg.LLM.Mode == "local" {
			go s.ensureLocalProvider()
		}
	}
	if err := cfg.Save(filepath.Join(cfg.DataDir, "config.json")); err != nil {
		writeError(w, http.StatusInternalServerError, "save config: "+err.Error())
		return
	}
	s.cfg = cfg
	s.getLLM(w, r)
}

func (s *Server) testLLM(w http.ResponseWriter, r *http.Request) {
	api, _ := s.mgr.Router().Providers()
	if api == nil {
		writeError(w, http.StatusBadRequest, "no API provider configured")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	start := time.Now()
	resp, err := api.Complete(ctx, llm.Request{
		System: "You are a connectivity probe. Reply with exactly: ok",
		Prompt: "ok", MaxTokens: 8, Temperature: 0.1,
	})
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "model": resp.Model, "provider": resp.Provider,
		"latency_ms": float64(time.Since(start).Microseconds()) / 1000,
		"text":       strings.TrimSpace(resp.Text),
	})
}

func (s *Server) localStatus(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg
	body := map[string]any{
		"mode":         s.mgr.Router().Mode(),
		"models_dir":   cfg.Local.ModelsDir,
		"runtime_path": cfg.Local.RuntimePath,
		"server_port":  cfg.Local.ServerPort,
		"tier":         cfg.Local.PreferredTier,
		"llm":          s.mgr.Router().Status(r.Context()),
		"note":         "model tiers are downloaded on first run, never bundled in the installer",
	}
	if st, ok := s.mgr.LocalStatus(); ok {
		body["runtime"] = st
		body["selected"] = st.Selected
		body["models"] = st.Models
		body["catalog"] = st.Catalog
	}
	writeJSON(w, http.StatusOK, body)
}

// localRecommend maps a mobile device's RAM/ABI to a model tier. The phone
// never bundles weights: this only tells the user which tier the core should
// download and run for that device.
func (s *Server) localRecommend(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Device string   `json:"device"`
		RAMMb  int64    `json:"ramMb"`
		ABIs   []string `json:"abis"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.RAMMb <= 0 {
		writeError(w, http.StatusBadRequest, "ramMb must be positive")
		return
	}
	arch := "arm64"
	if len(req.ABIs) > 0 {
		arch = req.ABIs[0]
	}
	hw := localmodel.HardwareInfo{
		OS: "android", Arch: arch, RAMBytes: uint64(req.RAMMb) << 20,
		RAMGB: float64(req.RAMMb) / 1024.0, Cores: 0, Threads: 0,
	}
	rec, err := localmodel.Recommend(hw, s.cfg.Local.PreferredTier)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	catalog := localmodel.DefaultCatalog()
	plan, ok := catalog.Plan(rec.Tier)
	if !ok {
		writeError(w, http.StatusInternalServerError, "no plan for tier "+string(rec.Tier))
		return
	}
	var approx int64
	if models, ok := catalog.RequiredModels(rec.Tier); ok {
		for _, m := range models {
			approx += m.SizeBytes
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"tier": rec.Tier, "reason": rec.Rationale,
		"download_required": true, "downloadRequired": true,
		"approx_bytes": approx, "approxBytes": approx,
		"hardware": hw, "plan": plan,
		"note": "models are downloaded by the core on first run and never bundled with the APK",
	})
}

func readJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("empty JSON body")
		}
		return err
	}
	return nil
}

// readOptionalJSON accepts an empty body and otherwise behaves like readJSON.
func readOptionalJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}

func corsMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func recoverMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				writeError(w, http.StatusInternalServerError, fmt.Sprintf("panic: %v", v))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) Serve(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.cfg.Addr)
	if err != nil {
		return err
	}
	return s.ServeListener(ctx, ln)
}

// ServeListener serves on an already-bound listener. It is used by the desktop
// shell so the UI can start on port 0 without racing another process.
func (s *Server) ServeListener(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()
	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdown); err != nil {
			return err
		}
		return nil
	}
}

// RecentEvents is exported for tests.
func (s *Server) RecentEvents(id string, n int) []bus.Event { return s.mgr.Events(id, n) }

type legacyTarget struct {
	InstanceID string `json:"instanceId"`
	Reason     string `json:"reason,omitempty"`
	Text       string `json:"text,omitempty"`
	Kind       string `json:"kind,omitempty"`
}

func (s *Server) legacyControl(fn func(string) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req legacyTarget
		if err := readJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if strings.TrimSpace(req.InstanceID) == "" {
			writeError(w, http.StatusBadRequest, "instanceId is required")
			return
		}
		if err := fn(req.InstanceID); err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "instanceId": req.InstanceID})
	}
}

func (s *Server) legacyMessage(w http.ResponseWriter, r *http.Request) {
	var req legacyTarget
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(req.InstanceID) == "" || strings.TrimSpace(req.Text) == "" {
		writeError(w, http.StatusBadRequest, "instanceId and text are required")
		return
	}
	if err := s.mgr.Submit(req.InstanceID, req.Text); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "note": "queued in the instruction buffer; applied at the next event boundary"})
}
