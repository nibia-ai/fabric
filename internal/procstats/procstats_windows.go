//go:build windows

package procstats

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"time"
)

type windowsProcessSample struct {
	RSS uint64  `json:"rss"`
	CPU float64 `json:"cpu"`
}

func readSnapshot(pid int) (Snapshot, error) {
	script := fmt.Sprintf(`$p=Get-Process -Id %d -ErrorAction Stop; [pscustomobject]@{rss=[uint64]$p.WorkingSet64;cpu=[double]$p.TotalProcessorTime.TotalSeconds} | ConvertTo-Json -Compress`, pid)
	var lastErr error
	for _, exe := range []string{"powershell.exe", "pwsh.exe"} {
		out, err := exec.Command(exe, "-NoProfile", "-NonInteractive", "-Command", script).Output()
		if err != nil {
			lastErr = err
			continue
		}
		var s windowsProcessSample
		if err := json.Unmarshal(out, &s); err != nil {
			return Snapshot{}, err
		}
		return Snapshot{At: time.Now(), CPUSeconds: s.CPU, RSSBytes: s.RSS}, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("PowerShell unavailable for pid %s", strconv.Itoa(pid))
	}
	return Snapshot{}, lastErr
}
