package lifecycle

import "time"

const (
	ReasonRevisionExpired = "revision-lifecycle-expired"
	ReasonScaleDownWindow = "scale-down-window"
	ReasonRestoreReplicas = "restore-previous-replicas"
	ReasonActiveWindow    = "active-window"
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
}

type Decision struct {
	DesiredReplicas         int32
	Reason                  string
	NeedsSnapshot           bool
	ShouldScale             bool
	ClearSnapshotAfterScale bool
}

func Decide(input DecisionInput) Decision {
	if !input.Now.Before(input.FirstSeenAt.Add(input.MaxAge)) {
		return downDecision(input, input.ExpiredTarget, ReasonRevisionExpired)
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
		return Decision{desired, ReasonRestoreReplicas, false, desired != input.CurrentReplicas, true}
	}
	return Decision{input.CurrentReplicas, ReasonActiveWindow, false, false, false}
}

func downDecision(input DecisionInput, target int32, reason string) Decision {
	baseline := input.CurrentReplicas
	needsSnapshot := input.SnapshotReplicas == nil
	if input.SnapshotReplicas != nil {
		baseline = *input.SnapshotReplicas
	}
	desired := min(target, baseline)
	return Decision{desired, reason, needsSnapshot, desired != input.CurrentReplicas, false}
}
