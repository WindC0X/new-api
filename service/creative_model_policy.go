package service

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
)

const CreativeModelPolicyOptionKey = "creative.model_policy"

var creativeModelPolicyModalities = []string{"text", "agent", "image", "video", "audio"}

var creativeModelPolicyModalitySet = map[string]struct{}{
	"text":  {},
	"agent": {},
	"image": {},
	"video": {},
	"audio": {},
}

type CreativeModelPolicy struct {
	Version int                                `json:"version"`
	Global  CreativeModelPolicyRule            `json:"global,omitempty"`
	Groups  map[string]CreativeModelPolicyRule `json:"groups,omitempty"`
}

type CreativeModelPolicyRule struct {
	Defaults    map[string]string   `json:"defaults,omitempty"`
	Recommended map[string][]string `json:"recommended,omitempty"`
}

type CreativeModelPolicyStale struct {
	Defaults    map[string]string   `json:"defaults,omitempty"`
	Recommended map[string][]string `json:"recommended,omitempty"`
}

type CreativeEffectiveModelPolicy struct {
	Version     int                       `json:"version"`
	Defaults    map[string]string         `json:"defaults,omitempty"`
	Recommended map[string][]string       `json:"recommended,omitempty"`
	Stale       *CreativeModelPolicyStale `json:"stale,omitempty"`
}

type CreativeModelPolicyDiagnostics struct {
	StaleByGroup map[string]CreativeModelPolicyStale `json:"staleByGroup,omitempty"`
}

type CreativeModelPolicyGroupPool struct {
	Group            string                       `json:"group"`
	Description      string                       `json:"description,omitempty"`
	Models           []string                     `json:"models"`
	ModelsByModality map[string][]string          `json:"modelsByModality,omitempty"`
	ModelCount       int                          `json:"modelCount"`
	EffectivePolicy  CreativeEffectiveModelPolicy `json:"effectivePolicy"`
}

type CreativeModelPolicyAdminState struct {
	Key               string                         `json:"key"`
	AllowedModalities []string                       `json:"allowedModalities"`
	Policy            CreativeModelPolicy            `json:"policy"`
	PolicyJSON        string                         `json:"policyJSON"`
	CleanedPolicy     CreativeModelPolicy            `json:"cleanedPolicy"`
	CleanedPolicyJSON string                         `json:"cleanedPolicyJSON"`
	ModelPools        []CreativeModelPolicyGroupPool `json:"modelPools"`
	Diagnostics       CreativeModelPolicyDiagnostics `json:"diagnostics"`
}

type CreativeModelPolicyAvailableModel struct {
	ID                     string
	SupportedEndpointTypes []constant.EndpointType
}

func CreativeModelPolicyAllowedModalities() []string {
	return append([]string(nil), creativeModelPolicyModalities...)
}

func EmptyCreativeModelPolicy() CreativeModelPolicy {
	return CreativeModelPolicy{Version: 1}
}

func NormalizeCreativeModelPolicyJSON(raw string) (CreativeModelPolicy, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return EmptyCreativeModelPolicy(), nil
	}
	var value any
	if err := common.Unmarshal([]byte(trimmed), &value); err != nil {
		return CreativeModelPolicy{}, err
	}
	return NormalizeCreativeModelPolicyValue(value)
}

func NormalizeCreativeModelPolicyValue(value any) (CreativeModelPolicy, error) {
	if value == nil {
		return EmptyCreativeModelPolicy(), nil
	}
	if text, ok := value.(string); ok {
		trimmed := strings.TrimSpace(text)
		if trimmed == "" {
			return EmptyCreativeModelPolicy(), nil
		}
		if !strings.HasPrefix(trimmed, "{") {
			return CreativeModelPolicy{}, errors.New("policy must be a JSON object")
		}
		return NormalizeCreativeModelPolicyJSON(trimmed)
	}
	if field, forbidden := creativeModelPolicyUnsafeField(value); forbidden {
		return CreativeModelPolicy{}, fmt.Errorf("forbidden field %s", field)
	}
	object, ok := value.(map[string]any)
	if !ok {
		return CreativeModelPolicy{}, errors.New("policy must be a JSON object")
	}

	policy := EmptyCreativeModelPolicy()
	if rawVersion, ok := object["version"]; ok {
		version, err := creativeModelPolicyInt(rawVersion)
		if err != nil {
			return CreativeModelPolicy{}, fmt.Errorf("version: %w", err)
		}
		if version != 1 {
			return CreativeModelPolicy{}, fmt.Errorf("unsupported policy version %d", version)
		}
		policy.Version = version
	}
	if rawGlobal, ok := object["global"]; ok {
		rule, err := normalizeCreativeModelPolicyRule(rawGlobal, "global")
		if err != nil {
			return CreativeModelPolicy{}, err
		}
		policy.Global = rule
	}
	if rawGroups, ok := object["groups"]; ok {
		groupsObject, ok := rawGroups.(map[string]any)
		if !ok {
			return CreativeModelPolicy{}, errors.New("groups must be an object")
		}
		groupNames := make([]string, 0, len(groupsObject))
		for group := range groupsObject {
			groupNames = append(groupNames, group)
		}
		sort.Strings(groupNames)
		for _, groupName := range groupNames {
			trimmedGroup := strings.TrimSpace(groupName)
			if trimmedGroup == "" {
				continue
			}
			rule, err := normalizeCreativeModelPolicyRule(groupsObject[groupName], "groups."+trimmedGroup)
			if err != nil {
				return CreativeModelPolicy{}, err
			}
			if creativeModelPolicyRuleEmpty(rule) {
				continue
			}
			if policy.Groups == nil {
				policy.Groups = make(map[string]CreativeModelPolicyRule)
			}
			policy.Groups[trimmedGroup] = rule
		}
	}
	return policy, nil
}

func CreativeModelPolicyJSON(policy CreativeModelPolicy) (string, error) {
	normalized, err := NormalizeCreativeModelPolicyValue(policyToJSONValue(policy))
	if err != nil {
		return "", err
	}
	encoded, err := common.Marshal(normalized)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func GetStoredCreativeModelPolicy() (CreativeModelPolicy, error) {
	common.OptionMapRWMutex.RLock()
	raw := common.OptionMap[CreativeModelPolicyOptionKey]
	common.OptionMapRWMutex.RUnlock()
	return NormalizeCreativeModelPolicyJSON(raw)
}

func UpdateStoredCreativeModelPolicy(policy CreativeModelPolicy) (CreativeModelPolicy, string, error) {
	normalized, err := NormalizeCreativeModelPolicyValue(policyToJSONValue(policy))
	if err != nil {
		return CreativeModelPolicy{}, "", err
	}
	policyJSON, err := CreativeModelPolicyJSON(normalized)
	if err != nil {
		return CreativeModelPolicy{}, "", err
	}
	common.OptionMapRWMutex.Lock()
	if common.OptionMap == nil {
		common.OptionMap = map[string]string{}
	}
	common.OptionMapRWMutex.Unlock()
	if err := model.UpdateOption(CreativeModelPolicyOptionKey, policyJSON); err != nil {
		return CreativeModelPolicy{}, "", err
	}
	return normalized, policyJSON, nil
}

func BuildEffectiveCreativeModelPolicy(policy CreativeModelPolicy, group string, availableModels []string) (CreativeEffectiveModelPolicy, string) {
	return BuildEffectiveCreativeModelPolicyForModels(policy, group, creativeModelPolicyAvailableModelsFromIDs(availableModels))
}

func BuildEffectiveCreativeModelPolicyForModels(policy CreativeModelPolicy, group string, availableModels []CreativeModelPolicyAvailableModel) (CreativeEffectiveModelPolicy, string) {
	availableByModality := creativeModelPolicyAvailableSets(availableModels)
	version := policy.Version
	if version == 0 {
		version = 1
	}
	effective := CreativeEffectiveModelPolicy{Version: version}
	stale := CreativeModelPolicyStale{}
	groupRule, hasGroupRule := policy.Groups[strings.TrimSpace(group)]

	for _, modality := range creativeModelPolicyModalities {
		availableSet := availableByModality[modality]
		if modelID, ok := chooseCreativeDefault(policy.Global, groupRule, hasGroupRule, modality, availableSet, &stale); ok {
			if effective.Defaults == nil {
				effective.Defaults = make(map[string]string)
			}
			effective.Defaults[modality] = modelID
		}
		if models := chooseCreativeRecommended(policy.Global, groupRule, hasGroupRule, modality, availableSet, &stale); len(models) > 0 {
			if effective.Recommended == nil {
				effective.Recommended = make(map[string][]string)
			}
			effective.Recommended[modality] = models
		}
	}
	if !creativeModelPolicyStaleEmpty(stale) {
		effective.Stale = &stale
	}
	encoded, err := common.Marshal(effective)
	if err != nil {
		return effective, ""
	}
	return effective, common.Sha1(encoded)
}

func BuildCreativeModelPolicyAdminState(policy CreativeModelPolicy) (CreativeModelPolicyAdminState, error) {
	normalized, err := NormalizeCreativeModelPolicyValue(policyToJSONValue(policy))
	if err != nil {
		return CreativeModelPolicyAdminState{}, err
	}
	policyJSON, err := CreativeModelPolicyJSON(normalized)
	if err != nil {
		return CreativeModelPolicyAdminState{}, err
	}
	modelPools, poolsByGroup, diagnostics := buildCreativeModelPolicyGroupPools(normalized)
	cleaned := CleanCreativeModelPolicy(normalized, poolsByGroup)
	cleanedJSON, err := CreativeModelPolicyJSON(cleaned)
	if err != nil {
		return CreativeModelPolicyAdminState{}, err
	}
	return CreativeModelPolicyAdminState{
		Key:               CreativeModelPolicyOptionKey,
		AllowedModalities: CreativeModelPolicyAllowedModalities(),
		Policy:            normalized,
		PolicyJSON:        policyJSON,
		CleanedPolicy:     cleaned,
		CleanedPolicyJSON: cleanedJSON,
		ModelPools:        modelPools,
		Diagnostics:       diagnostics,
	}, nil
}

func GetCreativeModelPolicyAdminState() (CreativeModelPolicyAdminState, error) {
	policy, err := GetStoredCreativeModelPolicy()
	if err != nil {
		return CreativeModelPolicyAdminState{}, err
	}
	return BuildCreativeModelPolicyAdminState(policy)
}

func CleanCreativeModelPolicy(policy CreativeModelPolicy, poolsByGroup map[string][]string) CreativeModelPolicy {
	cleaned := EmptyCreativeModelPolicy()
	cleaned.Version = policy.Version
	if cleaned.Version == 0 {
		cleaned.Version = 1
	}

	globalAvailableIDs := make([]string, 0)
	globalSeen := make(map[string]struct{})
	for _, models := range poolsByGroup {
		for _, modelID := range models {
			modelID = strings.TrimSpace(modelID)
			if modelID == "" {
				continue
			}
			if _, seen := globalSeen[modelID]; seen {
				continue
			}
			globalSeen[modelID] = struct{}{}
			globalAvailableIDs = append(globalAvailableIDs, modelID)
		}
	}
	globalAvailable := creativeModelPolicyAvailableSets(creativeModelPolicyAvailableModelsFromIDs(globalAvailableIDs))
	cleaned.Global = filterCreativeModelPolicyRule(policy.Global, globalAvailable)

	for group, rule := range policy.Groups {
		available := creativeModelPolicyAvailableSets(creativeModelPolicyAvailableModelsFromIDs(poolsByGroup[group]))
		filtered := filterCreativeModelPolicyRule(rule, available)
		if creativeModelPolicyRuleEmpty(filtered) {
			continue
		}
		if cleaned.Groups == nil {
			cleaned.Groups = make(map[string]CreativeModelPolicyRule)
		}
		cleaned.Groups[group] = filtered
	}
	return cleaned
}

func normalizeCreativeModelPolicyRule(value any, path string) (CreativeModelPolicyRule, error) {
	object, ok := value.(map[string]any)
	if !ok {
		return CreativeModelPolicyRule{}, fmt.Errorf("%s must be an object", path)
	}
	rule := CreativeModelPolicyRule{}
	if rawDefaults, ok := object["defaults"]; ok {
		defaults, err := normalizeCreativeModelPolicyDefaults(rawDefaults, path+".defaults")
		if err != nil {
			return CreativeModelPolicyRule{}, err
		}
		rule.Defaults = defaults
	}
	if rawRecommended, ok := object["recommended"]; ok {
		recommended, err := normalizeCreativeModelPolicyRecommended(rawRecommended, path+".recommended")
		if err != nil {
			return CreativeModelPolicyRule{}, err
		}
		rule.Recommended = recommended
	}
	return rule, nil
}

func normalizeCreativeModelPolicyDefaults(value any, path string) (map[string]string, error) {
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s must be an object", path)
	}
	defaults := make(map[string]string)
	for _, modality := range sortedPolicyKeys(object) {
		if !creativeModelPolicyAllowedModality(modality) {
			continue
		}
		modelID, ok := object[modality].(string)
		if !ok {
			return nil, fmt.Errorf("%s.%s must be a string", path, modality)
		}
		modelID, err := normalizeCreativePolicyModelID(modelID, path+"."+modality)
		if err != nil {
			return nil, err
		}
		if modelID != "" {
			defaults[modality] = modelID
		}
	}
	if len(defaults) == 0 {
		return nil, nil
	}
	return defaults, nil
}

func normalizeCreativeModelPolicyRecommended(value any, path string) (map[string][]string, error) {
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s must be an object", path)
	}
	recommended := make(map[string][]string)
	for _, modality := range sortedPolicyKeys(object) {
		if !creativeModelPolicyAllowedModality(modality) {
			continue
		}
		items, ok := object[modality].([]any)
		if !ok {
			return nil, fmt.Errorf("%s.%s must be an array", path, modality)
		}
		seen := make(map[string]struct{})
		models := make([]string, 0, len(items))
		for idx, item := range items {
			modelID, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("%s.%s[%d] must be a string", path, modality, idx)
			}
			modelID, err := normalizeCreativePolicyModelID(modelID, fmt.Sprintf("%s.%s[%d]", path, modality, idx))
			if err != nil {
				return nil, err
			}
			if modelID == "" {
				continue
			}
			if _, exists := seen[modelID]; exists {
				continue
			}
			seen[modelID] = struct{}{}
			models = append(models, modelID)
		}
		if len(models) > 0 {
			recommended[modality] = models
		}
	}
	if len(recommended) == 0 {
		return nil, nil
	}
	return recommended, nil
}

func normalizeCreativePolicyModelID(value string, path string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", nil
	}
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		return "", fmt.Errorf("%s must be a model id, not a URL", path)
	}
	if strings.Contains(lower, "bearer ") || strings.Contains(lower, "api_key=") || strings.Contains(lower, "apikey=") || strings.Contains(lower, "secret=") || strings.HasPrefix(lower, "sk-") {
		return "", fmt.Errorf("%s looks like a secret", path)
	}
	return trimmed, nil
}

func creativeModelPolicyInt(value any) (int, error) {
	switch typed := value.(type) {
	case float64:
		if typed != float64(int(typed)) {
			return 0, errors.New("must be an integer")
		}
		return int(typed), nil
	case int:
		return typed, nil
	case int64:
		return int(typed), nil
	case string:
		if strings.TrimSpace(typed) == "" {
			return 0, errors.New("must be an integer")
		}
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		if err != nil {
			return 0, errors.New("must be an integer")
		}
		return parsed, nil
	default:
		return 0, errors.New("must be an integer")
	}
}

func chooseCreativeDefault(globalRule CreativeModelPolicyRule, groupRule CreativeModelPolicyRule, hasGroupRule bool, modality string, available map[string]struct{}, stale *CreativeModelPolicyStale) (string, bool) {
	if hasGroupRule {
		if modelID, ok := groupRule.Defaults[modality]; ok {
			if _, available := available[modelID]; available {
				return modelID, true
			}
			addCreativeStaleDefault(stale, modality, modelID)
		}
	}
	if modelID, ok := globalRule.Defaults[modality]; ok {
		if _, available := available[modelID]; available {
			return modelID, true
		}
		addCreativeStaleDefault(stale, modality, modelID)
	}
	return "", false
}

func chooseCreativeRecommended(globalRule CreativeModelPolicyRule, groupRule CreativeModelPolicyRule, hasGroupRule bool, modality string, available map[string]struct{}, stale *CreativeModelPolicyStale) []string {
	if hasGroupRule {
		if models, ok := groupRule.Recommended[modality]; ok {
			filtered := filterCreativeRecommendedList(models, modality, available, stale)
			if len(filtered) > 0 {
				return filtered
			}
		}
	}
	if models, ok := globalRule.Recommended[modality]; ok {
		return filterCreativeRecommendedList(models, modality, available, stale)
	}
	return nil
}

func filterCreativeRecommendedList(models []string, modality string, available map[string]struct{}, stale *CreativeModelPolicyStale) []string {
	seen := make(map[string]struct{})
	filtered := make([]string, 0, len(models))
	for _, modelID := range models {
		if _, ok := available[modelID]; ok {
			if _, exists := seen[modelID]; exists {
				continue
			}
			seen[modelID] = struct{}{}
			filtered = append(filtered, modelID)
			continue
		}
		addCreativeStaleRecommended(stale, modality, modelID)
	}
	return filtered
}

func addCreativeStaleDefault(stale *CreativeModelPolicyStale, modality string, modelID string) {
	if stale == nil || strings.TrimSpace(modelID) == "" {
		return
	}
	if stale.Defaults == nil {
		stale.Defaults = make(map[string]string)
	}
	stale.Defaults[modality] = modelID
}

func addCreativeStaleRecommended(stale *CreativeModelPolicyStale, modality string, modelID string) {
	if stale == nil || strings.TrimSpace(modelID) == "" {
		return
	}
	if stale.Recommended == nil {
		stale.Recommended = make(map[string][]string)
	}
	for _, existing := range stale.Recommended[modality] {
		if existing == modelID {
			return
		}
	}
	stale.Recommended[modality] = append(stale.Recommended[modality], modelID)
}

func filterCreativeModelPolicyRule(rule CreativeModelPolicyRule, available map[string]map[string]struct{}) CreativeModelPolicyRule {
	filtered := CreativeModelPolicyRule{}
	for _, modality := range creativeModelPolicyModalities {
		availableSet := available[modality]
		if modelID, ok := rule.Defaults[modality]; ok {
			if _, available := availableSet[modelID]; available {
				if filtered.Defaults == nil {
					filtered.Defaults = make(map[string]string)
				}
				filtered.Defaults[modality] = modelID
			}
		}
		if models, ok := rule.Recommended[modality]; ok {
			kept := make([]string, 0, len(models))
			seen := make(map[string]struct{})
			for _, modelID := range models {
				if _, available := availableSet[modelID]; !available {
					continue
				}
				if _, exists := seen[modelID]; exists {
					continue
				}
				seen[modelID] = struct{}{}
				kept = append(kept, modelID)
			}
			if len(kept) > 0 {
				if filtered.Recommended == nil {
					filtered.Recommended = make(map[string][]string)
				}
				filtered.Recommended[modality] = kept
			}
		}
	}
	return filtered
}

func buildCreativeModelPolicyGroupPools(policy CreativeModelPolicy) ([]CreativeModelPolicyGroupPool, map[string][]string, CreativeModelPolicyDiagnostics) {
	groups := creativeModelPolicyAdminGroups(policy)
	usableGroupDescriptions := setting.GetUserUsableGroupsCopy()
	modelPools := make([]CreativeModelPolicyGroupPool, 0, len(groups))
	poolsByGroup := make(map[string][]string, len(groups))
	diagnostics := CreativeModelPolicyDiagnostics{}
	for _, group := range groups {
		models, _ := GetUserCreativeModelPool(group)
		poolsByGroup[group] = models
		effective, _ := BuildEffectiveCreativeModelPolicy(policy, group, models)
		if effective.Stale != nil {
			if diagnostics.StaleByGroup == nil {
				diagnostics.StaleByGroup = make(map[string]CreativeModelPolicyStale)
			}
			diagnostics.StaleByGroup[group] = *effective.Stale
		}
		modelPools = append(modelPools, CreativeModelPolicyGroupPool{
			Group:            group,
			Description:      usableGroupDescriptions[group],
			Models:           models,
			ModelsByModality: creativeModelPolicyModelsByModality(models),
			ModelCount:       len(models),
			EffectivePolicy:  effective,
		})
	}
	return modelPools, poolsByGroup, diagnostics
}

func creativeModelPolicyModelsByModality(modelIDs []string) map[string][]string {
	availableModels := creativeModelPolicyAvailableModelsFromIDs(modelIDs)
	modelsByModality := make(map[string][]string, len(creativeModelPolicyModalities))
	for _, modality := range creativeModelPolicyModalities {
		modelsByModality[modality] = make([]string, 0)
	}
	for _, item := range availableModels {
		for _, modality := range creativeModelPolicyModalities {
			if CreativeModelSupportsPolicyModality(item.ID, item.SupportedEndpointTypes, modality) {
				modelsByModality[modality] = append(modelsByModality[modality], item.ID)
			}
		}
	}
	return modelsByModality
}

func creativeModelPolicyAdminGroups(policy CreativeModelPolicy) []string {
	seen := make(map[string]struct{})
	for group := range setting.GetUserUsableGroupsCopy() {
		if strings.TrimSpace(group) != "" {
			seen[group] = struct{}{}
		}
	}
	for group := range policy.Groups {
		if strings.TrimSpace(group) != "" {
			seen[group] = struct{}{}
		}
	}
	for _, ability := range model.GetAllEnableAbilities() {
		if strings.TrimSpace(ability.Group) != "" {
			seen[ability.Group] = struct{}{}
		}
	}
	if len(seen) == 0 {
		seen["default"] = struct{}{}
	}
	groups := make([]string, 0, len(seen))
	for group := range seen {
		groups = append(groups, group)
	}
	sort.Strings(groups)
	return groups
}

func creativeModelPolicySet(models []string) map[string]struct{} {
	set := make(map[string]struct{}, len(models))
	for _, modelID := range models {
		modelID = strings.TrimSpace(modelID)
		if modelID != "" {
			set[modelID] = struct{}{}
		}
	}
	return set
}

func creativeModelPolicyAvailableModelsFromIDs(modelIDs []string) []CreativeModelPolicyAvailableModel {
	available := make([]CreativeModelPolicyAvailableModel, 0, len(modelIDs))
	for _, modelID := range modelIDs {
		modelID = strings.TrimSpace(modelID)
		if modelID == "" {
			continue
		}
		available = append(available, CreativeModelPolicyAvailableModel{
			ID:                     modelID,
			SupportedEndpointTypes: model.GetModelSupportEndpointTypes(modelID),
		})
	}
	return available
}

func creativeModelPolicyAvailableSets(models []CreativeModelPolicyAvailableModel) map[string]map[string]struct{} {
	sets := make(map[string]map[string]struct{}, len(creativeModelPolicyModalities))
	for _, modality := range creativeModelPolicyModalities {
		sets[modality] = make(map[string]struct{})
	}
	for _, item := range models {
		modelID := strings.TrimSpace(item.ID)
		if modelID == "" {
			continue
		}
		for _, modality := range creativeModelPolicyModalities {
			if CreativeModelSupportsPolicyModality(modelID, item.SupportedEndpointTypes, modality) {
				sets[modality][modelID] = struct{}{}
			}
		}
	}
	return sets
}

func CreativeModelSupportsPolicyModality(modelID string, endpointTypes []constant.EndpointType, modality string) bool {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return false
	}
	modality = strings.TrimSpace(strings.ToLower(modality))
	mediaHint := creativeModelPolicyMediaHint(modelID)
	switch modality {
	case "image":
		if mediaHint == "image" || creativeEndpointTypesContain(endpointTypes, constant.EndpointTypeImageGeneration) {
			return true
		}
		return mediaHint == "" && len(endpointTypes) == 0
	case "video":
		if mediaHint == "video" || creativeEndpointTypesContain(endpointTypes, constant.EndpointTypeOpenAIVideo) {
			return true
		}
		return mediaHint == "" && len(endpointTypes) == 0
	case "audio":
		return mediaHint == "audio" || (mediaHint == "" && len(endpointTypes) == 0)
	case "text", "agent":
		if mediaHint != "" {
			return false
		}
		if len(endpointTypes) == 0 {
			return true
		}
		return creativeEndpointTypesContainAny(endpointTypes,
			constant.EndpointTypeOpenAI,
			constant.EndpointTypeOpenAIResponse,
			constant.EndpointTypeOpenAIResponseCompact,
			constant.EndpointTypeAnthropic,
			constant.EndpointTypeGemini,
		)
	default:
		return false
	}
}

func creativeEndpointTypesContainAny(endpointTypes []constant.EndpointType, expected ...constant.EndpointType) bool {
	for _, endpointType := range expected {
		if creativeEndpointTypesContain(endpointTypes, endpointType) {
			return true
		}
	}
	return false
}

func creativeEndpointTypesContain(endpointTypes []constant.EndpointType, expected constant.EndpointType) bool {
	for _, endpointType := range endpointTypes {
		if endpointType == expected {
			return true
		}
	}
	return false
}

func creativeModelPolicyMediaHint(modelID string) string {
	lower := strings.ToLower(strings.TrimSpace(modelID))
	if lower == "" {
		return ""
	}
	if common.IsImageGenerationModel(lower) || strings.HasPrefix(lower, "mj_") || strings.HasPrefix(lower, "mj-") || strings.Contains(lower, "midjourney") || strings.Contains(lower, "image") || strings.Contains(lower, "dall-e") || strings.Contains(lower, "cogview") {
		return "image"
	}
	if strings.Contains(lower, "suno") || strings.Contains(lower, "music") || strings.Contains(lower, "lyrics") || strings.Contains(lower, "audio") || strings.Contains(lower, "speech") {
		return "audio"
	}
	if strings.Contains(lower, "video") || strings.Contains(lower, "sora") || strings.Contains(lower, "veo") || strings.Contains(lower, "kling") || strings.Contains(lower, "seedance") || strings.Contains(lower, "runway") || strings.Contains(lower, "hailuo") || strings.Contains(lower, "pika") || strings.Contains(lower, "wanx") {
		return "video"
	}
	return ""
}

func creativeModelPolicyRuleEmpty(rule CreativeModelPolicyRule) bool {
	return len(rule.Defaults) == 0 && len(rule.Recommended) == 0
}

func creativeModelPolicyStaleEmpty(stale CreativeModelPolicyStale) bool {
	return len(stale.Defaults) == 0 && len(stale.Recommended) == 0
}

func creativeModelPolicyAllowedModality(modality string) bool {
	_, ok := creativeModelPolicyModalitySet[modality]
	return ok
}

func sortedPolicyKeys(object map[string]any) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func creativeModelPolicyUnsafeField(value any) (string, bool) {
	return creativeModelPolicyUnsafeFieldAt(value, "")
}

func creativeModelPolicyUnsafeFieldAt(value any, path string) (string, bool) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			childPath := key
			if path != "" {
				childPath = path + "." + key
			}
			if path != "groups" && creativeModelPolicyForbiddenKey(key) {
				return childPath, true
			}
			if found, ok := creativeModelPolicyUnsafeFieldAt(child, childPath); ok {
				return found, true
			}
		}
	case []any:
		for i, child := range typed {
			childPath := fmt.Sprintf("%s[%d]", path, i)
			if path == "" {
				childPath = fmt.Sprintf("[%d]", i)
			}
			if found, ok := creativeModelPolicyUnsafeFieldAt(child, childPath); ok {
				return found, true
			}
		}
	case string:
		if creativeModelPolicyForbiddenString(typed) {
			if path == "" {
				return "value", true
			}
			return path, true
		}
	}
	return "", false
}

func creativeModelPolicyForbiddenKey(key string) bool {
	normalized := creativeModelPolicyNormalizeKey(key)
	if normalized == "" {
		return false
	}
	if strings.HasPrefix(normalized, "upstream") || strings.Contains(normalized, "channel") || strings.Contains(normalized, "provider") || strings.Contains(normalized, "callback") || strings.Contains(normalized, "webhook") || strings.Contains(normalized, "notify") || strings.Contains(normalized, "notificat") || strings.Contains(normalized, "baseurl") || strings.Contains(normalized, "owner") || strings.Contains(normalized, "user") || strings.Contains(normalized, "routing") || strings.Contains(normalized, "routegroup") || strings.Contains(normalized, "groupoverride") {
		return true
	}
	switch normalized {
	case "apikey", "apikeys", "apisecret", "authorization", "bearer", "bearertoken", "key", "selectedkey", "requestkey", "reqkey", "secret", "secretkey", "token", "accesstoken", "refreshtoken", "idtoken", "internaltoken", "baseuri", "notify", "notifyurl", "callbackurl", "webhookurl", "route", "router", "routinggroup":
		return true
	default:
		return false
	}
}

func creativeModelPolicyForbiddenString(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return false
	}
	lower := strings.ToLower(trimmed)
	return strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "sk-") || strings.Contains(lower, "bearer ") || strings.Contains(lower, "api_key=") || strings.Contains(lower, "apikey=") || strings.Contains(lower, "secret=") || strings.Contains(lower, "token=")
}

func creativeModelPolicyNormalizeKey(key string) string {
	var builder strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(key)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

func policyToJSONValue(policy CreativeModelPolicy) map[string]any {
	value := map[string]any{"version": policy.Version}
	if value["version"] == 0 {
		value["version"] = 1
	}
	value["global"] = ruleToJSONValue(policy.Global)
	if len(policy.Groups) > 0 {
		groups := make(map[string]any, len(policy.Groups))
		for group, rule := range policy.Groups {
			groups[group] = ruleToJSONValue(rule)
		}
		value["groups"] = groups
	}
	return value
}

func ruleToJSONValue(rule CreativeModelPolicyRule) map[string]any {
	value := make(map[string]any)
	if len(rule.Defaults) > 0 {
		defaults := make(map[string]any, len(rule.Defaults))
		for modality, modelID := range rule.Defaults {
			defaults[modality] = modelID
		}
		value["defaults"] = defaults
	}
	if len(rule.Recommended) > 0 {
		recommended := make(map[string]any, len(rule.Recommended))
		for modality, models := range rule.Recommended {
			items := make([]any, 0, len(models))
			for _, modelID := range models {
				items = append(items, modelID)
			}
			recommended[modality] = items
		}
		value["recommended"] = recommended
	}
	return value
}
