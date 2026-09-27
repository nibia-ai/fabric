package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/nibia-ai/fabric/internal/executor"
	"github.com/nibia-ai/fabric/internal/identity"
	"github.com/nibia-ai/fabric/internal/lease"
	"github.com/nibia-ai/fabric/internal/types"
	"github.com/nibia-ai/fabric/internal/version"
)

func testController(t *testing.T) *controller {
	t.Helper()

	dir := t.TempDir()
	ident, err := identity.EnsureController(dir)
	if err != nil {
		t.Fatal(err)
	}
	trust, err := newTrustStore(filepath.Join(dir, "trust.json"))
	if err != nil {
		t.Fatal(err)
	}
	jobs := lease.NewStore(nil, nil)

	return &controller{
		reg:           newRegistry(),
		pairs:         newPairCodes(),
		trust:         trust,
		jobs:          jobs,
		relayHub:      nil,
		relays:        nil,
		ident:         ident,
		securePort:    8443,
		advertiseHost: "127.0.0.1",
		pairPort:      8080,
	}
}

func protocolRequest(t *testing.T, method, url string, body []byte) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(headerVersion, version.Version)
	req.Header.Set(headerProtocol, version.ProtocolVersion)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

func TestInfoEndpoint(t *testing.T) {
	c := testController(t)
	srv := httptest.NewServer(c.adminMux())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/info")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var info types.ControllerInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		t.Fatal(err)
	}
	if info.Version != version.Version {
		t.Fatalf("version=%q want %q", info.Version, version.Version)
	}
	if info.ProtocolVersion != version.ProtocolVersion {
		t.Fatalf("protocol=%q want %q", info.ProtocolVersion, version.ProtocolVersion)
	}
	if info.Fingerprint == "" {
		t.Fatal("controller fingerprint is empty")
	}
}

func TestPairingCodeAndClaim(t *testing.T) {
	c := testController(t)
	srv := httptest.NewServer(c.adminMux())
	defer srv.Close()

	req := protocolRequest(t, http.MethodPost, srv.URL+"/v1/pairing/code", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("pair code status=%d want %d", resp.StatusCode, http.StatusCreated)
	}

	var pairCode types.PairCodeResponse
	if err := json.NewDecoder(resp.Body).Decode(&pairCode); err != nil {
		t.Fatal(err)
	}
	if len(pairCode.Code) != 6 {
		t.Fatalf("pair code=%q, expected 6 digits", pairCode.Code)
	}

	agentDir := t.TempDir()
	state, key, err := identity.EnsureAgentIdentity(agentDir, "test-secure-node")
	if err != nil {
		t.Fatal(err)
	}
	csr, err := identity.CreateCSR(state, key)
	if err != nil {
		t.Fatal(err)
	}

	claim := types.PairClaimRequest{
		Code:            pairCode.Code,
		NodeID:          state.NodeID,
		Name:            state.Name,
		ProtocolVersion: version.ProtocolVersion,
		CSRPEM:          string(csr),
	}
	body, err := json.Marshal(claim)
	if err != nil {
		t.Fatal(err)
	}

	claimReq := protocolRequest(t, http.MethodPost, srv.URL+"/v1/pairing/claim", body)
	claimResp, err := http.DefaultClient.Do(claimReq)
	if err != nil {
		t.Fatal(err)
	}
	defer claimResp.Body.Close()

	if claimResp.StatusCode != http.StatusCreated {
		t.Fatalf("claim status=%d want %d", claimResp.StatusCode, http.StatusCreated)
	}

	var paired types.PairClaimResponse
	if err := json.NewDecoder(claimResp.Body).Decode(&paired); err != nil {
		t.Fatal(err)
	}
	if paired.CertificatePEM == "" || paired.CACertificatePEM == "" {
		t.Fatal("pairing did not return certificates")
	}
	if paired.ControllerFingerprint != c.ident.Fingerprint {
		t.Fatalf("fingerprint=%q want %q", paired.ControllerFingerprint, c.ident.Fingerprint)
	}

	records := c.trust.list()
	if len(records) != 1 {
		t.Fatalf("trusted records=%d want 1", len(records))
	}
	if records[0].NodeID != state.NodeID {
		t.Fatalf("trusted node=%q want %q", records[0].NodeID, state.NodeID)
	}
}

func TestPairCodeCannotBeReusedAfterSuccessfulClaim(t *testing.T) {
	c := testController(t)

	code, _, err := c.pairs.create()
	if err != nil {
		t.Fatal(err)
	}
	if !c.pairs.consume(code) {
		t.Fatal("fresh pairing code was rejected")
	}
	c.pairs.success(code)
	if c.pairs.consume(code) {
		t.Fatal("pairing code was reusable after success")
	}
}

func TestAdminEndpointRejectsNonLoopback(t *testing.T) {
	c := testController(t)

	req := protocolRequest(t, http.MethodGet, "http://controller/v1/trust", nil)
	req.RemoteAddr = "192.168.1.25:54321"
	rr := httptest.NewRecorder()

	c.adminMux().ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status=%d want %d", rr.Code, http.StatusForbidden)
	}
}

func TestRevokePrefersActiveSameNameAfterRejoin(t *testing.T) {
	c := testController(t)

	oldRevoked := types.TrustRecord{
		NodeID: "old-id", Name: "Worker-Linux", Serial: "1", IssuedAt: time.Now().Add(-time.Hour),
	}
	now := time.Now()
	oldRevoked.RevokedAt = &now

	active := types.TrustRecord{
		NodeID: "new-id", Name: "Worker-Linux", Serial: "2", IssuedAt: time.Now(),
	}

	c.trust.records = []types.TrustRecord{oldRevoked, active}
	got, ok, err := c.trust.revoke("Worker-Linux")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected matching trust record")
	}
	if got.NodeID != "new-id" {
		t.Fatalf("revoked %q; want active new-id", got.NodeID)
	}
}

func TestRegistryGracefulOfflineAndHeartbeatRecovery(t *testing.T) {
	r := newRegistry()
	n := types.Node{ID: "win", Name: "Worker-Windows"}
	r.upsert(n)
	status := r.list()
	if len(status) != 1 || status[0].State != "online" {
		t.Fatalf("expected online after heartbeat, got %+v", status)
	}
	if !r.markOffline("win") {
		t.Fatal("expected markOffline to find registered node")
	}
	status = r.list()
	if len(status) != 1 || status[0].State != "offline" {
		t.Fatalf("expected immediate offline after graceful shutdown, got %+v", status)
	}
	r.upsert(n)
	status = r.list()
	if len(status) != 1 || status[0].State != "online" {
		t.Fatalf("expected next heartbeat to restore online state, got %+v", status)
	}
}

func TestLifecycleCleanupWorkload(t *testing.T) {
	if !lifecycleCleanupWorkload(executor.TaskLlamaRPCStop) {
		t.Fatal("RPC stop must be lifecycle cleanup")
	}
	if !lifecycleCleanupWorkload(executor.TaskPowerGuardRelease) {
		t.Fatal("power guard release must be lifecycle cleanup")
	}
	if lifecycleCleanupWorkload(executor.TaskLlamaRPCStart) {
		t.Fatal("RPC start must not target offline nodes")
	}
}

func TestPinnedLifecycleCleanupSchedulesAgainstTemporarilyOfflineNode(t *testing.T) {
	c := testController(t)
	c.reg.upsert(types.Node{
		ID:            "win",
		Name:          "Windows-Worker",
		ResourceState: "PRESSURED",
		Capabilities: []string{
			"executor-v1",
			"task-llamacpp-rpc-worker",
			"runtime:llama.cpp",
			"llamacpp:rpc-worker",
		},
	})
	if !c.reg.markOffline("win") {
		t.Fatal("expected test node to be registered")
	}

	cleanup, status, err := c.scheduleJob(types.JobRequest{
		Name: "cleanup-rpc-win",
		Requirements: types.JobRequirements{
			RequiredNodeID: "win",
			AllowBusy:      true,
		},
		Workload:    types.WorkloadSpec{Type: executor.TaskLlamaRPCStop, RPCPort: 50052},
		MaxAttempts: 1,
	})
	if err != nil || status != http.StatusCreated {
		t.Fatalf("cleanup schedule failed: status=%d err=%v", status, err)
	}
	if cleanup.State != "PLACED" || cleanup.Placement.SelectedNodeID != "win" {
		t.Fatalf("cleanup=%+v", cleanup)
	}

	ordinary, status, err := c.scheduleJob(types.JobRequest{
		Name: "start-rpc-win",
		Requirements: types.JobRequirements{
			RequiredNodeID: "win",
			AllowBusy:      true,
		},
		Workload:    types.WorkloadSpec{Type: executor.TaskLlamaRPCStart, RPCPort: 50052},
		MaxAttempts: 1,
	})
	if err != nil || status != http.StatusCreated {
		t.Fatalf("ordinary schedule failed: status=%d err=%v", status, err)
	}
	if ordinary.State != "UNSCHEDULABLE" {
		t.Fatalf("ordinary workload must still reject offline node: %+v", ordinary)
	}
}

func TestGenerativeFabricEndpoint(t *testing.T) {
	c := testController(t)
	c.reg.upsert(types.Node{
		ID: "mac", Name: "Mac", OS: "darwin", Arch: "arm64",
		CPUPhysical: 10, CPULogical: 10,
		MemoryTotalMB: 16384, MemoryAvailableMB: 9000,
		ResourceState: "READY",
		Capabilities:  []string{"runtime:llama.cpp", "llamacpp:rpc-coordinator", "llamacpp:rpc-worker"},
		Runtimes:      []types.RuntimeInventory{{Name: "llama.cpp", Installed: true, Reachable: true, Features: []string{"rpc-coordinator", "rpc-worker"}}},
	})
	srv := httptest.NewServer(c.adminMux())
	defer srv.Close()

	req := protocolRequest(t, http.MethodGet, srv.URL+"/v1/generative/fabric", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200", resp.StatusCode)
	}
	var summary types.GenerativeFabricSummary
	if err := json.NewDecoder(resp.Body).Decode(&summary); err != nil {
		t.Fatal(err)
	}
	if summary.KnownNodes != 1 || summary.OnlineNodes != 1 {
		t.Fatalf("unexpected summary: %+v", summary)
	}
}
