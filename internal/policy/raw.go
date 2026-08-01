package policy

import (
	"gitlab.glb.osl-nucleus.com/devops/kube-workload-lifecycle-manager/internal/schedule"
	"gitlab.glb.osl-nucleus.com/devops/kube-workload-lifecycle-manager/internal/workload"
)

type rawPolicySet struct {
	APIVersion string   `yaml:"apiVersion"`
	Kind       string   `yaml:"kind"`
	Spec       *rawSpec `yaml:"spec"`
}

type rawSpec struct {
	Policies *[]rawPolicy `yaml:"policies"`
}

type rawPolicy struct {
	Name      string        `yaml:"name"`
	Priority  *int32        `yaml:"priority"`
	Target    *rawTarget    `yaml:"target"`
	Lifecycle *rawLifecycle `yaml:"lifecycle,omitempty"`
	Replicas  *rawReplicas  `yaml:"replicas,omitempty"`
	Schedule  *rawSchedule  `yaml:"schedule,omitempty"`
}

type rawTarget struct {
	Kinds    *[]workload.Kind `yaml:"kinds"`
	Selector *rawSelector     `yaml:"selector"`
}

type rawSelector struct {
	MatchLabels      map[string]string        `yaml:"matchLabels,omitempty"`
	MatchExpressions []rawSelectorRequirement `yaml:"matchExpressions,omitempty"`
}

type rawSelectorRequirement struct {
	Key      string    `yaml:"key"`
	Operator string    `yaml:"operator"`
	Values   *[]string `yaml:"values,omitempty"`
}

type rawLifecycle struct {
	MaxAge   *string      `yaml:"maxAge,omitempty"`
	Revision *rawRevision `yaml:"revision,omitempty"`
}

type rawRevision struct {
	Source     *string   `yaml:"source,omitempty"`
	Containers *[]string `yaml:"containers,omitempty"`
}

type rawReplicas struct {
	ScheduledDown *int32 `yaml:"scheduledDown,omitempty"`
	Expired       *int32 `yaml:"expired,omitempty"`
}

type rawSchedule struct {
	TimeZone    string             `yaml:"timeZone"`
	DownWindows *[]schedule.Window `yaml:"downWindows"`
}
