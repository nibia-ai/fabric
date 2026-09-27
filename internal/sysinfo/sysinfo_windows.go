//go:build windows

package sysinfo

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

type winStatic struct {
	Name                      string `json:"Name"`
	NumberOfCores             int    `json:"NumberOfCores"`
	NumberOfLogicalProcessors int    `json:"NumberOfLogicalProcessors"`
}

type winDynamic struct {
	TotalVisibleMemorySize uint64  `json:"TotalVisibleMemorySize"`
	FreePhysicalMemory     uint64  `json:"FreePhysicalMemory"`
	LastBootUpTime         string  `json:"LastBootUpTime"`
	CPU                    float64 `json:"CPU"`
	SwapTotalKB            uint64  `json:"SwapTotalKB"`
	SwapUsedKB             uint64  `json:"SwapUsedKB"`
}

func platformStatic() (StaticInfo, error) {
	script := `$p=Get-CimInstance Win32_Processor | Select-Object -First 1 Name,NumberOfCores,NumberOfLogicalProcessors; $p | ConvertTo-Json -Compress`
	out, err := powershell(script)
	if err != nil {
		return StaticInfo{Platform: "Windows", CPULogical: runtime.NumCPU()}, err
	}
	var w winStatic
	if err := json.Unmarshal(out, &w); err != nil {
		return StaticInfo{}, err
	}
	return StaticInfo{Platform: "Windows", CPUModel: strings.TrimSpace(w.Name), CPUPhysical: w.NumberOfCores, CPULogical: w.NumberOfLogicalProcessors}, nil
}

func platformDynamic() (DynamicInfo, error) {
	script := `$os=Get-CimInstance Win32_OperatingSystem; $cpu=(Get-CimInstance Win32_Processor | Measure-Object -Property LoadPercentage -Average).Average; $pf=Get-CimInstance Win32_PageFileUsage; [pscustomobject]@{TotalVisibleMemorySize=[uint64]$os.TotalVisibleMemorySize;FreePhysicalMemory=[uint64]$os.FreePhysicalMemory;LastBootUpTime=$os.LastBootUpTime.ToString("o");CPU=[double]$cpu;SwapTotalKB=[uint64](($pf | Measure-Object -Property AllocatedBaseSize -Sum).Sum*1024);SwapUsedKB=[uint64](($pf | Measure-Object -Property CurrentUsage -Sum).Sum*1024)} | ConvertTo-Json -Compress`
	out, err := powershell(script)
	if err != nil {
		return DynamicInfo{}, err
	}
	var w winDynamic
	if err := json.Unmarshal(out, &w); err != nil {
		return DynamicInfo{}, err
	}
	d := DynamicInfo{
		CPUUsedPct:         w.CPU,
		MemoryTotalMB:      w.TotalVisibleMemorySize / 1024,
		MemoryAvailableMB:  w.FreePhysicalMemory / 1024,
		MemoryPressurePct:  -1,
		MemoryStallSomePct: -1,
		MemoryStallFullPct: -1,
		SwapTotalMB:        w.SwapTotalKB / 1024,
		SwapUsedMB:         w.SwapUsedKB / 1024,
		Load1:              -1,
		Load5:              -1,
		Load15:             -1,
	}
	if t, err := time.Parse(time.RFC3339Nano, w.LastBootUpTime); err == nil {
		d.UptimeSeconds = uint64(time.Since(t).Seconds())
	}
	return d, nil
}

func powershell(script string) ([]byte, error) {
	var lastErr error
	for _, exe := range []string{"powershell.exe", "pwsh.exe"} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		out, err := exec.CommandContext(ctx, exe, "-NoProfile", "-NonInteractive", "-Command", script).Output()
		timedOut := ctx.Err() == context.DeadlineExceeded
		cancel()
		if err == nil {
			return out, nil
		}
		if timedOut {
			return nil, fmt.Errorf("%s telemetry timed out after 5s", exe)
		}
		lastErr = err
	}
	if lastErr != nil {
		return nil, fmt.Errorf("PowerShell telemetry failed: %w", lastErr)
	}
	return nil, fmt.Errorf("PowerShell is unavailable")
}
