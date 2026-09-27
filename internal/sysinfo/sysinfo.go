package sysinfo

import (
	"fmt"
	"math"
	"runtime"
	"sort"
	"strings"
)

type StaticInfo struct {
	Platform     string
	CPUModel     string
	CPUPhysical  int
	CPULogical   int
	Capabilities []string
}

type DynamicInfo struct {
	CPUUsedPct float64
	Load1      float64
	Load5      float64
	Load15     float64

	MemoryTotalMB     uint64
	MemoryAvailableMB uint64
	MemoryUsedMB      uint64
	MemoryUsedPct     float64
	MemoryPressurePct float64
	// Memory PSI stall percentages are populated on Linux when /proc/pressure/memory is available.
	// They are -1 on platforms where that signal is unavailable.
	MemoryStallSomePct float64
	MemoryStallFullPct float64
	CompressedMB       uint64
	SwapTotalMB        uint64
	SwapUsedMB         uint64
	UptimeSeconds      uint64
}

func Static() (StaticInfo, error) {
	// Preserve any platform fallback metadata even when the native probe is only
	// partially successful. Callers can keep a useful platform/core snapshot and
	// retry richer static telemetry later instead of publishing zeroed identity.
	info, err := platformStatic()
	if info.Platform == "" {
		info.Platform = runtime.GOOS + "/" + runtime.GOARCH
	}
	if info.CPULogical == 0 {
		info.CPULogical = runtime.NumCPU()
	}
	if info.CPUPhysical == 0 {
		info.CPUPhysical = info.CPULogical
	}
	info.Capabilities = normalizeCapabilities(info.Capabilities)
	return info, err
}

func Dynamic() (DynamicInfo, error) {
	info, err := platformDynamic()
	if err != nil {
		return DynamicInfo{}, err
	}
	if info.MemoryTotalMB > 0 {
		if info.MemoryAvailableMB > info.MemoryTotalMB {
			info.MemoryAvailableMB = info.MemoryTotalMB
		}
		info.MemoryUsedMB = info.MemoryTotalMB - info.MemoryAvailableMB
		info.MemoryUsedPct = 100 * float64(info.MemoryUsedMB) / float64(info.MemoryTotalMB)
	}
	return info, nil
}

func MemoryHeadroomPct(d DynamicInfo) float64 {
	// macOS exposes a native memory-pressure free percentage that better reflects
	// reclaimable/compressed memory than a literal free-byte ratio. Other
	// platforms fall back to available physical memory percentage.
	if d.MemoryPressurePct >= 0 {
		return math.Max(0, math.Min(100, d.MemoryPressurePct))
	}
	if d.MemoryTotalMB == 0 {
		return -1
	}
	return math.Max(0, math.Min(100, 100*float64(d.MemoryAvailableMB)/float64(d.MemoryTotalMB)))
}

func MemoryPressureLevel(d DynamicInfo) string {
	headroom := MemoryHeadroomPct(d)
	level := "UNKNOWN"
	if d.MemoryPressurePct >= 0 {
		// Preserve macOS native pressure semantics: below 12% free pressure
		// headroom was already considered pressured in earlier releases.
		switch {
		case headroom < 0:
			level = "UNKNOWN"
		case headroom < 12:
			level = "CRITICAL"
		case headroom < 25:
			level = "HIGH"
		case headroom < 45:
			level = "MODERATE"
		default:
			level = "LOW"
		}
	} else {
		switch {
		case headroom < 0:
			level = "UNKNOWN"
		case headroom < 8:
			level = "CRITICAL"
		case headroom < 18:
			level = "HIGH"
		case headroom < 30:
			level = "MODERATE"
		default:
			level = "LOW"
		}
	}

	// Linux PSI adds an actual contention signal on top of headroom. Avoid
	// pretending the raw PSI percentages are equivalent to macOS pressure; use
	// them only to raise the normalized severity when stalls are happening.
	if d.MemoryStallFullPct >= 1 || d.MemoryStallSomePct >= 5 {
		if level == "LOW" || level == "MODERATE" || level == "UNKNOWN" {
			level = "HIGH"
		}
	} else if d.MemoryStallSomePct >= 1 {
		if level == "LOW" || level == "UNKNOWN" {
			level = "MODERATE"
		}
	}
	return level
}

func ResourceState(d DynamicInfo) string {
	headroom := MemoryHeadroomPct(d)
	pressure := MemoryPressureLevel(d)
	if pressure == "CRITICAL" {
		return "pressured"
	}
	if d.CPUUsedPct >= 90 || pressure == "HIGH" || (headroom >= 0 && headroom < 18) {
		return "busy"
	}
	return "ready"
}

func ResourceScore(d DynamicInfo) float64 {
	cpuFree := 0.5
	if d.CPUUsedPct >= 0 {
		cpuFree = clamp01(1 - d.CPUUsedPct/100)
	}

	memFree := 0.5
	if d.MemoryTotalMB > 0 {
		memFree = clamp01(float64(d.MemoryAvailableMB) / float64(d.MemoryTotalMB))
	}

	pressure := memFree
	if h := MemoryHeadroomPct(d); h >= 0 {
		pressure = clamp01(h / 100)
	}
	// PSI is a contention penalty on Linux, not a replacement for headroom.
	if d.MemoryStallSomePct > 0 {
		pressure *= clamp01(1 - math.Min(d.MemoryStallSomePct, 20)/25)
	}

	// A high existing swap allocation is a weak penalty only. Swap usage can be
	// historical on macOS even after current memory pressure has recovered.
	swapPenalty := 0.0
	if d.SwapTotalMB > 0 {
		swapPenalty = 0.08 * clamp01(float64(d.SwapUsedMB)/float64(d.SwapTotalMB))
	}

	score := 0.55*cpuFree + 0.30*memFree + 0.15*pressure - swapPenalty
	return math.Round(clamp01(score)*100) / 100
}

func BaseCapabilities(osName, arch string, s StaticInfo, d DynamicInfo) []string {
	caps := append([]string{}, s.Capabilities...)
	caps = append(caps, "cpu", osName, arch)
	if osName == "darwin" && arch == "arm64" {
		caps = append(caps, "apple-silicon")
	}
	if arch == "arm64" {
		caps = append(caps, "arm64", "neon")
	}
	if arch == "amd64" {
		caps = append(caps, "x86_64")
	}
	if d.MemoryTotalMB > 0 {
		gb := int(math.Round(float64(d.MemoryTotalMB) / 1024))
		caps = append(caps, fmt.Sprintf("ram:%dgb", gb))
	}
	return normalizeCapabilities(caps)
}

func normalizeCapabilities(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, c := range in {
		c = strings.ToLower(strings.TrimSpace(c))
		if c == "" {
			continue
		}
		if _, ok := seen[c]; ok {
			continue
		}
		seen[c] = struct{}{}
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
