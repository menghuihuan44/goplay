package localmodel

import (
	"runtime"
	"strings"
	"testing"
)

func TestRecommendTierThresholds(t *testing.T) {
	cases := []struct {
		ram  uint64
		want Tier
	}{
		{0, TierLow},
		{4 * GiB, TierLow},
		{8 * GiB, TierLow},
		{15*GiB - 1, TierLow},
		{15 * GiB, TierMedium},
		{16 * GiB, TierMedium},
		{31*GiB - 1, TierMedium},
		{31 * GiB, TierHigh},
		{64 * GiB, TierHigh},
	}
	for _, c := range cases {
		if got := RecommendTier(c.ram); got != c.want {
			t.Errorf("RecommendTier(%d GiB) = %q, want %q", c.ram/GiB, got, c.want)
		}
	}
	if TierMinRAM(TierLow) != 8*GiB || TierMinRAM(TierMedium) != 16*GiB || TierMinRAM(TierHigh) != 32*GiB {
		t.Errorf("TierMinRAM values drifted")
	}
}

func TestRecommendRespectsPreferenceAndApple(t *testing.T) {
	low := HardwareInfo{OS: "windows", Arch: "amd64", RAMBytes: 8 * GiB, ChipModel: "Intel"}
	rec, err := Recommend(low, "")
	if err != nil || rec.Tier != TierLow {
		t.Fatalf("8GiB Intel = %+v err=%v", rec, err)
	}
	if !strings.Contains(rec.Rationale, "8.0 GiB") {
		t.Errorf("rationale = %q", rec.Rationale)
	}
	rec, err = Recommend(low, "high")
	if err != nil || rec.Tier != TierHigh {
		t.Fatalf("explicit high = %+v err=%v", rec, err)
	}
	if !strings.Contains(rec.Rationale, "below comfortable minimum") {
		t.Errorf("override rationale should warn: %q", rec.Rationale)
	}
	if _, err := Recommend(low, "ultra"); err == nil {
		t.Errorf("invalid preferred tier should fail")
	}

	apple := HardwareInfo{OS: "darwin", Arch: "arm64", RAMBytes: 8 * GiB, ChipModel: "Apple M2"}
	rec, err = Recommend(apple, "auto")
	if err != nil || rec.Tier != TierMedium {
		t.Fatalf("Apple Silicon bump = %+v err=%v", rec, err)
	}
	if !strings.Contains(rec.Rationale, "Apple Silicon") {
		t.Errorf("Apple rationale = %q", rec.Rationale)
	}
	// Explicit low must win over the Apple bump.
	rec, err = Recommend(apple, "low")
	if err != nil || rec.Tier != TierLow {
		t.Fatalf("explicit low on Apple = %+v err=%v", rec, err)
	}
}

func TestRecommendedThreads(t *testing.T) {
	cases := map[int]int{-1: 4, 0: 4, 1: 1, 2: 2, 4: 4, 8: 8, 16: 8}
	for in, want := range cases {
		if got := RecommendedThreads(in); got != want {
			t.Errorf("RecommendedThreads(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestDetectReturnsUsableHardware(t *testing.T) {
	hw, err := Detect()
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if hw.OS != runtime.GOOS || hw.Arch != runtime.GOARCH {
		t.Errorf("detected %s/%s, want %s/%s", hw.OS, hw.Arch, runtime.GOOS, runtime.GOARCH)
	}
	if hw.RAMBytes < 1<<20 || hw.RAMGB <= 0 {
		t.Errorf("RAM detection looks wrong: %+v", hw)
	}
	if hw.Cores < 1 || hw.Threads < 1 || hw.Threads > 8 {
		t.Errorf("CPU detection looks wrong: %+v", hw)
	}
}
