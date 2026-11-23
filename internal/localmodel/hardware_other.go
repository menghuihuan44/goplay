//go:build !windows && !linux && !android && !darwin

package localmodel

import (
	"fmt"
	"runtime"
)

func totalMemoryBytes() (uint64, error) {
	return 0, fmt.Errorf("RAM detection is not implemented for %s/%s", runtime.GOOS, runtime.GOARCH)
}

func detectChipModel() string { return "" }
