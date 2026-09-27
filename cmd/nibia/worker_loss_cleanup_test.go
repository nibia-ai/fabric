package main

import (
	"testing"
	"time"

	"github.com/nibia-ai/fabric/internal/types"
)

func TestCleanupNodeUnavailable(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		node types.GenerativeNodeCapability
		want bool
	}{
		{
			name: "fresh ready node remains available",
			node: types.GenerativeNodeCapability{NodeState: "READY", LastSeen: now.Add(-2 * time.Second)},
			want: false,
		},
		{
			name: "offline node unavailable immediately",
			node: types.GenerativeNodeCapability{NodeState: "OFFLINE", LastSeen: now},
			want: true,
		},
		{
			name: "stale heartbeat remains available until controller marks offline",
			node: types.GenerativeNodeCapability{NodeState: "READY", LastSeen: now.Add(-2 * generativeReadinessFreshness)},
			want: false,
		},
		{
			name: "missing heartbeat unavailable",
			node: types.GenerativeNodeCapability{NodeState: "READY"},
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cleanupNodeUnavailable(tc.node, now); got != tc.want {
				t.Fatalf("cleanupNodeUnavailable()=%v want %v", got, tc.want)
			}
		})
	}
}
