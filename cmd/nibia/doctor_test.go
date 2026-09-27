package main

import (
	"testing"

	"github.com/nibia-ai/fabric/internal/types"
	"github.com/nibia-ai/fabric/internal/version"
)

func TestControllerCompatibilityLabel(t *testing.T) {
	tests := []struct {
		name string
		info types.ControllerInfo
		want string
	}{
		{"match", types.ControllerInfo{Version: version.Version, ProtocolVersion: version.ProtocolVersion}, "MATCH"},
		{"version mismatch", types.ControllerInfo{Version: "0.6.99-test", ProtocolVersion: version.ProtocolVersion}, "VERSION MISMATCH"},
		{"protocol mismatch", types.ControllerInfo{Version: version.Version, ProtocolVersion: "999"}, "PROTOCOL MISMATCH"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := controllerCompatibilityLabel(tt.info); got != tt.want {
				t.Fatalf("controllerCompatibilityLabel() = %q, want %q", got, tt.want)
			}
		})
	}
}
