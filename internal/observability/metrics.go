package observability

import (
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	"gitlab.glb.osl-nucleus.com/devops/kube-workload-lifecycle-manager/internal/controller"
	"gitlab.glb.osl-nucleus.com/devops/kube-workload-lifecycle-manager/internal/policy"
	"gitlab.glb.osl-nucleus.com/devops/kube-workload-lifecycle-manager/internal/state"
)

const metricNamespace = "workload_lifecycle_controller"

type Metrics struct {
	reconciliations *prometheus.CounterVec
	decisions       *prometheus.CounterVec
	errors          *prometheus.CounterVec
	policyReloads   *prometheus.CounterVec
	policyReady     prometheus.Gauge
	ready           prometheus.Gauge
	stateBytes      *prometheus.GaugeVec
	stateNearLimit  prometheus.Gauge
	stateExceeded   prometheus.Gauge
}

func NewMetrics(registerer prometheus.Registerer) *Metrics {
	metrics := &Metrics{
		reconciliations: prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: metricNamespace, Name: "reconciliations_total", Help: "Reconciliation outcomes by workload kind and result."}, []string{"kind", "result"}),
		decisions:       prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: metricNamespace, Name: "decisions_total", Help: "Controller decisions by workload kind and bounded reason."}, []string{"kind", "reason"}),
		errors:          prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: metricNamespace, Name: "errors_total", Help: "Controller errors by component and bounded reason."}, []string{"component", "reason"}),
		policyReloads:   prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: metricNamespace, Name: "policy_reloads_total", Help: "Policy reload events by bounded result."}, []string{"result"}),
		policyReady:     prometheus.NewGauge(prometheus.GaugeOpts{Namespace: metricNamespace, Name: "policy_ready", Help: "Whether an initial valid policy snapshot exists."}),
		ready:           prometheus.NewGauge(prometheus.GaugeOpts{Namespace: metricNamespace, Name: "ready", Help: "Whether the controller is ready to reconcile."}),
		stateBytes:      prometheus.NewGaugeVec(prometheus.GaugeOpts{Namespace: metricNamespace, Name: "state_store_bytes", Help: "StateStore payload and total ConfigMap data bytes."}, []string{"scope"}),
		stateNearLimit:  prometheus.NewGauge(prometheus.GaugeOpts{Namespace: metricNamespace, Name: "state_store_near_limit", Help: "Whether StateStore usage is at or above its warning threshold."}),
		stateExceeded:   prometheus.NewGauge(prometheus.GaugeOpts{Namespace: metricNamespace, Name: "state_store_exceeded", Help: "Whether StateStore usage exceeds its hard capacity."}),
	}
	registerer.MustRegister(metrics.reconciliations, metrics.decisions, metrics.errors, metrics.policyReloads,
		metrics.policyReady, metrics.ready, metrics.stateBytes, metrics.stateNearLimit, metrics.stateExceeded)
	return metrics
}

func (metrics *Metrics) ObserveReconcile(event controller.ReconcileObservation) {
	kind := string(event.Workload.Kind)
	metrics.reconciliations.WithLabelValues(kind, event.Result).Inc()
	metrics.decisions.WithLabelValues(kind, boundedReason(event.Reason)).Inc()
	if event.Err != nil {
		metrics.errors.WithLabelValues("reconcile", boundedReason(event.Reason)).Inc()
	}
}

func (metrics *Metrics) ObservePolicyEvent(event policy.Event) {
	result := string(event.Type)
	metrics.policyReloads.WithLabelValues(result).Inc()
	if event.Err != nil {
		metrics.errors.WithLabelValues("policy", result).Inc()
	}
}

func (metrics *Metrics) ObserveError(component string) {
	metrics.errors.WithLabelValues(component, "runtime-error").Inc()
}

func (metrics *Metrics) SetReadiness(policyReady, ready bool) {
	metrics.policyReady.Set(boolFloat(policyReady))
	metrics.ready.Set(boolFloat(ready))
}

func (metrics *Metrics) ObserveStateCapacity(capacity state.Capacity) {
	metrics.stateBytes.WithLabelValues("state").Set(float64(capacity.StateBytes))
	metrics.stateBytes.WithLabelValues("total").Set(float64(capacity.TotalBytes))
	metrics.stateNearLimit.Set(boolFloat(capacity.NearLimit))
	metrics.stateExceeded.Set(boolFloat(capacity.Exceeded))
}

func boundedReason(reason string) string {
	if strings.HasPrefix(reason, "scale-down-window") {
		return "scale-down-window"
	}
	switch reason {
	case "revision-lifecycle-expired", "restore-previous-replicas", "active-window",
		"no-policy-snapshot", "policy-conflict", "no-matching-policy", "transition-error":
		return reason
	default:
		return "other"
	}
}

func boolFloat(value bool) float64 {
	if value {
		return 1
	}
	return 0
}
