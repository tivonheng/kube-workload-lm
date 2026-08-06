package controller

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"gitlab.glb.osl-nucleus.com/devops/kube-workload-lifecycle-manager/internal/lifecycle"
	"gitlab.glb.osl-nucleus.com/devops/kube-workload-lifecycle-manager/internal/policy"
	"gitlab.glb.osl-nucleus.com/devops/kube-workload-lifecycle-manager/internal/workload"
)

const orchestrationPolicyYAML = `apiVersion: lifecycle.example.com/v1alpha1
kind: WorkloadLifecyclePolicySet
spec:
  policies:
    - name: managed
      priority: 10
      target:
        kinds: [Deployment, StatefulSet]
        selector:
          matchLabels: {managed: "true"}
      lifecycle:
        maxAge: 72h
        revision: {source: ContainerImages}
      replicas: {scheduledDown: 0, expired: 0}
`

type fakeInventory struct {
	mu         sync.Mutex
	items      map[workload.Kind][]workload.Workload
	listErr    map[workload.Kind]error
	streams    chan workload.WatchStream
	watchCalls int
}

func (fake *fakeInventory) List(_ context.Context, kind workload.Kind) ([]workload.Workload, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return append([]workload.Workload(nil), fake.items[kind]...), fake.listErr[kind]
}
func (fake *fakeInventory) Watch(ctx context.Context) (workload.WatchStream, error) {
	fake.mu.Lock()
	fake.watchCalls++
	fake.mu.Unlock()
	select {
	case stream := <-fake.streams:
		return stream, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (fake *fakeInventory) set(kind workload.Kind, items []workload.Workload) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.items[kind] = append([]workload.Workload(nil), items...)
}

type fakeWorkloadStream struct {
	events  chan workload.Event
	stopped chan struct{}
	once    sync.Once
}

func newFakeWorkloadStream() *fakeWorkloadStream {
	return &fakeWorkloadStream{events: make(chan workload.Event), stopped: make(chan struct{})}
}
func (stream *fakeWorkloadStream) ResultChan() <-chan workload.Event { return stream.events }
func (stream *fakeWorkloadStream) Stop()                             { stream.once.Do(func() { close(stream.stopped) }) }

type fakeHPAWatcher struct{ streams chan workload.HPAWatchStream }

func (fake *fakeHPAWatcher) Watch(ctx context.Context) (workload.HPAWatchStream, error) {
	select {
	case stream := <-fake.streams:
		return stream, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type fakeHPAStream struct {
	events  chan workload.HPAEvent
	stopped chan struct{}
	once    sync.Once
}

func newFakeHPAStream() *fakeHPAStream {
	return &fakeHPAStream{events: make(chan workload.HPAEvent), stopped: make(chan struct{})}
}
func (stream *fakeHPAStream) ResultChan() <-chan workload.HPAEvent { return stream.events }
func (stream *fakeHPAStream) Stop()                                { stream.once.Do(func() { close(stream.stopped) }) }

type fakePolicyWatcher struct{ streams chan policy.WatchStream }

func (fake *fakePolicyWatcher) Watch(ctx context.Context) (policy.WatchStream, error) {
	select {
	case stream := <-fake.streams:
		return stream, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type fakePolicyStream struct {
	events  chan policy.Event
	stopped chan struct{}
	once    sync.Once
}

func newFakePolicyStream() *fakePolicyStream {
	return &fakePolicyStream{events: make(chan policy.Event), stopped: make(chan struct{})}
}
func (stream *fakePolicyStream) ResultChan() <-chan policy.Event { return stream.events }
func (stream *fakePolicyStream) Stop()                           { stream.once.Do(func() { close(stream.stopped) }) }

type recordingTransition struct {
	mu    sync.Mutex
	calls []workload.Workload
	fail  map[string]error
}

func (fake *recordingTransition) Reconcile(_ context.Context, current workload.Workload, _ policy.Policy) (lifecycle.Decision, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.calls = append(fake.calls, current)
	return lifecycle.Decision{}, fake.fail[current.Key()]
}
func (fake *recordingTransition) count() int {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return len(fake.calls)
}
func (fake *recordingTransition) keys() []string {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	keys := make([]string, len(fake.calls))
	for index := range fake.calls {
		keys[index] = fake.calls[index].Key()
	}
	return keys
}

type manualTicker struct {
	ticks   chan time.Time
	stopped chan struct{}
	once    sync.Once
}

func newManualTicker() *manualTicker {
	return &manualTicker{ticks: make(chan time.Time), stopped: make(chan struct{})}
}
func (ticker *manualTicker) Chan() <-chan time.Time { return ticker.ticks }
func (ticker *manualTicker) Stop()                  { ticker.once.Do(func() { close(ticker.stopped) }) }

type controllerHarness struct {
	controller     *Controller
	inventory      *fakeInventory
	workloadStream *fakeWorkloadStream
	hpaStream      *fakeHPAStream
	policyStream   *fakePolicyStream
	transition     *recordingTransition
	ticker         *manualTicker
	errorsMu       sync.Mutex
	errors         []error
}

func newControllerHarness(t *testing.T, items map[workload.Kind][]workload.Workload) *controllerHarness {
	t.Helper()
	manager := &policy.Manager{}
	if err := manager.Reload([]byte(orchestrationPolicyYAML)); err != nil {
		t.Fatal(err)
	}
	workloadStream, hpaStream, policyStream := newFakeWorkloadStream(), newFakeHPAStream(), newFakePolicyStream()
	inventory := &fakeInventory{items: items, listErr: map[workload.Kind]error{}, streams: make(chan workload.WatchStream, 8)}
	inventory.streams <- workloadStream
	hpa := &fakeHPAWatcher{streams: make(chan workload.HPAWatchStream, 8)}
	hpa.streams <- hpaStream
	policies := &fakePolicyWatcher{streams: make(chan policy.WatchStream, 8)}
	policies.streams <- policyStream
	transition := &recordingTransition{fail: map[string]error{}}
	harness := &controllerHarness{inventory: inventory, workloadStream: workloadStream, hpaStream: hpaStream, policyStream: policyStream, transition: transition, ticker: newManualTicker()}
	built, err := NewController(inventory, hpa, manager, policies, transition, ControllerOptions{
		ReconcileInterval: time.Hour, WatchRetryDelay: time.Nanosecond,
		HandleError: func(err error) {
			harness.errorsMu.Lock()
			harness.errors = append(harness.errors, err)
			harness.errorsMu.Unlock()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	built.newTicker = func(time.Duration) reconcileTicker { return harness.ticker }
	built.sleep = func(ctx context.Context, _ time.Duration) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	harness.controller = built
	return harness
}

func managed(kind workload.Kind, name string) workload.Workload {
	return workload.Workload{Kind: kind, Namespace: "team-a", Name: name, UID: name + "-uid", Labels: map[string]string{"managed": "true"}, Containers: []workload.Container{{Name: "main", Image: "app:v1"}}}
}

func (harness *controllerHarness) start(t *testing.T) (context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- harness.controller.Run(ctx) }()
	return cancel, done
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for controller condition")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestPeriodicReconcileUsesDefaultAndCoversBothKinds(t *testing.T) {
	if DefaultReconcileInterval != 30*time.Second {
		t.Fatalf("default interval = %s", DefaultReconcileInterval)
	}
	harness := newControllerHarness(t, map[workload.Kind][]workload.Workload{
		workload.KindDeployment:  {managed(workload.KindDeployment, "api")},
		workload.KindStatefulSet: {managed(workload.KindStatefulSet, "db")},
	})
	cancel, done := harness.start(t)
	waitFor(t, func() bool { return harness.transition.count() == 2 })
	harness.ticker.ticks <- time.Now()
	waitFor(t, func() bool { return harness.transition.count() == 4 })
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("controller did not stop")
	}
	for name, stopped := range map[string]<-chan struct{}{"workload": harness.workloadStream.stopped, "HPA": harness.hpaStream.stopped, "policy": harness.policyStream.stopped, "ticker": harness.ticker.stopped} {
		select {
		case <-stopped:
		default:
			t.Fatalf("%s resource was not stopped", name)
		}
	}
}

func TestWatchEventsTriggerTargetedAndFullReconciliation(t *testing.T) {
	harness := newControllerHarness(t, map[workload.Kind][]workload.Workload{})
	cancel, done := harness.start(t)
	defer func() { cancel(); <-done }()
	current := managed(workload.KindDeployment, "api")
	harness.workloadStream.events <- workload.Event{Type: workload.EventModified, Kind: current.Kind, Workload: &current}
	waitFor(t, func() bool { return harness.transition.count() == 1 })
	harness.inventory.set(workload.KindDeployment, []workload.Workload{current})
	harness.hpaStream.events <- workload.HPAEvent{Type: workload.EventAdded}
	waitFor(t, func() bool { return harness.transition.count() == 2 })
	harness.policyStream.events <- policy.Event{Type: policy.EventReloaded}
	waitFor(t, func() bool { return harness.transition.count() == 3 })
}

func TestWorkloadWatchErrorRecreatesWatchAndContinues(t *testing.T) {
	harness := newControllerHarness(t, map[workload.Kind][]workload.Workload{})
	replacement := newFakeWorkloadStream()
	harness.inventory.streams <- replacement
	cancel, done := harness.start(t)
	defer func() { cancel(); <-done }()
	harness.workloadStream.events <- workload.Event{Type: workload.EventError, Err: errors.New("expired resource version")}
	waitFor(t, func() bool {
		harness.inventory.mu.Lock()
		defer harness.inventory.mu.Unlock()
		return harness.inventory.watchCalls >= 2
	})
	select {
	case <-harness.workloadStream.stopped:
	default:
		t.Fatal("failed workload watch was not stopped")
	}
	current := managed(workload.KindStatefulSet, "db")
	replacement.events <- workload.Event{Type: workload.EventAdded, Kind: current.Kind, Workload: &current}
	waitFor(t, func() bool { return harness.transition.count() == 1 })
}

func TestReconcileFailuresAreIsolatedPerWorkloadAndKind(t *testing.T) {
	failed := managed(workload.KindDeployment, "broken")
	healthy := managed(workload.KindDeployment, "healthy")
	stateful := managed(workload.KindStatefulSet, "db")
	harness := newControllerHarness(t, map[workload.Kind][]workload.Workload{
		workload.KindDeployment: {failed, healthy}, workload.KindStatefulSet: {stateful},
	})
	harness.transition.fail[failed.Key()] = errors.New("state write failed")
	cancel, done := harness.start(t)
	waitFor(t, func() bool { return harness.transition.count() == 3 })
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	keys := harness.transition.keys()
	if len(keys) != 3 || keys[0] != failed.Key() || keys[1] != healthy.Key() || keys[2] != stateful.Key() {
		t.Fatalf("reconciled keys = %v", keys)
	}
	harness.errorsMu.Lock()
	defer harness.errorsMu.Unlock()
	if len(harness.errors) != 1 || !errors.Is(harness.errors[0], harness.transition.fail[failed.Key()]) {
		t.Fatalf("reported errors = %v", harness.errors)
	}
}

func TestListFailureForOneKindDoesNotBlockOtherKind(t *testing.T) {
	stateful := managed(workload.KindStatefulSet, "db")
	harness := newControllerHarness(t, map[workload.Kind][]workload.Workload{workload.KindStatefulSet: {stateful}})
	harness.inventory.listErr[workload.KindDeployment] = errors.New("deployments unavailable")
	cancel, done := harness.start(t)
	waitFor(t, func() bool { return harness.transition.count() == 1 })
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if keys := harness.transition.keys(); len(keys) != 1 || keys[0] != stateful.Key() {
		t.Fatalf("reconciled keys = %v", keys)
	}
}
