//go:build windows

// Package native binds the optional goplay_native.dll helper. The DLL only uses
// documented screen-capture and SendInput APIs; this package never opens a game
// process, reads its memory, or hooks a graphics API.
package native

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"goplay/internal/domain"
)

var (
	ErrUnavailable = errors.New("native: goplay_native.dll is not available")
	loadMu         sync.Mutex
)

func findDLL(explicit string) (string, error) {
	candidates := []string{}
	if explicit != "" {
		candidates = append(candidates, explicit)
	}
	if v := os.Getenv("GOPLAY_NATIVE_DLL"); v != "" {
		candidates = append(candidates, v)
	}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidates = append(candidates, filepath.Join(dir, "goplay_native.dll"))
		candidates = append(candidates, filepath.Join(dir, "native", "build", "goplay_native.dll"))
	}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates,
			filepath.Join(wd, "goplay_native.dll"),
			filepath.Join(wd, "native", "build", "goplay_native.dll"))
	}
	for _, p := range candidates {
		if p == "" {
			continue
		}
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
			abs, _ := filepath.Abs(p)
			return abs, nil
		}
	}
	return "", fmt.Errorf("%w: build native/ with build.ps1, or set GOPLAY_NATIVE_DLL", ErrUnavailable)
}

type dll struct {
	lib *syscall.LazyDLL
}

// PrimaryScreenSize reports the physical primary-monitor size used to map
// normalized action coordinates onto SendInput pixels. It reads only metrics,
// not any game state.
func PrimaryScreenSize() (int, int) {
	user32 := syscall.NewLazyDLL("user32.dll")
	get := user32.NewProc("GetSystemMetrics")
	cx, _, _ := get.Call(0) // SM_CXSCREEN
	cy, _, _ := get.Call(1) // SM_CYSCREEN
	if int32(cx) <= 0 || int32(cy) <= 0 {
		return 1920, 1080
	}
	return int(cx), int(cy)
}

func loadDLL(explicit string) (*dll, error) {
	path, err := findDLL(explicit)
	if err != nil {
		return nil, err
	}
	loadMu.Lock()
	defer loadMu.Unlock()
	lib := syscall.NewLazyDLL(path)
	if err := lib.Load(); err != nil {
		return nil, fmt.Errorf("native: load %s: %w", path, err)
	}
	return &dll{lib: lib}, nil
}

// ScreenSource captures a monitor with GDI/DXGI. It is an external,
// non-injected perception source.
type ScreenSource struct {
	cap     *dll
	monitor int
	mu      sync.Mutex
	closed  bool
}

var captureMu sync.Mutex // gp_capture_init is process-global.

func OpenScreenSource(dllPath string, monitor int) (*ScreenSource, error) {
	d, err := loadDLL(dllPath)
	if err != nil {
		return nil, err
	}
	init := d.lib.NewProc("gp_capture_init")
	rc, _, callErr := init.Call(uintptr(monitor))
	if rc != 0 {
		return nil, fmt.Errorf("native: gp_capture_init(%d) failed: %v %v", monitor, rc, callErr)
	}
	return &ScreenSource{cap: d, monitor: monitor}, nil
}

func (s *ScreenSource) Grab() (domain.Frame, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return domain.Frame{}, errors.New("native: capture closed")
	}
	var data unsafe.Pointer
	var w, h, stride int32
	proc := s.cap.lib.NewProc("gp_capture_frame")
	rc, _, callErr := proc.Call(
		uintptr(unsafe.Pointer(&data)),
		uintptr(unsafe.Pointer(&w)),
		uintptr(unsafe.Pointer(&h)),
		uintptr(unsafe.Pointer(&stride)),
	)
	if rc != 0 || data == nil || w <= 0 || h <= 0 {
		return domain.Frame{}, fmt.Errorf("native: gp_capture_frame failed: rc=%d %v", rc, callErr)
	}
	defer s.cap.lib.NewProc("gp_capture_free").Call(uintptr(data))
	n := int(w) * int(h) * 4
	bgra := unsafe.Slice((*byte)(data), n)
	rgba := make([]byte, n)
	for i := 0; i+3 < n; i += 4 {
		rgba[i] = bgra[i+2]
		rgba[i+1] = bgra[i+1]
		rgba[i+2] = bgra[i]
		rgba[i+3] = 255
	}
	return domain.Frame{Width: int(w), Height: int(h), RGBA: rgba, Source: "screen", At: time.Now()}, nil
}

func (s *ScreenSource) Describe() string {
	return "windows-gdi/dxgi"
}

func (s *ScreenSource) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	captureMu.Lock()
	s.cap.lib.NewProc("gp_capture_close").Call()
	captureMu.Unlock()
	return nil
}

// Input sends keyboard and mouse events through SendInput.
type Input struct {
	cap  *dll
	mu   sync.Mutex
	init bool
	w, h int
}

func OpenInput(dllPath string, width, height int) (*Input, error) {
	d, err := loadDLL(dllPath)
	if err != nil {
		return nil, err
	}
	rc, _, callErr := d.lib.NewProc("gp_input_init").Call()
	if rc != 0 {
		return nil, fmt.Errorf("native: gp_input_init failed: rc=%d %v", rc, callErr)
	}
	if width <= 0 {
		width = 1920
	}
	if height <= 0 {
		height = 1080
	}
	return &Input{cap: d, init: true, w: width, h: height}, nil
}

func vkFor(name string) (uint32, bool) {
	names := map[string]uint32{
		"a": 0x41, "b": 0x42, "c": 0x43, "d": 0x44, "e": 0x45, "f": 0x46,
		"g": 0x47, "h": 0x48, "i": 0x49, "j": 0x4A, "k": 0x4B, "l": 0x4C,
		"m": 0x4D, "n": 0x4E, "o": 0x4F, "p": 0x50, "q": 0x51, "r": 0x52,
		"s": 0x53, "t": 0x54, "u": 0x55, "v": 0x56, "w": 0x57, "x": 0x58,
		"y": 0x59, "z": 0x5A,
		"0": 0x30, "1": 0x31, "2": 0x32, "3": 0x33, "4": 0x34,
		"5": 0x35, "6": 0x36, "7": 0x37, "8": 0x38, "9": 0x39,
		"space": 0x20, "enter": 0x0D, "return": 0x0D, "esc": 0x1B,
		"escape": 0x1B, "tab": 0x09, "backspace": 0x08, "shift": 0x10,
		"ctrl": 0x11, "alt": 0x12, "up": 0x26, "down": 0x28, "left": 0x25,
		"right": 0x27, "f1": 0x70, "f2": 0x71, "f3": 0x72, "f4": 0x73,
	}
	v, ok := names[name]
	return v, ok
}

func (i *Input) PressKey(name string, holdMS int) error {
	vk, ok := vkFor(name)
	if !ok {
		return fmt.Errorf("native: unknown key %q", name)
	}
	if holdMS <= 0 {
		holdMS = 25
	}
	rc, _, callErr := i.cap.lib.NewProc("gp_input_key_press").Call(uintptr(vk), uintptr(holdMS))
	if rc != 0 {
		return fmt.Errorf("native: gp_input_key_press(%s): rc=%d %v", name, rc, callErr)
	}
	return nil
}

func (i *Input) Click(nx, ny float64, button int, count int) error {
	if nx < 0 || nx > 1 || ny < 0 || ny > 1 {
		return fmt.Errorf("native: normalized click out of range: %v,%v", nx, ny)
	}
	x, y := int(nx*float64(i.w)), int(ny*float64(i.h))
	if count <= 0 {
		count = 1
	}
	rc, _, callErr := i.cap.lib.NewProc("gp_input_click").Call(uintptr(x), uintptr(y), uintptr(button), uintptr(count))
	if rc != 0 {
		return fmt.Errorf("native: gp_input_click(%d,%d): rc=%d %v", x, y, rc, callErr)
	}
	return nil
}

func (i *Input) TypeText(text string) error {
	p, err := syscall.UTF16PtrFromString(text)
	if err != nil {
		return err
	}
	rc, _, callErr := i.cap.lib.NewProc("gp_input_text").Call(uintptr(unsafe.Pointer(p)))
	if rc != 0 {
		return fmt.Errorf("native: gp_input_text: rc=%d %v", rc, callErr)
	}
	return nil
}
