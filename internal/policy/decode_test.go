package policy

import (
	"strings"
	"testing"
	"time"

	"gitlab.glb.osl-nucleus.com/devops/kube-workload-lifecycle-manager/internal/lifecycle"
)

func TestDecodeValidPolicy(t *testing.T) {
	set, err := Decode([]byte(validPolicyYAML))
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Policies) != 1 || set.Policies[0].Lifecycle.MaxAge != 72*time.Hour {
		t.Fatalf("unexpected compiled policy: %#v", set)
	}
	policy := set.Policies[0]
	if policy.Lifecycle.Revision.Source != lifecycle.SourceContainerImages || len(policy.Lifecycle.Revision.Containers) != 1 {
		t.Fatalf("unexpected revision defaults: %#v", policy.Lifecycle.Revision)
	}
	if policy.Schedule == nil || policy.Schedule.TimeZone != "Asia/Shanghai" {
		t.Fatalf("unexpected schedule: %#v", policy.Schedule)
	}
}

func TestDecodeAppliesDefaults(t *testing.T) {
	minimal := `apiVersion: lifecycle.example.com/v1alpha1
kind: WorkloadLifecyclePolicySet
spec:
  policies:
    - name: defaults
      priority: 0
      target:
        kinds: [Deployment]
        selector:
          matchLabels: {app: demo}
`
	set, err := Decode([]byte(minimal))
	if err != nil {
		t.Fatal(err)
	}
	got := set.Policies[0]
	if got.Lifecycle.MaxAge != DefaultMaxAge || got.Lifecycle.Revision.Containers != nil || got.Replicas != (Replicas{}) || got.Schedule != nil {
		t.Fatalf("defaults not applied: %#v", got)
	}
}

func TestDecodeRejectsInvalidDocuments(t *testing.T) {
	tests := map[string]string{
		"unknown-field":      strings.Replace(validPolicyYAML, "      priority: 100", "      priority: 100\n      typo: true", 1),
		"explicit-null":      strings.Replace(validPolicyYAML, "        timeZone: Asia/Shanghai", "        timeZone: null", 1),
		"empty-containers":   strings.Replace(validPolicyYAML, "          containers: [app]", "          containers: []", 1),
		"bad-timezone":       strings.Replace(validPolicyYAML, "Asia/Shanghai", "Mars/Olympus", 1),
		"negative-replicas":  strings.Replace(validPolicyYAML, "        expired: 0", "        expired: -1", 1),
		"bad-day":            strings.Replace(validPolicyYAML, "[MON, TUE, WED, THU, FRI]", "[MONDAY]", 1),
		"in-without-values":  strings.Replace(validPolicyYAML, "              values: [test, staging]\n", "", 1),
		"multiple-documents": validPolicyYAML + "---\n{}\n",
	}
	for name, document := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := Decode([]byte(document)); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestDecodeExpiredAction(t *testing.T) {
	baseYAML := func(expiredActionLine string) string {
		lifecycle := "      lifecycle:\n        maxAge: 72h\n"
		if expiredActionLine != "" {
			lifecycle += "        " + expiredActionLine + "\n"
		}
		return `apiVersion: lifecycle.example.com/v1alpha1
kind: WorkloadLifecyclePolicySet
spec:
  policies:
    - name: test-expired-action
      priority: 50
      target:
        kinds: [Deployment]
        selector:
          matchLabels:
            app: demo
` + lifecycle
	}

	t.Run("scale-accepted", func(t *testing.T) {
		set, err := Decode([]byte(baseYAML(`expiredAction: scale`)))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := set.Policies[0].Lifecycle.ExpiredAction; got != ExpiredActionScale {
			t.Fatalf("expected ExpiredAction=%q, got %q", ExpiredActionScale, got)
		}
	})

	t.Run("delete-accepted", func(t *testing.T) {
		set, err := Decode([]byte(baseYAML(`expiredAction: delete`)))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := set.Policies[0].Lifecycle.ExpiredAction; got != ExpiredActionDelete {
			t.Fatalf("expected ExpiredAction=%q, got %q", ExpiredActionDelete, got)
		}
	})

	t.Run("nil-defaults-to-scale", func(t *testing.T) {
		set, err := Decode([]byte(baseYAML("")))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := set.Policies[0].Lifecycle.ExpiredAction; got != ExpiredActionScale {
			t.Fatalf("expected ExpiredAction=%q (default), got %q", ExpiredActionScale, got)
		}
	})

	t.Run("empty-string-defaults-to-scale", func(t *testing.T) {
		set, err := Decode([]byte(baseYAML(`expiredAction: ""`)))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := set.Policies[0].Lifecycle.ExpiredAction; got != ExpiredActionScale {
			t.Fatalf("expected ExpiredAction=%q (default), got %q", ExpiredActionScale, got)
		}
	})

	t.Run("invalid-value-rejected", func(t *testing.T) {
		_, err := Decode([]byte(baseYAML(`expiredAction: remove`)))
		if err == nil {
			t.Fatal("expected error for invalid expiredAction")
		}
		if !strings.Contains(err.Error(), "invalid expiredAction") {
			t.Fatalf("error should mention 'invalid expiredAction', got: %v", err)
		}
	})
}
