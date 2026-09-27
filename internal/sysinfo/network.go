package sysinfo

import (
	"fmt"
	"net"
	"sort"
	"strings"

	"github.com/nibia-ai/fabric/internal/types"
)

// NetworkInventory returns non-loopback IPv4 addresses with a conservative
// preference for physical/private LAN interfaces. Virtual/VPN interfaces are
// reported for visibility but are not preferred for fabric traffic.
func NetworkInventory() ([]types.NetworkInterfaceInventory, string) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, ""
	}
	type scored struct {
		item  types.NetworkInterfaceInventory
		score int
	}
	var rows []scored
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		virtual := likelyVirtualInterface(iface.Name)
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			var ip net.IP
			switch x := a.(type) {
			case *net.IPNet:
				ip = x.IP
			case *net.IPAddr:
				ip = x.IP
			}
			if ip == nil || ip.To4() == nil || ip.IsLoopback() || ip.IsUnspecified() {
				continue
			}
			ip = ip.To4()
			preferredName := preferredInterfaceName(iface.Name)
			preferred := ip.IsPrivate() && !virtual && preferredName
			item := types.NetworkInterfaceInventory{Name: iface.Name, Address: ip.String(), Private: ip.IsPrivate(), Virtual: virtual, Preferred: preferred}
			score := 0
			if item.Private {
				score += 100
			}
			if !item.Virtual {
				score += 40
			}
			if preferredName {
				score += 30
			}
			rows = append(rows, scored{item: item, score: score})
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].score != rows[j].score {
			return rows[i].score > rows[j].score
		}
		if rows[i].item.Name != rows[j].item.Name {
			return rows[i].item.Name < rows[j].item.Name
		}
		return rows[i].item.Address < rows[j].item.Address
	})
	out := make([]types.NetworkInterfaceInventory, 0, len(rows))
	preferred := ""
	for i, r := range rows {
		item := r.item
		if i == 0 && item.Private && !item.Virtual {
			item.Preferred = true
		}
		if preferred == "" && item.Preferred {
			preferred = item.Address
		}
		out = append(out, item)
	}
	if preferred == "" && len(out) > 0 {
		preferred = out[0].Address
	}
	return out, preferred
}

func likelyVirtualInterface(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	tokens := []string{"utun", "tun", "tap", "protun", "vpn", "wireguard", "tailscale", "zerotier", "zt", "ppp", "ipsec", "docker", "veth", "virbr", "vmnet", "vbox", "hyper-v", "vethernet", "wsl", "cni", "flannel", "kube", "podman", "lxc", "incus"}
	if strings.HasPrefix(n, "br-") || strings.HasPrefix(n, "wg") {
		return true
	}
	for _, t := range tokens {
		if strings.Contains(n, t) {
			return true
		}
	}
	return false
}

func preferredInterfaceName(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	return n == "wi-fi" || n == "wifi" || n == "ethernet" ||
		strings.HasPrefix(n, "en") || strings.HasPrefix(n, "eth") ||
		strings.HasPrefix(n, "wlan") || strings.HasPrefix(n, "wl")
}

// OverridePreferredAddress marks a specific local IPv4 address as preferred.
// The address must already be present in the host network inventory and must
// belong to a non-virtual interface. This provides a deterministic escape hatch
// on hosts such as macOS where interface names alone do not distinguish Wi-Fi
// from USB/Thunderbolt Ethernet reliably.
func OverridePreferredAddress(networks []types.NetworkInterfaceInventory, address string) ([]types.NetworkInterfaceInventory, string, error) {
	address = strings.TrimSpace(address)
	ip := net.ParseIP(address)
	if ip == nil || ip.To4() == nil {
		return networks, "", fmt.Errorf("%q is not a valid IPv4 address", address)
	}
	found := -1
	for i := range networks {
		if networks[i].Address == address {
			if networks[i].Virtual {
				return networks, "", fmt.Errorf("%s belongs to virtual/VPN interface %s", address, networks[i].Name)
			}
			found = i
			break
		}
	}
	if found < 0 {
		return networks, "", fmt.Errorf("%s is not assigned to an active local IPv4 interface", address)
	}
	out := append([]types.NetworkInterfaceInventory(nil), networks...)
	for i := range out {
		out[i].Preferred = i == found
	}
	return out, address, nil
}
