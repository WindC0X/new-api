package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
)

const (
	CreativeAdapterEnabledOptionKey        = "creative.adapter.enabled"
	CreativeAdapterCanaryGroupsOptionKey   = "creative.adapter.canary_groups"
	CreativeModelBindingsOptionKey         = "creative.model_bindings"
	CreativeMockImageTasksEnabledOptionKey = "creative.mock_image_tasks.enabled"
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

var creativeAdapterAllowedPresets = creativeAdapterAllowedPresetSet()

var creativeAdapterAllowedParameterTemplates = creativeAdapterAllowedParameterTemplateSet()

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

type CreativeChannelSummary struct {
	Id     int      `json:"id"`
	Name   string   `json:"name"`
	Group  string   `json:"group"`
	Status int      `json:"status"`
	Models []string `json:"models"`
}

type CreativeChannelSummaryList struct {
	Items    []CreativeChannelSummary `json:"items"`
	Total    int64                    `json:"total"`
	Page     int                      `json:"page"`
	PageSize int                      `json:"page_size"`
}

type CreativeAdapterManifest struct {
	Id                   string   `json:"id"`
	Label                string   `json:"label"`
	Description          string   `json:"description"`
	Modality             string   `json:"modality"`
	ProviderFamily       string   `json:"providerFamily,omitempty"`
	TransportMode        string   `json:"transportMode"`
	Status               string   `json:"status"`
	DefaultTemplate      string   `json:"defaultTemplate"`
	AllowedTemplates     []string `json:"allowedTemplates"`
	CanBeEnabled         bool     `json:"canBeEnabled"`
	RequiresChannel      bool     `json:"requiresChannel"`
	SupportsProviderCall bool     `json:"supportsProviderCall"`
	BindingIdPrefix      string   `json:"bindingIdPrefix,omitempty"`
	BindingIdSuffix      string   `json:"bindingIdSuffix,omitempty"`
	Notes                []string `json:"notes,omitempty"`
}

type CreativeParameterTemplate struct {
	Id          string                            `json:"id"`
	Label       string                            `json:"label"`
	Description string                            `json:"description"`
	Modality    string                            `json:"modality"`
	Schema      []dto.CreativeParameterSchemaItem `json:"schema"`
}

type CreativeAdapterManifestAdminState struct {
	Manifests          []CreativeAdapterManifest   `json:"manifests"`
	ParameterTemplates []CreativeParameterTemplate `json:"parameterTemplates"`
}

type CreativeModelBindingDryRunItem struct {
	Id                   string         `json:"id"`
	ProviderModelId      string         `json:"providerModelId"`
	PriceModelId         string         `json:"priceModelId"`
	LockedChannelId      *int           `json:"lockedChannelId,omitempty"`
	FinalProviderModelId string         `json:"finalProviderModelId,omitempty"`
	Modality             string         `json:"modality"`
	Enabled              bool           `json:"enabled"`
	AdapterPreset        string         `json:"adapterPreset"`
	ParameterTemplate    string         `json:"parameterTemplate"`
	RequestPreview       map[string]any `json:"requestPreview"`
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
	ChannelId         int            `json:"channelId"`
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
	"upstream",
	"model",
	"modelid",
	"modelname",
	"modelref",
	"proxy",
	"organization",
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
	"storagebackend",
}

var creativeSensitiveStringForbiddenFragments = []string{
	"apikey",
	"authorization",
	"bearer",
	"token",
	"secret",
	"credential",
	"baseurl",
	"url",
	"endpoint",
	"host",
	"header",
	"channel",
	"provider",
	"upstream",
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

func creativeAdapterManifestsRegistry() []CreativeAdapterManifest {
	return []CreativeAdapterManifest{
		{
			Id:                   "mock_image_task",
			Label:                "Mock image task",
			Description:          "Local mock image task execution for Creative adapter preview and safe catalog validation.",
			Modality:             "image",
			ProviderFamily:       "new-api-creative",
			TransportMode:        "mock",
			Status:               "available",
			DefaultTemplate:      "mock_gpt_image",
			AllowedTemplates:     []string{"mock_gpt_image"},
			CanBeEnabled:         true,
			RequiresChannel:      false,
			SupportsProviderCall: false,
			BindingIdPrefix:      "mock",
			BindingIdSuffix:      "preview",
			Notes:                []string{"No upstream provider request is made."},
		},
		{
			Id:                   "grsai_gpt_image_dryrun",
			Label:                "GrsAI GPT image dry-run",
			Description:          "Offline GrsAI fixture request preview. It cannot be exposed to users as a live Creative model in Phase A.",
			Modality:             "image",
			ProviderFamily:       "grsai",
			TransportMode:        "dry_run",
			Status:               "available",
			DefaultTemplate:      "grsai_gpt_image",
			AllowedTemplates:     []string{"grsai_gpt_image"},
			CanBeEnabled:         false,
			RequiresChannel:      true,
			SupportsProviderCall: false,
			BindingIdPrefix:      "grsai",
			BindingIdSuffix:      "dryrun",
			Notes:                []string{"Dry-run/fixture only. Real provider transport is a follow-up task."},
		},
		{
			Id:                   "duomi_image_live",
			Label:                "Duomi image live",
			Description:          "Duomi live async image adapter. Provider connection details stay in Channels; users only see this logical Creative binding.",
			Modality:             "image",
			ProviderFamily:       "duomi",
			TransportMode:        "live",
			Status:               "available",
			DefaultTemplate:      "duomi_gpt_image",
			AllowedTemplates:     []string{"duomi_gpt_image"},
			CanBeEnabled:         true,
			RequiresChannel:      true,
			SupportsProviderCall: true,
			BindingIdPrefix:      "duomi",
			BindingIdSuffix:      "live",
			Notes:                []string{"Configure keys, base URL, and model support in Channels.", "Live smoke is not part of normal dry-run validation."},
		},
		{
			Id:                   "grsai_image_live",
			Label:                "GrsAI image live",
			Description:          "GrsAI live async image adapter for GPT image and nano-banana model families.",
			Modality:             "image",
			ProviderFamily:       "grsai",
			TransportMode:        "live",
			Status:               "available",
			DefaultTemplate:      "grsai_gpt_image",
			AllowedTemplates:     []string{"grsai_gpt_image", "grsai_gpt_image_vip", "grsai_nano_banana"},
			CanBeEnabled:         true,
			RequiresChannel:      true,
			SupportsProviderCall: true,
			BindingIdPrefix:      "grsai",
			BindingIdSuffix:      "live",
			Notes:                []string{"Configure keys, base URL, and model support in Channels.", "replyType is forced to async by the backend adapter."},
		},
	}
}

func creativeParameterTemplatesRegistry() []CreativeParameterTemplate {
	return []CreativeParameterTemplate{
		{
			Id:          "mock_gpt_image",
			Label:       "Mock GPT image",
			Description: "Safe mock image parameter schema.",
			Modality:    "image",
			Schema: []dto.CreativeParameterSchemaItem{
				{
					Id:           "size",
					Label:        "图片尺寸",
					ShortLabel:   "尺寸",
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
					Label:        "质量",
					ShortLabel:   "质量",
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
		},
		{
			Id:          "grsai_gpt_image",
			Label:       "GrsAI GPT image",
			Description: "GrsAI GPT image async parameter schema.",
			Modality:    "image",
			Schema: []dto.CreativeParameterSchemaItem{
				{
					Id:           "aspectRatio",
					Label:        "图片尺寸",
					ShortLabel:   "尺寸",
					Description:  "GrsAI documented aspectRatio value; square uses the documented 1024×1024 value.",
					Type:         "enum",
					DefaultValue: "1024x1024",
					Options: []dto.CreativeParamOption{
						{Value: "1024x1024", Label: "1024×1024 (1:1)"},
						{Value: "16:9", Label: "16:9"},
						{Value: "9:16", Label: "9:16"},
						{Value: "4:3", Label: "4:3"},
						{Value: "3:4", Label: "3:4"},
					},
					Order: 10,
				},
			},
		},
		{
			Id:          "grsai_gpt_image_vip",
			Label:       "GrsAI GPT image VIP",
			Description: "GrsAI GPT image VIP async pixel-size parameter schema.",
			Modality:    "image",
			Schema: []dto.CreativeParameterSchemaItem{
				{
					Id:           "aspectRatio",
					Label:        "尺寸",
					ShortLabel:   "尺寸",
					Description:  "GrsAI VIP pixel output size.",
					Type:         "enum",
					DefaultValue: "1K",
					Options: []dto.CreativeParamOption{
						{Value: "1K", Label: "1K"},
						{Value: "2K", Label: "2K"},
						{Value: "4K", Label: "4K"},
					},
					Order: 10,
				},
			},
		},
		{
			Id:          "duomi_gpt_image",
			Label:       "Duomi GPT image",
			Description: "Duomi GPT image async parameter schema.",
			Modality:    "image",
			Schema: []dto.CreativeParameterSchemaItem{
				{
					Id:           "size",
					Label:        "图片尺寸",
					ShortLabel:   "尺寸",
					Description:  "Image size or aspect ratio interpreted by the Duomi adapter.",
					Type:         "enum",
					DefaultValue: "1024x1024",
					Options: []dto.CreativeParamOption{
						{Value: "auto", Label: "Auto"},
						{Value: "1024x1024", Label: "1024×1024 (1:1)"},
						{Value: "16:9", Label: "16:9"},
						{Value: "9:16", Label: "9:16"},
						{Value: "4:3", Label: "4:3"},
						{Value: "3:4", Label: "3:4"},
						{Value: "3:2", Label: "3:2"},
						{Value: "2:3", Label: "2:3"},
					},
					Order: 10,
				},
				{
					Id:           "quality",
					Label:        "质量",
					ShortLabel:   "质量",
					Description:  "Duomi quality parameter.",
					Type:         "enum",
					DefaultValue: "medium",
					Options: []dto.CreativeParamOption{
						{Value: "low", Label: "Low"},
						{Value: "medium", Label: "Medium"},
						{Value: "high", Label: "High"},
					},
					Order: 20,
				},
			},
		},
		{
			Id:          "grsai_nano_banana",
			Label:       "GrsAI nano-banana",
			Description: "GrsAI nano-banana async parameter schema.",
			Modality:    "image",
			Schema: []dto.CreativeParameterSchemaItem{
				{
					Id:           "aspectRatio",
					Label:        "比例",
					ShortLabel:   "比例",
					Description:  "Nano-banana aspect ratio.",
					Type:         "enum",
					DefaultValue: "auto",
					Options: []dto.CreativeParamOption{
						{Value: "auto", Label: "Auto"},
						{Value: "1:1", Label: "1:1"},
						{Value: "16:9", Label: "16:9"},
						{Value: "9:16", Label: "9:16"},
						{Value: "4:3", Label: "4:3"},
						{Value: "3:4", Label: "3:4"},
						{Value: "3:2", Label: "3:2"},
						{Value: "2:3", Label: "2:3"},
						{Value: "5:4", Label: "5:4"},
						{Value: "4:5", Label: "4:5"},
						{Value: "21:9", Label: "21:9"},
						{Value: "1:4", Label: "1:4"},
						{Value: "4:1", Label: "4:1"},
						{Value: "1:8", Label: "1:8"},
						{Value: "8:1", Label: "8:1"},
					},
					Order: 10,
				},
				{
					Id:           "imageSize",
					Label:        "尺寸档位",
					ShortLabel:   "尺寸",
					Description:  "Nano-banana image size tier.",
					Type:         "enum",
					DefaultValue: "1K",
					Options:      []dto.CreativeParamOption{{Value: "1K", Label: "1K"}},
					Order:        20,
				},
			},
		},
	}
}

func creativeAdapterAllowedPresetSet() map[string]struct{} {
	result := make(map[string]struct{})
	for _, manifest := range creativeAdapterManifestsRegistry() {
		// Manifests are advertised to admin UI independently from storage
		// eligibility. Stored Phase-A bindings must remain offline-only.
		if !creativeAdapterManifestCanBeSaved(manifest) {
			continue
		}
		result[manifest.Id] = struct{}{}
	}
	return result
}

func creativeAdapterAllowedParameterTemplateSet() map[string]struct{} {
	result := make(map[string]struct{})
	for _, template := range creativeParameterTemplatesRegistry() {
		result[template.Id] = struct{}{}
	}
	return result
}

func CreativeAdapterManifestByID(id string) (CreativeAdapterManifest, bool) {
	id = strings.TrimSpace(id)
	for _, manifest := range creativeAdapterManifestsRegistry() {
		if manifest.Id == id {
			return manifest, true
		}
	}
	return CreativeAdapterManifest{}, false
}

func creativeAdapterManifestCanBeSaved(manifest CreativeAdapterManifest) bool {
	if manifest.Status != "available" {
		return false
	}
	switch manifest.TransportMode {
	case "mock", "dry_run":
		return true
	case "live":
		return CreativeImageLiveAdapterPreset(manifest.Id)
	default:
		return false
	}
}

func CreativeParameterTemplateByID(id string) (CreativeParameterTemplate, bool) {
	id = strings.TrimSpace(id)
	for _, template := range creativeParameterTemplatesRegistry() {
		if template.Id == id {
			return template, true
		}
	}
	return CreativeParameterTemplate{}, false
}

func GetCreativeAdapterManifestAdminState() (CreativeAdapterManifestAdminState, error) {
	manifests := creativeAdapterManifestsRegistry()
	templates := creativeParameterTemplatesRegistry()
	templateMap := make(map[string]CreativeParameterTemplate, len(templates))
	for _, template := range templates {
		if err := ValidateCreativeParameterSchema(template.Schema); err != nil {
			return CreativeAdapterManifestAdminState{}, fmt.Errorf("parameterTemplate %q invalid: %w", template.Id, err)
		}
		templateMap[template.Id] = template
	}
	for _, manifest := range manifests {
		if strings.TrimSpace(manifest.Id) == "" || strings.TrimSpace(manifest.DefaultTemplate) == "" {
			return CreativeAdapterManifestAdminState{}, errors.New("creative adapter manifest id and defaultTemplate are required")
		}
		defaultTemplate, ok := templateMap[manifest.DefaultTemplate]
		if !ok {
			return CreativeAdapterManifestAdminState{}, fmt.Errorf("adapter manifest %q defaultTemplate %q is not registered", manifest.Id, manifest.DefaultTemplate)
		}
		if defaultTemplate.Modality != manifest.Modality {
			return CreativeAdapterManifestAdminState{}, fmt.Errorf("adapter manifest %q defaultTemplate %q modality mismatch", manifest.Id, manifest.DefaultTemplate)
		}
		for _, templateID := range manifest.AllowedTemplates {
			template, ok := templateMap[templateID]
			if !ok {
				return CreativeAdapterManifestAdminState{}, fmt.Errorf("adapter manifest %q allowed template %q is not registered", manifest.Id, templateID)
			}
			if template.Modality != manifest.Modality {
				return CreativeAdapterManifestAdminState{}, fmt.Errorf("adapter manifest %q allowed template %q modality mismatch", manifest.Id, templateID)
			}
		}
	}
	return CreativeAdapterManifestAdminState{
		Manifests:          manifests,
		ParameterTemplates: templates,
	}, nil
}

// GetCreativePreviewModelBindingsForGroup returns Phase-A preview bindings only.
// It is fail-closed by default: both the global flag and a canary group match
// are required. These bindings are catalog/schema previews only and are not a
// provider routing contract.
func GetCreativePreviewModelBindingsForGroup(userGroup string) []dto.CreativeModelCatalogItem {
	if !creativeAdapterPreviewEnabled() || !creativeMockImageTasksEnabled() || !creativeAdapterCanaryGroupAllowed(userGroup) {
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
		if binding.AdapterPreset == CreativeImageAdapterPresetMock {
			if !creativeMockImageTasksEnabled() || binding.ParameterTemplate != "mock_gpt_image" {
				continue
			}
		} else if CreativeImageLiveAdapterPreset(binding.AdapterPreset) {
			if binding.ChannelId == nil || !creativeLiveBindingChannelReady(binding) {
				continue
			}
		} else {
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
	config, err := normalizeCreativeModelBindingsConfigForStoredRead(config)
	if err != nil {
		return CreativeModelBindingsConfig{}, err
	}
	return config, nil
}

func GetStoredCreativeModelBindingsConfig() (CreativeModelBindingsConfig, error) {
	return ParseCreativeModelBindingsConfig(creativeOptionValue(CreativeModelBindingsOptionKey))
}

func BuildCreativeModelBindingsAdminState(config CreativeModelBindingsConfig) (CreativeModelBindingsAdminState, error) {
	normalized, err := normalizeCreativeModelBindingsConfigForStoredRead(config)
	if err != nil {
		return CreativeModelBindingsAdminState{}, err
	}
	bytes, err := common.Marshal(normalized)
	if err != nil {
		return CreativeModelBindingsAdminState{}, err
	}
	return CreativeModelBindingsAdminState{Config: normalized, ConfigJSON: string(bytes)}, nil
}

func GetCreativeModelBindingsAdminState() (CreativeModelBindingsAdminState, error) {
	config, err := GetStoredCreativeModelBindingsConfig()
	if err != nil {
		return CreativeModelBindingsAdminState{}, err
	}
	return BuildCreativeModelBindingsAdminState(config)
}

func ListCreativeChannelSummaries(page int, pageSize int, keyword string, channelID int, group string) (CreativeChannelSummaryList, error) {
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = common.ItemsPerPage
	}
	if pageSize > 100 {
		pageSize = 100
	}
	keyword = strings.TrimSpace(keyword)
	group = model.NormalizeChannelGroupFilter(group)

	type channelSummaryRow struct {
		Id     int    `gorm:"column:id"`
		Name   string `gorm:"column:name"`
		Group  string `gorm:"column:group"`
		Status int    `gorm:"column:status"`
		Models string `gorm:"column:models"`
	}

	query := model.DB.Model(&model.Channel{}).
		Select([]string{"id", "name", "group", "status", "models"})
	if channelID > 0 {
		query = query.Where("id = ?", channelID)
	} else {
		query = model.ApplyChannelGroupFilter(query, group)
		if keyword != "" {
			like := "%" + keyword + "%"
			if parsedID, err := strconv.Atoi(keyword); err == nil && parsedID > 0 {
				query = query.Where("id = ? OR name LIKE ? OR models LIKE ?", parsedID, like, like)
			} else {
				query = query.Where("name LIKE ? OR models LIKE ?", like, like)
			}
		}
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return CreativeChannelSummaryList{}, err
	}

	var rows []channelSummaryRow
	if err := query.Order("id desc").
		Limit(pageSize).
		Offset((page - 1) * pageSize).
		Find(&rows).Error; err != nil {
		return CreativeChannelSummaryList{}, err
	}

	items := make([]CreativeChannelSummary, 0, len(rows))
	for _, row := range rows {
		items = append(items, CreativeChannelSummary{
			Id:     row.Id,
			Name:   row.Name,
			Group:  row.Group,
			Status: row.Status,
			Models: creativeChannelSummaryModels(row.Models),
		})
	}
	return CreativeChannelSummaryList{
		Items:    items,
		Total:    total,
		Page:     page,
		PageSize: pageSize,
	}, nil
}

func creativeChannelSummaryModels(raw string) []string {
	parts := strings.Split(strings.Trim(raw, ","), ",")
	models := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		modelID := strings.TrimSpace(part)
		if modelID == "" {
			continue
		}
		if _, ok := seen[modelID]; ok {
			continue
		}
		seen[modelID] = struct{}{}
		models = append(models, modelID)
	}
	return models
}

func ResolveCreativeImageModelBindingForGroup(bindingID string, userGroup string, userParams map[string]any) (CreativeResolvedModelBinding, error) {
	if !creativeAdapterPreviewEnabled() {
		return CreativeResolvedModelBinding{}, errors.New("creative adapter is disabled")
	}
	bindingID = strings.TrimSpace(bindingID)
	if bindingID == "" {
		return CreativeResolvedModelBinding{}, errors.New("creative image model is required")
	}
	builtInPreview := mockCreativeImagePreviewBinding()
	isBuiltInPreview := bindingID == builtInPreview.Id
	config, err := GetStoredCreativeModelBindingsConfig()
	if err != nil {
		if isBuiltInPreview {
			return resolveBuiltInCreativeImagePreviewBindingForGroup(builtInPreview, userGroup, userParams)
		}
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
		if binding.AdapterPreset == CreativeImageAdapterPresetMock {
			if !creativeMockImageTasksEnabled() {
				return CreativeResolvedModelBinding{}, errors.New("creative mock image task route is disabled")
			}
			if binding.ParameterTemplate != "mock_gpt_image" {
				return CreativeResolvedModelBinding{}, fmt.Errorf("creative binding %q is not available for mock image tasks", bindingID)
			}
		} else if CreativeImageLiveAdapterPreset(binding.AdapterPreset) {
			if binding.ChannelId == nil {
				return CreativeResolvedModelBinding{}, fmt.Errorf("creative image binding %q requires a locked channel", bindingID)
			}
			if !creativeLiveBindingChannelReady(binding) {
				return CreativeResolvedModelBinding{}, fmt.Errorf("creative image binding %q channel is not available", bindingID)
			}
		} else {
			return CreativeResolvedModelBinding{}, fmt.Errorf("creative binding %q adapter preset is not executable", bindingID)
		}
		if !creativeBindingCanaryGroupAllowed(binding, userGroup) {
			return CreativeResolvedModelBinding{}, fmt.Errorf("creative image binding %q is not enabled for this group", bindingID)
		}
		normalizedParams, err := ValidateCreativeUserParamsForSchema(binding.ParameterSchema, userParams)
		if err != nil {
			return CreativeResolvedModelBinding{}, err
		}
		channelID := 0
		if binding.ChannelId != nil {
			channelID = *binding.ChannelId
		}
		return CreativeResolvedModelBinding{
			Binding:           binding,
			BindingId:         binding.Id,
			ProviderModelId:   binding.ProviderModelId,
			PriceModelId:      binding.PriceModelId,
			AdapterPreset:     binding.AdapterPreset,
			ParameterTemplate: binding.ParameterTemplate,
			ChannelId:         channelID,
			UserParams:        normalizedParams,
		}, nil
	}
	if isBuiltInPreview {
		return resolveBuiltInCreativeImagePreviewBindingForGroup(builtInPreview, userGroup, userParams)
	}
	return CreativeResolvedModelBinding{}, fmt.Errorf("creative image binding %q was not found", bindingID)
}

func resolveBuiltInCreativeImagePreviewBindingForGroup(binding dto.CreativeModelCatalogItem, userGroup string, userParams map[string]any) (CreativeResolvedModelBinding, error) {
	if !creativeMockImageTasksEnabled() {
		return CreativeResolvedModelBinding{}, errors.New("creative mock image task route is disabled")
	}
	if !creativeAdapterCanaryGroupAllowed(userGroup) {
		return CreativeResolvedModelBinding{}, fmt.Errorf("creative image binding %q is not enabled for this group", binding.Id)
	}
	if err := ValidateCreativeParameterSchema(binding.ParameterSchema); err != nil {
		return CreativeResolvedModelBinding{}, fmt.Errorf("built-in creative image binding %q schema is invalid: %w", binding.Id, err)
	}
	normalizedParams, err := ValidateCreativeUserParamsForSchema(binding.ParameterSchema, userParams)
	if err != nil {
		return CreativeResolvedModelBinding{}, err
	}
	return CreativeResolvedModelBinding{
		Binding: CreativeModelBindingConfig{
			Id:                binding.Id,
			ProviderModelId:   binding.ProviderModelId,
			PriceModelId:      binding.PriceModelId,
			DisplayName:       binding.DisplayName,
			Modality:          "image",
			Enabled:           true,
			AdapterPreset:     "mock_image_task",
			ParameterTemplate: "mock_gpt_image",
			RecommendedScore:  binding.RecommendedScore,
			SortOrder:         binding.SortOrder,
			ParameterSchema:   binding.ParameterSchema,
		},
		BindingId:         binding.Id,
		ProviderModelId:   binding.ProviderModelId,
		PriceModelId:      binding.PriceModelId,
		AdapterPreset:     "mock_image_task",
		ParameterTemplate: "mock_gpt_image",
		ChannelId:         0,
		UserParams:        normalizedParams,
	}, nil
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
	tags := []string{"creative-adapter", binding.Modality}
	if CreativeImageLiveAdapterPreset(binding.AdapterPreset) {
		tags = append(tags, "live")
		if manifest, ok := CreativeAdapterManifestByID(binding.AdapterPreset); ok && manifest.ProviderFamily != "" {
			tags = append(tags, manifest.ProviderFamily)
		}
	} else if binding.AdapterPreset == CreativeImageAdapterPresetMock {
		tags = append(tags, "mock")
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
		Tags:                   tags,
		RecommendedScore:       binding.RecommendedScore,
		SortOrder:              binding.SortOrder,
		ParameterSchema:        creativeVisibleParameterSchema(binding.ParameterSchema),
		ParameterSchemaPresent: true,
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
	config, err := normalizeCreativeModelBindingsConfigForStoredRead(config)
	if err != nil {
		return CreativeModelBindingsConfig{}, err
	}
	if err := validateCreativeModelBindingsConfig(config, true); err != nil {
		return CreativeModelBindingsConfig{}, err
	}
	return config, nil
}

func normalizeCreativeModelBindingsConfigForStoredRead(config CreativeModelBindingsConfig) (CreativeModelBindingsConfig, error) {
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
	if err := validateCreativeModelBindingsConfig(config, false); err != nil {
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
		if manifest, ok := CreativeAdapterManifestByID(binding.AdapterPreset); ok {
			result.NoProviderCall = result.NoProviderCall && creativeAdapterManifestCanBeSaved(manifest)
		} else {
			result.NoProviderCall = false
		}
		preview := creativeModelBindingDryRunRequestPreview(binding)
		lockedChannelID, finalProviderModelID, channelModelID := creativeModelBindingDryRunChannelPreview(binding)
		if lockedChannelID != nil {
			preview["lockedChannelId"] = *lockedChannelID
			preview["finalProviderModelId"] = finalProviderModelID
			if channelModelID != "" && channelModelID != finalProviderModelID {
				preview["channelModelId"] = channelModelID
			}
		}
		result.Bindings = append(result.Bindings, CreativeModelBindingDryRunItem{
			Id:                   binding.Id,
			ProviderModelId:      binding.ProviderModelId,
			PriceModelId:         binding.PriceModelId,
			LockedChannelId:      lockedChannelID,
			FinalProviderModelId: finalProviderModelID,
			Modality:             binding.Modality,
			Enabled:              binding.Enabled,
			AdapterPreset:        binding.AdapterPreset,
			ParameterTemplate:    binding.ParameterTemplate,
			RequestPreview:       RedactCreativeDryRunValue(preview).(map[string]any),
		})
	}
	return result, nil
}

func creativeModelBindingDryRunRequestPreview(binding CreativeModelBindingConfig) map[string]any {
	if binding.AdapterPreset == CreativeImageAdapterPresetDuomiLive && binding.ParameterTemplate == "duomi_gpt_image" {
		return map[string]any{
			"transport":         "live",
			"adapterFamily":     "duomi",
			"operation":         "image_generate_async_preview",
			"offline":           true,
			"model":             binding.ProviderModelId,
			"priceModel":        binding.PriceModelId,
			"parameterTemplate": binding.ParameterTemplate,
			"requestBody": map[string]any{
				"model":   binding.ProviderModelId,
				"prompt":  "<user-prompt>",
				"size":    creativeDryRunSchemaDefault(binding.ParameterSchema, "size", "1024x1024"),
				"quality": creativeDryRunSchemaDefault(binding.ParameterSchema, "quality", "medium"),
			},
			"responseShape": map[string]any{
				"id":    "<provider-task-id>",
				"state": "running|succeeded|failed",
				"data":  map[string]any{"images": []any{map[string]any{"url": "[REDACTED]"}}},
			},
		}
	}
	if binding.AdapterPreset == CreativeImageAdapterPresetGrsAILive {
		requestBody := map[string]any{
			"model":       binding.ProviderModelId,
			"prompt":      "<user-prompt>",
			"aspectRatio": creativeDryRunSchemaDefault(binding.ParameterSchema, "aspectRatio", "1024x1024"),
			"replyType":   "async",
		}
		if binding.ParameterTemplate == "grsai_nano_banana" {
			requestBody["imageSize"] = creativeDryRunSchemaDefault(binding.ParameterSchema, "imageSize", "1K")
		}
		return map[string]any{
			"transport":         "live",
			"adapterFamily":     "grsai",
			"operation":         "image_generate_async_preview",
			"offline":           true,
			"model":             binding.ProviderModelId,
			"priceModel":        binding.PriceModelId,
			"parameterTemplate": binding.ParameterTemplate,
			"requestBody":       requestBody,
			"responseShape": map[string]any{
				"id":      "<provider-task-id>",
				"status":  "running|violation|succeeded|failed",
				"results": []any{map[string]any{"url": "[REDACTED]"}},
			},
		}
	}
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

func creativeModelBindingDryRunChannelPreview(binding CreativeModelBindingConfig) (*int, string, string) {
	finalProviderModelID := strings.TrimSpace(binding.ProviderModelId)
	if binding.ChannelId == nil {
		return nil, finalProviderModelID, ""
	}
	lockedChannelID := *binding.ChannelId
	channelModelID := finalProviderModelID
	channel, err := model.GetChannelById(lockedChannelID, false)
	if err == nil && channel != nil {
		if resolved := creativeDryRunSourceModelForFinalProviderModel(channel, finalProviderModelID); resolved != "" {
			channelModelID = resolved
		}
	}
	return &lockedChannelID, finalProviderModelID, channelModelID
}

func creativeDryRunSourceModelForFinalProviderModel(channel *model.Channel, finalProviderModelID string) string {
	finalProviderModelID = strings.TrimSpace(finalProviderModelID)
	if finalProviderModelID == "" || channel == nil {
		return ""
	}
	modelMapping := strings.TrimSpace(channel.GetModelMapping())
	mapped := make(map[string]string)
	if modelMapping != "" && modelMapping != "{}" {
		if err := common.Unmarshal([]byte(modelMapping), &mapped); err != nil {
			return ""
		}
	}
	for _, modelID := range channel.GetModels() {
		trimmedModelID := strings.TrimSpace(modelID)
		if trimmedModelID == "" {
			continue
		}
		if creativeChannelModelMappingResolvesTo(trimmedModelID, mapped, finalProviderModelID) {
			return trimmedModelID
		}
	}
	return ""
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
	manifest, ok := CreativeAdapterManifestByID(preset)
	if !ok || manifest.Status == "future" {
		return false
	}
	for _, allowed := range manifest.AllowedTemplates {
		if allowed == template {
			return true
		}
	}
	return false
}

func ParseCreativeGrsAIImageFixtureResponse(raw []byte) (CreativeGrsAIImageFixtureSummary, error) {
	type fixtureResponsePayload struct {
		Id       string            `json:"id"`
		Status   string            `json:"status"`
		Results  []json.RawMessage `json:"results"`
		Progress int               `json:"progress"`
		Error    string            `json:"error"`
	}
	var response struct {
		fixtureResponsePayload
		Data *fixtureResponsePayload `json:"data"`
	}
	if err := common.Unmarshal(raw, &response); err != nil {
		return CreativeGrsAIImageFixtureSummary{}, err
	}
	payload := response.fixtureResponsePayload
	if response.Data != nil {
		payload = *response.Data
	}
	id := strings.TrimSpace(payload.Id)
	status := strings.TrimSpace(strings.ToLower(payload.Status))
	if id == "" {
		return CreativeGrsAIImageFixtureSummary{}, errors.New("grsai image fixture response id is required")
	}
	if CreativeSensitiveStringValue(id) {
		return CreativeGrsAIImageFixtureSummary{}, errors.New("grsai image fixture response id contains sensitive material")
	}
	switch status {
	case "running", "violation", "succeeded", "failed":
	default:
		return CreativeGrsAIImageFixtureSummary{}, errors.New("grsai image fixture response status is unsupported")
	}
	if status == "succeeded" && len(payload.Results) == 0 {
		return CreativeGrsAIImageFixtureSummary{}, errors.New("grsai image fixture response has no result")
	}
	summary := CreativeGrsAIImageFixtureSummary{
		Id:          id,
		Status:      status,
		ResultCount: len(payload.Results),
		Progress:    payload.Progress,
	}
	if errorText := strings.TrimSpace(payload.Error); errorText != "" && !CreativeSensitiveStringValue(errorText) {
		summary.Error = errorText
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
			if CreativeForbiddenKey(key) && !creativeDryRunSafeDiagnosticKey(key) {
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

func creativeDryRunSafeDiagnosticKey(key string) bool {
	switch strings.TrimSpace(key) {
	case "model", "priceModel", "lockedChannelId", "finalProviderModelId", "channelModelId":
		return true
	default:
		return false
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

func creativeLiveBindingChannelReady(binding CreativeModelBindingConfig) bool {
	return creativeValidateLiveBindingChannel(binding) == nil
}

func creativeValidateLiveBindingChannel(binding CreativeModelBindingConfig) error {
	id := strings.TrimSpace(binding.Id)
	if binding.ChannelId == nil || *binding.ChannelId <= 0 {
		return fmt.Errorf("binding %q adapterPreset %q requires channelId", id, binding.AdapterPreset)
	}
	channel, err := model.GetChannelById(*binding.ChannelId, true)
	if err != nil {
		return fmt.Errorf("binding %q channelId %d was not found", id, *binding.ChannelId)
	}
	if channel == nil {
		return fmt.Errorf("binding %q channelId %d was not found", id, *binding.ChannelId)
	}
	if channel.Status != common.ChannelStatusEnabled {
		return fmt.Errorf("binding %q channelId %d is disabled", id, *binding.ChannelId)
	}
	if !creativeChannelSupportsProviderModel(channel, binding.ProviderModelId) {
		return fmt.Errorf("binding %q channelId %d does not support providerModelId %q", id, *binding.ChannelId, binding.ProviderModelId)
	}
	if channel.BaseURL == nil || strings.TrimSpace(*channel.BaseURL) == "" {
		return fmt.Errorf("binding %q channelId %d requires explicit baseURL for live adapter", id, *binding.ChannelId)
	}
	if !creativeChannelHasAvailableKey(channel) {
		return fmt.Errorf("binding %q channelId %d requires an available channel key for live adapter", id, *binding.ChannelId)
	}
	return nil
}

func creativeChannelHasAvailableKey(channel *model.Channel) bool {
	if channel == nil {
		return false
	}
	if !channel.ChannelInfo.IsMultiKey {
		return strings.TrimSpace(channel.Key) != ""
	}
	keys := channel.GetKeys()
	if len(keys) == 0 {
		return false
	}
	for index, key := range keys {
		if strings.TrimSpace(key) == "" {
			continue
		}
		status := common.ChannelStatusEnabled
		if channel.ChannelInfo.MultiKeyStatusList != nil {
			if configuredStatus, ok := channel.ChannelInfo.MultiKeyStatusList[index]; ok {
				status = configuredStatus
			}
		}
		if status == common.ChannelStatusEnabled {
			return true
		}
	}
	return false
}

func creativeProviderModelAllowedForTemplate(preset string, template string, providerModelID string) bool {
	providerModelID = strings.TrimSpace(providerModelID)
	switch preset {
	case CreativeImageAdapterPresetDuomiLive:
		return template == "duomi_gpt_image" && providerModelID == "gpt-image-2"
	case CreativeImageAdapterPresetGrsAILive:
		switch template {
		case "grsai_gpt_image":
			return providerModelID == "gpt-image-2"
		case "grsai_gpt_image_vip":
			return providerModelID == "gpt-image-2-vip"
		case "grsai_nano_banana":
			return creativeGrsAINanoBananaProviderModel(providerModelID)
		default:
			return false
		}
	default:
		return true
	}
}

func creativeGrsAINanoBananaProviderModel(modelID string) bool {
	switch strings.TrimSpace(modelID) {
	case "nano-banana",
		"nano-banana-fast",
		"nano-banana-2",
		"nano-banana-2-cl",
		"nano-banana-2-4k-cl",
		"nano-banana-pro",
		"nano-banana-pro-cl",
		"nano-banana-pro-vip",
		"nano-banana-pro-4k-vip":
		return true
	default:
		return false
	}
}

func ValidateCreativeModelBindingsConfig(config CreativeModelBindingsConfig) error {
	return validateCreativeModelBindingsConfig(config, true)
}

func validateCreativeModelBindingsConfig(config CreativeModelBindingsConfig, validateRuntimeChannel bool) error {
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
		manifest, ok := CreativeAdapterManifestByID(preset)
		if !ok {
			return fmt.Errorf("binding %q adapterPreset %q is not supported", id, binding.AdapterPreset)
		}
		if _, ok := creativeAdapterAllowedPresets[preset]; !ok {
			return fmt.Errorf("binding %q adapterPreset %q is not supported", id, binding.AdapterPreset)
		}
		if !creativeAdapterManifestCanBeSaved(manifest) {
			return fmt.Errorf("binding %q adapterPreset %q is not supported for offline binding save", id, preset)
		}
		if manifest.Modality != modality {
			return fmt.Errorf("binding %q adapterPreset %q does not support modality %q", id, preset, modality)
		}
		if binding.Enabled && !manifest.CanBeEnabled {
			return fmt.Errorf("binding %q adapterPreset %q cannot be enabled in this phase", id, preset)
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
		if !creativeProviderModelAllowedForTemplate(preset, template, binding.ProviderModelId) {
			return fmt.Errorf("binding %q providerModelId %q is not supported by adapterPreset %q with parameterTemplate %q", id, binding.ProviderModelId, preset, template)
		}
		if err := creativeValidateAdapterParameterSchemaIDs(binding); err != nil {
			return err
		}
		if binding.ChannelId != nil && *binding.ChannelId <= 0 {
			return fmt.Errorf("binding %q channelId must be positive", id)
		}
		if manifest.TransportMode == "live" && manifest.RequiresChannel && binding.ChannelId == nil {
			return fmt.Errorf("binding %q adapterPreset %q requires channelId", id, preset)
		}
		if CreativeImageLiveAdapterPreset(preset) && validateRuntimeChannel {
			if err := creativeValidateLiveBindingChannel(binding); err != nil {
				return err
			}
		} else if binding.ChannelId != nil && validateRuntimeChannel {
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
		if binding.Enabled && len(binding.CanaryGroups) == 0 {
			return fmt.Errorf("binding %q must set canaryGroups before enabling", id)
		}
		for groupIndex, group := range binding.CanaryGroups {
			trimmedGroup := strings.TrimSpace(group)
			if trimmedGroup == "" {
				return fmt.Errorf("binding %q canaryGroups contains an empty group", id)
			}
			if creativeCanaryGroupSensitiveValue(trimmedGroup) {
				return fmt.Errorf("binding %q canaryGroups[%d] contains sensitive material", id, groupIndex)
			}
			if trimmedGroup != "*" && CreativeForbiddenKey(trimmedGroup) {
				return fmt.Errorf("binding %q canaryGroups[%d] is forbidden", id, groupIndex)
			}
			if binding.Enabled && trimmedGroup != "*" && !creativeKnownCanaryGroup(trimmedGroup) {
				return fmt.Errorf("binding %q canaryGroups[%d] unknown group %q", id, groupIndex, trimmedGroup)
			}
		}
		if err := ValidateCreativeParameterSchema(binding.ParameterSchema); err != nil {
			return fmt.Errorf("binding %q parameterSchema invalid: %w", id, err)
		}
	}
	return nil
}

func creativeValidateAdapterParameterSchemaIDs(binding CreativeModelBindingConfig) error {
	if !CreativeImageLiveAdapterPreset(binding.AdapterPreset) {
		return nil
	}
	allowed := creativeAdapterSupportedParameterIDs(binding.AdapterPreset, binding.ParameterTemplate)
	if len(allowed) == 0 {
		return fmt.Errorf("binding %q adapterPreset %q with parameterTemplate %q has no supported parameter fields", binding.Id, binding.AdapterPreset, binding.ParameterTemplate)
	}
	if len(binding.ParameterSchema) == 0 {
		return fmt.Errorf("binding %q live parameterSchema is required", binding.Id)
	}
	visibleFieldCount := 0
	for _, item := range binding.ParameterSchema {
		id := strings.TrimSpace(item.Id)
		if id == "" {
			continue
		}
		if _, ok := allowed[id]; !ok {
			return fmt.Errorf("binding %q parameterSchema field %q is not supported by adapterPreset %q with parameterTemplate %q", binding.Id, id, binding.AdapterPreset, binding.ParameterTemplate)
		}
		if err := creativeValidateAdapterParameterSchemaValueSet(binding, item); err != nil {
			return err
		}
		if !item.Hidden {
			visibleFieldCount++
		}
	}
	if visibleFieldCount == 0 {
		return fmt.Errorf("binding %q live parameterSchema must expose at least one visible field", binding.Id)
	}
	return nil
}

func creativeValidateAdapterParameterSchemaValueSet(binding CreativeModelBindingConfig, item dto.CreativeParameterSchemaItem) error {
	if strings.TrimSpace(binding.AdapterPreset) != CreativeImageAdapterPresetGrsAILive ||
		strings.TrimSpace(binding.ParameterTemplate) != "grsai_gpt_image_vip" ||
		strings.TrimSpace(item.Id) != "aspectRatio" {
		return nil
	}
	allowedValues := map[string]struct{}{"1K": {}, "2K": {}, "4K": {}}
	if item.DefaultValue != nil {
		if _, ok := allowedValues[strings.TrimSpace(fmt.Sprint(item.DefaultValue))]; !ok {
			return fmt.Errorf("binding %q parameterSchema field %q defaultValue is not supported by adapterPreset %q with parameterTemplate %q", binding.Id, item.Id, binding.AdapterPreset, binding.ParameterTemplate)
		}
	}
	for _, option := range item.Options {
		if _, ok := allowedValues[strings.TrimSpace(fmt.Sprint(option.Value))]; !ok {
			return fmt.Errorf("binding %q parameterSchema field %q option %q is not supported by adapterPreset %q with parameterTemplate %q", binding.Id, item.Id, option.Value, binding.AdapterPreset, binding.ParameterTemplate)
		}
	}
	return nil
}

func creativeAdapterSupportedParameterIDs(preset string, template string) map[string]struct{} {
	switch strings.TrimSpace(preset) + "|" + strings.TrimSpace(template) {
	case CreativeImageAdapterPresetDuomiLive + "|duomi_gpt_image":
		return map[string]struct{}{"size": {}, "quality": {}}
	case CreativeImageAdapterPresetGrsAILive + "|grsai_gpt_image":
		return map[string]struct{}{"aspectRatio": {}}
	case CreativeImageAdapterPresetGrsAILive + "|grsai_gpt_image_vip":
		return map[string]struct{}{"aspectRatio": {}}
	case CreativeImageAdapterPresetGrsAILive + "|grsai_nano_banana":
		return map[string]struct{}{"aspectRatio": {}, "imageSize": {}}
	default:
		return nil
	}
}

func creativeKnownCanaryGroup(group string) bool {
	group = strings.TrimSpace(group)
	if group == "" {
		return false
	}
	usableGroups := setting.GetUserUsableGroupsCopy()
	_, ok := usableGroups[group]
	return ok
}

func creativeCanaryGroupSensitiveValue(value string) bool {
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
	return false
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

func CreativeAdapterPreviewEnabled() bool {
	return creativeAdapterPreviewEnabled()
}

func CreativeMockImageTasksEnabled() bool {
	return creativeAdapterPreviewEnabled() && creativeMockImageTasksEnabled()
}

func creativeMockImageTasksEnabled() bool {
	return strings.EqualFold(strings.TrimSpace(creativeOptionValue(CreativeMockImageTasksEnabledOptionKey)), "true")
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
	normalized := NormalizeCreativeForbiddenKey(trimmed)
	for _, fragment := range creativeSensitiveStringForbiddenFragments {
		if strings.Contains(normalized, fragment) {
			return true
		}
	}
	return false
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
	template, _ := CreativeParameterTemplateByID("mock_gpt_image")
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
		ParameterSchema:        template.Schema,
		ParameterSchemaPresent: true,
	}
}
