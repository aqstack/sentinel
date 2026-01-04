// Package k8s provides Kubernetes client helpers for node and pod operations.
package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
)

// MigrationReason describes why a migration was triggered.
type MigrationReason string

const (
	ReasonThermalCritical   MigrationReason = "thermal_critical"
	ReasonMemoryPressure    MigrationReason = "memory_pressure"
	ReasonPredictedFailure  MigrationReason = "predicted_failure"
	ReasonManualRequest     MigrationReason = "manual_request"
	ReasonPartitionRecovery MigrationReason = "partition_recovery"
)

// MigrationRequest represents a request to migrate workloads off a node.
type MigrationRequest struct {
	NodeName    string          `json:"node_name"`
	Reason      MigrationReason `json:"reason"`
	Priority    int             `json:"priority"` // Higher = more urgent
	GracePeriod int64           `json:"grace_period_seconds"`
	DryRun      bool            `json:"dry_run"`
	RequestedAt time.Time       `json:"requested_at"`
	RequestedBy string          `json:"requested_by"` // predictor, consensus, manual
}

// MigrationResult contains the outcome of a migration operation.
type MigrationResult struct {
	Request     MigrationRequest `json:"request"`
	StartedAt   time.Time        `json:"started_at"`
	CompletedAt time.Time        `json:"completed_at"`
	Success     bool             `json:"success"`
	PodsEvicted int              `json:"pods_evicted"`
	PodsFailed  int              `json:"pods_failed"`
	DrainResult *DrainResult     `json:"drain_result,omitempty"`
	Error       string           `json:"error,omitempty"`
}

// Migrator handles preemptive workload migration.
type Migrator struct {
	client   *Client
	nodeName string

	mu             sync.Mutex
	inProgress     bool
	currentRequest *MigrationRequest
	history        []MigrationResult
	maxHistory     int

	// Callbacks
	onMigrationStart    func(MigrationRequest)
	onMigrationComplete func(MigrationResult)
}

// MigratorOption configures the Migrator.
type MigratorOption func(*Migrator)

// WithMigrationStartCallback sets a callback for migration start.
func WithMigrationStartCallback(fn func(MigrationRequest)) MigratorOption {
	return func(m *Migrator) {
		m.onMigrationStart = fn
	}
}

// WithMigrationCompleteCallback sets a callback for migration completion.
func WithMigrationCompleteCallback(fn func(MigrationResult)) MigratorOption {
	return func(m *Migrator) {
		m.onMigrationComplete = fn
	}
}

// NewMigrator creates a new Migrator.
func NewMigrator(client *Client, opts ...MigratorOption) *Migrator {
	m := &Migrator{
		client:     client,
		nodeName:   client.nodeName,
		maxHistory: 100,
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// RequestMigration initiates a migration if not already in progress.
func (m *Migrator) RequestMigration(ctx context.Context, req MigrationRequest) (*MigrationResult, error) {
	m.mu.Lock()
	if m.inProgress {
		m.mu.Unlock()
		return nil, fmt.Errorf("migration already in progress")
	}
	m.inProgress = true
	m.currentRequest = &req
	m.mu.Unlock()

	defer func() {
		m.mu.Lock()
		m.inProgress = false
		m.currentRequest = nil
		m.mu.Unlock()
	}()

	result := MigrationResult{
		Request:   req,
		StartedAt: time.Now(),
	}

	if m.onMigrationStart != nil {
		m.onMigrationStart(req)
	}

	// Perform the drain
	drainResult, err := m.client.DrainNode(ctx, req.GracePeriod)
	result.DrainResult = drainResult
	result.CompletedAt = time.Now()

	if err != nil {
		result.Error = err.Error()
		result.Success = false
	} else {
		result.Success = drainResult.Success
		result.PodsEvicted = len(drainResult.EvictedPods)
		result.PodsFailed = len(drainResult.FailedEvictions)
	}

	// Store in history
	m.mu.Lock()
	m.history = append(m.history, result)
	if len(m.history) > m.maxHistory {
		m.history = m.history[1:]
	}
	m.mu.Unlock()

	if m.onMigrationComplete != nil {
		m.onMigrationComplete(result)
	}

	return &result, nil
}

// IsInProgress returns true if a migration is currently in progress.
func (m *Migrator) IsInProgress() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.inProgress
}

// GetCurrentRequest returns the current migration request, if any.
func (m *Migrator) GetCurrentRequest() *MigrationRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.currentRequest == nil {
		return nil
	}
	req := *m.currentRequest
	return &req
}

// GetHistory returns migration history.
func (m *Migrator) GetHistory() []MigrationResult {
	m.mu.Lock()
	defer m.mu.Unlock()
	history := make([]MigrationResult, len(m.history))
	copy(history, m.history)
	return history
}

// PartitionReconciler handles reconciliation after partition heals.
type PartitionReconciler struct {
	client   *Client
	nodeName string
}

// NewPartitionReconciler creates a new PartitionReconciler.
func NewPartitionReconciler(client *Client) *PartitionReconciler {
	return &PartitionReconciler{
		client:   client,
		nodeName: client.nodeName,
	}
}

// ReconciliationAction represents an action taken during partition.
type ReconciliationAction struct {
	Type      string          `json:"type"` // cordon, evict, taint
	Target    string          `json:"target"`
	Timestamp time.Time       `json:"timestamp"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

// ReconciliationResult contains the outcome of reconciliation.
type ReconciliationResult struct {
	NodeName    string                 `json:"node_name"`
	StartedAt   time.Time              `json:"started_at"`
	CompletedAt time.Time              `json:"completed_at"`
	Actions     []ReconciliationAction `json:"actions_reconciled"`
	Conflicts   []string               `json:"conflicts,omitempty"`
	Success     bool                   `json:"success"`
}

// Reconcile processes actions taken during partition and reconciles with control plane.
func (r *PartitionReconciler) Reconcile(ctx context.Context, actions []ReconciliationAction) (*ReconciliationResult, error) {
	result := &ReconciliationResult{
		NodeName:  r.nodeName,
		StartedAt: time.Now(),
		Actions:   actions,
	}

	// Check current node state from control plane
	node, err := r.client.GetNode(ctx)
	if err != nil {
		result.Conflicts = append(result.Conflicts, fmt.Sprintf("failed to get node state: %v", err))
		result.CompletedAt = time.Now()
		return result, err
	}

	// Reconcile each action
	for _, action := range actions {
		switch action.Type {
		case "cordon":
			// If we cordoned during partition, verify it's still cordoned or uncordon if recovered
			if !node.Spec.Unschedulable {
				result.Conflicts = append(result.Conflicts,
					"node was cordoned during partition but is now schedulable")
			}

		case "taint":
			// Check if our taint is still present
			var taintPayload struct {
				Key   string `json:"key"`
				Value string `json:"value"`
			}
			if err := json.Unmarshal(action.Payload, &taintPayload); err == nil {
				found := false
				for _, t := range node.Spec.Taints {
					if t.Key == taintPayload.Key {
						found = true
						break
					}
				}
				if !found {
					result.Conflicts = append(result.Conflicts,
						fmt.Sprintf("taint %s was applied during partition but is now missing", taintPayload.Key))
				}
			}

		case "evict":
			// Evictions are final, just log them
			// The scheduler will have rescheduled pods elsewhere
		}
	}

	result.CompletedAt = time.Now()
	result.Success = len(result.Conflicts) == 0

	return result, nil
}

// CheckAndRecoverNode checks if node should be uncordoned after partition recovery.
func (r *PartitionReconciler) CheckAndRecoverNode(ctx context.Context, wasCordonedDuringPartition bool) error {
	if !wasCordonedDuringPartition {
		return nil
	}

	// Get current node state
	node, err := r.client.GetNode(ctx)
	if err != nil {
		return err
	}

	// Check if node is healthy enough to uncordon
	for _, cond := range node.Status.Conditions {
		if cond.Type == corev1.NodeReady && cond.Status != corev1.ConditionTrue {
			// Node is not ready, keep cordoned
			return fmt.Errorf("node not ready, keeping cordoned")
		}
		if cond.Type == corev1.NodeMemoryPressure && cond.Status == corev1.ConditionTrue {
			return fmt.Errorf("memory pressure, keeping cordoned")
		}
		if cond.Type == corev1.NodeDiskPressure && cond.Status == corev1.ConditionTrue {
			return fmt.Errorf("disk pressure, keeping cordoned")
		}
	}

	// Node is healthy, uncordon
	return r.client.UncordonNode(ctx)
}
