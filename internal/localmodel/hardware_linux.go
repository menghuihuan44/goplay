//go:build linux || android

package localmodel

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

func totalMemoryBytes() (uint64, error) {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(line, "MemTotal:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		kb, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("parse /proc/meminfo: %w", err)
		}
		return kb * 1024, nil
	}
	return 0, fmt.Errorf("/proc/meminfo: MemTotal not found")
}

func detectChipModel() string {
	if v := strings.TrimSpace(os.Getenv("GOPLAY_CHIP_MODEL")); v != "" {
		return v
	}
	b, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(strings.ToLower(key)) {
		case "model name", "hardware", "processor":
			if v := strings.TrimSpace(val); v != "" && !isNumeric(v) {
				return v
			}
		}
	}
	return ""
}

func isNumeric(s string) bool {
	_, err := strconv.Atoi(s)
	return err == nil
}
