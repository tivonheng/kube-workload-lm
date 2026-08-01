package policy

import (
	"regexp"
	"testing"

	"gitlab.glb.osl-nucleus.com/devops/kube-workload-lifecycle-manager/internal/workload"
	"k8s.io/apimachinery/pkg/labels"
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
	result := Match(set, workload.KindDeployment, "test-workload", labels)
	if result.Policy == nil || result.Policy.Name != "temporary" || result.Conflict() {
		t.Fatalf("unexpected result: %#v", result)
	}
	if result := Match(set, workload.KindStatefulSet, "test-workload", map[string]string{"environment": "test"}); result.Policy != nil {
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
	result := Match(set, workload.KindDeployment, "test-workload", map[string]string{"lifecycle.example.com/policy": "temporary", "environment": "test"})
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
	if result := Match(set, workload.KindDeployment, "test-workload", matching); result.Policy == nil {
		t.Fatalf("expected selector match: %#v", result)
	}
	matching["debug"] = "true"
	if result := Match(set, workload.KindDeployment, "test-workload", matching); result.Policy != nil {
		t.Fatalf("DoesNotExist matched an existing label: %#v", result)
	}
}

func TestMatchNamePatterns(t *testing.T) {
	tests := []struct {
		name       string
		set        PolicySet
		kind       workload.Kind
		wlName     string
		labels     map[string]string
		wantMatch  bool
		wantPolicy string
		wantConf   bool
	}{
		{
			name: "full-match semantics: exact match",
			set: PolicySet{Policies: []Policy{{
				Name: "pfb-policy", Priority: 10,
				Target: Target{
					Kinds:            []workload.Kind{workload.KindDeployment},
					compiledPatterns: []*regexp.Regexp{regexp.MustCompile("^(?:pfb\\d+)$")},
					NamePatterns:     []string{"pfb\\d+"},
				},
			}}},
			kind:       workload.KindDeployment,
			wlName:     "pfb123",
			labels:     map[string]string{},
			wantMatch:  true,
			wantPolicy: "pfb-policy",
		},
		{
			name: "full-match semantics: prefix substring does not match",
			set: PolicySet{Policies: []Policy{{
				Name: "pfb-policy", Priority: 10,
				Target: Target{
					Kinds:            []workload.Kind{workload.KindDeployment},
					compiledPatterns: []*regexp.Regexp{regexp.MustCompile("^(?:pfb\\d+)$")},
					NamePatterns:     []string{"pfb\\d+"},
				},
			}}},
			kind:      workload.KindDeployment,
			wlName:    "my-pfb123",
			labels:    map[string]string{},
			wantMatch: false,
		},
		{
			name: "full-match semantics: suffix substring does not match",
			set: PolicySet{Policies: []Policy{{
				Name: "pfb-policy", Priority: 10,
				Target: Target{
					Kinds:            []workload.Kind{workload.KindDeployment},
					compiledPatterns: []*regexp.Regexp{regexp.MustCompile("^(?:pfb\\d+)$")},
					NamePatterns:     []string{"pfb\\d+"},
				},
			}}},
			kind:      workload.KindDeployment,
			wlName:    "pfb123-extra",
			labels:    map[string]string{},
			wantMatch: false,
		},
		{
			name: "OR combination: selector matches, name does not",
			set: PolicySet{Policies: []Policy{{
				Name: "or-policy", Priority: 10,
				Target: Target{
					Kinds:            []workload.Kind{workload.KindDeployment},
					compiledSelector: mustCompileLabelSelector(t, "app=web"),
					compiledPatterns: []*regexp.Regexp{regexp.MustCompile("^(?:pfb\\d+)$")},
					NamePatterns:     []string{"pfb\\d+"},
				},
			}}},
			kind:       workload.KindDeployment,
			wlName:     "not-matching-name",
			labels:     map[string]string{"app": "web"},
			wantMatch:  true,
			wantPolicy: "or-policy",
		},
		{
			name: "OR combination: name matches, selector does not",
			set: PolicySet{Policies: []Policy{{
				Name: "or-policy", Priority: 10,
				Target: Target{
					Kinds:            []workload.Kind{workload.KindDeployment},
					compiledSelector: mustCompileLabelSelector(t, "app=web"),
					compiledPatterns: []*regexp.Regexp{regexp.MustCompile("^(?:pfb\\d+)$")},
					NamePatterns:     []string{"pfb\\d+"},
				},
			}}},
			kind:       workload.KindDeployment,
			wlName:     "pfb999",
			labels:     map[string]string{"app": "other"},
			wantMatch:  true,
			wantPolicy: "or-policy",
		},
		{
			name: "OR combination: neither matches",
			set: PolicySet{Policies: []Policy{{
				Name: "or-policy", Priority: 10,
				Target: Target{
					Kinds:            []workload.Kind{workload.KindDeployment},
					compiledSelector: mustCompileLabelSelector(t, "app=web"),
					compiledPatterns: []*regexp.Regexp{regexp.MustCompile("^(?:pfb\\d+)$")},
					NamePatterns:     []string{"pfb\\d+"},
				},
			}}},
			kind:      workload.KindDeployment,
			wlName:    "something-else",
			labels:    map[string]string{"app": "other"},
			wantMatch: false,
		},
		{
			name: "selector only: matching labels, non-matching name",
			set: PolicySet{Policies: []Policy{{
				Name: "selector-only", Priority: 10,
				Target: Target{
					Kinds:            []workload.Kind{workload.KindDeployment},
					compiledSelector: mustCompileLabelSelector(t, "app=web"),
				},
			}}},
			kind:       workload.KindDeployment,
			wlName:     "random-name",
			labels:     map[string]string{"app": "web"},
			wantMatch:  true,
			wantPolicy: "selector-only",
		},
		{
			name: "namePatterns only: non-matching labels, matching name",
			set: PolicySet{Policies: []Policy{{
				Name: "name-only", Priority: 10,
				Target: Target{
					Kinds:            []workload.Kind{workload.KindDeployment},
					compiledPatterns: []*regexp.Regexp{regexp.MustCompile("^(?:staging-.*)$")},
					NamePatterns:     []string{"staging-.*"},
				},
			}}},
			kind:       workload.KindDeployment,
			wlName:     "staging-api",
			labels:     map[string]string{"app": "unrelated"},
			wantMatch:  true,
			wantPolicy: "name-only",
		},
		{
			name: "empty name: namePatterns do not match",
			set: PolicySet{Policies: []Policy{{
				Name: "name-only", Priority: 10,
				Target: Target{
					Kinds:            []workload.Kind{workload.KindDeployment},
					compiledPatterns: []*regexp.Regexp{regexp.MustCompile("^(?:.*)$")},
					NamePatterns:     []string{".*"},
				},
			}}},
			kind:      workload.KindDeployment,
			wlName:    "",
			labels:    map[string]string{},
			wantMatch: false,
		},
		{
			name: "priority and conflict unchanged: two namePatterns policies same priority",
			set: PolicySet{Policies: []Policy{
				{
					Name: "alpha", Priority: 50,
					Target: Target{
						Kinds:            []workload.Kind{workload.KindDeployment},
						compiledPatterns: []*regexp.Regexp{regexp.MustCompile("^(?:pfb\\d+)$")},
						NamePatterns:     []string{"pfb\\d+"},
					},
				},
				{
					Name: "beta", Priority: 50,
					Target: Target{
						Kinds:            []workload.Kind{workload.KindDeployment},
						compiledPatterns: []*regexp.Regexp{regexp.MustCompile("^(?:pfb.*)$")},
						NamePatterns:     []string{"pfb.*"},
					},
				},
			}},
			kind:     workload.KindDeployment,
			wlName:   "pfb123",
			labels:   map[string]string{},
			wantConf: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Match(tt.set, tt.kind, tt.wlName, tt.labels)
			if tt.wantConf {
				if !result.Conflict() {
					t.Fatalf("expected conflict, got %#v", result)
				}
				return
			}
			if tt.wantMatch {
				if result.Policy == nil {
					t.Fatalf("expected match, got no policy")
				}
				if result.Policy.Name != tt.wantPolicy {
					t.Fatalf("expected policy %q, got %q", tt.wantPolicy, result.Policy.Name)
				}
			} else {
				if result.Policy != nil {
					t.Fatalf("expected no match, got policy %q", result.Policy.Name)
				}
			}
		})
	}
}

// mustCompileLabelSelector is a test helper that creates a compiled labels.Selector
// from a simple "key=value" expression for use in test PolicySet construction.
func mustCompileLabelSelector(t *testing.T, expr string) labels.Selector {
	t.Helper()
	sel, err := labels.Parse(expr)
	if err != nil {
		t.Fatalf("failed to parse label selector %q: %v", expr, err)
	}
	return sel
}
