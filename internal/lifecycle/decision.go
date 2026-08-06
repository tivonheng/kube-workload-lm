package lifecycle

import "time"

const (
	ReasonRevisionExpired                 = "revision-lifecycle-expired"
	ReasonRevisionExpiredDeleted          = "revision-lifecycle-expired:deleted"
	ReasonScaleDownSkipped                = "scale-down-skipped:redeploy"
	ReasonScaleDownSkippedReplicaOverride = "scale-down-skipped:replica-override"
	ReasonScaleDownWindow                 = "scale-down-window"
	ReasonRestoreReplicas                 = "restore-previous-replicas"
	ReasonActiveWindow                    = "active-window"
)

type DecisionInput struct {
	Now                 time.Time
	FirstSeenAt         time.Time
	MaxAge              time.Duration
	CurrentReplicas     int32
	SnapshotReplicas    *int32
	ScheduledDown       bool
	ScheduledWindowName string
	ScheduledTarget     int32
	ExpiredTarget       int32
	ScaleDownSkipped    bool
	ScaleDownSkipReason string
	ExpiredAction       string
}

type Decision struct {
	DesiredReplicas         int32
	Reason                  string
	NeedsSnapshot           bool
	ShouldScale             bool
	ClearSnapshotAfterScale bool
	DeleteWorkload          bool
}

func Decide(input DecisionInput) Decision {
	if !input.Now.Before(input.FirstSeenAt.Add(input.MaxAge)) {
		if input.ExpiredAction == "delete" {
			return Decision{input.CurrentReplicas, ReasonRevisionExpiredDeleted, false, false, false, true}
		}
		return downDecision(input, input.ExpiredTarget, ReasonRevisionExpired)
	}
	if input.ScaleDownSkipped && input.ScheduledDown {
		reason := input.ScaleDownSkipReason
		if reason == "" {
			reason = ReasonScaleDownSkipped
		}
		return Decision{input.CurrentReplicas, reason, false, false, false, false}
	}
	if input.ScheduledDown {
		reason := ReasonScaleDownWindow
		if input.ScheduledWindowName != "" {
			reason += ":" + input.ScheduledWindowName
		}
		return downDecision(input, input.ScheduledTarget, reason)
	}
	if input.SnapshotReplicas != nil {
		desired := *input.SnapshotReplicas
		return Decision{desired, ReasonRestoreReplicas, false, desired != input.CurrentReplicas, true, false}
	}
	return Decision{input.CurrentReplicas, ReasonActiveWindow, false, false, false, false}
}

func downDecision(input DecisionInput, target int32, reason string) Decision {
	baseline := input.CurrentReplicas
	needsSnapshot := input.SnapshotReplicas == nil
	if input.SnapshotReplicas != nil {
		baseline = *input.SnapshotReplicas
	}
	desired := min(target, baseline)
	return Decision{desired, reason, needsSnapshot, desired != input.CurrentReplicas, false, false}
}
