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

func (readiness *Readiness) PolicyReady() bool {
	if readiness.policies == nil {
		return false
	}
	_, ok := readiness.policies.Current()
	return ok
}

func (readiness *Readiness) Ready() bool {
	return readiness.controllerOn.Load() && readiness.dependencies.Load() && readiness.PolicyReady()
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
}
