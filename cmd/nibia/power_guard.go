package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/nibia-ai/fabric/internal/executor"
	"github.com/nibia-ai/fabric/internal/powerguard"
	"github.com/nibia-ai/fabric/internal/types"
)

type executionPowerGuards struct {
	local      *powerguard.Guard
	controller string
	remote     []types.GenerativeNodeCapability
	once       sync.Once
}

func acquireExecutionPowerGuards(controller string, plan automaticGenerativePlanN, candidates []types.GenerativeNodeCapability) (*executionPowerGuards, int, []string) {
	guards := &executionPowerGuards{controller: controller}
	warnings := []string{}
	active := 0

	local, err := powerguard.Acquire("active NIBIA generative workload")
	if err != nil {
		warnings = append(warnings, fmt.Sprintf("Primary power guard unavailable: %v", err))
	} else {
		guards.local = local
		active++
	}

	byID := make(map[string]types.GenerativeNodeCapability, len(candidates))
	for _, node := range candidates {
		byID[node.NodeID] = node
	}
	for _, d := range plan.Selected {
		if d.Local {
			continue
		}
		node, ok := byID[d.NodeID]
		if !ok {
			warnings = append(warnings, fmt.Sprintf("%s power guard unavailable: selected node metadata not found", d.NodeName))
			continue
		}
		status, err := runPowerGuardAction(controller, node, executor.TaskPowerGuardAcquire)
		if err != nil || !status.Active {
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("%s power guard unavailable: %v", node.NodeName, err))
			} else {
				warnings = append(warnings, fmt.Sprintf("%s power guard did not enter active state", node.NodeName))
			}
			continue
		}
		guards.remote = append(guards.remote, node)
		active++
	}
	return guards, active, warnings
}

func (g *executionPowerGuards) Release() []string {
	if g == nil {
		return nil
	}
	warnings := []string{}
	g.once.Do(func() {
		for i := len(g.remote) - 1; i >= 0; i-- {
			node := g.remote[i]
			if _, err := runPowerGuardAction(g.controller, node, executor.TaskPowerGuardRelease); err != nil {
				if !errors.Is(err, errCleanupTargetUnavailable) {
					warnings = append(warnings, fmt.Sprintf("%s power guard release failed: %v", node.NodeName, err))
				}
			}
		}
		if g.local != nil {
			if err := g.local.Release(); err != nil {
				warnings = append(warnings, fmt.Sprintf("Primary power guard release failed: %v", err))
			}
		}
	})
	return warnings
}

func runPowerGuardAction(controller string, found types.GenerativeNodeCapability, workload string) (executor.PowerGuardStatus, error) {
	action := "status"
	switch workload {
	case executor.TaskPowerGuardAcquire:
		action = "acquire"
	case executor.TaskPowerGuardRelease:
		action = "release"
	}
	req := types.JobRequest{
		Name: "power-guard-" + action + "-" + strings.ToLower(strings.ReplaceAll(found.NodeName, " ", "-")),
		Requirements: types.JobRequirements{
			RequiredNodeID: found.NodeID,
			Capabilities:   []string{"executor-v1", "task-power-guard"},
			AllowBusy:      true,
		},
		Workload:    types.WorkloadSpec{Type: workload},
		MaxAttempts: 1,
	}
	var job types.ScheduledJob
	if err := apiJSON(http.MethodPost, controller, "/v1/jobs", req, &job); err != nil {
		return executor.PowerGuardStatus{}, err
	}
	waitFor := 30 * time.Second
	if workload == executor.TaskPowerGuardRelease {
		waitFor = 15 * time.Second
	}
	var err error
	if workload == executor.TaskPowerGuardRelease {
		job, err = waitScheduledCleanupJob(controller, job, waitFor, found.NodeID)
	} else {
		job, err = waitScheduledJob(controller, job, waitFor)
	}
	if err != nil && workload == executor.TaskPowerGuardRelease {
		cancelScheduledJobBestEffort(controller, job.ID)
	}
	if err != nil {
		return executor.PowerGuardStatus{}, err
	}
	if !strings.EqualFold(job.State, "SUCCEEDED") || job.Result == nil || !job.Result.Success {
		return executor.PowerGuardStatus{}, fmt.Errorf("power guard %s ended in %s", action, job.State)
	}
	status, ok := decodePowerGuardStatus(job)
	if !ok {
		return executor.PowerGuardStatus{}, fmt.Errorf("power guard %s returned no typed status", action)
	}
	return status, nil
}

func decodePowerGuardStatus(job types.ScheduledJob) (executor.PowerGuardStatus, bool) {
	if job.Result == nil || job.Result.Executor == nil || job.Result.Executor.Data == nil {
		return executor.PowerGuardStatus{}, false
	}
	b, err := json.Marshal(job.Result.Executor.Data)
	if err != nil {
		return executor.PowerGuardStatus{}, false
	}
	var out executor.PowerGuardStatus
	if err := json.Unmarshal(b, &out); err != nil {
		return executor.PowerGuardStatus{}, false
	}
	return out, true
}
