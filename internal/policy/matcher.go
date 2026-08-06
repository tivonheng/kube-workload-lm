package policy

import (
	"sort"

	"gitlab.glb.osl-nucleus.com/devops/kube-workload-lifecycle-manager/internal/workload"
)

type MatchResult struct {
	Policy        *Policy
	ConflictNames []string
}

func (result MatchResult) Conflict() bool { return len(result.ConflictNames) > 0 }

func Match(set PolicySet, kind workload.Kind, name string, workloadLabels map[string]string) MatchResult {
	var matches []*Policy
	for index := range set.Policies {
		candidate := &set.Policies[index]
		if candidate.Target.matches(kind, name, workloadLabels) {
			matches = append(matches, candidate)
		}
	}
	if len(matches) == 0 {
		return MatchResult{}
	}
	highest := matches[0].Priority
	for _, candidate := range matches[1:] {
		if candidate.Priority > highest {
			highest = candidate.Priority
		}
	}
	var winners []*Policy
	for _, candidate := range matches {
		if candidate.Priority == highest {
			winners = append(winners, candidate)
		}
	}
	if len(winners) == 1 {
		return MatchResult{Policy: winners[0]}
	}
	names := make([]string, len(winners))
	for index, winner := range winners {
		names[index] = winner.Name
	}
	sort.Strings(names)
	return MatchResult{ConflictNames: names}
}
