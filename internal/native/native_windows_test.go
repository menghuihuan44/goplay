//go:build windows

package native

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrimaryScreenSize(t *testing.T) {
	w, h := PrimaryScreenSize()
	if w <= 0 || h <= 0 {
		t.Fatalf("screen size=%dx%d", w, h)
	}
}

func TestCaptureSmokeIfDLLBuilt(t *testing.T) {
	dll := filepath.Join("..", "..", "native", "build", "goplay_native.dll")
	if _, err := os.Stat(dll); err != nil {
		t.Skip("native/build/goplay_native.dll not built")
	}
	src, err := OpenScreenSource(dll, 0)
	if err != nil {
		t.Skipf("capture unavailable in this session: %v", err)
	}
	defer src.Close()
	f, err := src.Grab()
	if err != nil {
		t.Skipf("capture failed in this session: %v", err)
	}
	if f.Width <= 0 || f.Height <= 0 || len(f.RGBA) != f.Width*f.Height*4 {
		t.Fatalf("bad frame: %dx%d bytes=%d", f.Width, f.Height, len(f.RGBA))
	}
	if f.Source != "screen" {
		t.Fatalf("source=%q", f.Source)
	}
}
