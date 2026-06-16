package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
)

const (
	CreativeAdapterEnabledOptionKey      = "creative.adapter.enabled"
	CreativeAdapterCanaryGroupsOptionKey = "creative.adapter.canary_groups"
	CreativeModelBindingsOptionKey       = "creative.model_bindings"
)

var creativeParameterIDPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_:-]{0,63}$`)

var creativeParameterAllowedTypes = map[string]struct{}{
	"enum":    {},
	"string":  {},
	"number":  {},
	"integer": {},
	"boolean": {},
}

var creativeAdapterAllowedModalities = map[string]struct{}{
	"image": {},
}

var creativeAdapterAllowedPresets = map[string]struct{}{
	"mock_image_task":        {},
	"grsai_gpt_image_dryrun": {},
}

var creativeAdapterAllowedParameterTemplates = map[string]struct{}{
	"mock_gpt_image":  {},
	"grsai_gpt_image": {},
}

var creativeModelBindingsTopLevelKeys = map[string]struct{}{
	"version":  {},
	"bindings": {},
}

var creativeModelBindingAllowedKeys = map[string]struct{}{
	"id":                {},
	"providerModelId":   {},
	"priceModelId":      {},
	"displayName":       {},
	"modality":          {},
	"enabled":           {},
	"canaryGroups":      {},
	"channelId":         {},
	"adapterPreset":     {},
	"parameterTemplate": {},
	"recommendedScore":  {},
	"sortOrder":         {},
	"parameterSchema":   {},
}

var creativeParameterSchemaAllowedKeys = map[string]struct{}{
	"id":           {},
	"label":        {},
	"shortLabel":   {},
	"description":  {},
	"type":         {},
	"defaultValue": {},
	"options":      {},
	"min":          {},
	"max":          {},
	"step":         {},
	"required":     {},
	"order":        {},
	"hidden":       {},
}

var creativeParameterOptionAllowedKeys = map[string]struct{}{
	"value": {},
	"label": {},
}

type CreativeModelBindingsConfig struct {
	Version  int                          `json:"version"`
	Bindings []CreativeModelBindingConfig `json:"bindings"`
}

type CreativeModelBindingConfig struct {
	Id                string                            `json:"id"`
	ProviderModelId   string                            `json:"providerModelId"`
	PriceModelId      string                            `json:"priceModelId"`
	DisplayName       string                            `json:"displayName,omitempty"`
	Modality          string                            `json:"modality"`
	Enabled           bool                              `json:"enabled"`
	CanaryGroups      []string                          `json:"canaryGroups,omitempty"`
	ChannelId         *int                              `json:"channelId,omitempty"`
	AdapterPreset     string                            `json:"adapterPreset"`
	ParameterTemplate string                            `json:"parameterTemplate"`
	RecommendedScore  *int                              `json:"recommendedScore,omitempty"`
	SortOrder         *int                              `json:"sortOrder,omitempty"`
	ParameterSchema   []dto.CreativeParameterSchemaItem `json:"parameterSchema,omitempty"`
}

type CreativeModelBindingsAdminState struct {
	Config     CreativeModelBindingsConfig `json:"config"`
	ConfigJSON string                      `json:"configJSON"`
}

type CreativeModelBindingsDryRunResult struct {
	NoProviderCall bool                             `json:"noProviderCall"`
	Bindings       []CreativeModelBindingDryRunItem `json:"bindings"`
}

type CreativeModelBindingDryRunItem struct {
	Id                string         `json:"id"`
	ProviderModelId   string         `json:"providerModelId"`
	PriceModelId      string         `json:"priceModelId"`
	Modality          string         `json:"modality"`
	Enabled           bool           `json:"enabled"`
	AdapterPreset     string         `json:"adapterPreset"`
	ParameterTemplate string         `json:"parameterTemplate"`
	RequestPreview    map[string]any `json:"requestPreview"`
}

type CreativeGrsAIImageFixtureSummary struct {
	Id          string `json:"id"`
	Status      string `json:"status"`
	ResultCount int    `json:"resultCount"`
	Progress    int    `json:"progress,omitempty"`
	Error       string `json:"error,omitempty"`
}

type CreativeResolvedModelBinding struct {
	Binding           CreativeModelBindingConfig
	BindingId         string         `json:"bindingId"`
	ProviderModelId   string         `json:"providerModelId"`
	PriceModelId      string         `json:"priceModelId"`
	AdapterPreset     string         `json:"adapterPreset"`
	ParameterTemplate string         `json:"parameterTemplate"`
	UserParams        map[string]any `json:"userParams"`
}

var creativeParameterForbiddenFragments = []string{
	"apikey",
	"authorization",
	"bearer",
	"token",
	"secret",
	"key",
	"credential",
	"baseurl",
	"url",
	"endpoint",
	"host",
	"header",
	"channel",
	"provider",
	"modelid",
	"modelname",
	"modelref",
	"sourceprofileid",
	"profileid",
	"internaloptions",
	"onprogress",
	"onsubmitted",
	"idempotency",
	"route",
	"routing",
	"group",
	"user",
	"owner",
	"notify",
	"notifyhook",
	"callback",
	"webhook",
	"mjapisecret",
}

// GetCreativePreviewModelBindingsForGroup returns Phase-A preview bindings only.
// It is fail-closed by default: both the global flag and a canary group match
// are required. These bindings are catalog/schema previews only and are not a
// provider routing contract.
func GetCreativePreviewModelBindingsForGroup(userGroup string) []dto.CreativeModelCatalogItem {
	if !creativeAdapterPreviewEnabled() || !creativeAdapterCanaryGroupAllowed(userGroup) {
		return nil
	}
	binding := mockCreativeImagePreviewBinding()
	if err := ValidateCreativeParameterSchema(binding.ParameterSchema); err != nil {
		common.SysError("invalid built-in creative preview schema: " + err.Error())
		return nil
	}
	return []dto.CreativeModelCatalogItem{binding}
}

func GetStoredCreativeModelBindingsCatalogForGroup(userGroup string) []dto.CreativeModelCatalogItem {
	if !creativeAdapterPreviewEnabled() {
		return nil
	}
	config, err := GetStoredCreativeModelBindingsConfig()
	if err != nil {
		common.SysError("invalid creative model bindings config: " + err.Error())
		return nil
	}
	items := make([]dto.CreativeModelCatalogItem, 0, len(config.Bindings))
	for _, binding := range config.Bindings {
		if !binding.Enabled || binding.Modality != "image" || !creativeBindingCanaryGroupAllowed(binding, userGroup) {
			continue
		}
		if binding.AdapterPreset != "mock_image_task" || binding.ParameterTemplate != "mock_gpt_image" {
			continue
		}
		if err := ValidateCreativeParameterSchema(binding.ParameterSchema); err != nil {
			common.SysError("invalid stored creative binding schema for " + binding.Id + ": " + err.Error())
			continue
		}
		items = append(items, creativeModelCatalogItemFromBinding(binding))
	}
	sort.SliceStable(items, func(i, j int) bool {
		left, right := items[i], items[j]
		if left.SortOrder != nil && right.SortOrder != nil && *left.SortOrder != *right.SortOrder {
			return *left.SortOrder < *right.SortOrder
		}
		if left.SortOrder != nil && right.SortOrder == nil {
			return true
		}
		if left.SortOrder == nil && right.SortOrder != nil {
			return false
		}
		return left.Id < right.Id
	})
	return items
}

func ParseCreativeModelBindingsConfig(raw string) (CreativeModelBindingsConfig, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return CreativeModelBindingsConfig{Version: 1}, nil
	}
	if err := validateCreativeModelBindingsRawJSONKeys(trimmed); err != nil {
		return CreativeModelBindingsConfig{}, err
	}
	var config CreativeModelBindingsConfig
	if err := common.UnmarshalJsonStr(trimmed, &config); err != nil {
		return CreativeModelBindingsConfig{}, err
	}
	if config.Version == 0 {
		config.Version = 1
	}
	if config.Version != 1 {
		return CreativeModelBindingsConfig{}, fmt.Errorf("unsupported creative model bindings version %d", config.Version)
	}
	if config.Bindings == nil {
		config.Bindings = []CreativeModelBindingConfig{}
	}
	config, err := NormalizeCreativeModelBindingsConfig(config)
	if err != nil {
		return CreativeModelBindingsConfig{}, err
	}
	return config, nil
}

func GetStoredCreativeModelBindingsConfig() (CreativeModelBindingsConfig, error) {
	return ParseCreativeModelBindingsConfig(creativeOptionValue(CreativeModelBindingsOptionKey))
}

func BuildCreativeModelBindingsAdminState(config CreativeModelBindingsConfig) (CreativeModelBindingsAdminState, error) {
	normalized, err := NormalizeCreativeModelBindingsConfig(config)
	if err != nil {
		return CreativeModelBindingsAdminState{}, err
	}
	configJSON, err := NormalizeCreativeModelBindingsConfigJSON(config)
	if err != nil {
		return CreativeModelBindingsAdminState{}, err
	}
	return CreativeModelBindingsAdminState{Config: normalized, ConfigJSON: configJSON}, nil
}

func GetCreativeModelBindingsAdminState() (CreativeModelBindingsAdminState, error) {
	config, err := GetStoredCreativeModelBindingsConfig()
	if err != nil {
		return CreativeModelBindingsAdminState{}, err
	}
	return BuildCreativeModelBindingsAdminState(config)
}

func ResolveCreativeImageModelBindingForGroup(bindingID string, userGroup string, userParams map[string]any) (CreativeResolvedModelBinding, error) {
	if !creativeAdapterPreviewEnabled() {
		return CreativeResolvedModelBinding{}, errors.New("creative adapter is disabled")
	}
	bindingID = strings.TrimSpace(bindingID)
	if bindingID == "" {
		return CreativeResolvedModelBinding{}, errors.New("creative image model is required")
	}
	config, err := GetStoredCreativeModelBindingsConfig()
	if err != nil {
		return CreativeResolvedModelBinding{}, err
	}
	for _, binding := range config.Bindings {
		if binding.Id != bindingID {
			continue
		}
		if !binding.Enabled {
			return CreativeResolvedModelBinding{}, fmt.Errorf("creative image binding %q is disabled", bindingID)
		}
		if binding.Modality != "image" {
			return CreativeResolvedModelBinding{}, fmt.Errorf("creative binding %q is not an image binding", bindingID)
		}
		if binding.AdapterPreset != "mock_image_task" || binding.ParameterTemplate != "mock_gpt_image" {
			return CreativeResolvedModelBinding{}, fmt.Errorf("creative binding %q is not available for mock image tasks", bindingID)
		}
		if !creativeBindingCanaryGroupAllowed(binding, userGroup) {
			return CreativeResolvedModelBinding{}, fmt.Errorf("creative image binding %q is not enabled for this group", bindingID)
		}
		normalizedParams, err := ValidateCreativeUserParamsForSchema(binding.ParameterSchema, userParams)
		if err != nil {
			return CreativeResolvedModelBinding{}, err
		}
		return CreativeResolvedModelBinding{
			Binding:           binding,
			BindingId:         binding.Id,
			ProviderModelId:   binding.ProviderModelId,
			PriceModelId:      binding.PriceModelId,
			AdapterPreset:     binding.AdapterPreset,
			ParameterTemplate: binding.ParameterTemplate,
			UserParams:        normalizedParams,
		}, nil
	}
	return CreativeResolvedModelBinding{}, fmt.Errorf("creative image binding %q was not found", bindingID)
}

func GetCreativeModelBindingByID(bindingID string) (CreativeModelBindingConfig, bool, error) {
	bindingID = strings.TrimSpace(bindingID)
	if bindingID == "" {
		return CreativeModelBindingConfig{}, false, nil
	}
	config, err := GetStoredCreativeModelBindingsConfig()
	if err != nil {
		return CreativeModelBindingConfig{}, false, err
	}
	for _, binding := range config.Bindings {
		if binding.Id == bindingID {
			return binding, true, nil
		}
	}
	return CreativeModelBindingConfig{}, false, nil
}

func creativeModelCatalogItemFromBinding(binding CreativeModelBindingConfig) dto.CreativeModelCatalogItem {
	displayName := binding.DisplayName
	if displayName == "" {
		displayName = binding.Id
	}
	return dto.CreativeModelCatalogItem{
		Id:                     binding.Id,
		Object:                 "model",
		Created:                0,
		OwnedBy:                "new-api-creative",
		SupportedEndpointTypes: []constant.EndpointType{constant.EndpointTypeImageGeneration},
		ProviderModelId:        binding.ProviderModelId,
		PriceModelId:           binding.PriceModelId,
		Label:                  displayName,
		DisplayName:            displayName,
		ShortLabel:             displayName,
		Description:            "Creative adapter binding managed by new-api.",
		Type:                   binding.Modality,
		Modality:               binding.Modality,
		Vendor:                 "new-api-creative",
		Tags:                   []string{"creative-adapter", "mock", binding.Modality},
		RecommendedScore:       binding.RecommendedScore,
		SortOrder:              binding.SortOrder,
		ParameterSchema:        creativeVisibleParameterSchema(binding.ParameterSchema),
	}
}

func creativeVisibleParameterSchema(schema []dto.CreativeParameterSchemaItem) []dto.CreativeParameterSchemaItem {
	if len(schema) == 0 {
		return nil
	}
	visible := make([]dto.CreativeParameterSchemaItem, 0, len(schema))
	for _, item := range schema {
		if item.Hidden {
			continue
		}
		visible = append(visible, item)
	}
	return visible
}

func creativeBindingCanaryGroupAllowed(binding CreativeModelBindingConfig, userGroup string) bool {
	if len(binding.CanaryGroups) == 0 {
		return false
	}
	userGroup = strings.TrimSpace(userGroup)
	for _, group := range binding.CanaryGroups {
		if group == "*" || group == userGroup {
			return true
		}
	}
	return false
}

func ValidateCreativeUserParamsForSchema(schema []dto.CreativeParameterSchemaItem, userParams map[string]any) (map[string]any, error) {
	allowed := make(map[string]dto.CreativeParameterSchemaItem, len(schema))
	hidden := make(map[string]struct{}, len(schema))
	normalized := make(map[string]any, len(userParams))
	for _, item := range schema {
		if item.Hidden {
			hidden[item.Id] = struct{}{}
			continue
		}
		allowed[item.Id] = item
	}
	for key, value := range userParams {
		trimmedKey := strings.TrimSpace(key)
		item, ok := allowed[trimmedKey]
		if !ok {
			if _, isHidden := hidden[trimmedKey]; isHidden {
				return nil, fmt.Errorf("userParams contains hidden field %q", trimmedKey)
			}
			if CreativeForbiddenKey(trimmedKey) {
				return nil, fmt.Errorf("userParams contains forbidden field %q", trimmedKey)
			}
			return nil, fmt.Errorf("userParams contains unsupported field %q", trimmedKey)
		}
		typedValue, err := validateCreativeUserParamValue(item, value)
		if err != nil {
			return nil, fmt.Errorf("userParams.%s invalid: %w", trimmedKey, err)
		}
		normalized[trimmedKey] = typedValue
	}
	for _, item := range schema {
		if item.Hidden {
			if _, exists := userParams[item.Id]; exists {
				return nil, fmt.Errorf("userParams contains hidden field %q", item.Id)
			}
			continue
		}
		if item.Required {
			if _, exists := normalized[item.Id]; !exists {
				return nil, fmt.Errorf("userParams.%s is required", item.Id)
			}
		}
	}
	return normalized, nil
}

func NormalizeCreativeModelBindingsConfig(config CreativeModelBindingsConfig) (CreativeModelBindingsConfig, error) {
	if config.Version == 0 {
		config.Version = 1
	}
	if config.Bindings == nil {
		config.Bindings = []CreativeModelBindingConfig{}
	}
	for index := range config.Bindings {
		binding := &config.Bindings[index]
		binding.Id = strings.TrimSpace(binding.Id)
		binding.ProviderModelId = strings.TrimSpace(binding.ProviderModelId)
		binding.PriceModelId = strings.TrimSpace(binding.PriceModelId)
		binding.DisplayName = strings.TrimSpace(binding.DisplayName)
		binding.Modality = strings.ToLower(strings.TrimSpace(binding.Modality))
		binding.AdapterPreset = strings.TrimSpace(binding.AdapterPreset)
		binding.ParameterTemplate = strings.TrimSpace(binding.ParameterTemplate)
		for groupIndex := range binding.CanaryGroups {
			binding.CanaryGroups[groupIndex] = strings.TrimSpace(binding.CanaryGroups[groupIndex])
		}
		for schemaIndex := range binding.ParameterSchema {
			item := &binding.ParameterSchema[schemaIndex]
			item.Id = strings.TrimSpace(item.Id)
			item.Label = strings.TrimSpace(item.Label)
			item.ShortLabel = strings.TrimSpace(item.ShortLabel)
			item.Description = strings.TrimSpace(item.Description)
			item.Type = strings.ToLower(strings.TrimSpace(item.Type))
			for optionIndex := range item.Options {
				item.Options[optionIndex].Label = strings.TrimSpace(item.Options[optionIndex].Label)
			}
		}
	}
	if err := ValidateCreativeModelBindingsConfig(config); err != nil {
		return CreativeModelBindingsConfig{}, err
	}
	return config, nil
}

func NormalizeCreativeModelBindingsConfigJSON(config CreativeModelBindingsConfig) (string, error) {
	normalized, err := NormalizeCreativeModelBindingsConfig(config)
	if err != nil {
		return "", err
	}
	bytes, err := common.Marshal(normalized)
	if err != nil {
		return "", err
	}
	return string(bytes), nil
}

func UpdateStoredCreativeModelBindingsConfig(config CreativeModelBindingsConfig) (CreativeModelBindingsConfig, string, error) {
	configJSON, err := NormalizeCreativeModelBindingsConfigJSON(config)
	if err != nil {
		return CreativeModelBindingsConfig{}, "", err
	}
	if err := model.UpdateOption(CreativeModelBindingsOptionKey, configJSON); err != nil {
		return CreativeModelBindingsConfig{}, "", err
	}
	parsed, err := ParseCreativeModelBindingsConfig(configJSON)
	if err != nil {
		return CreativeModelBindingsConfig{}, "", err
	}
	return parsed, configJSON, nil
}

func BuildCreativeModelBindingsDryRun(config CreativeModelBindingsConfig) (CreativeModelBindingsDryRunResult, error) {
	config, err := NormalizeCreativeModelBindingsConfig(config)
	if err != nil {
		return CreativeModelBindingsDryRunResult{}, err
	}
	result := CreativeModelBindingsDryRunResult{
		NoProviderCall: true,
		Bindings:       make([]CreativeModelBindingDryRunItem, 0, len(config.Bindings)),
	}
	for _, binding := range config.Bindings {
		preview := creativeModelBindingDryRunRequestPreview(binding)
		result.Bindings = append(result.Bindings, CreativeModelBindingDryRunItem{
			Id:                binding.Id,
			ProviderModelId:   binding.ProviderModelId,
			PriceModelId:      binding.PriceModelId,
			Modality:          binding.Modality,
			Enabled:           binding.Enabled,
			AdapterPreset:     binding.AdapterPreset,
			ParameterTemplate: binding.ParameterTemplate,
			RequestPreview:    RedactCreativeDryRunValue(preview).(map[string]any),
		})
	}
	return result, nil
}

func creativeModelBindingDryRunRequestPreview(binding CreativeModelBindingConfig) map[string]any {
	if binding.AdapterPreset == "grsai_gpt_image_dryrun" && binding.ParameterTemplate == "grsai_gpt_image" {
		return map[string]any{
			"transport":         "fixture",
			"adapterFamily":     "grsai",
			"operation":         "image_generate_request_preview",
			"offline":           true,
			"model":             binding.ProviderModelId,
			"priceModel":        binding.PriceModelId,
			"parameterTemplate": binding.ParameterTemplate,
			"requestBody": map[string]any{
				"model":       binding.ProviderModelId,
				"prompt":      "<user-prompt>",
				"images":      []any{"<managed-input-image-ref>"},
				"aspectRatio": creativeDryRunSchemaDefault(binding.ParameterSchema, "aspectRatio", "1024x1024"),
				"replyType":   "json",
			},
			"responseShape": map[string]any{
				"id":       "<provider-task-id>",
				"status":   "running|violation|succeeded|failed",
				"results":  []any{map[string]any{"url": "[REDACTED]"}},
				"progress": 0,
				"error":    "<provider-error>",
			},
		}
	}
	return map[string]any{
		"transport":         "mock",
		"operation":         "image_task_preview",
		"model":             binding.ProviderModelId,
		"priceModel":        binding.PriceModelId,
		"parameterTemplate": binding.ParameterTemplate,
	}
}

func creativeDryRunSchemaDefault(schema []dto.CreativeParameterSchemaItem, id string, fallback any) any {
	for _, item := range schema {
		if item.Id == id && item.DefaultValue != nil {
			return item.DefaultValue
		}
	}
	return fallback
}

func creativeAdapterPresetTemplateAllowed(preset string, template string) bool {
	switch preset {
	case "mock_image_task":
		return template == "mock_gpt_image"
	case "grsai_gpt_image_dryrun":
		return template == "grsai_gpt_image"
	default:
		return false
	}
}

func ParseCreativeGrsAIImageFixtureResponse(raw []byte) (CreativeGrsAIImageFixtureSummary, error) {
	var response struct {
		Id      string `json:"id"`
		Status  string `json:"status"`
		Results []struct {
			URL string `json:"url"`
		} `json:"results"`
		Progress int    `json:"progress"`
		Error    string `json:"error"`
	}
	if err := common.Unmarshal(raw, &response); err != nil {
		return CreativeGrsAIImageFixtureSummary{}, err
	}
	id := strings.TrimSpace(response.Id)
	status := strings.TrimSpace(strings.ToLower(response.Status))
	if id == "" {
		return CreativeGrsAIImageFixtureSummary{}, errors.New("grsai image fixture response id is required")
	}
	switch status {
	case "running", "violation", "succeeded", "failed":
	default:
		return CreativeGrsAIImageFixtureSummary{}, fmt.Errorf("grsai image fixture response status %q is unsupported", status)
	}
	if status == "succeeded" && len(response.Results) == 0 {
		return CreativeGrsAIImageFixtureSummary{}, errors.New("grsai image fixture response has no result")
	}
	summary := CreativeGrsAIImageFixtureSummary{
		Id:          id,
		Status:      status,
		ResultCount: len(response.Results),
		Progress:    response.Progress,
	}
	if response.Error != "" && !CreativeSensitiveStringValue(response.Error) {
		summary.Error = response.Error
	}
	return summary, nil
}

func validateCreativeModelBindingsRawJSONKeys(raw string) error {
	var root map[string]json.RawMessage
	if err := common.Unmarshal([]byte(raw), &root); err != nil {
		return err
	}
	if root == nil {
		return errors.New("creative.model_bindings must be an object")
	}
	if err := validateCreativeRawObjectKeys("creative.model_bindings", root, creativeModelBindingsTopLevelKeys); err != nil {
		return err
	}
	if rawBindings, ok := root["bindings"]; ok {
		if creativeRawJSONIsNull(rawBindings) {
			return errors.New("creative.model_bindings bindings must be an array")
		}
		var bindings []json.RawMessage
		if err := common.Unmarshal(rawBindings, &bindings); err != nil {
			return fmt.Errorf("creative.model_bindings bindings must be an array: %w", err)
		}
		for index, rawBinding := range bindings {
			scope := fmt.Sprintf("creative.model_bindings.bindings[%d]", index)
			var binding map[string]json.RawMessage
			if err := common.Unmarshal(rawBinding, &binding); err != nil {
				return fmt.Errorf("%s must be an object: %w", scope, err)
			}
			if err := validateCreativeRawObjectKeys(scope, binding, creativeModelBindingAllowedKeys); err != nil {
				return err
			}
			if rawSchema, ok := binding["parameterSchema"]; ok {
				if creativeRawJSONIsNull(rawSchema) {
					return fmt.Errorf("%s.parameterSchema must be an array", scope)
				}
				var schema []json.RawMessage
				if err := common.Unmarshal(rawSchema, &schema); err != nil {
					return fmt.Errorf("%s.parameterSchema must be an array: %w", scope, err)
				}
				for schemaIndex, rawItem := range schema {
					schemaScope := fmt.Sprintf("%s.parameterSchema[%d]", scope, schemaIndex)
					var item map[string]json.RawMessage
					if err := common.Unmarshal(rawItem, &item); err != nil {
						return fmt.Errorf("%s must be an object: %w", schemaScope, err)
					}
					if err := validateCreativeRawObjectKeys(schemaScope, item, creativeParameterSchemaAllowedKeys); err != nil {
						return err
					}
					if rawOptions, ok := item["options"]; ok {
						if creativeRawJSONIsNull(rawOptions) {
							return fmt.Errorf("%s.options must be an array", schemaScope)
						}
						var options []json.RawMessage
						if err := common.Unmarshal(rawOptions, &options); err != nil {
							return fmt.Errorf("%s.options must be an array: %w", schemaScope, err)
						}
						for optionIndex, rawOption := range options {
							optionScope := fmt.Sprintf("%s.options[%d]", schemaScope, optionIndex)
							var option map[string]json.RawMessage
							if err := common.Unmarshal(rawOption, &option); err != nil {
								return fmt.Errorf("%s must be an object: %w", optionScope, err)
							}
							if err := validateCreativeRawObjectKeys(optionScope, option, creativeParameterOptionAllowedKeys); err != nil {
								return err
							}
						}
					}
				}
			}
		}
	}
	return nil
}

func creativeRawJSONIsNull(raw json.RawMessage) bool {
	return strings.EqualFold(strings.TrimSpace(string(raw)), "null")
}

func validateCreativeRawObjectKeys(scope string, object map[string]json.RawMessage, allowed map[string]struct{}) error {
	for key := range object {
		if _, ok := allowed[key]; ok {
			continue
		}
		if CreativeForbiddenKey(key) {
			return fmt.Errorf("%s contains forbidden key %q", scope, key)
		}
		return fmt.Errorf("%s contains unsupported key %q", scope, key)
	}
	return nil
}

func RedactCreativeDryRunValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		redacted := make(map[string]any, len(typed))
		for key, item := range typed {
			if CreativeForbiddenKey(key) {
				redacted[key] = "[REDACTED]"
				continue
			}
			redacted[key] = RedactCreativeDryRunValue(item)
		}
		return redacted
	case []any:
		redacted := make([]any, 0, len(typed))
		for _, item := range typed {
			redacted = append(redacted, RedactCreativeDryRunValue(item))
		}
		return redacted
	case []string:
		redacted := make([]any, 0, len(typed))
		for _, item := range typed {
			redacted = append(redacted, RedactCreativeDryRunValue(item))
		}
		return redacted
	default:
		if text, ok := value.(string); ok && CreativeSensitiveStringValue(text) {
			return "[REDACTED]"
		}
		return value
	}
}

func creativeChannelSupportsProviderModel(channel *model.Channel, providerModelID string) bool {
	if channel == nil {
		return false
	}
	providerModelID = strings.TrimSpace(providerModelID)
	if providerModelID == "" {
		return false
	}
	models := make(map[string]struct{})
	for _, modelID := range channel.GetModels() {
		trimmedModelID := strings.TrimSpace(modelID)
		if trimmedModelID == "" {
			continue
		}
		models[trimmedModelID] = struct{}{}
	}
	modelMapping := strings.TrimSpace(channel.GetModelMapping())
	mapped := make(map[string]string)
	if modelMapping != "" && modelMapping != "{}" {
		if err := common.Unmarshal([]byte(modelMapping), &mapped); err != nil {
			return false
		}
	}
	if _, ok := models[providerModelID]; ok {
		return creativeChannelModelMappingResolvesTo(providerModelID, mapped, providerModelID)
	}
	for from := range mapped {
		trimmedFrom := strings.TrimSpace(from)
		if _, ok := models[trimmedFrom]; ok && creativeChannelModelMappingResolvesTo(trimmedFrom, mapped, providerModelID) {
			return true
		}
	}
	return false
}

func creativeChannelModelMappingResolvesTo(modelID string, mapped map[string]string, providerModelID string) bool {
	current := strings.TrimSpace(modelID)
	if current == "" {
		return false
	}
	visited := map[string]struct{}{current: {}}
	for {
		next := strings.TrimSpace(mapped[current])
		if next == "" {
			return current == providerModelID
		}
		if _, ok := visited[next]; ok {
			return next == current && current == providerModelID
		}
		visited[next] = struct{}{}
		current = next
	}
}

func ValidateCreativeModelBindingsConfig(config CreativeModelBindingsConfig) error {
	if config.Version != 1 {
		return fmt.Errorf("unsupported creative model bindings version %d", config.Version)
	}
	seen := make(map[string]struct{}, len(config.Bindings))
	for _, binding := range config.Bindings {
		id := strings.TrimSpace(binding.Id)
		if id == "" {
			return errors.New("binding id is required")
		}
		if !creativeParameterIDPattern.MatchString(id) {
			return fmt.Errorf("binding %q has invalid id", id)
		}
		if CreativeForbiddenKey(id) {
			return fmt.Errorf("binding %q uses a forbidden control field", id)
		}
		if CreativeSensitiveStringValue(binding.DisplayName) {
			return fmt.Errorf("binding %q displayName contains sensitive material", id)
		}
		key := strings.ToLower(id)
		if _, ok := seen[key]; ok {
			return fmt.Errorf("binding %q is duplicated", id)
		}
		seen[key] = struct{}{}
		if binding.Enabled && creativeBindingIDCollidesWithEnabledChannelModel(id) {
			return fmt.Errorf("binding %q conflicts with an enabled channel model id", id)
		}
		if strings.TrimSpace(binding.ProviderModelId) == "" {
			return fmt.Errorf("binding %q providerModelId is required", id)
		}
		if CreativeSensitiveStringValue(binding.ProviderModelId) {
			return fmt.Errorf("binding %q providerModelId contains sensitive material", id)
		}
		if strings.TrimSpace(binding.PriceModelId) == "" {
			return fmt.Errorf("binding %q priceModelId is required", id)
		}
		if CreativeSensitiveStringValue(binding.PriceModelId) {
			return fmt.Errorf("binding %q priceModelId contains sensitive material", id)
		}
		modality := strings.TrimSpace(strings.ToLower(binding.Modality))
		if modality == "" {
			return fmt.Errorf("binding %q modality is required", id)
		}
		if _, ok := creativeAdapterAllowedModalities[modality]; !ok {
			return fmt.Errorf("binding %q modality %q is not supported", id, binding.Modality)
		}
		preset := strings.TrimSpace(binding.AdapterPreset)
		if preset == "" {
			return fmt.Errorf("binding %q adapterPreset is required", id)
		}
		if _, ok := creativeAdapterAllowedPresets[preset]; !ok {
			return fmt.Errorf("binding %q adapterPreset %q is not supported", id, binding.AdapterPreset)
		}
		template := strings.TrimSpace(binding.ParameterTemplate)
		if template == "" {
			return fmt.Errorf("binding %q parameterTemplate is required", id)
		}
		if _, ok := creativeAdapterAllowedParameterTemplates[template]; !ok {
			return fmt.Errorf("binding %q parameterTemplate %q is not supported", id, binding.ParameterTemplate)
		}
		if !creativeAdapterPresetTemplateAllowed(preset, template) {
			return fmt.Errorf("binding %q adapterPreset %q cannot use parameterTemplate %q", id, preset, template)
		}
		if binding.ChannelId != nil && *binding.ChannelId <= 0 {
			return fmt.Errorf("binding %q channelId must be positive", id)
		}
		if binding.ChannelId != nil {
			channel, err := model.GetChannelById(*binding.ChannelId, false)
			if err != nil {
				return fmt.Errorf("binding %q channelId %d was not found", id, *binding.ChannelId)
			}
			if channel.Status != common.ChannelStatusEnabled {
				return fmt.Errorf("binding %q channelId %d is disabled", id, *binding.ChannelId)
			}
			if !creativeChannelSupportsProviderModel(channel, binding.ProviderModelId) {
				return fmt.Errorf("binding %q channelId %d does not support providerModelId %q", id, *binding.ChannelId, binding.ProviderModelId)
			}
		}
		for _, group := range binding.CanaryGroups {
			trimmedGroup := strings.TrimSpace(group)
			if trimmedGroup == "" {
				return fmt.Errorf("binding %q canaryGroups contains an empty group", id)
			}
			if CreativeSensitiveStringValue(trimmedGroup) {
				return fmt.Errorf("binding %q canary group %q contains sensitive material", id, group)
			}
			if trimmedGroup != "*" && CreativeForbiddenKey(trimmedGroup) {
				return fmt.Errorf("binding %q canary group %q is forbidden", id, group)
			}
		}
		if err := ValidateCreativeParameterSchema(binding.ParameterSchema); err != nil {
			return fmt.Errorf("binding %q parameterSchema invalid: %w", id, err)
		}
	}
	return nil
}

func creativeBindingIDCollidesWithEnabledChannelModel(bindingID string) bool {
	normalizedBindingID := strings.ToLower(strings.TrimSpace(bindingID))
	if normalizedBindingID == "" {
		return false
	}
	for _, ability := range model.GetAllEnableAbilities() {
		if strings.ToLower(strings.TrimSpace(ability.Model)) == normalizedBindingID {
			return true
		}
	}
	return false
}

func ValidateCreativeParameterSchema(schema []dto.CreativeParameterSchemaItem) error {
	if len(schema) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(schema))
	for _, item := range schema {
		id := strings.TrimSpace(item.Id)
		if id == "" {
			return errors.New("schema item id is required")
		}
		if !creativeParameterIDPattern.MatchString(id) {
			return fmt.Errorf("schema item %q has invalid id", id)
		}
		if CreativeForbiddenKey(id) {
			return fmt.Errorf("schema item %q uses a forbidden control field", id)
		}
		key := strings.ToLower(id)
		if _, ok := seen[key]; ok {
			return fmt.Errorf("schema item %q is duplicated", id)
		}
		seen[key] = struct{}{}
		if strings.TrimSpace(item.Label) == "" {
			return fmt.Errorf("schema item %q label is required", id)
		}
		for _, label := range []string{item.Label, item.ShortLabel, item.Description} {
			if CreativeSensitiveStringValue(label) {
				return fmt.Errorf("schema item %q display text contains sensitive material", id)
			}
		}
		paramType := strings.TrimSpace(strings.ToLower(item.Type))
		if _, ok := creativeParameterAllowedTypes[paramType]; !ok {
			return fmt.Errorf("schema item %q has unsupported type %q", id, item.Type)
		}
		if item.DefaultValue != nil {
			if !creativeParameterScalarValue(item.DefaultValue) {
				return fmt.Errorf("schema item %q defaultValue has non-scalar value", id)
			}
			if creativeParameterValueSensitive(item.DefaultValue) {
				return fmt.Errorf("schema item %q defaultValue contains sensitive material", id)
			}
			if paramType != "enum" && !creativeParameterDefaultMatchesType(paramType, item.DefaultValue) {
				return fmt.Errorf("schema item %q defaultValue does not match type %q", id, paramType)
			}
		}
		if paramType == "enum" {
			if len(item.Options) == 0 {
				return fmt.Errorf("schema item %q enum requires options", id)
			}
			for _, option := range item.Options {
				if !creativeParameterScalarValue(option.Value) {
					return fmt.Errorf("schema item %q enum option has non-scalar value", id)
				}
				if creativeParameterValueSensitive(option.Value) || CreativeSensitiveStringValue(option.Label) {
					return fmt.Errorf("schema item %q enum option contains sensitive material", id)
				}
			}
			if item.DefaultValue != nil && !creativeParameterOptionContainsValue(item.Options, item.DefaultValue) {
				return fmt.Errorf("schema item %q defaultValue is not in options", id)
			}
		}
	}
	return nil
}

func validateCreativeUserParamValue(item dto.CreativeParameterSchemaItem, value any) (any, error) {
	if value == nil {
		return nil, errors.New("value is required")
	}
	if creativeParameterValueSensitive(value) {
		return nil, errors.New("value contains sensitive material")
	}
	paramType := strings.TrimSpace(strings.ToLower(item.Type))
	switch paramType {
	case "enum":
		if !creativeParameterScalarValue(value) {
			return nil, errors.New("enum value must be scalar")
		}
		if !creativeParameterOptionContainsValue(item.Options, value) {
			return nil, errors.New("enum value is not allowed")
		}
		return value, nil
	case "string":
		if _, ok := value.(string); !ok {
			return nil, errors.New("string value expected")
		}
		return value, nil
	case "boolean":
		if _, ok := value.(bool); !ok {
			return nil, errors.New("boolean value expected")
		}
		return value, nil
	case "number":
		number, _, ok := creativeParameterNumber(value)
		if !ok {
			return nil, errors.New("number value expected")
		}
		if item.Min != nil && number < *item.Min {
			return nil, errors.New("number is below minimum")
		}
		if item.Max != nil && number > *item.Max {
			return nil, errors.New("number is above maximum")
		}
		return value, nil
	case "integer":
		number, _, ok := creativeParameterNumber(value)
		if !ok || math.Trunc(number) != number {
			return nil, errors.New("integer value expected")
		}
		if item.Min != nil && number < *item.Min {
			return nil, errors.New("integer is below minimum")
		}
		if item.Max != nil && number > *item.Max {
			return nil, errors.New("integer is above maximum")
		}
		return int(number), nil
	default:
		return nil, errors.New("unsupported schema type")
	}
}

func creativeAdapterPreviewEnabled() bool {
	return strings.EqualFold(strings.TrimSpace(creativeOptionValue(CreativeAdapterEnabledOptionKey)), "true")
}

func creativeAdapterCanaryGroupAllowed(userGroup string) bool {
	allowed := creativeOptionList(CreativeAdapterCanaryGroupsOptionKey)
	if len(allowed) == 0 {
		return false
	}
	group := strings.TrimSpace(userGroup)
	for _, candidate := range allowed {
		if candidate == "*" || candidate == group {
			return true
		}
	}
	return false
}

func creativeOptionValue(key string) string {
	common.OptionMapRWMutex.RLock()
	defer common.OptionMapRWMutex.RUnlock()
	return common.OptionMap[key]
}

func creativeOptionList(key string) []string {
	raw := strings.TrimSpace(creativeOptionValue(key))
	if raw == "" {
		return nil
	}
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		switch r {
		case ',', ';', '|', '\n', '\r', '\t', ' ':
			return true
		default:
			return false
		}
	})
	seen := make(map[string]struct{}, len(parts))
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		value := strings.TrimSpace(part)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		values = append(values, value)
	}
	sort.Strings(values)
	return values
}

func NormalizeCreativeForbiddenKey(key string) string {
	var builder strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(key)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

func CreativeForbiddenKey(key string) bool {
	normalized := NormalizeCreativeForbiddenKey(key)
	if normalized == "" {
		return false
	}
	for _, fragment := range creativeParameterForbiddenFragments {
		if strings.Contains(normalized, fragment) {
			return true
		}
	}
	return false
}

func CreativeSensitiveStringValue(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return false
	}
	lower := strings.ToLower(trimmed)
	if strings.Contains(lower, "://") || strings.HasPrefix(lower, "data:") {
		return true
	}
	for _, marker := range []string{
		"bearer ",
		"sk-",
		"x-amz-",
		"x-oss-",
		"signature=",
		"credential=",
		"expires=",
		"cookie=",
		"set-cookie:",
		"csrf=",
		"nonce=",
		"api_key=",
		"apikey=",
		"access_key=",
		"accesskey=",
		"object_key=",
		"objectkey=",
		"secret=",
		"token=",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return CreativeForbiddenKey(trimmed)
}

func creativeParameterValueSensitive(value any) bool {
	text, ok := value.(string)
	return ok && CreativeSensitiveStringValue(text)
}

func creativeParameterDefaultMatchesType(paramType string, value any) bool {
	switch paramType {
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "number":
		return creativeParameterNumericValue(value)
	case "integer":
		return creativeParameterIntegerValue(value)
	default:
		return false
	}
}

func creativeParameterNumericValue(value any) bool {
	_, _, ok := creativeParameterNumber(value)
	return ok
}

func creativeParameterIntegerValue(value any) bool {
	number, integral, ok := creativeParameterNumber(value)
	return ok && (integral || math.Trunc(number) == number)
}

func creativeParameterNumber(value any) (float64, bool, bool) {
	switch typed := value.(type) {
	case int:
		return float64(typed), true, true
	case int8:
		return float64(typed), true, true
	case int16:
		return float64(typed), true, true
	case int32:
		return float64(typed), true, true
	case int64:
		return float64(typed), true, true
	case uint:
		return float64(typed), true, true
	case uint8:
		return float64(typed), true, true
	case uint16:
		return float64(typed), true, true
	case uint32:
		return float64(typed), true, true
	case uint64:
		return float64(typed), true, true
	case float32:
		number := float64(typed)
		return number, false, !math.IsNaN(number) && !math.IsInf(number, 0)
	case float64:
		return typed, false, !math.IsNaN(typed) && !math.IsInf(typed, 0)
	default:
		return 0, false, false
	}
}

func creativeParameterOptionContainsValue(options []dto.CreativeParamOption, value any) bool {
	needleKind, needleValue, ok := creativeParameterComparableValue(value)
	if !ok {
		return false
	}
	for _, option := range options {
		kind, comparableValue, ok := creativeParameterComparableValue(option.Value)
		if ok && kind == needleKind && comparableValue == needleValue {
			return true
		}
	}
	return false
}

func creativeParameterScalarValue(value any) bool {
	_, _, ok := creativeParameterComparableValue(value)
	return ok
}

func creativeParameterComparableValue(value any) (string, string, bool) {
	switch typed := value.(type) {
	case string:
		return "string", typed, true
	case bool:
		return "bool", fmt.Sprintf("%t", typed), true
	case int:
		return "number", fmt.Sprintf("%d", typed), true
	case int8:
		return "number", fmt.Sprintf("%d", typed), true
	case int16:
		return "number", fmt.Sprintf("%d", typed), true
	case int32:
		return "number", fmt.Sprintf("%d", typed), true
	case int64:
		return "number", fmt.Sprintf("%d", typed), true
	case uint:
		return "number", fmt.Sprintf("%d", typed), true
	case uint8:
		return "number", fmt.Sprintf("%d", typed), true
	case uint16:
		return "number", fmt.Sprintf("%d", typed), true
	case uint32:
		return "number", fmt.Sprintf("%d", typed), true
	case uint64:
		return "number", fmt.Sprintf("%d", typed), true
	case float32:
		number := float64(typed)
		if math.IsNaN(number) || math.IsInf(number, 0) {
			return "", "", false
		}
		return "number", fmt.Sprintf("%.9g", typed), true
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return "", "", false
		}
		return "number", fmt.Sprintf("%.17g", typed), true
	default:
		return "", "", false
	}
}

func mockCreativeImagePreviewBinding() dto.CreativeModelCatalogItem {
	recommendedScore := 10
	sortOrder := 1000
	return dto.CreativeModelCatalogItem{
		Id:                     "mock:gpt-image-2:preview",
		Object:                 "model",
		Created:                0,
		OwnedBy:                "new-api-creative",
		SupportedEndpointTypes: []constant.EndpointType{constant.EndpointTypeImageGeneration},
		ProviderModelId:        "gpt-image-2",
		PriceModelId:           "mock-gpt-image-2-price",
		Label:                  "GPT Image 2 · Mock Preview",
		DisplayName:            "GPT Image 2 · Mock Preview",
		ShortLabel:             "Mock GPT Image 2",
		ShortCode:              "mgpt2",
		Description:            "Mock-only Creative adapter preview binding. It is not routed to any provider.",
		Type:                   "image",
		Modality:               "image",
		Vendor:                 "new-api-creative",
		Tags:                   []string{"creative-adapter", "mock", "preview", "image"},
		RecommendedScore:       &recommendedScore,
		SortOrder:              &sortOrder,
		ParameterSchema: []dto.CreativeParameterSchemaItem{
			{
				Id:           "size",
				Label:        "Size",
				ShortLabel:   "Size",
				Description:  "Mock preview image size.",
				Type:         "enum",
				DefaultValue: "1024x1024",
				Options: []dto.CreativeParamOption{
					{Value: "1024x1024", Label: "1024×1024"},
					{Value: "16:9", Label: "16:9"},
				},
				Order: 10,
			},
			{
				Id:           "quality",
				Label:        "Quality",
				ShortLabel:   "Quality",
				Description:  "Mock preview quality.",
				Type:         "enum",
				DefaultValue: "auto",
				Options: []dto.CreativeParamOption{
					{Value: "auto", Label: "Auto"},
					{Value: "high", Label: "High"},
				},
				Order: 20,
			},
		},
	}
}
