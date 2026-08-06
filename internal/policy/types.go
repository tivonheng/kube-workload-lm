package policy

import (
	"regexp"
	"time"

	"gitlab.glb.osl-nucleus.com/devops/kube-workload-lifecycle-manager/internal/lifecycle"
	"gitlab.glb.osl-nucleus.com/devops/kube-workload-lifecycle-manager/internal/schedule"
	"gitlab.glb.osl-nucleus.com/devops/kube-workload-lifecycle-manager/internal/workload"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

const (
	APIVersion    = "lifecycle.example.com/v1alpha1"
	Kind          = "WorkloadLifecyclePolicySet"
	DefaultMaxAge = 72 * time.Hour

	ExpiredActionScale  = "scale"
	ExpiredActionDelete = "delete"
)

type PolicySet struct {
	APIVersion string
	Kind       string
	Policies   []Policy
}

type Policy struct {
	Name      string
	Priority  int32
	Target    Target
	Lifecycle Lifecycle
	Replicas  Replicas
	Schedule  *Schedule
}

type Target struct {
	Kinds            []workload.Kind
	Selector         metav1.LabelSelector
	compiledSelector labels.Selector
	NamePatterns     []string
	compiledPatterns []*regexp.Regexp
}

type Lifecycle struct {
	MaxAge        time.Duration
	Revision      lifecycle.TrackingSpec
	ExpiredAction string // "scale" (default) | "delete"
}

type Replicas struct {
	ScheduledDown int32
	Expired       int32
}

type Schedule struct {
	TimeZone    string
	DownWindows []schedule.Window
}

func (target Target) matches(kind workload.Kind, name string, workloadLabels map[string]string) bool {
	if !target.kindMatches(kind) {
		return false
	}
	if target.selectorMatches(workloadLabels) {
		return true
	}
	return target.namePatternMatches(name)
}

func (target Target) kindMatches(kind workload.Kind) bool {
	for _, candidate := range target.Kinds {
		if candidate == kind {
			return true
		}
	}
	return false
}

func (target Target) selectorMatches(workloadLabels map[string]string) bool {
	if target.compiledSelector == nil {
		return false
	}
	return target.compiledSelector.Matches(labels.Set(workloadLabels))
}

func (target Target) namePatternMatches(name string) bool {
	if name == "" || len(target.compiledPatterns) == 0 {
		return false
	}
	for _, pattern := range target.compiledPatterns {
		if pattern.MatchString(name) {
			return true
		}
	}
	return false
}
