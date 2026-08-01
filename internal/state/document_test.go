package state

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"gitlab.glb.osl-nucleus.com/devops/kube-workload-lifecycle-manager/internal/lifecycle"
	"gitlab.glb.osl-nucleus.com/devops/kube-workload-lifecycle-manager/internal/workload"
)

const testHash = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

type testStore struct{}

func (testStore) Load(context.Context) (Document, error) { return NewDocument(), nil }
func (testStore) Save(context.Context, Document) error   { return nil }

var _ StateStore = testStore{}
var _ Store = testStore{}

func TestDocumentRoundTripUsesVersionedUTCJSON(t *testing.T) {
	location := time.FixedZone("UTC+8", 8*60*60)
	document := validDocument()
	state := document.States["Deployment/test/api"]
	state.FirstSeenAt = time.Date(2026, 8, 1, 12, 0, 0, 0, location)
	state.LastSeenAt = time.Date(2026, 8, 1, 13, 0, 0, 0, location)
	state.ReplicaSnapshot.CapturedAt = time.Date(2026, 8, 1, 12, 30, 0, 0, location)
	document.States[state.Key()] = state

	encoded, err := EncodeDocument(document)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, want := range []string{`"formatVersion":1`, `"replicas":0`, `"firstSeenAt":"2026-08-01T04:00:00Z"`, `"capturedAt":"2026-08-01T04:30:00Z"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("encoded document does not contain %s: %s", want, text)
		}
	}
	if state.FirstSeenAt.Location() != location {
		t.Fatal("encoding mutated caller-owned state")
	}

	decoded, err := DecodeDocument(encoded)
	if err != nil {
		t.Fatal(err)
	}
	got := decoded.States[state.Key()]
	if decoded.FormatVersion != FormatVersion || got.ReplicaSnapshot == nil || got.ReplicaSnapshot.Replicas != 0 {
		t.Fatalf("snapshot presence or document version was lost: %#v", decoded)
	}
	if got.FirstSeenAt.Location() != time.UTC || got.Revision.Source != lifecycle.SourceContainerImages {
		t.Fatalf("decoded state is not canonical: %#v", got)
	}
}
func TestDecodeDocumentRejectsInvalidStateInsteadOfReinitializing(t *testing.T) {
	valid, err := EncodeDocument(validDocument())
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string][]byte{
		"empty-object":        []byte(`{}`),
		"unsupported-version": []byte(strings.Replace(string(valid), `"formatVersion":1`, `"formatVersion":2`, 1)),
		"unknown-field":       []byte(strings.Replace(string(valid), `"states":{`, `"unexpected":true,"states":{`, 1)),
		"null-states":         []byte(strings.Replace(string(valid), string(valid[strings.Index(string(valid), `"states":`)+len(`"states":`):]), `null}`, 1)),
		"wrong-key":           []byte(strings.Replace(string(valid), `Deployment/test/api`, `Deployment/test/other`, 1)),
		"non-utc-time":        []byte(strings.Replace(string(valid), `2026-08-01T06:00:00Z`, `2026-08-01T14:00:00+08:00`, 1)),
		"bad-hash":            []byte(strings.Replace(string(valid), testHash, `sha256:not-a-digest`, 1)),
		"negative-snapshot":   []byte(strings.Replace(string(valid), `"replicas":0`, `"replicas":-1`, 1)),
		"trailing-value":      append(append([]byte(nil), valid...), []byte(` {}`)...),
		"malformed":           []byte(`{"formatVersion":1`),
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			document, err := DecodeDocument(input)
			if err == nil {
				t.Fatalf("invalid state decoded as %#v", document)
			}
			if !errors.Is(err, ErrInvalidDocument) {
				t.Fatalf("error must identify invalid persisted state: %v", err)
			}
		})
	}

	_, err = DecodeDocument(tests["unsupported-version"])
	if !errors.Is(err, ErrUnsupportedFormatVersion) {
		t.Fatalf("unsupported version must remain distinguishable: %v", err)
	}
}

func TestDocumentRejectsNonCanonicalRevisionAndInvalidSnapshot(t *testing.T) {
	tests := map[string]func(*Document){
		"unordered-containers": func(document *Document) {
			state := document.States["Deployment/test/api"]
			state.Revision.Containers[0], state.Revision.Containers[1] = state.Revision.Containers[1], state.Revision.Containers[0]
			document.States[state.Key()] = state
		},
		"missing-snapshot-reason": func(document *Document) {
			state := document.States["Deployment/test/api"]
			state.ReplicaSnapshot.CapturedReason = ""
			document.States[state.Key()] = state
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			document := validDocument()
			mutate(&document)
			if _, err := json.Marshal(document); !errors.Is(err, ErrInvalidDocument) {
				t.Fatalf("expected invalid document error, got %v", err)
			}
		})
	}
}
func TestNewDocumentIsImmediatelyValidAndOwnsItsMap(t *testing.T) {
	first := NewDocument()
	second := NewDocument()
	first.States["extra"] = WorkloadState{}
	if second.FormatVersion != FormatVersion || second.States == nil || len(second.States) != 0 {
		t.Fatalf("documents must have independent initialized maps: %#v", second)
	}
	if err := ValidateDocument(second); err != nil {
		t.Fatalf("new document must be valid: %v", err)
	}
}

func validDocument() Document {
	firstSeen := time.Date(2026, 8, 1, 6, 0, 0, 0, time.UTC)
	state := WorkloadState{
		Kind:             workload.KindDeployment,
		Namespace:        "test",
		Name:             "api",
		WorkloadUID:      "uid-1",
		LastPolicyName:   "temporary-workloads-3d",
		TrackingSpecHash: testHash,
		RevisionHash:     testHash,
		Revision: Revision{
			Source: lifecycle.SourceContainerImages,
			Containers: []workload.Container{
				{Name: "app", Image: "registry.example.com/app:v1"},
				{Name: "sidecar", Image: "registry.example.com/sidecar:v1"},
			},
		},
		FirstSeenAt: firstSeen,
		LastSeenAt:  firstSeen.Add(time.Hour),
		ReplicaSnapshot: &ReplicaSnapshot{
			Replicas:       0,
			CapturedAt:     firstSeen.Add(30 * time.Minute),
			CapturedReason: "scale-down-window:night",
		},
	}
	return Document{
		FormatVersion: FormatVersion,
		States:        map[string]WorkloadState{state.Key(): state},
	}
}
