//go:build darwin

package sysinfo

import (
	"bufio"
	"bytes"
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

func platformStatic() (StaticInfo, error) {
	info := StaticInfo{Platform: "macOS", CPULogical: runtime.NumCPU()}
	info.CPUPhysical = sysctlInt("hw.physicalcpu")
	if n := sysctlInt("hw.logicalcpu"); n > 0 {
		info.CPULogical = n
	}
	info.CPUModel = sysctlString("machdep.cpu.brand_string")
	if info.CPUModel == "" || strings.EqualFold(info.CPUModel, "Apple processor") {
		if out, err := exec.Command("system_profiler", "SPHardwareDataType").Output(); err == nil {
			s := bufio.NewScanner(bytes.NewReader(out))
			for s.Scan() {
				line := strings.TrimSpace(s.Text())
				if strings.HasPrefix(line, "Chip:") {
					info.CPUModel = strings.TrimSpace(strings.TrimPrefix(line, "Chip:"))
					break
				}
			}
		}
	}
	if info.CPUModel == "" {
		info.CPUModel = sysctlString("hw.model")
	}
	if runtime.GOARCH == "amd64" {
		features := sysctlString("machdep.cpu.features") + " " + sysctlString("machdep.cpu.leaf7_features")
		for _, flag := range strings.Fields(strings.ToLower(features)) {
			switch flag {
			case "avx", "avx2", "avx512f", "sse4.2", "aes":
				flag = strings.ReplaceAll(flag, ".", "_")
				info.Capabilities = append(info.Capabilities, flag)
			}
		}
	}
	return info, nil
}

func platformDynamic() (DynamicInfo, error) {
	d := DynamicInfo{CPUUsedPct: -1, MemoryPressurePct: -1, MemoryStallSomePct: -1, MemoryStallFullPct: -1}

	if out, err := exec.Command("top", "-l", "1", "-n", "0").Output(); err == nil {
		s := bufio.NewScanner(bytes.NewReader(out))
		for s.Scan() {
			line := strings.TrimSpace(s.Text())
			if strings.HasPrefix(line, "CPU usage:") {
				if idle, ok := percentageAfter(line, "idle"); ok {
					d.CPUUsedPct = 100 - idle
				}
			}
		}
	}

	totalBytes, err := sysctlUint64("hw.memsize")
	if err != nil {
		return DynamicInfo{}, err
	}
	d.MemoryTotalMB = totalBytes / 1024 / 1024

	vmOut, err := exec.Command("vm_stat").Output()
	if err != nil {
		return DynamicInfo{}, err
	}
	pageSize := uint64(4096)
	var free, inactive, speculative, purgeable, compressed uint64
	s := bufio.NewScanner(bytes.NewReader(vmOut))
	for s.Scan() {
		line := s.Text()
		if strings.HasPrefix(line, "Mach Virtual Memory Statistics:") {
			if i := strings.Index(line, "page size of "); i >= 0 {
				rest := line[i+len("page size of "):]
				if fields := strings.Fields(rest); len(fields) > 0 {
					pageSize, _ = strconv.ParseUint(fields[0], 10, 64)
				}
			}
			continue
		}
		pages := parseVMStatPages(line)
		switch {
		case strings.HasPrefix(line, "Pages free:"):
			free = pages
		case strings.HasPrefix(line, "Pages inactive:"):
			inactive = pages
		case strings.HasPrefix(line, "Pages speculative:"):
			speculative = pages
		case strings.HasPrefix(line, "Pages purgeable:"):
			purgeable = pages
		case strings.HasPrefix(line, "Pages occupied by compressor:"):
			compressed = pages
		}
	}
	availablePages := free + inactive + speculative + purgeable
	d.MemoryAvailableMB = availablePages * pageSize / 1024 / 1024
	d.CompressedMB = compressed * pageSize / 1024 / 1024

	if out, err := exec.Command("memory_pressure").Output(); err == nil {
		s := bufio.NewScanner(bytes.NewReader(out))
		for s.Scan() {
			line := strings.TrimSpace(s.Text())
			if strings.HasPrefix(line, "System-wide memory free percentage:") {
				raw := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "System-wide memory free percentage:"), "%"))
				if v, err := strconv.ParseFloat(raw, 64); err == nil {
					d.MemoryPressurePct = v
				}
			}
		}
	}

	if out, err := exec.Command("sysctl", "-n", "vm.swapusage").Output(); err == nil {
		text := string(out)
		d.SwapTotalMB = parseSwapMB(text, "total")
		d.SwapUsedMB = parseSwapMB(text, "used")
	}

	if out, err := exec.Command("sysctl", "-n", "vm.loadavg").Output(); err == nil {
		fields := strings.Fields(strings.NewReplacer("{", "", "}", "").Replace(string(out)))
		if len(fields) >= 3 {
			d.Load1, _ = strconv.ParseFloat(fields[0], 64)
			d.Load5, _ = strconv.ParseFloat(fields[1], 64)
			d.Load15, _ = strconv.ParseFloat(fields[2], 64)
		}
	}

	if out, err := exec.Command("sysctl", "-n", "kern.boottime").Output(); err == nil {
		text := string(out)
		if i := strings.Index(text, "sec = "); i >= 0 {
			rest := text[i+len("sec = "):]
			end := strings.IndexAny(rest, ", }")
			if end > 0 {
				if sec, err := strconv.ParseInt(rest[:end], 10, 64); err == nil {
					d.UptimeSeconds = uint64(time.Now().Unix() - sec)
				}
			}
		}
	}
	return d, nil
}

func sysctlString(name string) string {
	out, err := exec.Command("sysctl", "-n", name).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func sysctlInt(name string) int {
	v, _ := strconv.Atoi(sysctlString(name))
	return v
}

func sysctlUint64(name string) (uint64, error) {
	raw := sysctlString(name)
	if raw == "" {
		return 0, fmt.Errorf("sysctl %s unavailable", name)
	}
	return strconv.ParseUint(raw, 10, 64)
}

func parseVMStatPages(line string) uint64 {
	parts := strings.SplitN(line, ":", 2)
	if len(parts) != 2 {
		return 0
	}
	raw := strings.TrimSpace(strings.TrimSuffix(parts[1], "."))
	v, _ := strconv.ParseUint(raw, 10, 64)
	return v
}

func percentageAfter(line, label string) (float64, bool) {
	fields := strings.Fields(line)
	for i, f := range fields {
		if strings.TrimSuffix(f, ",") == label && i > 0 {
			raw := strings.TrimSuffix(strings.TrimSuffix(fields[i-1], "%"), ",")
			v, err := strconv.ParseFloat(raw, 64)
			return v, err == nil
		}
	}
	return 0, false
}

func parseSwapMB(text, key string) uint64 {
	fields := strings.Fields(text)
	for i, f := range fields {
		if f == key && i+2 < len(fields) && fields[i+1] == "=" {
			raw := fields[i+2]
			if len(raw) < 2 {
				return 0
			}
			unit := raw[len(raw)-1]
			value, err := strconv.ParseFloat(raw[:len(raw)-1], 64)
			if err != nil {
				return 0
			}
			switch unit {
			case 'G':
				value *= 1024
			case 'K':
				value /= 1024
			}
			return uint64(value)
		}
	}
	return 0
}
