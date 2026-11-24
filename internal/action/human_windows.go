//go:build windows

package action

import (
	"context"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

var (
	user32               = syscall.NewLazyDLL("user32.dll")
	kernel32             = syscall.NewLazyDLL("kernel32.dll")
	procGetLastInputInfo = user32.NewProc("GetLastInputInfo")
	procGetTickCount64   = kernel32.NewProc("GetTickCount64")
	procGetForegroundWin = user32.NewProc("GetForegroundWindow")
	procGetWindowTextW   = user32.NewProc("GetWindowTextW")
)

type lastInputInfo struct {
	cbSize uint32
	dwTime uint32
}

// SystemHuman detects real keyboard/mouse activity on Windows without hooks or
// injection, using GetLastInputInfo (the documented idle-time API).
//
// Two filters make the detector usable by an agent that injects its own input:
//
//   - MarkInjected records the input-queue timestamp produced by our SendInput
//     call. State ignores that timestamp (and anything older), so the agent does
//     not mistake its own keystroke for a human takeover.
//   - SetTargetTitles restricts human detection to the game window. Clicking
//     GoPlay, a browser or another program no longer pauses the agent.
//
// Both filters are pure window/input-queue metadata: no process handle is
// opened and no game memory is read.
type SystemHuman struct {
	IdleAfter time.Duration

	mu            sync.Mutex
	ignoreThrough uint32
	targetTitles  []string
}

func NewSystemHuman() *SystemHuman { return &SystemHuman{IdleAfter: 1200 * time.Millisecond} }

// MarkInjected remembers the timestamp of the most recent synthetic input. The
// engine calls it immediately after a successful SendInput action.
func (s *SystemHuman) MarkInjected() {
	li := lastInputInfo{cbSize: uint32(unsafe.Sizeof(lastInputInfo{}))}
	r, _, _ := procGetLastInputInfo.Call(uintptr(unsafe.Pointer(&li)))
	if r == 0 {
		return
	}
	s.mu.Lock()
	if li.dwTime > s.ignoreThrough {
		s.ignoreThrough = li.dwTime
	}
	s.mu.Unlock()
}

// SetTargetTitles limits human detection to windows whose title contains one of
// the given substrings (case-insensitive). Empty clears the restriction.
func (s *SystemHuman) SetTargetTitles(titles []string) {
	clean := make([]string, 0, len(titles))
	for _, t := range titles {
		if t = strings.TrimSpace(t); t != "" {
			clean = append(clean, strings.ToLower(t))
		}
	}
	s.mu.Lock()
	s.targetTitles = clean
	s.mu.Unlock()
}

func (s *SystemHuman) State(ctx context.Context) HumanState {
	title := foregroundWindowTitle()
	s.mu.Lock()
	targets := append([]string(nil), s.targetTitles...)
	ignoreThrough := s.ignoreThrough
	s.mu.Unlock()

	if len(targets) > 0 {
		lower := strings.ToLower(title)
		matched := false
		for _, t := range targets {
			if strings.Contains(lower, t) {
				matched = true
				break
			}
		}
		if !matched {
			return HumanState{}
		}
	} else if isGoPlayWindow(title) {
		// Without an explicit game target, at least never treat the agent's own
		// console as the game.
		return HumanState{}
	}

	li := lastInputInfo{cbSize: uint32(unsafe.Sizeof(lastInputInfo{}))}
	r, _, _ := procGetLastInputInfo.Call(uintptr(unsafe.Pointer(&li)))
	if r == 0 {
		return HumanState{}
	}
	// Everything up to ignoreThrough was injected by us. A real key press after
	// the action produces a newer dwTime and is detected normally.
	if li.dwTime <= ignoreThrough {
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
		Reason: "windows last-input idle time in target window",
	}
}

func foregroundWindowTitle() string {
	hwnd, _, _ := procGetForegroundWin.Call()
	if hwnd == 0 {
		return ""
	}
	buf := make([]uint16, 512)
	n, _, _ := procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if n == 0 {
		return ""
	}
	return syscall.UTF16ToString(buf[:n])
}

func isGoPlayWindow(title string) bool {
	lower := strings.ToLower(title)
	return strings.Contains(lower, "goplay")
}
