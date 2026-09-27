package discovery

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	MulticastAddress = "239.255.77.77:47777"
	QueryType        = "nibia-discover"
	ResponseType     = "nibia-controller"
	DiscoveryVersion = "nibia-multicast-v2"
)

type Message struct {
	Type        string `json:"type"`
	Nonce       string `json:"nonce,omitempty"`
	Name        string `json:"name,omitempty"`
	Hostname    string `json:"hostname,omitempty"`
	PairURL     string `json:"pair_url,omitempty"`
	SecureURL   string `json:"secure_url,omitempty"`
	Version     string `json:"version,omitempty"`
	Protocol    string `json:"protocol,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	Discovery   string `json:"discovery,omitempty"`
}

type InterfaceInfo struct {
	Name      string
	Address   string
	Private   bool
	Virtual   bool
	Preferred bool
	Score     int
}

type Options struct {
	IncludeVirtual bool
}

type result struct {
	msg Message
	err error
}

func Interfaces(includeVirtual bool) ([]InterfaceInfo, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}

	var out []InterfaceInfo
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		if iface.Flags&net.FlagMulticast == 0 {
			continue
		}

		virtual := isLikelyVirtual(iface.Name)
		if virtual && !includeVirtual {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ip := ipFromAddr(addr)
			if ip == nil || ip.To4() == nil || ip.IsLoopback() || ip.IsUnspecified() {
				continue
			}
			ip = ip.To4()
			info := InterfaceInfo{
				Name:      iface.Name,
				Address:   ip.String(),
				Private:   ip.IsPrivate(),
				Virtual:   virtual,
				Preferred: preferredInterfaceName(iface.Name),
			}
			info.Score = interfaceScore(info)
			out = append(out, info)
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Address < out[j].Address
	})
	return out, nil
}

func BestAdvertiseHost(includeVirtual bool) string {
	ifaces, err := Interfaces(includeVirtual)
	if err == nil && len(ifaces) > 0 {
		return ifaces[0].Address
	}
	return "127.0.0.1"
}

func Serve(ctx context.Context, build func() Message, opts Options) error {
	group, err := net.ResolveUDPAddr("udp4", MulticastAddress)
	if err != nil {
		return err
	}

	infos, err := Interfaces(opts.IncludeVirtual)
	if err != nil {
		return err
	}
	if len(infos) == 0 {
		return errors.New("no eligible UP+MULTICAST IPv4 interfaces found")
	}

	type listener struct {
		conn  *net.UDPConn
		iface InterfaceInfo
	}
	var listeners []listener
	var errs []string

	for _, info := range infos {
		iface, err := interfaceByNameAndIP(info.Name, info.Address)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s(%s): %v", info.Name, info.Address, err))
			continue
		}
		conn, err := net.ListenMulticastUDP("udp4", iface, group)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s(%s): %v", info.Name, info.Address, err))
			continue
		}
		_ = conn.SetReadBuffer(64 * 1024)
		listeners = append(listeners, listener{conn: conn, iface: info})
	}

	if len(listeners) == 0 {
		return fmt.Errorf("unable to join multicast group on any eligible interface: %s", strings.Join(errs, "; "))
	}

	var wg sync.WaitGroup
	for _, l := range listeners {
		l := l
		wg.Add(1)
		go func() {
			defer wg.Done()
			serveConn(ctx, l.conn, build)
		}()
	}

	<-ctx.Done()
	for _, l := range listeners {
		_ = l.conn.Close()
	}
	wg.Wait()
	return nil
}

func serveConn(ctx context.Context, conn *net.UDPConn, build func() Message) {
	buf := make([]byte, 4096)
	for {
		n, src, err := conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		var q Message
		if err := json.Unmarshal(buf[:n], &q); err != nil || q.Type != QueryType {
			continue
		}

		resp := build()
		resp.Type = ResponseType
		resp.Nonce = q.Nonce
		if resp.Discovery == "" {
			resp.Discovery = DiscoveryVersion
		}
		b, err := json.Marshal(resp)
		if err != nil {
			continue
		}
		// Responses are unicast back to the discoverer's ephemeral socket.
		// That avoids requiring the CLI itself to join the multicast group.
		_, _ = conn.WriteToUDP(b, src)

		if ctx.Err() != nil {
			return
		}
	}
}

func Discover(ctx context.Context, timeout time.Duration, protocol string, opts Options) ([]Message, error) {
	group, err := net.ResolveUDPAddr("udp4", MulticastAddress)
	if err != nil {
		return nil, err
	}

	infos, err := Interfaces(opts.IncludeVirtual)
	if err != nil {
		return nil, err
	}
	if len(infos) == 0 {
		return nil, errors.New("no eligible UP+MULTICAST IPv4 interfaces found")
	}

	nonce, err := nonce()
	if err != nil {
		return nil, err
	}
	query, _ := json.Marshal(Message{
		Type:      QueryType,
		Nonce:     nonce,
		Protocol:  protocol,
		Discovery: DiscoveryVersion,
	})

	results := make(chan result, 64)
	var wg sync.WaitGroup

	for _, info := range infos {
		info := info
		wg.Add(1)
		go func() {
			defer wg.Done()
			discoverOnInterface(ctx, timeout, group, info, query, nonce, results)
		}()
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	seen := map[string]Message{}
	var errorsSeen []string
	for r := range results {
		if r.err != nil {
			errorsSeen = append(errorsSeen, r.err.Error())
			continue
		}
		key := messageKey(r.msg)
		seen[key] = r.msg
	}

	out := make([]Message, 0, len(seen))
	for _, msg := range seen {
		out = append(out, msg)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name == out[j].Name {
			return out[i].PairURL < out[j].PairURL
		}
		return out[i].Name < out[j].Name
	})

	if len(out) == 0 && len(errorsSeen) == len(infos) {
		return nil, fmt.Errorf("discovery failed on all interfaces: %s", strings.Join(errorsSeen, "; "))
	}
	return out, nil
}

func discoverOnInterface(
	ctx context.Context,
	timeout time.Duration,
	group *net.UDPAddr,
	info InterfaceInfo,
	query []byte,
	nonce string,
	results chan<- result,
) {
	localIP := net.ParseIP(info.Address)
	if localIP == nil {
		results <- result{err: fmt.Errorf("%s: invalid interface address %q", info.Name, info.Address)}
		return
	}

	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: localIP, Port: 0})
	if err != nil {
		results <- result{err: fmt.Errorf("%s(%s): listen: %w", info.Name, info.Address, err)}
		return
	}
	defer conn.Close()

	deadline := time.Now().Add(timeout)
	_ = conn.SetDeadline(deadline)

	// Binding the socket to the interface's IPv4 address gives the kernel a
	// deterministic source/interface choice without a platform-specific API.
	if _, err := conn.WriteToUDP(query, group); err != nil {
		results <- result{err: fmt.Errorf("%s(%s): multicast send: %w", info.Name, info.Address, err)}
		return
	}

	buf := make([]byte, 4096)
	for {
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				return
			}
			if ctx.Err() != nil {
				return
			}
			results <- result{err: fmt.Errorf("%s(%s): receive: %w", info.Name, info.Address, err)}
			return
		}

		var msg Message
		if err := json.Unmarshal(buf[:n], &msg); err != nil {
			continue
		}
		if msg.Type != ResponseType || msg.Nonce != nonce {
			continue
		}
		results <- result{msg: msg}
	}
}

func MergeMessages(groups ...[]Message) []Message {
	seen := map[string]Message{}
	for _, group := range groups {
		for _, msg := range group {
			seen[messageKey(msg)] = msg
		}
	}
	out := make([]Message, 0, len(seen))
	for _, msg := range seen {
		out = append(out, msg)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name == out[j].Name {
			return out[i].PairURL < out[j].PairURL
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func messageKey(msg Message) string {
	if strings.TrimSpace(msg.Fingerprint) != "" {
		return strings.ToLower(strings.TrimSpace(msg.Fingerprint))
	}
	return strings.ToLower(strings.TrimSpace(msg.Hostname + "|" + msg.PairURL))
}

func isLikelyVirtual(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	prefixes := []string{
		"utun", "tun", "tap", "wg", "ppp", "ipsec",
		"docker", "br-", "veth", "virbr", "vmnet", "vbox",
		"tailscale", "zt", "ham", "awdl", "llw",
	}
	for _, p := range prefixes {
		if strings.HasPrefix(n, p) {
			return true
		}
	}
	return false
}

func preferredInterfaceName(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	switch n {
	case "en0", "en1", "eth0", "eth1", "wlan0", "wlan1", "wi-fi", "ethernet":
		return true
	default:
		return false
	}
}

func interfaceScore(info InterfaceInfo) int {
	score := 0
	if info.Private {
		score += 100
	}
	if info.Preferred {
		score += 30
	}
	if !info.Virtual {
		score += 20
	}
	return score
}

func interfaceByNameAndIP(name, address string) (*net.Interface, error) {
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return nil, err
	}
	want := net.ParseIP(address)
	if want == nil {
		return nil, errors.New("invalid IP")
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return nil, err
	}
	for _, addr := range addrs {
		ip := ipFromAddr(addr)
		if ip != nil && ip.Equal(want) {
			return iface, nil
		}
	}
	return nil, fmt.Errorf("address %s no longer belongs to interface %s", address, name)
}

func ipFromAddr(addr net.Addr) net.IP {
	switch a := addr.(type) {
	case *net.IPNet:
		return a.IP
	case *net.IPAddr:
		return a.IP
	default:
		host, _, err := net.SplitHostPort(addr.String())
		if err == nil {
			return net.ParseIP(host)
		}
		s := addr.String()
		if idx := strings.IndexByte(s, '/'); idx >= 0 {
			s = s[:idx]
		}
		return net.ParseIP(s)
	}
}

func nonce() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
