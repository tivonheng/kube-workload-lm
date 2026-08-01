package state

import (
	"context"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const ConfigMapDataKey = "states.json"

var (
	ErrMissingStateData         = errors.New("state ConfigMap is missing states.json")
	ErrConflictRetriesExhausted = errors.New("state ConfigMap conflict retries exhausted")
)

// ConfigMapClient is the subset of the Kubernetes ConfigMap API used by the
// state store. A typed CoreV1 ConfigMap client satisfies this interface.
type ConfigMapClient interface {
	Get(context.Context, string, metav1.GetOptions) (*corev1.ConfigMap, error)
	Create(context.Context, *corev1.ConfigMap, metav1.CreateOptions) (*corev1.ConfigMap, error)
	Update(context.Context, *corev1.ConfigMap, metav1.UpdateOptions) (*corev1.ConfigMap, error)
}

// ConflictBackoff bounds retries after optimistic-concurrency conflicts.
type ConflictBackoff struct {
	Steps        int
	InitialDelay time.Duration
	Factor       float64
	MaxDelay     time.Duration
}

var DefaultConflictBackoff = ConflictBackoff{
	Steps: 5, InitialDelay: 10 * time.Millisecond, Factor: 2, MaxDelay: 160 * time.Millisecond,
}

// ConfigMapStateStore persists the complete state document in one ConfigMap.
type ConfigMapStateStore struct {
	client           ConfigMapClient
	namespace        string
	name             string
	backoff          ConflictBackoff
	sleep            func(context.Context, time.Duration) error
	capacityObserver CapacityObserver
}

var _ StateStore = (*ConfigMapStateStore)(nil)

func NewConfigMapStateStore(client ConfigMapClient, namespace, name string) *ConfigMapStateStore {
	return &ConfigMapStateStore{
		client: client, namespace: namespace, name: name,
		backoff: DefaultConflictBackoff, sleep: sleepContext,
	}
}

// SetCapacityObserver attaches a metrics or logging sink. It should be set
// during construction, before the store is used concurrently.
func (s *ConfigMapStateStore) SetCapacityObserver(observer CapacityObserver) {
	s.capacityObserver = observer
}

func (s *ConfigMapStateStore) Load(ctx context.Context) (Document, error) {
	var document Document
	err := s.retry(ctx, func() error {
		configMap, err := s.client.Get(ctx, s.name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			configMap, err = s.create(ctx, NewDocument())
		}
		if err != nil {
			return err
		}
		s.observeCapacity(configMap)
		document, err = decodeConfigMap(configMap)
		return err
	})
	if err != nil {
		return Document{}, fmt.Errorf("load state ConfigMap %s/%s: %w", s.namespace, s.name, err)
	}
	return document, nil
}

func (s *ConfigMapStateStore) Save(ctx context.Context, document Document) error {
	payload, err := EncodeDocument(document)
	if err != nil {
		return fmt.Errorf("encode state ConfigMap %s/%s: %w", s.namespace, s.name, err)
	}
	err = s.retry(ctx, func() error {
		configMap, getErr := s.client.Get(ctx, s.name, metav1.GetOptions{})
		if apierrors.IsNotFound(getErr) {
			_, getErr = s.createPayload(ctx, payload)
			return getErr
		}
		if getErr != nil {
			return getErr
		}
		// Never replace malformed persisted state with a seemingly fresh document.
		if _, decodeErr := decodeConfigMap(configMap); decodeErr != nil {
			return decodeErr
		}
		updated := configMap.DeepCopy()
		if updated.Data == nil {
			updated.Data = make(map[string]string)
		}
		updated.Data[ConfigMapDataKey] = string(payload)
		if capacity := s.observeCapacity(updated); capacity.Exceeded {
			return fmt.Errorf("%w: %d bytes exceeds %d", ErrStateCapacityExceeded, capacity.TotalBytes, capacity.LimitBytes)
		}
		_, updateErr := s.client.Update(ctx, updated, metav1.UpdateOptions{})
		return updateErr
	})
	if err != nil {
		return fmt.Errorf("save state ConfigMap %s/%s: %w", s.namespace, s.name, err)
	}
	return nil
}

func (s *ConfigMapStateStore) create(ctx context.Context, document Document) (*corev1.ConfigMap, error) {
	payload, err := EncodeDocument(document)
	if err != nil {
		return nil, err
	}
	return s.createPayload(ctx, payload)
}

func (s *ConfigMapStateStore) createPayload(ctx context.Context, payload []byte) (*corev1.ConfigMap, error) {
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: s.name, Namespace: s.namespace},
		Data:       map[string]string{ConfigMapDataKey: string(payload)},
	}
	if capacity := s.observeCapacity(configMap); capacity.Exceeded {
		return nil, fmt.Errorf("%w: %d bytes exceeds %d", ErrStateCapacityExceeded, capacity.TotalBytes, capacity.LimitBytes)
	}
	return s.client.Create(ctx, configMap, metav1.CreateOptions{})
}

func (s *ConfigMapStateStore) observeCapacity(configMap *corev1.ConfigMap) Capacity {
	stateBytes := 0
	totalBytes := 0
	if configMap != nil {
		for key, value := range configMap.Data {
			totalBytes += len(value)
			if key == ConfigMapDataKey {
				stateBytes = len(value)
			}
		}
		for _, value := range configMap.BinaryData {
			totalBytes += len(value)
		}
	}
	capacity := MeasureCapacity(stateBytes, totalBytes)
	if s.capacityObserver != nil {
		s.capacityObserver.ObserveStateCapacity(capacity)
	}
	return capacity
}

func decodeConfigMap(configMap *corev1.ConfigMap) (Document, error) {
	payload, ok := configMap.Data[ConfigMapDataKey]
	if !ok {
		return Document{}, ErrMissingStateData
	}
	return DecodeDocument([]byte(payload))
}

func (s *ConfigMapStateStore) retry(ctx context.Context, operation func() error) error {
	backoff := s.backoff
	if backoff.Steps < 1 {
		backoff.Steps = 1
	}
	if backoff.Factor < 1 {
		backoff.Factor = 1
	}
	delay := backoff.InitialDelay
	var lastErr error
	for attempt := 0; attempt < backoff.Steps; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		lastErr = operation()
		if lastErr == nil {
			return nil
		}
		if !apierrors.IsConflict(lastErr) && !apierrors.IsAlreadyExists(lastErr) {
			return lastErr
		}
		if attempt == backoff.Steps-1 {
			break
		}
		if err := s.sleep(ctx, delay); err != nil {
			return err
		}
		delay = time.Duration(float64(delay) * backoff.Factor)
		if backoff.MaxDelay > 0 && delay > backoff.MaxDelay {
			delay = backoff.MaxDelay
		}
	}
	return fmt.Errorf("%w: %v", ErrConflictRetriesExhausted, lastErr)
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
