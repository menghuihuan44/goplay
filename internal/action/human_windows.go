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
	procGetAsyncKeyState = user32.NewProc("GetAsyncKeyState")
	procGetCursorPos     = user32.NewProc("GetCursorPos")
	procGetSystemMetrics = user32.NewProc("GetSystemMetrics")
)

type lastInputInfo struct {
	cbSize uint32
	dwTime uint32
}

type point struct {
	X int32
	Y int32
}

type recordedEvent struct {
	ev   InputEvent
	tick uint32
}

// SystemHuman detects real keyboard/mouse activity on Windows without hooks or
// injection, using GetLastInputInfo (the documented idle-time API) plus
// GetAsyncKeyState for the individual keys. It also records those keys and
// clicks so the teaching layer can learn from a human demonstration.
//
// Three filters make it safe for an agent that injects its own input:
//
//   - MarkInjected records the input-queue timestamp produced by our SendInput
//     call. State and DrainInputEvents ignore that timestamp (and anything
//     older), so the agent does not mistake its own keystroke for a human.
//   - SetTargetTitles restricts detection and recording to the game window.
//     Clicking GoPlay, a browser or another program no longer pauses the agent.
//   - Recording is disabled when no target title is set, so a default instance
//     never captures the user's desktop input.
//
// All of this is window/input-queue metadata: no process handle is opened and
// no game memory is read.
type SystemHuman struct {
	IdleAfter time.Duration

	mu            sync.Mutex
	ignoreThrough uint32
	targetTitles  []string
	events        []recordedEvent
	lastHumanAt   time.Time
	lastHumanTick uint32

	prevKeys    map[int]bool
	lastKeyAt   map[int]time.Time
	prevButtons map[int]bool
	stop        chan struct{}
	stopOnce    sync.Once
}

func NewSystemHuman() *SystemHuman {
	s := &SystemHuman{
		IdleAfter:   1200 * time.Millisecond,
		prevKeys:    make(map[int]bool, 32),
		lastKeyAt:   make(map[int]time.Time, 32),
		prevButtons: make(map[int]bool, 4),
		stop:        make(chan struct{}),
	}
	go s.pollInput()
	return s
}

// Close stops the input sampler. It is idempotent.
func (s *SystemHuman) Close() error {
	s.stopOnce.Do(func() { close(s.stop) })
	return nil
}

// MarkInjected remembers the timestamp of the most recent synthetic input. The
// engine calls it immediately after a successful SendInput action.
func (s *SystemHuman) MarkInjected() {
	li := lastInputInfo{cbSize: uint32(unsafe.Sizeof(lastInputInfo{}))}
	r, _, _ := procGetLastInputInfo.Call(uintptr(unsafe.Pointer(&li)))
	nowTick, _, _ := procGetTickCount64.Call()
	if r == 0 && nowTick == 0 {
		return
	}
	// GetLastInputInfo can lag a synthesized SendInput by a few milliseconds.
	// Ignore through the current tick plus a tiny grace window instead of only
	// the last-input timestamp, otherwise our own late key-down is recorded as
	// a human demonstration and immediately re-triggers takeover.
	through := uint32(nowTick) + 40
	if r != 0 && li.dwTime > through {
		through = li.dwTime
	}
	s.mu.Lock()
	if through > s.ignoreThrough {
		s.ignoreThrough = through
	}
	kept := s.events[:0]
	for _, e := range s.events {
		if e.tick > s.ignoreThrough {
			kept = append(kept, e)
		}
	}
	s.events = kept
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

// DrainInputEvents returns the human events observed since the previous drain.
// Events produced by the agent's own injection are filtered out.
func (s *SystemHuman) DrainInputEvents() []InputEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]InputEvent, 0, len(s.events))
	for _, e := range s.events {
		if e.tick <= s.ignoreThrough {
			continue
		}
		out = append(out, e.ev)
	}
	// Delivered events must leave the buffer, otherwise every later drain
	// replays them and the teaching recorder sees the same key over and over.
	s.events = s.events[:0]
	return out
}

func (s *SystemHuman) State(ctx context.Context) HumanState {
	title := foregroundWindowTitle()
	targets, ignoreThrough := s.snapshot()
	if !targetMatches(targets, title) {
		return HumanState{}
	}
	if isGoPlayWindow(title) {
		return HumanState{}
	}

	// Do not use GetLastInputInfo alone here. Some DirectDraw/DirectInput games
	// warp or clip the cursor every frame (or otherwise synthesize queue input)
	// while the user is not touching anything. That keeps the system idle timer
	// near zero and would pause the agent forever. We instead trust the polled
	// keyboard/mouse buttons, which are the inputs a human actually plays with.
	s.mu.Lock()
	lastHumanAt := s.lastHumanAt
	lastHumanTick := s.lastHumanTick
	s.mu.Unlock()
	if lastHumanTick <= ignoreThrough {
		return HumanState{}
	}
	limit := s.IdleAfter
	if limit <= 0 {
		limit = 1200 * time.Millisecond
	}
	idle := time.Since(lastHumanAt)
	if idle > limit {
		return HumanState{}
	}
	return HumanState{
		Active: true,
		Since:  lastHumanAt,
		Reason: "windows keyboard/mouse input in target window",
	}
}

func (s *SystemHuman) snapshot() ([]string, uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.targetTitles...), s.ignoreThrough
}

func targetMatches(targets []string, title string) bool {
	if len(targets) == 0 {
		return false
	}
	lower := strings.ToLower(title)
	for _, t := range targets {
		if strings.Contains(lower, t) {
			return true
		}
	}
	return false
}

func (s *SystemHuman) pollInput() {
	ticker := time.NewTicker(12 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
		}
		targets, _ := s.snapshot()
		title := foregroundWindowTitle()
		if !targetMatches(targets, title) {
			s.resetPressed()
			continue
		}
		s.scanKeys()
		s.scanMouse()
	}
}

func (s *SystemHuman) resetPressed() {
	s.mu.Lock()
	s.prevKeys = make(map[int]bool, 32)
	s.prevButtons = make(map[int]bool, 4)
	s.mu.Unlock()
}

var polledKeys = map[int]string{
	0x41: "a", 0x42: "b", 0x43: "c", 0x44: "d", 0x45: "e", 0x46: "f",
	0x47: "g", 0x48: "h", 0x49: "i", 0x4A: "j", 0x4B: "k", 0x4C: "l",
	0x4D: "m", 0x4E: "n", 0x4F: "o", 0x50: "p", 0x51: "q", 0x52: "r",
	0x53: "s", 0x54: "t", 0x55: "u", 0x56: "v", 0x57: "w", 0x58: "x",
	0x59: "y", 0x5A: "z",
	0x30: "0", 0x31: "1", 0x32: "2", 0x33: "3", 0x34: "4",
	0x35: "5", 0x36: "6", 0x37: "7", 0x38: "8", 0x39: "9",
	0x0D: "enter", 0x1B: "esc", 0x20: "space", 0x09: "tab", 0x08: "backspace",
	0x10: "shift", 0x11: "ctrl", 0x12: "alt",
	0x25: "left", 0x26: "up", 0x27: "right", 0x28: "down",
	0x70: "f1", 0x71: "f2", 0x72: "f3", 0x73: "f4",
}

func (s *SystemHuman) scanKeys() {
	tick := lastInputTick()
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for vk, name := range polledKeys {
		down := keyDown(vk)
		prev := s.prevKeys[vk]
		// A hold must produce one key-down, even if focus briefly flips and
		// resetPressed() clears the edge map underneath us. Duplicate edges
		// used to flood the teaching recorder and exhaust its step budget.
		repeated := now.Sub(s.lastKeyAt[vk]) < 180*time.Millisecond
		if down && tick > s.ignoreThrough {
			s.lastHumanAt = now
			s.lastHumanTick = tick
		}
		if down && !prev && tick > s.ignoreThrough && !repeated {
			s.pushLocked(InputEvent{Type: "key", Key: name, At: now}, tick)
			s.lastKeyAt[vk] = now
		}
		s.prevKeys[vk] = down
	}
}

func (s *SystemHuman) scanMouse() {
	w, _, _ := procGetSystemMetrics.Call(0) // SM_CXSCREEN
	h, _, _ := procGetSystemMetrics.Call(1) // SM_CYSCREEN
	if w == 0 || h == 0 {
		return
	}
	var p point
	if ok, _, _ := procGetCursorPos.Call(uintptr(unsafe.Pointer(&p))); ok == 0 {
		return
	}
	tick := lastInputTick()
	now := time.Now()
	x := float64(p.X) / float64(w)
	y := float64(p.Y) / float64(h)
	if x < 0 {
		x = 0
	}
	if x > 1 {
		x = 1
	}
	if y < 0 {
		y = 0
	}
	if y > 1 {
		y = 1
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for vk, name := range map[int]string{0x01: "left", 0x02: "right", 0x04: "middle"} {
		down := keyDown(vk)
		prev := s.prevButtons[vk]
		if down && tick > s.ignoreThrough {
			s.lastHumanAt = now
			s.lastHumanTick = tick
		}
		if down && !prev && tick > s.ignoreThrough {
			s.pushLocked(InputEvent{Type: "mouse_down", Button: name, X: x, Y: y, At: now}, tick)
		}
		s.prevButtons[vk] = down
	}
}

func (s *SystemHuman) pushLocked(ev InputEvent, tick uint32) {
	if len(s.events) >= 512 {
		copy(s.events, s.events[len(s.events)-256:])
		s.events = s.events[:256]
	}
	s.events = append(s.events, recordedEvent{ev: ev, tick: tick})
}

func keyDown(vk int) bool {
	r, _, _ := procGetAsyncKeyState.Call(uintptr(vk))
	return r&0x8000 != 0
}

func lastInputTick() uint32 {
	li := lastInputInfo{cbSize: uint32(unsafe.Sizeof(lastInputInfo{}))}
	r, _, _ := procGetLastInputInfo.Call(uintptr(unsafe.Pointer(&li)))
	if r == 0 {
		return 0
	}
	return li.dwTime
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
