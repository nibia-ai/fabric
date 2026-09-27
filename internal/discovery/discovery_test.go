package discovery

import (
	"testing"
)

func TestLikelyVirtualInterfaces(t *testing.T) {
	virtual := []string{
		"utun0", "tun0", "tap1", "wg0", "ppp0",
		"docker0", "br-123", "vethabc", "virbr0",
		"vmnet1", "vboxnet0", "tailscale0", "awdl0", "llw0",
	}
	for _, name := range virtual {
		if !isLikelyVirtual(name) {
			t.Errorf("%q should be virtual", name)
		}
	}

	physical := []string{"en0", "en1", "eth0", "wlan0"}
	for _, name := range physical {
		if isLikelyVirtual(name) {
			t.Errorf("%q should not be virtual", name)
		}
	}
}

func TestInterfaceScorePrefersPrivatePhysical(t *testing.T) {
	physical := InterfaceInfo{
		Name:      "en0",
		Private:   true,
		Virtual:   false,
		Preferred: true,
	}
	vpn := InterfaceInfo{
		Name:      "utun3",
		Private:   true,
		Virtual:   true,
		Preferred: false,
	}
	if interfaceScore(physical) <= interfaceScore(vpn) {
		t.Fatalf("physical score=%d should exceed vpn score=%d",
			interfaceScore(physical), interfaceScore(vpn))
	}
}

func TestMergeMessagesDeduplicatesFingerprint(t *testing.T) {
	a := Message{
		Name:        "NIBIA Fabric",
		Hostname:    "host-a",
		PairURL:     "http://192.168.1.10:8080",
		Fingerprint: "AA:BB",
	}
	b := a
	b.PairURL = "http://127.0.0.1:8080"

	out := MergeMessages([]Message{a}, []Message{b})
	if len(out) != 1 {
		t.Fatalf("got %d messages; want 1", len(out))
	}
}
