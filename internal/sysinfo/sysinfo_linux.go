//go:build linux

package sysinfo

import (
	"bufio"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

func platformStatic() (StaticInfo, error) {
	info := StaticInfo{Platform: "linux", CPULogical: runtime.NumCPU()}
	f, err := os.Open("/proc/cpuinfo")
	if err != nil {
		return info, err
	}
	defer f.Close()

	physicalCores := map[string]struct{}{}
	physicalID, coreID := "0", ""
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := s.Text()
		if strings.TrimSpace(line) == "" {
			if coreID != "" {
				physicalCores[physicalID+":"+coreID] = struct{}{}
			}
			physicalID, coreID = "0", ""
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key, value := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		switch key {
		case "model name", "Hardware":
			if info.CPUModel == "" {
				info.CPUModel = value
			}
		case "physical id":
			physicalID = value
		case "core id":
			coreID = value
		case "flags", "Features":
			for _, flag := range strings.Fields(value) {
				switch flag {
				case "avx", "avx2", "avx512f", "sse4_2", "aes", "neon", "asimd":
					if flag == "asimd" {
						flag = "neon"
					}
					info.Capabilities = append(info.Capabilities, flag)
				}
			}
		}
	}
	if coreID != "" {
		physicalCores[physicalID+":"+coreID] = struct{}{}
	}
	if err := s.Err(); err != nil {
		return info, err
	}
	if len(physicalCores) > 0 {
		info.CPUPhysical = len(physicalCores)
	}
	return info, nil
}

func platformDynamic() (DynamicInfo, error) {
	cpuPct, err := linuxCPUPercent(200 * time.Millisecond)
	if err != nil {
		return DynamicInfo{}, err
	}

	mem, err := linuxMemInfo()
	if err != nil {
		return DynamicInfo{}, err
	}
	mem.CPUUsedPct = cpuPct
	mem.MemoryPressurePct = -1
	mem.MemoryStallSomePct = -1
	mem.MemoryStallFullPct = -1
	if b, err := os.ReadFile("/proc/pressure/memory"); err == nil {
		mem.MemoryStallSomePct, mem.MemoryStallFullPct = parseLinuxMemoryPSI(string(b))
	}

	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		fields := strings.Fields(string(b))
		if len(fields) >= 3 {
			mem.Load1, _ = strconv.ParseFloat(fields[0], 64)
			mem.Load5, _ = strconv.ParseFloat(fields[1], 64)
			mem.Load15, _ = strconv.ParseFloat(fields[2], 64)
		}
	}
	if b, err := os.ReadFile("/proc/uptime"); err == nil {
		fields := strings.Fields(string(b))
		if len(fields) > 0 {
			up, _ := strconv.ParseFloat(fields[0], 64)
			mem.UptimeSeconds = uint64(up)
		}
	}
	return mem, nil
}

func linuxMemInfo() (DynamicInfo, error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return DynamicInfo{}, err
	}
	defer f.Close()

	vals := map[string]uint64{}
	s := bufio.NewScanner(f)
	for s.Scan() {
		fields := strings.Fields(s.Text())
		if len(fields) < 2 {
			continue
		}
		v, err := strconv.ParseUint(fields[1], 10, 64)
		if err == nil {
			vals[strings.TrimSuffix(fields[0], ":")] = v
		}
	}
	if err := s.Err(); err != nil {
		return DynamicInfo{}, err
	}
	if vals["MemTotal"] == 0 {
		return DynamicInfo{}, fmt.Errorf("MemTotal not found")
	}
	return DynamicInfo{
		MemoryTotalMB:     vals["MemTotal"] / 1024,
		MemoryAvailableMB: vals["MemAvailable"] / 1024,
		SwapTotalMB:       vals["SwapTotal"] / 1024,
		SwapUsedMB:        (vals["SwapTotal"] - vals["SwapFree"]) / 1024,
	}, nil
}

type cpuTimes struct{ idle, total uint64 }

func readLinuxCPUTimes() (cpuTimes, error) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return cpuTimes{}, err
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	if !s.Scan() {
		return cpuTimes{}, fmt.Errorf("missing cpu line in /proc/stat")
	}
	fields := strings.Fields(s.Text())
	if len(fields) < 5 || fields[0] != "cpu" {
		return cpuTimes{}, fmt.Errorf("unexpected /proc/stat cpu line")
	}
	var nums []uint64
	for _, f := range fields[1:] {
		v, _ := strconv.ParseUint(f, 10, 64)
		nums = append(nums, v)
	}
	var total uint64
	for _, v := range nums {
		total += v
	}
	idle := nums[3]
	if len(nums) > 4 {
		idle += nums[4]
	}
	return cpuTimes{idle: idle, total: total}, nil
}

func linuxCPUPercent(interval time.Duration) (float64, error) {
	a, err := readLinuxCPUTimes()
	if err != nil {
		return -1, err
	}
	time.Sleep(interval)
	b, err := readLinuxCPUTimes()
	if err != nil {
		return -1, err
	}
	dTotal := b.total - a.total
	dIdle := b.idle - a.idle
	if dTotal == 0 {
		return 0, nil
	}
	return 100 * (1 - float64(dIdle)/float64(dTotal)), nil
}

func parseLinuxMemoryPSI(text string) (some, full float64) {
	some, full = -1, -1
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 2 {
			continue
		}
		name := fields[0]
		for _, field := range fields[1:] {
			if !strings.HasPrefix(field, "avg10=") {
				continue
			}
			v, err := strconv.ParseFloat(strings.TrimPrefix(field, "avg10="), 64)
			if err != nil {
				continue
			}
			switch name {
			case "some":
				some = v
			case "full":
				full = v
			}
		}
	}
	return some, full
}
