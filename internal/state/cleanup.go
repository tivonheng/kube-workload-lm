package state

import (
	"errors"
	"fmt"
	"time"

	"gitlab.glb.osl-nucleus.com/devops/kube-workload-lifecycle-manager/internal/workload"
)

// KindInventory is authoritative only when ListSucceeded is true. Callers must
// leave it false after any List/API failure; its contents are then ignored.
type KindInventory struct {
	ListSucceeded bool
	UIDs          map[string]string
}

// Inventory contains complete-list results by supported workload kind. A
// missing kind is incomplete and can never authorize deletion.
type Inventory map[workload.Kind]KindInventory

// CleanupStats describes a cleanup pass without workload-level labels.
type CleanupStats struct {
	Removed             int
	Retained            int
	ProtectedSnapshots  int
	IncompleteInventory int
}

// InventoryKey returns the key used inside KindInventory.UIDs.
func InventoryKey(namespace, name string) string {
	return namespace + "/" + name
}

// CleanupDeleted removes state only when a successful complete list confirms
// that the same UID no longer exists and lastSeenAt is outside the configured
// retention period. The input document is never mutated.
func CleanupDeleted(document Document, inventory Inventory, now time.Time, retention time.Duration) (Document, CleanupStats, error) {
	var stats CleanupStats
	if err := ValidateDocument(document); err != nil {
		return Document{}, stats, err
	}
	if now.IsZero() {
		return Document{}, stats, errors.New("cleanup time must be set")
	}
	if retention < 0 {
		return Document{}, stats, errors.New("cleanup retention must be non-negative")
	}

	cleaned := normalizeDocument(document)
	for key, current := range document.States {
		listed, complete := inventory[current.Kind]
		if !complete || !listed.ListSucceeded {
			stats.Retained++
			stats.IncompleteInventory++
			continue
		}
		liveUID, exists := listed.UIDs[InventoryKey(current.Namespace, current.Name)]
		if exists && liveUID == "" {
			return Document{}, CleanupStats{}, fmt.Errorf("inventory contains empty UID for %s", key)
		}
		if exists && liveUID == current.WorkloadUID {
			stats.Retained++
			if current.ReplicaSnapshot != nil {
				stats.ProtectedSnapshots++
			}
			continue
		}
		if now.Before(current.LastSeenAt.Add(retention)) {
			stats.Retained++
			continue
		}
		delete(cleaned.States, key)
		stats.Removed++
	}
	return cleaned, stats, nil
}
