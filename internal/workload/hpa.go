package workload

import (
	"context"
	"errors"
	"fmt"
	"sync"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	typedautoscalingv2 "k8s.io/client-go/kubernetes/typed/autoscaling/v2"
)

const (
	HPAReasonAllowed        = "no-hpa-conflict"
	HPAReasonConflict       = "hpa-conflict"
	HPAReasonDetectionError = "hpa-detection-error"
)

var (
	ErrHPAConflict          = errors.New("workload is managed by an HPA")
	ErrHPAWatchDisconnected = errors.New("HPA watch disconnected")
)

type HPAReference struct {
	Namespace string
	Name      string
}

type HPATargetReference struct {
	APIVersion string
	Kind       string
	Namespace  string
	Name       string
}

// HPAEvaluation is fail-closed: Allowed is true only after a successful
// cluster-wide lookup proves that no HPA targets the workload. The controller
// must return before state or scale mutation whenever Allowed is false.
type HPAEvaluation struct {
	Allowed        bool
	Reason         string
	ConflictingHPA *HPAReference
	Err            error
}

// Rejection returns nil only when state and scale mutations are safe.
func (evaluation HPAEvaluation) Rejection() error {
	if evaluation.Allowed {
		return nil
	}
	if evaluation.Err != nil {
		return evaluation.Err
	}
	return ErrHPAConflict
}

type HPAEvent struct {
	Type      EventType
	Namespace string
	Name      string
	Target    *HPATargetReference
	Err       error
}

type HPAWatchStream interface {
	ResultChan() <-chan HPAEvent
	Stop()
}

// HPADetector is the admission boundary used before replicaSnapshot or scale
// mutation. Every HPA watch event requires the controller to re-evaluate its
// workload inventory because a modified HPA may move between targets.
type HPADetector interface {
	Evaluate(context.Context, Workload) HPAEvaluation
	Watch(context.Context) (HPAWatchStream, error)
}

type KubernetesHPADetector struct {
	autoscaling typedautoscalingv2.AutoscalingV2Interface
}

var _ HPADetector = (*KubernetesHPADetector)(nil)

func NewKubernetesHPADetector(autoscaling typedautoscalingv2.AutoscalingV2Interface) *KubernetesHPADetector {
	return &KubernetesHPADetector{autoscaling: autoscaling}
}

func (detector *KubernetesHPADetector) Evaluate(ctx context.Context, target Workload) HPAEvaluation {
	if target.Kind != KindDeployment && target.Kind != KindStatefulSet {
		return rejectedHPAEvaluation(fmt.Errorf("detect HPA for %s/%s/%s: %w", target.Kind, target.Namespace, target.Name, ErrUnsupportedKind))
	}
	items, err := detector.autoscaling.HorizontalPodAutoscalers(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return rejectedHPAEvaluation(fmt.Errorf("list HPAs across all namespaces for %s %s/%s: %w", target.Kind, target.Namespace, target.Name, err))
	}
	for index := range items.Items {
		hpa := &items.Items[index]
		if hpaTargetsWorkload(hpa, target) {
			return HPAEvaluation{
				Reason:         HPAReasonConflict,
				ConflictingHPA: &HPAReference{Namespace: hpa.Namespace, Name: hpa.Name},
			}
		}
	}
	return HPAEvaluation{Allowed: true, Reason: HPAReasonAllowed}
}

func rejectedHPAEvaluation(err error) HPAEvaluation {
	return HPAEvaluation{Reason: HPAReasonDetectionError, Err: err}
}
func hpaTargetsWorkload(hpa *autoscalingv2.HorizontalPodAutoscaler, target Workload) bool {
	ref := hpa.Spec.ScaleTargetRef
	return ref.APIVersion == "apps/v1" &&
		ref.Kind == string(target.Kind) &&
		ref.Name == target.Name &&
		hpa.Namespace == target.Namespace
}

func targetReference(hpa *autoscalingv2.HorizontalPodAutoscaler) *HPATargetReference {
	if hpa == nil {
		return nil
	}
	return &HPATargetReference{
		APIVersion: hpa.Spec.ScaleTargetRef.APIVersion,
		Kind:       hpa.Spec.ScaleTargetRef.Kind,
		Namespace:  hpa.Namespace,
		Name:       hpa.Spec.ScaleTargetRef.Name,
	}
}

func (detector *KubernetesHPADetector) Watch(ctx context.Context) (HPAWatchStream, error) {
	watchCtx, cancel := context.WithCancel(ctx)
	source, err := detector.autoscaling.HorizontalPodAutoscalers(metav1.NamespaceAll).Watch(watchCtx, metav1.ListOptions{})
	if err != nil {
		cancel()
		return nil, fmt.Errorf("watch HPAs across all namespaces: %w", err)
	}
	stream := &hpaWatchStream{
		cancel: cancel,
		result: make(chan HPAEvent),
		source: source,
	}
	go stream.forward(watchCtx)
	return stream, nil
}

type hpaWatchStream struct {
	cancel context.CancelFunc
	result chan HPAEvent
	source watch.Interface
	once   sync.Once
}

func (stream *hpaWatchStream) ResultChan() <-chan HPAEvent { return stream.result }

func (stream *hpaWatchStream) Stop() {
	stream.once.Do(func() {
		stream.cancel()
		stream.source.Stop()
	})
}

func (stream *hpaWatchStream) forward(ctx context.Context) {
	defer close(stream.result)
	defer stream.source.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case raw, ok := <-stream.source.ResultChan():
			if !ok {
				stream.send(ctx, HPAEvent{Type: EventDisconnected, Err: ErrHPAWatchDisconnected})
				return
			}
			if !stream.send(ctx, convertHPAWatchEvent(raw)) {
				return
			}
		}
	}
}
func (stream *hpaWatchStream) send(ctx context.Context, event HPAEvent) bool {
	select {
	case stream.result <- event:
		return true
	case <-ctx.Done():
		return false
	}
}

func convertHPAWatchEvent(raw watch.Event) HPAEvent {
	if raw.Type == watch.Error {
		return HPAEvent{Type: EventError, Err: fmt.Errorf("HPA watch error: %w", apierrors.FromObject(raw.Object))}
	}
	typeOfEvent, ok := eventType(raw.Type)
	if !ok {
		return HPAEvent{Type: EventError, Err: fmt.Errorf("HPA watch returned unsupported event type %q", raw.Type)}
	}
	hpa, ok := raw.Object.(*autoscalingv2.HorizontalPodAutoscaler)
	if !ok {
		return HPAEvent{Type: EventError, Err: fmt.Errorf("HPA watch returned %T", raw.Object)}
	}
	return HPAEvent{
		Type:      typeOfEvent,
		Namespace: hpa.Namespace,
		Name:      hpa.Name,
		Target:    targetReference(hpa),
	}
}
