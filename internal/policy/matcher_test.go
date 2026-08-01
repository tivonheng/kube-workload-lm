package policy

import (
	"testing"

	"gitlab.glb.osl-nucleus.com/devops/kube-workload-lifecycle-manager/internal/workload"
)

func TestMatchChoosesHighestPriorityAndKind(t *testing.T) {
	set, err := Decode([]byte(validPolicyYAML + `    - name: lower
      priority: 10
      target:
        kinds: [Deployment]
        selector:
          matchLabels:
            lifecycle.example.com/policy: temporary
`))
	if err != nil {
		t.Fatal(err)
	}
	labels := map[string]string{"lifecycle.example.com/policy": "temporary", "environment": "test"}
	result := Match(set, workload.KindDeployment, labels)
	if result.Policy == nil || result.Policy.Name != "temporary" || result.Conflict() {
		t.Fatalf("unexpected result: %#v", result)
	}
	if result := Match(set, workload.KindStatefulSet, map[string]string{"environment": "test"}); result.Policy != nil {
		t.Fatalf("unexpected match: %#v", result)
	}
}

func TestMatchRejectsHighestPriorityTie(t *testing.T) {
	set, err := Decode([]byte(validPolicyYAML + `    - name: tied
      priority: 100
      target:
        kinds: [Deployment]
        selector:
          matchLabels:
            lifecycle.example.com/policy: temporary
`))
	if err != nil {
		t.Fatal(err)
	}
	result := Match(set, workload.KindDeployment, map[string]string{"lifecycle.example.com/policy": "temporary", "environment": "test"})
	if !result.Conflict() || result.Policy != nil || len(result.ConflictNames) != 2 || result.ConflictNames[0] != "temporary" {
		t.Fatalf("expected deterministic conflict, got %#v", result)
	}
}

func TestMatchSupportsKubernetesSelectorOperators(t *testing.T) {
	inValues := []string{"test", "staging"}
	notInValues := []string{"frontend"}
	selector, compiled, err := compileSelector(&rawSelector{MatchExpressions: []rawSelectorRequirement{
		{Key: "environment", Operator: "In", Values: &inValues},
		{Key: "tier", Operator: "NotIn", Values: &notInValues},
		{Key: "app", Operator: "Exists"},
		{Key: "debug", Operator: "DoesNotExist"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	set := PolicySet{Policies: []Policy{{Name: "operators", Priority: 1, Target: Target{
		Kinds: []workload.Kind{workload.KindDeployment}, Selector: selector, compiledSelector: compiled,
	}}}}
	matching := map[string]string{"environment": "test", "tier": "backend", "app": "api"}
	if result := Match(set, workload.KindDeployment, matching); result.Policy == nil {
		t.Fatalf("expected selector match: %#v", result)
	}
	matching["debug"] = "true"
	if result := Match(set, workload.KindDeployment, matching); result.Policy != nil {
		t.Fatalf("DoesNotExist matched an existing label: %#v", result)
	}
}
