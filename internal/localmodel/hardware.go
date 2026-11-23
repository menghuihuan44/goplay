package localmodel

import (
	"fmt"
	"math"
	"runtime"
	"strings"
)

// GiB is one gibibyte. RAM thresholds are expressed in GiB because that is how
// OS memory APIs report physical memory.
const GiB = uint64(1) << 30

// HardwareInfo is the subset of device facts used for tier selection. RAM is
// physical memory as reported by the OS; installed RAM is usually a little
// higher because firmware reserves some.
type HardwareInfo struct {
	OS        string  `json:"os"`
	Arch      string  `json:"arch"`
	RAMBytes  uint64  `json:"ram_bytes"`
	RAMGB     float64 `json:"ram_gb"`
	Cores     int     `json:"cores"`
	Threads   int     `json:"threads"`
	ChipModel string  `json:"chip_model,omitempty"`
}

// Detect gathers OS, architecture, physical RAM, CPU count and (where possible)
// the chip model. Chip detection is best-effort and never fatal.
func Detect() (HardwareInfo, error) {
	ram, err := totalMemoryBytes()
	if err != nil {
		return HardwareInfo{}, fmt.Errorf("localmodel: detect RAM: %w", err)
	}
	hw := HardwareInfo{
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
		RAMBytes:  ram,
		RAMGB:     round2(float64(ram) / float64(GiB)),
		Cores:     runtime.NumCPU(),
		ChipModel: detectChipModel(),
	}
	hw.Threads = RecommendedThreads(hw.Cores)
	return hw, nil
}

// RecommendedThreads caps CPU threads for llama.cpp. More threads than physical
// cores usually hurts CPU inference; 4-8 is the useful range for these models.
func RecommendedThreads(cores int) int {
	if cores <= 0 {
		return 4
	}
	if cores < 2 {
		return 1
	}
	if cores > 8 {
		return 8
	}
	return cores
}

// RecommendTier maps physical RAM to a tier. Thresholds include headroom so a
// 16 GiB machine (reported as ~15.5 GiB) is not demoted to the low tier.
func RecommendTier(ramBytes uint64) Tier {
	switch {
	case ramBytes < 15*GiB:
		return TierLow
	case ramBytes < 31*GiB:
		return TierMedium
	default:
		return TierHigh
	}
}

// TierMinRAM is the comfortable minimum RAM shown in the catalog.
func TierMinRAM(t Tier) uint64 {
	switch t {
	case TierLow:
		return 8 * GiB
	case TierMedium:
		return 16 * GiB
	case TierHigh:
		return 32 * GiB
	default:
		return 8 * GiB
	}
}

// Recommendation is the result of hardware detection plus (optional) user
// preference. The catalog plan is looked up separately by the caller/CLI.
type Recommendation struct {
	Hardware    HardwareInfo `json:"hardware"`
	Tier        Tier         `json:"tier"`
	MinRAMBytes uint64       `json:"min_ram_bytes"`
	MinRAMGB    float64      `json:"min_ram_gb"`
	Rationale   string       `json:"rationale"`
}

// Recommend selects a tier. An explicit preferred tier (low/medium/high) wins
// over automatic selection but is annotated when the machine is below the
// comfortable minimum. "auto" or "" means automatic.
//
// The only chip-aware adjustment is a conservative one-step bump for Apple
// Silicon with at least 8 GiB. Unified memory and the efficiency cores make the
// medium model practical on 8 GiB Macs. No other vendor is special-cased.
func Recommend(hw HardwareInfo, preferred string) (Recommendation, error) {
	tier := RecommendTier(hw.RAMBytes)
	ram := fmt.Sprintf("%.1f GiB RAM", float64(hw.RAMBytes)/float64(GiB))
	reasons := []string{ram}

	if p := strings.TrimSpace(preferred); p != "" && !strings.EqualFold(p, "auto") {
		t, ok := ParseTier(p)
		if !ok {
			return Recommendation{}, fmt.Errorf("localmodel: invalid tier %q (want auto|low|medium|high)", preferred)
		}
		tier = t
		reasons = append(reasons, "explicit --tier override")
	} else if tier == TierLow && hw.Arch == "arm64" &&
		strings.Contains(strings.ToLower(hw.ChipModel), "apple m") &&
		hw.RAMBytes >= 8*GiB {
		tier = TierMedium
		reasons = append(reasons, "Apple Silicon efficiency allows one tier up")
	}

	minRAM := TierMinRAM(tier)
	if hw.RAMBytes > 0 && hw.RAMBytes < minRAM {
		reasons = append(reasons, fmt.Sprintf("below comfortable minimum %.0f GiB; expect slower tokens", float64(minRAM)/float64(GiB)))
	}
	return Recommendation{
		Hardware:    hw,
		Tier:        tier,
		MinRAMBytes: minRAM,
		MinRAMGB:    float64(minRAM) / float64(GiB),
		Rationale:   strings.Join(reasons, "; "),
	}, nil
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }
