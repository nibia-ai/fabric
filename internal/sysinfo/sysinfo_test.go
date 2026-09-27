package sysinfo

import "testing"

func TestResourceState(t *testing.T) {
	tests := []struct {
		name string
		d    DynamicInfo
		want string
	}{
		{"ready", DynamicInfo{CPUUsedPct: 20, MemoryTotalMB: 16000, MemoryAvailableMB: 8000, MemoryPressurePct: 70}, "ready"},
		{"busy cpu", DynamicInfo{CPUUsedPct: 95, MemoryTotalMB: 16000, MemoryAvailableMB: 8000, MemoryPressurePct: 70}, "busy"},
		{"pressured mac", DynamicInfo{CPUUsedPct: 20, MemoryTotalMB: 16000, MemoryAvailableMB: 8000, MemoryPressurePct: 8}, "pressured"},
		{"pressured available", DynamicInfo{CPUUsedPct: 20, MemoryTotalMB: 16000, MemoryAvailableMB: 1000, MemoryPressurePct: -1}, "pressured"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ResourceState(tt.d); got != tt.want {
				t.Fatalf("got %q want %q", got, tt.want)
			}
		})
	}
}

func TestResourceScoreRange(t *testing.T) {
	for _, d := range []DynamicInfo{
		{CPUUsedPct: 0, MemoryTotalMB: 16000, MemoryAvailableMB: 16000, MemoryPressurePct: 100},
		{CPUUsedPct: 100, MemoryTotalMB: 16000, MemoryAvailableMB: 0, MemoryPressurePct: 0, SwapTotalMB: 8000, SwapUsedMB: 8000},
	} {
		got := ResourceScore(d)
		if got < 0 || got > 1 {
			t.Fatalf("score out of range: %v", got)
		}
	}
}

func TestLikelyVirtualInterface(t *testing.T) {
	for _, name := range []string{"ProTUN", "utun4", "Tailscale", "vEthernet (WSL)"} {
		if !likelyVirtualInterface(name) {
			t.Fatalf("expected %q to be virtual", name)
		}
	}
	if likelyVirtualInterface("Wi-Fi") {
		t.Fatal("Wi-Fi must not be classified as virtual")
	}
	if likelyVirtualInterface("Ethernet") {
		t.Fatal("Ethernet must not be classified as virtual")
	}
}

func TestMemoryHeadroomAndPressureLevel(t *testing.T) {
	tests := []struct {
		name     string
		d        DynamicInfo
		headroom float64
		level    string
	}{
		{"mac native low pressure", DynamicInfo{MemoryTotalMB: 16000, MemoryAvailableMB: 6000, MemoryPressurePct: 77, MemoryStallSomePct: -1, MemoryStallFullPct: -1}, 77, "LOW"},
		{"generic moderate", DynamicInfo{MemoryTotalMB: 16000, MemoryAvailableMB: 4000, MemoryPressurePct: -1, MemoryStallSomePct: -1, MemoryStallFullPct: -1}, 25, "MODERATE"},
		{"generic high", DynamicInfo{MemoryTotalMB: 16000, MemoryAvailableMB: 2000, MemoryPressurePct: -1, MemoryStallSomePct: -1, MemoryStallFullPct: -1}, 12.5, "HIGH"},
		{"psi raises severity", DynamicInfo{MemoryTotalMB: 16000, MemoryAvailableMB: 8000, MemoryPressurePct: -1, MemoryStallSomePct: 6, MemoryStallFullPct: 0}, 50, "HIGH"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MemoryHeadroomPct(tt.d); got != tt.headroom {
				t.Fatalf("headroom got %.2f want %.2f", got, tt.headroom)
			}
			if got := MemoryPressureLevel(tt.d); got != tt.level {
				t.Fatalf("level got %q want %q", got, tt.level)
			}
		})
	}
}
