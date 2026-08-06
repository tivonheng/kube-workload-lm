package policy

import (
	"testing"
)

func TestManagerKeepsLastGoodSnapshot(t *testing.T) {
	manager := &Manager{}
	if _, ok := manager.Current(); ok {
		t.Fatal("empty manager reported a snapshot")
	}
	if err := manager.Reload([]byte(validPolicyYAML)); err != nil {
		t.Fatal(err)
	}
	first, ok := manager.Current()
	if !ok || first.Policies[0].Name != "temporary" {
		t.Fatalf("missing valid snapshot: %#v", first)
	}
	first.Policies[0].Target.Kinds[0] = "mutated"
	first.Policies[0].Target.Selector.MatchLabels["mutated"] = "true"
	if err := manager.Reload([]byte("invalid: true")); err == nil {
		t.Fatal("invalid reload succeeded")
	}
	current, ok := manager.Current()
	if !ok || current.Policies[0].Target.Kinds[0] != "Deployment" || current.Policies[0].Target.Selector.MatchLabels["mutated"] != "" {
		t.Fatalf("last-good snapshot was mutated: %#v", current)
	}
}
