package policy

import (
	"context"
	"errors"
	"fmt"
	"sync"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/watch"
)

const DefaultConfigMapDataKey = "policies.yaml"

var (
	ErrMissingPolicyData       = errors.New("policy ConfigMap is missing policy data")
	ErrPolicyWatchDisconnected = errors.New("policy ConfigMap watch disconnected")
)

type EventType string

const (
	EventReloaded       EventType = "reloaded"
	EventReloadRejected EventType = "reload-rejected"
	EventDeleted        EventType = "deleted"
	EventError          EventType = "error"
	EventDisconnected   EventType = "disconnected"
)

type Event struct {
	Type EventType
	Err  error
}

type WatchStream interface {
	ResultChan() <-chan Event
	Stop()
}

type Watcher interface {
	Watch(context.Context) (WatchStream, error)
}

type ConfigMapWatchClient interface {
	Watch(context.Context, metav1.ListOptions) (watch.Interface, error)
}

// ConfigMapWatcher watches one policy ConfigMap. Valid updates atomically reload
// Manager; invalid updates are reported while Manager retains its last snapshot.
type ConfigMapWatcher struct {
	client  ConfigMapWatchClient
	name    string
	dataKey string
	manager *Manager
}

var _ Watcher = (*ConfigMapWatcher)(nil)

func NewConfigMapWatcher(client ConfigMapWatchClient, name, dataKey string, manager *Manager) *ConfigMapWatcher {
	if dataKey == "" {
		dataKey = DefaultConfigMapDataKey
	}
	return &ConfigMapWatcher{client: client, name: name, dataKey: dataKey, manager: manager}
}

func (watcher *ConfigMapWatcher) Watch(ctx context.Context) (WatchStream, error) {
	watchCtx, cancel := context.WithCancel(ctx)
	selector := fields.OneTermEqualSelector("metadata.name", watcher.name).String()
	source, err := watcher.client.Watch(watchCtx, metav1.ListOptions{FieldSelector: selector})
	if err != nil {
		cancel()
		return nil, fmt.Errorf("watch policy ConfigMap %q: %w", watcher.name, err)
	}
	stream := &configMapWatchStream{
		cancel: cancel, result: make(chan Event), source: source,
		name: watcher.name, dataKey: watcher.dataKey, manager: watcher.manager,
	}
	go stream.forward(watchCtx)
	return stream, nil
}

type configMapWatchStream struct {
	cancel  context.CancelFunc
	result  chan Event
	source  watch.Interface
	name    string
	dataKey string
	manager *Manager
	once    sync.Once
}

func (stream *configMapWatchStream) ResultChan() <-chan Event { return stream.result }

func (stream *configMapWatchStream) Stop() {
	stream.once.Do(func() {
		stream.cancel()
		stream.source.Stop()
	})
}

func (stream *configMapWatchStream) forward(ctx context.Context) {
	defer close(stream.result)
	defer stream.source.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case raw, ok := <-stream.source.ResultChan():
			if !ok {
				stream.send(ctx, Event{Type: EventDisconnected, Err: ErrPolicyWatchDisconnected})
				return
			}
			event, terminal := stream.convert(raw)
			if !stream.send(ctx, event) || terminal {
				return
			}
		}
	}
}

func (stream *configMapWatchStream) send(ctx context.Context, event Event) bool {
	select {
	case stream.result <- event:
		return true
	case <-ctx.Done():
		return false
	}
}

func (stream *configMapWatchStream) convert(raw watch.Event) (Event, bool) {
	if raw.Type == watch.Error {
		return Event{Type: EventError, Err: fmt.Errorf("policy ConfigMap watch error: %w", apierrors.FromObject(raw.Object))}, true
	}
	configMap, ok := raw.Object.(*corev1.ConfigMap)
	if !ok {
		return Event{Type: EventError, Err: fmt.Errorf("policy ConfigMap watch returned %T", raw.Object)}, true
	}
	if configMap.Name != stream.name {
		return Event{Type: EventReloadRejected, Err: fmt.Errorf("policy ConfigMap watch returned unexpected object %q", configMap.Name)}, false
	}
	if raw.Type == watch.Deleted {
		return Event{Type: EventDeleted}, false
	}
	if raw.Type != watch.Added && raw.Type != watch.Modified {
		return Event{Type: EventError, Err: fmt.Errorf("policy ConfigMap watch returned unsupported event type %q", raw.Type)}, true
	}
	payload, ok := configMap.Data[stream.dataKey]
	if !ok {
		return Event{Type: EventReloadRejected, Err: fmt.Errorf("%w %q", ErrMissingPolicyData, stream.dataKey)}, false
	}
	if stream.manager == nil {
		return Event{Type: EventReloadRejected, Err: errors.New("policy manager is not configured")}, false
	}
	if err := stream.manager.Reload([]byte(payload)); err != nil {
		return Event{Type: EventReloadRejected, Err: fmt.Errorf("reload policy ConfigMap %q: %w", stream.name, err)}, false
	}
	return Event{Type: EventReloaded}, false
}
