//go:build darwin

package localmodel

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

func sysctlValue(name string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "sysctl", "-n", name).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func totalMemoryBytes() (uint64, error) {
	v, err := sysctlValue("hw.memsize")
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse hw.memsize %q: %w", v, err)
	}
	return n, nil
}

func detectChipModel() string {
	if v := strings.TrimSpace(os.Getenv("GOPLAY_CHIP_MODEL")); v != "" {
		return v
	}
	if v, err := sysctlValue("machdep.cpu.brand_string"); err == nil && v != "" {
		return v
	}
	if v, err := sysctlValue("hw.model"); err == nil {
		return v
	}
	return ""
}
