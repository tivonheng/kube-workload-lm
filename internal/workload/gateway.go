package workload

import (
	"context"
	"errors"
	"fmt"
	"sync"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	typedappsv1 "k8s.io/client-go/kubernetes/typed/apps/v1"
)

var (
	ErrUnsupportedKind   = errors.New("unsupported workload kind")
	ErrWatchDisconnected = errors.New("workload watch disconnected")
)

type EventType string

const (
	EventAdded        EventType = "added"
	EventModified     EventType = "modified"
	EventDeleted      EventType = "deleted"
	EventError        EventType = "error"
	EventDisconnected EventType = "disconnected"
)

// Event carries either a converted workload or a signal requiring the upper
// layer to recreate watches and reconcile. Error and Disconnected have Err set.
type Event struct {
	Type     EventType
	Kind     Kind
	Workload *Workload
	Err      error
}

// WatchStream owns both workload watches. Stop is safe to call more than once.
type WatchStream interface {
	ResultChan() <-chan Event
	Stop()
}

// ReadClient is the complete Kubernetes capability available to this adapter.
// Its deliberately read-only surface prevents accidental workload mutation.
type ReadClient interface {
	ListDeployments(context.Context, metav1.ListOptions) (*appsv1.DeploymentList, error)
	WatchDeployments(context.Context, metav1.ListOptions) (watch.Interface, error)
	ListStatefulSets(context.Context, metav1.ListOptions) (*appsv1.StatefulSetList, error)
	WatchStatefulSets(context.Context, metav1.ListOptions) (watch.Interface, error)
}

// KubernetesReadClient adapts the generated typed client and always addresses
// NamespaceAll. It does not expose update, patch, delete, or scale operations.
type KubernetesReadClient struct {
	apps typedappsv1.AppsV1Interface
}

func NewKubernetesReadClient(apps typedappsv1.AppsV1Interface) *KubernetesReadClient {
	return &KubernetesReadClient{apps: apps}
}

func (c *KubernetesReadClient) ListDeployments(ctx context.Context, options metav1.ListOptions) (*appsv1.DeploymentList, error) {
	return c.apps.Deployments(metav1.NamespaceAll).List(ctx, options)
}

func (c *KubernetesReadClient) WatchDeployments(ctx context.Context, options metav1.ListOptions) (watch.Interface, error) {
	return c.apps.Deployments(metav1.NamespaceAll).Watch(ctx, options)
}

func (c *KubernetesReadClient) ListStatefulSets(ctx context.Context, options metav1.ListOptions) (*appsv1.StatefulSetList, error) {
	return c.apps.StatefulSets(metav1.NamespaceAll).List(ctx, options)
}

func (c *KubernetesReadClient) WatchStatefulSets(ctx context.Context, options metav1.ListOptions) (watch.Interface, error) {
	return c.apps.StatefulSets(metav1.NamespaceAll).Watch(ctx, options)
}

// WorkloadGateway is the read-only workload inventory and event boundary.
type WorkloadGateway interface {
	List(context.Context, Kind) ([]Workload, error)
	Watch(context.Context) (WatchStream, error)
}

type KubernetesGateway struct {
	client ReadClient
}

var _ WorkloadGateway = (*KubernetesGateway)(nil)

func NewKubernetesGateway(client ReadClient) *KubernetesGateway {
	return &KubernetesGateway{client: client}
}
func (g *KubernetesGateway) List(ctx context.Context, kind Kind) ([]Workload, error) {
	switch kind {
	case KindDeployment:
		items, err := g.client.ListDeployments(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, fmt.Errorf("list deployments across all namespaces: %w", err)
		}
		result := make([]Workload, 0, len(items.Items))
		for index := range items.Items {
			result = append(result, WorkloadFromDeployment(&items.Items[index]))
		}
		return result, nil
	case KindStatefulSet:
		items, err := g.client.ListStatefulSets(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, fmt.Errorf("list statefulsets across all namespaces: %w", err)
		}
		result := make([]Workload, 0, len(items.Items))
		for index := range items.Items {
			result = append(result, WorkloadFromStatefulSet(&items.Items[index]))
		}
		return result, nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedKind, kind)
	}
}

func (g *KubernetesGateway) Watch(ctx context.Context) (WatchStream, error) {
	watchCtx, cancel := context.WithCancel(ctx)
	deployments, err := g.client.WatchDeployments(watchCtx, metav1.ListOptions{})
	if err != nil {
		cancel()
		return nil, fmt.Errorf("watch deployments across all namespaces: %w", err)
	}
	statefulSets, err := g.client.WatchStatefulSets(watchCtx, metav1.ListOptions{})
	if err != nil {
		deployments.Stop()
		cancel()
		return nil, fmt.Errorf("watch statefulsets across all namespaces: %w", err)
	}

	stream := &mergedWatchStream{
		cancel:  cancel,
		result:  make(chan Event),
		watches: []watch.Interface{deployments, statefulSets},
	}
	stream.wait.Add(2)
	go stream.forward(watchCtx, KindDeployment, deployments)
	go stream.forward(watchCtx, KindStatefulSet, statefulSets)
	go func() {
		stream.wait.Wait()
		close(stream.result)
	}()
	return stream, nil
}

type mergedWatchStream struct {
	cancel  context.CancelFunc
	result  chan Event
	watches []watch.Interface
	wait    sync.WaitGroup
	once    sync.Once
}

func (s *mergedWatchStream) ResultChan() <-chan Event { return s.result }

func (s *mergedWatchStream) Stop() {
	s.once.Do(func() {
		s.cancel()
		for _, current := range s.watches {
			current.Stop()
		}
	})
}

func (s *mergedWatchStream) forward(ctx context.Context, kind Kind, source watch.Interface) {
	defer s.wait.Done()
	defer source.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case raw, ok := <-source.ResultChan():
			if !ok {
				select {
				case s.result <- Event{Type: EventDisconnected, Kind: kind, Err: ErrWatchDisconnected}:
				case <-ctx.Done():
				}
				return
			}
			event := convertWatchEvent(kind, raw)
			select {
			case s.result <- event:
			case <-ctx.Done():
				return
			}
		}
	}
}

func convertWatchEvent(kind Kind, raw watch.Event) Event {
	if raw.Type == watch.Error {
		return Event{Type: EventError, Kind: kind, Err: fmt.Errorf("%s watch error: %w", kind, apierrors.FromObject(raw.Object))}
	}
	eventType, ok := eventType(raw.Type)
	if !ok {
		return Event{Type: EventError, Kind: kind, Err: fmt.Errorf("%s watch returned unsupported event type %q", kind, raw.Type)}
	}
	converted, err := workloadFromObject(kind, raw.Object)
	if err != nil {
		return Event{Type: EventError, Kind: kind, Err: err}
	}
	return Event{Type: eventType, Kind: kind, Workload: &converted}
}
func eventType(value watch.EventType) (EventType, bool) {
	switch value {
	case watch.Added:
		return EventAdded, true
	case watch.Modified:
		return EventModified, true
	case watch.Deleted:
		return EventDeleted, true
	default:
		return "", false
	}
}

func workloadFromObject(kind Kind, object any) (Workload, error) {
	switch kind {
	case KindDeployment:
		deployment, ok := object.(*appsv1.Deployment)
		if !ok {
			return Workload{}, fmt.Errorf("Deployment watch returned %T", object)
		}
		return WorkloadFromDeployment(deployment), nil
	case KindStatefulSet:
		statefulSet, ok := object.(*appsv1.StatefulSet)
		if !ok {
			return Workload{}, fmt.Errorf("StatefulSet watch returned %T", object)
		}
		return WorkloadFromStatefulSet(statefulSet), nil
	default:
		return Workload{}, fmt.Errorf("%w: %q", ErrUnsupportedKind, kind)
	}
}

func WorkloadFromDeployment(deployment *appsv1.Deployment) Workload {
	return fromPodTemplate(KindDeployment, deployment.ObjectMeta, deployment.Spec.Template.Spec.Containers)
}

func WorkloadFromStatefulSet(statefulSet *appsv1.StatefulSet) Workload {
	return fromPodTemplate(KindStatefulSet, statefulSet.ObjectMeta, statefulSet.Spec.Template.Spec.Containers)
}

func fromPodTemplate(kind Kind, metadata metav1.ObjectMeta, source []corev1.Container) Workload {
	labels := make(map[string]string, len(metadata.Labels))
	for key, value := range metadata.Labels {
		labels[key] = value
	}
	containers := make([]Container, 0, len(source))
	for _, container := range source {
		containers = append(containers, Container{Name: container.Name, Image: container.Image})
	}
	return Workload{
		Kind:       kind,
		Namespace:  metadata.Namespace,
		Name:       metadata.Name,
		UID:        string(metadata.UID),
		Labels:     labels,
		Containers: containers,
	}
}
