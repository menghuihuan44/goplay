//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

var (
	user32                = syscall.NewLazyDLL("user32.dll")
	procGetWindowRect     = user32.NewProc("GetWindowRect")
	procMonitorFromWindow = user32.NewProc("MonitorFromWindow")
	procGetMonitorInfo    = user32.NewProc("GetMonitorInfoW")
	procSetWindowPos      = user32.NewProc("SetWindowPos")
)

const (
	monitorDefaultToNearest = 2
	swpNoSize               = 0x0001
	swpNoZOrder             = 0x0004
	swpNoActivate           = 0x0010
)

type winRect struct {
	left, top, right, bottom int32
}

type monitorInfo struct {
	cbSize    uint32
	rcMonitor winRect
	rcWork    winRect
	dwFlags   uint32
}

// centerOnScreen moves an already sized window into the work area of its
// monitor. Windows positions the window before webview_go has applied the real
// DPI scale, so growing it afterwards can push the bottom below the screen.
func centerOnScreen(handle unsafe.Pointer) {
	if handle == nil {
		return
	}
	hwnd := uintptr(handle)
	var r winRect
	if ok, _, _ := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r))); ok == 0 {
		return
	}
	mon, _, _ := procMonitorFromWindow.Call(hwnd, monitorDefaultToNearest)
	if mon == 0 {
		return
	}
	mi := monitorInfo{cbSize: uint32(unsafe.Sizeof(monitorInfo{}))}
	if ok, _, _ := procGetMonitorInfo.Call(mon, uintptr(unsafe.Pointer(&mi))); ok == 0 {
		return
	}
	w := r.right - r.left
	h := r.bottom - r.top
	if w <= 0 || h <= 0 {
		return
	}
	x := mi.rcWork.left + (mi.rcWork.right-mi.rcWork.left-w)/2
	y := mi.rcWork.top + (mi.rcWork.bottom-mi.rcWork.top-h)/2
	procSetWindowPos.Call(hwnd, 0, uintptr(x), uintptr(y), 0, 0, swpNoSize|swpNoZOrder|swpNoActivate)
}
