//go:build windows

package action

import (
	"context"
	"fmt"
	"time"

	"goplay/internal/domain"
	"goplay/internal/native"
)

// WindowsExecutor drives the focused game window through SendInput. It never
// targets a process: keyboard and mouse events go through the OS input queue.
type WindowsExecutor struct {
	In  *native.Input
	DLL string
}

func NewWindowsExecutor(dll string, width, height int) (*WindowsExecutor, error) {
	in, err := native.OpenInput(dll, width, height)
	if err != nil {
		return nil, err
	}
	return &WindowsExecutor{In: in, DLL: dll}, nil
}

func (e *WindowsExecutor) Describe() string { return "windows-sendinput" }

func (e *WindowsExecutor) Close() error { return nil }

func (e *WindowsExecutor) Do(ctx context.Context, a domain.Action) error {
	if e.In == nil {
		return fmt.Errorf("windows executor: input not initialized")
	}
	in := a.Input
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
