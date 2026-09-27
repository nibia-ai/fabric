package main

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/nibia-ai/fabric/internal/types"
)

// memoryReservePolicy keeps the public UX intentionally small:
//   - adaptive reserve is the default;
//   - --reserve-mb remains a backward-compatible global fixed override;
//   - --memory-reserve applies named per-node overrides such as
//     primary=2GiB,worker-a=20%,worker-b=768MiB.
//
// Adaptive reserve = 10% of physical RAM, with a 512 MiB floor and a 4 GiB cap.
// The reserve is subtracted from the conservative runtime/OS capacity basis.
type memoryReservePolicy struct {
	GlobalFixedMiB *uint64
	Overrides      map[string]memoryReserveSpec
}

type memoryReserveSpec struct {
	Kind    string // fixed | percent
	MiB     uint64
	Percent float64
	Raw     string
}

type resolvedMemoryReserve struct {
	MiB    uint64
	Source string
}

func parseMemoryReservePolicy(raw string, legacyFixedSet bool, legacyFixedMiB uint64) (memoryReservePolicy, error) {
	p := memoryReservePolicy{Overrides: map[string]memoryReserveSpec{}}
	if legacyFixedSet {
		v := legacyFixedMiB
		p.GlobalFixedMiB = &v
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return p, nil
	}
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		parts := strings.SplitN(item, "=", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
			return memoryReservePolicy{}, fmt.Errorf("invalid --memory-reserve entry %q; expected Node=2GiB, Node=768MiB, or Node=20%%", item)
		}
		key := strings.ToLower(strings.TrimSpace(parts[0]))
		spec, err := parseMemoryReserveSpec(parts[1])
		if err != nil {
			return memoryReservePolicy{}, fmt.Errorf("%s: %w", strings.TrimSpace(parts[0]), err)
		}
		p.Overrides[key] = spec
	}
	return p, nil
}

func parseMemoryReserveSpec(raw string) (memoryReserveSpec, error) {
	s := strings.TrimSpace(raw)
	lower := strings.ToLower(s)
	if strings.HasSuffix(lower, "%") {
		n := strings.TrimSpace(strings.TrimSuffix(lower, "%"))
		pct, err := strconv.ParseFloat(n, 64)
		if err != nil || pct <= 0 || pct >= 100 {
			return memoryReserveSpec{}, fmt.Errorf("invalid percentage %q; use a value > 0 and < 100%%", s)
		}
		return memoryReserveSpec{Kind: "percent", Percent: pct, Raw: s}, nil
	}

	multiplier := float64(1)
	number := lower
	switch {
	case strings.HasSuffix(lower, "gib"):
		multiplier = 1024
		number = strings.TrimSpace(strings.TrimSuffix(lower, "gib"))
	case strings.HasSuffix(lower, "gb"):
		multiplier = 1024
		number = strings.TrimSpace(strings.TrimSuffix(lower, "gb"))
	case strings.HasSuffix(lower, "mib"):
		number = strings.TrimSpace(strings.TrimSuffix(lower, "mib"))
	case strings.HasSuffix(lower, "mb"):
		number = strings.TrimSpace(strings.TrimSuffix(lower, "mb"))
	default:
		return memoryReserveSpec{}, fmt.Errorf("invalid reserve %q; include MiB, GiB, or %%", s)
	}
	value, err := strconv.ParseFloat(number, 64)
	if err != nil || value < 0 {
		return memoryReserveSpec{}, fmt.Errorf("invalid reserve %q", s)
	}
	mib := uint64(math.Ceil(value * multiplier))
	return memoryReserveSpec{Kind: "fixed", MiB: mib, Raw: s}, nil
}

func adaptiveMemoryReserveMiB(totalMiB, basisMiB uint64) uint64 {
	base := totalMiB
	if base == 0 {
		base = basisMiB
	}
	reserve := uint64(math.Ceil(float64(base) * 0.10))
	if reserve < 512 {
		reserve = 512
	}
	if reserve > 4096 {
		reserve = 4096
	}
	return reserve
}

func (p memoryReservePolicy) reserveForNode(n types.GenerativeNodeCapability, basisMiB uint64, local bool) resolvedMemoryReserve {
	keys := []string{strings.ToLower(strings.TrimSpace(n.NodeName)), strings.ToLower(strings.TrimSpace(n.NodeID))}
	if local {
		keys = append(keys, "primary", "local")
	}
	for _, key := range keys {
		if key == "" {
			continue
		}
		if spec, ok := p.Overrides[key]; ok {
			switch spec.Kind {
			case "fixed":
				return resolvedMemoryReserve{MiB: spec.MiB, Source: "override " + spec.Raw}
			case "percent":
				base := n.MemoryTotalMB
				if base == 0 {
					base = basisMiB
				}
				return resolvedMemoryReserve{
					MiB:    uint64(math.Ceil(float64(base) * spec.Percent / 100.0)),
					Source: fmt.Sprintf("override %.1f%%", spec.Percent),
				}
			}
		}
	}
	if p.GlobalFixedMiB != nil {
		return resolvedMemoryReserve{MiB: *p.GlobalFixedMiB, Source: "legacy global fixed"}
	}
	return resolvedMemoryReserve{MiB: adaptiveMemoryReserveMiB(n.MemoryTotalMB, basisMiB), Source: "adaptive 10%"}
}

func fixedMemoryReservePolicy(mib uint64) memoryReservePolicy {
	v := mib
	return memoryReservePolicy{GlobalFixedMiB: &v, Overrides: map[string]memoryReserveSpec{}}
}

func (p memoryReservePolicy) summary() string {
	if p.GlobalFixedMiB != nil && len(p.Overrides) == 0 {
		return fmt.Sprintf("fixed %d MiB (legacy --reserve-mb)", *p.GlobalFixedMiB)
	}
	if p.GlobalFixedMiB == nil && len(p.Overrides) == 0 {
		return "adaptive (10% physical RAM, min 512 MiB, max 4096 MiB)"
	}
	if p.GlobalFixedMiB != nil {
		return fmt.Sprintf("fixed %d MiB default + per-node overrides", *p.GlobalFixedMiB)
	}
	return "adaptive default + per-node overrides"
}

func minUint64(a, b uint64) uint64 {
	if a < b {
		return a
	}
	return b
}
