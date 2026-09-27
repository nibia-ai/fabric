package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nibia-ai/fabric/internal/discovery"
	"github.com/nibia-ai/fabric/internal/executor"
	"github.com/nibia-ai/fabric/internal/fabric"
	"github.com/nibia-ai/fabric/internal/identity"
	"github.com/nibia-ai/fabric/internal/lease"
	"github.com/nibia-ai/fabric/internal/rpcrelay"
	"github.com/nibia-ai/fabric/internal/scheduler"
	"github.com/nibia-ai/fabric/internal/types"
	"github.com/nibia-ai/fabric/internal/version"
)

const (
	headerVersion  = "X-Nibia-Version"
	headerProtocol = "X-Nibia-Protocol"
)

type nodeRecord struct {
	Node     types.Node `json:"node"`
	LastSeen time.Time  `json:"last_seen"`
}

type registry struct {
	mu    sync.RWMutex
	nodes map[string]nodeRecord
}

const nodeOfflineAfter = 30 * time.Second

func newRegistry() *registry { return &registry{nodes: make(map[string]nodeRecord)} }

func (r *registry) upsert(n types.Node) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nodes[n.ID] = nodeRecord{Node: n, LastSeen: time.Now().UTC()}
}

// markOffline records a clean agent shutdown immediately instead of waiting
// for the normal heartbeat timeout. A subsequent heartbeat simply upserts a
// fresh record and makes the node online again.
func (r *registry) markOffline(nodeID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.nodes[nodeID]
	if !ok {
		return false
	}
	rec.LastSeen = time.Now().UTC().Add(-nodeOfflineAfter - time.Millisecond)
	r.nodes[nodeID] = rec
	return true
}

func (r *registry) list() []types.NodeStatus {
	r.mu.RLock()
	defer r.mu.RUnlock()

	now := time.Now().UTC()
	out := make([]types.NodeStatus, 0, len(r.nodes))
	for _, rec := range r.nodes {
		state := "online"
		if now.Sub(rec.LastSeen) > nodeOfflineAfter {
			state = "offline"
		}
		out = append(out, types.NodeStatus{Node: rec.Node, LastSeen: rec.LastSeen, State: state})
	}
	return out
}

type pairEntry struct {
	ExpiresAt time.Time
	Attempts  int
}

type pairCodes struct {
	mu    sync.Mutex
	codes map[string]pairEntry
}

func newPairCodes() *pairCodes { return &pairCodes{codes: make(map[string]pairEntry)} }

func (p *pairCodes) create() (string, time.Time, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for i := 0; i < 20; i++ {
		n, err := rand.Int(rand.Reader, bigLimit())
		if err != nil {
			return "", time.Time{}, err
		}
		code := fmt.Sprintf("%06d", n.Int64())
		if _, exists := p.codes[code]; exists {
			continue
		}
		expires := time.Now().UTC().Add(5 * time.Minute)
		p.codes[code] = pairEntry{ExpiresAt: expires}
		return code, expires, nil
	}
	return "", time.Time{}, fmt.Errorf("unable to allocate pairing code")
}

func bigLimit() *big.Int {
	return big.NewInt(1000000)
}

func (p *pairCodes) consume(code string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	entry, ok := p.codes[code]
	if !ok || time.Now().UTC().After(entry.ExpiresAt) {
		delete(p.codes, code)
		return false
	}
	entry.Attempts++
	if entry.Attempts > 5 {
		delete(p.codes, code)
		return false
	}
	p.codes[code] = entry
	return true
}

func (p *pairCodes) success(code string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.codes, code)
}

type trustStore struct {
	mu      sync.RWMutex
	path    string
	records []types.TrustRecord
}

func newTrustStore(path string) (*trustStore, error) {
	s := &trustStore{path: path}
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, &s.records); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *trustStore) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s.records, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, b, 0o600)
}

func (s *trustStore) add(r types.TrustRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.records {
		if s.records[i].NodeID == r.NodeID {
			s.records[i] = r
			return s.saveLocked()
		}
	}
	s.records = append(s.records, r)
	return s.saveLocked()
}

func (s *trustStore) list() []types.TrustRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]types.TrustRecord, len(s.records))
	copy(out, s.records)
	return out
}

func (s *trustStore) trusted(serial string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, r := range s.records {
		if r.Serial == serial && r.RevokedAt == nil {
			return true
		}
	}
	return false
}

func (s *trustStore) revoke(needle string) (types.TrustRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Prefer an active matching record. This matters after a revoked device
	// resets its local identity and re-pairs using the same friendly name.
	for i := range s.records {
		if (strings.EqualFold(s.records[i].NodeID, needle) || strings.EqualFold(s.records[i].Name, needle)) &&
			s.records[i].RevokedAt == nil {
			now := time.Now().UTC()
			s.records[i].RevokedAt = &now
			if err := s.saveLocked(); err != nil {
				return types.TrustRecord{}, false, err
			}
			return s.records[i], true, nil
		}
	}

	// If there is only a historical revoked record, return it for idempotency.
	for i := range s.records {
		if strings.EqualFold(s.records[i].NodeID, needle) || strings.EqualFold(s.records[i].Name, needle) {
			return s.records[i], true, nil
		}
	}
	return types.TrustRecord{}, false, nil
}

func loadJobs(path string) ([]types.ScheduledJob, error) {
	var jobs []types.ScheduledJob
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, &jobs); err != nil {
			return nil, err
		}
	}
	return jobs, nil
}

func saveJobs(path string, jobs []types.ScheduledJob) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(jobs, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

func newJobID() (string, error) {
	return randomControllerID("job")
}

func randomControllerID(prefix string) (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return prefix + "-" + hex.EncodeToString(b), nil
}

type controller struct {
	reg           *registry
	pairs         *pairCodes
	trust         *trustStore
	jobs          *lease.Store
	relayHub      *rpcrelay.Hub
	relays        *rpcrelay.Manager
	ident         identity.ControllerIdentity
	securePort    int
	advertiseHost string
	pairPort      int
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set(headerVersion, version.Version)
	w.Header().Set(headerProtocol, version.ProtocolVersion)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func requireProtocol(w http.ResponseWriter, r *http.Request) bool {
	got := strings.TrimSpace(r.Header.Get(headerProtocol))
	if got == version.ProtocolVersion {
		return true
	}
	writeJSON(w, http.StatusUpgradeRequired, types.APIError{
		Error:             "incompatible NIBIA protocol",
		ControllerVersion: version.Version,
		ProtocolVersion:   version.ProtocolVersion,
		ReceivedProtocol:  got,
		RecommendedAction: "use a NIBIA client/agent that supports protocol " + version.ProtocolVersion,
	})
	return false
}

func requireLocal(w http.ResponseWriter, r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip != nil && ip.IsLoopback() {
		return true
	}
	writeJSON(w, http.StatusForbidden, types.APIError{
		Error:             "admin operation is restricted to localhost",
		RecommendedAction: "run this command on the controller host using http://127.0.0.1",
	})
	return false
}

func (c *controller) publicInfo() types.ControllerInfo {
	return types.ControllerInfo{
		Name:                "NIBIA Controller",
		Version:             version.Version,
		ProtocolVersion:     version.ProtocolVersion,
		Fingerprint:         c.ident.Fingerprint,
		SecureControllerURL: "https://" + net.JoinHostPort(c.advertiseHost, strconv.Itoa(c.securePort)),
		Discovery:           discovery.DiscoveryVersion,
	}
}

func (c *controller) pairURL() string {
	return "http://" + net.JoinHostPort(c.advertiseHost, strconv.Itoa(c.pairPort))
}

func lifecycleCleanupWorkload(workload string) bool {
	switch strings.TrimSpace(workload) {
	case executor.TaskLlamaRPCStop, executor.TaskPowerGuardRelease:
		return true
	default:
		return false
	}
}

func (c *controller) scheduleJob(req types.JobRequest) (types.ScheduledJob, int, error) {
	return c.scheduleJobWithHints(req, types.PlacementHints{})
}

func (c *controller) scheduleJobWithHints(
	req types.JobRequest,
	hints types.PlacementHints,
) (types.ScheduledJob, int, error) {
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		return types.ScheduledJob{}, http.StatusBadRequest, errors.New("job name is required")
	}
	if req.Requirements.MinPhysicalCores < 0 || req.Requirements.MinLogicalCores < 0 {
		return types.ScheduledJob{}, http.StatusBadRequest, errors.New("CPU requirements cannot be negative")
	}
	if err := executor.NormalizeWorkload(&req.Workload); err != nil {
		return types.ScheduledJob{}, http.StatusBadRequest, err
	}
	if req.MaxAttempts <= 0 {
		req.MaxAttempts = 3
	}
	if req.MaxAttempts > 10 {
		return types.ScheduledJob{}, http.StatusBadRequest, errors.New("max_attempts cannot exceed 10")
	}

	for _, cap := range executor.RequiredCapabilities(req.Workload) {
		if !containsCapability(req.Requirements.Capabilities, cap) {
			req.Requirements.Capabilities = append(req.Requirements.Capabilities, cap)
		}
	}

	id, err := newJobID()
	if err != nil {
		return types.ScheduledJob{}, http.StatusInternalServerError, fmt.Errorf("create job id: %w", err)
	}

	placement := scheduler.PlaceWithHints(c.reg.list(), req.Requirements, hints)
	if strings.TrimSpace(req.Requirements.RequiredNodeID) != "" && lifecycleCleanupWorkload(req.Workload.Type) {
		placement = scheduler.PlaceLifecycleCleanup(c.reg.list(), req.Requirements, hints)
	}
	state := scheduler.StatePlaced
	if placement.SelectedNodeID == "" {
		state = scheduler.StateUnschedulable
	}

	now := time.Now().UTC()
	job := types.ScheduledJob{
		ID:              id,
		Name:            req.Name,
		State:           state,
		Requirements:    req.Requirements,
		Workload:        req.Workload,
		Placement:       placement,
		MaxAttempts:     req.MaxAttempts,
		CreatedAt:       now,
		UpdatedAt:       now,
		ExecutionStatus: "QUEUED",
	}
	if state == scheduler.StateUnschedulable {
		job.ExecutionStatus = "WAITING_FOR_NODE"
	}
	if err := c.jobs.Add(job); err != nil {
		return types.ScheduledJob{}, http.StatusInternalServerError, fmt.Errorf("persist job: %w", err)
	}
	return job, http.StatusCreated, nil
}

func (c *controller) serveRelayTLS(ctx context.Context, address string, baseTLS *tls.Config, certFile, keyFile string) error {
	cfg := baseTLS.Clone()
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return err
	}
	cfg.Certificates = []tls.Certificate{cert}
	ln, err := tls.Listen("tcp", address, cfg)
	if err != nil {
		return err
	}
	defer ln.Close()
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	log.Printf("authenticated RPC relay listening on %s (mTLS; no raw llama.cpp RPC exposed)", address)
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go c.acceptRelayTunnel(conn)
	}
}

func (c *controller) acceptRelayTunnel(raw net.Conn) {
	conn, ok := raw.(*tls.Conn)
	if !ok {
		_ = raw.Close()
		return
	}
	defer func() {
		// Ownership transfers to the hub only after successful registration.
		if conn != nil {
			_ = conn.Close()
		}
	}()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if err := conn.Handshake(); err != nil {
		return
	}
	state := conn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return
	}
	peer := state.PeerCertificates[0]
	if !c.trust.trusted(peer.SerialNumber.String()) {
		return
	}
	reader := bufio.NewReader(conn)
	line, err := reader.ReadString('\n')
	if err != nil {
		return
	}
	fields := strings.Fields(strings.TrimSpace(line))
	if len(fields) != 3 || fields[0] != rpcrelay.ProtocolMagic {
		return
	}
	nodeID := fields[1]
	sessionID := fields[2]
	if nodeID != peer.Subject.CommonName || strings.TrimSpace(sessionID) == "" {
		return
	}
	if _, err := fmt.Fprintln(conn, "READY"); err != nil {
		return
	}
	_ = conn.SetDeadline(time.Time{})
	if err := c.relayHub.Register(nodeID, sessionID, conn); err != nil {
		return
	}
	conn = nil
}

func (c *controller) generativeNodeByID(nodeID string) (types.GenerativeNodeCapability, bool) {
	for _, node := range fabric.GenerativeSummary(c.reg.list()).Nodes {
		if strings.EqualFold(node.NodeID, strings.TrimSpace(nodeID)) {
			return node, true
		}
	}
	return types.GenerativeNodeCapability{}, false
}

func (c *controller) adminMux() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{
			"status": "ok", "version": version.Version, "protocol_version": version.ProtocolVersion,
		})
	})

	mux.HandleFunc("/v1/info", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, c.publicInfo())
	})

	mux.HandleFunc("/v1/pairing/code", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !requireLocal(w, r) || !requireProtocol(w, r) {
			return
		}
		code, expires, err := c.pairs.create()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, types.APIError{Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusCreated, types.PairCodeResponse{Code: code, ExpiresAt: expires})
	})

	mux.HandleFunc("/v1/pairing/claim", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req types.PairClaimRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, types.APIError{Error: "invalid pairing request"})
			return
		}
		if req.ProtocolVersion != version.ProtocolVersion {
			writeJSON(w, http.StatusUpgradeRequired, types.APIError{
				Error: "pairing protocol mismatch", ProtocolVersion: version.ProtocolVersion, ReceivedProtocol: req.ProtocolVersion,
			})
			return
		}
		req.Code = strings.TrimSpace(req.Code)
		req.NodeID = strings.TrimSpace(req.NodeID)
		req.Name = strings.TrimSpace(req.Name)
		if req.Code == "" || req.NodeID == "" || req.Name == "" || req.CSRPEM == "" {
			writeJSON(w, http.StatusBadRequest, types.APIError{Error: "code, node_id, name and csr_pem are required"})
			return
		}
		if !c.pairs.consume(req.Code) {
			writeJSON(w, http.StatusUnauthorized, types.APIError{Error: "invalid, expired, or exhausted pairing code"})
			return
		}

		certPEM, cert, err := identity.SignClientCSR([]byte(req.CSRPEM), req.NodeID, req.Name, c.ident.CACert, c.ident.CAKey)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, types.APIError{Error: "invalid CSR: " + err.Error()})
			return
		}
		sum := sha256.Sum256(cert.Raw)
		record := types.TrustRecord{
			NodeID:      req.NodeID,
			Name:        req.Name,
			Serial:      cert.SerialNumber.String(),
			Fingerprint: strings.ToUpper(hex.EncodeToString(sum[:])),
			IssuedAt:    time.Now().UTC(),
		}
		if err := c.trust.add(record); err != nil {
			writeJSON(w, http.StatusInternalServerError, types.APIError{Error: "persist trust record: " + err.Error()})
			return
		}
		c.pairs.success(req.Code)

		host := requestHostname(r)
		if host == "" {
			host = c.advertiseHost
		}
		secureURL := "https://" + net.JoinHostPort(host, strconv.Itoa(c.securePort))

		writeJSON(w, http.StatusCreated, types.PairClaimResponse{
			CertificatePEM:        certPEMString(certPEM),
			CACertificatePEM:      certPEMString(c.ident.CACertPEM),
			SecureControllerURL:   secureURL,
			ControllerFingerprint: c.ident.Fingerprint,
			ProtocolVersion:       version.ProtocolVersion,
		})
	})

	mux.HandleFunc("/v1/nodes", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !requireLocal(w, r) || !requireProtocol(w, r) {
			return
		}
		writeJSON(w, http.StatusOK, c.reg.list())
	})

	mux.HandleFunc("/v1/generative/fabric", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !requireLocal(w, r) || !requireProtocol(w, r) {
			return
		}
		writeJSON(w, http.StatusOK, fabric.GenerativeSummary(c.reg.list()))
	})

	mux.HandleFunc("/v1/generative/relays/", func(w http.ResponseWriter, r *http.Request) {
		if !requireLocal(w, r) || !requireProtocol(w, r) {
			return
		}
		rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/generative/relays/"), "/")
		parts := strings.Split(rest, "/")
		if len(parts) < 1 || parts[0] == "" {
			writeJSON(w, http.StatusBadRequest, types.APIError{Error: "node id is required"})
			return
		}
		nodeID := parts[0]
		if len(parts) == 2 && parts[1] == "probe" {
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			result := c.relays.Probe(nodeID, 5*time.Second)
			status := http.StatusOK
			if !result.Success {
				status = http.StatusServiceUnavailable
			}
			writeJSON(w, status, result)
			return
		}
		if len(parts) != 1 {
			writeJSON(w, http.StatusBadRequest, types.APIError{Error: "invalid relay path"})
			return
		}
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, c.relays.Status(nodeID))
		case http.MethodDelete:
			status, err := c.relays.Stop(nodeID)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, types.APIError{Error: err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, status)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	mux.HandleFunc("/v1/generative/relays", func(w http.ResponseWriter, r *http.Request) {
		if !requireLocal(w, r) || !requireProtocol(w, r) {
			return
		}
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, c.relays.List())
		case http.MethodPost:
			var req types.GenerativeRelayRequest
			if err := json.NewDecoder(io.LimitReader(r.Body, 16*1024)).Decode(&req); err != nil {
				writeJSON(w, http.StatusBadRequest, types.APIError{Error: "invalid generative relay request"})
				return
			}
			node, ok := c.generativeNodeByID(req.NodeID)
			if !ok || strings.EqualFold(node.NodeState, "OFFLINE") {
				writeJSON(w, http.StatusNotFound, types.APIError{Error: "generative node is not online"})
				return
			}
			if !node.WorkerCapable {
				writeJSON(w, http.StatusConflict, types.APIError{Error: "node is not llama.cpp RPC-worker capable"})
				return
			}
			req.NodeName = node.NodeName
			status, err := c.relays.Start(req)
			if err != nil {
				writeJSON(w, http.StatusConflict, types.APIError{Error: err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, status)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	mux.HandleFunc("/v1/trust", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !requireLocal(w, r) || !requireProtocol(w, r) {
			return
		}
		writeJSON(w, http.StatusOK, c.trust.list())
	})

	mux.HandleFunc("/v1/trust/revoke", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !requireLocal(w, r) || !requireProtocol(w, r) {
			return
		}
		var req types.RevokeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Node) == "" {
			writeJSON(w, http.StatusBadRequest, types.APIError{Error: "node is required"})
			return
		}
		record, ok, err := c.trust.revoke(strings.TrimSpace(req.Node))
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, types.APIError{Error: err.Error()})
			return
		}
		if !ok {
			writeJSON(w, http.StatusNotFound, types.APIError{Error: "trusted node not found"})
			return
		}
		writeJSON(w, http.StatusOK, record)
	})

	mux.HandleFunc("/v1/jobs", func(w http.ResponseWriter, r *http.Request) {
		if !requireLocal(w, r) || !requireProtocol(w, r) {
			return
		}

		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, sortedJobs(c.jobs.List()))

		case http.MethodPost:
			var req types.JobRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeJSON(w, http.StatusBadRequest, types.APIError{Error: "invalid job request"})
				return
			}
			job, status, err := c.scheduleJob(req)
			if err != nil {
				writeJSON(w, status, types.APIError{Error: err.Error()})
				return
			}
			writeJSON(w, http.StatusCreated, job)

		default:
			w.Header().Set("Allow", "GET, POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	mux.HandleFunc("/v1/jobs/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !requireLocal(w, r) || !requireProtocol(w, r) {
			return
		}
		id := strings.TrimSpace(strings.TrimPrefix(r.URL.Path, "/v1/jobs/"))
		if id == "" || strings.Contains(id, "/") {
			writeJSON(w, http.StatusBadRequest, types.APIError{Error: "job id is required"})
			return
		}
		job, ok := c.jobs.Get(id)
		if !ok {
			writeJSON(w, http.StatusNotFound, types.APIError{Error: "job not found"})
			return
		}
		writeJSON(w, http.StatusOK, job)
	})

	mux.HandleFunc("/v1/jobs/cancel/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !requireLocal(w, r) || !requireProtocol(w, r) {
			return
		}
		id := strings.TrimSpace(strings.TrimPrefix(r.URL.Path, "/v1/jobs/cancel/"))
		if id == "" || strings.Contains(id, "/") {
			writeJSON(w, http.StatusBadRequest, types.APIError{Error: "job id is required"})
			return
		}
		job, err := c.jobs.Cancel(id, time.Now().UTC())
		if err != nil {
			if errors.Is(err, lease.ErrNotFound) {
				writeJSON(w, http.StatusNotFound, types.APIError{Error: "job not found"})
				return
			}
			writeJSON(w, http.StatusConflict, types.APIError{Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, types.CancelJobResponse{ID: job.ID, State: job.State})
	})

	return mux
}

func (c *controller) secureMux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/nodes", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !requireProtocol(w, r) {
			return
		}
		if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
			writeJSON(w, http.StatusUnauthorized, types.APIError{Error: "client certificate required"})
			return
		}
		peer := r.TLS.PeerCertificates[0]
		if !c.trust.trusted(peer.SerialNumber.String()) {
			writeJSON(w, http.StatusForbidden, types.APIError{Error: "node certificate is not trusted or has been revoked"})
			return
		}

		var n types.Node
		if err := json.NewDecoder(r.Body).Decode(&n); err != nil {
			writeJSON(w, http.StatusBadRequest, types.APIError{Error: "invalid JSON"})
			return
		}
		if strings.TrimSpace(n.ID) == "" || strings.TrimSpace(n.Name) == "" {
			writeJSON(w, http.StatusBadRequest, types.APIError{Error: "id and name are required"})
			return
		}
		if n.ID != peer.Subject.CommonName {
			writeJSON(w, http.StatusForbidden, types.APIError{Error: "node id does not match certificate identity"})
			return
		}
		if n.ProtocolVersion != version.ProtocolVersion {
			writeJSON(w, http.StatusUpgradeRequired, types.APIError{Error: "heartbeat protocol mismatch"})
			return
		}
		n.UpdatedAt = time.Now().UTC()
		c.reg.upsert(n)
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "registered"})
	})

	mux.HandleFunc("/v1/nodes/offline", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !requireProtocol(w, r) {
			return
		}
		nodeID, _, ok := c.authenticatedNode(w, r)
		if !ok {
			return
		}
		c.reg.markOffline(nodeID)
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "offline"})
	})

	mux.HandleFunc("/v1/leases/next", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !requireProtocol(w, r) {
			return
		}
		nodeID, _, ok := c.authenticatedNode(w, r)
		if !ok {
			return
		}

		requested := strings.TrimSpace(r.URL.Query().Get("node_id"))
		if requested != "" && requested != nodeID {
			writeJSON(w, http.StatusForbidden, types.APIError{Error: "node_id does not match certificate identity"})
			return
		}

		assignment, found, err := c.jobs.ClaimNext(nodeID, leaseTTL, time.Now().UTC())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, types.APIError{Error: err.Error()})
			return
		}
		if !found {
			w.Header().Set(headerVersion, version.Version)
			w.Header().Set(headerProtocol, version.ProtocolVersion)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeJSON(w, http.StatusOK, assignment)
	})

	mux.HandleFunc("/v1/leases/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !requireProtocol(w, r) {
			return
		}
		nodeID, nodeName, ok := c.authenticatedNode(w, r)
		if !ok {
			return
		}

		rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/leases/"), "/")
		parts := strings.Split(rest, "/")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			writeJSON(w, http.StatusBadRequest, types.APIError{Error: "expected /v1/leases/{lease-id}/{action}"})
			return
		}
		leaseID := parts[0]
		action := parts[1]

		switch action {
		case "ack":
			var req types.LeaseAckRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeJSON(w, http.StatusBadRequest, types.APIError{Error: "invalid ack request"})
				return
			}
			if req.NodeID != nodeID {
				writeJSON(w, http.StatusForbidden, types.APIError{Error: "node id does not match certificate identity"})
				return
			}
			out, err := c.jobs.Ack(leaseID, nodeID, leaseTTL, time.Now().UTC())
			if writeLeaseError(w, err) {
				return
			}
			writeJSON(w, http.StatusOK, out)

		case "renew":
			var req types.LeaseRenewRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeJSON(w, http.StatusBadRequest, types.APIError{Error: "invalid renew request"})
				return
			}
			if req.NodeID != nodeID {
				writeJSON(w, http.StatusForbidden, types.APIError{Error: "node id does not match certificate identity"})
				return
			}
			out, err := c.jobs.Renew(leaseID, nodeID, leaseTTL, time.Now().UTC())
			if errors.Is(err, lease.ErrCancelled) {
				writeJSON(w, http.StatusConflict, out)
				return
			}
			if writeLeaseError(w, err) {
				return
			}
			writeJSON(w, http.StatusOK, out)

		case "complete":
			var req types.LeaseCompleteRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeJSON(w, http.StatusBadRequest, types.APIError{Error: "invalid completion request"})
				return
			}
			if req.NodeID != nodeID {
				writeJSON(w, http.StatusForbidden, types.APIError{Error: "node id does not match certificate identity"})
				return
			}
			job, err := c.jobs.Complete(
				leaseID, nodeID, nodeName,
				req.Success, req.Summary, req.DurationMS, req.ExecutionStartedAt,
				req.Executor, time.Now().UTC(),
			)
			if writeLeaseError(w, err) {
				return
			}
			writeJSON(w, http.StatusOK, job)

		default:
			writeJSON(w, http.StatusNotFound, types.APIError{Error: "unknown lease action"})
		}
	})

	return mux
}

const leaseTTL = 15 * time.Second

func (c *controller) authenticatedNode(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		writeJSON(w, http.StatusUnauthorized, types.APIError{Error: "client certificate required"})
		return "", "", false
	}
	peer := r.TLS.PeerCertificates[0]
	if !c.trust.trusted(peer.SerialNumber.String()) {
		writeJSON(w, http.StatusForbidden, types.APIError{Error: "node certificate is not trusted or has been revoked"})
		return "", "", false
	}
	nodeID := peer.Subject.CommonName
	nodeName := ""
	if len(peer.Subject.OrganizationalUnit) > 0 {
		nodeName = peer.Subject.OrganizationalUnit[0]
	}
	return nodeID, nodeName, true
}

func writeLeaseError(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, lease.ErrNotFound):
		writeJSON(w, http.StatusNotFound, types.APIError{Error: "lease not found"})
	case errors.Is(err, lease.ErrWrongNode):
		writeJSON(w, http.StatusForbidden, types.APIError{Error: "lease belongs to another node"})
	case errors.Is(err, lease.ErrExpired):
		writeJSON(w, http.StatusGone, types.APIError{Error: "lease expired"})
	case errors.Is(err, lease.ErrCancelled):
		writeJSON(w, http.StatusConflict, types.APIError{Error: "lease cancelled"})
	default:
		writeJSON(w, http.StatusConflict, types.APIError{Error: err.Error()})
	}
	return true
}

func containsCapability(values []string, want string) bool {
	want = strings.ToLower(strings.TrimSpace(want))
	for _, value := range values {
		if strings.ToLower(strings.TrimSpace(value)) == want {
			return true
		}
	}
	return false
}

func sortedJobs(jobs []types.ScheduledJob) []types.ScheduledJob {
	sort.Slice(jobs, func(i, j int) bool {
		return jobs[i].CreatedAt.After(jobs[j].CreatedAt)
	})
	return jobs
}

func main() {
	pairListen := flag.String("pair-listen", "0.0.0.0:8080", "LAN pairing/info listen address")
	secureListen := flag.String("secure-listen", "0.0.0.0:8443", "mTLS node API listen address")
	relayListen := flag.String("relay-listen", "0.0.0.0:9443", "mTLS reverse RPC relay listen address")
	advertiseHost := flag.String("advertise-host", "", "host/IP advertised to other NIBIA nodes")
	stateDir := flag.String("state-dir", identity.DefaultControllerDir(), "persistent controller state directory")
	noDiscovery := flag.Bool("no-discovery", false, "disable NIBIA LAN multicast discovery")
	includeVirtualDiscovery := flag.Bool("discovery-include-virtual", false, "include VPN/tunnel/virtual interfaces in discovery")
	flag.Parse()

	pairPort, err := portOf(*pairListen)
	if err != nil {
		log.Fatalf("invalid pair listen address: %v", err)
	}
	securePort, err := portOf(*secureListen)
	if err != nil {
		log.Fatalf("invalid secure listen address: %v", err)
	}
	host := strings.TrimSpace(*advertiseHost)
	if host == "" {
		host = discovery.BestAdvertiseHost(*includeVirtualDiscovery)
	}

	ident, err := identity.EnsureController(*stateDir)
	if err != nil {
		log.Fatalf("controller identity: %v", err)
	}
	trust, err := newTrustStore(filepath.Join(*stateDir, "trust.json"))
	if err != nil {
		log.Fatalf("trust store: %v", err)
	}
	jobsPath := filepath.Join(*stateDir, "jobs.json")
	initialJobs, err := loadJobs(jobsPath)
	if err != nil {
		log.Fatalf("job store: %v", err)
	}
	jobs := lease.NewStore(initialJobs, func(all []types.ScheduledJob) error {
		return saveJobs(jobsPath, all)
	})

	relayHub := rpcrelay.NewHub()
	relays := rpcrelay.NewManager(relayHub)

	c := &controller{
		reg:           newRegistry(),
		pairs:         newPairCodes(),
		trust:         trust,
		jobs:          jobs,
		relayHub:      relayHub,
		relays:        relays,
		ident:         ident,
		securePort:    securePort,
		advertiseHost: host,
		pairPort:      pairPort,
	}

	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(ident.CACertPEM) {
		log.Fatal("unable to load controller CA")
	}
	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS13,
		ClientAuth: tls.RequireAndVerifyClientCert,
		ClientCAs:  caPool,
	}

	adminServer := &http.Server{
		Addr:              *pairListen,
		Handler:           c.adminMux(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	secureServer := &http.Server{
		Addr:              *secureListen,
		Handler:           c.secureMux(),
		ReadHeaderTimeout: 5 * time.Second,
		TLSConfig:         tlsConfig,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if !*noDiscovery {
		ifaces, err := discovery.Interfaces(*includeVirtualDiscovery)
		if err != nil {
			log.Printf("discovery interface enumeration failed: %v", err)
		} else if len(ifaces) == 0 {
			log.Printf("no eligible discovery interfaces found; localhost access remains available")
		} else {
			for _, iface := range ifaces {
				log.Printf("discovery interface: %s %s private=%t preferred=%t",
					iface.Name, iface.Address, iface.Private, iface.Preferred)
			}
		}

		go func() {
			err := discovery.Serve(ctx, func() discovery.Message {
				info := c.publicInfo()
				hostname, _ := os.Hostname()
				return discovery.Message{
					Name:        "NIBIA Fabric",
					Hostname:    hostname,
					PairURL:     c.pairURL(),
					SecureURL:   info.SecureControllerURL,
					Version:     version.Version,
					Protocol:    version.ProtocolVersion,
					Fingerprint: ident.Fingerprint,
					Discovery:   discovery.DiscoveryVersion,
				}
			}, discovery.Options{IncludeVirtual: *includeVirtualDiscovery})
			if err != nil && ctx.Err() == nil {
				log.Printf("LAN discovery unavailable: %v", err)
			}
		}()
	}

	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				changed, err := c.jobs.ReapExpired(c.reg.list(), leaseTTL, now.UTC())
				if err != nil {
					log.Printf("lease reaper error: %v", err)
				} else if changed > 0 {
					log.Printf("lease reaper updated %d job(s)", changed)
				}
			}
		}
	}()

	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				c.relayHub.SweepAll(2 * time.Second)
			}
		}
	}()

	go func() {
		if err := c.serveRelayTLS(ctx, *relayListen, tlsConfig, ident.ServerCertFile, ident.ServerKeyFile); err != nil && ctx.Err() == nil {
			log.Fatalf("RPC relay server: %v", err)
		}
	}()

	go func() {
		log.Printf(
			"NIBIA controller v%s protocol=%s pairing=%s secure=%s relay=%s advertise=%s",
			version.Version, version.ProtocolVersion, *pairListen, *secureListen, *relayListen, host,
		)
		log.Printf("controller identity SHA-256: %s", ident.Fingerprint)
		if err := adminServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("pairing server: %v", err)
		}
	}()

	log.Fatal(secureServer.ListenAndServeTLS(ident.ServerCertFile, ident.ServerKeyFile))
}

func portOf(addr string) (int, error) {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(port)
}

func requestHostname(r *http.Request) string {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return strings.Trim(host, "[]")
}

func certPEMString(b []byte) string { return string(b) }
