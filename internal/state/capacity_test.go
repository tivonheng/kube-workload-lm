package state

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestMeasureCapacityExposesWarningAndHardLimit(t *testing.T) {
	tests := []struct {
		name      string
		total     int
		nearLimit bool
		exceeded  bool
	}{
		{name: "below-warning", total: ConfigMapCapacityWarningBytes - 1},
		{name: "at-warning", total: ConfigMapCapacityWarningBytes, nearLimit: true},
		{name: "at-limit", total: ConfigMapMaxDataBytes, nearLimit: true},
		{name: "over-limit", total: ConfigMapMaxDataBytes + 1, nearLimit: true, exceeded: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			capacity := MeasureCapacity(123, test.total)
			if capacity.StateBytes != 123 || capacity.TotalBytes != test.total ||
				capacity.NearLimit != test.nearLimit || capacity.Exceeded != test.exceeded {
				t.Fatalf("unexpected capacity: %#v", capacity)
			}
		})
	}
}

func TestConfigMapStateStoreReportsNearCapacityOnLoad(t *testing.T) {
	payload, err := EncodeDocument(NewDocument())
	if err != nil {
		t.Fatal(err)
	}
	padding := strings.Repeat("x", ConfigMapCapacityWarningBytes-len(payload))
	client := &fakeConfigMapClient{configMap: existingConfigMap(map[string]string{
		ConfigMapDataKey: string(payload), "padding": padding,
	}, "1")}
	store := NewConfigMapStateStore(client, "controller", "state")
	var observed Capacity
	store.SetCapacityObserver(CapacityObserverFunc(func(capacity Capacity) { observed = capacity }))

	if _, err := store.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !observed.NearLimit || observed.Exceeded || observed.TotalBytes != ConfigMapCapacityWarningBytes {
		t.Fatalf("near-limit capacity was not exposed: %#v", observed)
	}
}
func TestConfigMapStateStoreRejectsWritesOverCapacity(t *testing.T) {
	t.Run("state-payload", func(t *testing.T) {
		document := validDocument()
		current := document.States["Deployment/test/api"]
		current.Revision.Containers[0].Image = strings.Repeat("x", ConfigMapMaxDataBytes)
		document.States[current.Key()] = current
		client := &fakeConfigMapClient{}
		store := NewConfigMapStateStore(client, "controller", "state")
		var observed Capacity
		store.SetCapacityObserver(CapacityObserverFunc(func(capacity Capacity) { observed = capacity }))

		err := store.Save(context.Background(), document)
		if !errors.Is(err, ErrStateCapacityExceeded) || client.createCalls != 0 {
			t.Fatalf("oversized create was attempted: creates=%d err=%v", client.createCalls, err)
		}
		if !observed.Exceeded || observed.StateBytes <= ConfigMapMaxDataBytes {
			t.Fatalf("oversized state was not reported: %#v", observed)
		}
	})

	t.Run("combined-data", func(t *testing.T) {
		payload, err := EncodeDocument(NewDocument())
		if err != nil {
			t.Fatal(err)
		}
		client := &fakeConfigMapClient{configMap: existingConfigMap(map[string]string{
			ConfigMapDataKey: string(payload),
			"unrelated":      strings.Repeat("x", ConfigMapMaxDataBytes),
		}, "1")}
		store := NewConfigMapStateStore(client, "controller", "state")

		err = store.Save(context.Background(), validDocument())
		if !errors.Is(err, ErrStateCapacityExceeded) || client.updateCalls != 0 {
			t.Fatalf("combined oversized update was attempted: updates=%d err=%v", client.updateCalls, err)
		}
	})
}
