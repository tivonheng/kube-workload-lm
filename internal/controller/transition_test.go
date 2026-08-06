package controller

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"gitlab.glb.osl-nucleus.com/devops/kube-workload-lifecycle-manager/internal/lifecycle"
	"gitlab.glb.osl-nucleus.com/devops/kube-workload-lifecycle-manager/internal/policy"
	"gitlab.glb.osl-nucleus.com/devops/kube-workload-lifecycle-manager/internal/schedule"
	"gitlab.glb.osl-nucleus.com/devops/kube-workload-lifecycle-manager/internal/state"
	"gitlab.glb.osl-nucleus.com/devops/kube-workload-lifecycle-manager/internal/workload"
)

var testNow = time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)

type fixedClock struct{ now time.Time }

func (clock fixedClock) Now() time.Time { return clock.now }

type fakeHPA struct {
	result workload.HPAEvaluation
	ops    *[]string
}

func (fake fakeHPA) Evaluate(context.Context, workload.Workload) workload.HPAEvaluation {
	*fake.ops = append(*fake.ops, "hpa")
	return fake.result
}

type memoryStore struct {
	document state.Document
	ops      *[]string
	failSave func(state.Document) error
}

func (store *memoryStore) Load(context.Context) (state.Document, error) {
	*store.ops = append(*store.ops, "load")
	return cloneDocument(store.document), nil
}
func (store *memoryStore) Save(_ context.Context, document state.Document) error {
	label := "save"
	for _, value := range document.States {
		if value.ReplicaSnapshot != nil {
			label = fmt.Sprintf("save:snapshot:%d", value.ReplicaSnapshot.Replicas)
		}
	}
	*store.ops = append(*store.ops, label)
	if store.failSave != nil {
		if err := store.failSave(document); err != nil {
			return err
		}
	}
	store.document = cloneDocument(document)
	return nil
}

type fakeScale struct {
	replicas   int32
	ops        *[]string
	failUpdate error
}

func (scale *fakeScale) Read(_ context.Context, current workload.Workload) (workload.Workload, error) {
	*scale.ops = append(*scale.ops, fmt.Sprintf("read:%d", scale.replicas))
	current.CurrentReplicas = scale.replicas
	return current, nil
}
func (scale *fakeScale) Update(_ context.Context, _ workload.Workload, replicas int32) error {
	*scale.ops = append(*scale.ops, fmt.Sprintf("update:%d", replicas))
	if scale.failUpdate != nil {
		return scale.failUpdate
	}
	scale.replicas = replicas
	return nil
}

type noOpDeleter struct{}

func (noOpDeleter) Delete(context.Context, workload.Workload) error { return nil }

type fakeDeleter struct {
	called bool
	target workload.Workload
	err    error
}

func (d *fakeDeleter) Delete(_ context.Context, w workload.Workload) error {
	d.called = true
	d.target = w
	return d.err
}

func cloneDocument(document state.Document) state.Document {
	copy := state.Document{FormatVersion: document.FormatVersion, States: make(map[string]state.WorkloadState, len(document.States))}
	for key, value := range document.States {
		value.Revision.Containers = append([]workload.Container(nil), value.Revision.Containers...)
		if value.ReplicaSnapshot != nil {
			snapshot := *value.ReplicaSnapshot
			value.ReplicaSnapshot = &snapshot
		}
		copy.States[key] = value
	}
	return copy
}

func testWorkload(uid, image string) workload.Workload {
	return workload.Workload{Kind: workload.KindDeployment, Namespace: "team-a", Name: "api", UID: uid,
		Containers: []workload.Container{{Name: "api", Image: image}}, CurrentReplicas: 99}
}
func testPolicy(scheduled bool, target int32) policy.Policy {
	result := policy.Policy{Name: "temporary", Lifecycle: policy.Lifecycle{
		MaxAge: 72 * time.Hour, Revision: lifecycle.TrackingSpec{Source: lifecycle.SourceContainerImages},
	}, Replicas: policy.Replicas{ScheduledDown: target, Expired: 0}}
	if scheduled {
		result.Schedule = &policy.Schedule{TimeZone: "UTC", DownWindows: []schedule.Window{{
			Name: "always", StartDays: []schedule.Day{schedule.Monday, schedule.Tuesday, schedule.Wednesday,
				schedule.Thursday, schedule.Friday, schedule.Saturday, schedule.Sunday}, AllDay: true,
		}}}
	}
	return result
}
func newHarness(t *testing.T, document state.Document, replicas int32) (*Reconciler, *memoryStore, *fakeScale, *[]string) {
	t.Helper()
	ops := []string{}
	store := &memoryStore{document: document, ops: &ops}
	scale := &fakeScale{replicas: replicas, ops: &ops}
	reconciler, err := NewReconciler(store, scale, noOpDeleter{}, fakeHPA{result: workload.HPAEvaluation{Allowed: true}, ops: &ops}, fixedClock{testNow})
	if err != nil {
		t.Fatal(err)
	}
	return reconciler, store, scale, &ops
}

func TestDownscaleReadsLiveAndPersistsSnapshotBeforeScale(t *testing.T) {
	reconciler, store, scale, ops := newHarness(t, state.NewDocument(), 5)
	decision, err := reconciler.Reconcile(context.Background(), testWorkload("uid-1", "api:v1"), testPolicy(true, 2))
	if err != nil {
		t.Fatal(err)
	}
	if decision.DesiredReplicas != 2 || scale.replicas != 2 {
		t.Fatalf("decision=%#v replicas=%d", decision, scale.replicas)
	}
	snapshot := store.document.States["Deployment/team-a/api"].ReplicaSnapshot
	if snapshot == nil || snapshot.Replicas != 5 {
		t.Fatalf("snapshot=%#v, want live replicas 5", snapshot)
	}
	assertOrdered(t, *ops, "read:5", "save:snapshot:5", "update:2")
}

func TestSnapshotSaveFailurePreventsDownscale(t *testing.T) {
	reconciler, store, scale, ops := newHarness(t, state.NewDocument(), 5)
	store.failSave = func(document state.Document) error {
		if document.States["Deployment/team-a/api"].ReplicaSnapshot != nil {
			return errors.New("state unavailable")
		}
		return nil
	}
	_, err := reconciler.Reconcile(context.Background(), testWorkload("uid-1", "api:v1"), testPolicy(true, 0))
	if err == nil || strings.Contains(strings.Join(*ops, ","), "update:") {
		t.Fatalf("err=%v ops=%v", err, *ops)
	}
	if scale.replicas != 5 {
		t.Fatalf("replicas changed to %d", scale.replicas)
	}
}

func TestFailedDownscaleThenHigherLiveReplicaIsAcceptedAsOverride(t *testing.T) {
	reconciler, store, scale, _ := newHarness(t, state.NewDocument(), 5)
	scale.failUpdate = errors.New("conflict")
	if _, err := reconciler.Reconcile(context.Background(), testWorkload("uid-1", "api:v1"), testPolicy(true, 2)); err == nil {
		t.Fatal("expected scale failure")
	}
	if got := store.document.States["Deployment/team-a/api"].ReplicaSnapshot; got == nil || got.Replicas != 5 {
		t.Fatalf("snapshot after failure=%#v", got)
	}

	scale.failUpdate = nil
	scale.replicas = 8
	decision, err := reconciler.Reconcile(context.Background(), testWorkload("uid-1", "api:v1"), testPolicy(true, 2))
	if err != nil {
		t.Fatal(err)
	}
	got := store.document.States["Deployment/team-a/api"]
	if decision.Reason != lifecycle.ReasonScaleDownSkippedReplicaOverride || decision.ShouldScale || scale.replicas != 8 {
		t.Fatalf("decision=%#v replicas=%d", decision, scale.replicas)
	}
	if got.ReplicaSnapshot != nil || !got.ScaleDownSkipped || got.ScaleDownSkipReason != lifecycle.ReasonScaleDownSkippedReplicaOverride {
		t.Fatalf("override state=%#v", got)
	}
}

func TestRestoreScalesBeforeClearingAndRetriesSafely(t *testing.T) {
	current := testWorkload("uid-1", "api:v1")
	document := documentWithSnapshot(t, current, 5)
	reconciler, store, scale, ops := newHarness(t, document, 0)
	scale.failUpdate = errors.New("conflict")
	if _, err := reconciler.Reconcile(context.Background(), current, testPolicy(false, 0)); err == nil {
		t.Fatal("expected restore failure")
	}
	if store.document.States[current.Key()].ReplicaSnapshot == nil {
		t.Fatal("failed restore cleared snapshot")
	}
	scale.failUpdate = nil
	if _, err := reconciler.Reconcile(context.Background(), current, testPolicy(false, 0)); err != nil {
		t.Fatal(err)
	}
	if scale.replicas != 5 || store.document.States[current.Key()].ReplicaSnapshot != nil {
		t.Fatalf("replicas=%d state=%#v", scale.replicas, store.document.States[current.Key()])
	}
	lastUpdate := lastIndex(*ops, "update:5")
	lastClear := lastIndex(*ops, "save")
	if lastUpdate < 0 || lastClear <= lastUpdate {
		t.Fatalf("restore ordering=%v", *ops)
	}
}

func TestClearFailureLeavesRetryableSnapshot(t *testing.T) {
	current := testWorkload("uid-1", "api:v1")
	reconciler, store, scale, ops := newHarness(t, documentWithSnapshot(t, current, 4), 0)
	failClear := true
	store.failSave = func(document state.Document) error {
		if failClear && document.States[current.Key()].ReplicaSnapshot == nil {
			return errors.New("write failed")
		}
		return nil
	}
	if _, err := reconciler.Reconcile(context.Background(), current, testPolicy(false, 0)); err == nil {
		t.Fatal("expected clear failure")
	}
	if scale.replicas != 4 || store.document.States[current.Key()].ReplicaSnapshot == nil {
		t.Fatal("clear failure was not retryable")
	}
	failClear = false
	if _, err := reconciler.Reconcile(context.Background(), current, testPolicy(false, 0)); err != nil {
		t.Fatal(err)
	}
	if count(*ops, "update:4") != 2 || store.document.States[current.Key()].ReplicaSnapshot != nil {
		t.Fatalf("ops=%v state=%#v", *ops, store.document.States[current.Key()])
	}
}

func TestHPARejectionOccursBeforeStateOrScaleAccess(t *testing.T) {
	ops := []string{}
	store := &memoryStore{document: state.NewDocument(), ops: &ops}
	scale := &fakeScale{replicas: 3, ops: &ops}
	reconciler, _ := NewReconciler(store, scale, noOpDeleter{}, fakeHPA{result: workload.HPAEvaluation{Reason: workload.HPAReasonConflict}, ops: &ops}, fixedClock{testNow})
	if _, err := reconciler.Reconcile(context.Background(), testWorkload("uid-1", "api:v1"), testPolicy(true, 0)); !errors.Is(err, workload.ErrHPAConflict) {
		t.Fatalf("err=%v", err)
	}
	if strings.Join(ops, ",") != "hpa" {
		t.Fatalf("operations after HPA rejection: %v", ops)
	}
}

func TestSnapshotIdentityRules(t *testing.T) {
	old := testWorkload("uid-1", "api:v1")
	t.Run("same UID new revision preserves snapshot", func(t *testing.T) {
		reconciler, store, _, _ := newHarness(t, documentWithSnapshot(t, old, 7), 0)
		if _, err := reconciler.Reconcile(context.Background(), testWorkload("uid-1", "api:v2"), testPolicy(true, 0)); err != nil {
			t.Fatal(err)
		}
		got := store.document.States[old.Key()].ReplicaSnapshot
		if got == nil || got.Replicas != 7 {
			t.Fatalf("snapshot=%#v", got)
		}
	})
	t.Run("new UID discards old snapshot and captures live scale", func(t *testing.T) {
		reconciler, store, _, _ := newHarness(t, documentWithSnapshot(t, old, 7), 2)
		if _, err := reconciler.Reconcile(context.Background(), testWorkload("uid-2", "api:v1"), testPolicy(true, 0)); err != nil {
			t.Fatal(err)
		}
		got := store.document.States[old.Key()]
		if got.WorkloadUID != "uid-2" || got.ReplicaSnapshot == nil || got.ReplicaSnapshot.Replicas != 2 {
			t.Fatalf("state=%#v", got)
		}
	})
}

// TestDownscaleOrderingProperty validates Property 2 and Property 3.
// **Validates: Requirements 5.1, 5.2, 5.4**
func TestDownscaleOrderingProperty(t *testing.T) {
	property := func(snapshot uint8, configured uint8) bool {
		liveReplicas := int32(snapshot % 50)
		target := int32(configured % 50)
		reconciler, store, scale, ops := newHarness(t, state.NewDocument(), liveReplicas)
		decision, err := reconciler.Reconcile(context.Background(), testWorkload("uid-property", "api:v1"), testPolicy(true, target))
		if err != nil {
			return false
		}
		want := min(target, liveReplicas)
		captured := store.document.States["Deployment/team-a/api"].ReplicaSnapshot
		return captured != nil && captured.Replicas == liveReplicas && decision.DesiredReplicas == want &&
			scale.replicas == want && want <= liveReplicas && ordered(*ops, fmt.Sprintf("read:%d", liveReplicas),
			fmt.Sprintf("save:snapshot:%d", liveReplicas), fmt.Sprintf("update:%d", want), decision.ShouldScale)
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 100, Rand: rand.New(rand.NewSource(42))}); err != nil {
		t.Fatal(err)
	}
}

func documentWithSnapshot(t *testing.T, current workload.Workload, replicas int32) state.Document {
	t.Helper()
	revision, err := lifecycle.CalculateRevision(current.Containers, lifecycle.TrackingSpec{Source: lifecycle.SourceContainerImages})
	if err != nil {
		t.Fatal(err)
	}
	value := state.WorkloadState{
		Kind: current.Kind, Namespace: current.Namespace, Name: current.Name, WorkloadUID: current.UID,
		LastPolicyName: "temporary", TrackingSpecHash: revision.TrackingSpecHash, RevisionHash: revision.RevisionHash,
		Revision:    state.Revision{Source: lifecycle.SourceContainerImages, Containers: revision.Containers},
		FirstSeenAt: testNow, LastSeenAt: testNow,
		ReplicaSnapshot: &state.ReplicaSnapshot{Replicas: replicas, CapturedAt: testNow, CapturedReason: lifecycle.ReasonScaleDownWindow},
	}
	document := state.NewDocument()
	document.States[value.Key()] = value
	return document
}

func assertOrdered(t *testing.T, operations []string, expected ...string) {
	t.Helper()
	position := -1
	for _, item := range expected {
		next := indexAfter(operations, item, position+1)
		if next < 0 {
			t.Fatalf("%q not found after index %d in %v", item, position, operations)
		}
		position = next
	}
}
func ordered(operations []string, read, save, update string, expectUpdate bool) bool {
	readAt := indexAfter(operations, read, 0)
	saveAt := indexAfter(operations, save, readAt+1)
	if readAt < 0 || saveAt <= readAt {
		return false
	}
	updateAt := indexAfter(operations, update, saveAt+1)
	if expectUpdate {
		return updateAt > saveAt
	}
	return updateAt < 0
}
func indexAfter(values []string, target string, start int) int {
	for index := max(start, 0); index < len(values); index++ {
		if values[index] == target {
			return index
		}
	}
	return -1
}
func lastIndex(values []string, target string) int {
	for index := len(values) - 1; index >= 0; index-- {
		if values[index] == target {
			return index
		}
	}
	return -1
}
func count(values []string, target string) int {
	total := 0
	for _, value := range values {
		if value == target {
			total++
		}
	}
	return total
}

func TestReconcileIdentitySeparatesRevisionAndTrackingChanges(t *testing.T) {
	current := testWorkload("uid-1", "api:v1")
	basePolicy := testPolicy(false, 0)
	baseRevision, err := lifecycle.CalculateRevision(current.Containers, basePolicy.Lifecycle.Revision)
	if err != nil {
		t.Fatal(err)
	}
	baseline, changed := reconcileIdentity(state.WorkloadState{}, current, basePolicy, baseRevision, testNow)
	if !changed || !baseline.FirstSeenAt.Equal(testNow) {
		t.Fatalf("initial baseline = %#v changed=%v", baseline, changed)
	}

	metadataOnly := current
	metadataOnly.Labels = map[string]string{"release": "changed"}
	metadataOnly.CurrentReplicas = 17
	switchedPolicy := basePolicy
	switchedPolicy.Name = "different-policy"
	unchanged, changed := reconcileIdentity(baseline, metadataOnly, switchedPolicy, baseRevision, testNow.Add(time.Hour))
	if changed || !unchanged.FirstSeenAt.Equal(testNow) || unchanged.LastPolicyName != "different-policy" {
		t.Fatalf("metadata/policy switch reset identity: %#v changed=%v", unchanged, changed)
	}

	imageChanged := testWorkload("uid-1", "api:v2")
	imageRevision, err := lifecycle.CalculateRevision(imageChanged.Containers, basePolicy.Lifecycle.Revision)
	if err != nil {
		t.Fatal(err)
	}
	newRevision, changed := reconcileIdentity(unchanged, imageChanged, basePolicy, imageRevision, testNow.Add(2*time.Hour))
	if !changed || newRevision.TrackingSpecHash != unchanged.TrackingSpecHash || newRevision.RevisionHash == unchanged.RevisionHash {
		t.Fatalf("image change was not isolated to revision identity: %#v", newRevision)
	}

	namedPolicy := basePolicy
	namedPolicy.Lifecycle.Revision.Containers = []string{"api"}
	namedRevision, err := lifecycle.CalculateRevision(imageChanged.Containers, namedPolicy.Lifecycle.Revision)
	if err != nil {
		t.Fatal(err)
	}
	definitionChanged, changed := reconcileIdentity(newRevision, imageChanged, namedPolicy, namedRevision, testNow.Add(3*time.Hour))
	if !changed || definitionChanged.TrackingSpecHash == newRevision.TrackingSpecHash || definitionChanged.RevisionHash != newRevision.RevisionHash {
		t.Fatalf("tracking-definition change was not independently identified: %#v", definitionChanged)
	}
}

func TestExpiredRevisionStaysDownUntilNewRevisionRestoresSnapshot(t *testing.T) {
	current := testWorkload("uid-1", "api:v1")
	document := documentWithSnapshot(t, current, 5)
	old := document.States[current.Key()]
	old.FirstSeenAt = testNow.Add(-73 * time.Hour)
	document.States[current.Key()] = old
	reconciler, store, scale, _ := newHarness(t, document, 0)

	decision, err := reconciler.Reconcile(context.Background(), current, testPolicy(false, 0))
	if err != nil || decision.Reason != lifecycle.ReasonRevisionExpired || scale.replicas != 0 {
		t.Fatalf("expired decision=%#v replicas=%d err=%v", decision, scale.replicas, err)
	}
	if store.document.States[current.Key()].ReplicaSnapshot == nil {
		t.Fatal("expired revision lost restoration snapshot")
	}

	decision, err = reconciler.Reconcile(context.Background(), testWorkload("uid-1", "api:v2"), testPolicy(false, 0))
	if err != nil || decision.Reason != lifecycle.ReasonRestoreReplicas || scale.replicas != 5 {
		t.Fatalf("new revision decision=%#v replicas=%d err=%v", decision, scale.replicas, err)
	}
	if store.document.States[current.Key()].ReplicaSnapshot != nil {
		t.Fatal("successful new-revision restore retained snapshot")
	}
}

func TestInitialScheduledDownCapturesSnapshotAndWindowRevision(t *testing.T) {
	current := testWorkload("uid-initial-down", "api:v1")
	reconciler, store, scale, _ := newHarness(t, state.NewDocument(), 5)

	decision, err := reconciler.Reconcile(context.Background(), current, testPolicy(true, 2))
	if err != nil {
		t.Fatal(err)
	}
	persisted := store.document.States[current.Key()]
	if decision.Reason != lifecycle.ReasonScaleDownWindow+":always" || scale.replicas != 2 {
		t.Fatalf("decision=%#v replicas=%d", decision, scale.replicas)
	}
	if persisted.ReplicaSnapshot == nil || persisted.ReplicaSnapshot.Replicas != 5 {
		t.Fatalf("snapshot=%#v, want captured replicas 5", persisted.ReplicaSnapshot)
	}
	if persisted.WindowEntryRevision == "" || persisted.WindowEntryRevision != persisted.RevisionHash {
		t.Fatalf("window entry revision=%q revision=%q", persisted.WindowEntryRevision, persisted.RevisionHash)
	}
	if persisted.ScaleDownSkipped || persisted.ScaleDownSkipReason != "" {
		t.Fatalf("unexpected initial skip state: %#v", persisted)
	}
}

func TestExternalReplicaOverrideSkipsSameWindowAndSurvivesRestart(t *testing.T) {
	store, scale, ops, current, selected := prepareReplicaOverride(t)
	persisted := store.document.States[current.Key()]
	if !persisted.ScaleDownSkipped || persisted.ScaleDownSkipReason != lifecycle.ReasonScaleDownSkippedReplicaOverride {
		t.Fatalf("override state=%#v", persisted)
	}
	if persisted.ReplicaSnapshot != nil {
		t.Fatalf("override retained snapshot=%#v", persisted.ReplicaSnapshot)
	}

	beforeRestart := len(*ops)
	restarted, err := NewReconciler(store, scale, noOpDeleter{}, fakeHPA{result: workload.HPAEvaluation{Allowed: true}, ops: ops}, fixedClock{testNow})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := restarted.Reconcile(context.Background(), current, selected)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Reason != lifecycle.ReasonScaleDownSkippedReplicaOverride || decision.ShouldScale || scale.replicas != 4 {
		t.Fatalf("restart decision=%#v replicas=%d", decision, scale.replicas)
	}
	if strings.Contains(strings.Join((*ops)[beforeRestart:], ","), "update:") {
		t.Fatalf("restart performed scale write: %v", (*ops)[beforeRestart:])
	}
}

func TestReplicaOverrideClearsAtWindowBoundaryAndSchedulingResumes(t *testing.T) {
	t.Run("outside window", func(t *testing.T) {
		store, scale, ops, current, selected := prepareReplicaOverride(t)
		decision, err := (&Reconciler{store: store, scale: scale, deleter: noOpDeleter{}, hpa: fakeHPA{result: workload.HPAEvaluation{Allowed: true}, ops: ops}, clock: fixedClock{testNow}}).
			Reconcile(context.Background(), current, testPolicy(false, 2))
		if err != nil {
			t.Fatal(err)
		}
		cleared := store.document.States[current.Key()]
		if decision.Reason != lifecycle.ReasonActiveWindow || cleared.ScaleDownSkipped || cleared.ScaleDownSkipReason != "" || cleared.WindowEntryRevision != "" || cleared.WindowInstanceID != "" {
			t.Fatalf("outside-window decision=%#v state=%#v", decision, cleared)
		}

		decision, err = (&Reconciler{store: store, scale: scale, deleter: noOpDeleter{}, hpa: fakeHPA{result: workload.HPAEvaluation{Allowed: true}, ops: ops}, clock: fixedClock{testNow}}).
			Reconcile(context.Background(), current, selected)
		if err != nil {
			t.Fatal(err)
		}
		resumed := store.document.States[current.Key()]
		if decision.Reason != lifecycle.ReasonScaleDownWindow+":always" || scale.replicas != 2 || resumed.ReplicaSnapshot == nil || resumed.ReplicaSnapshot.Replicas != 4 {
			t.Fatalf("resumed decision=%#v replicas=%d state=%#v", decision, scale.replicas, resumed)
		}
	})

	t.Run("new window instance", func(t *testing.T) {
		store, scale, ops, current, selected := prepareReplicaOverride(t)
		oldInstance := store.document.States[current.Key()].WindowInstanceID
		nextWindow, err := NewReconciler(store, scale, noOpDeleter{}, fakeHPA{result: workload.HPAEvaluation{Allowed: true}, ops: ops}, fixedClock{testNow.Add(24 * time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		decision, err := nextWindow.Reconcile(context.Background(), current, selected)
		if err != nil {
			t.Fatal(err)
		}
		resumed := store.document.States[current.Key()]
		if decision.Reason != lifecycle.ReasonScaleDownWindow+":always" || scale.replicas != 2 {
			t.Fatalf("new-window decision=%#v replicas=%d", decision, scale.replicas)
		}
		if resumed.ScaleDownSkipped || resumed.ScaleDownSkipReason != "" || resumed.WindowInstanceID == oldInstance || resumed.ReplicaSnapshot == nil || resumed.ReplicaSnapshot.Replicas != 4 {
			t.Fatalf("old instance=%q resumed state=%#v", oldInstance, resumed)
		}
	})
}

func TestRedeploySkipRetainsRedeployReason(t *testing.T) {
	current := testWorkload("uid-redeploy", "api:v1")
	selected := testPolicy(true, 2)
	reconciler, store, scale, ops := newHarness(t, state.NewDocument(), 5)
	if _, err := reconciler.Reconcile(context.Background(), current, selected); err != nil {
		t.Fatal(err)
	}

	beforeRedeploy := len(*ops)
	decision, err := reconciler.Reconcile(context.Background(), testWorkload(current.UID, "api:v2"), selected)
	if err != nil {
		t.Fatal(err)
	}
	persisted := store.document.States[current.Key()]
	if decision.Reason != lifecycle.ReasonScaleDownSkipped || decision.ShouldScale || scale.replicas != 5 {
		t.Fatalf("redeploy decision=%#v replicas=%d", decision, scale.replicas)
	}
	if !persisted.ScaleDownSkipped || persisted.ScaleDownSkipReason != lifecycle.ReasonScaleDownSkipped || persisted.ReplicaSnapshot != nil {
		t.Fatalf("redeploy state=%#v", persisted)
	}
	updates := strings.Join((*ops)[beforeRedeploy:], ",")
	if !strings.Contains(updates, "update:5") || strings.Contains(updates, "update:2") {
		t.Fatalf("redeploy scale operations=%v", (*ops)[beforeRedeploy:])
	}
}

func prepareReplicaOverride(t *testing.T) (*memoryStore, *fakeScale, *[]string, workload.Workload, policy.Policy) {
	t.Helper()
	current := testWorkload("uid-override", "api:v1")
	selected := testPolicy(true, 2)
	reconciler, store, scale, ops := newHarness(t, state.NewDocument(), 5)
	if _, err := reconciler.Reconcile(context.Background(), current, selected); err != nil {
		t.Fatal(err)
	}

	scale.replicas = 4
	beforeOverride := len(*ops)
	decision, err := reconciler.Reconcile(context.Background(), current, selected)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Reason != lifecycle.ReasonScaleDownSkippedReplicaOverride || decision.ShouldScale || decision.DesiredReplicas != 4 || scale.replicas != 4 {
		t.Fatalf("override decision=%#v replicas=%d", decision, scale.replicas)
	}
	if strings.Contains(strings.Join((*ops)[beforeOverride:], ","), "update:") {
		t.Fatalf("override performed scale write: %v", (*ops)[beforeOverride:])
	}
	return store, scale, ops, current, selected
}

// --- Deletion logic tests ---

// testPolicyWithExpiredAction creates a policy with a given expiredAction value.
// If scheduled is true, an always-on window is attached.
func testPolicyWithExpiredAction(scheduled bool, target int32, expiredAction string) policy.Policy {
	p := testPolicy(scheduled, target)
	p.Lifecycle.ExpiredAction = expiredAction
	return p
}

// expiredDocument creates a document where the workload has been seen long enough ago
// to be considered expired (firstSeenAt is 73 hours before testNow, maxAge is 72h).
func expiredDocument(t *testing.T, current workload.Workload) state.Document {
	t.Helper()
	revision, err := lifecycle.CalculateRevision(current.Containers, lifecycle.TrackingSpec{Source: lifecycle.SourceContainerImages})
	if err != nil {
		t.Fatal(err)
	}
	value := state.WorkloadState{
		Kind: current.Kind, Namespace: current.Namespace, Name: current.Name, WorkloadUID: current.UID,
		LastPolicyName: "temporary", TrackingSpecHash: revision.TrackingSpecHash, RevisionHash: revision.RevisionHash,
		Revision:    state.Revision{Source: lifecycle.SourceContainerImages, Containers: revision.Containers},
		FirstSeenAt: testNow.Add(-73 * time.Hour), LastSeenAt: testNow,
	}
	document := state.NewDocument()
	document.States[value.Key()] = value
	return document
}

func TestExpiredDeleteCallsDeleterAndCleansState(t *testing.T) {
	current := testWorkload("uid-del-1", "api:v1")
	document := expiredDocument(t, current)

	ops := []string{}
	store := &memoryStore{document: document, ops: &ops}
	scale := &fakeScale{replicas: 3, ops: &ops}
	deleter := &fakeDeleter{}

	reconciler, err := NewReconciler(store, scale, deleter, fakeHPA{result: workload.HPAEvaluation{Allowed: true}, ops: &ops}, fixedClock{testNow})
	if err != nil {
		t.Fatal(err)
	}

	decision, err := reconciler.Reconcile(context.Background(), current, testPolicyWithExpiredAction(false, 0, policy.ExpiredActionDelete))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !decision.DeleteWorkload {
		t.Fatal("expected DeleteWorkload=true")
	}
	if decision.Reason != lifecycle.ReasonRevisionExpiredDeleted {
		t.Fatalf("reason=%q want %q", decision.Reason, lifecycle.ReasonRevisionExpiredDeleted)
	}
	if !deleter.called {
		t.Fatal("deleter.Delete was not called")
	}
	if _, exists := store.document.States[current.Key()]; exists {
		t.Fatal("state entry was not removed after successful deletion")
	}
}

func TestExpiredDeleteFailurePreservesState(t *testing.T) {
	current := testWorkload("uid-del-2", "api:v1")
	document := expiredDocument(t, current)

	ops := []string{}
	store := &memoryStore{document: document, ops: &ops}
	scale := &fakeScale{replicas: 3, ops: &ops}
	deleter := &fakeDeleter{err: errors.New("API unavailable")}

	reconciler, err := NewReconciler(store, scale, deleter, fakeHPA{result: workload.HPAEvaluation{Allowed: true}, ops: &ops}, fixedClock{testNow})
	if err != nil {
		t.Fatal(err)
	}

	_, err = reconciler.Reconcile(context.Background(), current, testPolicyWithExpiredAction(false, 0, policy.ExpiredActionDelete))
	if err == nil {
		t.Fatal("expected error from failed delete")
	}
	if !deleter.called {
		t.Fatal("deleter.Delete was not called")
	}
	if _, exists := store.document.States[current.Key()]; !exists {
		t.Fatal("state entry was removed despite delete failure")
	}
}

func TestExpiredDeleteSuccessButSaveFailsReturnsError(t *testing.T) {
	current := testWorkload("uid-del-3", "api:v1")
	document := expiredDocument(t, current)

	ops := []string{}
	store := &memoryStore{document: document, ops: &ops}
	// Fail on the save that happens after state cleanup (state entry removed from map).
	saveCallCount := 0
	store.failSave = func(doc state.Document) error {
		saveCallCount++
		// The first save is the identity reconcile save. The state cleanup save
		// happens when the document no longer has the workload key.
		if _, exists := doc.States[current.Key()]; !exists {
			return errors.New("configmap write failed")
		}
		return nil
	}
	scale := &fakeScale{replicas: 3, ops: &ops}
	deleter := &fakeDeleter{}

	reconciler, err := NewReconciler(store, scale, deleter, fakeHPA{result: workload.HPAEvaluation{Allowed: true}, ops: &ops}, fixedClock{testNow})
	if err != nil {
		t.Fatal(err)
	}

	_, err = reconciler.Reconcile(context.Background(), current, testPolicyWithExpiredAction(false, 0, policy.ExpiredActionDelete))
	if err == nil {
		t.Fatal("expected error from failed save after delete")
	}
	if !deleter.called {
		t.Fatal("deleter.Delete was not called")
	}
	if !strings.Contains(err.Error(), "clean state") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestNotExpiredWithDeletePolicyDoesNotCallDeleter(t *testing.T) {
	current := testWorkload("uid-del-4", "api:v1")
	// Use a fresh document — the workload is NOT expired (firstSeenAt will be set to now).
	document := state.NewDocument()

	ops := []string{}
	store := &memoryStore{document: document, ops: &ops}
	scale := &fakeScale{replicas: 3, ops: &ops}
	deleter := &fakeDeleter{}

	reconciler, err := NewReconciler(store, scale, deleter, fakeHPA{result: workload.HPAEvaluation{Allowed: true}, ops: &ops}, fixedClock{testNow})
	if err != nil {
		t.Fatal(err)
	}

	decision, err := reconciler.Reconcile(context.Background(), current, testPolicyWithExpiredAction(false, 0, policy.ExpiredActionDelete))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision.DeleteWorkload {
		t.Fatal("DeleteWorkload should be false for non-expired workload")
	}
	if deleter.called {
		t.Fatal("deleter.Delete should not be called when workload is not expired")
	}
}

func TestExpiredWithScalePolicyDoesNotCallDeleter(t *testing.T) {
	current := testWorkload("uid-del-5", "api:v1")
	document := expiredDocument(t, current)

	ops := []string{}
	store := &memoryStore{document: document, ops: &ops}
	scale := &fakeScale{replicas: 3, ops: &ops}
	deleter := &fakeDeleter{}

	reconciler, err := NewReconciler(store, scale, deleter, fakeHPA{result: workload.HPAEvaluation{Allowed: true}, ops: &ops}, fixedClock{testNow})
	if err != nil {
		t.Fatal(err)
	}

	decision, err := reconciler.Reconcile(context.Background(), current, testPolicyWithExpiredAction(false, 0, policy.ExpiredActionScale))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision.DeleteWorkload {
		t.Fatal("DeleteWorkload should be false for scale policy")
	}
	if decision.Reason != lifecycle.ReasonRevisionExpired {
		t.Fatalf("reason=%q want %q", decision.Reason, lifecycle.ReasonRevisionExpired)
	}
	if deleter.called {
		t.Fatal("deleter.Delete should not be called when expiredAction is scale")
	}
}
