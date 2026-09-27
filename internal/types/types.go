package types

import "time"

type ControllerInfo struct {
	Name                string `json:"name"`
	Version             string `json:"version"`
	ProtocolVersion     string `json:"protocol_version"`
	Fingerprint         string `json:"fingerprint,omitempty"`
	SecureControllerURL string `json:"secure_controller_url,omitempty"`
	Discovery           string `json:"discovery,omitempty"`
}

type PairCodeResponse struct {
	Code      string    `json:"code"`
	ExpiresAt time.Time `json:"expires_at"`
}

type PairClaimRequest struct {
	Code            string `json:"code"`
	NodeID          string `json:"node_id"`
	Name            string `json:"name"`
	ProtocolVersion string `json:"protocol_version"`
	CSRPEM          string `json:"csr_pem"`
}

type PairClaimResponse struct {
	CertificatePEM        string `json:"certificate_pem"`
	CACertificatePEM      string `json:"ca_certificate_pem"`
	SecureControllerURL   string `json:"secure_controller_url"`
	ControllerFingerprint string `json:"controller_fingerprint"`
	ProtocolVersion       string `json:"protocol_version"`
}

type TrustRecord struct {
	NodeID      string     `json:"node_id"`
	Name        string     `json:"name"`
	Serial      string     `json:"serial"`
	Fingerprint string     `json:"fingerprint"`
	IssuedAt    time.Time  `json:"issued_at"`
	RevokedAt   *time.Time `json:"revoked_at,omitempty"`
}

type RevokeRequest struct {
	Node string `json:"node"`
}

type NetworkInterfaceInventory struct {
	Name      string `json:"name"`
	Address   string `json:"address"`
	Private   bool   `json:"private"`
	Virtual   bool   `json:"virtual"`
	Preferred bool   `json:"preferred"`
}

type Node struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Hostname string `json:"hostname"`

	AgentVersion    string `json:"agent_version"`
	ProtocolVersion string `json:"protocol_version"`

	OS       string `json:"os"`
	Arch     string `json:"arch"`
	Platform string `json:"platform"`
	CPUModel string `json:"cpu_model"`

	CPUPhysical int     `json:"cpu_physical"`
	CPULogical  int     `json:"cpu_logical"`
	CPUUsedPct  float64 `json:"cpu_used_pct"`
	Load1       float64 `json:"load_1"`
	Load5       float64 `json:"load_5"`
	Load15      float64 `json:"load_15"`

	MemoryTotalMB       uint64  `json:"memory_total_mb"`
	MemoryAvailableMB   uint64  `json:"memory_available_mb"`
	MemoryUsedMB        uint64  `json:"memory_used_mb"`
	MemoryUsedPct       float64 `json:"memory_used_pct"`
	MemoryPressurePct   float64 `json:"memory_pressure_free_pct"`
	MemoryHeadroomPct   float64 `json:"memory_headroom_pct"`
	MemoryPressureLevel string  `json:"memory_pressure_level,omitempty"`
	CompressedMB        uint64  `json:"compressed_mb"`
	SwapTotalMB         uint64  `json:"swap_total_mb"`
	SwapUsedMB          uint64  `json:"swap_used_mb"`

	UptimeSeconds uint64   `json:"uptime_seconds"`
	Capabilities  []string `json:"capabilities"`
	ResourceState string   `json:"resource_state"`
	ResourceScore float64  `json:"resource_score"`

	PreferredAddress  string                      `json:"preferred_address,omitempty"`
	NetworkInterfaces []NetworkInterfaceInventory `json:"network_interfaces,omitempty"`

	Runtimes []RuntimeInventory `json:"runtimes,omitempty"`

	// Managed llama.cpp RPC worker telemetry is process-scoped and is kept
	// separate from system-wide CPU/RAM telemetry. This allows inference
	// reports to attribute worker consumption to NIBIA rather than to the
	// whole host.
	RPCWorkerManaged            bool      `json:"rpc_worker_managed,omitempty"`
	RPCWorkerRunning            bool      `json:"rpc_worker_running,omitempty"`
	RPCWorkerPID                int       `json:"rpc_worker_pid,omitempty"`
	RPCWorkerProcessCPUPercent  float64   `json:"rpc_worker_process_cpu_pct,omitempty"`
	RPCWorkerProcessRSSBytes    uint64    `json:"rpc_worker_process_rss_bytes,omitempty"`
	RPCWorkerPeakCPUPercent     float64   `json:"rpc_worker_peak_cpu_pct,omitempty"`
	RPCWorkerPeakRSSBytes       uint64    `json:"rpc_worker_peak_rss_bytes,omitempty"`
	RPCWorkerTelemetryStartedAt time.Time `json:"rpc_worker_telemetry_started_at,omitempty"`

	UpdatedAt time.Time `json:"updated_at"`
}

type NodeStatus struct {
	Node     Node      `json:"node"`
	LastSeen time.Time `json:"last_seen"`
	State    string    `json:"state"`
}

type RuntimeInventory struct {
	Name      string   `json:"name"`
	Version   string   `json:"version,omitempty"`
	Installed bool     `json:"installed"`
	Reachable bool     `json:"reachable"`
	Features  []string `json:"features,omitempty"`
}

type GenerativeNodeCapability struct {
	NodeID              string                      `json:"node_id"`
	NodeName            string                      `json:"node_name"`
	Hostname            string                      `json:"hostname,omitempty"`
	NodeState           string                      `json:"node_state"`
	ResourceState       string                      `json:"resource_state"`
	OS                  string                      `json:"os"`
	Arch                string                      `json:"arch"`
	Platform            string                      `json:"platform,omitempty"`
	CPUModel            string                      `json:"cpu_model,omitempty"`
	CPUUsedPct          float64                     `json:"cpu_used_pct"`
	MemoryUsedPct       float64                     `json:"memory_used_pct"`
	MemoryHeadroomPct   float64                     `json:"memory_headroom_pct"`
	MemoryPressureLevel string                      `json:"memory_pressure_level,omitempty"`
	PreferredAddress    string                      `json:"preferred_address,omitempty"`
	NetworkInterfaces   []NetworkInterfaceInventory `json:"network_interfaces,omitempty"`
	AgentVersion        string                      `json:"agent_version"`
	MemoryTotalMB       uint64                      `json:"memory_total_mb"`
	MemoryAvailableMB   uint64                      `json:"memory_available_mb"`
	PhysicalCores       int                         `json:"physical_cores"`
	LogicalCores        int                         `json:"logical_cores"`
	LlamaCPPVersion     string                      `json:"llama_cpp_version,omitempty"`
	LlamaCPPFeatures    []string                    `json:"llama_cpp_features,omitempty"`
	CoordinatorCapable  bool                        `json:"coordinator_capable"`
	WorkerCapable       bool                        `json:"worker_capable"`
	LastSeen            time.Time                   `json:"last_seen,omitempty"`
	TelemetryUpdatedAt  time.Time                   `json:"telemetry_updated_at,omitempty"`

	RPCWorkerManaged            bool      `json:"rpc_worker_managed,omitempty"`
	RPCWorkerRunning            bool      `json:"rpc_worker_running,omitempty"`
	RPCWorkerPID                int       `json:"rpc_worker_pid,omitempty"`
	RPCWorkerProcessCPUPercent  float64   `json:"rpc_worker_process_cpu_pct,omitempty"`
	RPCWorkerProcessRSSBytes    uint64    `json:"rpc_worker_process_rss_bytes,omitempty"`
	RPCWorkerPeakCPUPercent     float64   `json:"rpc_worker_peak_cpu_pct,omitempty"`
	RPCWorkerPeakRSSBytes       uint64    `json:"rpc_worker_peak_rss_bytes,omitempty"`
	RPCWorkerTelemetryStartedAt time.Time `json:"rpc_worker_telemetry_started_at,omitempty"`
}

type GenerativeFabricSummary struct {
	KnownNodes                  int                        `json:"known_nodes"`
	OnlineNodes                 int                        `json:"online_nodes"`
	LlamaCPPInstalledNodes      int                        `json:"llama_cpp_installed_nodes"`
	DistributedCoordinatorNodes int                        `json:"distributed_coordinator_nodes"`
	DistributedWorkerNodes      int                        `json:"distributed_worker_nodes"`
	AggregateTotalMemoryMB      uint64                     `json:"aggregate_total_memory_mb"`
	AggregateAvailableMemoryMB  uint64                     `json:"aggregate_available_memory_mb"`
	SharedAddressSpace          bool                       `json:"shared_address_space"`
	DistributedGenerativeReady  bool                       `json:"distributed_generative_ready"`
	Nodes                       []GenerativeNodeCapability `json:"nodes"`
}

type GenerativeRelayRequest struct {
	NodeID     string `json:"node_id"`
	NodeName   string `json:"node_name,omitempty"`
	LocalPort  int    `json:"local_port"`
	RemotePort int    `json:"remote_port"`
}

type GenerativeRelayStatus struct {
	NodeID              string    `json:"node_id"`
	NodeName            string    `json:"node_name,omitempty"`
	Managed             bool      `json:"managed"`
	Running             bool      `json:"running"`
	LocalEndpoint       string    `json:"local_endpoint"`
	RemoteEndpoint      string    `json:"remote_endpoint"`
	ReadyTunnelCount    int       `json:"ready_tunnel_count"`
	AcceptedConnections uint64    `json:"accepted_connections"`
	SuccessfulBridges   uint64    `json:"successful_bridges"`
	FailedBridges       uint64    `json:"failed_bridges"`
	ActiveBridges       int64     `json:"active_bridges"`
	BytesToWorker       uint64    `json:"bytes_to_worker"`
	BytesFromWorker     uint64    `json:"bytes_from_worker"`
	StartedAt           time.Time `json:"started_at,omitempty"`
	LastError           string    `json:"last_error,omitempty"`
	Transport           string    `json:"transport"`
	Exposure            string    `json:"exposure"`
}

type GenerativeRelayProbeResponse struct {
	NodeID        string `json:"node_id"`
	NodeName      string `json:"node_name,omitempty"`
	Success       bool   `json:"success"`
	LocalEndpoint string `json:"local_endpoint"`
	LatencyMS     int64  `json:"latency_ms"`
	Error         string `json:"error,omitempty"`
}

type APIError struct {
	Error             string `json:"error"`
	ControllerVersion string `json:"controller_version,omitempty"`
	ProtocolVersion   string `json:"protocol_version,omitempty"`
	ReceivedProtocol  string `json:"received_protocol,omitempty"`
	RecommendedAction string `json:"recommended_action,omitempty"`
}

type JobRequirements struct {
	MinPhysicalCores int      `json:"min_physical_cores,omitempty"`
	MinLogicalCores  int      `json:"min_logical_cores,omitempty"`
	MinMemoryMB      uint64   `json:"min_memory_mb,omitempty"`
	OS               string   `json:"os,omitempty"`
	Arch             string   `json:"arch,omitempty"`
	RequiredNodeID   string   `json:"required_node_id,omitempty"`
	ExcludedNodeIDs  []string `json:"excluded_node_ids,omitempty"`
	Capabilities     []string `json:"capabilities,omitempty"`
	AllowBusy        bool     `json:"allow_busy,omitempty"`
}

type WorkloadSpec struct {
	Type string `json:"type"`

	// Managed llama.cpp RPC worker lifecycle fields. NIBIA never accepts an
	// arbitrary executable or bind address here; workers are fixed to the
	// discovered ggml-rpc-server binary and loopback-only binding.
	RPCPort  int  `json:"rpc_port,omitempty"`
	RPCCache bool `json:"rpc_cache,omitempty"`
}

type JobRequest struct {
	Name         string          `json:"name"`
	Requirements JobRequirements `json:"requirements"`
	Workload     WorkloadSpec    `json:"workload"`
	MaxAttempts  int             `json:"max_attempts,omitempty"`
}

type CandidateScore struct {
	NodeID            string   `json:"node_id"`
	NodeName          string   `json:"node_name"`
	Eligible          bool     `json:"eligible"`
	RejectionReasons  []string `json:"rejection_reasons,omitempty"`
	CapacityScore     float64  `json:"capacity_score"`
	AvailabilityScore float64  `json:"availability_score"`
	AffinityScore     float64  `json:"affinity_score,omitempty"`
	PlacementScore    float64  `json:"placement_score"`
}

type PlacementHints struct {
	NodeAffinity map[string]float64 `json:"node_affinity,omitempty"`
}

type PlacementDecision struct {
	SelectedNodeID   string           `json:"selected_node_id,omitempty"`
	SelectedNodeName string           `json:"selected_node_name,omitempty"`
	Candidates       []CandidateScore `json:"candidates"`
}

type LeaseInfo struct {
	LeaseID         string     `json:"lease_id"`
	NodeID          string     `json:"node_id"`
	NodeName        string     `json:"node_name"`
	Attempt         int        `json:"attempt"`
	Status          string     `json:"status"`
	IssuedAt        time.Time  `json:"issued_at"`
	ExpiresAt       time.Time  `json:"expires_at"`
	AcknowledgedAt  *time.Time `json:"acknowledged_at,omitempty"`
	LastRenewedAt   *time.Time `json:"last_renewed_at,omitempty"`
	CancelRequested bool       `json:"cancel_requested,omitempty"`
}

type AttemptRecord struct {
	Attempt     int        `json:"attempt"`
	LeaseID     string     `json:"lease_id"`
	NodeID      string     `json:"node_id"`
	NodeName    string     `json:"node_name"`
	Status      string     `json:"status"`
	StartedAt   time.Time  `json:"started_at"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
	FailureKind string     `json:"failure_kind,omitempty"`
	Summary     string     `json:"summary,omitempty"`
}

type ExecutorResult struct {
	Kind string `json:"kind"`
	Data any    `json:"data,omitempty"`
}

type JobResult struct {
	NodeID             string          `json:"node_id"`
	NodeName           string          `json:"node_name"`
	Success            bool            `json:"success"`
	Summary            string          `json:"summary"`
	DurationMS         int64           `json:"duration_ms"`
	ExecutionStartedAt *time.Time      `json:"execution_started_at,omitempty"`
	CompletedAt        time.Time       `json:"completed_at"`
	Executor           *ExecutorResult `json:"executor,omitempty"`
}

type ScheduledJob struct {
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	State           string            `json:"state"`
	Requirements    JobRequirements   `json:"requirements"`
	Workload        WorkloadSpec      `json:"workload"`
	Placement       PlacementDecision `json:"placement"`
	Attempt         int               `json:"attempt"`
	MaxAttempts     int               `json:"max_attempts"`
	Attempts        []AttemptRecord   `json:"attempts,omitempty"`
	ActiveLease     *LeaseInfo        `json:"active_lease,omitempty"`
	Result          *JobResult        `json:"result,omitempty"`
	CreatedAt       time.Time         `json:"created_at"`
	UpdatedAt       time.Time         `json:"updated_at"`
	ExecutionStatus string            `json:"execution_status"`
}

type LeaseAssignment struct {
	LeaseID   string       `json:"lease_id"`
	JobID     string       `json:"job_id"`
	JobName   string       `json:"job_name"`
	NodeID    string       `json:"node_id"`
	NodeName  string       `json:"node_name"`
	Attempt   int          `json:"attempt"`
	ExpiresAt time.Time    `json:"expires_at"`
	Workload  WorkloadSpec `json:"workload"`
}

type LeaseAckRequest struct {
	NodeID string `json:"node_id"`
}

type LeaseRenewRequest struct {
	NodeID string `json:"node_id"`
}

type LeaseRenewResponse struct {
	ExpiresAt       time.Time `json:"expires_at"`
	CancelRequested bool      `json:"cancel_requested"`
}

type LeaseCompleteRequest struct {
	NodeID             string          `json:"node_id"`
	Success            bool            `json:"success"`
	Summary            string          `json:"summary"`
	DurationMS         int64           `json:"duration_ms"`
	ExecutionStartedAt *time.Time      `json:"execution_started_at,omitempty"`
	Executor           *ExecutorResult `json:"executor,omitempty"`
}

type CancelJobResponse struct {
	ID    string `json:"id"`
	State string `json:"state"`
}
