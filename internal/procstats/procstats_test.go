package procstats

import (
	"os"
	"testing"
	"time"
)

func TestTrackerCurrentProcess(t *testing.T) {
	tr := NewTracker(os.Getpid())
	first, err := tr.Sample()
	if err != nil {
		t.Fatalf("first sample: %v", err)
	}
	if first.RSSBytes == 0 {
		t.Fatalf("expected non-zero RSS")
	}

	deadline := time.Now().Add(30 * time.Millisecond)
	var x uint64
	for time.Now().Before(deadline) {
		x = x*1664525 + 1013904223
	}
	_ = x

	second, err := tr.Sample()
	if err != nil {
		t.Fatalf("second sample: %v", err)
	}
	if second.RSSBytes == 0 {
		t.Fatalf("expected non-zero RSS on second sample")
	}
	_, _, peakRSS := tr.Values()
	if peakRSS == 0 {
		t.Fatalf("expected non-zero peak RSS")
	}
}
