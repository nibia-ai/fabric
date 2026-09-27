//go:build linux

package procstats

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

var linuxClock = struct {
	sync.Once
	hz float64
}{hz: 100}

func linuxClockTicks() float64 {
	linuxClock.Do(func() {
		out, err := exec.Command("getconf", "CLK_TCK").Output()
		if err != nil {
			return
		}
		if v, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64); err == nil && v > 0 {
			linuxClock.hz = v
		}
	})
	return linuxClock.hz
}

func readSnapshot(pid int) (Snapshot, error) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return Snapshot{}, err
	}
	// comm (field 2) may contain spaces. Parse from the final ')' onward.
	text := string(raw)
	end := strings.LastIndex(text, ")")
	if end < 0 || end+2 >= len(text) {
		return Snapshot{}, fmt.Errorf("malformed /proc/%d/stat", pid)
	}
	fields := strings.Fields(text[end+2:])
	// fields now begins at original field 3. utime/stime are original 14/15,
	// therefore indexes 11/12 in this sliced representation.
	if len(fields) <= 12 {
		return Snapshot{}, fmt.Errorf("short /proc/%d/stat", pid)
	}
	utime, err1 := strconv.ParseUint(fields[11], 10, 64)
	stime, err2 := strconv.ParseUint(fields[12], 10, 64)
	if err1 != nil || err2 != nil {
		return Snapshot{}, fmt.Errorf("invalid cpu times for pid %d", pid)
	}

	f, err := os.Open(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return Snapshot{}, err
	}
	defer f.Close()
	var rssKB uint64
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if strings.HasPrefix(line, "VmRSS:") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				rssKB, _ = strconv.ParseUint(parts[1], 10, 64)
			}
			break
		}
	}
	if err := s.Err(); err != nil {
		return Snapshot{}, err
	}
	return Snapshot{
		At:         time.Now(),
		CPUSeconds: float64(utime+stime) / linuxClockTicks(),
		RSSBytes:   rssKB * 1024,
	}, nil
}
