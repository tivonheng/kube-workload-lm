package policy

import (
	"fmt"
	"regexp"
	"time"

	"gitlab.glb.osl-nucleus.com/devops/kube-workload-lifecycle-manager/internal/lifecycle"
	"gitlab.glb.osl-nucleus.com/devops/kube-workload-lifecycle-manager/internal/schedule"
	"gitlab.glb.osl-nucleus.com/devops/kube-workload-lifecycle-manager/internal/workload"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

func compilePolicySet(raw rawPolicySet) (PolicySet, error) {
	if raw.APIVersion != APIVersion {
		return PolicySet{}, fmt.Errorf("unsupported apiVersion %q", raw.APIVersion)
	}
	if raw.Kind != Kind {
		return PolicySet{}, fmt.Errorf("unsupported kind %q", raw.Kind)
	}
	if raw.Spec == nil || raw.Spec.Policies == nil {
		return PolicySet{}, fmt.Errorf("spec.policies must be configured")
	}
	result := PolicySet{APIVersion: raw.APIVersion, Kind: raw.Kind, Policies: make([]Policy, 0, len(*raw.Spec.Policies))}
	names := make(map[string]struct{}, len(*raw.Spec.Policies))
	for index, item := range *raw.Spec.Policies {
		compiled, err := compilePolicy(item)
		if err != nil {
			return PolicySet{}, fmt.Errorf("spec.policies[%d]: %w", index, err)
		}
		if _, exists := names[compiled.Name]; exists {
			return PolicySet{}, fmt.Errorf("spec.policies[%d]: duplicate policy name %q", index, compiled.Name)
		}
		names[compiled.Name] = struct{}{}
		result.Policies = append(result.Policies, compiled)
	}
	return result, nil
}

func compilePolicy(raw rawPolicy) (Policy, error) {
	if raw.Name == "" {
		return Policy{}, fmt.Errorf("name must be non-empty")
	}
	if raw.Priority == nil {
		return Policy{}, fmt.Errorf("priority must be configured")
	}
	target, err := compileTarget(raw.Target)
	if err != nil {
		return Policy{}, fmt.Errorf("target: %w", err)
	}
	lifecyclePolicy, err := compileLifecycle(raw.Lifecycle)
	if err != nil {
		return Policy{}, fmt.Errorf("lifecycle: %w", err)
	}
	replicas, err := compileReplicas(raw.Replicas)
	if err != nil {
		return Policy{}, fmt.Errorf("replicas: %w", err)
	}
	configuredSchedule, err := compileSchedule(raw.Schedule)
	if err != nil {
		return Policy{}, fmt.Errorf("schedule: %w", err)
	}
	return Policy{raw.Name, *raw.Priority, target, lifecyclePolicy, replicas, configuredSchedule}, nil
}

func compileTarget(raw *rawTarget) (Target, error) {
	if raw == nil || raw.Kinds == nil || len(*raw.Kinds) == 0 {
		return Target{}, fmt.Errorf("kinds must be configured and non-empty")
	}
	kinds := append([]workload.Kind(nil), (*raw.Kinds)...)
	seenKinds := make(map[workload.Kind]struct{}, len(kinds))
	for _, kind := range kinds {
		if kind != workload.KindDeployment && kind != workload.KindStatefulSet {
			return Target{}, fmt.Errorf("unsupported kind %q", kind)
		}
		if _, exists := seenKinds[kind]; exists {
			return Target{}, fmt.Errorf("duplicate kind %q", kind)
		}
		seenKinds[kind] = struct{}{}
	}

	hasSelector := raw.Selector != nil &&
		(len(raw.Selector.MatchLabels) > 0 || len(raw.Selector.MatchExpressions) > 0)
	hasNamePatterns := raw.NamePatterns != nil && len(*raw.NamePatterns) > 0

	if !hasSelector && !hasNamePatterns {
		return Target{}, fmt.Errorf("at least one of selector or namePatterns must be configured")
	}

	var selector metav1.LabelSelector
	var compiledSelector labels.Selector
	if hasSelector {
		var err error
		selector, compiledSelector, err = compileSelector(raw.Selector)
		if err != nil {
			return Target{}, err
		}
	}

	compiledPatterns, namePatterns, err := compileNamePatterns(raw.NamePatterns)
	if err != nil {
		return Target{}, err
	}

	return Target{
		Kinds: kinds, Selector: selector, compiledSelector: compiledSelector,
		NamePatterns: namePatterns, compiledPatterns: compiledPatterns,
	}, nil
}

func compileSelector(raw *rawSelector) (metav1.LabelSelector, labels.Selector, error) {
	if raw == nil || (len(raw.MatchLabels) == 0 && len(raw.MatchExpressions) == 0) {
		return metav1.LabelSelector{}, nil, fmt.Errorf("selector must be non-empty")
	}
	selector := metav1.LabelSelector{MatchLabels: make(map[string]string, len(raw.MatchLabels))}
	for key, value := range raw.MatchLabels {
		selector.MatchLabels[key] = value
	}
	selector.MatchExpressions = make([]metav1.LabelSelectorRequirement, 0, len(raw.MatchExpressions))
	for index, requirement := range raw.MatchExpressions {
		compiled := metav1.LabelSelectorRequirement{Key: requirement.Key}
		switch requirement.Operator {
		case string(metav1.LabelSelectorOpIn), string(metav1.LabelSelectorOpNotIn):
			if requirement.Values == nil || len(*requirement.Values) == 0 {
				return metav1.LabelSelector{}, nil, fmt.Errorf("matchExpressions[%d]: %s requires non-empty values", index, requirement.Operator)
			}
			compiled.Operator = metav1.LabelSelectorOperator(requirement.Operator)
			compiled.Values = append([]string(nil), (*requirement.Values)...)
		case string(metav1.LabelSelectorOpExists), string(metav1.LabelSelectorOpDoesNotExist):
			if requirement.Values != nil {
				return metav1.LabelSelector{}, nil, fmt.Errorf("matchExpressions[%d]: %s does not allow values", index, requirement.Operator)
			}
			compiled.Operator = metav1.LabelSelectorOperator(requirement.Operator)
		default:
			return metav1.LabelSelector{}, nil, fmt.Errorf("matchExpressions[%d]: unsupported operator %q", index, requirement.Operator)
		}
		selector.MatchExpressions = append(selector.MatchExpressions, compiled)
	}
	compiled, err := metav1.LabelSelectorAsSelector(&selector)
	if err != nil {
		return metav1.LabelSelector{}, nil, fmt.Errorf("invalid selector: %w", err)
	}
	return selector, compiled, nil
}

func compileNamePatterns(raw *[]string) ([]*regexp.Regexp, []string, error) {
	if raw == nil || len(*raw) == 0 {
		return nil, nil, nil
	}
	patterns := make([]string, 0, len(*raw))
	compiled := make([]*regexp.Regexp, 0, len(*raw))
	for i, pattern := range *raw {
		if pattern == "" {
			return nil, nil, fmt.Errorf("namePatterns[%d]: pattern must be non-empty", i)
		}
		anchored := "^(?:" + pattern + ")$"
		re, err := regexp.Compile(anchored)
		if err != nil {
			return nil, nil, fmt.Errorf("namePatterns[%d] %q: %w", i, pattern, err)
		}
		patterns = append(patterns, pattern)
		compiled = append(compiled, re)
	}
	return compiled, patterns, nil
}

func compileLifecycle(raw *rawLifecycle) (Lifecycle, error) {
	result := Lifecycle{MaxAge: DefaultMaxAge, Revision: lifecycle.TrackingSpec{Source: lifecycle.SourceContainerImages}}
	if raw == nil {
		return result, nil
	}
	if raw.MaxAge != nil {
		parsed, err := time.ParseDuration(*raw.MaxAge)
		if err != nil || parsed <= 0 {
			return Lifecycle{}, fmt.Errorf("maxAge must be a positive Go duration")
		}
		result.MaxAge = parsed
	}
	if raw.Revision == nil {
		return result, nil
	}
	if raw.Revision.Source != nil {
		if *raw.Revision.Source != lifecycle.SourceContainerImages {
			return Lifecycle{}, fmt.Errorf("unsupported revision source %q", *raw.Revision.Source)
		}
		result.Revision.Source = *raw.Revision.Source
	}
	if raw.Revision.Containers != nil {
		if len(*raw.Revision.Containers) == 0 {
			return Lifecycle{}, fmt.Errorf("revision.containers must be omitted or non-empty")
		}
		seen := make(map[string]struct{}, len(*raw.Revision.Containers))
		for _, name := range *raw.Revision.Containers {
			if name == "" {
				return Lifecycle{}, fmt.Errorf("revision.containers cannot contain an empty name")
			}
			if _, exists := seen[name]; exists {
				return Lifecycle{}, fmt.Errorf("duplicate revision container %q", name)
			}
			seen[name] = struct{}{}
		}
		result.Revision.Containers = append([]string(nil), (*raw.Revision.Containers)...)
	}
	return result, nil
}

func compileReplicas(raw *rawReplicas) (Replicas, error) {
	result := Replicas{}
	if raw == nil {
		return result, nil
	}
	if raw.ScheduledDown != nil {
		result.ScheduledDown = *raw.ScheduledDown
	}
	if raw.Expired != nil {
		result.Expired = *raw.Expired
	}
	if result.ScheduledDown < 0 || result.Expired < 0 {
		return Replicas{}, fmt.Errorf("replica targets must be non-negative")
	}
	return result, nil
}

func compileSchedule(raw *rawSchedule) (*Schedule, error) {
	if raw == nil {
		return nil, nil
	}
	if raw.TimeZone == "" {
		return nil, fmt.Errorf("timeZone must be configured")
	}
	if _, err := time.LoadLocation(raw.TimeZone); err != nil {
		return nil, fmt.Errorf("invalid IANA timeZone %q: %w", raw.TimeZone, err)
	}
	if raw.DownWindows == nil || len(*raw.DownWindows) == 0 {
		return nil, fmt.Errorf("downWindows must be configured and non-empty")
	}
	windows := append([]schedule.Window(nil), (*raw.DownWindows)...)
	names := make(map[string]struct{}, len(windows))
	for index, window := range windows {
		if err := schedule.ValidateWindow(window); err != nil {
			return nil, fmt.Errorf("downWindows[%d]: %w", index, err)
		}
		if _, exists := names[window.Name]; exists {
			return nil, fmt.Errorf("duplicate down window name %q", window.Name)
		}
		names[window.Name] = struct{}{}
	}
	return &Schedule{TimeZone: raw.TimeZone, DownWindows: windows}, nil
}
