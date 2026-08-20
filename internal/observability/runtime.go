package observability

import (
	"log/slog"
	"sync/atomic"

	"gitlab.glb.osl-nucleus.com/devops/kube-workload-lifecycle-manager/internal/controller"
	"gitlab.glb.osl-nucleus.com/devops/kube-workload-lifecycle-manager/internal/policy"
)

type PolicyProvider interface {
	Current() (policy.PolicySet, bool)
}

type Readiness struct {
	policies     PolicyProvider
	controllerOn atomic.Bool
	dependencies atomic.Bool
	leader       atomic.Bool
	shuttingDown atomic.Bool
}

func NewReadiness(policies PolicyProvider) *Readiness {
	return &Readiness{policies: policies}
}

func (readiness *Readiness) SetDependenciesReady(ready bool) {
	readiness.dependencies.Store(ready)
}

func (readiness *Readiness) SetControllerRunning(running bool) {
	readiness.controllerOn.Store(running)
}

// SetLeader records whether this replica currently holds the leader Lease. It
// affects how Ready is evaluated but never blocks a standby replica.
func (readiness *Readiness) SetLeader(leader bool) {
	readiness.leader.Store(leader)
}

// SetShuttingDown marks the replica as draining so it stops advertising
// readiness before its listener closes.
func (readiness *Readiness) SetShuttingDown(down bool) {
	readiness.shuttingDown.Store(down)
}

func (readiness *Readiness) Leading() bool {
	return readiness.leader.Load()
}

func (readiness *Readiness) DependenciesReady() bool {
	return readiness.dependencies.Load()
}

func (readiness *Readiness) PolicyReady() bool {
	if readiness.policies == nil {
		return false
	}
	_, ok := readiness.policies.Current()
	return ok
}

// Ready reports whether this replica is healthy enough to serve probes and, for
// a leader-elected controller, whether a rolling update may proceed.
//
// Readiness deliberately does not require leadership. Gating it on leadership
// deadlocks rolling updates: an incoming replica cannot acquire the Lease until
// the outgoing leader's Pod terminates, but the Deployment will not terminate
// that Pod until the incoming replica reports Ready. A standby replica is Ready
// once its dependencies are usable so it can take over as soon as the Lease is
// released. Leadership is observable through the leader metric instead.
func (readiness *Readiness) Ready() bool {
	if readiness.shuttingDown.Load() {
		return false
	}
	if !readiness.dependencies.Load() || !readiness.PolicyReady() {
		return false
	}
	if readiness.leader.Load() {
		return readiness.controllerOn.Load()
	}
	return true
}

type Telemetry struct {
	logger    *slog.Logger
	metrics   *Metrics
	readiness *Readiness
}

func NewTelemetry(logger *slog.Logger, metrics *Metrics, readiness *Readiness) *Telemetry {
	return &Telemetry{logger: logger, metrics: metrics, readiness: readiness}
}
func (telemetry *Telemetry) ControllerStarted() {
	telemetry.readiness.SetControllerRunning(true)
	telemetry.refreshReadiness()
	telemetry.logger.Info("controller started")
}

func (telemetry *Telemetry) ControllerStopped() {
	telemetry.readiness.SetControllerRunning(false)
	telemetry.refreshReadiness()
	telemetry.logger.Info("controller stopped")
}

func (telemetry *Telemetry) ObserveReconcile(event controller.ReconcileObservation) {
	telemetry.metrics.ObserveReconcile(event)
	if event.Err == nil && event.Result == "success" {
		telemetry.readiness.SetDependenciesReady(true)
	}
	telemetry.refreshReadiness()
	attributes := []any{
		"kind", event.Workload.Kind,
		"namespace", event.Workload.Namespace,
		"workload", event.Workload.Name,
		"policy", event.Policy,
		"reason", event.Reason,
		"result", event.Result,
		"desired_replicas", event.Decision.DesiredReplicas,
		"should_scale", event.Decision.ShouldScale,
	}
	if event.Err != nil {
		telemetry.logger.Error("workload reconciliation failed", append(attributes, "error", event.Err)...)
		return
	}
	telemetry.logger.Info("workload reconciliation completed", attributes...)
}

func (telemetry *Telemetry) ObservePolicyEvent(event policy.Event) {
	telemetry.metrics.ObservePolicyEvent(event)
	telemetry.refreshReadiness()
	if event.Err != nil {
		telemetry.logger.Error("policy reload event", "event", event.Type, "error", event.Err)
		return
	}
	telemetry.logger.Info("policy reload event", "event", event.Type)
}

func (telemetry *Telemetry) ObserveError(component string, err error) {
	telemetry.metrics.ObserveError(component)
	telemetry.logger.Error("controller runtime error", "component", component, "error", err)
}

func (telemetry *Telemetry) refreshReadiness() {
	telemetry.metrics.SetReadiness(telemetry.readiness.PolicyReady(), telemetry.readiness.Ready())
	telemetry.metrics.SetLeader(telemetry.readiness.Leading())
}
