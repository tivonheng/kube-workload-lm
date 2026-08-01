package policy

import (
	"sync"

	"gitlab.glb.osl-nucleus.com/devops/kube-workload-lifecycle-manager/internal/schedule"
	"gitlab.glb.osl-nucleus.com/devops/kube-workload-lifecycle-manager/internal/workload"
)

type Manager struct {
	mutex   sync.RWMutex
	current *PolicySet
}

func (manager *Manager) Reload(data []byte) error {
	compiled, err := Decode(data)
	if err != nil {
		return err
	}
	manager.mutex.Lock()
	manager.current = &compiled
	manager.mutex.Unlock()
	return nil
}

func (manager *Manager) Current() (PolicySet, bool) {
	manager.mutex.RLock()
	defer manager.mutex.RUnlock()
	if manager.current == nil {
		return PolicySet{}, false
	}
	return clonePolicySet(*manager.current), true
}

func clonePolicySet(source PolicySet) PolicySet {
	clone := source
	clone.Policies = append([]Policy(nil), source.Policies...)
	for index := range clone.Policies {
		sourcePolicy := &source.Policies[index]
		clonedPolicy := &clone.Policies[index]
		clonedPolicy.Target.Kinds = append([]workload.Kind(nil), sourcePolicy.Target.Kinds...)
		clonedPolicy.Target.Selector = *sourcePolicy.Target.Selector.DeepCopy()
		clonedPolicy.Lifecycle.Revision.Containers = append([]string(nil), sourcePolicy.Lifecycle.Revision.Containers...)
		if sourcePolicy.Schedule != nil {
			clonedSchedule := *sourcePolicy.Schedule
			clonedSchedule.DownWindows = append([]schedule.Window(nil), sourcePolicy.Schedule.DownWindows...)
			for windowIndex := range clonedSchedule.DownWindows {
				clonedSchedule.DownWindows[windowIndex].StartDays = append(
					[]schedule.Day(nil), sourcePolicy.Schedule.DownWindows[windowIndex].StartDays...,
				)
			}
			clonedPolicy.Schedule = &clonedSchedule
		}
	}
	return clone
}
