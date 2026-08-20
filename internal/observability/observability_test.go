package observability

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"gitlab.glb.osl-nucleus.com/devops/kube-workload-lifecycle-manager/internal/controller"
	"gitlab.glb.osl-nucleus.com/devops/kube-workload-lifecycle-manager/internal/lifecycle"
	"gitlab.glb.osl-nucleus.com/devops/kube-workload-lifecycle-manager/internal/policy"
	"gitlab.glb.osl-nucleus.com/devops/kube-workload-lifecycle-manager/internal/workload"
)

const validPolicy = `apiVersion: lifecycle.example.com/v1alpha1
kind: WorkloadLifecyclePolicySet
spec:
  policies:
    - name: managed
      priority: 10
      target:
        kinds: [Deployment]
        selector: {matchLabels: {managed: "true"}}
      lifecycle:
        maxAge: 24h
        revision: {source: ContainerImages}
      replicas: {scheduledDown: 0, expired: 0}
`

func TestTelemetryWritesStructuredJSONWithoutSensitiveWorkloadData(t *testing.T) {
	var output bytes.Buffer
	registry := prometheus.NewRegistry()
	manager := &policy.Manager{}
	telemetry := NewTelemetry(slog.New(slog.NewJSONHandler(&output, nil)), NewMetrics(registry), NewReadiness(manager))
	event := controller.ReconcileObservation{
		Workload: workload.Workload{Kind: workload.KindDeployment, Namespace: "team-a", Name: "api", UID: "sensitive-uid", Containers: []workload.Container{{Name: "app", Image: "registry/secret:v1"}}},
		Policy:   "managed", Reason: lifecycle.ReasonRevisionExpired, Result: "error", Err: errors.New("scale conflict"),
	}
	telemetry.ObserveReconcile(event)
	var record map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &record); err != nil {
		t.Fatalf("log is not JSON: %v: %s", err, output.String())
	}
	for key, want := range map[string]any{"kind": "Deployment", "namespace": "team-a", "workload": "api", "policy": "managed", "reason": lifecycle.ReasonRevisionExpired, "result": "error"} {
		if record[key] != want {
			t.Fatalf("field %s = %#v, want %#v", key, record[key], want)
		}
	}
	encoded := output.String()
	if strings.Contains(encoded, "sensitive-uid") || strings.Contains(encoded, "registry/secret") {
		t.Fatalf("log leaked UID or image: %s", encoded)
	}
}

func TestMetricLabelsRemainLowCardinality(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := NewMetrics(registry)
	for index, name := range []string{"api-123", "api-456"} {
		metrics.ObserveReconcile(controller.ReconcileObservation{
			Workload: workload.Workload{Kind: workload.KindDeployment, Namespace: "tenant", Name: name, UID: "uid-" + name},
			Reason:   "scale-down-window:dynamic-" + name, Result: "success",
			Decision: lifecycle.Decision{Reason: "scale-down-window:dynamic-" + name},
		})
		_ = index
	}
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		for _, metric := range family.Metric {
			for _, label := range metric.Label {
				if label.GetName() == "workload" || label.GetName() == "namespace" || label.GetName() == "uid" || strings.Contains(label.GetValue(), "api-") || strings.Contains(label.GetValue(), "dynamic-") {
					t.Fatalf("high-cardinality label in %s: %s=%s", family.GetName(), label.GetName(), label.GetValue())
				}
			}
		}
	}
	decision := metricFamily(t, families, metricNamespace+"_decisions_total")
	if len(decision.Metric) != 1 || decision.Metric[0].Counter.GetValue() != 2 {
		t.Fatalf("decision series = %#v, want one normalized series with value 2", decision.Metric)
	}
}

func TestHealthAndReadinessTransitionsKeepLastGoodPolicy(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := NewMetrics(registry)
	manager := &policy.Manager{}
	readiness := NewReadiness(manager)
	readiness.SetDependenciesReady(true)
	readiness.SetLeader(true)
	readiness.SetControllerRunning(true)
	handler := NewHTTPHandler(readiness, metrics, registry)
	assertStatus(t, handler, "/healthz", http.StatusOK)
	assertStatus(t, handler, "/readyz", http.StatusServiceUnavailable)
	if err := manager.Reload([]byte(validPolicy)); err != nil {
		t.Fatal(err)
	}
	assertStatus(t, handler, "/readyz", http.StatusOK)
	if err := manager.Reload([]byte("invalid: true")); err == nil {
		t.Fatal("invalid hot reload was accepted")
	}
	assertStatus(t, handler, "/readyz", http.StatusOK)
	readiness.SetControllerRunning(false)
	assertStatus(t, handler, "/readyz", http.StatusServiceUnavailable)
	assertStatus(t, handler, "/healthz", http.StatusOK)
}

// TestStandbyReplicaIsReadySoRollingUpdatesCannotDeadlock guards the rollout
// deadlock: when readiness required leadership, an incoming replica could never
// become Ready (it cannot hold the Lease while the outgoing leader still runs),
// so the Deployment never retired the old leader and the rollout stalled.
func TestStandbyReplicaIsReadySoRollingUpdatesCannotDeadlock(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := NewMetrics(registry)
	manager := &policy.Manager{}
	if err := manager.Reload([]byte(validPolicy)); err != nil {
		t.Fatal(err)
	}
	readiness := NewReadiness(manager)
	readiness.SetDependenciesReady(true)
	handler := NewHTTPHandler(readiness, metrics, registry)

	// Standby: never acquired the Lease, controller loop not running.
	if readiness.Leading() {
		t.Fatal("fresh replica reported leadership")
	}
	assertStatus(t, handler, "/readyz", http.StatusOK)
	assertBodyContains(t, handler, "/readyz", `"role":"standby"`)
	assertGauge(t, registry, metricNamespace+"_leader", 0)

	// Promotion to leader keeps it ready and flips the leader gauge.
	readiness.SetLeader(true)
	readiness.SetControllerRunning(true)
	assertStatus(t, handler, "/readyz", http.StatusOK)
	assertBodyContains(t, handler, "/readyz", `"role":"leader"`)
	assertGauge(t, registry, metricNamespace+"_leader", 1)

	// A leader whose controller loop died must fail readiness.
	readiness.SetControllerRunning(false)
	assertStatus(t, handler, "/readyz", http.StatusServiceUnavailable)

	// Demotion back to standby is ready again, ready to take over.
	readiness.SetLeader(false)
	assertStatus(t, handler, "/readyz", http.StatusOK)

	// Draining withdraws readiness regardless of role.
	readiness.SetShuttingDown(true)
	assertStatus(t, handler, "/readyz", http.StatusServiceUnavailable)
}

func TestStandbyNotReadyUntilDependenciesAndPolicyResolve(t *testing.T) {
	registry := prometheus.NewRegistry()
	manager := &policy.Manager{}
	readiness := NewReadiness(manager)
	handler := NewHTTPHandler(readiness, NewMetrics(registry), registry)

	// No policy snapshot and no dependencies yet.
	assertStatus(t, handler, "/readyz", http.StatusServiceUnavailable)
	readiness.SetDependenciesReady(true)
	assertStatus(t, handler, "/readyz", http.StatusServiceUnavailable)
	if err := manager.Reload([]byte(validPolicy)); err != nil {
		t.Fatal(err)
	}
	assertStatus(t, handler, "/readyz", http.StatusOK)
}

func TestExpiredDeleteReasonStaysASeparateBoundedSeries(t *testing.T) {
	for reason, want := range map[string]string{
		lifecycle.ReasonRevisionExpired:        "revision-lifecycle-expired",
		lifecycle.ReasonRevisionExpiredDeleted: "revision-lifecycle-expired:deleted",
		"scale-down-window:night":              "scale-down-window",
		"scale-down-skipped:redeploy":          "scale-down-skipped",
		"scale-down-skipped:replica-override":  "scale-down-skipped",
		"something-unexpected":                 "other",
	} {
		if got := boundedReason(reason); got != want {
			t.Fatalf("boundedReason(%q) = %q, want %q", reason, got, want)
		}
	}
}

func assertBodyContains(t *testing.T, handler http.Handler, path, want string) {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if !strings.Contains(response.Body.String(), want) {
		t.Fatalf("%s body = %s, want it to contain %s", path, response.Body.String(), want)
	}
}

func assertGauge(t *testing.T, registry *prometheus.Registry, name string, want float64) {
	t.Helper()
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	family := metricFamily(t, families, name)
	if len(family.Metric) != 1 || family.Metric[0].Gauge.GetValue() != want {
		t.Fatalf("%s = %#v, want %v", name, family.Metric, want)
	}
}
func assertStatus(t *testing.T, handler http.Handler, path string, want int) {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != want {
		t.Fatalf("%s status = %d, want %d; body=%s", path, response.Code, want, response.Body.String())
	}
}

func metricFamily(t *testing.T, families []*dto.MetricFamily, name string) *dto.MetricFamily {
	t.Helper()
	for _, family := range families {
		if family.GetName() == name {
			return family
		}
	}
	t.Fatalf("metric family %s not found", name)
	return nil
}
