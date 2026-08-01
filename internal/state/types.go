package state

import (
	"context"
	"time"

	"gitlab.glb.osl-nucleus.com/devops/kube-workload-lifecycle-manager/internal/workload"
)

const (
	// FormatVersion is the only state document format supported by this build.
	FormatVersion = 1
)

type ReplicaSnapshot struct {
	Replicas       int32     `json:"replicas"`
	CapturedAt     time.Time `json:"capturedAt"`
	CapturedReason string    `json:"capturedReason"`
}

// Revision is the auditable, canonical input used to calculate RevisionHash.
type Revision struct {
	Source     string               `json:"source"`
	Containers []workload.Container `json:"containers"`
}

type WorkloadState struct {
	Kind             workload.Kind    `json:"kind"`
	Namespace        string           `json:"namespace"`
	Name             string           `json:"name"`
	WorkloadUID      string           `json:"workloadUID"`
	LastPolicyName   string           `json:"lastPolicyName"`
	TrackingSpecHash string           `json:"trackingSpecHash"`
	RevisionHash     string           `json:"revisionHash"`
	Revision         Revision         `json:"revision"`
	FirstSeenAt      time.Time        `json:"firstSeenAt"`
	LastSeenAt       time.Time        `json:"lastSeenAt"`
	ReplicaSnapshot  *ReplicaSnapshot `json:"replicaSnapshot,omitempty"`

	// WindowEntryRevision 记录进入 downWindow 时（首次缩容时）的 RevisionHash。
	// 仅在 downWindow 内有值，窗口外清空。
	WindowEntryRevision string `json:"windowEntryRevision,omitempty"`

	// WindowInstanceID 标识当前 downWindow 的具体周期实例。
	// 格式: "{windowName}:{YYYY-MM-DD}" 其中日期为窗口开始日。
	// 用于区分同名窗口的不同周期（如每日夜间窗口的周一实例和周二实例）。
	WindowInstanceID string `json:"windowInstanceID,omitempty"`

	// ScaleDownSkipped marks this workload as exempt from scheduled downscaling
	// for the current window instance.
	ScaleDownSkipped bool `json:"scaleDownSkipped,omitempty"`

	// ScaleDownSkipReason records why scheduled downscaling was skipped so the
	// decision remains observable across reconciles and controller restarts.
	ScaleDownSkipReason string `json:"scaleDownSkipReason,omitempty"`
}

func (s WorkloadState) Key() string {
	return string(s.Kind) + "/" + s.Namespace + "/" + s.Name
}

// Document is the versioned payload persisted by a StateStore.
type Document struct {
	FormatVersion int                      `json:"formatVersion"`
	States        map[string]WorkloadState `json:"states"`
}

// NewDocument returns an initialized document using the current format.
func NewDocument() Document {
	return Document{FormatVersion: FormatVersion, States: make(map[string]WorkloadState)}
}

// StateStore abstracts durable, whole-document state persistence. Save must not
// report success until the document is durable. Implementations are responsible
// for their backend's concurrency control; callers must treat any error as a
// failed state transition and must not proceed with a dependent scale change.
type StateStore interface {
	Load(context.Context) (Document, error)
	Save(context.Context, Document) error
}

// Store is retained as a source-compatible alias for the initial scaffold.
// New code should use StateStore.
type Store = StateStore
