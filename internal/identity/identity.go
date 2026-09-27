package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	caCertFile     = "ca-cert.pem"
	caKeyFile      = "ca-key.pem"
	serverCertFile = "server-cert.pem"
	serverKeyFile  = "server-key.pem"
	agentKeyFile   = "agent-key.pem"
	agentCertFile  = "agent-cert.pem"
	agentCAFile    = "controller-ca.pem"
	agentStateFile = "agent.json"
)

type ControllerIdentity struct {
	CACertPEM      []byte
	CACert         *x509.Certificate
	CAKey          ed25519.PrivateKey
	Fingerprint    string
	ServerCertFile string
	ServerKeyFile  string
}

type AgentState struct {
	NodeID                string `json:"node_id"`
	Name                  string `json:"name"`
	SecureControllerURL   string `json:"secure_controller_url"`
	ControllerFingerprint string `json:"controller_fingerprint"`
	ProtocolVersion       string `json:"protocol_version"`
}

func DefaultControllerDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".nibia", "controller")
}

func DefaultAgentDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".nibia", "agent")
}

func EnsureController(dir string) (ControllerIdentity, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ControllerIdentity{}, err
	}

	caCertPath := filepath.Join(dir, caCertFile)
	caKeyPath := filepath.Join(dir, caKeyFile)

	var caCert *x509.Certificate
	var caKey ed25519.PrivateKey
	var caPEM []byte

	if fileExists(caCertPath) && fileExists(caKeyPath) {
		var err error
		caPEM, caCert, caKey, err = loadCA(caCertPath, caKeyPath)
		if err != nil {
			return ControllerIdentity{}, fmt.Errorf("load controller CA: %w", err)
		}
	} else {
		var err error
		caPEM, caCert, caKey, err = createCA(caCertPath, caKeyPath)
		if err != nil {
			return ControllerIdentity{}, fmt.Errorf("create controller CA: %w", err)
		}
	}

	serverCertPath := filepath.Join(dir, serverCertFile)
	serverKeyPath := filepath.Join(dir, serverKeyFile)
	if err := createServerCertificate(serverCertPath, serverKeyPath, caCert, caKey); err != nil {
		return ControllerIdentity{}, fmt.Errorf("create server certificate: %w", err)
	}

	return ControllerIdentity{
		CACertPEM:      caPEM,
		CACert:         caCert,
		CAKey:          caKey,
		Fingerprint:    FingerprintCertificate(caCert),
		ServerCertFile: serverCertPath,
		ServerKeyFile:  serverKeyPath,
	}, nil
}

func createCA(certPath, keyPath string) ([]byte, *x509.Certificate, ed25519.PrivateKey, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, nil, err
	}

	serial, err := randomSerial()
	if err != nil {
		return nil, nil, nil, err
	}
	now := time.Now().UTC()

	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "NIBIA Personal Fabric CA"},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.AddDate(10, 0, 0),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature | x509.KeyUsageCRLSign,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		return nil, nil, nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, nil, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})

	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, nil, nil, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})

	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		return nil, nil, nil, err
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return nil, nil, nil, err
	}
	return certPEM, cert, priv, nil
}

func loadCA(certPath, keyPath string) ([]byte, *x509.Certificate, ed25519.PrivateKey, error) {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, nil, nil, err
	}
	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil {
		return nil, nil, nil, errors.New("invalid CA certificate PEM")
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, nil, nil, err
	}

	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, nil, nil, err
	}
	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, nil, nil, errors.New("invalid CA private key PEM")
	}
	keyAny, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, nil, nil, err
	}
	key, ok := keyAny.(ed25519.PrivateKey)
	if !ok {
		return nil, nil, nil, errors.New("controller CA is not Ed25519")
	}
	return certPEM, cert, key, nil
}

func createServerCertificate(certPath, keyPath string, ca *x509.Certificate, caKey ed25519.PrivateKey) error {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	serial, err := randomSerial()
	if err != nil {
		return err
	}
	now := time.Now().UTC()

	dnsNames, ips := serverSANs()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "NIBIA Controller"},
		NotBefore:    now.Add(-5 * time.Minute),
		NotAfter:     now.AddDate(1, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     dnsNames,
		IPAddresses:  ips,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, pub, caKey)
	if err != nil {
		return err
	}

	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return err
	}

	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return err
	}
	return os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600)
}

func serverSANs() ([]string, []net.IP) {
	host, _ := os.Hostname()
	dnsSet := map[string]bool{"localhost": true}
	if host != "" {
		dnsSet[host] = true
		if !strings.HasSuffix(strings.ToLower(host), ".local") {
			dnsSet[host+".local"] = true
		}
	}

	ipSet := map[string]net.IP{
		"127.0.0.1": net.ParseIP("127.0.0.1"),
		"::1":       net.ParseIP("::1"),
	}
	ifaces, _ := net.Interfaces()
	for _, iface := range ifaces {
		addrs, _ := iface.Addrs()
		for _, addr := range addrs {
			var ip net.IP
			switch a := addr.(type) {
			case *net.IPNet:
				ip = a.IP
			case *net.IPAddr:
				ip = a.IP
			}
			if ip == nil {
				continue
			}
			ipSet[ip.String()] = ip
		}
	}

	var dns []string
	for name := range dnsSet {
		dns = append(dns, name)
	}
	var ips []net.IP
	for _, ip := range ipSet {
		ips = append(ips, ip)
	}
	return dns, ips
}

func EnsureAgentIdentity(dir, requestedName string) (AgentState, ed25519.PrivateKey, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return AgentState{}, nil, err
	}
	statePath := filepath.Join(dir, agentStateFile)
	keyPath := filepath.Join(dir, agentKeyFile)

	var state AgentState
	if b, err := os.ReadFile(statePath); err == nil {
		_ = json.Unmarshal(b, &state)
	}
	if state.NodeID == "" {
		id, err := randomID()
		if err != nil {
			return AgentState{}, nil, err
		}
		state.NodeID = id
	}
	if strings.TrimSpace(requestedName) != "" {
		state.Name = strings.TrimSpace(requestedName)
	}
	if state.Name == "" {
		host, _ := os.Hostname()
		state.Name = host
	}

	var priv ed25519.PrivateKey
	if fileExists(keyPath) {
		b, err := os.ReadFile(keyPath)
		if err != nil {
			return AgentState{}, nil, err
		}
		block, _ := pem.Decode(b)
		if block == nil {
			return AgentState{}, nil, errors.New("invalid agent private key PEM")
		}
		keyAny, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return AgentState{}, nil, err
		}
		var ok bool
		priv, ok = keyAny.(ed25519.PrivateKey)
		if !ok {
			return AgentState{}, nil, errors.New("agent key is not Ed25519")
		}
	} else {
		_, key, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return AgentState{}, nil, err
		}
		priv = key
		der, err := x509.MarshalPKCS8PrivateKey(priv)
		if err != nil {
			return AgentState{}, nil, err
		}
		if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
			return AgentState{}, nil, err
		}
	}

	if err := SaveAgentState(dir, state); err != nil {
		return AgentState{}, nil, err
	}
	return state, priv, nil
}

func CreateCSR(state AgentState, priv ed25519.PrivateKey) ([]byte, error) {
	tmpl := &x509.CertificateRequest{
		Subject: pkix.Name{
			CommonName:         state.NodeID,
			Organization:       []string{"NIBIA Node"},
			OrganizationalUnit: []string{state.Name},
		},
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, tmpl, priv)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}), nil
}

func SignClientCSR(csrPEM []byte, nodeID, name string, ca *x509.Certificate, caKey ed25519.PrivateKey) ([]byte, *x509.Certificate, error) {
	block, _ := pem.Decode(csrPEM)
	if block == nil {
		return nil, nil, errors.New("invalid CSR PEM")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, nil, err
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, nil, fmt.Errorf("invalid CSR signature: %w", err)
	}

	serial, err := randomSerial()
	if err != nil {
		return nil, nil, err
	}
	now := time.Now().UTC()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:         nodeID,
			Organization:       []string{"NIBIA Node"},
			OrganizationalUnit: []string{name},
		},
		NotBefore:   now.Add(-5 * time.Minute),
		NotAfter:    now.AddDate(1, 0, 0),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, csr.PublicKey, caKey)
	if err != nil {
		return nil, nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), cert, nil
}

func SaveAgentCredentials(dir string, state AgentState, certPEM, caPEM []byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, agentCertFile), certPEM, 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, agentCAFile), caPEM, 0o644); err != nil {
		return err
	}
	return SaveAgentState(dir, state)
}

func SaveAgentState(dir string, state AgentState) error {
	b, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, agentStateFile), b, 0o600)
}

func LoadAgentState(dir string) (AgentState, error) {
	var state AgentState
	b, err := os.ReadFile(filepath.Join(dir, agentStateFile))
	if err != nil {
		return state, err
	}
	err = json.Unmarshal(b, &state)
	return state, err
}

func AgentCredentialPaths(dir string) (certPath, keyPath, caPath string) {
	return filepath.Join(dir, agentCertFile), filepath.Join(dir, agentKeyFile), filepath.Join(dir, agentCAFile)
}

func FingerprintCertificate(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)
	raw := strings.ToUpper(hex.EncodeToString(sum[:]))
	var parts []string
	for i := 0; i < len(raw); i += 2 {
		parts = append(parts, raw[i:i+2])
	}
	return strings.Join(parts, ":")
}

func FingerprintPEM(certPEM []byte) (string, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return "", errors.New("invalid certificate PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", err
	}
	return FingerprintCertificate(cert), nil
}

func HostForSecureURL(pairURL string, securePort int) (string, error) {
	u, err := url.Parse(pairURL)
	if err != nil {
		return "", err
	}
	host := u.Hostname()
	if host == "" {
		return "", errors.New("pair URL has no host")
	}
	return "https://" + net.JoinHostPort(host, fmt.Sprintf("%d", securePort)), nil
}

func randomSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	return rand.Int(rand.Reader, limit)
}

func randomID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "node-" + hex.EncodeToString(b), nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func ResetAgentIdentity(dir string) ([]string, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("agent state directory cannot be empty")
	}

	paths := []string{
		filepath.Join(dir, agentStateFile),
		filepath.Join(dir, agentCertFile),
		filepath.Join(dir, agentCAFile),
		filepath.Join(dir, agentKeyFile),
	}

	var removed []string
	for _, path := range paths {
		err := os.Remove(path)
		switch {
		case err == nil:
			removed = append(removed, path)
		case errors.Is(err, os.ErrNotExist):
			continue
		default:
			return removed, err
		}
	}
	return removed, nil
}
