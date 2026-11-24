//go:build !windows

package action

import (
	"context"
	"errors"

	"goplay/internal/domain"
)

// WindowsExecutor is a compile-time placeholder so the manager and CLI can be
// cross-compiled on non-Windows hosts. Creating one always fails; the native
// capture source is unavailable there as well.
type WindowsExecutor struct{}

func NewWindowsExecutor(dll string, width, height int) (*WindowsExecutor, error) {
	return nil, errors.New("windows executor is only available on Windows")
}

func (e *WindowsExecutor) SetTargetTitles([]string) {}

func (e *WindowsExecutor) Describe() string { return "windows-sendinput (unavailable)" }

func (e *WindowsExecutor) Close() error { return nil }

func (e *WindowsExecutor) Do(ctx context.Context, a domain.Action) error {
	return errors.New("windows executor is only available on Windows")
}
