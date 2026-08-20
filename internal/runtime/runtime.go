package runtime

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"
)

const DefaultShutdownTimeout = 10 * time.Second

var (
	ErrInvalidDependencies = errors.New("runtime dependencies must be configured")
	ErrLeadershipLost      = errors.New("leader election leadership lost")
	ErrElectionStopped     = errors.New("leader election stopped unexpectedly")
	ErrControllerStopped   = errors.New("controller stopped unexpectedly")
	ErrHTTPServerStopped   = errors.New("HTTP server stopped unexpectedly")
	ErrShutdownTimeout     = errors.New("graceful shutdown timed out")
)

type Runner interface {
	Run(context.Context) error
}

type HTTPServer interface {
	ListenAndServe() error
	Shutdown(context.Context) error
}

type Readiness interface {
	SetControllerRunning(bool)
	SetLeader(bool)
	SetShuttingDown(bool)
}

type LeaderCallbacks struct {
	OnStartedLeading func(context.Context)
	OnStoppedLeading func()
	OnNewLeader      func(string)
}

type LeaderElector interface {
	Run(context.Context, LeaderCallbacks) error
}

type Options struct {
	ShutdownTimeout time.Duration
	OnNewLeader     func(string)
}

type Coordinator struct {
	elector         LeaderElector
	server          HTTPServer
	runner          Runner
	readiness       Readiness
	shutdownTimeout time.Duration
	onNewLeader     func(string)
}

func NewCoordinator(elector LeaderElector, server HTTPServer, runner Runner, readiness Readiness, options Options) (*Coordinator, error) {
	if elector == nil || server == nil || runner == nil || readiness == nil {
		return nil, ErrInvalidDependencies
	}
	timeout := options.ShutdownTimeout
	if timeout <= 0 {
		timeout = DefaultShutdownTimeout
	}
	onNewLeader := options.OnNewLeader
	if onNewLeader == nil {
		onNewLeader = func(string) {}
	}
	return &Coordinator{
		elector: elector, server: server, runner: runner, readiness: readiness,
		shutdownTimeout: timeout, onNewLeader: onNewLeader,
	}, nil
}

// Run serves health endpoints on every replica while leader election gates the
// mutation-capable controller. It does not return until all started components
// have stopped or the bounded shutdown deadline expires.
func (coordinator *Coordinator) Run(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(ctx)
	coordinator.readiness.SetShuttingDown(false)
	coordinator.readiness.SetLeader(false)
	coordinator.readiness.SetControllerRunning(false)
	serverResult := make(chan error, 1)
	electionResult := make(chan error, 1)
	controllerResult := make(chan error, 1)
	leadershipLost := make(chan struct{}, 1)
	var leaderStarted atomic.Bool
	var lost atomic.Bool
	var shuttingDown atomic.Bool

	go func() { serverResult <- coordinator.server.ListenAndServe() }()
	callbacks := LeaderCallbacks{
		OnStartedLeading: func(leaderCtx context.Context) {
			if runCtx.Err() != nil {
				return
			}
			leaderStarted.Store(true)
			coordinator.readiness.SetLeader(true)
			coordinator.readiness.SetControllerRunning(true)
			err := coordinator.runner.Run(leaderCtx)
			coordinator.readiness.SetControllerRunning(false)
			coordinator.readiness.SetLeader(false)
			if leaderCtx.Err() != nil && runCtx.Err() == nil && !shuttingDown.Load() {
				lost.Store(true)
				select {
				case leadershipLost <- struct{}{}:
				default:
				}
			}
			controllerResult <- err
		},
		OnStoppedLeading: func() {
			coordinator.readiness.SetControllerRunning(false)
			coordinator.readiness.SetLeader(false)
			if ctx.Err() == nil && !shuttingDown.Load() {
				lost.Store(true)
				select {
				case leadershipLost <- struct{}{}:
				default:
				}
			}
		},
		OnNewLeader: coordinator.onNewLeader,
	}
	go func() { electionResult <- coordinator.elector.Run(runCtx, callbacks) }()

	var result error
	serverDone, electionDone, controllerDone := false, false, false
	select {
	case <-ctx.Done():
		result = nil
	case <-leadershipLost:
		result = ErrLeadershipLost
	case err := <-controllerResult:
		controllerDone = true
		if ctx.Err() == nil {
			if lost.Load() {
				result = ErrLeadershipLost
			} else if err != nil {
				result = fmt.Errorf("controller run: %w", err)
			} else {
				result = ErrControllerStopped
			}
		}
	case err := <-electionResult:
		electionDone = true
		if ctx.Err() == nil {
			if lost.Load() || errors.Is(err, ErrLeadershipLost) {
				result = ErrLeadershipLost
			} else if err != nil {
				result = fmt.Errorf("leader election: %w", err)
			} else {
				result = ErrElectionStopped
			}
		}
	case err := <-serverResult:
		serverDone = true
		if ctx.Err() == nil {
			if err == nil || errors.Is(err, http.ErrServerClosed) {
				result = ErrHTTPServerStopped
			} else {
				result = fmt.Errorf("HTTP server: %w", err)
			}
		}
	}

	shuttingDown.Store(true)
	cancel()
	// Stop advertising readiness before the listener closes so the endpoint is
	// withdrawn while the process is still able to answer probes.
	coordinator.readiness.SetShuttingDown(true)
	coordinator.readiness.SetControllerRunning(false)
	coordinator.readiness.SetLeader(false)
	shutdownCtx, shutdownCancel := context.WithTimeout(context.WithoutCancel(ctx), coordinator.shutdownTimeout)
	defer shutdownCancel()
	shutdownResult := make(chan error, 1)
	go func() { shutdownResult <- coordinator.server.Shutdown(shutdownCtx) }()

	if !electionDone && !waitResult(shutdownCtx, electionResult) {
		return preferError(result, ErrShutdownTimeout)
	}
	if leaderStarted.Load() && !controllerDone && !waitResult(shutdownCtx, controllerResult) {
		return preferError(result, ErrShutdownTimeout)
	}
	if !serverDone && !waitResult(shutdownCtx, serverResult) {
		return preferError(result, ErrShutdownTimeout)
	}
	select {
	case err := <-shutdownResult:
		if err != nil && !errors.Is(err, context.Canceled) {
			return preferError(result, fmt.Errorf("shutdown HTTP server: %w", err))
		}
	case <-shutdownCtx.Done():
		return preferError(result, ErrShutdownTimeout)
	}
	return result
}

func waitResult[T any](ctx context.Context, result <-chan T) bool {
	select {
	case <-result:
		return true
	case <-ctx.Done():
		return false
	}
}

func preferError(primary, shutdown error) error {
	if primary != nil {
		return errors.Join(primary, shutdown)
	}
	return shutdown
}
