package lifecycle

import (
	"testing"

	"gitlab.glb.osl-nucleus.com/devops/kube-workload-lifecycle-manager/internal/workload"
)

func TestCalculateRevisionAllContainersIsCanonical(t *testing.T) {
	first, err := CalculateRevision([]workload.Container{{Name: "sidecar", Image: "proxy:v1"}, {Name: "app", Image: "app:v1"}}, TrackingSpec{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := CalculateRevision([]workload.Container{{Name: "app", Image: "app:v1"}, {Name: "sidecar", Image: "proxy:v1"}}, TrackingSpec{})
	if err != nil {
		t.Fatal(err)
	}
	if first.RevisionHash != second.RevisionHash || first.TrackingSpecHash != second.TrackingSpecHash {
		t.Fatal("hashes must not depend on workload container order")
	}
	if first.Containers[0].Name != "app" || first.Containers[1].Name != "sidecar" {
		t.Fatalf("containers are not canonical: %#v", first.Containers)
	}
}

func TestCalculateRevisionNamedContainersIgnoreSidecar(t *testing.T) {
	spec := TrackingSpec{Source: SourceContainerImages, Containers: []string{"app"}}
	first, err := CalculateRevision([]workload.Container{{Name: "app", Image: "app:v1"}, {Name: "sidecar", Image: "proxy:v1"}}, spec)
	if err != nil {
		t.Fatal(err)
	}
	second, err := CalculateRevision([]workload.Container{{Name: "app", Image: "app:v1"}, {Name: "sidecar", Image: "proxy:v2"}}, spec)
	if err != nil {
		t.Fatal(err)
	}
	if first.RevisionHash != second.RevisionHash {
		t.Fatal("untracked sidecar changed revision hash")
	}
	if _, err := CalculateRevision([]workload.Container{{Name: "sidecar", Image: "proxy:v1"}}, spec); err == nil {
		t.Fatal("expected missing tracked container error")
	}
}
