package procstats

import (
	"errors"
	"math"
	"sync"
	"time"
)

// Snapshot is a point-in-time process resource sample.
// CPUSeconds is cumulative process CPU time across all threads/cores.
// RSSBytes is resident memory attributed to the process by the OS.
type Snapshot struct {
	At         time.Time
	CPUSeconds float64
	RSSBytes   uint64
}

// Metrics are derived from consecutive snapshots. CPUPercent is expressed in
// the conventional process sense where 100% ~= one fully occupied logical CPU
// and values may exceed 100% for a multithreaded process.
type Metrics struct {
	CPUPercent float64
	RSSBytes   uint64
}

// Tracker converts cumulative process samples into current/peak utilization.
type Tracker struct {
	mu       sync.Mutex
	pid      int
	prev     Snapshot
	havePrev bool
	current  Metrics
	peakCPU  float64
	peakRSS  uint64
}

func NewTracker(pid int) *Tracker { return &Tracker{pid: pid} }

func (t *Tracker) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.prev = Snapshot{}
	t.havePrev = false
	t.current = Metrics{}
	t.peakCPU = 0
	t.peakRSS = 0
}

func (t *Tracker) Sample() (Metrics, error) {
	if t == nil || t.pid <= 0 {
		return Metrics{}, errors.New("invalid process tracker")
	}
	snap, err := readSnapshot(t.pid)
	if err != nil {
		return Metrics{}, err
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	m := Metrics{RSSBytes: snap.RSSBytes}
	if t.havePrev {
		wall := snap.At.Sub(t.prev.At).Seconds()
		cpu := snap.CPUSeconds - t.prev.CPUSeconds
		if wall > 0 && cpu >= 0 {
			m.CPUPercent = 100 * cpu / wall
			if math.IsNaN(m.CPUPercent) || math.IsInf(m.CPUPercent, 0) || m.CPUPercent < 0 {
				m.CPUPercent = 0
			}
		}
	}
	t.prev = snap
	t.havePrev = true
	t.current = m
	if m.CPUPercent > t.peakCPU {
		t.peakCPU = m.CPUPercent
	}
	if m.RSSBytes > t.peakRSS {
		t.peakRSS = m.RSSBytes
	}
	return m, nil
}

func (t *Tracker) Values() (current Metrics, peakCPU float64, peakRSS uint64) {
	if t == nil {
		return Metrics{}, 0, 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.current, t.peakCPU, t.peakRSS
}
