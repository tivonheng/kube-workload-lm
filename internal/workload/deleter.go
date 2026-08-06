package workload

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	typedappsv1 "k8s.io/client-go/kubernetes/typed/apps/v1"
)

// WorkloadDeleter deletes a Deployment or StatefulSet object.
type WorkloadDeleter interface {
	Delete(ctx context.Context, w Workload) error
}

// KubernetesDeleter implements WorkloadDeleter using the Kubernetes appsv1 typed client.
// It uses Foreground propagation policy to let the garbage collector handle cascading Pod cleanup.
type KubernetesDeleter struct {
	apps typedappsv1.AppsV1Interface
}

var _ WorkloadDeleter = (*KubernetesDeleter)(nil)

func NewKubernetesDeleter(apps typedappsv1.AppsV1Interface) *KubernetesDeleter {
	return &KubernetesDeleter{apps: apps}
}

func (d *KubernetesDeleter) Delete(ctx context.Context, w Workload) error {
	propagation := metav1.DeletePropagationForeground
	opts := metav1.DeleteOptions{PropagationPolicy: &propagation}
	switch w.Kind {
	case KindDeployment:
		return d.apps.Deployments(w.Namespace).Delete(ctx, w.Name, opts)
	case KindStatefulSet:
		return d.apps.StatefulSets(w.Namespace).Delete(ctx, w.Name, opts)
	default:
		return fmt.Errorf("%w: %q", ErrUnsupportedKind, w.Kind)
	}
}
