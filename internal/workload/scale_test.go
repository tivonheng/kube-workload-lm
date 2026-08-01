package workload

import (
	"context"
	"errors"
	"strings"
	"testing"

	autoscalingv1 "k8s.io/api/autoscaling/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	kubefake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestScaleGatewayReadUsesLatestScaleForUnifiedWorkload(t *testing.T) {
	client := kubefake.NewSimpleClientset()
	for resource, replicas := range map[string]int32{"deployments": 7, "statefulsets": 4} {
		resource, replicas := resource, replicas
		client.PrependReactor("get", resource, func(action k8stesting.Action) (bool, runtime.Object, error) {
			assertScaleAction(t, action, "get", resource)
			return true, &autoscalingv1.Scale{
				ObjectMeta: metav1.ObjectMeta{Name: action.(k8stesting.GetAction).GetName(), ResourceVersion: "latest"},
				Spec:       autoscalingv1.ScaleSpec{Replicas: replicas},
			}, nil
		})
	}
	gateway := NewKubernetesScaleGateway(client.AppsV1())

	deployment, err := gateway.Read(context.Background(), Workload{
		Kind: KindDeployment, Namespace: "team-a", Name: "api", CurrentReplicas: 99,
	})
	if err != nil {
		t.Fatal(err)
	}
	statefulSet, err := gateway.Read(context.Background(), Workload{
		Kind: KindStatefulSet, Namespace: "data", Name: "db", CurrentReplicas: 99,
	})
	if err != nil {
		t.Fatal(err)
	}
	if deployment.CurrentReplicas != 7 || statefulSet.CurrentReplicas != 4 {
		t.Fatalf("Read did not replace stale replicas from scale: deployment=%d statefulset=%d", deployment.CurrentReplicas, statefulSet.CurrentReplicas)
	}
	assertOnlyScaleActions(t, client.Actions())
}
func TestScaleGatewayUpdateUsesOnlyLiveScaleSubresources(t *testing.T) {
	client := kubefake.NewSimpleClientset()
	for resource := range map[string]struct{}{"deployments": {}, "statefulsets": {}} {
		resource := resource
		client.PrependReactor("get", resource, func(action k8stesting.Action) (bool, runtime.Object, error) {
			assertScaleAction(t, action, "get", resource)
			return true, &autoscalingv1.Scale{
				ObjectMeta: metav1.ObjectMeta{Name: action.(k8stesting.GetAction).GetName(), ResourceVersion: "live-rv"},
				Spec:       autoscalingv1.ScaleSpec{Replicas: 8},
			}, nil
		})
		client.PrependReactor("update", resource, func(action k8stesting.Action) (bool, runtime.Object, error) {
			assertScaleAction(t, action, "update", resource)
			scale, ok := action.(k8stesting.UpdateAction).GetObject().(*autoscalingv1.Scale)
			if !ok {
				t.Fatalf("update %s sent %T instead of Scale", resource, action.(k8stesting.UpdateAction).GetObject())
			}
			if scale.Spec.Replicas != 2 || scale.ResourceVersion != "live-rv" {
				t.Fatalf("update %s sent replicas=%d resourceVersion=%q", resource, scale.Spec.Replicas, scale.ResourceVersion)
			}
			return true, scale.DeepCopy(), nil
		})
	}
	gateway := NewKubernetesScaleGateway(client.AppsV1())

	for _, current := range []Workload{
		{Kind: KindDeployment, Namespace: "team-a", Name: "api"},
		{Kind: KindStatefulSet, Namespace: "data", Name: "db"},
	} {
		if err := gateway.Update(context.Background(), current, 2); err != nil {
			t.Fatalf("Update(%s): %v", current.Kind, err)
		}
	}
	if len(client.Actions()) != 4 {
		t.Fatalf("Update actions = %d, want two get/update scale pairs: %#v", len(client.Actions()), client.Actions())
	}
	assertOnlyScaleActions(t, client.Actions())
}

func TestScaleGatewayRejectsUnsupportedKindWithoutAPIAction(t *testing.T) {
	client := kubefake.NewSimpleClientset()
	gateway := NewKubernetesScaleGateway(client.AppsV1())
	unsupported := Workload{Kind: Kind("DaemonSet"), Namespace: "team-a", Name: "agent"}

	if _, err := gateway.Read(context.Background(), unsupported); !errors.Is(err, ErrUnsupportedKind) {
		t.Fatalf("Read error = %v, want ErrUnsupportedKind", err)
	}
	if err := gateway.Update(context.Background(), unsupported, 1); !errors.Is(err, ErrUnsupportedKind) {
		t.Fatalf("Update error = %v, want ErrUnsupportedKind", err)
	}
	if len(client.Actions()) != 0 {
		t.Fatalf("unsupported kind performed API actions: %#v", client.Actions())
	}
}
func TestScaleGatewayPreservesAPIErrors(t *testing.T) {
	t.Run("read", func(t *testing.T) {
		client := kubefake.NewSimpleClientset()
		forbidden := apierrors.NewForbidden(schema.GroupResource{Group: "apps", Resource: "deployments/scale"}, "api", errors.New("denied"))
		client.PrependReactor("get", "deployments", func(action k8stesting.Action) (bool, runtime.Object, error) {
			assertScaleAction(t, action, "get", "deployments")
			return true, nil, forbidden
		})
		_, err := NewKubernetesScaleGateway(client.AppsV1()).Read(context.Background(), Workload{
			Kind: KindDeployment, Namespace: "team-a", Name: "api",
		})
		if !apierrors.IsForbidden(err) {
			t.Fatalf("Read error = %v, want wrapped Forbidden", err)
		}
	})

	t.Run("update conflict", func(t *testing.T) {
		client := kubefake.NewSimpleClientset()
		client.PrependReactor("get", "statefulsets", func(action k8stesting.Action) (bool, runtime.Object, error) {
			assertScaleAction(t, action, "get", "statefulsets")
			return true, &autoscalingv1.Scale{ObjectMeta: metav1.ObjectMeta{Name: "db", ResourceVersion: "old"}}, nil
		})
		conflict := apierrors.NewConflict(schema.GroupResource{Group: "apps", Resource: "statefulsets/scale"}, "db", errors.New("changed"))
		client.PrependReactor("update", "statefulsets", func(action k8stesting.Action) (bool, runtime.Object, error) {
			assertScaleAction(t, action, "update", "statefulsets")
			return true, nil, conflict
		})
		err := NewKubernetesScaleGateway(client.AppsV1()).Update(context.Background(), Workload{
			Kind: KindStatefulSet, Namespace: "data", Name: "db",
		}, 0)
		if !apierrors.IsConflict(err) {
			t.Fatalf("Update error = %v, want wrapped Conflict", err)
		}
		assertOnlyScaleActions(t, client.Actions())
	})
}

func TestScaleGatewayHonorsCanceledContextWithoutAPIAction(t *testing.T) {
	client := kubefake.NewSimpleClientset()
	gateway := NewKubernetesScaleGateway(client.AppsV1())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	current := Workload{Kind: KindDeployment, Namespace: "team-a", Name: "api"}

	if _, err := gateway.Read(ctx, current); !errors.Is(err, context.Canceled) {
		t.Fatalf("Read error = %v, want context.Canceled", err)
	}
	if err := gateway.Update(ctx, current, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("Update error = %v, want context.Canceled", err)
	}
	if len(client.Actions()) != 0 {
		t.Fatalf("canceled context performed API actions: %#v", client.Actions())
	}
}
func assertScaleAction(t *testing.T, action k8stesting.Action, verb, resource string) {
	t.Helper()
	if action.GetVerb() != verb || action.GetResource().Resource != resource || action.GetSubresource() != "scale" {
		t.Fatalf("action = %s %s/%s, want %s %s/scale", action.GetVerb(), action.GetResource().Resource, action.GetSubresource(), verb, resource)
	}
}

func assertOnlyScaleActions(t *testing.T, actions []k8stesting.Action) {
	t.Helper()
	for index, action := range actions {
		resource := action.GetResource().Resource
		if action.GetSubresource() != "scale" || (resource != "deployments" && resource != "statefulsets") {
			t.Fatalf("action[%d] touched workload body: %s %s/%s", index, action.GetVerb(), resource, action.GetSubresource())
		}
		if action.GetVerb() != "get" && action.GetVerb() != "update" {
			t.Fatalf("action[%d] used unexpected verb: %s", index, action.GetVerb())
		}
	}
}

func TestScaleGatewayErrorIncludesWorkloadIdentity(t *testing.T) {
	client := kubefake.NewSimpleClientset()
	client.PrependReactor("get", "deployments", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("transport failed")
	})
	_, err := NewKubernetesScaleGateway(client.AppsV1()).Read(context.Background(), Workload{
		Kind: KindDeployment, Namespace: "team-a", Name: "api",
	})
	want := "get Deployment scale team-a/api"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want identity %q", err, want)
	}
}
