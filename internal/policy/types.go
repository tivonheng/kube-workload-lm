package policy

import (
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
}

type Lifecycle struct {
	MaxAge   time.Duration
	Revision lifecycle.TrackingSpec
}

type Replicas struct {
	ScheduledDown int32
	Expired       int32
}

type Schedule struct {
	TimeZone    string
	DownWindows []schedule.Window
}

func (target Target) matches(kind workload.Kind, workloadLabels map[string]string) bool {
	kindMatched := false
	for _, candidate := range target.Kinds {
		if candidate == kind {
			kindMatched = true
			break
		}
	}
	return kindMatched && target.compiledSelector.Matches(labels.Set(workloadLabels))
}
