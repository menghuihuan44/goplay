// Package action applies semantic actions through a real input channel. The
// executor is the only code allowed to synthesize keyboard/mouse/touch input;
// the safety audit checks that it never touches another process's memory.
package action

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"goplay/internal/domain"
	"goplay/internal/mockgame"
)

var ErrHumanControl = errors.New("human has control; agent action suppressed")

// ErrTargetNotFocused means the game window is not foreground, so injecting
// input would land in another application. The engine treats it as a safe pause,
// not as a game action failure.
var ErrTargetNotFocused = errors.New("target game window is not focused")

type Executor interface {
	Do(ctx context.Context, a domain.Action) error
	Describe() string
	Close() error
}

// HumanState is reported by a detector before every decision and immediately
// before every action.
type HumanState struct {
	Active bool      `json:"active"`
	Since  time.Time `json:"since,omitempty"`
	Reason string    `json:"reason,omitempty"`
}

type HumanDetector interface {
	State(ctx context.Context) HumanState
}

// InjectedInputMarker is implemented by detectors that can distinguish the
// agent's own synthesized input from a real human keystroke. The engine calls
// MarkInjected immediately after a successful SendInput action.
type InjectedInputMarker interface {
	MarkInjected()
}

// MockHuman simulates a user grabbing the controls. Tests and demos can call
// Touch() to exercise the handover path.
type MockHuman struct {
	mu   sync.Mutex
	last time.Time
}

func NewMockHuman() *MockHuman { return &MockHuman{} }

func (m *MockHuman) Touch() {
	m.mu.Lock()
	m.last = time.Now()
	m.mu.Unlock()
}

func (m *MockHuman) Release() {
	m.mu.Lock()
	m.last = time.Time{}
	m.mu.Unlock()
}

func (m *MockHuman) State(ctx context.Context) HumanState {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.last.IsZero() {
		return HumanState{}
	}
	idle := time.Since(m.last)
	if idle > 1500*time.Millisecond {
		return HumanState{}
	}
	return HumanState{Active: true, Since: m.last, Reason: "mock keyboard/mouse activity"}
}

// MockExecutor decodes the action's input into a semantic game action, exactly
// like a real input backend would deliver keystrokes to the game.
type MockExecutor struct {
	Game  *mockgame.Game
	Human *MockHuman
}

var mockKeyMap = map[string]string{
	"a": "attack", "d": "defend", "h": "heal", "w": "advance", "r": "rest",
	"enter": "advance", "space": "wait",
}

func (m *MockExecutor) Do(ctx context.Context, a domain.Action) error {
	if m.Human != nil && m.Human.State(ctx).Active {
		return ErrHumanControl
	}
	if m.Game == nil {
		return fmt.Errorf("mock executor has no game")
	}
	if a.Input == nil {
		return fmt.Errorf("action %q has no input spec", a.Key())
	}
	switch a.Input.Type {
	case "wait":
		return m.Game.Apply("wait")
	case "key":
		kind, ok := mockKeyMap[a.Input.Key]
		if !ok {
			return fmt.Errorf("no mock key mapping for %q", a.Input.Key)
		}
		if a.Kind != "" && kind != a.Kind {
			return fmt.Errorf("input key %q maps to %q but action kind is %q", a.Input.Key, kind, a.Kind)
		}
		return m.Game.Apply(kind)
	default:
		return fmt.Errorf("mock executor does not support input type %q", a.Input.Type)
	}
}

func (m *MockExecutor) Describe() string { return "mock-keyboard" }
func (m *MockExecutor) Close() error     { return nil }

// Limiter enforces a maximum action rate, independent of the decision tick.
type Limiter struct {
	mu       sync.Mutex
	interval time.Duration
	next     time.Time
}

func NewLimiter(perSecond int) *Limiter {
	if perSecond <= 0 {
		perSecond = 8
	}
	return &Limiter{interval: time.Second / time.Duration(perSecond)}
}

func (l *Limiter) Wait(ctx context.Context) error {
	l.mu.Lock()
	now := time.Now()
	if l.next.IsZero() || now.After(l.next) {
		l.next = now.Add(l.interval)
		l.mu.Unlock()
		return nil
	}
	wait := l.next.Sub(now)
	l.next = l.next.Add(l.interval)
	l.mu.Unlock()
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
