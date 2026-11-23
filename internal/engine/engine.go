// Package engine runs one isolated game loop per instance. It is the only
// place where perception, memory, scheduling, policy and execution meet.
package engine

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"goplay/internal/action"
	"goplay/internal/bus"
	"goplay/internal/domain"
	"goplay/internal/luabridge"
	"goplay/internal/memory"
	"goplay/internal/metrics"
	"goplay/internal/perception"
	"goplay/internal/policy"
	"goplay/internal/scheduler"
)

// Config tunes one runner. Defaults are production-safe; tests shorten them.
type Config struct {
	TickMS        int
	HumanResumeMS int
	MaxActionsSec int
	SettleMS      int
	SaveEvery     int
	MaxEpisodes   int // 0 = unlimited, 1 = stop at first terminal state
}

func DefaultConfig() Config {
	return Config{TickMS: 250, HumanResumeMS: 2500, MaxActionsSec: 8, SettleMS: 120, SaveEvery: 25, MaxEpisodes: 1}
}

// Options wires one game instance. Every field except Bus/Logger is required.
type Options struct {
	ID       string
	Name     string
	Profile  *luabridge.Profile
	Source   perception.Source
	Analyzer perception.Analyzer
	Executor action.Executor
	Human    action.HumanDetector
	Sched    *scheduler.Scheduler
	Policy   *policy.Policy
	Memory   *memory.InstanceMemory
	Save     func() error
	Reset    func() error // optional: reset the game for the next episode
	Bus      *bus.Bus
	Metrics  *metrics.Registry
	Config   Config
	Logger   *slog.Logger
}

// EpisodeResult is one finished attempt. It is the raw material for the
// convergence metric ("attempts to reach X% progress").
type EpisodeResult struct {
	Index      int64     `json:"index"`
	Won        bool      `json:"won"`
	Progress   float64   `json:"progress"`
	Actions    int64     `json:"actions"`
	Reward     float64   `json:"reward"`
	DurationMS int64     `json:"duration_ms"`
	At         time.Time `json:"at"`
}

type Runner struct {
	opts Options
	cfg  Config
	log  *slog.Logger
	lim  *action.Limiter

	mu           sync.Mutex
	status       domain.InstanceStatus
	paused       bool
	stopReq      bool
	started      bool
	startedAt    time.Time
	stats        domain.InstanceStats
	lastState    domain.State
	lastDec      policy.Decision
	lastReward   float64
	human        bool
	humanSince   time.Time
	humanAt      time.Time
	lastActions  []domain.Action
	episodesList []EpisodeResult
	epStart      time.Time
	epActions    int64
	epReward     float64

	cancel context.CancelFunc
	done   chan struct{}
	saves  int
}

func New(opts Options) (*Runner, error) {
	if opts.ID == "" || opts.Profile == nil || opts.Source == nil || opts.Analyzer == nil ||
		opts.Executor == nil || opts.Human == nil || opts.Sched == nil || opts.Policy == nil || opts.Memory == nil || opts.Metrics == nil {
		return nil, fmt.Errorf("engine: missing required option for instance %q", opts.ID)
	}
	cfg := opts.Config
	def := DefaultConfig()
	if cfg.TickMS <= 0 {
		cfg.TickMS = def.TickMS
	}
	if cfg.HumanResumeMS <= 0 {
		cfg.HumanResumeMS = def.HumanResumeMS
	}
	if cfg.MaxActionsSec <= 0 {
		cfg.MaxActionsSec = def.MaxActionsSec
	}
	if cfg.SettleMS < 0 {
		cfg.SettleMS = def.SettleMS
	}
	if cfg.SaveEvery <= 0 {
		cfg.SaveEvery = def.SaveEvery
	}
	if cfg.MaxEpisodes < 1 {
		cfg.MaxEpisodes = 1
	}
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Runner{
		opts: opts, cfg: cfg, log: log,
		lim:     action.NewLimiter(cfg.MaxActionsSec),
		status:  domain.StatusStopped,
		done:    make(chan struct{}),
		epStart: time.Now(),
	}, nil
}

func (r *Runner) ID() string { return r.opts.ID }

func (r *Runner) Start() {
	r.mu.Lock()
	if r.started {
		r.mu.Unlock()
		return
	}
	r.started = true
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.status = domain.StatusStarting
	r.startedAt = time.Now()
	r.stats.StartedAt = r.startedAt
	r.mu.Unlock()

	r.emit("instance.started", map[string]any{"profile": r.opts.Profile.ID})
	go func() {
		defer close(r.done)
		defer r.finish()
		r.loop(ctx)
	}()
}

func (r *Runner) Stop() {
	r.mu.Lock()
	cancel := r.cancel
	started := r.started
	r.stopReq = true
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if started {
		<-r.done
	}
}

// Submit queues a user message. It is only applied at the next event boundary.
func (r *Runner) Submit(text string) {
	r.opts.Sched.Submit(scheduler.Message{Text: text, At: time.Now()})
	r.emit("instance.user_message", map[string]any{"text": text})
}

func (r *Runner) Pause() {
	r.mu.Lock()
	r.paused = true
	if r.status == domain.StatusRunning {
		r.status = domain.StatusPaused
	}
	r.mu.Unlock()
	r.emit("instance.paused", nil)
}

func (r *Runner) Resume() {
	r.mu.Lock()
	r.paused = false
	if r.status == domain.StatusPaused {
		r.status = domain.StatusRunning
	}
	r.mu.Unlock()
	r.emit("instance.resumed", nil)
}

func (r *Runner) Status() domain.InstanceStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status
}

// Snapshot is the JSON-safe view used by the HTTP API and overlay.
type Snapshot struct {
	ID       string                  `json:"id"`
	Name     string                  `json:"name"`
	Profile  string                  `json:"profile"`
	Status   domain.InstanceStatus   `json:"status"`
	Stats    domain.InstanceStats    `json:"stats"`
	State    domain.State            `json:"state"`
	Control  domain.Control          `json:"control"`
	Decision DecisionView            `json:"decision"`
	Policy   policy.PolicyStats      `json:"policy"`
	Paused   bool                    `json:"paused"`
	Human    bool                    `json:"human_control"`
	Memory   memory.Stats            `json:"memory"`
	Episodes []EpisodeResult         `json:"episodes,omitempty"`
	Chat     []scheduler.ChatMessage `json:"chat,omitempty"`
}

type DecisionView struct {
	Action    domain.Action `json:"action"`
	Score     float64       `json:"score"`
	Source    string        `json:"source"`
	Provider  string        `json:"provider,omitempty"`
	Model     string        `json:"model,omitempty"`
	Reason    string        `json:"reason,omitempty"`
	StateKey  string        `json:"state_key,omitempty"`
	ActionKey string        `json:"action_key,omitempty"`
	LatencyMS float64       `json:"latency_ms,omitempty"`
	Fallback  bool          `json:"fallback,omitempty"`
	LLMUsed   bool          `json:"llm_used,omitempty"`
}

func (r *Runner) Snapshot() Snapshot {
	r.mu.Lock()
	s := Snapshot{
		ID: r.opts.ID, Name: r.opts.Name, Profile: r.opts.Profile.ID,
		Status: r.status, Stats: r.stats, State: r.lastState,
		Decision: DecisionView{
			Action: r.lastDec.Action, Score: r.lastDec.Score, Source: r.lastDec.Source,
			Reason: r.lastDec.Reason, StateKey: r.lastDec.StateKey, ActionKey: r.lastDec.ActionKey,
			LatencyMS: r.lastDec.LatencyMS, Fallback: r.lastDec.Fallback, LLMUsed: r.lastDec.LLMUsed,
			Provider: r.lastDec.Provider, Model: r.lastDec.Model,
		},
		Paused: r.paused, Human: r.human,
		Control:  r.opts.Sched.Control(),
		Episodes: append([]EpisodeResult(nil), r.episodesList...),
	}
	r.mu.Unlock()
	s.Policy = r.opts.Policy.Stats()
	s.Memory = r.opts.Memory.StatsSnapshot()
	s.Chat = r.opts.Sched.ChatLog()
	if len(s.Chat) > 40 {
		s.Chat = s.Chat[len(s.Chat)-40:]
	}
	return s
}

func (r *Runner) loop(ctx context.Context) {
	r.mu.Lock()
	if !r.paused {
		r.status = domain.StatusRunning
	}
	r.mu.Unlock()

	t := time.NewTicker(time.Duration(r.cfg.TickMS) * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		r.tick(ctx)
		r.mu.Lock()
		stop := r.stopReq
		r.mu.Unlock()
		if stop {
			return
		}
	}
}

func (r *Runner) tick(ctx context.Context) {
	now := time.Now()
	hs := r.opts.Human.State(ctx)
	if hs.Active {
		r.markHuman(hs, now)
		r.drain(scheduler.BoundaryHumanTakeover, now)
		return
	}
	r.mu.Lock()
	wasHuman := r.human
	lastHuman := r.humanAt
	r.mu.Unlock()
	if wasHuman {
		if now.Sub(lastHuman) < time.Duration(r.cfg.HumanResumeMS)*time.Millisecond {
			return
		}
		r.mu.Lock()
		r.human = false
		if r.status == domain.StatusHuman {
			r.status = domain.StatusRunning
		}
		r.mu.Unlock()
		r.emit("instance.human_released", nil)
	}

	r.mu.Lock()
	paused := r.paused
	r.mu.Unlock()
	if paused {
		r.drain(scheduler.BoundaryIdle, now)
		return
	}
	if err := r.step(ctx); err != nil {
		r.fail(err)
	}
}

func (r *Runner) markHuman(hs action.HumanState, now time.Time) {
	r.mu.Lock()
	first := !r.human
	r.human = true
	r.humanAt = now
	if first {
		r.status = domain.StatusHuman
		r.stats.HumanTakeovers++
		if hs.Since.IsZero() {
			r.humanSince = now
		} else {
			r.humanSince = hs.Since
		}
	}
	r.mu.Unlock()
	if !first {
		return
	}
	r.opts.Metrics.Inc(metrics.HumanTakeoverTotal, 1)
	if !hs.Since.IsZero() {
		if d := now.Sub(hs.Since); d >= 0 && d < 5*time.Second {
			r.opts.Metrics.Observe(metrics.HandoverLatencyMS, float64(d.Milliseconds()))
		}
	}
	r.emit("instance.human_takeover", map[string]any{"reason": hs.Reason})
}

func (r *Runner) drain(kind scheduler.BoundaryKind, now time.Time) scheduler.BoundaryResult {
	r.mu.Lock()
	acts := append([]domain.Action(nil), r.lastActions...)
	r.mu.Unlock()
	cc := classifyContext(r.opts.ID, r.opts.Name, acts)
	cc.Now = now
	res := r.opts.Sched.Boundary(kind, cc)
	r.apply(res)
	return res
}

func (r *Runner) step(ctx context.Context) error {
	start := time.Now()
	frame, err := r.opts.Source.Grab(ctx)
	if err != nil {
		return fmt.Errorf("capture: %w", err)
	}
	st, world, err := r.opts.Analyzer.Analyze(frame)
	if err != nil {
		return fmt.Errorf("analyze: %w", err)
	}
	r.opts.Metrics.Observe(metrics.PerceptionLatencyMS, float64(time.Since(start).Milliseconds()))

	// Lua profile turns raw perception into semantic state and legal actions.
	pst, err := r.opts.Profile.Observe(world, st)
	if err != nil {
		return fmt.Errorf("profile observe: %w", err)
	}
	acts, err := r.opts.Profile.Actions(world, pst)
	if err != nil {
		return fmt.Errorf("profile actions: %w", err)
	}
	r.mu.Lock()
	r.lastActions = append([]domain.Action(nil), acts...)
	r.mu.Unlock()

	cc := classifyContext(r.opts.ID, r.opts.Name, acts)
	res := r.opts.Sched.Boundary(scheduler.BoundaryBeforeDecision, cc)
	r.apply(res)

	r.mu.Lock()
	paused, stop := r.paused, r.stopReq
	r.mu.Unlock()
	if stop {
		return nil
	}
	if paused {
		return nil
	}

	// Terminal states are recorded before the next decision.
	if won, dead := terminal(pst, world); won || dead {
		r.mu.Lock()
		r.lastState = pst
		if won {
			r.stats.Progress = 1
			r.stats.BestProgress = 1
		} else if r.stats.Progress > r.stats.BestProgress {
			r.stats.BestProgress = r.stats.Progress
		}
		r.mu.Unlock()
		r.terminal(won)
		return nil
	}

	ctl := res.Control
	dec := r.opts.Policy.Decide(ctx, policy.Input{
		State: pst, Actions: acts, Control: ctl, Rules: r.opts.Profile.Rules, Hints: ctl.Hints,
	})
	r.opts.Metrics.Observe(metrics.DecisionLatencyMS, dec.LatencyMS)
	if dec.Fallback {
		r.opts.Metrics.Inc(metrics.PolicyFallbackTotal, 1)
	}
	r.mu.Lock()
	r.stats.Decisions++
	r.stats.LastDecisionAt = time.Now()
	r.lastState = pst
	r.lastDec = dec
	r.mu.Unlock()

	// Re-check human control immediately before acting. A user touching the
	// keyboard must never race an injected action.
	if hs := r.opts.Human.State(ctx); hs.Active {
		r.markHuman(hs, time.Now())
		return nil
	}
	if err := r.lim.Wait(ctx); err != nil {
		return err
	}
	if r.cfg.SettleMS > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(r.cfg.SettleMS) * time.Millisecond):
		}
	}
	actStart := time.Now()
	if err := r.opts.Executor.Do(ctx, dec.Action); err != nil {
		if err == action.ErrHumanControl {
			r.markHuman(r.opts.Human.State(ctx), time.Now())
			return nil
		}
		return fmt.Errorf("execute %q: %w", dec.Action.Key(), err)
	}
	lat := float64(time.Since(actStart).Milliseconds())
	r.opts.Metrics.Observe(metrics.ActionLatencyMS, lat)
	r.opts.Metrics.Inc(metrics.ActionsTotal, 1)
	r.mu.Lock()
	r.stats.Actions++
	r.stats.LastAction = dec.Action.Key()
	r.mu.Unlock()
	r.emit("instance.action", map[string]any{"action": dec.Action, "source": dec.Source, "latency_ms": lat})

	// Observe the aftermath through the same pixel pipeline.
	after, err := r.opts.Source.Grab(ctx)
	if err != nil {
		return fmt.Errorf("capture after action: %w", err)
	}
	st2, world2, err := r.opts.Analyzer.Analyze(after)
	if err != nil {
		return fmt.Errorf("analyze after action: %w", err)
	}
	interp, err := r.opts.Profile.Interpret(world, world2)
	if err != nil {
		return fmt.Errorf("profile interpret: %w", err)
	}
	r.opts.Policy.Update(dec, interp.Reward)

	r.mu.Lock()
	r.stats.Reward += interp.Reward
	if p := progressOf(st2, world2); p > r.stats.Progress {
		r.stats.Progress = p
	}
	if r.stats.Progress > r.stats.BestProgress {
		r.stats.BestProgress = r.stats.Progress
	}
	r.lastReward = interp.Reward
	r.mu.Unlock()

	r.remember(st, dec, interp, lat)
	r.opts.Metrics.Set(metrics.MemoryEntriesGauge, float64(r.opts.Memory.Len()))
	r.opts.Metrics.Set(metrics.MemoryBytesGauge, float64(r.opts.Memory.Bytes()))
	r.saves++
	if r.saves%r.cfg.SaveEvery == 0 {
		if err := r.save(); err != nil {
			r.log.Warn("save memory", "instance", r.opts.ID, "err", err)
		}
	}
	if interp.Terminal {
		r.terminal(interp.Won)
	}
	return nil
}

func (r *Runner) remember(st domain.State, dec policy.Decision, interp luabridge.Interpretation, latencyMS float64) {
	tags := []string{"action", dec.Action.Kind}
	tags = append(tags, dec.Action.Tags...)
	importance := 0.35
	if interp.Terminal {
		importance = 1
	} else if interp.Reward >= 5 || interp.Reward <= -3 {
		importance = 0.8
	}
	text := fmt.Sprintf("state=%s action=%s target=%s reward=%.2f source=%s", st.Summary, dec.Action.Kind, dec.Action.Target, interp.Reward, dec.Source)
	entry := memory.Entry{
		Tier: memory.TierWorking, Kind: memory.KindDecision, Text: text,
		Tags: tags, Values: map[string]float64{"reward": interp.Reward, "latency_ms": latencyMS},
		GameID: r.opts.ID, CreatedAt: time.Now(), Importance: importance, Source: "engine",
	}
	for k, v := range st.Values {
		if k == "hp" || k == "room" || k == "progress" {
			entry.Values[k] = v
		}
	}
	r.opts.Memory.Add(entry)
}

func (r *Runner) terminal(won bool) {
	now := time.Now()
	r.mu.Lock()
	progress := r.stats.Progress
	r.stats.Episodes++
	if won {
		r.stats.Wins++
	} else {
		r.stats.Losses++
	}
	ep := EpisodeResult{
		Index: r.stats.Episodes, Won: won, Progress: progress,
		Actions: r.stats.Actions - r.epActions, Reward: r.stats.Reward - r.epReward,
		DurationMS: now.Sub(r.epStart).Milliseconds(), At: now,
	}
	r.episodesList = append(r.episodesList, ep)
	if len(r.episodesList) > 200 {
		r.episodesList = r.episodesList[len(r.episodesList)-200:]
	}
	r.epActions = r.stats.Actions
	r.epReward = r.stats.Reward
	r.epStart = now
	r.mu.Unlock()
	if won {
		r.opts.Metrics.Inc("episodes_won_total", 1)
	} else {
		r.opts.Metrics.Inc("episodes_lost_total", 1)
	}
	r.opts.Metrics.Inc("episodes_total", 1)
	r.emit("instance.terminal", map[string]any{"won": won, "progress": progress, "episode": ep})

	r.opts.Memory.Add(memory.Entry{
		Tier: memory.TierFact, Kind: memory.KindSummary, GameID: r.opts.ID,
		Text: fmt.Sprintf("episode finished won=%v best_progress=%.2f actions=%d", won, r.stats.BestProgress, r.stats.Actions),
		Tags: []string{"episode", fmt.Sprintf("won_%v", won)}, Importance: 0.9, CreatedAt: time.Now(), Source: "engine",
	})
	if err := r.save(); err != nil {
		r.log.Warn("save terminal memory", "instance", r.opts.ID, "err", err)
	}

	if r.opts.Reset != nil && r.cfg.MaxEpisodes > 1 && r.stats.Episodes < int64(r.cfg.MaxEpisodes) {
		if err := r.opts.Reset(); err != nil {
			r.fail(fmt.Errorf("reset episode: %w", err))
			return
		}
		r.mu.Lock()
		r.stats.Progress = 0
		r.lastActions = nil
		r.lastState = domain.State{}
		r.mu.Unlock()
		r.emit("instance.episode_started", map[string]any{"episode": r.stats.Episodes + 1})
		return
	}
	r.mu.Lock()
	r.status = domain.StatusStopped
	r.stopReq = true
	r.mu.Unlock()
}

func (r *Runner) apply(res scheduler.BoundaryResult) {
	r.mu.Lock()
	for _, in := range res.Intents {
		switch in.Kind {
		case scheduler.IntentConstraint:
			if in.Constraint != nil {
				r.stats.Constraints++
			}
		case scheduler.IntentPreference:
			r.stats.Preferences++
		}
	}
	for _, cmd := range res.Commands {
		switch cmd {
		case "pause":
			r.paused = true
			if r.status == domain.StatusRunning {
				r.status = domain.StatusPaused
			}
		case "resume":
			r.paused = false
			if r.status == domain.StatusPaused {
				r.status = domain.StatusRunning
			}
		case "stop":
			r.stopReq = true
		}
	}
	paused, stop := r.paused, r.stopReq
	r.mu.Unlock()
	if paused {
		r.emit("instance.paused", map[string]any{"source": "user"})
	}
	if stop {
		r.emit("instance.stop_requested", nil)
	}
}

func (r *Runner) fail(err error) {
	if r.status == domain.StatusStopped {
		return
	}
	r.opts.Metrics.Inc(metrics.InstanceErrorsTotal, 1)
	r.mu.Lock()
	r.stats.Errors++
	r.stats.LastError = err.Error()
	r.status = domain.StatusError
	r.mu.Unlock()
	r.log.Error("instance error", "instance", r.opts.ID, "err", err)
	r.emit("instance.error", map[string]any{"error": err.Error()})
}

func (r *Runner) save() error {
	if r.opts.Save == nil {
		return nil
	}
	return r.opts.Save()
}

func (r *Runner) finish() {
	r.mu.Lock()
	if r.status != domain.StatusError {
		r.status = domain.StatusStopped
	}
	r.mu.Unlock()
	if err := r.save(); err != nil {
		r.log.Warn("save memory at shutdown", "instance", r.opts.ID, "err", err)
	}
	r.emit("instance.stopped", nil)
}

func (r *Runner) emit(kind string, data map[string]any) {
	if r.opts.Bus == nil {
		return
	}
	r.opts.Bus.Publish(bus.Event{Type: kind, InstanceID: r.opts.ID, Message: kind, Data: data})
}

func classifyContext(id, name string, acts []domain.Action) scheduler.ClassifyContext {
	cc := scheduler.ClassifyContext{InstanceID: id, GameName: name}
	seenKind := map[string]bool{}
	seenTarget := map[string]bool{}
	seenTag := map[string]bool{}
	for _, a := range acts {
		if a.Kind != "" && !seenKind[a.Kind] {
			seenKind[a.Kind] = true
			cc.KnownActions = append(cc.KnownActions, a.Kind)
		}
		if a.Target != "" && !seenTarget[a.Target] {
			seenTarget[a.Target] = true
			cc.KnownTargets = append(cc.KnownTargets, a.Target)
		}
		for _, t := range a.Tags {
			if t != "" && !seenTag[t] {
				seenTag[t] = true
				cc.KnownTags = append(cc.KnownTags, t)
			}
		}
	}
	sort.Strings(cc.KnownActions)
	sort.Strings(cc.KnownTargets)
	sort.Strings(cc.KnownTags)
	return cc
}

func terminal(st domain.State, world map[string]any) (won, dead bool) {
	for _, k := range []string{"won", "win", "victory"} {
		if st.Value(k) > 0.5 || truthy(world[k]) {
			won = true
		}
	}
	for _, k := range []string{"dead", "lost", "game_over"} {
		if st.Value(k) > 0.5 || truthy(world[k]) {
			dead = true
		}
	}
	if st.TextField("phase") == "won" {
		won = true
	}
	if st.TextField("phase") == "dead" {
		dead = true
	}
	return won, dead
}

func truthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case float64:
		return x > 0.5
	case int:
		return x > 0
	case string:
		return x == "true" || x == "1"
	}
	return false
}

func progressOf(st domain.State, world map[string]any) float64 {
	if p := st.Value("progress"); p > 0 {
		if p > 1 {
			return 1
		}
		return p
	}
	num := func(k string) float64 {
		if v := st.Value(k); v > 0 {
			return v
		}
		switch v := world[k].(type) {
		case float64:
			return v
		case int:
			return float64(v)
		}
		return 0
	}
	room, maxRooms := num("room"), num("max_rooms")
	if maxRooms <= 0 {
		maxRooms = 10
	}
	if room <= 0 {
		return 0
	}
	p := (room - 1) / maxRooms
	if em := num("enemy_max_hp"); em > 0 {
		p += (1 - num("enemy_hp")/em) / maxRooms
	}
	if p < 0 {
		return 0
	}
	if p > 0.999 {
		p = 0.999 // only an explicit win reaches 1.0
	}
	return p
}
