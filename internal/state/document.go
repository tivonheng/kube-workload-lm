package state

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"gitlab.glb.osl-nucleus.com/devops/kube-workload-lifecycle-manager/internal/lifecycle"
	"gitlab.glb.osl-nucleus.com/devops/kube-workload-lifecycle-manager/internal/workload"
)

var (
	ErrInvalidDocument          = errors.New("invalid state document")
	ErrUnsupportedFormatVersion = errors.New("unsupported state format version")
)

type wireDocument Document

// EncodeDocument validates a document and emits its canonical JSON form. All
// timestamps are converted to UTC before persistence without mutating input.
func EncodeDocument(document Document) ([]byte, error) {
	return json.Marshal(document)
}

// DecodeDocument strictly decodes and validates one complete state document.
// It never substitutes an empty document for malformed or unsupported state.
func DecodeDocument(data []byte) (Document, error) {
	var document Document
	if err := json.Unmarshal(data, &document); err != nil {
		if errors.Is(err, ErrInvalidDocument) {
			return Document{}, err
		}
		return Document{}, fmt.Errorf("%w: decode: %v", ErrInvalidDocument, err)
	}
	return document, nil
}

func (d Document) MarshalJSON() ([]byte, error) {
	normalized := normalizeDocument(d)
	if err := ValidateDocument(normalized); err != nil {
		return nil, err
	}
	return json.Marshal(wireDocument(normalized))
}

func (d *Document) UnmarshalJSON(data []byte) error {
	if d == nil {
		return fmt.Errorf("%w: nil decode target", ErrInvalidDocument)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var decoded wireDocument
	if err := decoder.Decode(&decoded); err != nil {
		return fmt.Errorf("%w: decode: %v", ErrInvalidDocument, err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("%w: multiple JSON values", ErrInvalidDocument)
		}
		return fmt.Errorf("%w: trailing data: %v", ErrInvalidDocument, err)
	}
	document := Document(decoded)
	if err := ValidateDocument(document); err != nil {
		return err
	}
	*d = normalizeDocument(document)
	return nil
}

// ValidateDocument verifies the complete persisted-state invariant.
func ValidateDocument(document Document) error {
	if document.FormatVersion != FormatVersion {
		return fmt.Errorf("%w: %w: got %d, want %d", ErrInvalidDocument, ErrUnsupportedFormatVersion, document.FormatVersion, FormatVersion)
	}
	if document.States == nil {
		return fmt.Errorf("%w: states must be an object", ErrInvalidDocument)
	}
	for key, state := range document.States {
		if err := validateWorkloadState(key, state); err != nil {
			return fmt.Errorf("%w: state %q: %v", ErrInvalidDocument, key, err)
		}
	}
	return nil
}

func validateWorkloadState(key string, state WorkloadState) error {
	if state.Kind != workload.KindDeployment && state.Kind != workload.KindStatefulSet {
		return fmt.Errorf("unsupported kind %q", state.Kind)
	}
	if state.Namespace == "" || state.Name == "" || state.WorkloadUID == "" {
		return errors.New("namespace, name, and workloadUID must be non-empty")
	}
	if key != state.Key() {
		return fmt.Errorf("key does not match identity %q", state.Key())
	}
	if state.LastPolicyName == "" {
		return errors.New("lastPolicyName must be non-empty")
	}
	if err := validateHash("trackingSpecHash", state.TrackingSpecHash); err != nil {
		return err
	}
	if err := validateHash("revisionHash", state.RevisionHash); err != nil {
		return err
	}
	if state.Revision.Source != lifecycle.SourceContainerImages {
		return fmt.Errorf("unsupported revision source %q", state.Revision.Source)
	}
	if len(state.Revision.Containers) == 0 {
		return errors.New("revision containers must be non-empty")
	}
	previousName := ""
	for _, container := range state.Revision.Containers {
		if container.Name == "" || container.Image == "" {
			return errors.New("revision container name and image must be non-empty")
		}
		if previousName != "" && container.Name <= previousName {
			return errors.New("revision containers must be uniquely sorted by name")
		}
		previousName = container.Name
	}
	if err := validateUTCTime("firstSeenAt", state.FirstSeenAt); err != nil {
		return err
	}
	if err := validateUTCTime("lastSeenAt", state.LastSeenAt); err != nil {
		return err
	}
	if state.LastSeenAt.Before(state.FirstSeenAt) {
		return errors.New("lastSeenAt must not be before firstSeenAt")
	}
	if state.ReplicaSnapshot != nil {
		if state.ReplicaSnapshot.Replicas < 0 {
			return errors.New("replicaSnapshot.replicas must be non-negative")
		}
		if err := validateUTCTime("replicaSnapshot.capturedAt", state.ReplicaSnapshot.CapturedAt); err != nil {
			return err
		}
		if state.ReplicaSnapshot.CapturedReason == "" {
			return errors.New("replicaSnapshot.capturedReason must be non-empty")
		}
	}
	return nil
}
func validateHash(field, value string) error {
	const prefix = "sha256:"
	if !strings.HasPrefix(value, prefix) || len(value) != len(prefix)+64 {
		return fmt.Errorf("%s must be sha256 followed by 64 lowercase hexadecimal characters", field)
	}
	digest := strings.TrimPrefix(value, prefix)
	if digest != strings.ToLower(digest) {
		return fmt.Errorf("%s must use lowercase hexadecimal", field)
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return fmt.Errorf("%s has invalid hexadecimal digest", field)
	}
	return nil
}

func validateUTCTime(field string, value time.Time) error {
	if value.IsZero() {
		return fmt.Errorf("%s must be set", field)
	}
	_, offset := value.Zone()
	if offset != 0 {
		return fmt.Errorf("%s must use UTC", field)
	}
	return nil
}

func normalizeDocument(document Document) Document {
	normalized := document
	if document.States == nil {
		return normalized
	}
	normalized.States = make(map[string]WorkloadState, len(document.States))
	for key, original := range document.States {
		state := original
		state.FirstSeenAt = state.FirstSeenAt.UTC()
		state.LastSeenAt = state.LastSeenAt.UTC()
		state.Revision.Containers = append([]workload.Container(nil), state.Revision.Containers...)
		if original.ReplicaSnapshot != nil {
			snapshot := *original.ReplicaSnapshot
			snapshot.CapturedAt = snapshot.CapturedAt.UTC()
			state.ReplicaSnapshot = &snapshot
		}
		normalized.States[key] = state
	}
	return normalized
}
