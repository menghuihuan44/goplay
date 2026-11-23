package eval

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"goplay/internal/action"
	"goplay/internal/domain"
	"goplay/internal/engine"
	"goplay/internal/llm"
	"goplay/internal/luabridge"
	"goplay/internal/memory"
	"goplay/internal/metrics"
	"goplay/internal/mockgame"
	"goplay/internal/perception"
	"goplay/internal/policy"
	"goplay/internal/scheduler"
)

// harnessConfig controls one assembled mock-dungeon instance.
type harnessConfig struct {
	ID             string
	Seed           int64
	Noise          float64
	MaxEpisodes    int
	TickMS         int
	HumanResumeMS  int
	MaxActionsSec  int
	MemoryBudgetKB int
	Dir            string // optional; defaults to a fresh temp dir
}

type actionRecord struct {
	At     time.Time
	Kind   string
	Target string
}

// recordingExecutor wraps action.MockExecutor and records successfully
// executed actions in order. It is the only way to observe the exact action
// stream without modifying the engine.
type recordingExecutor struct {
	inner *action.MockExecutor

	mu  sync.Mutex
	log []actionRecord
}

func (e *recordingExecutor) Do(ctx context.Context, a domain.Action) error {
	err := e.inner.Do(ctx, a)
	if err == nil {
		e.mu.Lock()
		e.log = append(e.log, actionRecord{At: time.Now(), Kind: a.Kind, Target: a.Target})
		e.mu.Unlock()
	}
	return err
}

func (e *recordingExecutor) Describe() string { return "recording:" + e.inner.Describe() }

func (e *recordingExecutor) Close() error { return e.inner.Close() }

func (e *recordingExecutor) snapshot() []actionRecord {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]actionRecord, len(e.log))
	copy(out, e.log)
	return out
}

func (e *recordingExecutor) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.log)
}

func (e *recordingExecutor) countAfter(t time.Time) int {
	n := 0
	for _, r := range e.snapshot() {
		if r.At.After(t) {
			n++
		}
	}
	return n
}

// harness is a fully wired, observable goplay instance running the built-in
// mock dungeon profile against the mock game renderer.
type harness struct {
	ID      string
	Game    *mockgame.Game
	Human   *action.MockHuman
	Profile *luabridge.Profile
	Mgr     *memory.Manager
	Mem     *memory.InstanceMemory
	Reg     *metrics.Registry
	Sched   *scheduler.Scheduler
	Runner  *engine.Runner
	Exec    *recordingExecutor

	dir       string
	ownDir    bool
	closeOnce sync.Once
}

func newHarness(cfg harnessConfig) (*harness, error) {
	if cfg.ID == "" {
		cfg.ID = fmt.Sprintf("eval-%d", time.Now().UnixNano())
	}
	if cfg.Noise <= 0 {
		cfg.Noise = 0.001
	}
	if cfg.MaxEpisodes < 1 {
		cfg.MaxEpisodes = 1
	}
	if cfg.TickMS <= 0 {
		cfg.TickMS = 5
	}
	if cfg.HumanResumeMS <= 0 {
		cfg.HumanResumeMS = 150
	}
	if cfg.MaxActionsSec <= 0 {
		cfg.MaxActionsSec = 1000
	}
	if cfg.MemoryBudgetKB <= 0 {
		cfg.MemoryBudgetKB = 256
	}
	dir := cfg.Dir
	ownDir := false
	if dir == "" {
		var err error
		dir, err = os.MkdirTemp("", "goplay-eval-*")
		if err != nil {
			return nil, err
		}
		ownDir = true
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	game := mockgame.New(cfg.Seed)
	human := action.NewMockHuman()
	prof, err := luabridge.LoadBuiltin("mock_dungeon")
	if err != nil {
		return nil, fmt.Errorf("load profile: %w", err)
	}
	mgr, err := memory.Open(filepath.Join(dir, "memory"), cfg.MemoryBudgetKB)
	if err != nil {
		prof.Close()
		return nil, fmt.Errorf("open memory: %w", err)
	}
	im := mgr.Instance(cfg.ID)
	reg := metrics.New()
	sched := scheduler.New(cfg.ID, "Mock Dungeon", prof.Rules, mgr, nil, reg, "")
	src := perception.NewMockSource(game, cfg.Noise)
	pol := policy.New(nil, reg, policy.DefaultConfig())
	exec := &recordingExecutor{inner: &action.MockExecutor{Game: game, Human: human}}

	runner, err := engine.New(engine.Options{
		ID:       cfg.ID,
		Name:     "eval " + cfg.ID,
		Profile:  prof,
		Source:   src,
		Analyzer: perception.MockAnalyzer{},
		Executor: exec,
		Human:    human,
		Metrics:  reg,
		Sched:    sched,
		Policy:   pol,
		Memory:   im,
		Save:     func() error { return mgr.SaveInstance(cfg.ID) },
		Reset: func() error {
			game.Reset()
			return nil
		},
		Config: engine.Config{
			TickMS:        cfg.TickMS,
			HumanResumeMS: cfg.HumanResumeMS,
			MaxActionsSec: cfg.MaxActionsSec,
			SettleMS:      0,
			SaveEvery:     1000,
			MaxEpisodes:   cfg.MaxEpisodes,
		},
	})
	if err != nil {
		prof.Close()
		return nil, fmt.Errorf("new engine: %w", err)
	}
	return &harness{
		ID: cfg.ID, Game: game, Human: human, Profile: prof,
		Mgr: mgr, Mem: im, Reg: reg, Sched: sched, Runner: runner, Exec: exec,
		dir: dir, ownDir: ownDir,
	}, nil
}

func (h *harness) Start() { h.Runner.Start() }

func (h *harness) Stop() { h.Runner.Stop() }

func (h *harness) Close() {
	h.closeOnce.Do(func() {
		h.Runner.Stop()
		if h.Profile != nil {
			h.Profile.Close()
		}
		if h.ownDir {
			_ = os.RemoveAll(h.dir)
		}
	})
}

func (h *harness) Snapshot() engine.Snapshot { return h.Runner.Snapshot() }

func (h *harness) Status() domain.InstanceStatus { return h.Runner.Status() }

func (h *harness) Submit(text string) { h.Runner.Submit(text) }

// waitFor polls cond every 2ms until it is true, the timeout expires, or ctx is
// cancelled. It returns a descriptive error so suite failures are diagnosable
// from the JSON report.
func waitFor(ctx context.Context, timeout time.Duration, what string, cond func() bool) error {
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("wait for %s: %w", what, err)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s waiting for %s", timeout, what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// waitActions blocks until the recorded action count reaches at least n.
func (h *harness) waitActions(ctx context.Context, n int, timeout time.Duration) error {
	return waitFor(ctx, timeout, fmt.Sprintf("%d actions", n), func() bool {
		return h.Exec.count() >= n
	})
}

// waitTerminal blocks until the runner stops (normal or error).
func (h *harness) waitTerminal(ctx context.Context, timeout time.Duration) error {
	return waitFor(ctx, timeout, "terminal state", func() bool {
		st := h.Status()
		return st == domain.StatusStopped || st == domain.StatusError
	})
}

// ensure llm.Provider is used: the harness reserves the nil policy provider
// path deliberately. This compile-time assertion documents that eval uses the
// abstract provider interface only through the local-model suite.
var _ llm.Provider = (*llm.Mock)(nil)
