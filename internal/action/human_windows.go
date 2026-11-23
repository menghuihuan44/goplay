//go:build windows

package action

import (
	"context"
	"syscall"
	"time"
	"unsafe"
)

var (
	user32               = syscall.NewLazyDLL("user32.dll")
	kernel32             = syscall.NewLazyDLL("kernel32.dll")
	procGetLastInputInfo = user32.NewProc("GetLastInputInfo")
	procGetTickCount64   = kernel32.NewProc("GetTickCount64")
)

type lastInputInfo struct {
	cbSize uint32
	dwTime uint32
}

// SystemHuman detects real keyboard/mouse activity on Windows without hooks or
// injection, using GetLastInputInfo (the documented idle-time API).
type SystemHuman struct {
	IdleAfter time.Duration
}

func NewSystemHuman() *SystemHuman { return &SystemHuman{IdleAfter: 1200 * time.Millisecond} }

func (s *SystemHuman) State(ctx context.Context) HumanState {
	li := lastInputInfo{cbSize: uint32(unsafe.Sizeof(lastInputInfo{}))}
	r, _, _ := procGetLastInputInfo.Call(uintptr(unsafe.Pointer(&li)))
	if r == 0 {
		return HumanState{}
	}
	tick, _, _ := procGetTickCount64.Call()
	if tick == 0 {
		return HumanState{}
	}
	now := uint64(tick)
	last := uint64(li.dwTime)
	idle := now - last
	limit := s.IdleAfter
	if limit <= 0 {
		limit = 1200 * time.Millisecond
	}
	if time.Duration(idle)*time.Millisecond > limit {
		return HumanState{}
	}
	return HumanState{
		Active: true,
		Since:  time.Now().Add(-time.Duration(idle) * time.Millisecond),
		Reason: "windows last-input idle time",
	}
}
