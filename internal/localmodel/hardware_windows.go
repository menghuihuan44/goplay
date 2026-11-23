//go:build windows

package localmodel

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

var procGlobalMemoryStatusEx = syscall.NewLazyDLL("kernel32.dll").NewProc("GlobalMemoryStatusEx")

func totalMemoryBytes() (uint64, error) {
	var m memoryStatusEx
	m.Length = uint32(unsafe.Sizeof(m))
	r, _, err := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&m)))
	if r == 0 {
		return 0, err
	}
	return m.TotalPhys, nil
}

// detectChipModel checks an explicit override first, then the PROCESSOR_IDENTIFIER
// environment variable, then the registry value used by Task Manager.
func detectChipModel() string {
	for _, key := range []string{"GOPLAY_CHIP_MODEL", "PROCESSOR_IDENTIFIER"} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "reg", "query",
		`HKLM\HARDWARE\DESCRIPTION\System\CentralProcessor\0`,
		"/v", "ProcessorNameString").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(line, "ProcessorNameString") {
			continue
		}
		if i := strings.Index(line, "REG_SZ"); i >= 0 {
			return strings.TrimSpace(line[i+len("REG_SZ"):])
		}
	}
	return ""
}
