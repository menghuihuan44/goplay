//go:build windows

package action

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"goplay/internal/domain"
	"goplay/internal/native"
)

// WindowsExecutor drives the focused game window through SendInput. It never
// targets a process: keyboard and mouse events go through the OS input queue.
//
// A target window title is mandatory. The executor refuses to inject anything
// unless that window is foreground, so an agent can never type into the chat
// window, a browser or another application when the game loses focus.
type WindowsExecutor struct {
	In  *native.Input
	DLL string

	mu     sync.Mutex
	titles []string
}

func NewWindowsExecutor(dll string, width, height int) (*WindowsExecutor, error) {
	in, err := native.OpenInput(dll, width, height)
	if err != nil {
		return nil, err
	}
	return &WindowsExecutor{In: in, DLL: dll}, nil
}

// SetTargetTitles binds the executor to one or more window-title substrings.
func (e *WindowsExecutor) SetTargetTitles(titles []string) {
	clean := make([]string, 0, len(titles))
	for _, t := range titles {
		if t = strings.TrimSpace(t); t != "" {
			clean = append(clean, strings.ToLower(t))
		}
	}
	e.mu.Lock()
	e.titles = clean
	e.mu.Unlock()
}

func (e *WindowsExecutor) targetFocused() (bool, string) {
	title := foregroundWindowTitle()
	e.mu.Lock()
	titles := append([]string(nil), e.titles...)
	e.mu.Unlock()
	if len(titles) == 0 {
		return false, "尚未绑定游戏窗口标题；请在实例设置里填写窗口标题（例如 Scarlet Devil）"
	}
	lower := strings.ToLower(title)
	for _, t := range titles {
		if strings.Contains(lower, t) {
			return true, title
		}
	}
	return false, fmt.Sprintf("游戏窗口不在前台（当前前台：%s）；为避免误操作，已暂停输入", strings.TrimSpace(title))
}

// TargetFocused reports whether the configured game window is foreground. The
// engine checks this before capturing so a background game cannot make the
// agent analyze whatever private window the user brought to the front.
func (e *WindowsExecutor) TargetFocused() (bool, string) { return e.targetFocused() }

func (e *WindowsExecutor) Describe() string { return "windows-sendinput" }

func (e *WindowsExecutor) Close() error { return nil }

func (e *WindowsExecutor) Do(ctx context.Context, a domain.Action) error {
	if e.In == nil {
		return fmt.Errorf("windows executor: input not initialized")
	}
	in := a.Input
	if in == nil {
		return fmt.Errorf("windows executor: action has no input spec")
	}
	if in.Type != "wait" {
		if ok, why := e.targetFocused(); !ok {
			return fmt.Errorf("%w: %s", ErrTargetNotFocused, why)
		}
	}
	switch in.Type {
	case "wait", "":
		d := time.Duration(in.DurationMS) * time.Millisecond
		if d <= 0 {
			d = 200 * time.Millisecond
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d):
		}
		return nil
	case "key":
		return e.In.PressKey(in.Key, in.DurationMS)
	case "text":
		return e.In.TypeText(in.Text)
	case "click", "tap":
		button := 0
		switch in.Button {
		case "right":
			button = 1
		case "middle":
			button = 2
		}
		return e.In.Click(in.X, in.Y, button, in.ClickCount)
	case "move":
		// A click at the same point is not equivalent to a move; avoid inventing
		// an action. Games driven only by mouse movement should use a profile
		// that emits click/tap with an explicit target.
		return fmt.Errorf("windows executor: move is not mapped; use click/tap")
	case "swipe":
		return fmt.Errorf("windows executor: swipe is not supported on desktop")
	default:
		return fmt.Errorf("windows executor: unsupported input type %q", in.Type)
	}
}
