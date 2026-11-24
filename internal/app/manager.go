// Package app owns the set of game instances and the shared services they use.
// It is deliberately transport-agnostic: the HTTP/WebSocket API is a thin layer
// on top, and the same manager can be driven by tests or a CLI.
package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"goplay/internal/action"
	"goplay/internal/bridge"
	"goplay/internal/bus"
	"goplay/internal/config"
	"goplay/internal/domain"
	"goplay/internal/engine"
	"goplay/internal/llm"
	"goplay/internal/localruntime"
	"goplay/internal/luabridge"
	"goplay/internal/memory"
	"goplay/internal/metrics"
	"goplay/internal/mockgame"
	"goplay/internal/native"
	"goplay/internal/perception"
	"goplay/internal/policy"
	"goplay/internal/scheduler"
)

const (
	SourceMock    = "mock"
	SourceWindows = "windows"
	SourceAndroid = "android"
)

var idPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

// Spec is the persisted description of one game instance. Runtime objects are
// rebuilt from it when the daemon starts.
type Spec struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	ProfileID  string `json:"profile_id"`
	SourceKind string `json:"source_kind"`
	// TargetWindow is an optional window-title substring. When set, only input
	// while that window is foreground counts as human takeover; the agent's own
	// console and other programs are ignored.
	TargetWindow  string    `json:"target_window,omitempty"`
	Seed          int64     `json:"seed"`
	MaxEpisodes   int       `json:"max_episodes"`
	TickMS        int       `json:"tick_ms"`
	HumanResumeMS int       `json:"human_resume_ms"`
	LLMMode       string    `json:"llm_mode,omitempty"`
	AutoStart     bool      `json:"auto_start"`
	CreatedAt     time.Time `json:"created_at"`
}

func (s *Spec) normalize() {
	if s.SourceKind == "" {
		s.SourceKind = SourceMock
	}
	if s.ProfileID == "" {
		switch s.SourceKind {
		case SourceMock:
			s.ProfileID = "mock_dungeon"
		case SourceWindows:
			s.ProfileID = "generic"
		default:
			s.ProfileID = "generic_touch"
		}
	}
	if s.MaxEpisodes <= 0 {
		s.MaxEpisodes = 1
	}
	if s.MaxEpisodes > 1000 {
		s.MaxEpisodes = 1000
	}
	if s.Seed == 0 {
		s.Seed = time.Now().UnixNano() & 0x7fffffff
	}
	if s.Name == "" {
		s.Name = s.ID
	}
	if s.CreatedAt.IsZero() {
		s.CreatedAt = time.Now()
	}
}

type managed struct {
	mu       sync.Mutex
	spec     Spec
	profile  *luabridge.Profile
	game     *mockgame.Game
	source   perception.Source
	analyzer perception.Analyzer
	executor action.Executor
	human    *action.MockHuman
	sched    *scheduler.Scheduler
	pol      *policy.Policy
	runner   *engine.Runner
	sess     *bridge.Session
}

// Manager owns every live instance and the cross-instance stores.
type Manager struct {
	cfg    config.Config
	reg    *metrics.Registry
	bus    *bus.Bus
	mem    *memory.Manager
	router *llm.Router
	log    *slog.Logger
	local  *localruntime.Runtime

	mu    sync.RWMutex
	insts map[string]*managed
	specs map[string]Spec
}

type registryFile struct {
	Version   int    `json:"version"`
	Instances []Spec `json:"instances"`
}

// New opens the data directory and restores the persisted instances.
func New(cfg config.Config, reg *metrics.Registry, router *llm.Router, log *slog.Logger) (*Manager, error) {
	if reg == nil {
		reg = metrics.New()
	}
	if log == nil {
		log = slog.Default()
	}
	mem, err := memory.Open(filepath.Join(cfg.DataDir, "memory"), cfg.MemoryBudgetKB)
	if err != nil {
		return nil, err
	}
	m := &Manager{
		cfg: cfg, reg: reg, bus: bus.New(8192), mem: mem, router: router, log: log,
		insts: map[string]*managed{}, specs: map[string]Spec{},
	}
	if router != nil {
		router.SetRoutes(RoutesFromConfig(cfg.LLM.Routes))
	}
	if err := m.loadRegistry(); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) Config() config.Config      { return m.cfg }
func (m *Manager) Metrics() *metrics.Registry { return m.reg }
func (m *Manager) Bus() *bus.Bus              { return m.bus }
func (m *Manager) Memory() *memory.Manager    { return m.mem }
func (m *Manager) Router() *llm.Router        { return m.router }

// RoutesFromConfig converts persisted per-role routes to the llm package form.
func RoutesFromConfig(in map[string]config.RouteConfig) map[string]llm.RouteConfig {
	out := make(map[string]llm.RouteConfig, len(in))
	for role, rc := range in {
		out[role] = llm.RouteConfig{Mode: rc.Mode, Model: rc.Model}
	}
	return out
}

// RoutesToConfig converts router routes into the config representation.
func RoutesToConfig(in map[string]llm.RouteConfig) map[string]config.RouteConfig {
	out := make(map[string]config.RouteConfig, len(in))
	for role, rc := range in {
		out[role] = config.RouteConfig{Mode: rc.Mode, Model: rc.Model}
	}
	return out
}

func (m *Manager) registryPath() string { return filepath.Join(m.cfg.DataDir, "instances.json") }

func (m *Manager) loadRegistry() error {
	b, err := os.ReadFile(m.registryPath())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var rf registryFile
	if err := json.Unmarshal(b, &rf); err != nil {
		return fmt.Errorf("load instances: %w", err)
	}
	for _, s := range rf.Instances {
		s.normalize()
		if err := m.build(s); err != nil {
			m.log.Warn("restore instance", "id", s.ID, "err", err)
			continue
		}
		m.specs[s.ID] = s
	}
	for id, spec := range m.specs {
		if spec.AutoStart {
			if err := m.Start(id); err != nil {
				m.log.Warn("auto start", "id", id, "err", err)
			}
		}
	}
	return nil
}

func (m *Manager) saveRegistryLocked() error {
	rf := registryFile{Version: 1}
	ids := make([]string, 0, len(m.specs))
	for id := range m.specs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		rf.Instances = append(rf.Instances, m.specs[id])
	}
	b, err := json.MarshalIndent(rf, "", "  ")
	if err != nil {
		return err
	}
	path := m.registryPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func safeDir(id string) string {
	var b strings.Builder
	for _, r := range id {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "default"
	}
	return b.String()
}

// build creates the runtime objects for one spec. It does not start the loop.
func (m *Manager) build(spec Spec) error {
	spec.normalize()
	if !idPattern.MatchString(spec.ID) {
		return fmt.Errorf("invalid instance id %q", spec.ID)
	}
	switch spec.SourceKind {
	case SourceMock, SourceWindows, SourceAndroid:
	default:
		return fmt.Errorf("source %q is not available in this build; use %q, %q or %q", spec.SourceKind, SourceMock, SourceWindows, SourceAndroid)
	}
	prof, err := luabridge.LoadBuiltin(spec.ProfileID)
	if err != nil {
		return err
	}
	var (
		game  *mockgame.Game
		human action.HumanDetector
		mockH *action.MockHuman
		src   perception.Source
		an    perception.Analyzer
		ex    action.Executor
		reset func() error
		sess  *bridge.Session
	)
	if spec.SourceKind == SourceMock {
		game = mockgame.New(spec.Seed)
		mockH = &action.MockHuman{}
		human = mockH
		src = perception.NewMockSource(game, 0.001)
		an = perception.MockAnalyzer{}
		ex = &action.MockExecutor{Game: game, Human: mockH}
		reset = func() error { game.Reset(); return nil }
	} else if spec.SourceKind == SourceAndroid {
		sess = bridge.NewSession(spec.ID)
		human = sess
		src = sess
		an = perception.NewVisionAnalyzer(m.router.For(llm.RolePerception), perception.GenericAnalyzer{})
		ex = sess
	} else {
		ns, err := native.OpenScreenSource("", 0)
		if err != nil {
			prof.Close()
			return err
		}
		w, h := native.PrimaryScreenSize()
		we, err := action.NewWindowsExecutor("", w, h)
		if err != nil {
			ns.Close()
			prof.Close()
			return err
		}
		if t := strings.TrimSpace(spec.TargetWindow); t != "" {
			we.SetTargetTitles([]string{t})
		}
		sh := action.NewSystemHuman()
		if t := strings.TrimSpace(spec.TargetWindow); t != "" {
			sh.SetTargetTitles([]string{t})
		}
		human = sh
		src = &ctxSource{inner: ns}
		an = perception.NewVisionAnalyzer(m.router.For(llm.RolePerception), perception.GenericAnalyzer{})
		ex = we
	}
	ctlPath := filepath.Join(m.cfg.DataDir, "instances", safeDir(spec.ID), "control.json")
	sch := scheduler.New(spec.ID, spec.Name, prof.Rules, m.mem, nil, m.reg, ctlPath)
	pol := policy.New(m.router.For(llm.RoleStrategy), m.reg, policy.DefaultConfig())
	opts := engine.Options{
		ID: spec.ID, Name: spec.Name, Profile: prof, Source: src, Analyzer: an,
		Executor: ex, Human: human, Sched: sch, Policy: pol,
		Memory: m.mem.Instance(spec.ID), Bus: m.bus, Metrics: m.reg,
		Save:  func() error { return m.mem.SaveInstance(spec.ID) },
		Reset: reset,
		Config: engine.Config{
			TickMS: spec.TickMS, HumanResumeMS: spec.HumanResumeMS,
			MaxActionsSec: m.cfg.MaxActionRate, SettleMS: 120, SaveEvery: 25,
			MaxEpisodes: spec.MaxEpisodes,
		},
		Logger: m.log.With("instance", spec.ID),
	}
	runner, err := engine.New(opts)
	if err != nil {
		prof.Close()
		return err
	}
	mi := &managed{
		spec: spec, profile: prof, game: game, source: src, analyzer: an,
		executor: ex, human: mockH, sched: sch, pol: pol, runner: runner, sess: sess,
	}
	m.mu.Lock()
	m.insts[spec.ID] = mi
	m.mu.Unlock()
	return nil
}

// Create validates, persists and optionally starts a new instance.
func (m *Manager) Create(spec Spec) (InstanceView, error) {
	spec.normalize()
	if !idPattern.MatchString(spec.ID) {
		return InstanceView{}, fmt.Errorf("instance id must match %s", idPattern.String())
	}
	m.mu.RLock()
	_, exists := m.insts[spec.ID]
	count := len(m.insts)
	m.mu.RUnlock()
	if exists {
		return InstanceView{}, fmt.Errorf("instance %q already exists", spec.ID)
	}
	if count >= m.cfg.MaxInstances {
		return InstanceView{}, fmt.Errorf("instance limit %d reached", m.cfg.MaxInstances)
	}
	if err := m.build(spec); err != nil {
		return InstanceView{}, err
	}
	m.mu.Lock()
	m.specs[spec.ID] = spec
	err := m.saveRegistryLocked()
	m.mu.Unlock()
	if err != nil {
		return InstanceView{}, err
	}
	if spec.AutoStart {
		if err := m.Start(spec.ID); err != nil {
			return InstanceView{}, err
		}
	}
	m.emit("instance.created", spec.ID, map[string]any{"profile": spec.ProfileID, "source": spec.SourceKind})
	return m.Get(spec.ID)
}

func (m *Manager) get(id string) (*managed, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	mi, ok := m.insts[id]
	return mi, ok
}

// Get returns one instance view.
func (m *Manager) Get(id string) (InstanceView, error) {
	mi, ok := m.get(id)
	if !ok {
		return InstanceView{}, fmt.Errorf("instance %q not found", id)
	}
	mi.mu.Lock()
	defer mi.mu.Unlock()
	if mi.runner == nil {
		return InstanceView{}, fmt.Errorf("instance %q has no runner", id)
	}
	snap := mi.runner.Snapshot()
	return InstanceView{
		Snapshot: snap, SourceKind: mi.spec.SourceKind, Seed: mi.spec.Seed,
		MaxEpisodes: mi.spec.MaxEpisodes, CreatedAt: mi.spec.CreatedAt,
	}, nil
}

// List returns every instance, sorted by id.
func (m *Manager) List() []InstanceView {
	m.mu.RLock()
	ids := make([]string, 0, len(m.insts))
	for id := range m.insts {
		ids = append(ids, id)
	}
	m.mu.RUnlock()
	sort.Strings(ids)
	out := make([]InstanceView, 0, len(ids))
	for _, id := range ids {
		if v, err := m.Get(id); err == nil {
			out = append(out, v)
		}
	}
	return out
}

// Start restarts a stopped runner with a clean game episode while keeping the
// learned policy and both memory stores.
func (m *Manager) Start(id string) error {
	mi, ok := m.get(id)
	if !ok {
		return fmt.Errorf("instance %q not found", id)
	}
	mi.mu.Lock()
	defer mi.mu.Unlock()
	st := mi.runner.Status()
	if st == domain.StatusRunning || st == domain.StatusStarting || st == domain.StatusPaused || st == domain.StatusHuman {
		return nil
	}
	if st == domain.StatusStopped && mi.runner != nil && mi.game != nil {
		mi.game.Reset()
	}
	mi.runner.Start()
	m.emit("instance.start_requested", id, nil)
	return nil
}

func (m *Manager) Stop(id string) error {
	mi, ok := m.get(id)
	if !ok {
		return fmt.Errorf("instance %q not found", id)
	}
	mi.runner.Stop()
	return nil
}

func (m *Manager) Pause(id string) error {
	mi, ok := m.get(id)
	if !ok {
		return fmt.Errorf("instance %q not found", id)
	}
	if mi.runner.Status() == domain.StatusStopped {
		return fmt.Errorf("instance %q is stopped", id)
	}
	mi.runner.Pause()
	return nil
}

func (m *Manager) Resume(id string) error {
	mi, ok := m.get(id)
	if !ok {
		return fmt.Errorf("instance %q not found", id)
	}
	mi.runner.Resume()
	return nil
}

// Submit routes a user utterance into the instance's instruction buffer. The
// scheduler applies it at the next event boundary.
func (m *Manager) Submit(id, text string) error {
	mi, ok := m.get(id)
	if !ok {
		return fmt.Errorf("instance %q not found", id)
	}
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("empty message")
	}
	mi.runner.Submit(text)
	return nil
}

// ChatResult is the outcome of a conversational turn. Fallback is true when
// no language provider produced the reply, so the client can show a hint.
type ChatResult struct {
	Reply    string
	Provider string
	Model    string
	Fallback bool
}

const chatFallback = "语言模型暂时不可用。请到设置 → 语言模型为“语言”层配置 API Key，或下载并启动本地模型后重试。你的消息已经进入指令队列，会在下一个决策周期生效。"

// Chat submits the user's instruction to the scheduler (keeping the normal
// decision-cycle semantics) and returns a conversational reply generated by
// the language role. It never fails because of a missing or broken model.
func (m *Manager) Chat(ctx context.Context, id, text string) (string, error) {
	res, err := m.ChatDetailed(ctx, id, text)
	if err != nil {
		return "", err
	}
	return res.Reply, nil
}

// ChatDetailed is Chat with provider/model/fallback metadata for the API.
func (m *Manager) ChatDetailed(ctx context.Context, id, text string) (ChatResult, error) {
	mi, ok := m.get(id)
	if !ok {
		return ChatResult{}, fmt.Errorf("instance %q not found", id)
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return ChatResult{}, fmt.Errorf("empty message")
	}

	msgID := fmt.Sprintf("msg-%d", time.Now().UnixNano())
	mi.sched.Submit(scheduler.Message{ID: msgID, Text: text, At: time.Now()})
	m.emit("instance.user_message", id, map[string]any{"text": text})

	snap := mi.runner.Snapshot()
	system, prompt := chatPrompt(mi.spec.Name, snap, text)
	res := ChatResult{}
	var provider llm.Provider
	if m.router != nil {
		provider = m.router.For(llm.RoleLanguage)
	}
	if provider != nil && provider.Available(ctx) {
		callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		resp, err := provider.Complete(callCtx, llm.Request{
			System: system, Prompt: prompt, Temperature: 0.6, MaxTokens: 220,
		})
		cancel()
		if err != nil {
			m.log.Warn("language reply failed", "instance", id, "err", err)
		} else if reply := strings.TrimSpace(resp.Text); reply != "" {
			res.Reply = reply
			res.Provider = resp.Provider
			res.Model = resp.Model
		}
	}
	if res.Reply == "" {
		res.Fallback = true
		res.Reply = chatFallback
	}
	mi.sched.AddChat(scheduler.ChatMessage{
		ID: msgID + "-r", Role: "assistant", Text: res.Reply, Kind: "chat", At: time.Now(),
	})
	return res, nil
}

// chatPrompt describes the external-agent boundary and the few facts the
// language model may use. It must not suggest that GoPlay reads game memory.
func chatPrompt(gameName string, snap engine.Snapshot, text string) (string, string) {
	system := "你是 GoPlay 的外部游戏代理对话助手。GoPlay 通过截图与公共接口从游戏外部操作，不读取也不修改游戏进程内存。请用简短中文回答（不超过120字），结合当前游戏、最近状态与最近决策；看不到的信息不要编造。"
	var b strings.Builder
	fmt.Fprintf(&b, "游戏：%s\n", gameName)
	if s := strings.TrimSpace(snap.State.Summary); s != "" {
		fmt.Fprintf(&b, "最近状态：%s\n", chatTruncate(s, 200))
	}
	if len(snap.State.Values) > 0 {
		keys := make([]string, 0, len(snap.State.Values))
		for k := range snap.State.Values {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if len(keys) > 6 {
			keys = keys[:6]
		}
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s=%g", k, snap.State.Values[k]))
		}
		fmt.Fprintf(&b, "关键数值：%s\n", strings.Join(parts, ", "))
	}
	if kind := snap.Decision.Action.Kind; kind != "" {
		reason := strings.TrimSpace(snap.Decision.Reason)
		if reason != "" {
			fmt.Fprintf(&b, "最近决策：%s（%s）\n", kind, chatTruncate(reason, 160))
		} else {
			fmt.Fprintf(&b, "最近决策：%s\n", kind)
		}
	}
	recent := make([]scheduler.ChatMessage, 0, 4)
	for _, msg := range snap.Chat {
		if msg.Kind == "queued" || strings.TrimSpace(msg.Text) == "" {
			continue
		}
		recent = append(recent, msg)
	}
	if len(recent) > 4 {
		recent = recent[len(recent)-4:]
	}
	if len(recent) > 0 {
		b.WriteString("最近对话：\n")
		for _, msg := range recent {
			role := "用户"
			if msg.Role == "assistant" {
				role = "代理"
			}
			fmt.Fprintf(&b, "- %s：%s\n", role, chatTruncate(strings.TrimSpace(msg.Text), 120))
		}
	}
	fmt.Fprintf(&b, "用户说：%s", text)
	return system, b.String()
}

func chatTruncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func (m *Manager) AddConstraint(id string, c domain.Constraint) error {
	mi, ok := m.get(id)
	if !ok {
		return fmt.Errorf("instance %q not found", id)
	}
	if c.ID == "" {
		c.ID = fmt.Sprintf("c-%d", time.Now().UnixNano())
	}
	if c.Text == "" {
		return fmt.Errorf("constraint text is required")
	}
	c.Source = "api"
	mi.sched.AddConstraint(c)
	m.emit("instance.constraint_added", id, map[string]any{"constraint": c})
	return nil
}

func (m *Manager) Delete(id string) error {
	m.mu.Lock()
	mi, ok := m.insts[id]
	delete(m.insts, id)
	delete(m.specs, id)
	err := m.saveRegistryLocked()
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("instance %q not found", id)
	}
	mi.runner.Stop()
	mi.profile.Close()
	mi.source.Close()
	mi.executor.Close()
	if err := m.mem.DeleteInstance(id); err != nil {
		m.log.Warn("delete memory", "id", id, "err", err)
	}
	m.emit("instance.deleted", id, nil)
	return err
}

// Events returns recent bus events, optionally filtered to one instance.
func (m *Manager) Events(id string, limit int) []bus.Event {
	if limit <= 0 || limit > 2000 {
		limit = 200
	}
	all := m.bus.Recent(8192)
	out := make([]bus.Event, 0, limit)
	for _, e := range all {
		if id != "" && e.InstanceID != "" && e.InstanceID != id {
			continue
		}
		out = append(out, e)
	}
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

// GlobalPreferences returns the cross-game soft preferences.
func (m *Manager) GlobalPreferences() []domain.Preference {
	return m.mem.Global().PreferenceList()
}

func (m *Manager) AddGlobalPreference(p domain.Preference) error {
	if strings.TrimSpace(p.Text) == "" && len(p.Weights) == 0 {
		return fmt.Errorf("preference needs text or weights")
	}
	if p.ID == "" {
		p.ID = fmt.Sprintf("gp-%d", time.Now().UnixNano())
	}
	p.Source = "api"
	now := time.Now()
	if p.CreatedAt.IsZero() {
		p.CreatedAt = now
	}
	p.UpdatedAt = now
	if err := m.mem.AddGlobalPreference(p); err != nil {
		return err
	}
	m.refreshAllControls()
	m.emit("global.preference_added", "", map[string]any{"preference": p})
	return nil
}

func (m *Manager) DeleteGlobalPreference(id string) error {
	if !m.mem.Global().RemovePreference(id) {
		return fmt.Errorf("preference %q not found", id)
	}
	if err := m.mem.SaveGlobal(); err != nil {
		return err
	}
	m.refreshAllControls()
	m.emit("global.preference_removed", "", map[string]any{"id": id})
	return nil
}

func (m *Manager) refreshAllControls() {
	m.mu.RLock()
	list := make([]*managed, 0, len(m.insts))
	for _, mi := range m.insts {
		list = append(list, mi)
	}
	m.mu.RUnlock()
	for _, mi := range list {
		mi.sched.RefreshControl()
	}
}

// Recall searches one instance's layered memory.
func (m *Manager) Recall(id, query string, k int) ([]memory.Scored, error) {
	if _, ok := m.get(id); !ok {
		return nil, fmt.Errorf("instance %q not found", id)
	}
	return m.mem.Instance(id).Recall(query, k), nil
}

// Profiles returns metadata for every embedded Lua profile.
func (m *Manager) Profiles() []domain.GameProfileInfo {
	out := []domain.GameProfileInfo{}
	for _, id := range luabridge.BuiltinIDs() {
		p, err := luabridge.LoadBuiltin(id)
		if err != nil {
			continue
		}
		out = append(out, p.Info())
		p.Close()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Close stops all instances and flushes both memory stores.
func (m *Manager) Close(ctx context.Context) error {
	m.mu.RLock()
	list := make([]*managed, 0, len(m.insts))
	for _, mi := range m.insts {
		list = append(list, mi)
	}
	m.mu.RUnlock()
	for _, mi := range list {
		mi.runner.Stop()
		mi.profile.Close()
		mi.source.Close()
		mi.executor.Close()
	}
	if err := m.mem.SaveAll(); err != nil {
		return err
	}
	_ = ctx
	return nil
}

func (m *Manager) emit(typ, id string, data map[string]any) {
	m.bus.Publish(bus.Event{Type: typ, InstanceID: id, At: time.Now(), Data: data})
}

// InstanceView is the flattened JSON view used by the dashboard.
type InstanceView struct {
	engine.Snapshot
	SourceKind  string    `json:"source_kind"`
	Seed        int64     `json:"seed"`
	MaxEpisodes int       `json:"max_episodes"`
	CreatedAt   time.Time `json:"created_at"`
}

// ctxSource adapts the native capture (which has no context parameter) to the
// perception.Source interface used by the engine.
type ctxSource struct{ inner *native.ScreenSource }

func (s *ctxSource) Grab(ctx context.Context) (domain.Frame, error) {
	select {
	case <-ctx.Done():
		return domain.Frame{}, ctx.Err()
	default:
	}
	return s.inner.Grab()
}
func (s *ctxSource) Describe() string { return s.inner.Describe() }
func (s *ctxSource) Close() error     { return s.inner.Close() }

// AttachLocal makes the optional local-model runtime visible to the HTTP API.
// The runtime is owned (started and closed) by the daemon, not the manager.
func (m *Manager) AttachLocal(rt *localruntime.Runtime) {
	m.mu.Lock()
	m.local = rt
	m.mu.Unlock()
}

// LocalRuntime returns the attached on-device runtime, or nil.
func (m *Manager) LocalRuntime() *localruntime.Runtime {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.local
}

// LocalStatus returns the local runtime status when one is attached.
func (m *Manager) LocalStatus() (localruntime.Status, bool) {
	m.mu.RLock()
	rt := m.local
	m.mu.RUnlock()
	if rt == nil {
		return localruntime.Status{}, false
	}
	return rt.Status(), true
}

// AndroidSession returns the bridge session for an Android instance.
func (m *Manager) AndroidSession(id string) (*bridge.Session, bool) {
	m.mu.RLock()
	mi := m.insts[id]
	m.mu.RUnlock()
	if mi == nil || mi.sess == nil {
		return nil, false
	}
	return mi.sess, true
}
