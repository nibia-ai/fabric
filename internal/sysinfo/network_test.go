package sysinfo

import (
	"testing"

	"github.com/nibia-ai/fabric/internal/types"
)

func TestLikelyVirtualInterfaceExtended(t *testing.T) {
	virtual := []string{"br-2f10aa", "docker0", "veth123", "virbr0", "ProTUN", "utun4", "wg0", "tailscale0", "vEthernet (WSL)", "cni0", "podman0"}
	for _, name := range virtual {
		if !likelyVirtualInterface(name) {
			t.Fatalf("expected %q to be virtual", name)
		}
	}
	physical := []string{"en0", "eth0", "enp3s0", "wlp2s0", "Wi-Fi", "Ethernet"}
	for _, name := range physical {
		if likelyVirtualInterface(name) {
			t.Fatalf("expected %q to be physical", name)
		}
	}
}

func TestPreferredInterfaceName(t *testing.T) {
	for _, name := range []string{"en0", "eth0", "enp3s0", "wlp2s0", "wlan0", "Wi-Fi", "Ethernet"} {
		if !preferredInterfaceName(name) {
			t.Fatalf("expected %q to be a preferred physical interface name", name)
		}
	}
	for _, name := range []string{"br-abc", "docker0", "utun2", "ProTUN"} {
		if preferredInterfaceName(name) {
			t.Fatalf("did not expect %q to be preferred", name)
		}
	}
}

func TestOverridePreferredAddress(t *testing.T) {
	networks := []types.NetworkInterfaceInventory{
		{Name: "en0", Address: "192.168.1.20", Private: true, Preferred: true},
		{Name: "en7", Address: "10.42.0.10", Private: true},
	}
	out, preferred, err := OverridePreferredAddress(networks, "10.42.0.10")
	if err != nil {
		t.Fatalf("override failed: %v", err)
	}
	if preferred != "10.42.0.10" {
		t.Fatalf("preferred=%q", preferred)
	}
	if out[0].Preferred || !out[1].Preferred {
		t.Fatalf("preferred flags not updated: %#v", out)
	}
}

func TestOverridePreferredAddressRejectsMissing(t *testing.T) {
	networks := []types.NetworkInterfaceInventory{{Name: "en0", Address: "192.168.1.20", Private: true}}
	if _, _, err := OverridePreferredAddress(networks, "10.42.0.10"); err == nil {
		t.Fatal("expected missing-address error")
	}
}
