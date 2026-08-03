package lifecycle

import (
	"testing"
	"time"
)

func TestDecideReplicaStateMachine(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	snapshot := int32(3)
	tests := []struct {
		name  string
		input DecisionInput
		want  Decision
	}{
		{"expired-precedes-window", DecisionInput{Now: now, FirstSeenAt: now.Add(-73 * time.Hour), MaxAge: 72 * time.Hour, CurrentReplicas: 3, ScheduledDown: true, ScheduledTarget: 1, ExpiredTarget: 0}, Decision{0, ReasonRevisionExpired, true, true, false, false}},
		{"scheduled-captures", DecisionInput{Now: now, FirstSeenAt: now, MaxAge: 72 * time.Hour, CurrentReplicas: 3, ScheduledDown: true, ScheduledWindowName: "night", ScheduledTarget: 0}, Decision{0, ReasonScaleDownWindow + ":night", true, true, false, false}},
		{"scheduled-keeps-snapshot", DecisionInput{Now: now, FirstSeenAt: now, MaxAge: 72 * time.Hour, CurrentReplicas: 1, SnapshotReplicas: &snapshot, ScheduledDown: true, ScheduledTarget: 0}, Decision{0, ReasonScaleDownWindow, false, true, false, false}},
		{"restore", DecisionInput{Now: now, FirstSeenAt: now, MaxAge: 72 * time.Hour, CurrentReplicas: 0, SnapshotReplicas: &snapshot}, Decision{3, ReasonRestoreReplicas, false, true, true, false}},
		{"active-preserves", DecisionInput{Now: now, FirstSeenAt: now, MaxAge: 72 * time.Hour, CurrentReplicas: 5}, Decision{5, ReasonActiveWindow, false, false, false, false}},
		{"down-never-scales-up", DecisionInput{Now: now, FirstSeenAt: now, MaxAge: 72 * time.Hour, CurrentReplicas: 1, ScheduledDown: true, ScheduledTarget: 2}, Decision{1, ReasonScaleDownWindow, true, false, false, false}},
		{"legacy-skip-falls-back-to-redeploy", DecisionInput{Now: now, FirstSeenAt: now, MaxAge: 72 * time.Hour, CurrentReplicas: 4, ScheduledDown: true, ScaleDownSkipped: true}, Decision{4, ReasonScaleDownSkipped, false, false, false, false}},
		{"replica-override-skip-retains-reason", DecisionInput{Now: now, FirstSeenAt: now, MaxAge: 72 * time.Hour, CurrentReplicas: 4, ScheduledDown: true, ScaleDownSkipped: true, ScaleDownSkipReason: ReasonScaleDownSkippedReplicaOverride}, Decision{4, ReasonScaleDownSkippedReplicaOverride, false, false, false, false}},
		{"expired-precedes-redeploy-skip", DecisionInput{Now: now, FirstSeenAt: now.Add(-73 * time.Hour), MaxAge: 72 * time.Hour, CurrentReplicas: 4, ScheduledDown: true, ExpiredTarget: 0, ScaleDownSkipped: true, ScaleDownSkipReason: ReasonScaleDownSkipped}, Decision{0, ReasonRevisionExpired, true, true, false, false}},
		{"expired-precedes-replica-override-skip", DecisionInput{Now: now, FirstSeenAt: now.Add(-73 * time.Hour), MaxAge: 72 * time.Hour, CurrentReplicas: 4, ScheduledDown: true, ExpiredTarget: 0, ScaleDownSkipped: true, ScaleDownSkipReason: ReasonScaleDownSkippedReplicaOverride}, Decision{0, ReasonRevisionExpired, true, true, false, false}},

		// ExpiredAction=delete tests
		{"expired-delete-action-deletes-workload", DecisionInput{Now: now, FirstSeenAt: now.Add(-73 * time.Hour), MaxAge: 72 * time.Hour, CurrentReplicas: 3, ExpiredAction: "delete"}, Decision{3, ReasonRevisionExpiredDeleted, false, false, false, true}},
		{"expired-scale-action-scales-down", DecisionInput{Now: now, FirstSeenAt: now.Add(-73 * time.Hour), MaxAge: 72 * time.Hour, CurrentReplicas: 3, ExpiredTarget: 0, ExpiredAction: "scale"}, Decision{0, ReasonRevisionExpired, true, true, false, false}},
		{"not-expired-delete-action-normal-logic", DecisionInput{Now: now, FirstSeenAt: now, MaxAge: 72 * time.Hour, CurrentReplicas: 5, ExpiredAction: "delete"}, Decision{5, ReasonActiveWindow, false, false, false, false}},
		{"expired-delete-precedes-skip", DecisionInput{Now: now, FirstSeenAt: now.Add(-73 * time.Hour), MaxAge: 72 * time.Hour, CurrentReplicas: 4, ScheduledDown: true, ExpiredTarget: 0, ScaleDownSkipped: true, ScaleDownSkipReason: ReasonScaleDownSkipped, ExpiredAction: "delete"}, Decision{4, ReasonRevisionExpiredDeleted, false, false, false, true}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := Decide(test.input); got != test.want {
				t.Fatalf("got %#v, want %#v", got, test.want)
			}
		})
	}
}
