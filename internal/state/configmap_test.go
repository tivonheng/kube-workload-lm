package state

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestConfigMapStateStoreInitializesMissingConfigMap(t *testing.T) {
	client := &fakeConfigMapClient{}
	store := NewConfigMapStateStore(client, "controller", "lifecycle-state")

	document, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if document.FormatVersion != FormatVersion || document.States == nil || len(document.States) != 0 {
		t.Fatalf("unexpected initial document: %#v", document)
	}
	if client.createCalls != 1 || client.configMap == nil {
		t.Fatalf("missing ConfigMap was not created: calls=%d", client.createCalls)
	}
	persisted, err := decodeConfigMap(client.configMap)
	if err != nil || persisted.FormatVersion != FormatVersion {
		t.Fatalf("created ConfigMap has invalid states.json: %#v, %v", client.configMap.Data, err)
	}
}

func TestConfigMapStateStoreStrictlyRejectsInvalidExistingState(t *testing.T) {
	tests := map[string]map[string]string{
		"missing-key": {},
		"malformed":   {ConfigMapDataKey: `{"formatVersion":1`},
		"unsupported": {ConfigMapDataKey: `{"formatVersion":2,"states":{}}`},
	}
	for name, data := range tests {
		t.Run(name, func(t *testing.T) {
			client := &fakeConfigMapClient{configMap: existingConfigMap(data, "3")}
			store := NewConfigMapStateStore(client, "controller", "lifecycle-state")
			if _, err := store.Load(context.Background()); err == nil {
				t.Fatal("invalid persisted state was accepted")
			}
			if client.createCalls != 0 || client.updateCalls != 0 {
				t.Fatalf("invalid state was silently replaced: creates=%d updates=%d", client.createCalls, client.updateCalls)
			}
		})
	}
}

func TestConfigMapStateStoreSaveUsesLatestResourceVersionAfterConflict(t *testing.T) {
	payload, err := EncodeDocument(NewDocument())
	if err != nil {
		t.Fatal(err)
	}
	client := &fakeConfigMapClient{
		configMap:          existingConfigMap(map[string]string{ConfigMapDataKey: string(payload), "owner": "keep"}, "7"),
		conflictsRemaining: 1,
	}
	store := NewConfigMapStateStore(client, "controller", "lifecycle-state")
	store.backoff = ConflictBackoff{Steps: 3, InitialDelay: time.Millisecond, Factor: 2}
	var delays []time.Duration
	store.sleep = func(_ context.Context, delay time.Duration) error {
		delays = append(delays, delay)
		return nil
	}

	if err := store.Save(context.Background(), validDocument()); err != nil {
		t.Fatal(err)
	}
	if got, want := client.updateResourceVersions, []string{"7", "8"}; !equalStrings(got, want) {
		t.Fatalf("updates did not use refreshed resourceVersion: got %v want %v", got, want)
	}
	if len(delays) != 1 || delays[0] != time.Millisecond {
		t.Fatalf("unexpected conflict backoff: %v", delays)
	}
	if client.configMap.Data["owner"] != "keep" {
		t.Fatal("save discarded unrelated ConfigMap data")
	}
	persisted, err := decodeConfigMap(client.configMap)
	if err != nil || len(persisted.States) != 1 {
		t.Fatalf("saved document was not persisted: %#v, %v", persisted, err)
	}
}

func TestConfigMapStateStoreConflictBackoffIsBoundedAndCancelable(t *testing.T) {
	payload, err := EncodeDocument(NewDocument())
	if err != nil {
		t.Fatal(err)
	}
	t.Run("bounded", func(t *testing.T) {
		client := &fakeConfigMapClient{configMap: existingConfigMap(map[string]string{ConfigMapDataKey: string(payload)}, "1"), alwaysConflict: true}
		store := NewConfigMapStateStore(client, "controller", "state")
		store.backoff = ConflictBackoff{Steps: 3, InitialDelay: time.Millisecond, Factor: 2}
		store.sleep = func(context.Context, time.Duration) error { return nil }
		err := store.Save(context.Background(), validDocument())
		if !errors.Is(err, ErrConflictRetriesExhausted) || client.updateCalls != 3 {
			t.Fatalf("retry was not bounded: calls=%d err=%v", client.updateCalls, err)
		}
	})
	t.Run("cancelable", func(t *testing.T) {
		client := &fakeConfigMapClient{configMap: existingConfigMap(map[string]string{ConfigMapDataKey: string(payload)}, "1"), alwaysConflict: true}
		store := NewConfigMapStateStore(client, "controller", "state")
		store.backoff = ConflictBackoff{Steps: 3, InitialDelay: time.Hour, Factor: 2}
		ctx, cancel := context.WithCancel(context.Background())
		store.sleep = func(ctx context.Context, delay time.Duration) error {
			cancel()
			return sleepContext(ctx, delay)
		}
		err := store.Save(ctx, validDocument())
		if !errors.Is(err, context.Canceled) || client.updateCalls != 1 {
			t.Fatalf("cancellation did not stop retries: calls=%d err=%v", client.updateCalls, err)
		}
	})
}

func TestConfigMapStateStoreSaveDoesNotOverwriteInvalidState(t *testing.T) {
	client := &fakeConfigMapClient{configMap: existingConfigMap(map[string]string{ConfigMapDataKey: `null`}, "4")}
	store := NewConfigMapStateStore(client, "controller", "state")
	err := store.Save(context.Background(), validDocument())
	if !errors.Is(err, ErrInvalidDocument) || client.updateCalls != 0 {
		t.Fatalf("invalid state should block save without update: calls=%d err=%v", client.updateCalls, err)
	}
}

type fakeConfigMapClient struct {
	configMap              *corev1.ConfigMap
	getCalls               int
	createCalls            int
	updateCalls            int
	conflictsRemaining     int
	alwaysConflict         bool
	updateResourceVersions []string
}

var configMapResource = schema.GroupResource{Resource: "configmaps"}

func (f *fakeConfigMapClient) Get(ctx context.Context, name string, _ metav1.GetOptions) (*corev1.ConfigMap, error) {
	f.getCalls++
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.configMap == nil {
		return nil, apierrors.NewNotFound(configMapResource, name)
	}
	return f.configMap.DeepCopy(), nil
}

func (f *fakeConfigMapClient) Create(ctx context.Context, configMap *corev1.ConfigMap, _ metav1.CreateOptions) (*corev1.ConfigMap, error) {
	f.createCalls++
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.configMap != nil {
		return nil, apierrors.NewAlreadyExists(configMapResource, configMap.Name)
	}
	f.configMap = configMap.DeepCopy()
	f.configMap.ResourceVersion = "1"
	return f.configMap.DeepCopy(), nil
}

func (f *fakeConfigMapClient) Update(ctx context.Context, configMap *corev1.ConfigMap, _ metav1.UpdateOptions) (*corev1.ConfigMap, error) {
	f.updateCalls++
	f.updateResourceVersions = append(f.updateResourceVersions, configMap.ResourceVersion)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.configMap == nil {
		return nil, apierrors.NewNotFound(configMapResource, configMap.Name)
	}
	if configMap.ResourceVersion != f.configMap.ResourceVersion {
		return nil, apierrors.NewConflict(configMapResource, configMap.Name, errors.New("stale resourceVersion"))
	}
	if f.alwaysConflict || f.conflictsRemaining > 0 {
		if f.conflictsRemaining > 0 {
			f.conflictsRemaining--
		}
		f.configMap.ResourceVersion = nextResourceVersion(f.configMap.ResourceVersion)
		return nil, apierrors.NewConflict(configMapResource, configMap.Name, errors.New("simulated concurrent update"))
	}
	f.configMap = configMap.DeepCopy()
	f.configMap.ResourceVersion = nextResourceVersion(configMap.ResourceVersion)
	return f.configMap.DeepCopy(), nil
}

func existingConfigMap(data map[string]string, resourceVersion string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "lifecycle-state", Namespace: "controller", ResourceVersion: resourceVersion},
		Data:       data,
	}
}

func nextResourceVersion(current string) string {
	value, _ := strconv.Atoi(current)
	return strconv.Itoa(value + 1)
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
