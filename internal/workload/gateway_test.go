package workload

import (
	"context"
	"errors"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	kubefake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestWorkloadConversionReadsOnlyAllowedFields(t *testing.T) {
	replicas := int32(7)
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "api", Namespace: "team-a", UID: types.UID("deployment-uid"),
			Labels: map[string]string{"app": "api"}, Annotations: map[string]string{"ignored": "value"},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				InitContainers: []corev1.Container{{Name: "migration", Image: "migration:v1"}},
				Containers: []corev1.Container{
					{Name: "sidecar", Image: "sidecar:v2", Env: []corev1.EnvVar{{Name: "IGNORED", Value: "yes"}}},
					{Name: "app", Image: "app:v3"},
				},
			}},
		},
		Status: appsv1.DeploymentStatus{Replicas: 9},
	}

	converted := WorkloadFromDeployment(deployment)
	if converted.Kind != KindDeployment || converted.Namespace != "team-a" || converted.Name != "api" || converted.UID != "deployment-uid" {
		t.Fatalf("metadata was not converted: %#v", converted)
	}
	if converted.CurrentReplicas != 0 {
		t.Fatalf("task 5.1 must not read replicas, got %d", converted.CurrentReplicas)
	}
	if len(converted.Containers) != 2 || converted.Containers[0] != (Container{Name: "sidecar", Image: "sidecar:v2"}) || converted.Containers[1] != (Container{Name: "app", Image: "app:v3"}) {
		t.Fatalf("ordinary containers were not converted: %#v", converted.Containers)
	}
	deployment.Labels["app"] = "mutated"
	if converted.Labels["app"] != "api" {
		t.Fatal("conversion retained a mutable labels alias")
	}

	statefulSet := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "team-b", UID: types.UID("statefulset-uid")},
		Spec: appsv1.StatefulSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "database", Image: "db:v4"}},
		}}},
	}
	statefulWorkload := WorkloadFromStatefulSet(statefulSet)
	if statefulWorkload.Kind != KindStatefulSet || statefulWorkload.Key() != "StatefulSet/team-b/db" || len(statefulWorkload.Containers) != 1 {
		t.Fatalf("StatefulSet was not converted: %#v", statefulWorkload)
	}
}
func TestKubernetesGatewayListsBothKindsAcrossNamespaces(t *testing.T) {
	client := kubefake.NewSimpleClientset(
		deploymentObject("team-a", "api", "api:v1"),
		deploymentObject("team-b", "worker", "worker:v2"),
		statefulSetObject("data", "redis", "redis:v7"),
	)
	gateway := NewKubernetesGateway(NewKubernetesReadClient(client.AppsV1()))

	deployments, err := gateway.List(context.Background(), KindDeployment)
	if err != nil {
		t.Fatal(err)
	}
	statefulSets, err := gateway.List(context.Background(), KindStatefulSet)
	if err != nil {
		t.Fatal(err)
	}
	if len(deployments) != 2 || deployments[0].Namespace == deployments[1].Namespace {
		t.Fatalf("Deployment list did not span namespaces: %#v", deployments)
	}
	if len(statefulSets) != 1 || statefulSets[0].Namespace != "data" || statefulSets[0].Kind != KindStatefulSet {
		t.Fatalf("unexpected StatefulSet list: %#v", statefulSets)
	}
	for _, action := range client.Actions() {
		if action.GetVerb() != "list" || action.GetNamespace() != metav1.NamespaceAll {
			t.Fatalf("gateway performed non-list or namespaced action: %s %s namespace=%q", action.GetVerb(), action.GetResource().Resource, action.GetNamespace())
		}
	}
}

func deploymentObject(namespace, name, image string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, UID: types.UID(name + "-uid"), Labels: map[string]string{"name": name}},
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "main", Image: image}},
		}}},
	}
}

func statefulSetObject(namespace, name, image string) *appsv1.StatefulSet {
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, UID: types.UID(name + "-uid")},
		Spec: appsv1.StatefulSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "main", Image: image}},
		}}},
	}
}
func TestKubernetesGatewayWatchConvertsEventsAndSignalsFailures(t *testing.T) {
	client := kubefake.NewSimpleClientset()
	deploymentWatch := watch.NewRaceFreeFake()
	statefulSetWatch := watch.NewRaceFreeFake()
	client.PrependWatchReactor("deployments", func(action k8stesting.Action) (bool, watch.Interface, error) {
		if action.GetNamespace() != metav1.NamespaceAll {
			t.Fatalf("Deployment watch namespace = %q", action.GetNamespace())
		}
		return true, deploymentWatch, nil
	})
	client.PrependWatchReactor("statefulsets", func(action k8stesting.Action) (bool, watch.Interface, error) {
		if action.GetNamespace() != metav1.NamespaceAll {
			t.Fatalf("StatefulSet watch namespace = %q", action.GetNamespace())
		}
		return true, statefulSetWatch, nil
	})

	stream, err := NewKubernetesGateway(NewKubernetesReadClient(client.AppsV1())).Watch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Stop()

	deploymentWatch.Add(deploymentObject("team-a", "api", "api:v2"))
	added := receiveEvent(t, stream.ResultChan())
	if added.Type != EventAdded || added.Kind != KindDeployment || added.Workload == nil || added.Workload.Namespace != "team-a" || added.Workload.Containers[0].Image != "api:v2" {
		t.Fatalf("unexpected converted watch event: %#v", added)
	}

	deploymentWatch.Error(&metav1.Status{
		Status: metav1.StatusFailure, Reason: metav1.StatusReasonExpired,
		Message: "resource version expired", Code: 410,
	})
	watchError := receiveEvent(t, stream.ResultChan())
	if watchError.Type != EventError || watchError.Kind != KindDeployment || watchError.Workload != nil || !apierrors.IsResourceExpired(watchError.Err) {
		t.Fatalf("watch error was not signaled to upper layer: %#v", watchError)
	}

	statefulSetWatch.Stop()
	disconnected := receiveEvent(t, stream.ResultChan())
	if disconnected.Type != EventDisconnected || disconnected.Kind != KindStatefulSet || !errors.Is(disconnected.Err, ErrWatchDisconnected) {
		t.Fatalf("watch disconnect was not signaled to upper layer: %#v", disconnected)
	}
}
func TestKubernetesGatewayWatchRejectsUnexpectedObjects(t *testing.T) {
	event := convertWatchEvent(KindDeployment, watch.Event{Type: watch.Modified, Object: &corev1.Pod{}})
	if event.Type != EventError || event.Workload != nil || event.Err == nil {
		t.Fatalf("unexpected object should become an upper-layer error signal: %#v", event)
	}
}

func receiveEvent(t *testing.T, events <-chan Event) Event {
	t.Helper()
	select {
	case event, ok := <-events:
		if !ok {
			t.Fatal("watch stream closed before expected event")
		}
		return event
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for watch event")
		return Event{}
	}
}
