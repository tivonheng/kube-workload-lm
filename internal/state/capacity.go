package state

import "errors"

const (
	// ConfigMapMaxDataBytes is the Kubernetes limit for the combined values in
	// Data and BinaryData. Keys and object metadata are not counted by the API.
	ConfigMapMaxDataBytes = 1 << 20
	// ConfigMapCapacityWarningBytes exposes an early warning before writes reach
	// the hard ConfigMap limit.
	ConfigMapCapacityWarningBytes = ConfigMapMaxDataBytes * 80 / 100
)

var ErrStateCapacityExceeded = errors.New("state ConfigMap capacity exceeded")

// Capacity describes the current state payload and complete ConfigMap data
// usage. It is intentionally label-free so metrics adapters can expose it
// without introducing workload-level cardinality.
type Capacity struct {
	StateBytes   int
	TotalBytes   int
	WarningBytes int
	LimitBytes   int
	NearLimit    bool
	Exceeded     bool
}

// CapacityObserver is the metrics/logging attachment point for state storage.
type CapacityObserver interface {
	ObserveStateCapacity(Capacity)
}

// CapacityObserverFunc adapts a function to CapacityObserver.
type CapacityObserverFunc func(Capacity)

func (f CapacityObserverFunc) ObserveStateCapacity(capacity Capacity) {
	f(capacity)
}

// MeasureCapacity calculates warning and hard-limit status. totalBytes must
// include every value in ConfigMap Data and BinaryData, not only states.json.
func MeasureCapacity(stateBytes, totalBytes int) Capacity {
	return Capacity{
		StateBytes: stateBytes, TotalBytes: totalBytes,
		WarningBytes: ConfigMapCapacityWarningBytes,
		LimitBytes:   ConfigMapMaxDataBytes,
		NearLimit:    totalBytes >= ConfigMapCapacityWarningBytes,
		Exceeded:     totalBytes > ConfigMapMaxDataBytes,
	}
}
