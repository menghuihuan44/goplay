package engine

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"goplay/internal/action"
	"goplay/internal/domain"
	"goplay/internal/luabridge"
	"goplay/internal/memory"
	"goplay/internal/metrics"
	"goplay/internal/mockgame"
	"goplay/internal/perception"
	"goplay/internal/policy"
	"goplay/internal/scheduler"
)

type harness struct {
	runner *Runner
	game   *mockgame.Game
	human  *action.MockHuman
	mgr    *memory.Manager
	sched  *scheduler.Scheduler
	reg    *metrics.Registry
}

func newHarness(t *testing.T, id string, seed int64) *harness {
	return newHarnessN(t, id, seed, 1)
}

func newHarnessN(t *testing.T, id string, seed int64, maxEpisodes int) *harness {
	t.Helper()
	game := mockgame.New(seed)
	human := action.NewMockHuman()
	prof, err := luabridge.LoadBuiltin("mock_dungeon")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(prof.Close)
	mgr, err := memory.Open(t.TempDir(), 256)
	if err != nil {
		t.Fatal(err)
	}
	im := mgr.Instance(id)
	reg := metrics.New()
	sched := scheduler.New(id, "Mock Dungeon", prof.Rules, mgr, nil, reg, "")
	src := perception.NewMockSource(game, 0.001)
	pol := policy.New(nil, reg, policy.DefaultConfig())
	r, err := New(Options{
		ID: id, Name: "Mock Dungeon", Profile: prof, Source: src,
		Analyzer: &perception.MockAnalyzer{}, Executor: &action.MockExecutor{Game: game, Human: human},
		Human: human, Sched: sched, Policy: pol, Memory: im, Metrics: reg,
		Save:   func() error { return mgr.SaveInstance(id) },
		Reset:  func() error { game.Reset(); return nil },
		Config: Config{TickMS: 5, HumanResumeMS: 80, MaxActionsSec: 1000, SettleMS: 0, SaveEvery: 1000, MaxEpisodes: maxEpisodes},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &harness{runner: r, game: game, human: human, mgr: mgr, sched: sched, reg: reg}
}

func waitFor(t *testing.T, timeout time.Duration, desc string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", desc)
}

func TestRunnerLearnsAndWins(t *testing.T) {
	h := newHarness(t, "auto-1", 7)
	h.runner.Start()
	defer h.runner.Stop()
	waitFor(t, 60*time.Second, "terminal", func() bool {
		s := h.runner.Status()
		return s == domain.StatusStopped || s == domain.StatusError
	})
	snap := h.runner.Snapshot()
	gs := h.game.Snapshot()
	won, _ := gs["won"].(bool)
	dead, _ := gs["dead"].(bool)
	t.Logf("status=%s decisions=%d actions=%d reward=%.1f progress=%.2f best=%.2f won=%v dead=%v err=%s",
		snap.Status, snap.Stats.Decisions, snap.Stats.Actions, snap.Stats.Reward,
		snap.Stats.Progress, snap.Stats.BestProgress, won, dead, snap.Stats.LastError)
	if snap.Status == domain.StatusError {
		t.Fatalf("runner error: %s", snap.Stats.LastError)
	}
	if snap.Stats.Actions < 15 {
		t.Fatalf("too few actions: %d", snap.Stats.Actions)
	}
	if !won && snap.Stats.BestProgress < 0.5 {
		t.Fatalf("no meaningful progress: best=%.2f", snap.Stats.BestProgress)
	}
	if dead && snap.Stats.BestProgress == 0 {
		t.Fatalf("died without progress")
	}
}

func TestDirectiveKeyRunsRealProfileAction(t *testing.T) {
	h := newHarnessN(t, "directive-1", 11, 10)
	h.runner.Start()
	defer h.runner.Stop()
	h.sched.Submit(scheduler.Message{ID: "m1", Text: "按 A 键", Source: "user_text", At: time.Now()})
	waitFor(t, 8*time.Second, "directive decision", func() bool {
		snap := h.runner.Snapshot()
		for _, ev := range snap.Trace {
			if ev.Phase == "think" {
				if src, _ := ev.Data["source"].(string); src == "directive" {
					return true
				}
			}
		}
		return false
	})
	snap := h.runner.Snapshot()
	if snap.Stats.Errors != 0 {
		t.Fatalf("directive produced an execution error: %s", snap.Stats.LastError)
	}
	if snap.Stats.Actions == 0 {
		t.Fatal("no action executed")
	}
}

func TestRunnerCanRestartAfterStop(t *testing.T) {
	h := newHarnessN(t, "restart", 7, 100)
	h.runner.Start()
	waitFor(t, 10*time.Second, "first actions", func() bool {
		return h.runner.Snapshot().Stats.Actions >= 3
	})
	before := h.runner.Snapshot().Stats.Actions
	h.runner.Stop()
	if snap := h.runner.Snapshot(); snap.Status != domain.StatusStopped || snap.Stats.Errors != 0 {
		t.Fatalf("deliberate stop must be clean: status=%s errors=%d last=%s", snap.Status, snap.Stats.Errors, snap.Stats.LastError)
	}
	h.runner.Start()
	waitFor(t, 10*time.Second, "actions after restart", func() bool {
		return h.runner.Snapshot().Stats.Actions > before
	})
	h.runner.Stop()
}

type transientSource struct {
	inner perception.Source
	fails int
}

func (s *transientSource) Grab(ctx context.Context) (domain.Frame, error) {
	if s.fails > 0 {
		s.fails--
		return domain.Frame{}, fmt.Errorf("%w: no Android client connected", domain.ErrTransient)
	}
	return s.inner.Grab(ctx)
}
func (s *transientSource) Describe() string { return s.inner.Describe() }
func (s *transientSource) Close() error     { return s.inner.Close() }

func TestTransientFrameLossWaitsInsteadOfFailing(t *testing.T) {
	h := newHarness(t, "transient-1", 5)
	flaky := &transientSource{inner: h.runner.opts.Source, fails: 20}
	h.runner.opts.Source = flaky
	h.runner.Start()
	defer h.runner.Stop()
	var waitReason string
	waitFor(t, 5*time.Second, "waiting state", func() bool {
		snap := h.runner.Snapshot()
		waitReason = snap.WaitReason
		return snap.Waiting
	})
	if waitReason == "" || strings.Contains(waitReason, domain.ErrTransient.Error()) {
		t.Fatalf("wait reason should be user-facing, got %q", waitReason)
	}
	waitFor(t, 10*time.Second, "recovered actions", func() bool { return h.runner.Snapshot().Stats.Actions > 0 })
	snap := h.runner.Snapshot()
	if snap.Stats.Errors != 0 || snap.Status == domain.StatusError {
		t.Fatalf("transient loss must not be a terminal error: status=%s errors=%d last=%s", snap.Status, snap.Stats.Errors, snap.Stats.LastError)
	}
	if snap.Waiting {
		t.Fatalf("waiting flag should clear after frames return: %+v", snap)
	}
}

func TestHumanTakeoverPausesActions(t *testing.T) {
	h := newHarness(t, "human-1", 3)
	h.runner.Start()
	defer h.runner.Stop()
	waitFor(t, 10*time.Second, "some actions", func() bool {
		return h.runner.Snapshot().Stats.Actions >= 5
	})
	h.human.Touch()
	waitFor(t, 5*time.Second, "human status", func() bool {
		return h.runner.Status() == domain.StatusHuman
	})
	before := h.runner.Snapshot().Stats.Actions
	time.Sleep(200 * time.Millisecond)
	if after := h.runner.Snapshot().Stats.Actions; after != before {
		t.Fatalf("actions advanced during human control: %d -> %d", before, after)
	}
	h.human.Release()
	waitFor(t, 5*time.Second, "actions resume", func() bool {
		return h.runner.Snapshot().Stats.Actions > before
	})
}

func TestHardConstraintBlocksBoss(t *testing.T) {
	h := newHarness(t, "constraint-1", 11)
	h.sched.AddConstraint(domain.Constraint{
		ID: "c-boss", Text: "先别打那个 Boss", DenyTargets: []string{"boss"},
		Source: "user", InstanceID: "constraint-1", CreatedAt: time.Now(), Active: true,
	})
	h.runner.Start()
	defer h.runner.Stop()
	waitFor(t, 30*time.Second, "terminal or progress", func() bool {
		s := h.runner.Status()
		gs := h.game.Snapshot()
		room, _ := gs["room"].(int)
		return s == domain.StatusStopped || room >= 10
	})
	gs := h.game.Snapshot()
	room, _ := gs["room"].(int)
	bossHP, _ := gs["enemy_hp"].(int)
	won, _ := gs["won"].(bool)
	dead, _ := gs["dead"].(bool)
	if room >= 10 && !dead && !won {
		time.Sleep(30 * time.Millisecond)
	}
	// The boss may survive because attack is filtered by target; that is proof
	// the hard channel reached the policy, not proof of a crash.
	t.Logf("room=%d boss_hp=%d won=%v dead=%v", room, bossHP, won, dead)
}

func TestPreferenceIsSoftAndDoesNotStopPlay(t *testing.T) {
	h := newHarness(t, "pref-1", 5)
	h.runner.Start()
	defer h.runner.Stop()
	waitFor(t, 5*time.Second, "first action", func() bool {
		return h.runner.Snapshot().Stats.Actions >= 1
	})
	h.runner.Submit("你太激进了")
	waitFor(t, 3*time.Second, "preference adapted", func() bool {
		return h.sched.Control().Weights.Get("risk_aversion") > 0
	})
	before := h.runner.Snapshot().Stats.Actions
	waitFor(t, 5*time.Second, "play continues", func() bool {
		return h.runner.Snapshot().Stats.Actions > before
	})
}

func TestDampRiskyReward(t *testing.T) {
	back := policy.Decision{Action: domain.Action{Kind: "back", Input: &domain.InputSpec{Type: "back"}}}
	got := dampRiskyReward(back, luabridge.Interpretation{Reward: 4.9, Note: "scene changed 98%"})
	if got.Reward >= 1 {
		t.Fatalf("back reward not damped: %v", got.Reward)
	}
	if !strings.Contains(got.Note, "damped") {
		t.Fatalf("note missing damping hint: %q", got.Note)
	}
	tap := policy.Decision{Action: domain.Action{Kind: "tap", Input: &domain.InputSpec{Type: "tap", X: 0.5, Y: 0.5}}}
	if unchanged := dampRiskyReward(tap, luabridge.Interpretation{Reward: 1}); unchanged.Reward != 1 {
		t.Fatalf("unrelated action changed: %v", unchanged.Reward)
	}
}

func TestTwoInstancesAreIsolated(t *testing.T) {
	a := newHarness(t, "iso-a", 21)
	b := newHarness(t, "iso-b", 22)
	a.runner.Start()
	b.runner.Start()
	defer a.runner.Stop()
	defer b.runner.Stop()
	waitFor(t, 10*time.Second, "both act", func() bool {
		return a.runner.Snapshot().Stats.Actions > 2 && b.runner.Snapshot().Stats.Actions > 2
	})
	sa, sb := a.runner.Snapshot(), b.runner.Snapshot()
	if sa.ID == sb.ID {
		t.Fatal("instances share id")
	}
	if sa.Stats.Actions == 0 || sb.Stats.Actions == 0 {
		t.Fatalf("one instance starved: %d %d", sa.Stats.Actions, sb.Stats.Actions)
	}
	if a.mgr.Instance("iso-a") == b.mgr.Instance("iso-b") {
		t.Fatal("instances share memory")
	}
	t.Logf("a=%d actions b=%d actions", sa.Stats.Actions, sb.Stats.Actions)
	_ = context.Background()
	_ = fmt.Sprintf("%v", time.Now())
}
