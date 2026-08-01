package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/prometheus/client_golang/prometheus"
	"gitlab.glb.osl-nucleus.com/devops/kube-workload-lifecycle-manager/internal/controller"
	"gitlab.glb.osl-nucleus.com/devops/kube-workload-lifecycle-manager/internal/lifecycle"
	"gitlab.glb.osl-nucleus.com/devops/kube-workload-lifecycle-manager/internal/observability"
	"gitlab.glb.osl-nucleus.com/devops/kube-workload-lifecycle-manager/internal/policy"
	controllerruntime "gitlab.glb.osl-nucleus.com/devops/kube-workload-lifecycle-manager/internal/runtime"
	"gitlab.glb.osl-nucleus.com/devops/kube-workload-lifecycle-manager/internal/state"
	"gitlab.glb.osl-nucleus.com/devops/kube-workload-lifecycle-manager/internal/workload"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const (
	defaultNamespace       = "lifecycle-system"
	defaultPolicyConfigMap = "workload-lifecycle-policies"
	defaultStateConfigMap  = "workload-lifecycle-state"
	defaultHTTPAddress     = ":8080"
	defaultLeaseName       = controllerruntime.DefaultLeaseName
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	if err := run(ctx, logger); err != nil {
		logger.Error("controller terminated", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, logger *slog.Logger) error {
	config, err := rest.InClusterConfig()
	if err != nil {
		return fmt.Errorf("load in-cluster Kubernetes configuration: %w", err)
	}
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("create Kubernetes client: %w", err)
	}

	namespace := envOrDefault("POD_NAMESPACE", defaultNamespace)
	policyName := envOrDefault("POLICY_CONFIGMAP", defaultPolicyConfigMap)
	stateName := envOrDefault("STATE_CONFIGMAP", defaultStateConfigMap)
	manager := &policy.Manager{}
	registry := prometheus.NewRegistry()
	metrics := observability.NewMetrics(registry)
	readiness := observability.NewReadiness(manager)
	telemetry := observability.NewTelemetry(logger, metrics, readiness)
	configMaps := client.CoreV1().ConfigMaps(namespace)
	initialPolicy, policyErr := configMaps.Get(ctx, policyName, metav1.GetOptions{})
	if policyErr == nil {
		payload, ok := initialPolicy.Data[policy.DefaultConfigMapDataKey]
		if !ok {
			policyErr = fmt.Errorf("%w %q", policy.ErrMissingPolicyData, policy.DefaultConfigMapDataKey)
		} else {
			policyErr = manager.Reload([]byte(payload))
		}
	}
	if policyErr != nil {
		telemetry.ObservePolicyEvent(policy.Event{Type: policy.EventReloadRejected, Err: policyErr})
	} else {
		telemetry.ObservePolicyEvent(policy.Event{Type: policy.EventReloaded})
	}

	stateStore := state.NewConfigMapStateStore(configMaps, namespace, stateName)
	stateStore.SetCapacityObserver(metrics)
	if _, err := stateStore.Load(ctx); err != nil {
		telemetry.ObserveError("state-store", err)
	} else {
		readiness.SetDependenciesReady(true)
	}

	inventory := workload.NewKubernetesGateway(workload.NewKubernetesReadClient(client.AppsV1()))
	hpa := workload.NewKubernetesHPADetector(client.AutoscalingV2())
	scale := workload.NewKubernetesScaleGateway(client.AppsV1())
	reconciler, err := controller.NewReconciler(stateStore, scale, hpa, lifecycle.RealClock{})
	if err != nil {
		return err
	}
	policyWatcher := policy.NewConfigMapWatcher(configMaps, policyName, policy.DefaultConfigMapDataKey, manager)
	orchestrator, err := controller.NewController(inventory, hpa, manager, policyWatcher, reconciler, controller.ControllerOptions{
		Observer: telemetry,
		HandleError: func(err error) {
			telemetry.ObserveError("controller", err)
		},
	})
	if err != nil {
		return err
	}

	handler := observability.NewHTTPHandler(readiness, metrics, registry)
	server := &http.Server{Addr: envOrDefault("OBSERVABILITY_ADDR", defaultHTTPAddress), Handler: handler}
	identity, err := controllerruntime.NewLeaderIdentity()
	if err != nil {
		return err
	}
	leaseNamespace := envOrDefault("LEADER_ELECTION_NAMESPACE", namespace)
	leaseName := envOrDefault("LEADER_ELECTION_NAME", defaultLeaseName)
	elector, err := controllerruntime.NewKubernetesElector(client, leaseNamespace, leaseName, identity)
	if err != nil {
		return err
	}
	runtime, err := controllerruntime.NewCoordinator(elector, server, orchestrator, readiness, controllerruntime.Options{
		OnNewLeader: func(leader string) {
			logger.Info("leader election update", "leader", leader, "identity", identity, "lease_namespace", leaseNamespace, "lease_name", leaseName)
		},
	})
	if err != nil {
		return err
	}
	return runtime.Run(ctx)
}

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
