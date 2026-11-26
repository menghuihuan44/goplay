// Package engine runs one isolated game loop per instance. It is the only
// place where perception, memory, scheduling, policy and execution meet.
package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"log/slog"
	"math"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
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
	"goplay/internal/teach"
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
	ID   string
	Name string
	// GameName/Package describe what the user launched. They are context for
	// the prior-knowledge layer and the policy prompt, never hard-coded keys.
	GameName    string
	GamePackage string
	GamePrior   string
	Profile     *luabridge.Profile
	Source      perception.Source
	Analyzer    perception.Analyzer
	Executor    action.Executor
	Human       action.HumanDetector
	Sched       *scheduler.Scheduler
	Policy      *policy.Policy
	Memory      *memory.InstanceMemory
	// Teacher records human demonstrations and proposes learned actions. It is
	// optional so tests and headless instances can run without a store.
	Teacher *teach.Recorder
	Save    func() error
	Reset   func() error // optional: reset the game for the next episode
	Bus     *bus.Bus
	Metrics *metrics.Registry
	Config  Config
	Logger  *slog.Logger
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
	waiting      bool
	waitKind     string
	waitReason   string
	waitSince    time.Time
	lastRecover  time.Time
	lastActions  []domain.Action
	lastActAt    time.Time
	episodesList []EpisodeResult
	traceSeq     int64
	trace        []TraceEvent
	epStart      time.Time
	epActions    int64
	epReward     float64
	gamePrior    string
	gamePriorAt  time.Time

	cancel context.CancelFunc
	done   chan struct{}
	saves  int

	frameMu        sync.Mutex
	frameJPEG      []byte
	frameAt        time.Time
	frameW         int
	frameH         int
	frameCh        chan domain.Frame
	lastFrameOffer atomic.Int64
	frameWG        sync.WaitGroup
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
		lim:         action.NewLimiter(cfg.MaxActionsSec),
		status:      domain.StatusStopped,
		done:        make(chan struct{}),
		frameCh:     make(chan domain.Frame, 1),
		epStart:     time.Now(),
		gamePrior:   opts.GamePrior,
		gamePriorAt: time.Now(),
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
	r.stopReq = false
	r.done = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.status = domain.StatusStarting
	r.startedAt = time.Now()
	r.stats.StartedAt = r.startedAt
	r.mu.Unlock()

	r.emit("instance.started", map[string]any{"profile": r.opts.Profile.ID})
	r.narrate(fmt.Sprintf("我开始玩「%s」了。我会先看画面，边玩边学。", r.opts.Name))
	r.frameWG.Add(1)
	go func() {
		defer r.frameWG.Done()
		r.frameLoop(ctx)
	}()
	go func() {
		// Deferred in reverse: finish first, then clear started, and only then
		// close done. A caller that waits for done can restart immediately
		// without racing either the old status write or the started flag.
		defer close(r.done)
		defer func() {
			r.mu.Lock()
			r.started = false
			r.mu.Unlock()
		}()
		defer r.finish()
		r.loop(ctx)
	}()
}

func (r *Runner) Stop() {
	r.mu.Lock()
	cancel := r.cancel
	started := r.started
	done := r.done
	r.stopReq = true
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if started {
		<-done
		r.frameWG.Wait()
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
	wasHuman := r.human
	if wasHuman {
		// An explicit resume (including the release endpoint) is a forced
		// hand-back: it clears human control immediately. If the client simply
		// stops reporting, tick still uses the 2.5s HumanResumeMS window.
		r.human = false
		r.humanAt = time.Time{}
		r.humanSince = time.Time{}
	}
	if r.status == domain.StatusPaused || r.status == domain.StatusHuman {
		r.status = domain.StatusRunning
	}
	r.mu.Unlock()
	if r.opts.Teacher != nil {
		r.opts.Teacher.End(time.Now())
	}
	r.emit("instance.resumed", nil)
	if wasHuman {
		r.emit("instance.human_released", map[string]any{"reason": "user-released"})
	}
}

// SetGamePrior installs a soft, model-generated hypothesis about the game
// (genre, likely goals, what to watch). It is context for the policy prompt,
// never a list of hard-coded keys. The trace records when it arrived so the UI
// can distinguish "learned some context" from "starting from zero".
func (r *Runner) SetGamePrior(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	r.mu.Lock()
	if text == r.gamePrior {
		r.mu.Unlock()
		return
	}
	r.gamePrior = text
	r.gamePriorAt = time.Now()
	r.mu.Unlock()
	r.traceEvent("prior", "获得游戏先验知识（软提示，不写死按键）", map[string]any{"text": text})
}

// trace appends one visible thought-chain event. It is called from the single
// decision loop, but also from event handlers, so it takes the lock.
func (r *Runner) traceEvent(phase, msg string, data map[string]any) {
	r.mu.Lock()
	r.traceSeq++
	ev := TraceEvent{Seq: r.traceSeq, At: time.Now(), Phase: phase, Message: msg, Data: data}
	r.trace = append(r.trace, ev)
	if len(r.trace) > 60 {
		r.trace = r.trace[len(r.trace)-60:]
	}
	r.mu.Unlock()
	r.emit("instance.trace", map[string]any{"seq": ev.Seq, "phase": phase, "message": msg, "data": data})
}

func (r *Runner) Status() domain.InstanceStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status
}

// Snapshot is the JSON-safe view used by the HTTP API and overlay.
type Snapshot struct {
	ID       string                `json:"id"`
	Name     string                `json:"name"`
	Profile  string                `json:"profile"`
	Status   domain.InstanceStatus `json:"status"`
	Stats    domain.InstanceStats  `json:"stats"`
	State    domain.State          `json:"state"`
	Display  domain.DisplayView    `json:"display"`
	Control  domain.Control        `json:"control"`
	Decision DecisionView          `json:"decision"`
	Policy   policy.PolicyStats    `json:"policy"`
	Paused   bool                  `json:"paused"`
	Human    bool                  `json:"human_control"`
	// Waiting means the screen pipeline is temporarily unavailable (phone
	// rotated, permission dialog, WebSocket reconnect). It is not an error and
	// the instance keeps running while retrying.
	Waiting     bool      `json:"waiting"`
	WaitReason  string    `json:"wait_reason,omitempty"`
	WaitSince   time.Time `json:"wait_since,omitempty"`
	FrameAt     time.Time `json:"frame_at,omitempty"`
	FrameWidth  int       `json:"frame_width,omitempty"`
	FrameHeight int       `json:"frame_height,omitempty"`
	// FrameJPEG is the latest encoded frame for GET /frame. It is intentionally
	// excluded from JSON so periodic snapshots stay small.
	FrameJPEG []byte                  `json:"-"`
	Memory    memory.Stats            `json:"memory"`
	Teaching  teach.Status            `json:"teaching"`
	Episodes  []EpisodeResult         `json:"episodes,omitempty"`
	Chat      []scheduler.ChatMessage `json:"chat,omitempty"`
	// Trace is the agent's visible thought chain: what it saw, what it chose,
	// what it pressed, and what changed. It is capped and safe to show in the UI.
	Trace []TraceEvent `json:"trace,omitempty"`
	// GamePrior is the model-generated, soft hypothesis about this game. It is
	// shown in the UI so the user can tell "learned genre" from "no idea yet".
	GamePrior string `json:"game_prior,omitempty"`
}

type TraceEvent struct {
	Seq     int64          `json:"seq"`
	At      time.Time      `json:"at"`
	Phase   string         `json:"phase"`
	Message string         `json:"message"`
	Data    map[string]any `json:"data,omitempty"`
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
	Demo      bool          `json:"demo,omitempty"`
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
			Provider: r.lastDec.Provider, Model: r.lastDec.Model, Demo: r.lastDec.Demo,
		},
		Paused: r.paused, Human: r.human,
		Waiting: r.waiting, WaitReason: r.waitReason, WaitSince: r.waitSince,
		Control:   r.opts.Sched.Control(),
		Episodes:  append([]EpisodeResult(nil), r.episodesList...),
		Trace:     append([]TraceEvent(nil), r.trace...),
		GamePrior: r.gamePrior,
	}
	r.mu.Unlock()
	s.Display = domain.BuildDisplay(s.State, s.Stats, s.Stats.LastAction, s.Decision.Reason)
	r.frameMu.Lock()
	s.FrameAt = r.frameAt
	s.FrameWidth = r.frameW
	s.FrameHeight = r.frameH
	s.FrameJPEG = r.frameJPEG
	r.frameMu.Unlock()
	s.Policy = r.opts.Policy.Stats()
	s.Memory = r.opts.Memory.StatsSnapshot()
	if r.opts.Teacher != nil {
		s.Teaching = r.opts.Teacher.Status()
	}
	s.Chat = r.opts.Sched.ChatLog()
	if len(s.Chat) > 40 {
		s.Chat = s.Chat[len(s.Chat)-40:]
	}
	return s
}

const (
	frameMaxEdge     = 960
	frameJPEGQuality = 70
	frameMinInterval = time.Second
)

// offerFrame hands a freshly captured frame to the encoder without blocking the
// decision loop. Frames are limited to one per second; if the encoder is still
// busy the intermediate frame is dropped.
func (r *Runner) offerFrame(f domain.Frame) {
	if f.Width <= 0 || f.Height <= 0 || len(f.RGBA) < f.Width*f.Height*4 {
		return
	}
	now := time.Now()
	prev := r.lastFrameOffer.Load()
	if prev != 0 && now.Sub(time.Unix(0, prev)) < frameMinInterval {
		return
	}
	if !r.lastFrameOffer.CompareAndSwap(prev, now.UnixNano()) {
		return
	}
	select {
	case r.frameCh <- f:
	default:
	}
}

func (r *Runner) frameLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case f := <-r.frameCh:
			jpg, w, h, at, err := encodeFrameJPEG(f)
			if err != nil {
				r.log.Debug("frame encode skipped", "instance", r.opts.ID, "err", err)
				continue
			}
			r.frameMu.Lock()
			r.frameJPEG = jpg
			r.frameAt = at
			r.frameW = w
			r.frameH = h
			r.frameMu.Unlock()
		}
	}
}

// LastFrameJPEG returns a private copy of the last encoded frame and its capture
// time. It is safe to call from any goroutine.
func (r *Runner) LastFrameJPEG() ([]byte, time.Time) {
	r.frameMu.Lock()
	defer r.frameMu.Unlock()
	return append([]byte(nil), r.frameJPEG...), r.frameAt
}

// FrameInfo describes the latest frame for diagnostics without exposing the
// JPEG bytes. age_ms is -1 until the first frame is encoded.
func (r *Runner) FrameInfo() map[string]any {
	r.frameMu.Lock()
	defer r.frameMu.Unlock()
	ageMS := int64(-1)
	if !r.frameAt.IsZero() {
		ageMS = time.Since(r.frameAt).Milliseconds()
		if ageMS < 0 {
			ageMS = 0
		}
	}
	return map[string]any{
		"available": len(r.frameJPEG) > 0,
		"at":        r.frameAt,
		"age_ms":    ageMS,
		"width":     r.frameW,
		"height":    r.frameH,
		"bytes":     len(r.frameJPEG),
	}
}

func encodeFrameJPEG(f domain.Frame) ([]byte, int, int, time.Time, error) {
	need := f.Width * f.Height * 4
	if f.Width <= 0 || f.Height <= 0 || len(f.RGBA) < need {
		return nil, 0, 0, time.Time{}, fmt.Errorf("frame %dx%d has %d RGBA bytes, need %d", f.Width, f.Height, len(f.RGBA), need)
	}
	at := f.At
	if at.IsZero() {
		at = time.Now()
	}
	src := &image.RGBA{Pix: f.RGBA[:need], Stride: f.Width * 4, Rect: image.Rect(0, 0, f.Width, f.Height)}
	dst := image.Image(src)
	w, h := f.Width, f.Height
	if f.Width > frameMaxEdge || f.Height > frameMaxEdge {
		w, h = scaledDimensions(f.Width, f.Height)
		scaled := image.NewRGBA(image.Rect(0, 0, w, h))
		scaleNearest(src, scaled)
		dst = scaled
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: frameJPEGQuality}); err != nil {
		return nil, 0, 0, time.Time{}, fmt.Errorf("encode JPEG: %w", err)
	}
	return buf.Bytes(), w, h, at, nil
}

func scaledDimensions(w, h int) (int, int) {
	if w <= frameMaxEdge && h <= frameMaxEdge {
		return w, h
	}
	if w >= h {
		nh := h * frameMaxEdge / w
		if nh < 1 {
			nh = 1
		}
		return frameMaxEdge, nh
	}
	nw := w * frameMaxEdge / h
	if nw < 1 {
		nw = 1
	}
	return nw, frameMaxEdge
}

func scaleNearest(src *image.RGBA, dst *image.RGBA) {
	sw, sh := src.Rect.Dx(), src.Rect.Dy()
	dw, dh := dst.Rect.Dx(), dst.Rect.Dy()
	for y := 0; y < dh; y++ {
		sy := y * sh / dh
		srcOff := sy * src.Stride
		dstOff := y * dst.Stride
		for x := 0; x < dw; x++ {
			sx := x * sw / dw
			copy(dst.Pix[dstOff+x*4:dstOff+x*4+4], src.Pix[srcOff+sx*4:srcOff+sx*4+4])
		}
	}
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
		r.observeHuman(ctx, now)
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
		if r.opts.Teacher != nil {
			r.opts.Teacher.End(now)
		}
		r.emit("instance.human_released", nil)
	}

	r.mu.Lock()
	paused := r.paused
	r.mu.Unlock()
	if paused {
		r.drain(scheduler.BoundaryIdle, now)
		return
	}
	r.maybeRecover()
	if err := r.step(ctx); err != nil {
		// A deliberate stop cancels the runner context. That is not a game or
		// action failure, so it must not turn the instance card into an error.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		r.fail(err)
	}
}

// narrate publishes a short, factual self-talk line. The Android client speaks it
// through TTS even when no voice conversation is active, and the web console
// shows it in the event feed.
func (r *Runner) narrate(text string) {
	r.emit("instance.narration", map[string]any{"text": text})
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

// observeHuman feeds a human-control tick to the teaching recorder. It drains
// raw input events even when no recorder is attached so the detector buffer
// cannot grow during a long takeover.
func (r *Runner) observeHuman(ctx context.Context, now time.Time) {
	var events []action.InputEvent
	if obs, ok := r.opts.Human.(action.InputObserver); ok {
		events = obs.DrainInputEvents()
	}
	if r.opts.Teacher == nil {
		return
	}
	frame, err := r.opts.Source.Grab(ctx)
	if err != nil {
		return
	}
	r.offerFrame(frame)
	r.opts.Teacher.Observe(frame, events, true, now)
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

// noteWaiting converts a transient screen/bridge failure into a visible
// waiting state instead of a terminal error. It reports whether the error was
// transient so callers can return without marking the instance failed.
func (r *Runner) noteWaiting(err error) bool {
	transient := errors.Is(err, domain.ErrTransient)
	unfocused := errors.Is(err, action.ErrTargetNotFocused)
	if !transient && !unfocused {
		return false
	}
	reason := err.Error()
	reason = strings.TrimPrefix(reason, domain.ErrTransient.Error()+": ")
	reason = strings.TrimSuffix(reason, ": "+domain.ErrTransient.Error())
	reason = strings.TrimPrefix(reason, action.ErrTargetNotFocused.Error()+": ")
	now := time.Now()
	r.mu.Lock()
	kind := "capture"
	if unfocused {
		kind = "target"
	}
	first := !r.waiting || r.waitKind != kind
	r.waiting = true
	r.waitKind = kind
	r.waitReason = reason
	if first {
		r.waitSince = now
	}
	r.mu.Unlock()
	if first {
		r.log.Info("instance waiting for screen", "instance", r.opts.ID, "reason", reason)
		r.emit("instance.waiting", map[string]any{"reason": reason})
		r.traceEvent("wait", "画面/窗口暂不可用："+reason, map[string]any{"kind": kind})
	}
	return true
}

func (r *Runner) clearWaiting() {
	r.mu.Lock()
	was := r.waiting
	since := r.waitSince
	r.waiting = false
	r.waitKind = ""
	r.waitReason = ""
	r.waitSince = time.Time{}
	r.mu.Unlock()
	if was {
		waited := time.Since(since).Milliseconds()
		r.emit("instance.waiting_end", map[string]any{"waited_ms": waited})
		r.traceEvent("resume", "画面恢复，继续决策", map[string]any{"waited_ms": waited})
	}
}

// clearCaptureWaiting ends a screen-loss wait but deliberately leaves a
// game-window-focus wait in place until an action actually succeeds. Otherwise
// a successful screenshot would make the UI flap between waiting and running on
// every tick while the game is in the background.
func (r *Runner) clearCaptureWaiting() {
	r.mu.Lock()
	isCapture := r.waiting && r.waitKind == "capture"
	r.mu.Unlock()
	if isCapture {
		r.clearWaiting()
	}
}

// maybeRecover asks the perception source to rebuild a stalled capture. It is
// throttled and only runs for capture waits: a background game window cannot be
// recovered by the agent and must be brought forward by the user.
func sameInput(a, b *domain.InputSpec) bool {
	if a == nil || b == nil {
		return false
	}
	if a.Type != b.Type {
		return false
	}
	switch a.Type {
	case "key":
		return a.Key != "" && strings.EqualFold(a.Key, b.Key)
	case "click":
		return math.Abs(a.X-b.X) < 0.001 && math.Abs(a.Y-b.Y) < 0.001
	case "text":
		return a.Text == b.Text
	}
	return false
}

func (r *Runner) maybeRecover() {
	now := time.Now()
	r.mu.Lock()
	waiting, kind := r.waiting, r.waitKind
	since, last := r.waitSince, r.lastRecover
	r.mu.Unlock()
	if !waiting || kind != "capture" || since.IsZero() {
		return
	}
	if now.Sub(since) < 3*time.Second || (!last.IsZero() && now.Sub(last) < 3*time.Second) {
		return
	}
	r.mu.Lock()
	r.lastRecover = now
	r.mu.Unlock()
	if rec, ok := r.opts.Source.(interface{ RequestRecover(string) error }); ok {
		if err := rec.RequestRecover("frame stream stalled"); err != nil {
			r.traceEvent("recover", "请求手机重建截屏失败："+err.Error(), nil)
			return
		}
		r.traceEvent("recover", "已请求手机重建截屏（旋转/权限/断流自动恢复）", nil)
		return
	}
	r.traceEvent("recover", "等待画面恢复，当前采集源不支持主动重建", nil)
}

type frameSinceSource interface {
	GrabSince(context.Context, time.Time) (domain.Frame, error)
}

type latestFrameSource interface {
	Latest() (domain.Frame, bool)
}

// grabFrame waits for a frame captured after since when the source supports it.
// Generic sources keep their old behaviour.
func (r *Runner) grabFrame(ctx context.Context, since time.Time) (domain.Frame, error) {
	if since.IsZero() {
		return r.opts.Source.Grab(ctx)
	}
	if s, ok := r.opts.Source.(frameSinceSource); ok {
		return s.GrabSince(ctx, since)
	}
	return r.opts.Source.Grab(ctx)
}

var ocrTextPattern = regexp.MustCompile("\"text\"\\s*:\\s*\"((?:[^\"\\\\]|\\\\.)*)\"")

// ocrTexts returns the recognized labels in reading order, lower-cased and
// de-duplicated. OCR boxes jitter between frames, so raw JSON is useless for
// detecting a real screen transition.
func ocrTexts(meta string) []string {
	if meta == "" {
		return nil
	}
	matches := ocrTextPattern.FindAllStringSubmatch(meta, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(matches))
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		v := strings.ToLower(strings.TrimSpace(m[1]))
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

func containsLabel(texts []string, label string) bool {
	label = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(label, "text:")))
	if label == "" {
		return false
	}
	for _, t := range texts {
		if strings.Contains(t, label) || strings.Contains(label, t) {
			return true
		}
	}
	return false
}

// screenChangedSince reports whether the screen moved on while a slow decision
// was being made. For a tap on an OCR label, the decisive test is whether that
// label still exists: OCR of the same static screen drops or adds a box now and
// then, but the button text stays. Also abort when a quit confirmation appears,
// because the old coordinates could land on "exit game".
func (r *Runner) screenChangedSince(frame domain.Frame, action domain.Action) bool {
	ls, ok := r.opts.Source.(latestFrameSource)
	if !ok {
		return false
	}
	latest, ok := ls.Latest()
	if !ok || !latest.At.After(frame.At.Add(120*time.Millisecond)) {
		return false
	}
	before := ocrTexts(frame.Meta["ocr"])
	now := ocrTexts(latest.Meta["ocr"])
	if action.Kind == "tap_text" {
		if !containsLabel(now, action.Target) {
			return true
		}
	}
	if !containsAnyLabel(before, destructiveScreenWords) && containsAnyLabel(now, destructiveScreenWords) {
		return true
	}
	return false
}

var destructiveScreenWords = []string{"退出游戏", "结束游戏", "退出登录", "健康游戏忠告", "quit game", "exit game"}

func containsAnyLabel(texts []string, words []string) bool {
	for _, w := range words {
		if containsLabel(texts, w) {
			return true
		}
	}
	return false
}

func (r *Runner) step(ctx context.Context) error {
	start := time.Now()
	// A desktop executor can require its game window to be foreground. Check
	// before grabbing: otherwise a background game would make us capture and
	// analyze whatever private window the user is actually using.
	if guard, ok := r.opts.Executor.(interface {
		TargetFocused() (bool, string)
	}); ok {
		if focused, why := guard.TargetFocused(); !focused {
			r.noteWaiting(fmt.Errorf("%w: %s", action.ErrTargetNotFocused, why))
			return nil
		}
	}
	frame, err := r.grabFrame(ctx, r.lastActAt)
	if err != nil {
		if r.noteWaiting(err) {
			return nil
		}
		return fmt.Errorf("capture: %w", err)
	}
	r.clearCaptureWaiting()
	r.offerFrame(frame)
	st, world, err := r.opts.Analyzer.Analyze(frame)
	if err != nil {
		return fmt.Errorf("analyze: %w", err)
	}
	r.opts.Metrics.Observe(metrics.PerceptionLatencyMS, float64(time.Since(start).Milliseconds()))
	// The Android client reports which package owns the screen. If the user
	// leaves the game (launcher, another app, a system dialog), stop deciding
	// instead of tapping whatever is now visible.
	if pkg := r.opts.GamePackage; pkg != "" {
		if fg := strings.TrimSpace(frame.Meta["foreground"]); fg != "" {
			if fg == pkg {
				// The user came back to the game (or a vendor overlay stopped
				// masking it). Leaving the target wait set here would freeze the
				// instance forever because decisions are skipped while waiting.
				r.clearWaiting()
			} else if fg != "com.goplay.mobile" {
				r.noteWaiting(fmt.Errorf("%w: 前台是 %s，目标游戏是 %s", action.ErrTargetNotFocused, fg, pkg))
				return nil
			}
		}
	}
	r.traceEvent("see", st.Summary, map[string]any{
		"tags": st.Tags, "scene_change": st.Value("scene_change"),
		"frame_delta": st.Value("frame_delta"), "edge_ratio": st.Value("edge_ratio"),
		"text_count": st.Value("text_count"), "ui_text": truncateRunes(st.Text["ui_text"], 120),
		"hash": shortHash(st.Hash),
	})

	// Lua profile turns raw perception into semantic state and legal actions.
	pst, err := r.opts.Profile.Observe(world, st)
	if err != nil {
		return fmt.Errorf("profile observe: %w", err)
	}
	acts, err := r.opts.Profile.Actions(world, pst)
	if err != nil {
		return fmt.Errorf("profile actions: %w", err)
	}
	// A demonstrated action is injected into the current legal set, not applied
	// blindly: policy still validates it and the executor still checks focus.
	var demoActs []domain.Action
	demoReason := ""
	if r.opts.Teacher != nil {
		if h, ok := r.opts.Teacher.Hint(frame); ok {
			demoActs = append(demoActs, h.Action)
			demoReason = fmt.Sprintf("模仿你的示范（画面相似度 %.0f%%）：%s", h.Similarity*100, h.Note)
			acts = append(append([]domain.Action(nil), demoActs...), acts...)
		}
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

	// One-shot directives are collected only when they can actually be applied.
	// Taking them before a terminal/paused return would silently lose the user's
	// instruction. They join the legal set so policy can pick an action the
	// profile itself does not offer (for example a key unique to this game),
	// while hard constraints still filter them.
	var directiveActs []domain.Action
	directiveReason := ""
	skippedKeyboard := 0
	for _, d := range r.opts.Sched.TakeDirectives() {
		act := d.Action
		// The Android client is touch-only: turning "press Z" into a key event
		// would reach the bridge and fail with "keyboard action ... is not
		// supported by the touch client". Drop it here with a trace instead of
		// surfacing an action error the user cannot fix.
		if act.Input != nil && act.Input.Type == "key" && r.opts.Profile.ID == "generic_touch" {
			skippedKeyboard++
			continue
		}
		// If the profile already exposes this exact input, run the profile
		// action instead of the synthetic directive action. Executors such as
		// the mock game validate the action kind, and the UI reads better when
		// the real "attack" action is shown rather than "directive_key_a".
		for _, legal := range acts {
			if sameInput(legal.Input, act.Input) {
				act = legal
				break
			}
		}
		directiveActs = append(directiveActs, act)
		if directiveReason == "" {
			directiveReason = "执行你的指令：" + d.Text
		}
	}
	if skippedKeyboard > 0 {
		r.traceEvent("directive", "触摸端不支持键盘指令，已忽略；可以改用“点哪里/滑动方向”", map[string]any{"skipped_keyboard": skippedKeyboard})
	}
	if len(directiveActs) > 0 {
		r.traceEvent("directive", directiveReason, map[string]any{"count": len(directiveActs)})
		merged := append([]domain.Action(nil), directiveActs...)
		for _, a := range acts {
			dup := false
			for _, d := range directiveActs {
				if d.Key() == a.Key() {
					dup = true
					break
				}
			}
			if !dup {
				merged = append(merged, a)
			}
		}
		acts = merged
	}

	ctl := res.Control
	r.mu.Lock()
	gameName, gamePackage, gamePrior := r.opts.GameName, r.opts.GamePackage, r.gamePrior
	r.mu.Unlock()
	dec := r.opts.Policy.Decide(ctx, policy.Input{
		State: pst, Actions: acts, Control: ctl, Rules: r.opts.Profile.Rules, Hints: ctl.Hints,
		GameName: gameName, GamePackage: gamePackage, GamePrior: gamePrior,
		Demo: demoActs, DemoReason: demoReason,
		Directive: directiveActs, DirectiveReason: directiveReason,
	})
	thinkMsg := fmt.Sprintf("有 %d 个合法动作，选择 %s", len(acts), dec.Action.Key())
	if len(dec.Blocked) > 0 {
		thinkMsg += fmt.Sprintf("（安全过滤拦截 %d 个危险/无效动作）", len(dec.Blocked))
	}
	r.traceEvent("think", thinkMsg, map[string]any{
		"source": dec.Source, "reason": dec.Reason, "state_key": dec.StateKey,
		"demo": dec.Demo, "directive": len(directiveActs) > 0,
		"llm_used": dec.LLMUsed, "candidates": candidateKeys(dec.Candidates, 6),
		"blocked": dec.Blocked,
	})
	r.opts.Metrics.Observe(metrics.DecisionLatencyMS, dec.LatencyMS)
	if dec.Source == "demo" && r.opts.Teacher != nil {
		r.opts.Teacher.MarkUsed()
	}
	if dec.Fallback {
		r.opts.Metrics.Inc(metrics.PolicyFallbackTotal, 1)
	}
	r.mu.Lock()
	r.stats.Decisions++
	r.stats.LastDecisionAt = time.Now()
	r.lastState = pst
	r.lastDec = dec
	r.mu.Unlock()

	// A slow local model can spend many seconds deciding. If the screen moved on
	// in the meantime, the coordinates in this decision describe the previous
	// screen (for example a "开始游戏" box that is now "退出游戏" on a health
	// warning). Drop the stale action and decide again from the new frame.
	if r.screenChangedSince(frame, dec.Action) {
		r.traceEvent("stale", "画面在决策期间已变化，放弃过期动作", nil)
		return nil
	}

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
		// A phone that briefly drops its WebSocket must not kill the instance:
		// the client reconnects on its own and the next tick retries. Real
		// action failures (bad gesture/unsupported input) still fail loudly.
		if r.noteWaiting(err) {
			return nil
		}
		return fmt.Errorf("execute %q: %w", dec.Action.Key(), err)
	}
	if marker, ok := r.opts.Human.(action.InjectedInputMarker); ok && dec.Action.Input != nil && dec.Action.Input.Type != "wait" {
		// Tell the detector that the input it is about to see came from us.
		// Without this every agent click looks like a human takeover.
		marker.MarkInjected()
	}
	r.clearWaiting()
	r.mu.Lock()
	r.lastActAt = actStart
	r.mu.Unlock()
	lat := float64(time.Since(actStart).Milliseconds())
	r.opts.Metrics.Observe(metrics.ActionLatencyMS, lat)
	r.opts.Metrics.Inc(metrics.ActionsTotal, 1)
	r.mu.Lock()
	r.stats.Actions++
	actionCount := r.stats.Actions
	r.stats.LastAction = dec.Action.Key()
	r.mu.Unlock()
	r.emit("instance.action", map[string]any{"action": dec.Action, "source": dec.Source, "latency_ms": lat})
	r.traceEvent("act", "执行 "+dec.Action.Key(), map[string]any{
		"input": inputView(dec.Action.Input), "source": dec.Source, "latency_ms": lat,
		"reason": dec.Action.Reason,
	})
	if n := actionCount; n > 0 && n%10 == 0 {
		reason := strings.TrimSpace(dec.Reason)
		if len([]rune(reason)) > 60 {
			reason = string([]rune(reason)[:60]) + "…"
		}
		line := fmt.Sprintf("我在玩「%s」，已经操作了 %d 次，刚才是 %s。", r.opts.Name, n, dec.Action.Key())
		if reason != "" {
			line += "理由：" + reason
		}
		r.narrate(line)
	}

	// Observe the aftermath through the same pixel pipeline. Wait for a frame
	// captured after the tap; reusing the pre-action frame made the loop think
	// nothing changed and repeat the same stale coordinates.
	after, err := r.grabFrame(ctx, actStart)
	if err != nil {
		if r.noteWaiting(err) {
			return nil
		}
		return fmt.Errorf("capture after action: %w", err)
	}
	r.offerFrame(after)
	st2, world2, err := r.opts.Analyzer.Analyze(after)
	if err != nil {
		return fmt.Errorf("analyze after action: %w", err)
	}
	interp, err := r.opts.Profile.Interpret(world, world2)
	if err != nil {
		return fmt.Errorf("profile interpret: %w", err)
	}
	interp = dampRiskyReward(dec, interp)
	r.opts.Policy.Update(dec, interp.Reward)
	effective := interp.Reward >= 0.05
	r.traceEvent("result", interp.Note, map[string]any{
		"reward": interp.Reward, "delta": interp.Delta, "terminal": interp.Terminal, "effective": effective,
		"scene_change": st2.Value("scene_change"), "frame_delta": st2.Value("frame_delta"),
	})

	r.mu.Lock()
	r.stats.Reward += interp.Reward
	if effective {
		r.stats.EffectiveActions++
	}
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

// dampRiskyReward keeps navigation keys that leave the current screen from
// looking like progress. A back press often produces a 98% scene change (the
// game closes and the launcher appears), which would otherwise teach the
// policy that quitting is the most rewarding move. Closing a popup still earns
// a small positive reward, enough to keep the action useful.
func dampRiskyReward(dec policy.Decision, interp luabridge.Interpretation) luabridge.Interpretation {
	if dec.Action.Input == nil {
		return interp
	}
	risky := dec.Action.Input.Type == "back"
	if key := strings.ToLower(dec.Action.Input.Key); key == "escape" || key == "esc" {
		risky = true
	}
	if !risky || interp.Reward <= 0 {
		return interp
	}
	interp.Reward *= 0.1
	if interp.Note != "" {
		interp.Note += ", back is risky (reward damped)"
	} else {
		interp.Note = "back is risky (reward damped)"
	}
	return interp
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
	if r.opts.Teacher != nil {
		r.opts.Teacher.End(time.Now())
	}
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

func shortHash(h string) string {
	if len(h) <= 8 {
		return h
	}
	return h[:8]
}

// candidateKeys keeps the visible trace short while still showing what the
// policy considered. It never exposes hidden model reasoning.
func candidateKeys(cands []policy.Candidate, n int) []string {
	if n <= 0 || len(cands) == 0 {
		return nil
	}
	if len(cands) < n {
		n = len(cands)
	}
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, fmt.Sprintf("%s(%.2f)", cands[i].Action.Key(), cands[i].Score))
	}
	return out
}

func inputView(in *domain.InputSpec) map[string]any {
	if in == nil {
		return nil
	}
	m := map[string]any{"type": in.Type}
	if in.Key != "" {
		m["key"] = in.Key
	}
	if in.Text != "" {
		m["text"] = in.Text
	}
	if in.Type == "click" || in.Type == "tap" {
		m["x"], m["y"] = in.X, in.Y
	}
	if in.DurationMS > 0 {
		m["duration_ms"] = in.DurationMS
	}
	return m
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

// truncateRunes keeps trace payloads small while preserving valid UTF-8.
func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
