package workload

import (
	"context"
	"errors"
	"testing"
	"time"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	kubefake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestHPADetectorMatchesExactTargetAcrossNamespaces(t *testing.T) {
	client := kubefake.NewSimpleClientset(
		hpaObject("team-a", "api-hpa", "apps/v1", "Deployment", "api"),
		hpaObject("data", "db-hpa", "apps/v1", "StatefulSet", "db"),
		hpaObject("team-a", "legacy-hpa", "apps/v1beta1", "Deployment", "legacy"),
		hpaObject("team-a", "worker-hpa", "apps/v1", "DaemonSet", "worker"),
	)
	detector := NewKubernetesHPADetector(client.AutoscalingV2())

	tests := []struct {
		name     string
		target   Workload
		rejected bool
		hpaName  string
	}{
		{"Deployment", Workload{Kind: KindDeployment, Namespace: "team-a", Name: "api"}, true, "api-hpa"},
		{"StatefulSet", Workload{Kind: KindStatefulSet, Namespace: "data", Name: "db"}, true, "db-hpa"},
		{"namespace differs", Workload{Kind: KindDeployment, Namespace: "team-b", Name: "api"}, false, ""},
		{"apiVersion differs", Workload{Kind: KindDeployment, Namespace: "team-a", Name: "legacy"}, false, ""},
		{"kind differs", Workload{Kind: KindDeployment, Namespace: "team-a", Name: "worker"}, false, ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertHPAEvaluation(t, detector.Evaluate(context.Background(), test.target), test.rejected, test.hpaName)
		})
	}
	for _, action := range client.Actions() {
		if action.GetVerb() != "list" || action.GetResource().Resource != "horizontalpodautoscalers" || action.GetNamespace() != metav1.NamespaceAll {
			t.Fatalf("detector action = %s %s namespace=%q, want cluster-wide HPA list", action.GetVerb(), action.GetResource().Resource, action.GetNamespace())
		}
	}
}

func TestHPADetectorFailsClosedOnDetectionErrors(t *testing.T) {
	client := kubefake.NewSimpleClientset()
	forbidden := apierrors.NewForbidden(
		schema.GroupResource{Group: "autoscaling", Resource: "horizontalpodautoscalers"},
		"", errors.New("denied"),
	)
	client.PrependReactor("list", "horizontalpodautoscalers", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, forbidden
	})
	detector := NewKubernetesHPADetector(client.AutoscalingV2())

	evaluation := detector.Evaluate(context.Background(), Workload{
		Kind: KindDeployment, Namespace: "team-a", Name: "api",
	})
	if evaluation.Allowed || evaluation.Reason != HPAReasonDetectionError || !apierrors.IsForbidden(evaluation.Rejection()) {
		t.Fatalf("detection error did not reject safely: %#v, rejection=%v", evaluation, evaluation.Rejection())
	}

	unsupported := detector.Evaluate(context.Background(), Workload{
		Kind: Kind("DaemonSet"), Namespace: "team-a", Name: "agent",
	})
	if unsupported.Allowed || !errors.Is(unsupported.Rejection(), ErrUnsupportedKind) {
		t.Fatalf("unsupported kind did not reject safely: %#v", unsupported)
	}
	if len(client.Actions()) != 1 {
		t.Fatalf("unsupported kind should not make another API call: %#v", client.Actions())
	}
}

func TestHPAEvaluationRejectionPreventsMutationBoundary(t *testing.T) {
	client := kubefake.NewSimpleClientset(
		hpaObject("team-a", "api-hpa", "apps/v1", "Deployment", "api"),
	)
	evaluation := NewKubernetesHPADetector(client.AutoscalingV2()).Evaluate(context.Background(), Workload{
		Kind: KindDeployment, Namespace: "team-a", Name: "api",
	})
	if evaluation.Allowed || evaluation.Reason != HPAReasonConflict || !errors.Is(evaluation.Rejection(), ErrHPAConflict) {
		t.Fatalf("conflict evaluation = %#v, rejection=%v", evaluation, evaluation.Rejection())
	}
	if evaluation.ConflictingHPA == nil || *evaluation.ConflictingHPA != (HPAReference{Namespace: "team-a", Name: "api-hpa"}) {
		t.Fatalf("conflicting HPA identity = %#v", evaluation.ConflictingHPA)
	}
}
func TestHPADetectorWatchesTargetChangesAcrossNamespaces(t *testing.T) {
	client := kubefake.NewSimpleClientset()
	source := watch.NewRaceFreeFake()
	client.PrependWatchReactor("horizontalpodautoscalers", func(action k8stesting.Action) (bool, watch.Interface, error) {
		if action.GetNamespace() != metav1.NamespaceAll {
			t.Fatalf("HPA watch namespace = %q, want NamespaceAll", action.GetNamespace())
		}
		return true, source, nil
	})
	stream, err := NewKubernetesHPADetector(client.AutoscalingV2()).Watch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Stop()

	hpa := hpaObject("team-a", "capacity", "apps/v1", "Deployment", "api")
	source.Add(hpa)
	assertHPAEvent(t, receiveHPAEvent(t, stream.ResultChan()), EventAdded, "team-a", "capacity", HPATargetReference{
		APIVersion: "apps/v1", Kind: "Deployment", Namespace: "team-a", Name: "api",
	})

	moved := hpa.DeepCopy()
	moved.Namespace = "data"
	moved.Spec.ScaleTargetRef.Kind = "StatefulSet"
	moved.Spec.ScaleTargetRef.Name = "db"
	source.Modify(moved)
	assertHPAEvent(t, receiveHPAEvent(t, stream.ResultChan()), EventModified, "data", "capacity", HPATargetReference{
		APIVersion: "apps/v1", Kind: "StatefulSet", Namespace: "data", Name: "db",
	})

	source.Delete(moved)
	assertHPAEvent(t, receiveHPAEvent(t, stream.ResultChan()), EventDeleted, "data", "capacity", HPATargetReference{
		APIVersion: "apps/v1", Kind: "StatefulSet", Namespace: "data", Name: "db",
	})
}

func TestHPAWatchSignalsErrorsAndUnexpectedObjects(t *testing.T) {
	statusEvent := convertHPAWatchEvent(watch.Event{Type: watch.Error, Object: &metav1.Status{
		Status: metav1.StatusFailure, Reason: metav1.StatusReasonExpired, Code: 410,
	}})
	if statusEvent.Type != EventError || !apierrors.IsResourceExpired(statusEvent.Err) {
		t.Fatalf("watch API error = %#v", statusEvent)
	}
	unexpected := convertHPAWatchEvent(watch.Event{Type: watch.Modified, Object: &corev1.Pod{}})
	if unexpected.Type != EventError || unexpected.Err == nil || unexpected.Target != nil {
		t.Fatalf("unexpected watch object = %#v", unexpected)
	}
}
func hpaObject(namespace, name, apiVersion, kind, targetName string) *autoscalingv2.HorizontalPodAutoscaler {
	return &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{
				APIVersion: apiVersion, Kind: kind, Name: targetName,
			},
			MaxReplicas: 10,
		},
	}
}

func assertHPAEvaluation(t *testing.T, evaluation HPAEvaluation, rejected bool, hpaName string) {
	t.Helper()
	if evaluation.Allowed == rejected {
		t.Fatalf("Allowed = %t, want %t: %#v", evaluation.Allowed, !rejected, evaluation)
	}
	if !rejected {
		if evaluation.Reason != HPAReasonAllowed || evaluation.Rejection() != nil || evaluation.ConflictingHPA != nil {
			t.Fatalf("allowed evaluation = %#v, rejection=%v", evaluation, evaluation.Rejection())
		}
		return
	}
	if evaluation.Reason != HPAReasonConflict || !errors.Is(evaluation.Rejection(), ErrHPAConflict) || evaluation.ConflictingHPA == nil || evaluation.ConflictingHPA.Name != hpaName {
		t.Fatalf("conflict evaluation = %#v, rejection=%v", evaluation, evaluation.Rejection())
	}
}

func receiveHPAEvent(t *testing.T, events <-chan HPAEvent) HPAEvent {
	t.Helper()
	select {
	case event, ok := <-events:
		if !ok {
			t.Fatal("HPA watch closed before expected event")
		}
		return event
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for HPA event")
		return HPAEvent{}
	}
}

func assertHPAEvent(t *testing.T, event HPAEvent, eventType EventType, namespace, name string, target HPATargetReference) {
	t.Helper()
	if event.Type != eventType || event.Namespace != namespace || event.Name != name || event.Err != nil || event.Target == nil || *event.Target != target {
		t.Fatalf("HPA event = %#v, want type=%s identity=%s/%s target=%#v", event, eventType, namespace, name, target)
	}
}
