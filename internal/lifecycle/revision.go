package lifecycle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"gitlab.glb.osl-nucleus.com/devops/kube-workload-lifecycle-manager/internal/workload"
)

const SourceContainerImages = "ContainerImages"

type TrackingSpec struct {
	Source     string   `json:"source" yaml:"source"`
	Containers []string `json:"containers,omitempty" yaml:"containers,omitempty"`
}

type Revision struct {
	TrackingSpecHash string               `json:"trackingSpecHash"`
	RevisionHash     string               `json:"revisionHash"`
	Containers       []workload.Container `json:"containers"`
}

type canonicalTracking struct {
	Version    int      `json:"version"`
	Source     string   `json:"source"`
	Mode       string   `json:"mode"`
	Containers []string `json:"containers"`
}

func CalculateRevision(containers []workload.Container, spec TrackingSpec) (Revision, error) {
	if spec.Source == "" {
		spec.Source = SourceContainerImages
	}
	if spec.Source != SourceContainerImages {
		return Revision{}, fmt.Errorf("unsupported revision source %q", spec.Source)
	}
	selected, names, mode, err := selectContainers(containers, spec.Containers)
	if err != nil {
		return Revision{}, err
	}
	trackingHash, err := hashJSON(canonicalTracking{1, spec.Source, mode, names})
	if err != nil {
		return Revision{}, err
	}
	revisionHash, err := hashJSON(selected)
	if err != nil {
		return Revision{}, err
	}
	return Revision{trackingHash, revisionHash, selected}, nil
}

func selectContainers(containers []workload.Container, tracked []string) ([]workload.Container, []string, string, error) {
	available := make(map[string]workload.Container, len(containers))
	for _, container := range containers {
		if container.Name == "" || container.Image == "" {
			return nil, nil, "", errors.New("container name and image must be non-empty")
		}
		if _, exists := available[container.Name]; exists {
			return nil, nil, "", fmt.Errorf("duplicate workload container %q", container.Name)
		}
		available[container.Name] = container
	}
	if len(available) == 0 {
		return nil, nil, "", errors.New("workload has no regular containers")
	}
	if tracked != nil && len(tracked) == 0 {
		return nil, nil, "", errors.New("tracked containers must be omitted or non-empty")
	}

	mode := "AllContainers"
	names := make([]string, 0, len(available))
	if tracked == nil {
		for name := range available {
			names = append(names, name)
		}
	} else {
		mode = "NamedContainers"
		names = append(names, tracked...)
	}
	sort.Strings(names)
	selected := make([]workload.Container, 0, len(names))
	for index, name := range names {
		if index > 0 && names[index-1] == name {
			return nil, nil, "", fmt.Errorf("duplicate tracked container %q", name)
		}
		container, exists := available[name]
		if !exists {
			return nil, nil, "", fmt.Errorf("tracked container %q not found", name)
		}
		selected = append(selected, container)
	}
	return selected, names, mode, nil
}

func hashJSON(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("marshal canonical value: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
