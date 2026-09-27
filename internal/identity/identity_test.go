package identity

import (
	"crypto/x509"
	"encoding/pem"
	"testing"
)

func TestControllerAndClientCertificateFlow(t *testing.T) {
	controllerDir := t.TempDir()
	agentDir := t.TempDir()

	ctrl, err := EnsureController(controllerDir)
	if err != nil {
		t.Fatal(err)
	}
	if ctrl.Fingerprint == "" {
		t.Fatal("missing controller fingerprint")
	}

	state, key, err := EnsureAgentIdentity(agentDir, "test-node")
	if err != nil {
		t.Fatal(err)
	}
	csr, err := CreateCSR(state, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM, cert, err := SignClientCSR(csr, state.NodeID, state.Name, ctrl.CACert, ctrl.CAKey)
	if err != nil {
		t.Fatal(err)
	}
	if cert.Subject.CommonName != state.NodeID {
		t.Fatalf("certificate CN=%q, want %q", cert.Subject.CommonName, state.NodeID)
	}
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("invalid certificate PEM")
	}
	parsed, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.ExtKeyUsage) == 0 || parsed.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth {
		t.Fatal("client certificate missing client auth usage")
	}
}

func TestControllerIdentityPersists(t *testing.T) {
	dir := t.TempDir()
	a, err := EnsureController(dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := EnsureController(dir)
	if err != nil {
		t.Fatal(err)
	}
	if a.Fingerprint != b.Fingerprint {
		t.Fatalf("controller CA identity changed: %s != %s", a.Fingerprint, b.Fingerprint)
	}
}

func TestResetAgentIdentity(t *testing.T) {
	dir := t.TempDir()
	state, _, err := EnsureAgentIdentity(dir, "node-to-reset")
	if err != nil {
		t.Fatal(err)
	}
	if state.NodeID == "" {
		t.Fatal("missing node id")
	}

	removed, err := ResetAgentIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) == 0 {
		t.Fatal("expected identity files to be removed")
	}

	newState, _, err := EnsureAgentIdentity(dir, "node-to-reset")
	if err != nil {
		t.Fatal(err)
	}
	if newState.NodeID == state.NodeID {
		t.Fatal("reset did not create a new node identity")
	}
}
