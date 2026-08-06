package state

import (
	"testing"
	"time"

	"gitlab.glb.osl-nucleus.com/devops/kube-workload-lifecycle-manager/internal/workload"
)

func TestCleanupDeletedProtectsLiveSameUIDSnapshot(t *testing.T) {
	document := validDocument()
	current := document.States["Deployment/test/api"]
	now := current.LastSeenAt.Add(30 * 24 * time.Hour)
	inventory := Inventory{
		workload.KindDeployment: {
			ListSucceeded: true,
			UIDs:          map[string]string{InventoryKey(current.Namespace, current.Name): current.WorkloadUID},
		},
	}

	// Policy mismatch, policy conflict, and HPA rejection are deliberately not
	// cleanup inputs: identity from the complete workload list is authoritative.
	for _, managementResult := range []string{"unmatched", "policy-conflict", "hpa-conflict"} {
		t.Run(managementResult, func(t *testing.T) {
			cleaned, stats, err := CleanupDeleted(document, inventory, now, 24*time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			if len(cleaned.States) != 1 || stats.Removed != 0 || stats.ProtectedSnapshots != 1 {
				t.Fatalf("live snapshot was not protected: states=%d stats=%#v", len(cleaned.States), stats)
			}
		})
	}
}

func TestCleanupDeletedRequiresSuccessfulListAndRetention(t *testing.T) {
	document := validDocument()
	current := document.States["Deployment/test/api"]
	now := current.LastSeenAt.Add(48 * time.Hour)

	t.Run("failed-list-cannot-delete", func(t *testing.T) {
		cleaned, stats, err := CleanupDeleted(document, Inventory{
			workload.KindDeployment: {ListSucceeded: false},
		}, now, 24*time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if len(cleaned.States) != 1 || stats.IncompleteInventory != 1 || stats.Removed != 0 {
			t.Fatalf("failed list triggered cleanup: states=%d stats=%#v", len(cleaned.States), stats)
		}
	})

	t.Run("missing-kind-cannot-delete", func(t *testing.T) {
		cleaned, stats, err := CleanupDeleted(document, Inventory{}, now, 24*time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if len(cleaned.States) != 1 || stats.IncompleteInventory != 1 {
			t.Fatalf("missing inventory triggered cleanup: states=%d stats=%#v", len(cleaned.States), stats)
		}
	})

	t.Run("confirmed-deletion-after-retention", func(t *testing.T) {
		cleaned, stats, err := CleanupDeleted(document, Inventory{
			workload.KindDeployment: {ListSucceeded: true, UIDs: map[string]string{}},
		}, now, 24*time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if len(cleaned.States) != 0 || stats.Removed != 1 {
			t.Fatalf("confirmed stale deletion was retained: states=%d stats=%#v", len(cleaned.States), stats)
		}
		if len(document.States) != 1 {
			t.Fatal("cleanup mutated the caller-owned document")
		}
	})

	t.Run("retention-not-elapsed", func(t *testing.T) {
		cleaned, stats, err := CleanupDeleted(document, Inventory{
			workload.KindDeployment: {ListSucceeded: true, UIDs: map[string]string{}},
		}, current.LastSeenAt.Add(23*time.Hour), 24*time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if len(cleaned.States) != 1 || stats.Retained != 1 {
			t.Fatalf("young deleted state was removed: states=%d stats=%#v", len(cleaned.States), stats)
		}
	})
}
