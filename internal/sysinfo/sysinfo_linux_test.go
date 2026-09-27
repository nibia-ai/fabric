//go:build linux

package sysinfo

import "testing"

func TestParseLinuxMemoryPSI(t *testing.T) {
	some, full := parseLinuxMemoryPSI("some avg10=1.25 avg60=0.50 avg300=0.10 total=123\nfull avg10=0.25 avg60=0.10 avg300=0.01 total=45\n")
	if some != 1.25 || full != 0.25 {
		t.Fatalf("got some=%.2f full=%.2f", some, full)
	}
}
