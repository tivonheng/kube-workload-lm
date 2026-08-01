package workload

import (
	"context"
	"fmt"

	autoscalingv1 "k8s.io/api/autoscaling/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	typedappsv1 "k8s.io/client-go/kubernetes/typed/apps/v1"
)

// ScaleGateway is the only capability used to read or mutate workload replicas.
// Read returns a copy populated from the live scale subresource; Update never
// writes or patches the Deployment or StatefulSet object itself.
type ScaleGateway interface {
	Read(context.Context, Workload) (Workload, error)
	Update(context.Context, Workload, int32) error
}

type KubernetesScaleGateway struct {
	apps typedappsv1.AppsV1Interface
}

var _ ScaleGateway = (*KubernetesScaleGateway)(nil)

func NewKubernetesScaleGateway(apps typedappsv1.AppsV1Interface) *KubernetesScaleGateway {
	return &KubernetesScaleGateway{apps: apps}
}

func (g *KubernetesScaleGateway) Read(ctx context.Context, current Workload) (Workload, error) {
	scale, err := g.get(ctx, current)
	if err != nil {
		return Workload{}, err
	}
	current.CurrentReplicas = scale.Spec.Replicas
	return current, nil
}

func (g *KubernetesScaleGateway) Update(ctx context.Context, current Workload, replicas int32) error {
	scale, err := g.get(ctx, current)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("update %s scale %s/%s: %w", current.Kind, current.Namespace, current.Name, err)
	}
	scale.Spec.Replicas = replicas
	return g.update(ctx, current, scale)
}
func (g *KubernetesScaleGateway) get(ctx context.Context, current Workload) (*autoscalingv1.Scale, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("get %s scale %s/%s: %w", current.Kind, current.Namespace, current.Name, err)
	}

	var (
		scale *autoscalingv1.Scale
		err   error
	)
	switch current.Kind {
	case KindDeployment:
		scale, err = g.apps.Deployments(current.Namespace).GetScale(ctx, current.Name, metav1.GetOptions{})
	case KindStatefulSet:
		scale, err = g.apps.StatefulSets(current.Namespace).GetScale(ctx, current.Name, metav1.GetOptions{})
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedKind, current.Kind)
	}
	if err != nil {
		return nil, fmt.Errorf("get %s scale %s/%s: %w", current.Kind, current.Namespace, current.Name, err)
	}
	if scale == nil {
		return nil, fmt.Errorf("get %s scale %s/%s: API returned an empty scale", current.Kind, current.Namespace, current.Name)
	}
	return scale, nil
}

func (g *KubernetesScaleGateway) update(ctx context.Context, current Workload, scale *autoscalingv1.Scale) error {
	var err error
	switch current.Kind {
	case KindDeployment:
		_, err = g.apps.Deployments(current.Namespace).UpdateScale(ctx, current.Name, scale, metav1.UpdateOptions{})
	case KindStatefulSet:
		_, err = g.apps.StatefulSets(current.Namespace).UpdateScale(ctx, current.Name, scale, metav1.UpdateOptions{})
	default:
		return fmt.Errorf("%w: %q", ErrUnsupportedKind, current.Kind)
	}
	if err != nil {
		return fmt.Errorf("update %s scale %s/%s: %w", current.Kind, current.Namespace, current.Name, err)
	}
	return nil
}
