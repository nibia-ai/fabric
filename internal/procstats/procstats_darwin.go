//go:build darwin

package procstats

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

func readSnapshot(pid int) (Snapshot, error) {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "rss=", "-o", "time=").Output()
	if err != nil {
		return Snapshot{}, err
	}
	fields := strings.Fields(strings.TrimSpace(string(out)))
	if len(fields) < 2 {
		return Snapshot{}, fmt.Errorf("unexpected ps output for pid %d", pid)
	}
	rssKB, err := strconv.ParseUint(fields[0], 10, 64)
	if err != nil {
		return Snapshot{}, err
	}
	cpuSeconds, err := parsePSCPUTime(fields[1])
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{At: time.Now(), CPUSeconds: cpuSeconds, RSSBytes: rssKB * 1024}, nil
}

func parsePSCPUTime(raw string) (float64, error) {
	// macOS ps TIME is commonly [[dd-]hh:]mm:ss[.cc].
	days := 0.0
	if i := strings.Index(raw, "-"); i >= 0 {
		d, err := strconv.ParseFloat(raw[:i], 64)
		if err != nil {
			return 0, err
		}
		days = d
		raw = raw[i+1:]
	}
	parts := strings.Split(raw, ":")
	var hours, minutes, seconds float64
	switch len(parts) {
	case 2:
		minutes, _ = strconv.ParseFloat(parts[0], 64)
		seconds, _ = strconv.ParseFloat(parts[1], 64)
	case 3:
		hours, _ = strconv.ParseFloat(parts[0], 64)
		minutes, _ = strconv.ParseFloat(parts[1], 64)
		seconds, _ = strconv.ParseFloat(parts[2], 64)
	default:
		return 0, fmt.Errorf("unexpected process cpu time %q", raw)
	}
	return days*86400 + hours*3600 + minutes*60 + seconds, nil
}
