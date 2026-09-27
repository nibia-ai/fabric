package lease

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/nibia-ai/fabric/internal/scheduler"
	"github.com/nibia-ai/fabric/internal/types"
)

const (
	JobPlaced        = "PLACED"
	JobLeased        = "LEASED"
	JobRunning       = "RUNNING"
	JobSucceeded     = "SUCCEEDED"
	JobFailed        = "FAILED"
	JobCancelled     = "CANCELLED"
	JobUnschedulable = "UNSCHEDULABLE"

	LeaseOffered   = "OFFERED"
	LeaseRunning   = "RUNNING"
	LeaseCompleted = "COMPLETED"
	LeaseExpired   = "EXPIRED"
	LeaseCancelled = "CANCELLED"
)

var (
	ErrNotFound     = errors.New("not found")
	ErrWrongNode    = errors.New("lease belongs to another node")
	ErrExpired      = errors.New("lease expired")
	ErrCancelled    = errors.New("lease cancelled")
	ErrInvalidState = errors.New("invalid lease/job state")
)

type Store struct {
	mu   sync.RWMutex
	jobs []types.ScheduledJob
	save func([]types.ScheduledJob) error
}

func NewStore(initial []types.ScheduledJob, save func([]types.ScheduledJob) error) *Store {
	cp := make([]types.ScheduledJob, len(initial))
	copy(cp, initial)
	return &Store{jobs: cp, save: save}
}

func (s *Store) Add(job types.ScheduledJob) error {
	return s.AddMany([]types.ScheduledJob{job})
}

func (s *Store) AddMany(jobs []types.ScheduledJob) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(jobs) == 0 {
		return nil
	}
	s.jobs = append(s.jobs, jobs...)
	if err := s.persistLocked(); err != nil {
		s.jobs = s.jobs[:len(s.jobs)-len(jobs)]
		return err
	}
	return nil
}

func (s *Store) List() []types.ScheduledJob {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]types.ScheduledJob, len(s.jobs))
	copy(out, s.jobs)
	return out
}

func (s *Store) Get(id string) (types.ScheduledJob, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, job := range s.jobs {
		if strings.EqualFold(job.ID, id) {
			return job, true
		}
	}
	return types.ScheduledJob{}, false
}

func (s *Store) ClaimNext(nodeID string, ttl time.Duration, now time.Time) (types.LeaseAssignment, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.jobs {
		job := &s.jobs[i]
		if job.State != JobPlaced {
			continue
		}
		if job.Placement.SelectedNodeID != nodeID {
			continue
		}
		if strings.TrimSpace(job.Workload.Type) == "" {
			continue
		}
		if job.Attempt >= job.MaxAttempts {
			continue
		}

		leaseID, err := randomID("lease")
		if err != nil {
			return types.LeaseAssignment{}, false, err
		}
		job.Attempt++
		job.State = JobLeased
		job.ExecutionStatus = "WAITING_ACK"
		job.UpdatedAt = now.UTC()
		job.ActiveLease = &types.LeaseInfo{
			LeaseID:   leaseID,
			NodeID:    nodeID,
			NodeName:  job.Placement.SelectedNodeName,
			Attempt:   job.Attempt,
			Status:    LeaseOffered,
			IssuedAt:  now.UTC(),
			ExpiresAt: now.UTC().Add(ttl),
		}
		job.Attempts = append(job.Attempts, types.AttemptRecord{
			Attempt:   job.Attempt,
			LeaseID:   leaseID,
			NodeID:    nodeID,
			NodeName:  job.Placement.SelectedNodeName,
			Status:    LeaseOffered,
			StartedAt: now.UTC(),
		})
		if err := s.persistLocked(); err != nil {
			return types.LeaseAssignment{}, false, err
		}

		return assignment(*job), true, nil
	}
	return types.LeaseAssignment{}, false, nil
}

func (s *Store) Ack(leaseID, nodeID string, ttl time.Duration, now time.Time) (types.LeaseRenewResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	job, err := s.findLeaseLocked(leaseID)
	if err != nil {
		return types.LeaseRenewResponse{}, err
	}
	if err := validateLease(job, leaseID, nodeID, now); err != nil {
		return types.LeaseRenewResponse{}, err
	}
	if job.State == JobCancelled || job.ActiveLease.CancelRequested {
		return types.LeaseRenewResponse{ExpiresAt: job.ActiveLease.ExpiresAt, CancelRequested: true}, ErrCancelled
	}
	if job.State != JobLeased || job.ActiveLease.Status != LeaseOffered {
		return types.LeaseRenewResponse{}, ErrInvalidState
	}

	t := now.UTC()
	job.State = JobRunning
	job.ExecutionStatus = "RUNNING"
	job.UpdatedAt = t
	job.ActiveLease.Status = LeaseRunning
	updateAttempt(job, job.ActiveLease.LeaseID, LeaseRunning, "", "", nil)
	job.ActiveLease.AcknowledgedAt = &t
	job.ActiveLease.LastRenewedAt = &t
	job.ActiveLease.ExpiresAt = t.Add(ttl)

	if err := s.persistLocked(); err != nil {
		return types.LeaseRenewResponse{}, err
	}
	return types.LeaseRenewResponse{ExpiresAt: job.ActiveLease.ExpiresAt}, nil
}

func (s *Store) Renew(leaseID, nodeID string, ttl time.Duration, now time.Time) (types.LeaseRenewResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	job, err := s.findLeaseLocked(leaseID)
	if err != nil {
		return types.LeaseRenewResponse{}, err
	}
	if job.ActiveLease == nil || job.ActiveLease.NodeID != nodeID {
		return types.LeaseRenewResponse{}, ErrWrongNode
	}
	if job.State == JobCancelled || job.ActiveLease.CancelRequested {
		return types.LeaseRenewResponse{ExpiresAt: job.ActiveLease.ExpiresAt, CancelRequested: true}, ErrCancelled
	}
	if now.After(job.ActiveLease.ExpiresAt) {
		return types.LeaseRenewResponse{}, ErrExpired
	}
	if job.State != JobRunning || job.ActiveLease.Status != LeaseRunning {
		return types.LeaseRenewResponse{}, ErrInvalidState
	}

	t := now.UTC()
	job.UpdatedAt = t
	job.ActiveLease.LastRenewedAt = &t
	job.ActiveLease.ExpiresAt = t.Add(ttl)
	if err := s.persistLocked(); err != nil {
		return types.LeaseRenewResponse{}, err
	}
	return types.LeaseRenewResponse{ExpiresAt: job.ActiveLease.ExpiresAt}, nil
}

func (s *Store) Complete(
	leaseID, nodeID, nodeName string,
	success bool,
	summary string,
	durationMS int64,
	executionStartedAt *time.Time,
	executor *types.ExecutorResult,
	now time.Time,
) (types.ScheduledJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	job, err := s.findLeaseLocked(leaseID)
	if err != nil {
		return types.ScheduledJob{}, err
	}
	if job.ActiveLease == nil || job.ActiveLease.NodeID != nodeID {
		return types.ScheduledJob{}, ErrWrongNode
	}
	if job.State == JobCancelled || job.ActiveLease.CancelRequested {
		return types.ScheduledJob{}, ErrCancelled
	}
	if now.After(job.ActiveLease.ExpiresAt) {
		return types.ScheduledJob{}, ErrExpired
	}
	if job.State != JobRunning || job.ActiveLease.Status != LeaseRunning {
		return types.ScheduledJob{}, ErrInvalidState
	}

	t := now.UTC()
	job.ActiveLease.Status = LeaseCompleted
	status := LeaseCompleted
	if !success {
		status = "FAILED"
	}
	updateAttempt(job, job.ActiveLease.LeaseID, status, "", trimSummary(summary), &t)
	job.UpdatedAt = t
	var actualStart *time.Time
	if executionStartedAt != nil && !executionStartedAt.IsZero() {
		start := executionStartedAt.UTC()
		actualStart = &start
	}
	job.Result = &types.JobResult{
		NodeID:             nodeID,
		NodeName:           nodeName,
		Success:            success,
		Summary:            trimSummary(summary),
		DurationMS:         durationMS,
		ExecutionStartedAt: actualStart,
		CompletedAt:        t,
		Executor:           executor,
	}
	if success {
		job.State = JobSucceeded
		job.ExecutionStatus = "SUCCEEDED"
	} else {
		job.State = JobFailed
		job.ExecutionStatus = "FAILED"
	}

	if err := s.persistLocked(); err != nil {
		return types.ScheduledJob{}, err
	}
	return *job, nil
}

func (s *Store) Cancel(jobID string, now time.Time) (types.ScheduledJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.jobs {
		job := &s.jobs[i]
		if !strings.EqualFold(job.ID, jobID) {
			continue
		}
		switch job.State {
		case JobSucceeded, JobFailed:
			return types.ScheduledJob{}, fmt.Errorf("%w: terminal job cannot be cancelled", ErrInvalidState)
		case JobCancelled:
			return *job, nil
		}

		job.State = JobCancelled
		job.ExecutionStatus = "CANCELLED"
		job.UpdatedAt = now.UTC()
		if job.ActiveLease != nil {
			job.ActiveLease.CancelRequested = true
			job.ActiveLease.Status = LeaseCancelled
			finished := now.UTC()
			updateAttempt(job, job.ActiveLease.LeaseID, LeaseCancelled, "cancelled", "cancelled by controller", &finished)
		}
		if err := s.persistLocked(); err != nil {
			return types.ScheduledJob{}, err
		}
		return *job, nil
	}
	return types.ScheduledJob{}, ErrNotFound
}

// ReapExpired expires dead leases, retries them on a new placement, and also
// re-evaluates retry-waiting unschedulable jobs when the fabric changes.
func (s *Store) ReapExpired(nodes []types.NodeStatus, ttl time.Duration, now time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	changed := 0
	for i := range s.jobs {
		job := &s.jobs[i]

		if job.State == JobLeased || job.State == JobRunning {
			if job.ActiveLease == nil || !now.After(job.ActiveLease.ExpiresAt) {
				continue
			}

			failedNodeID := job.ActiveLease.NodeID
			job.ActiveLease.Status = LeaseExpired
			finished := now.UTC()
			updateAttempt(job, job.ActiveLease.LeaseID, LeaseExpired, "lease_expired", "worker stopped renewing lease", &finished)
			job.UpdatedAt = now.UTC()

			if job.Attempt >= job.MaxAttempts {
				job.State = JobFailed
				job.ExecutionStatus = "LEASE_EXPIRED_MAX_ATTEMPTS"
				changed++
				continue
			}

			placementNodes := excludeNode(nodes, failedNodeID)

			decision := scheduler.Place(placementNodes, job.Requirements)
			job.Placement = decision
			job.ActiveLease = nil
			if decision.SelectedNodeID == "" {
				job.State = JobUnschedulable
				job.ExecutionStatus = "RETRY_WAITING_FOR_NODE"
			} else {
				job.State = JobPlaced
				job.ExecutionStatus = "RETRY_PLACED"
			}
			changed++
			continue
		}

		if job.State == JobUnschedulable &&
			job.Attempt > 0 &&
			job.Attempt < job.MaxAttempts &&
			job.ExecutionStatus == "RETRY_WAITING_FOR_NODE" {

			decision := scheduler.Place(nodes, job.Requirements)
			if decision.SelectedNodeID != "" {
				job.Placement = decision
				job.State = JobPlaced
				job.ExecutionStatus = "RETRY_PLACED"
				job.UpdatedAt = now.UTC()
				changed++
			}
		}
	}

	if changed > 0 {
		if err := s.persistLocked(); err != nil {
			return changed, err
		}
	}
	return changed, nil
}

func updateAttempt(job *types.ScheduledJob, leaseID, status, failureKind, summary string, finishedAt *time.Time) {
	for i := range job.Attempts {
		if job.Attempts[i].LeaseID == leaseID {
			job.Attempts[i].Status = status
			if failureKind != "" {
				job.Attempts[i].FailureKind = failureKind
			}
			if summary != "" {
				job.Attempts[i].Summary = summary
			}
			if finishedAt != nil {
				t := finishedAt.UTC()
				job.Attempts[i].FinishedAt = &t
			}
			return
		}
	}
}

func assignment(job types.ScheduledJob) types.LeaseAssignment {
	l := job.ActiveLease
	return types.LeaseAssignment{
		LeaseID:   l.LeaseID,
		JobID:     job.ID,
		JobName:   job.Name,
		NodeID:    l.NodeID,
		NodeName:  l.NodeName,
		Attempt:   l.Attempt,
		ExpiresAt: l.ExpiresAt,
		Workload:  job.Workload,
	}
}

func validateLease(job *types.ScheduledJob, leaseID, nodeID string, now time.Time) error {
	if job.ActiveLease == nil {
		return ErrNotFound
	}
	if job.ActiveLease.LeaseID != leaseID {
		return ErrNotFound
	}
	if job.ActiveLease.NodeID != nodeID {
		return ErrWrongNode
	}
	if now.After(job.ActiveLease.ExpiresAt) {
		return ErrExpired
	}
	return nil
}

func (s *Store) findLeaseLocked(leaseID string) (*types.ScheduledJob, error) {
	for i := range s.jobs {
		if s.jobs[i].ActiveLease != nil && s.jobs[i].ActiveLease.LeaseID == leaseID {
			return &s.jobs[i], nil
		}
	}
	return nil, ErrNotFound
}

func (s *Store) persistLocked() error {
	if s.save == nil {
		return nil
	}
	cp := make([]types.ScheduledJob, len(s.jobs))
	copy(cp, s.jobs)
	return s.save(cp)
}

func appendUniqueNodeID(values []string, nodeID string) []string {
	nodeID = strings.TrimSpace(nodeID)
	if nodeID == "" {
		return values
	}
	for _, existing := range values {
		if strings.EqualFold(strings.TrimSpace(existing), nodeID) {
			return values
		}
	}
	return append(values, nodeID)
}

func excludeNode(nodes []types.NodeStatus, nodeID string) []types.NodeStatus {
	out := make([]types.NodeStatus, len(nodes))
	copy(out, nodes)
	for i := range out {
		if out[i].Node.ID == nodeID {
			out[i].State = "offline"
		}
	}
	return out
}

func trimSummary(v string) string {
	v = strings.TrimSpace(v)
	const max = 2048
	if len(v) > max {
		return v[:max]
	}
	return v
}

func randomID(prefix string) (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return prefix + "-" + hex.EncodeToString(b), nil
}
