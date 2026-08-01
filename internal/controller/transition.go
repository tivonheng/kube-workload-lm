package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gitlab.glb.osl-nucleus.com/devops/kube-workload-lifecycle-manager/internal/lifecycle"
	"gitlab.glb.osl-nucleus.com/devops/kube-workload-lifecycle-manager/internal/policy"
	"gitlab.glb.osl-nucleus.com/devops/kube-workload-lifecycle-manager/internal/schedule"
	"gitlab.glb.osl-nucleus.com/devops/kube-workload-lifecycle-manager/internal/state"
	"gitlab.glb.osl-nucleus.com/devops/kube-workload-lifecycle-manager/internal/workload"
)

var ErrInvalidReconciler = errors.New("controller reconciler dependencies must be configured")

// HPAEvaluator is deliberately narrower than workload.HPADetector: transition
// orchestration may evaluate admission but does not own watch management.
type HPAEvaluator interface {
	Evaluate(context.Context, workload.Workload) workload.HPAEvaluation
}

// Reconciler performs one workload's durable state and scale transition.
type Reconciler struct {
	store state.StateStore
	scale workload.ScaleGateway
	hpa   HPAEvaluator
	clock lifecycle.Clock
}

func NewReconciler(store state.StateStore, scale workload.ScaleGateway, hpa HPAEvaluator, clock lifecycle.Clock) (*Reconciler, error) {
	if store == nil || scale == nil || hpa == nil || clock == nil {
		return nil, ErrInvalidReconciler
	}
	return &Reconciler{store: store, scale: scale, hpa: hpa, clock: clock}, nil
}

// Reconcile applies the selected policy to a current workload. The caller owns
// inventory reads and policy matching; this method owns all mutation ordering.
func (r *Reconciler) Reconcile(ctx context.Context, current workload.Workload, selected policy.Policy) (lifecycle.Decision, error) {
	if rejection := r.hpa.Evaluate(ctx, current).Rejection(); rejection != nil {
		return lifecycle.Decision{}, fmt.Errorf("reject %s: %w", current.Key(), rejection)
	}
	revision, err := lifecycle.CalculateRevision(current.Containers, selected.Lifecycle.Revision)
	if err != nil {
		return lifecycle.Decision{}, fmt.Errorf("calculate revision for %s: %w", current.Key(), err)
	}
	now := r.clock.Now().UTC()
	document, err := r.store.Load(ctx)
	if err != nil {
		return lifecycle.Decision{}, fmt.Errorf("load state for %s: %w", current.Key(), err)
	}

	currentState, identityChanged := reconcileIdentity(document.States[current.Key()], current, selected, revision, now)
	document.States[current.Key()] = currentState
	if identityChanged {
		if err := r.save(ctx, document, current.Key(), "persist revision baseline"); err != nil {
			return lifecycle.Decision{}, err
		}
	}

	live, err := r.scale.Read(ctx, current)
	if err != nil {
		return lifecycle.Decision{}, fmt.Errorf("read live scale for %s: %w", current.Key(), err)
	}
	windowName, scheduledDown, err := matchSchedule(now, selected.Schedule)
	if err != nil {
		return lifecycle.Decision{}, fmt.Errorf("evaluate schedule for %s: %w", current.Key(), err)
	}

	var windowInstanceID string
	if scheduledDown && windowName != "" {
		windowInstanceID, err = computeWindowInstanceID(now, selected.Schedule, windowName)
		if err != nil {
			return lifecycle.Decision{}, fmt.Errorf("compute window instance ID for %s: %w", current.Key(), err)
		}
	}
	currentState = reconcileWindowCycle(currentState, windowInstanceID, scheduledDown)

	// 确保在 downWindow 内始终有 WindowEntryRevision 记录（兼容升级和已缩容场景）
	if scheduledDown && windowInstanceID != "" && currentState.WindowEntryRevision == "" {
		currentState.WindowEntryRevision = revision.RevisionHash
		currentState.WindowInstanceID = windowInstanceID
	}
	document.States[current.Key()] = currentState

	// 检测窗口内 Revision 变化
	if scheduledDown && !currentState.ScaleDownSkipped && currentState.WindowEntryRevision != "" && currentState.WindowEntryRevision != revision.RevisionHash {
		currentState, err = r.handleRedeployDuringWindow(ctx, document, currentState, live, current.Key())
		if err != nil {
			return lifecycle.Decision{}, fmt.Errorf("handle redeploy during window for %s: %w", current.Key(), err)
		}
	}

	// Once a prior scheduled downscale is represented by a snapshot, a live
	// replica count above the target is accepted as an external override. Clear
	// the snapshot so the accepted replica count is not restored or overwritten.
	if scheduledDown && !currentState.ScaleDownSkipped && currentState.ReplicaSnapshot != nil && live.CurrentReplicas > selected.Replicas.ScheduledDown {
		currentState.ScaleDownSkipped = true
		currentState.ScaleDownSkipReason = lifecycle.ReasonScaleDownSkippedReplicaOverride
		currentState.ReplicaSnapshot = nil
		document.States[current.Key()] = currentState
		if err := r.save(ctx, document, current.Key(), "mark scale-down-skipped:replica-override"); err != nil {
			return lifecycle.Decision{}, err
		}
	}

	var snapshotReplicas *int32
	if currentState.ReplicaSnapshot != nil {
		value := currentState.ReplicaSnapshot.Replicas
		snapshotReplicas = &value
	}
	decision := lifecycle.Decide(lifecycle.DecisionInput{
		Now: now, FirstSeenAt: currentState.FirstSeenAt, MaxAge: selected.Lifecycle.MaxAge,
		CurrentReplicas: live.CurrentReplicas, SnapshotReplicas: snapshotReplicas,
		ScheduledDown: scheduledDown, ScheduledWindowName: windowName,
		ScheduledTarget: selected.Replicas.ScheduledDown, ExpiredTarget: selected.Replicas.Expired,
		ScaleDownSkipped: currentState.ScaleDownSkipped, ScaleDownSkipReason: currentState.ScaleDownSkipReason,
	})
	if err := r.applyDecision(ctx, live, decision, document, currentState, identityChanged, now); err != nil {
		return decision, err
	}
	return decision, nil
}

func reconcileIdentity(previous state.WorkloadState, current workload.Workload, selected policy.Policy, revision lifecycle.Revision, now time.Time) (state.WorkloadState, bool) {
	sameUID := previous.WorkloadUID != "" && previous.WorkloadUID == current.UID
	sameIdentity := sameUID && previous.TrackingSpecHash == revision.TrackingSpecHash && previous.RevisionHash == revision.RevisionHash
	firstSeen := now
	if sameIdentity {
		firstSeen = previous.FirstSeenAt
	}
	var snapshot *state.ReplicaSnapshot
	if sameUID && previous.ReplicaSnapshot != nil {
		copy := *previous.ReplicaSnapshot
		snapshot = &copy
	}
	next := state.WorkloadState{
		Kind: current.Kind, Namespace: current.Namespace, Name: current.Name, WorkloadUID: current.UID,
		LastPolicyName: selected.Name, TrackingSpecHash: revision.TrackingSpecHash, RevisionHash: revision.RevisionHash,
		Revision:    state.Revision{Source: lifecycle.SourceContainerImages, Containers: append([]workload.Container(nil), revision.Containers...)},
		FirstSeenAt: firstSeen, LastSeenAt: now, ReplicaSnapshot: snapshot,
	}
	// Preserve window/skip state when same UID (these are managed by reconcileWindowCycle)
	if sameUID {
		next.WindowEntryRevision = previous.WindowEntryRevision
		next.WindowInstanceID = previous.WindowInstanceID
		next.ScaleDownSkipped = previous.ScaleDownSkipped
		next.ScaleDownSkipReason = previous.ScaleDownSkipReason
	}
	return next, !sameIdentity
}

func matchSchedule(now time.Time, configured *policy.Schedule) (string, bool, error) {
	if configured == nil {
		return "", false, nil
	}
	return schedule.Match(now, configured.TimeZone, configured.DownWindows)
}

func computeWindowInstanceID(now time.Time, configured *policy.Schedule, windowName string) (string, error) {
	if configured == nil {
		return "", nil
	}
	return schedule.WindowInstanceID(now, configured.TimeZone, windowName, configured.DownWindows)
}

// reconcileWindowCycle 处理窗口周期边界。
// 如果当前不在 downWindow 或周期实例变化，清除 skip 相关状态。
func reconcileWindowCycle(current state.WorkloadState, windowInstanceID string, scheduledDown bool) state.WorkloadState {
	if !scheduledDown || windowInstanceID != current.WindowInstanceID {
		current.WindowEntryRevision = ""
		current.WindowInstanceID = ""
		current.ScaleDownSkipped = false
		current.ScaleDownSkipReason = ""
	}
	return current
}

func (r *Reconciler) applyDecision(ctx context.Context, live workload.Workload, decision lifecycle.Decision, document state.Document, current state.WorkloadState, identitySaved bool, now time.Time) error {
	key := live.Key()
	if decision.NeedsSnapshot {
		current.ReplicaSnapshot = &state.ReplicaSnapshot{
			Replicas: live.CurrentReplicas, CapturedAt: now, CapturedReason: decision.Reason,
		}
		document.States[key] = current
		if err := r.save(ctx, document, key, "persist replica snapshot"); err != nil {
			return err
		}
		if decision.ShouldScale {
			if err := r.updateScale(ctx, live, decision.DesiredReplicas, decision.Reason); err != nil {
				return err
			}
		}
		return nil
	}

	if decision.ClearSnapshotAfterScale {
		// A restore always performs an idempotent scale write, even when the live
		// value already matches, so a successful update strictly precedes clear.
		if err := r.updateScale(ctx, live, decision.DesiredReplicas, decision.Reason); err != nil {
			return err
		}
		current.ReplicaSnapshot = nil
		document.States[key] = current
		return r.save(ctx, document, key, "clear replica snapshot")
	}

	if decision.ShouldScale {
		if err := r.updateScale(ctx, live, decision.DesiredReplicas, decision.Reason); err != nil {
			return err
		}
	}
	// Persist lastSeenAt and policy metadata. A new identity was already saved;
	// avoid a redundant state write when no later transition changed anything.
	if identitySaved && !decision.ShouldScale {
		return nil
	}
	document.States[key] = current
	return r.save(ctx, document, key, "update workload state")
}

// handleRedeployDuringWindow 处理窗口内 Revision 变化。
// 如果存在快照则先恢复，然后标记 ScaleDownSkipped。
func (r *Reconciler) handleRedeployDuringWindow(ctx context.Context, document state.Document,
	current state.WorkloadState, live workload.Workload, key string) (state.WorkloadState, error) {

	if current.ReplicaSnapshot != nil {
		// 恢复到快照副本数
		if err := r.updateScale(ctx, live, current.ReplicaSnapshot.Replicas, lifecycle.ReasonScaleDownSkipped); err != nil {
			return current, err // 恢复失败，保留 snapshot，不标记 skip
		}
		current.ReplicaSnapshot = nil
	}

	current.ScaleDownSkipped = true
	current.ScaleDownSkipReason = lifecycle.ReasonScaleDownSkipped
	document.States[key] = current
	if err := r.save(ctx, document, key, "mark scale-down-skipped"); err != nil {
		return current, err
	}
	return current, nil
}

func (r *Reconciler) save(ctx context.Context, document state.Document, key, operation string) error {
	if err := r.store.Save(ctx, document); err != nil {
		return fmt.Errorf("%s for %s: %w", operation, key, err)
	}
	return nil
}

func (r *Reconciler) updateScale(ctx context.Context, current workload.Workload, replicas int32, reason string) error {
	if err := r.scale.Update(ctx, current, replicas); err != nil {
		return fmt.Errorf("update scale for %s to %d (%s): %w", current.Key(), replicas, reason, err)
	}
	return nil
}
