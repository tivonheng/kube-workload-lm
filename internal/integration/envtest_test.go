package integration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"gitlab.glb.osl-nucleus.com/devops/kube-workload-lifecycle-manager/internal/controller"
	"gitlab.glb.osl-nucleus.com/devops/kube-workload-lifecycle-manager/internal/lifecycle"
	"gitlab.glb.osl-nucleus.com/devops/kube-workload-lifecycle-manager/internal/policy"
	"gitlab.glb.osl-nucleus.com/devops/kube-workload-lifecycle-manager/internal/state"
	"gitlab.glb.osl-nucleus.com/devops/kube-workload-lifecycle-manager/internal/workload"
)

const envtestPolicy = `apiVersion: lifecycle.example.com/v1alpha1
kind: WorkloadLifecyclePolicySet
spec:
  policies:
    - name: envtest
      priority: 100
      target:
        kinds: [Deployment, StatefulSet]
        selector:
          matchLabels: {lifecycle.example.com/envtest: "true"}
      lifecycle:
        maxAge: 72h
        revision: {source: ContainerImages}
      replicas: {scheduledDown: 0, expired: 0}
`
const envtestDownPolicy = envtestPolicy + `      schedule:
        timeZone: UTC
        downWindows:
          - name: always
            startDays: [MON, TUE, WED, THU, FRI, SAT, SUN]
            allDay: true
`

type fixedClock struct{ now time.Time }

func (clock fixedClock) Now() time.Time { return clock.now }

func TestEnvtestKubernetesIntegration(t *testing.T) {
	assets := envtestAssets(t)
	testEnvironment := &envtest.Environment{
		BinaryAssetsDirectory:    assets,
		ControlPlaneStartTimeout: 30 * time.Second,
		ControlPlaneStopTimeout:  30 * time.Second,
	}
	config, err := testEnvironment.Start()
	if err != nil {
		t.Fatalf("start envtest control plane with assets %q: %v", assets, err)
	}
	t.Cleanup(func() {
		if err := testEnvironment.Stop(); err != nil {
			t.Errorf("stop envtest control plane: %v", err)
		}
	})
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		t.Fatalf("create envtest client: %v", err)
	}
	ctx := context.Background()
	namespace := "lifecycle-envtest"
	if _, err := client.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: namespace},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create envtest namespace: %v", err)
	}

	t.Run("real inventory and scale subresources", func(t *testing.T) {
		testInventoryAndScale(t, ctx, client, namespace)
	})
	t.Run("HPA rejection precedes durable mutation", func(t *testing.T) {
		testHPARejection(t, ctx, client, namespace)
	})
	t.Run("ConfigMap policy watch and state persistence", func(t *testing.T) {
		testConfigMaps(t, ctx, client, namespace)
	})
	t.Run("watch reconciliation and restart restoration", func(t *testing.T) {
		testWatchAndRestart(t, ctx, client, namespace)
	})
	t.Run("lost snapshot is never guessed", func(t *testing.T) {
		testLostSnapshot(t, ctx, client, namespace)
	})
}
func envtestAssets(t *testing.T) string {
	t.Helper()
	if configured, explicit := os.LookupEnv("KUBEBUILDER_ASSETS"); explicit {
		if err := validateEnvtestAssets(configured); err != nil {
			t.Fatalf("KUBEBUILDER_ASSETS is configured but invalid: %v", err)
		}
		return configured
	}
	defaultPath := filepath.Join("/usr", "local", "kubebuilder", "bin")
	if err := validateEnvtestAssets(defaultPath); err != nil {
		t.Skipf("envtest binaries unavailable: %v; set KUBEBUILDER_ASSETS or run make test-envtest", err)
	}
	return defaultPath
}

func validateEnvtestAssets(directory string) error {
	if directory == "" {
		return errors.New("asset directory is empty")
	}
	for _, binary := range []string{"kube-apiserver", "etcd", "kubectl"} {
		path := filepath.Join(directory, binary)
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("required binary %s: %w", path, err)
		}
		if info.IsDir() || info.Mode()&0o111 == 0 {
			return fmt.Errorf("required binary %s is not executable", path)
		}
	}
	return nil
}

func testInventoryAndScale(t *testing.T, ctx context.Context, client kubernetes.Interface, namespace string) {
	deployment := newDeployment(namespace, "inventory-deployment", 5, "app:v1")
	statefulSet := newStatefulSet(namespace, "inventory-statefulset", 4, "db:v1")
	if _, err := client.AppsV1().Deployments(namespace).Create(ctx, deployment, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.AppsV1().StatefulSets(namespace).Create(ctx, statefulSet, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	gateway := workload.NewKubernetesGateway(workload.NewKubernetesReadClient(client.AppsV1()))
	deployments, err := gateway.List(ctx, workload.KindDeployment)
	if err != nil {
		t.Fatal(err)
	}
	statefulSets, err := gateway.List(ctx, workload.KindStatefulSet)
	if err != nil {
		t.Fatal(err)
	}
	assertInventoryContains(t, deployments, namespace, deployment.Name, "app:v1")
	assertInventoryContains(t, statefulSets, namespace, statefulSet.Name, "db:v1")

	scale := workload.NewKubernetesScaleGateway(client.AppsV1())
	for _, test := range []struct {
		current workload.Workload
		want    int32
	}{
		{workload.Workload{Kind: workload.KindDeployment, Namespace: namespace, Name: deployment.Name}, 2},
		{workload.Workload{Kind: workload.KindStatefulSet, Namespace: namespace, Name: statefulSet.Name}, 1},
	} {
		if err := scale.Update(ctx, test.current, test.want); err != nil {
			t.Fatalf("update %s scale: %v", test.current.Kind, err)
		}
		live, err := scale.Read(ctx, test.current)
		if err != nil || live.CurrentReplicas != test.want {
			t.Fatalf("read %s scale: replicas=%d err=%v", test.current.Kind, live.CurrentReplicas, err)
		}
	}
	gotDeployment, err := client.AppsV1().Deployments(namespace).Get(ctx, deployment.Name, metav1.GetOptions{})
	if err != nil || gotDeployment.Spec.Template.Spec.Containers[0].Image != "app:v1" {
		t.Fatalf("scale update changed Deployment template: image=%q err=%v", gotDeployment.Spec.Template.Spec.Containers[0].Image, err)
	}
	gotStatefulSet, err := client.AppsV1().StatefulSets(namespace).Get(ctx, statefulSet.Name, metav1.GetOptions{})
	if err != nil || gotStatefulSet.Spec.Template.Spec.Containers[0].Image != "db:v1" {
		t.Fatalf("scale update changed StatefulSet template: image=%q err=%v", gotStatefulSet.Spec.Template.Spec.Containers[0].Image, err)
	}
	statefulStore := state.NewConfigMapStateStore(client.CoreV1().ConfigMaps(namespace), namespace, "stateful-restore-state")
	statefulReconciler := newReconciler(t, statefulStore, client, time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC))
	if _, err := statefulReconciler.Reconcile(ctx, workload.WorkloadFromStatefulSet(gotStatefulSet), selectedPolicy(t, envtestDownPolicy)); err != nil {
		t.Fatalf("downscale StatefulSet: %v", err)
	}
	assertScale(t, ctx, client, workload.KindStatefulSet, namespace, statefulSet.Name, 0)
	latestStatefulSet, err := client.AppsV1().StatefulSets(namespace).Get(ctx, statefulSet.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := statefulReconciler.Reconcile(ctx, workload.WorkloadFromStatefulSet(latestStatefulSet), selectedPolicy(t, envtestPolicy)); err != nil {
		t.Fatalf("restore StatefulSet: %v", err)
	}
	assertScale(t, ctx, client, workload.KindStatefulSet, namespace, statefulSet.Name, 1)
}

func testHPARejection(t *testing.T, ctx context.Context, client kubernetes.Interface, namespace string) {
	created, err := client.AppsV1().Deployments(namespace).Create(ctx,
		newDeployment(namespace, "hpa-deployment", 3, "app:v1"), metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	minReplicas := int32(1)
	_, err = client.AutoscalingV2().HorizontalPodAutoscalers(namespace).Create(ctx, &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Name: "hpa-deployment", Namespace: namespace},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{APIVersion: "apps/v1", Kind: "Deployment", Name: created.Name},
			MinReplicas:    &minReplicas, MaxReplicas: 10,
		},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	store := state.NewConfigMapStateStore(client.CoreV1().ConfigMaps(namespace), namespace, "hpa-state")
	reconciler := newReconciler(t, store, client, time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC))
	_, err = reconciler.Reconcile(ctx, workload.WorkloadFromDeployment(created), selectedPolicy(t, envtestDownPolicy))
	if !errors.Is(err, workload.ErrHPAConflict) {
		t.Fatalf("reconcile error = %v, want HPA conflict", err)
	}
	document, loadErr := store.Load(ctx)
	if loadErr != nil || len(document.States) != 0 {
		t.Fatalf("HPA rejection mutated state: states=%d err=%v", len(document.States), loadErr)
	}
	assertScale(t, ctx, client, workload.KindDeployment, namespace, created.Name, 3)
}
func testConfigMaps(t *testing.T, ctx context.Context, client kubernetes.Interface, namespace string) {
	manager := &policy.Manager{}
	watcher := policy.NewConfigMapWatcher(client.CoreV1().ConfigMaps(namespace), "watched-policy", policy.DefaultConfigMapDataKey, manager)
	stream, err := watcher.Watch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Stop()
	configMap, err := client.CoreV1().ConfigMaps(namespace).Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "watched-policy", Namespace: namespace},
		Data:       map[string]string{policy.DefaultConfigMapDataKey: envtestPolicy},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if event := receivePolicyEvent(t, stream.ResultChan()); event.Type != policy.EventReloaded || event.Err != nil {
		t.Fatalf("valid policy event = %#v", event)
	}
	configMap.Data[policy.DefaultConfigMapDataKey] = "invalid: true"
	if _, err := client.CoreV1().ConfigMaps(namespace).Update(ctx, configMap, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if event := receivePolicyEvent(t, stream.ResultChan()); event.Type != policy.EventReloadRejected || event.Err == nil {
		t.Fatalf("invalid policy event = %#v", event)
	}
	current, ok := manager.Current()
	if !ok || len(current.Policies) != 1 || current.Policies[0].Name != "envtest" {
		t.Fatalf("invalid update replaced last-good policy: %#v", current)
	}

	firstStore := state.NewConfigMapStateStore(client.CoreV1().ConfigMaps(namespace), namespace, "persistent-state")
	document, err := firstStore.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := firstStore.Save(ctx, document); err != nil {
		t.Fatal(err)
	}
	secondStore := state.NewConfigMapStateStore(client.CoreV1().ConfigMaps(namespace), namespace, "persistent-state")
	reloaded, err := secondStore.Load(ctx)
	if err != nil || reloaded.FormatVersion != state.FormatVersion || reloaded.States == nil {
		t.Fatalf("state did not survive store recreation: %#v err=%v", reloaded, err)
	}
}

func testWatchAndRestart(t *testing.T, ctx context.Context, client kubernetes.Interface, namespace string) {
	created, err := client.AppsV1().Deployments(namespace).Create(ctx,
		newDeployment(namespace, "watched-restart", 3, "app:v1"), metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	policyConfigMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "controller-policy", Namespace: namespace},
		Data:       map[string]string{policy.DefaultConfigMapDataKey: envtestDownPolicy},
	}
	if _, err := client.CoreV1().ConfigMaps(namespace).Create(ctx, policyConfigMap, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	manager := &policy.Manager{}
	if err := manager.Reload([]byte(envtestDownPolicy)); err != nil {
		t.Fatal(err)
	}
	store := state.NewConfigMapStateStore(client.CoreV1().ConfigMaps(namespace), namespace, "restart-state")
	reconciler := newReconciler(t, store, client, now)
	inventory := workload.NewKubernetesGateway(workload.NewKubernetesReadClient(client.AppsV1()))
	hpa := workload.NewKubernetesHPADetector(client.AutoscalingV2())
	policyWatcher := policy.NewConfigMapWatcher(client.CoreV1().ConfigMaps(namespace), policyConfigMap.Name, policy.DefaultConfigMapDataKey, manager)
	runner, err := controller.NewController(inventory, hpa, manager, policyWatcher, reconciler, controller.ControllerOptions{
		ReconcileInterval: time.Hour,
		WatchRetryDelay:   10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- runner.Run(runCtx) }()
	waitFor(t, "initial event-driven downscale", func() bool {
		return scaleEquals(ctx, client, workload.KindDeployment, namespace, created.Name, 0)
	})
	document, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	before := document.States["Deployment/"+namespace+"/"+created.Name]
	if before.ReplicaSnapshot == nil || before.ReplicaSnapshot.Replicas != 3 {
		t.Fatalf("downscale snapshot = %#v", before.ReplicaSnapshot)
	}

	live, err := client.AppsV1().Deployments(namespace).Get(ctx, created.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	live.Spec.Template.Spec.Containers[0].Image = "app:v2"
	if _, err := client.AppsV1().Deployments(namespace).Update(ctx, live, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "Deployment watch reconciliation", func() bool {
		latest, loadErr := store.Load(ctx)
		return loadErr == nil && latest.States[before.Key()].RevisionHash != before.RevisionHash
	})
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("stop first controller: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first controller did not stop")
	}

	latest, err := client.AppsV1().Deployments(namespace).Get(ctx, created.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	restarted := newReconciler(t,
		state.NewConfigMapStateStore(client.CoreV1().ConfigMaps(namespace), namespace, "restart-state"), client, now)
	decision, err := restarted.Reconcile(ctx, workload.WorkloadFromDeployment(latest), selectedPolicy(t, envtestPolicy))
	if err != nil || decision.Reason != lifecycle.ReasonRestoreReplicas {
		t.Fatalf("restart restore decision = %#v err=%v", decision, err)
	}
	assertScale(t, ctx, client, workload.KindDeployment, namespace, created.Name, 3)
	restored, err := store.Load(ctx)
	if err != nil || restored.States[before.Key()].ReplicaSnapshot != nil {
		t.Fatalf("restart did not clear snapshot after scale: state=%#v err=%v", restored.States[before.Key()], err)
	}
}
func testLostSnapshot(t *testing.T, ctx context.Context, client kubernetes.Interface, namespace string) {
	created, err := client.AppsV1().Deployments(namespace).Create(ctx,
		newDeployment(namespace, "lost-snapshot", 4, "app:v1"), metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	store := state.NewConfigMapStateStore(client.CoreV1().ConfigMaps(namespace), namespace, "lost-state")
	reconciler := newReconciler(t, store, client, now)
	if _, err := reconciler.Reconcile(ctx, workload.WorkloadFromDeployment(created), selectedPolicy(t, envtestDownPolicy)); err != nil {
		t.Fatal(err)
	}
	assertScale(t, ctx, client, workload.KindDeployment, namespace, created.Name, 0)
	if err := client.CoreV1().ConfigMaps(namespace).Delete(ctx, "lost-state", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	latest, err := client.AppsV1().Deployments(namespace).Get(ctx, created.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	restarted := newReconciler(t,
		state.NewConfigMapStateStore(client.CoreV1().ConfigMaps(namespace), namespace, "lost-state"), client, now)
	decision, err := restarted.Reconcile(ctx, workload.WorkloadFromDeployment(latest), selectedPolicy(t, envtestPolicy))
	if err != nil {
		t.Fatal(err)
	}
	if decision.ShouldScale || decision.DesiredReplicas != 0 || decision.Reason != lifecycle.ReasonActiveWindow {
		t.Fatalf("lost snapshot caused guessed restore: %#v", decision)
	}
	assertScale(t, ctx, client, workload.KindDeployment, namespace, created.Name, 0)
}

func newReconciler(t *testing.T, store state.StateStore, client kubernetes.Interface, now time.Time) *controller.Reconciler {
	t.Helper()
	reconciler, err := controller.NewReconciler(
		store,
		workload.NewKubernetesScaleGateway(client.AppsV1()),
		workload.NewKubernetesHPADetector(client.AutoscalingV2()),
		fixedClock{now: now},
	)
	if err != nil {
		t.Fatal(err)
	}
	return reconciler
}

func selectedPolicy(t *testing.T, document string) policy.Policy {
	t.Helper()
	set, err := policy.Decode([]byte(document))
	if err != nil {
		t.Fatal(err)
	}
	return set.Policies[0]
}
func newDeployment(namespace, name string, replicas int32, image string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: namespace,
			Labels: map[string]string{"lifecycle.example.com/envtest": "true"},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: image}}},
			},
		},
	}
}

func newStatefulSet(namespace, name string, replicas int32, image string) *appsv1.StatefulSet {
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: namespace,
			Labels: map[string]string{"lifecycle.example.com/envtest": "true"},
		},
		Spec: appsv1.StatefulSetSpec{
			ServiceName: name,
			Replicas:    &replicas,
			Selector:    &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: image}}},
			},
		},
	}
}

func assertInventoryContains(t *testing.T, items []workload.Workload, namespace, name, image string) {
	t.Helper()
	for _, item := range items {
		if item.Namespace == namespace && item.Name == name {
			if len(item.Containers) != 1 || item.Containers[0].Image != image || item.UID == "" {
				t.Fatalf("inventory item %s/%s = %#v", namespace, name, item)
			}
			return
		}
	}
	t.Fatalf("inventory does not contain %s/%s: %#v", namespace, name, items)
}
func assertScale(t *testing.T, ctx context.Context, client kubernetes.Interface, kind workload.Kind, namespace, name string, want int32) {
	t.Helper()
	if !scaleEquals(ctx, client, kind, namespace, name, want) {
		gateway := workload.NewKubernetesScaleGateway(client.AppsV1())
		live, err := gateway.Read(ctx, workload.Workload{Kind: kind, Namespace: namespace, Name: name})
		t.Fatalf("%s %s/%s replicas=%d err=%v, want %d", kind, namespace, name, live.CurrentReplicas, err, want)
	}
}

func scaleEquals(ctx context.Context, client kubernetes.Interface, kind workload.Kind, namespace, name string, want int32) bool {
	live, err := workload.NewKubernetesScaleGateway(client.AppsV1()).Read(ctx, workload.Workload{
		Kind: kind, Namespace: namespace, Name: name,
	})
	return err == nil && live.CurrentReplicas == want
}

func waitFor(t *testing.T, description string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", description)
}

func receivePolicyEvent(t *testing.T, events <-chan policy.Event) policy.Event {
	t.Helper()
	select {
	case event, ok := <-events:
		if !ok {
			t.Fatal("policy watch closed before expected event")
		}
		return event
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for policy watch event")
		return policy.Event{}
	}
}
