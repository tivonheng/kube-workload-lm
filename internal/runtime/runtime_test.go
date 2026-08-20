package runtime

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	kubefake "k8s.io/client-go/kubernetes/fake"
)

type fakeElector struct {
	callbacks chan LeaderCallbacks
}

func (elector *fakeElector) Run(ctx context.Context, callbacks LeaderCallbacks) error {
	elector.callbacks <- callbacks
	<-ctx.Done()
	return nil
}

type fakeServer struct {
	started          chan struct{}
	stopped          chan struct{}
	shutdownOnce     sync.Once
	deadlineObserved atomic.Bool
	listenErr        error
	shutdownErr      error
}

func newFakeServer() *fakeServer {
	return &fakeServer{started: make(chan struct{}), stopped: make(chan struct{})}
}

func (server *fakeServer) ListenAndServe() error {
	close(server.started)
	if server.listenErr != nil {
		return server.listenErr
	}
	<-server.stopped
	return http.ErrServerClosed
}

func (server *fakeServer) Shutdown(ctx context.Context) error {
	if _, ok := ctx.Deadline(); ok {
		server.deadlineObserved.Store(true)
	}
	server.shutdownOnce.Do(func() { close(server.stopped) })
	return server.shutdownErr
}

type fakeRunner struct {
	started chan struct{}
	stopped chan struct{}
	err     error
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{started: make(chan struct{}), stopped: make(chan struct{})}
}

func (runner *fakeRunner) Run(ctx context.Context) error {
	close(runner.started)
	if runner.err != nil {
		return runner.err
	}
	<-ctx.Done()
	close(runner.stopped)
	return nil
}

type fakeReadiness struct {
	running      atomic.Bool
	leader       atomic.Bool
	shuttingDown atomic.Bool
}

func (readiness *fakeReadiness) SetControllerRunning(running bool) {
	readiness.running.Store(running)
}

func (readiness *fakeReadiness) SetLeader(leader bool) {
	readiness.leader.Store(leader)
}

func (readiness *fakeReadiness) SetShuttingDown(down bool) {
	readiness.shuttingDown.Store(down)
}

func startCoordinator(t *testing.T) (context.CancelFunc, <-chan error, *fakeElector, *fakeServer, *fakeRunner, *fakeReadiness) {
	t.Helper()
	elector := &fakeElector{callbacks: make(chan LeaderCallbacks, 1)}
	server := newFakeServer()
	runner := newFakeRunner()
	readiness := &fakeReadiness{}
	coordinator, err := NewCoordinator(elector, server, runner, readiness, Options{ShutdownTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- coordinator.Run(ctx) }()
	return cancel, result, elector, server, runner, readiness
}

func receiveCallbacks(t *testing.T, elector *fakeElector) LeaderCallbacks {
	t.Helper()
	select {
	case callbacks := <-elector.callbacks:
		return callbacks
	case <-time.After(time.Second):
		t.Fatal("leader election did not start")
		return LeaderCallbacks{}
	}
}

func receiveResult(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("runtime did not stop")
		return nil
	}
}

func TestNonLeaderServesProbesWithoutRunningController(t *testing.T) {
	cancel, result, elector, server, runner, readiness := startCoordinator(t)
	_ = receiveCallbacks(t, elector)
	<-server.started
	select {
	case <-runner.started:
		t.Fatal("controller ran before leadership was acquired")
	default:
	}
	if readiness.running.Load() {
		t.Fatal("non-leader reported controller running")
	}
	if readiness.leader.Load() {
		t.Fatal("non-leader reported leadership")
	}
	if readiness.shuttingDown.Load() {
		t.Fatal("running standby reported shutting down")
	}
	cancel()
	if err := receiveResult(t, result); err != nil {
		t.Fatalf("graceful cancellation returned %v", err)
	}
	select {
	case <-server.stopped:
	default:
		t.Fatal("HTTP server was not shut down")
	}
	if !server.deadlineObserved.Load() {
		t.Fatal("HTTP shutdown did not receive a bounded context")
	}
}

func TestLeadershipAcquisitionRunsControllerAndSignalCancellationStopsEverything(t *testing.T) {
	cancel, result, elector, _, runner, readiness := startCoordinator(t)
	callbacks := receiveCallbacks(t, elector)
	ctx, stopLeader := context.WithCancel(context.Background())
	defer stopLeader()
	go callbacks.OnStartedLeading(ctx)
	<-runner.started
	if !readiness.running.Load() {
		t.Fatal("leader did not become ready")
	}
	if !readiness.leader.Load() {
		t.Fatal("leader did not report leadership")
	}
	cancel()
	stopLeader()
	if err := receiveResult(t, result); err != nil {
		t.Fatalf("graceful cancellation returned %v", err)
	}
	<-runner.stopped
	if readiness.running.Load() || readiness.leader.Load() {
		t.Fatal("stopped leader remained ready")
	}
	if !readiness.shuttingDown.Load() {
		t.Fatal("shutdown did not withdraw readiness")
	}
}

func TestLeadershipLossFailsSafeAndCancelsController(t *testing.T) {
	_, result, elector, server, runner, readiness := startCoordinator(t)
	callbacks := receiveCallbacks(t, elector)
	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	go callbacks.OnStartedLeading(leaderCtx)
	<-runner.started
	cancelLeader()
	callbacks.OnStoppedLeading()
	if err := receiveResult(t, result); !errors.Is(err, ErrLeadershipLost) {
		t.Fatalf("result = %v, want ErrLeadershipLost", err)
	}
	<-runner.stopped
	if readiness.running.Load() {
		t.Fatal("leadership loss left readiness true")
	}
	if readiness.leader.Load() {
		t.Fatal("leadership loss left the leader flag set")
	}
	select {
	case <-server.stopped:
	default:
		t.Fatal("leadership loss did not shut down HTTP server")
	}
}

func TestUnexpectedHTTPFailureCancelsElectionWithDeterministicError(t *testing.T) {
	elector := &fakeElector{callbacks: make(chan LeaderCallbacks, 1)}
	server := newFakeServer()
	server.listenErr = errors.New("bind failed")
	runner := newFakeRunner()
	readiness := &fakeReadiness{}
	coordinator, err := NewCoordinator(elector, server, runner, readiness, Options{ShutdownTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	err = coordinator.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "HTTP server: bind failed") {
		t.Fatalf("result = %v", err)
	}
}

func TestKubernetesElectorUsesStableLeaseAndUniqueIdentity(t *testing.T) {
	t.Setenv("POD_NAME", "controller-7f8d")
	first, err := NewLeaderIdentity()
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewLeaderIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if first == second || !strings.HasPrefix(first, "controller-7f8d_") {
		t.Fatalf("identities = %q and %q", first, second)
	}
	elector, err := NewKubernetesElector(kubefake.NewSimpleClientset(), "lifecycle-system", DefaultLeaseName, first)
	if err != nil {
		t.Fatal(err)
	}
	if got := elector.config.Lock.Describe(); got != "lifecycle-system/"+DefaultLeaseName {
		t.Fatalf("lock = %q", got)
	}
	if elector.config.LeaseDuration != DefaultLeaseDuration || !elector.config.ReleaseOnCancel {
		t.Fatalf("unexpected election config: %#v", elector.config)
	}
}

func TestControllerFailureTerminatesRuntimeDeterministically(t *testing.T) {
	cancel, result, elector, _, runner, _ := startCoordinator(t)
	defer cancel()
	runner.err = errors.New("reconcile loop failed")
	callbacks := receiveCallbacks(t, elector)
	go callbacks.OnStartedLeading(context.Background())
	if err := receiveResult(t, result); err == nil || !strings.Contains(err.Error(), "controller run: reconcile loop failed") {
		t.Fatalf("result = %v", err)
	}
}

func TestNewCoordinatorRejectsMissingDependencies(t *testing.T) {
	if _, err := NewCoordinator(nil, newFakeServer(), newFakeRunner(), &fakeReadiness{}, Options{}); !errors.Is(err, ErrInvalidDependencies) {
		t.Fatalf("error = %v, want ErrInvalidDependencies", err)
	}
}
