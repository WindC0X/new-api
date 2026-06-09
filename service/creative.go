package service

import (
	"sort"

	"github.com/QuantumNous/new-api/model"
)

// GetUserCreativeModelPool returns the full callable model pool across all
// groups the current user may use. The returned groups are the groups considered
// for ownership/routing and are not UI grouping policy.
func GetUserCreativeModelPool(userGroup string) ([]string, []string) {
	groups := orderedCreativeUsableGroups(userGroup)
	seen := make(map[string]struct{})
	models := make([]string, 0)
	for _, group := range groups {
		for _, modelName := range model.GetGroupEnabledModels(group) {
			if _, ok := seen[modelName]; ok {
				continue
			}
			seen[modelName] = struct{}{}
			models = append(models, modelName)
		}
	}
	sort.Strings(models)
	return models, groups
}

// SelectCreativeModelGroup chooses a user-usable group that can call modelName.
// It prefers the user's current group, then stable lexicographic order for the
// remaining usable groups; clients do not send or persist provider overrides.
func SelectCreativeModelGroup(userGroup string, modelName string) (string, bool) {
	if modelName == "" {
		return "", false
	}
	for _, group := range orderedCreativeUsableGroups(userGroup) {
		for _, enabledModel := range model.GetGroupEnabledModels(group) {
			if enabledModel == modelName {
				return group, true
			}
		}
	}
	return "", false
}

func orderedCreativeUsableGroups(userGroup string) []string {
	usable := GetUserUsableGroups(userGroup)
	groups := make([]string, 0, len(usable))
	if userGroup != "" {
		if _, ok := usable[userGroup]; ok {
			groups = append(groups, userGroup)
		}
	}
	rest := make([]string, 0, len(usable))
	for group := range usable {
		if group == userGroup {
			continue
		}
		rest = append(rest, group)
	}
	sort.Strings(rest)
	groups = append(groups, rest...)
	return groups
}
