package service

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCreativePreviewBindingsAreFailClosedByDefault(t *testing.T) {
	withCreativeAdapterPreviewOptions(t, "", "")

	bindings := GetCreativePreviewModelBindingsForGroup("default")

	require.Empty(t, bindings)
}

func TestCreativePreviewBindingsRequireEnabledCanaryGroup(t *testing.T) {
	withCreativeAdapterPreviewOptions(t, "true", "vip, test")

	require.Empty(t, GetCreativePreviewModelBindingsForGroup("default"))
	bindings := GetCreativePreviewModelBindingsForGroup("test")

	require.Len(t, bindings, 1)
	binding := bindings[0]
	require.Equal(t, "mock:gpt-image-2:preview", binding.Id)
	require.Equal(t, "gpt-image-2", binding.ProviderModelId)
	require.Equal(t, "mock-gpt-image-2-price", binding.PriceModelId)
	require.NotEqual(t, binding.Id, binding.ProviderModelId)
	require.NotEqual(t, binding.ProviderModelId, binding.PriceModelId)
	require.NotEmpty(t, binding.ParameterSchema)
}

func TestValidateCreativeParameterSchemaRejectsUnsafeAndInvalidFields(t *testing.T) {
	tests := []struct {
		name   string
		schema []dto.CreativeParameterSchemaItem
	}{
		{
			name: "unsafe id",
			schema: []dto.CreativeParameterSchemaItem{{
				Id:    "callbackUrl",
				Label: "Callback",
				Type:  "string",
			}},
		},
		{
			name: "enum missing options",
			schema: []dto.CreativeParameterSchemaItem{{
				Id:    "size",
				Label: "Size",
				Type:  "enum",
			}},
		},
		{
			name: "enum default outside options",
			schema: []dto.CreativeParameterSchemaItem{{
				Id:           "size",
				Label:        "Size",
				Type:         "enum",
				DefaultValue: "bad",
				Options:      []dto.CreativeParamOption{{Value: "1024x1024", Label: "1024×1024"}},
			}},
		},
		{
			name: "enum default string does not match numeric option",
			schema: []dto.CreativeParameterSchemaItem{{
				Id:           "count",
				Label:        "Count",
				Type:         "enum",
				DefaultValue: "1",
				Options:      []dto.CreativeParamOption{{Value: 1, Label: "1"}},
			}},
		},
		{
			name: "enum option object rejected",
			schema: []dto.CreativeParameterSchemaItem{{
				Id:      "size",
				Label:   "Size",
				Type:    "enum",
				Options: []dto.CreativeParamOption{{Value: map[string]any{"bad": true}, Label: "bad"}},
			}},
		},
		{
			name: "enum option array rejected",
			schema: []dto.CreativeParameterSchemaItem{{
				Id:      "size",
				Label:   "Size",
				Type:    "enum",
				Options: []dto.CreativeParamOption{{Value: []any{"bad"}, Label: "bad"}},
			}},
		},
		{
			name: "default object rejected",
			schema: []dto.CreativeParameterSchemaItem{{
				Id:           "promptStrength",
				Label:        "Prompt Strength",
				Type:         "number",
				DefaultValue: map[string]any{"bad": true},
			}},
		},
		{
			name: "duplicate ids",
			schema: []dto.CreativeParameterSchemaItem{
				{Id: "size", Label: "Size", Type: "string"},
				{Id: "SIZE", Label: "Size", Type: "string"},
			},
		},
		{
			name: "string default rejects boolean",
			schema: []dto.CreativeParameterSchemaItem{{
				Id:           "style",
				Label:        "Style",
				Type:         "string",
				DefaultValue: true,
			}},
		},
		{
			name: "integer default rejects fractional number",
			schema: []dto.CreativeParameterSchemaItem{{
				Id:           "seed",
				Label:        "Seed",
				Type:         "integer",
				DefaultValue: 1.5,
			}},
		},
		{
			name: "number default rejects nan",
			schema: []dto.CreativeParameterSchemaItem{{
				Id:           "guidance",
				Label:        "Guidance",
				Type:         "number",
				DefaultValue: math.NaN(),
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Error(t, ValidateCreativeParameterSchema(tt.schema))
		})
	}
}

func TestValidateCreativeParameterSchemaAcceptsTypedValues(t *testing.T) {
	schema := []dto.CreativeParameterSchemaItem{
		{
			Id:           "size",
			Label:        "Size",
			Type:         "enum",
			DefaultValue: "1024x1024",
			Options:      []dto.CreativeParamOption{{Value: "1024x1024", Label: "1024×1024"}},
		},
		{Id: "seed", Label: "Seed", Type: "integer"},
		{Id: "enhance", Label: "Enhance", Type: "boolean", DefaultValue: true},
	}

	require.NoError(t, ValidateCreativeParameterSchema(schema))
}

func TestValidateCreativeUserParamsForSchemaIsTypedAndFailClosed(t *testing.T) {
	schema := []dto.CreativeParameterSchemaItem{
		{
			Id:      "size",
			Label:   "Size",
			Type:    "enum",
			Options: []dto.CreativeParamOption{{Value: "1024x1024", Label: "1024×1024"}},
		},
		{Id: "seed", Label: "Seed", Type: "integer", Min: common.GetPointer[float64](1), Max: common.GetPointer[float64](10)},
		{Id: "enhance", Label: "Enhance", Type: "boolean"},
		{Id: "internalCallback", Label: "Internal Callback", Type: "string", Hidden: true},
	}

	normalized, err := ValidateCreativeUserParamsForSchema(schema, map[string]any{
		"size":    "1024x1024",
		"seed":    float64(2),
		"enhance": true,
	})
	require.NoError(t, err)
	require.Equal(t, "1024x1024", normalized["size"])
	require.Equal(t, 2, normalized["seed"])
	require.Equal(t, true, normalized["enhance"])

	for _, tt := range []struct {
		name   string
		params map[string]any
	}{
		{name: "hidden field", params: map[string]any{"internalCallback": "server-only"}},
		{name: "forbidden field", params: map[string]any{"callback": "https://evil.example/cb"}},
		{name: "unsupported field", params: map[string]any{"style": "oil"}},
		{name: "wrong type", params: map[string]any{"enhance": "true"}},
		{name: "enum outside options", params: map[string]any{"size": "2048x2048"}},
		{name: "integer outside bounds", params: map[string]any{"seed": float64(11)}},
		{name: "sensitive value", params: map[string]any{"size": "https://provider.example/object?token=secret"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ValidateCreativeUserParamsForSchema(schema, tt.params)
			require.Error(t, err)
		})
	}
}

func TestValidateCreativeUserParamsForSchemaMaterializesDefaults(t *testing.T) {
	schema := []dto.CreativeParameterSchemaItem{
		{Id: "size", Label: "Size", Type: "enum", DefaultValue: "1:1", Options: []dto.CreativeParamOption{{Value: "1:1", Label: "1:1"}}},
		{Id: "quality", Label: "Quality", Type: "enum", DefaultValue: "auto", Options: []dto.CreativeParamOption{{Value: "auto", Label: "Auto"}, {Value: "low", Label: "Low"}}},
		{Id: "internal", Label: "Internal", Type: "string", Hidden: true, DefaultValue: "server-only"},
	}

	normalized, err := ValidateCreativeUserParamsForSchema(schema, nil)
	require.NoError(t, err)
	require.Equal(t, map[string]any{"size": "1:1", "quality": "auto"}, normalized)
	require.NotContains(t, normalized, "internal")
}

func TestResolveCreativeImageModelBindingForGroupIsMockOnlyAndGroupScoped(t *testing.T) {
	config := validCreativeModelBindingsConfigForTest()
	config.Bindings[0].Enabled = true
	config.Bindings[0].CanaryGroups = []string{"vip"}
	configJSON, err := NormalizeCreativeModelBindingsConfigJSON(config)
	require.NoError(t, err)
	withCreativeCapabilityOptions(t, map[string]string{
		CreativeAdapterEnabledOptionKey:        "true",
		CreativeMockImageTasksEnabledOptionKey: "true",
		CreativeModelBindingsOptionKey:         configJSON,
	})

	resolved, err := ResolveCreativeImageModelBindingForGroup("mock:gpt-image-2:preview", "vip", map[string]any{"size": "1024x1024"})
	require.NoError(t, err)
	require.Equal(t, "mock:gpt-image-2:preview", resolved.BindingId)
	require.Equal(t, "gpt-image-2", resolved.ProviderModelId)
	require.Equal(t, "mock-gpt-image-2-price", resolved.PriceModelId)
	require.Equal(t, map[string]any{"size": "1024x1024"}, resolved.UserParams)

	_, err = ResolveCreativeImageModelBindingForGroup("mock:gpt-image-2:preview", "default", map[string]any{"size": "1024x1024"})
	require.Error(t, err)

	withCreativeCapabilityOptions(t, map[string]string{
		CreativeAdapterEnabledOptionKey: "",
		CreativeModelBindingsOptionKey:  configJSON,
	})
	_, err = ResolveCreativeImageModelBindingForGroup("mock:gpt-image-2:preview", "vip", map[string]any{"size": "1024x1024"})
	require.Error(t, err)
}

func TestResolveCreativeImageModelBindingForGroupResolvesExposedBuiltInPreview(t *testing.T) {
	withCreativeCapabilityOptions(t, map[string]string{
		CreativeAdapterEnabledOptionKey:        "true",
		CreativeMockImageTasksEnabledOptionKey: "true",
		CreativeAdapterCanaryGroupsOptionKey:   "vip",
		CreativeModelBindingsOptionKey:         "",
	})
	require.Len(t, GetCreativePreviewModelBindingsForGroup("vip"), 1)

	resolved, err := ResolveCreativeImageModelBindingForGroup("mock:gpt-image-2:preview", "vip", map[string]any{
		"size":    "1024x1024",
		"quality": "auto",
	})

	require.NoError(t, err)
	require.Equal(t, "mock:gpt-image-2:preview", resolved.BindingId)
	require.Equal(t, "gpt-image-2", resolved.ProviderModelId)
	require.Equal(t, "mock-gpt-image-2-price", resolved.PriceModelId)
	require.Equal(t, "mock_image_task", resolved.AdapterPreset)
	require.Equal(t, "mock_gpt_image", resolved.ParameterTemplate)
	require.Equal(t, 0, resolved.ChannelId)
	require.Equal(t, map[string]any{"quality": "auto", "size": "1024x1024"}, resolved.UserParams)
	require.True(t, resolved.Binding.Enabled)
	require.Empty(t, resolved.Binding.CanaryGroups)
}

func TestResolveCreativeImageModelBindingForGroupFailsClosedForBuiltInPreviewGates(t *testing.T) {
	tests := []struct {
		name    string
		options map[string]string
		group   string
		wantErr string
	}{
		{
			name: "preview disabled",
			options: map[string]string{
				CreativeAdapterEnabledOptionKey:        "",
				CreativeMockImageTasksEnabledOptionKey: "true",
				CreativeAdapterCanaryGroupsOptionKey:   "vip",
				CreativeModelBindingsOptionKey:         "",
			},
			group:   "vip",
			wantErr: "creative adapter is disabled",
		},
		{
			name: "mock disabled",
			options: map[string]string{
				CreativeAdapterEnabledOptionKey:        "true",
				CreativeMockImageTasksEnabledOptionKey: "",
				CreativeAdapterCanaryGroupsOptionKey:   "vip",
				CreativeModelBindingsOptionKey:         "",
			},
			group:   "vip",
			wantErr: "mock image task route is disabled",
		},
		{
			name: "canary miss",
			options: map[string]string{
				CreativeAdapterEnabledOptionKey:        "true",
				CreativeMockImageTasksEnabledOptionKey: "true",
				CreativeAdapterCanaryGroupsOptionKey:   "vip",
				CreativeModelBindingsOptionKey:         "",
			},
			group:   "default",
			wantErr: "not enabled for this group",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withCreativeCapabilityOptions(t, tt.options)

			_, err := ResolveCreativeImageModelBindingForGroup("mock:gpt-image-2:preview", tt.group, map[string]any{"size": "1024x1024"})

			require.Error(t, err)
			require.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestStoredCreativeModelBindingsCatalogHonorsKillSwitchesAndHidesHiddenSchema(t *testing.T) {
	config := validCreativeModelBindingsConfigForTest()
	config.Bindings[0].Enabled = true
	config.Bindings[0].CanaryGroups = []string{"vip"}
	config.Bindings[0].ParameterSchema = append(config.Bindings[0].ParameterSchema, dto.CreativeParameterSchemaItem{
		Id:     "serverOnly",
		Label:  "Server Only",
		Type:   "string",
		Hidden: true,
	})
	configJSON, err := NormalizeCreativeModelBindingsConfigJSON(config)
	require.NoError(t, err)

	withCreativeCapabilityOptions(t, map[string]string{
		CreativeAdapterEnabledOptionKey:        "true",
		CreativeMockImageTasksEnabledOptionKey: "true",
		CreativeModelBindingsOptionKey:         configJSON,
	})
	items := GetStoredCreativeModelBindingsCatalogForGroup("vip")
	require.Len(t, items, 1)
	require.Equal(t, "mock:gpt-image-2:preview", items[0].Id)
	require.Equal(t, "gpt-image-2", items[0].ProviderModelId)
	require.Equal(t, "mock-gpt-image-2-price", items[0].PriceModelId)
	require.Len(t, items[0].ParameterSchema, 1)
	require.Equal(t, "size", items[0].ParameterSchema[0].Id)

	require.Empty(t, GetStoredCreativeModelBindingsCatalogForGroup("default"))

	config.Bindings[0].Enabled = false
	disabledJSON, err := NormalizeCreativeModelBindingsConfigJSON(config)
	require.NoError(t, err)
	withCreativeCapabilityOptions(t, map[string]string{
		CreativeAdapterEnabledOptionKey:        "true",
		CreativeMockImageTasksEnabledOptionKey: "true",
		CreativeModelBindingsOptionKey:         disabledJSON,
	})
	require.Empty(t, GetStoredCreativeModelBindingsCatalogForGroup("vip"))

	withCreativeCapabilityOptions(t, map[string]string{
		CreativeAdapterEnabledOptionKey: "",
		CreativeModelBindingsOptionKey:  configJSON,
	})
	require.Empty(t, GetStoredCreativeModelBindingsCatalogForGroup("vip"))
}

func TestStoredCreativeModelBindingsCatalogSerializesPresentEmptySchema(t *testing.T) {
	tests := []struct {
		name   string
		schema []dto.CreativeParameterSchemaItem
	}{
		{
			name:   "explicit empty schema",
			schema: []dto.CreativeParameterSchemaItem{},
		},
		{
			name: "all hidden schema",
			schema: []dto.CreativeParameterSchemaItem{{
				Id:     "serverOnly",
				Label:  "Server Only",
				Type:   "string",
				Hidden: true,
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := validCreativeModelBindingsConfigForTest()
			config.Bindings[0].Enabled = true
			config.Bindings[0].CanaryGroups = []string{"vip"}
			config.Bindings[0].ParameterSchema = tt.schema
			configJSON, err := NormalizeCreativeModelBindingsConfigJSON(config)
			require.NoError(t, err)

			withCreativeCapabilityOptions(t, map[string]string{
				CreativeAdapterEnabledOptionKey:        "true",
				CreativeMockImageTasksEnabledOptionKey: "true",
				CreativeModelBindingsOptionKey:         configJSON,
			})
			items := GetStoredCreativeModelBindingsCatalogForGroup("vip")
			require.Len(t, items, 1)
			require.Empty(t, items[0].ParameterSchema)

			encoded, err := common.Marshal(items[0])
			require.NoError(t, err)
			require.Contains(t, string(encoded), `"parameterSchema":[]`)
		})
	}
}

func TestStoredCreativeModelBindingsRequireExplicitMockImageTaskEnablement(t *testing.T) {
	config := validCreativeModelBindingsConfigForTest()
	config.Bindings[0].Enabled = true
	config.Bindings[0].CanaryGroups = []string{"vip"}
	configJSON, err := NormalizeCreativeModelBindingsConfigJSON(config)
	require.NoError(t, err)

	withCreativeCapabilityOptions(t, map[string]string{
		CreativeAdapterEnabledOptionKey:        "true",
		CreativeMockImageTasksEnabledOptionKey: "",
		CreativeModelBindingsOptionKey:         configJSON,
	})

	require.Empty(t, GetStoredCreativeModelBindingsCatalogForGroup("vip"))
	_, err = ResolveCreativeImageModelBindingForGroup("mock:gpt-image-2:preview", "vip", map[string]any{"size": "1024x1024"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "mock image task route is disabled")
}

func TestStoredCreativeModelBindingsCatalogHidesFixtureProviderBindings(t *testing.T) {
	config := grsAIGPTImageDryRunConfigForTest()
	config.Bindings[0].Enabled = true
	config.Bindings[0].CanaryGroups = []string{"vip"}
	_, err := NormalizeCreativeModelBindingsConfigJSON(config)
	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot be enabled")

	config.Bindings[0].Enabled = false
	configJSON, err := NormalizeCreativeModelBindingsConfigJSON(config)
	require.NoError(t, err)
	withCreativeCapabilityOptions(t, map[string]string{
		CreativeAdapterEnabledOptionKey:        "true",
		CreativeMockImageTasksEnabledOptionKey: "true",
		CreativeModelBindingsOptionKey:         configJSON,
	})

	require.Empty(t, GetStoredCreativeModelBindingsCatalogForGroup("vip"))
	_, err = ResolveCreativeImageModelBindingForGroup(config.Bindings[0].Id, "vip", map[string]any{"aspectRatio": "1024x1024"})
	require.Error(t, err)
}

func withCreativeAdapterPreviewOptions(t *testing.T, enabled string, canaryGroups string) {
	t.Helper()

	common.OptionMapRWMutex.Lock()
	originalMap := common.OptionMap
	copyMap := make(map[string]string, len(originalMap)+3)
	for key, value := range originalMap {
		copyMap[key] = value
	}
	if enabled == "" {
		delete(copyMap, CreativeAdapterEnabledOptionKey)
		delete(copyMap, CreativeMockImageTasksEnabledOptionKey)
	} else {
		copyMap[CreativeAdapterEnabledOptionKey] = enabled
		copyMap[CreativeMockImageTasksEnabledOptionKey] = enabled
	}
	if canaryGroups == "" {
		delete(copyMap, CreativeAdapterCanaryGroupsOptionKey)
	} else {
		copyMap[CreativeAdapterCanaryGroupsOptionKey] = canaryGroups
	}
	common.OptionMap = copyMap
	common.OptionMapRWMutex.Unlock()

	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		common.OptionMap = originalMap
		common.OptionMapRWMutex.Unlock()
	})
}

func withCreativeCapabilityOptions(t *testing.T, values map[string]string) {
	t.Helper()

	common.OptionMapRWMutex.Lock()
	originalMap := common.OptionMap
	copyMap := make(map[string]string, len(originalMap)+len(values))
	for key, value := range originalMap {
		copyMap[key] = value
	}
	for key, value := range values {
		if value == "" {
			delete(copyMap, key)
			continue
		}
		copyMap[key] = value
	}
	common.OptionMap = copyMap
	common.OptionMapRWMutex.Unlock()

	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		common.OptionMap = originalMap
		common.OptionMapRWMutex.Unlock()
	})
}

func TestCreativeForbiddenKeyNormalizerCoversControlVariants(t *testing.T) {
	for _, key := range []string{
		"notifyHook",
		"notify_hook",
		"notify-hook",
		"headers.Authorization",
		"callback_url",
		"owner-id",
		"X-Notify",
		"sourceProfileId",
		"idempotency:key",
		"base URL",
		"base\tURL",
		"headers/Authorization",
		"upstream",
		"upstreamOptions",
		"xUpstreamConfig",
		"model",
		"proxy",
		"organization",
		"storageBackend",
		"sourceUrl",
		"objectKey",
		"bucketUrl",
		"signedUrl",
		"presignedUrl",
		"accessKeyId",
		"secretAccessKey",
		"s3Endpoint",
	} {
		require.True(t, CreativeForbiddenKey(key), key)
	}
	require.Equal(t, "notifyhook", NormalizeCreativeForbiddenKey("notify_hook"))
	for _, key := range []string{"size", "aspectRatio", "quality", "n", "seed"} {
		require.False(t, CreativeForbiddenKey(key), key)
	}
}

func TestParseCreativeModelBindingsConfigValidatesVersionAndDistinctIDs(t *testing.T) {
	raw := `{
		"version": 1,
		"bindings": [{
			"id": "mock:gpt-image-2:preview",
			"providerModelId": "gpt-image-2",
			"priceModelId": "mock-gpt-image-2-price",
			"displayName": "Mock GPT Image 2",
			"modality": "image",
			"enabled": false,
			"canaryGroups": ["test"],
			"adapterPreset": "mock_image_task",
			"parameterTemplate": "mock_gpt_image",
			"recommendedScore": 10,
			"sortOrder": 100,
			"parameterSchema": [{
				"id": "size",
				"label": "Size",
				"type": "enum",
				"defaultValue": "1024x1024",
				"options": [{"value": "1024x1024", "label": "1024×1024"}]
			}]
		}]
	}`

	config, err := ParseCreativeModelBindingsConfig(raw)
	require.NoError(t, err)
	require.Equal(t, 1, config.Version)
	require.Len(t, config.Bindings, 1)
	require.Equal(t, "mock:gpt-image-2:preview", config.Bindings[0].Id)
	require.Equal(t, "gpt-image-2", config.Bindings[0].ProviderModelId)
	require.Equal(t, "mock-gpt-image-2-price", config.Bindings[0].PriceModelId)
	require.NotEqual(t, config.Bindings[0].Id, config.Bindings[0].ProviderModelId)
	require.NotEqual(t, config.Bindings[0].ProviderModelId, config.Bindings[0].PriceModelId)
}

func TestParseCreativeModelBindingsConfigAllowsGrsAIFixtureDryRunOnly(t *testing.T) {
	raw := `{
		"version": 1,
		"bindings": [{
			"id": "grsai:gpt-image-2:dryrun",
			"providerModelId": "gpt-image-2",
			"priceModelId": "grsai-gpt-image-2-price",
			"displayName": "GrsAI GPT Image 2 Dry Run",
			"modality": "image",
			"enabled": false,
			"canaryGroups": ["test"],
			"adapterPreset": "grsai_gpt_image_dryrun",
			"parameterTemplate": "grsai_gpt_image",
			"parameterSchema": [{
				"id": "aspectRatio",
				"label": "Aspect Ratio",
				"type": "enum",
				"defaultValue": "1024x1024",
				"options": [{"value": "1024x1024", "label": "1024×1024"}]
			}]
		}]
	}`

	config, err := ParseCreativeModelBindingsConfig(raw)
	require.NoError(t, err)
	require.Equal(t, "grsai_gpt_image_dryrun", config.Bindings[0].AdapterPreset)
	require.Equal(t, "grsai_gpt_image", config.Bindings[0].ParameterTemplate)
}

func TestCreativeAdapterManifestRegistryExposesSafeTemplates(t *testing.T) {
	state, err := GetCreativeAdapterManifestAdminState()
	require.NoError(t, err)
	require.NotEmpty(t, state.Manifests)
	require.NotEmpty(t, state.ParameterTemplates)

	var sawMock, sawDuomiLive, sawQualityLabel, sawDuomiAspect, sawDuomiImageSize, sawDuomiQualityLabel, sawNanoTemplate, sawGrsAISquareLabel, sawGrsAIImageSize, sawGrsAIQuality, sawGrsAIVIPQuality bool
	for _, manifest := range state.Manifests {
		require.NotContains(t, manifest.Description, "apiKey")
		require.NotContains(t, manifest.Description, "baseUrl")
		if manifest.Id == "mock_image_task" {
			sawMock = true
			require.True(t, manifest.CanBeEnabled)
			require.Equal(t, []string{"mock_gpt_image"}, manifest.AllowedTemplates)
		}
		if manifest.Id == "duomi_image_live" {
			sawDuomiLive = true
			require.True(t, manifest.CanBeEnabled)
			require.Equal(t, "available", manifest.Status)
			require.Equal(t, "live", manifest.TransportMode)
		}
	}
	for _, template := range state.ParameterTemplates {
		if template.Id == "grsai_nano_banana" {
			sawNanoTemplate = true
		}
		if template.Id == "grsai_gpt_image" {
			for _, item := range template.Schema {
				if item.Id == "aspectRatio" {
					require.Equal(t, "图片尺寸", item.Label)
					require.Equal(t, "尺寸", item.ShortLabel)
					for _, option := range item.Options {
						if option.Value == "1:1" && option.Label == "1:1 方形" {
							sawGrsAISquareLabel = true
						}
					}
				}
				if item.Id == "imageSize" {
					require.Equal(t, "图片分辨率", item.Label)
					require.Equal(t, []dto.CreativeParamOption{{Value: "1K", Label: "1K"}}, item.Options)
					sawGrsAIImageSize = true
				}
			}
		}
		if template.Id == "grsai_gpt_image" {
			ids := make([]string, 0, len(template.Schema))
			for _, item := range template.Schema {
				ids = append(ids, item.Id)
				if item.Id == "quality" {
					require.Equal(t, "质量", item.Label)
					require.Equal(t, "auto", item.DefaultValue)
					require.Equal(t, []dto.CreativeParamOption{{Value: "auto", Label: "自动"}, {Value: "low", Label: "快速"}, {Value: "medium", Label: "标准"}, {Value: "high", Label: "高清"}}, item.Options)
					sawGrsAIQuality = true
				}
			}
			require.Equal(t, []string{"aspectRatio", "imageSize", "quality"}, ids)
		}
		if template.Id == "grsai_gpt_image_vip" {
			ids := make([]string, 0, len(template.Schema))
			for _, item := range template.Schema {
				ids = append(ids, item.Id)
				if item.Id == "aspectRatio" {
					require.NotContains(t, fmtAnyForTest(item.Options), "1:3")
					require.NotContains(t, fmtAnyForTest(item.Options), "9:21")
				}
				if item.Id == "quality" {
					require.Equal(t, "质量", item.Label)
					require.Equal(t, "auto", item.DefaultValue)
					require.Equal(t, []dto.CreativeParamOption{{Value: "auto", Label: "自动"}, {Value: "low", Label: "快速"}, {Value: "medium", Label: "标准"}, {Value: "high", Label: "高清"}}, item.Options)
					sawGrsAIVIPQuality = true
				}
			}
			require.Equal(t, []string{"aspectRatio", "imageSize", "quality"}, ids)
		}
		if template.Id == "duomi_gpt_image" {
			ids := make([]string, 0, len(template.Schema))
			for _, item := range template.Schema {
				ids = append(ids, item.Id)
				if item.Id == "aspectRatio" {
					require.Equal(t, "图片尺寸", item.Label)
					require.NotContains(t, fmtAnyForTest(item.Options), "1024x1024")
					require.NotContains(t, fmtAnyForTest(item.Options), "1:2")
					require.NotContains(t, fmtAnyForTest(item.Options), "2:1")
					require.Contains(t, fmtAnyForTest(item.Options), "21:9")
					sawDuomiAspect = true
				}
				if item.Id == "imageSize" {
					require.Equal(t, "图片分辨率", item.Label)
					require.Equal(t, []dto.CreativeParamOption{{Value: "1K", Label: "1K"}}, item.Options)
					sawDuomiImageSize = true
				}
				if item.Id == "quality" && item.Label == "质量" && item.ShortLabel == "质量" {
					sawDuomiQualityLabel = true
				}
			}
			require.Equal(t, []string{"aspectRatio", "imageSize", "quality"}, ids)
		}
		for _, item := range template.Schema {
			if item.Id == "quality" && item.Label == "质量" {
				sawQualityLabel = true
			}
		}
	}
	require.True(t, sawMock)
	require.True(t, sawDuomiLive)
	require.True(t, sawDuomiAspect)
	require.True(t, sawDuomiImageSize)
	require.True(t, sawQualityLabel)
	require.True(t, sawDuomiQualityLabel)
	require.True(t, sawNanoTemplate)
	require.True(t, sawGrsAISquareLabel)
	require.True(t, sawGrsAIImageSize)
	require.True(t, sawGrsAIQuality)
	require.True(t, sawGrsAIVIPQuality)
}

func TestCreativeLiveBindingAcceptsDuomiCustomMappedAspectOptions(t *testing.T) {
	setupCreativeCapabilityServiceTestDB(t)
	baseURL := "https://duomi.example"
	channelID := 7110
	require.NoError(t, model.DB.Create(&model.Channel{
		Id:      channelID,
		Key:     "test-key",
		Status:  common.ChannelStatusEnabled,
		Name:    "creative-duomi-contract",
		Models:  "gpt-image-2",
		Group:   "default",
		BaseURL: &baseURL,
	}).Error)

	config := CreativeModelBindingsConfig{
		Version: 1,
		Bindings: []CreativeModelBindingConfig{{
			Id:                "duomi:gpt-image-2:live",
			ProviderModelId:   "gpt-image-2",
			PriceModelId:      "gpt-image-2",
			DisplayName:       "Duomi GPT Image 2",
			Modality:          "image",
			Enabled:           false,
			ChannelId:         &channelID,
			AdapterPreset:     CreativeImageAdapterPresetDuomiLive,
			ParameterTemplate: "duomi_gpt_image",
			ParameterSchema:   creativeDuomiGPTImageSchemaForTest("21:9", "auto"),
		}},
	}

	require.NoError(t, ValidateCreativeModelBindingsConfig(config))

	config.Bindings[0].ParameterSchema[0].Options = append(config.Bindings[0].ParameterSchema[0].Options, dto.CreativeParamOption{Value: "9:21", Label: "9:21 超高"})
	err := ValidateCreativeModelBindingsConfig(config)
	require.Error(t, err)
	require.Contains(t, err.Error(), "9:21")
}

func TestValidateCreativeModelBindingsConfigRejectsEnabledDryRunAndInvalidLive(t *testing.T) {
	setupCreativeCapabilityServiceTestDB(t)

	config := grsAIGPTImageDryRunConfigForTest()
	config.Bindings[0].Enabled = true
	err := ValidateCreativeModelBindingsConfig(config)
	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot be enabled")

	config = validCreativeModelBindingsConfigForTest()
	config.Bindings[0].AdapterPreset = "duomi_image_live"
	config.Bindings[0].ParameterTemplate = "duomi_gpt_image"
	config.Bindings[0].ParameterSchema = creativeDuomiGPTImageSchemaForTest("1:1", "auto")
	config.Bindings[0].Enabled = false
	err = ValidateCreativeModelBindingsConfig(config)
	require.Error(t, err)
	require.Contains(t, err.Error(), "requires channelId")
}

func validCreativeModelBindingsConfigForTest() CreativeModelBindingsConfig {
	return CreativeModelBindingsConfig{
		Version: 1,
		Bindings: []CreativeModelBindingConfig{{
			Id:                "mock:gpt-image-2:preview",
			ProviderModelId:   "gpt-image-2",
			PriceModelId:      "mock-gpt-image-2-price",
			DisplayName:       "Mock GPT Image 2",
			Modality:          "image",
			Enabled:           false,
			CanaryGroups:      []string{"test"},
			AdapterPreset:     "mock_image_task",
			ParameterTemplate: "mock_gpt_image",
			ParameterSchema: []dto.CreativeParameterSchemaItem{{
				Id:           "size",
				Label:        "Size",
				Type:         "enum",
				DefaultValue: "1024x1024",
				Options:      []dto.CreativeParamOption{{Value: "1024x1024", Label: "1024×1024"}},
			}},
		}},
	}
}

func grsAIGPTImageDryRunConfigForTest() CreativeModelBindingsConfig {
	return CreativeModelBindingsConfig{
		Version: 1,
		Bindings: []CreativeModelBindingConfig{{
			Id:                "grsai:gpt-image-2:dryrun",
			ProviderModelId:   "gpt-image-2",
			PriceModelId:      "grsai-gpt-image-2-price",
			DisplayName:       "GrsAI GPT Image 2 Dry Run",
			Modality:          "image",
			Enabled:           false,
			CanaryGroups:      []string{"test"},
			AdapterPreset:     "grsai_gpt_image_dryrun",
			ParameterTemplate: "grsai_gpt_image",
			ParameterSchema: []dto.CreativeParameterSchemaItem{{
				Id:           "aspectRatio",
				Label:        "Aspect Ratio",
				Type:         "enum",
				DefaultValue: "1024x1024",
				Options: []dto.CreativeParamOption{
					{Value: "1024x1024", Label: "1024×1024"},
					{Value: "1536x1024", Label: "1536×1024"},
					{Value: "1024x1536", Label: "1024×1536"},
				},
			}},
		}},
	}
}

func creativeFakeSecretCorpusForTest() []string {
	return []string{
		"Bearer sk-test-secret",
		"sk-test-secret",
		"https://provider.example/v1/images?X-Amz-Signature=abc&X-Amz-Credential=credential",
		"https://oss.example/object?Expires=999999&Signature=abc",
		"data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAAB",
		"api_key=secret",
		"access_key=secret",
		"cookie=session-secret",
		"csrf=csrf-secret",
		"nonce=nonce-secret",
		"object_key=private/object.png",
		"token=secret",
	}
}

func TestParseCreativeModelBindingsConfigRejectsUnsafeConfig(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{
			name: "unsupported version",
			raw:  `{"version":2,"bindings":[]}`,
		},
		{
			name: "duplicate binding id",
			raw:  `{"version":1,"bindings":[{"id":"mock:image:a","providerModelId":"p","priceModelId":"price","modality":"image","adapterPreset":"mock_image_task","parameterTemplate":"mock_gpt_image"},{"id":"MOCK:IMAGE:A","providerModelId":"p","priceModelId":"price","modality":"image","adapterPreset":"mock_image_task","parameterTemplate":"mock_gpt_image"}]}`,
		},
		{
			name: "forbidden binding id",
			raw:  `{"version":1,"bindings":[{"id":"callback:image","providerModelId":"p","priceModelId":"price","modality":"image","adapterPreset":"mock_image_task","parameterTemplate":"mock_gpt_image"}]}`,
		},
		{
			name: "missing provider model",
			raw:  `{"version":1,"bindings":[{"id":"mock:image:a","priceModelId":"price","modality":"image","adapterPreset":"mock_image_task","parameterTemplate":"mock_gpt_image"}]}`,
		},
		{
			name: "forbidden schema id",
			raw:  `{"version":1,"bindings":[{"id":"mock:image:a","providerModelId":"p","priceModelId":"price","modality":"image","adapterPreset":"mock_image_task","parameterTemplate":"mock_gpt_image","parameterSchema":[{"id":"notifyHook","label":"Hook","type":"string"}]}]}`,
		},
		{
			name: "forbidden raw admin key",
			raw:  `{"version":1,"bindings":[{"id":"mock:image:a","providerModelId":"p","priceModelId":"price","modality":"image","adapterPreset":"mock_image_task","parameterTemplate":"mock_gpt_image","baseURL":"https://provider.example"}]}`,
		},
		{
			name: "unsupported raw schema key",
			raw:  `{"version":1,"bindings":[{"id":"mock:image:a","providerModelId":"p","priceModelId":"price","modality":"image","adapterPreset":"mock_image_task","parameterTemplate":"mock_gpt_image","parameterSchema":[{"id":"size","label":"Size","type":"string","headers":{"Authorization":"Bearer sk-test"}}]}]}`,
		},
		{
			name: "sensitive provider model value",
			raw:  `{"version":1,"bindings":[{"id":"mock:image:a","providerModelId":"https://provider.example/model?X-Amz-Signature=secret","priceModelId":"price","modality":"image","adapterPreset":"mock_image_task","parameterTemplate":"mock_gpt_image"}]}`,
		},
		{
			name: "sensitive price model value",
			raw:  `{"version":1,"bindings":[{"id":"mock:image:a","providerModelId":"p","priceModelId":"Bearer sk-test-secret","modality":"image","adapterPreset":"mock_image_task","parameterTemplate":"mock_gpt_image"}]}`,
		},
		{
			name: "null root",
			raw:  `null`,
		},
		{
			name: "null bindings",
			raw:  `{"version":1,"bindings":null}`,
		},
		{
			name: "null parameter schema",
			raw:  `{"version":1,"bindings":[{"id":"mock:image:a","providerModelId":"p","priceModelId":"price","modality":"image","adapterPreset":"mock_image_task","parameterTemplate":"mock_gpt_image","parameterSchema":null}]}`,
		},
		{
			name: "null options",
			raw:  `{"version":1,"bindings":[{"id":"mock:image:a","providerModelId":"p","priceModelId":"price","modality":"image","adapterPreset":"mock_image_task","parameterTemplate":"mock_gpt_image","parameterSchema":[{"id":"size","label":"Size","type":"enum","options":null}]}]}`,
		},
		{
			name: "duomi preset blocked",
			raw:  `{"version":1,"bindings":[{"id":"duomi:image:a","providerModelId":"gpt-image-2","priceModelId":"price","modality":"image","adapterPreset":"duomi_gpt_image","parameterTemplate":"mock_gpt_image"}]}`,
		},
		{
			name: "grsai template blocked",
			raw:  `{"version":1,"bindings":[{"id":"grsai:image:a","providerModelId":"gpt-image-2","priceModelId":"price","modality":"image","adapterPreset":"mock_image_task","parameterTemplate":"grsai_gpt_image"}]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseCreativeModelBindingsConfig(tt.raw)
			require.Error(t, err)
		})
	}
}

func TestCreativeModelBindingsRejectFakeSecretCorpusBeforeDiagnostics(t *testing.T) {
	for _, secret := range creativeFakeSecretCorpusForTest() {
		t.Run("provider "+secret, func(t *testing.T) {
			config := validCreativeModelBindingsConfigForTest()
			config.Bindings[0].ProviderModelId = secret
			state, err := BuildCreativeModelBindingsAdminState(config)
			require.Error(t, err)
			require.Empty(t, state.ConfigJSON)
			require.NotContains(t, err.Error(), secret)
		})
		t.Run("schema default "+secret, func(t *testing.T) {
			config := validCreativeModelBindingsConfigForTest()
			config.Bindings[0].ParameterSchema = []dto.CreativeParameterSchemaItem{{
				Id:           "style",
				Label:        "Style",
				Type:         "string",
				DefaultValue: secret,
			}}
			state, err := BuildCreativeModelBindingsAdminState(config)
			require.Error(t, err)
			require.Empty(t, state.ConfigJSON)
			require.NotContains(t, err.Error(), secret)
		})
		t.Run("schema option "+secret, func(t *testing.T) {
			config := validCreativeModelBindingsConfigForTest()
			config.Bindings[0].ParameterSchema = []dto.CreativeParameterSchemaItem{{
				Id:           "style",
				Label:        "Style",
				Type:         "enum",
				DefaultValue: "safe",
				Options: []dto.CreativeParamOption{
					{Value: "safe", Label: "Safe"},
					{Value: secret, Label: "Unsafe"},
				},
			}}
			_, err := BuildCreativeModelBindingsDryRun(config)
			require.Error(t, err)
			require.NotContains(t, err.Error(), secret)
		})
	}
}

func TestNormalizeCreativeModelBindingsConfigTrimsAndPersistsCanonicalValues(t *testing.T) {
	raw := `{
		"version": 1,
		"bindings": [{
			"id": " mock:gpt-image-2:preview ",
			"providerModelId": " gpt-image-2 ",
			"priceModelId": " mock-gpt-image-2-price ",
			"displayName": " Mock GPT Image 2 ",
			"modality": " IMAGE ",
			"enabled": false,
			"canaryGroups": [" test "],
			"adapterPreset": " mock_image_task ",
			"parameterTemplate": " mock_gpt_image ",
			"parameterSchema": [{
				"id": " size ",
				"label": " Size ",
				"type": " ENUM ",
				"defaultValue": "1024x1024",
				"options": [{"value": "1024x1024", "label": " 1024×1024 "}]
			}]
		}]
	}`

	config, err := ParseCreativeModelBindingsConfig(raw)
	require.NoError(t, err)
	binding := config.Bindings[0]
	require.Equal(t, "mock:gpt-image-2:preview", binding.Id)
	require.Equal(t, "gpt-image-2", binding.ProviderModelId)
	require.Equal(t, "mock-gpt-image-2-price", binding.PriceModelId)
	require.Equal(t, "Mock GPT Image 2", binding.DisplayName)
	require.Equal(t, "image", binding.Modality)
	require.Equal(t, []string{"test"}, binding.CanaryGroups)
	require.Equal(t, "mock_image_task", binding.AdapterPreset)
	require.Equal(t, "mock_gpt_image", binding.ParameterTemplate)
	require.Equal(t, "size", binding.ParameterSchema[0].Id)
	require.Equal(t, "Size", binding.ParameterSchema[0].Label)
	require.Equal(t, "enum", binding.ParameterSchema[0].Type)
	require.Equal(t, "1024×1024", binding.ParameterSchema[0].Options[0].Label)

	normalizedJSON, err := NormalizeCreativeModelBindingsConfigJSON(config)
	require.NoError(t, err)
	require.NotContains(t, normalizedJSON, `" IMAGE "`)
	require.NotContains(t, normalizedJSON, `" size "`)
}

func TestValidateCreativeModelBindingsConfigRejectsUnsupportedRoutingFields(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*CreativeModelBindingConfig)
	}{
		{
			name: "unknown preset",
			mutate: func(binding *CreativeModelBindingConfig) {
				binding.AdapterPreset = "duomi_live_call"
			},
		},
		{
			name: "unknown parameter template",
			mutate: func(binding *CreativeModelBindingConfig) {
				binding.ParameterTemplate = "grsai_live_template"
			},
		},
		{
			name: "wrong modality",
			mutate: func(binding *CreativeModelBindingConfig) {
				binding.Modality = "video"
			},
		},
		{
			name: "invalid channel",
			mutate: func(binding *CreativeModelBindingConfig) {
				channelId := 0
				binding.ChannelId = &channelId
			},
		},
		{
			name: "forbidden canary group",
			mutate: func(binding *CreativeModelBindingConfig) {
				binding.CanaryGroups = []string{"ownerId"}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := validCreativeModelBindingsConfigForTest()
			tt.mutate(&config.Bindings[0])
			require.Error(t, ValidateCreativeModelBindingsConfig(config))
		})
	}
}

func TestValidateCreativeModelBindingsConfigCanaryGroupsFailClosedForEnabledBindings(t *testing.T) {
	setupCreativeCapabilityServiceTestDB(t)

	config := validCreativeModelBindingsConfigForTest()
	config.Bindings[0].Enabled = true
	config.Bindings[0].CanaryGroups = []string{"beta"}
	err := ValidateCreativeModelBindingsConfig(config)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown group")

	config = validCreativeModelBindingsConfigForTest()
	config.Bindings[0].Enabled = true
	config.Bindings[0].CanaryGroups = []string{"vip"}
	require.NoError(t, ValidateCreativeModelBindingsConfig(config))

	config = validCreativeModelBindingsConfigForTest()
	config.Bindings[0].Enabled = true
	config.Bindings[0].CanaryGroups = []string{"*"}
	require.NoError(t, ValidateCreativeModelBindingsConfig(config))

	config = validCreativeModelBindingsConfigForTest()
	config.Bindings[0].Enabled = false
	config.Bindings[0].CanaryGroups = []string{"futureprivate"}
	require.NoError(t, ValidateCreativeModelBindingsConfig(config))
}

func TestValidateCreativeModelBindingsConfigUsesCurrentUserUsableGroups(t *testing.T) {
	setupCreativeCapabilityServiceTestDB(t)
	restore := setting.UserUsableGroups2JSONString()
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"default":"默认分组","vip":"vip分组","beta":"Beta 分组"}`))
	t.Cleanup(func() {
		require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(restore))
	})

	config := validCreativeModelBindingsConfigForTest()
	config.Bindings[0].Enabled = true
	config.Bindings[0].CanaryGroups = []string{"beta"}
	require.NoError(t, ValidateCreativeModelBindingsConfig(config))
}

func TestValidateCreativeModelBindingsConfigRedactsSensitiveCanaryGroupErrors(t *testing.T) {
	config := validCreativeModelBindingsConfigForTest()
	rawGroup := "https://provider.example/private/group?token=secret"
	config.Bindings[0].CanaryGroups = []string{rawGroup}

	err := ValidateCreativeModelBindingsConfig(config)
	require.Error(t, err)
	require.Contains(t, err.Error(), "canaryGroups[0]")
	require.NotContains(t, err.Error(), rawGroup)
	require.NotContains(t, err.Error(), "provider.example")
	require.NotContains(t, err.Error(), "token=secret")
}

func TestValidateCreativeModelBindingsConfigRejectsMissingOrDisabledChannel(t *testing.T) {
	setupCreativeCapabilityServiceTestDB(t)
	config := validCreativeModelBindingsConfigForTest()
	missingChannelID := 404
	config.Bindings[0].ChannelId = &missingChannelID
	require.Error(t, ValidateCreativeModelBindingsConfig(config))

	require.NoError(t, model.DB.Create(&model.Channel{
		Id:     12,
		Type:   1,
		Key:    "redacted",
		Status: common.ChannelStatusManuallyDisabled,
		Name:   "disabled creative channel",
		Models: "gpt-image-2",
	}).Error)
	config = validCreativeModelBindingsConfigForTest()
	disabledChannelID := 12
	config.Bindings[0].ChannelId = &disabledChannelID
	require.Error(t, ValidateCreativeModelBindingsConfig(config))

	require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", 12).Update("status", common.ChannelStatusEnabled).Error)
	require.NoError(t, ValidateCreativeModelBindingsConfig(config))
}

func TestValidateCreativeModelBindingsConfigRequiresChannelProviderModelSupport(t *testing.T) {
	setupCreativeCapabilityServiceTestDB(t)
	require.NoError(t, model.DB.Create(&model.Channel{
		Id:     21,
		Type:   1,
		Key:    "redacted",
		Status: common.ChannelStatusEnabled,
		Name:   "wrong model creative channel",
		Models: "other-model",
	}).Error)
	config := validCreativeModelBindingsConfigForTest()
	channelID := 21
	config.Bindings[0].ChannelId = &channelID
	require.Error(t, ValidateCreativeModelBindingsConfig(config))

	mappingToOther := `{"gpt-image-2":"other-upstream-model"}`
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", 21).Updates(map[string]any{
		"model_mapping": &mappingToOther,
	}).Error)
	require.Error(t, ValidateCreativeModelBindingsConfig(config))

	mapping := `{"logical-image":"gpt-image-2"}`
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", 21).Updates(map[string]any{
		"models":        "logical-image",
		"model_mapping": &mapping,
	}).Error)
	require.NoError(t, ValidateCreativeModelBindingsConfig(config))

	chainMapping := `{"logical-image":"provider-alias","provider-alias":"gpt-image-2"}`
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", 21).Updates(map[string]any{
		"model_mapping": &chainMapping,
	}).Error)
	require.NoError(t, ValidateCreativeModelBindingsConfig(config))

	cyclicMapping := `{"logical-image":"provider-alias","provider-alias":"logical-image"}`
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", 21).Updates(map[string]any{
		"model_mapping": &cyclicMapping,
	}).Error)
	require.Error(t, ValidateCreativeModelBindingsConfig(config))

	mappingDirectToOther := `{"gpt-image-2":"other-upstream-model"}`
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", 21).Updates(map[string]any{
		"models":        "gpt-image-2",
		"model_mapping": &mappingDirectToOther,
	}).Error)
	require.Error(t, ValidateCreativeModelBindingsConfig(config))

	mappingDirectIdentity := `{"gpt-image-2":"gpt-image-2"}`
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", 21).Updates(map[string]any{
		"model_mapping": &mappingDirectIdentity,
	}).Error)
	require.NoError(t, ValidateCreativeModelBindingsConfig(config))
}

func TestBuildCreativeModelBindingsDryRunExposesLockedChannelPreview(t *testing.T) {
	setupCreativeCapabilityServiceTestDB(t)
	mapping := `{"logical-image":"gpt-image-2"}`
	require.NoError(t, model.DB.Create(&model.Channel{
		Id:           41,
		Type:         1,
		Key:          "redacted",
		Status:       common.ChannelStatusEnabled,
		Name:         "mapped creative channel",
		Models:       "logical-image",
		ModelMapping: &mapping,
	}).Error)

	config := validCreativeModelBindingsConfigForTest()
	channelID := 41
	config.Bindings[0].ChannelId = &channelID

	result, err := BuildCreativeModelBindingsDryRun(config)
	require.NoError(t, err)
	require.True(t, result.NoProviderCall)
	require.Len(t, result.Bindings, 1)
	require.Equal(t, &channelID, result.Bindings[0].LockedChannelId)
	require.Equal(t, "gpt-image-2", result.Bindings[0].FinalProviderModelId)
	require.Equal(t, channelID, result.Bindings[0].RequestPreview["lockedChannelId"])
	require.Equal(t, "gpt-image-2", result.Bindings[0].RequestPreview["finalProviderModelId"])
	require.Equal(t, "logical-image", result.Bindings[0].RequestPreview["channelModelId"])
	require.NotContains(t, fmtAnyForTest(result), "redacted")
}

func TestValidateCreativeModelBindingsConfigRejectsEnabledBindingIDCollidingWithChannelModel(t *testing.T) {
	setupCreativeCapabilityServiceTestDB(t)
	require.NoError(t, model.DB.Create(&model.Ability{
		Group:     "test",
		Model:     "mock:gpt-image-2:preview",
		ChannelId: 31,
		Enabled:   true,
	}).Error)

	config := validCreativeModelBindingsConfigForTest()
	config.Bindings[0].Enabled = true
	err := ValidateCreativeModelBindingsConfig(config)
	require.Error(t, err)
	require.Contains(t, err.Error(), "conflicts with an enabled channel model id")

	config.Bindings[0].Enabled = false
	require.NoError(t, ValidateCreativeModelBindingsConfig(config))
}

func setupCreativeCapabilityServiceTestDB(t *testing.T) {
	t.Helper()
	originalDB := model.DB
	originalUsingSQLite := common.UsingSQLite
	originalUsingMySQL := common.UsingMySQL
	originalUsingPostgreSQL := common.UsingPostgreSQL
	originalRedisEnabled := common.RedisEnabled

	common.UsingSQLite = true
	common.UsingMySQL = false
	common.UsingPostgreSQL = false
	common.RedisEnabled = false

	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}))

	t.Cleanup(func() {
		sqlDB, err := db.DB()
		if err == nil {
			_ = sqlDB.Close()
		}
		model.DB = originalDB
		common.UsingSQLite = originalUsingSQLite
		common.UsingMySQL = originalUsingMySQL
		common.UsingPostgreSQL = originalUsingPostgreSQL
		common.RedisEnabled = originalRedisEnabled
	})
}

func TestBuildCreativeModelBindingsDryRunIsMockOnlyAndRedactsUnsafePreviewFields(t *testing.T) {
	config := validCreativeModelBindingsConfigForTest()

	result, err := BuildCreativeModelBindingsDryRun(config)
	require.NoError(t, err)
	require.True(t, result.NoProviderCall)
	require.Len(t, result.Bindings, 1)
	require.Equal(t, "mock", result.Bindings[0].RequestPreview["transport"])
	require.Equal(t, "gpt-image-2", result.Bindings[0].RequestPreview["model"])

	redacted := RedactCreativeDryRunValue(map[string]any{
		"Authorization": "Bearer sk-test",
		"baseURL":       "https://provider.example",
		"model":         "https://provider.example/model?X-Amz-Signature=secret",
		"artifact":      creativeFakeSecretCorpusForTest(),
		"body": map[string]any{
			"callbackUrl": "https://evil.example/cb",
			"size":        "1024x1024",
		},
	}).(map[string]any)
	require.Equal(t, "[REDACTED]", redacted["Authorization"])
	require.Equal(t, "[REDACTED]", redacted["baseURL"])
	require.Equal(t, "[REDACTED]", redacted["model"])
	for _, artifact := range redacted["artifact"].([]any) {
		require.Equal(t, "[REDACTED]", artifact)
	}
	body := redacted["body"].(map[string]any)
	require.Equal(t, "[REDACTED]", body["callbackUrl"])
	require.Equal(t, "1024x1024", body["size"])
}

func TestBuildCreativeModelBindingsDryRunSupportsGrsAIFixtureWithoutProviderMaterial(t *testing.T) {
	config := grsAIGPTImageDryRunConfigForTest()

	result, err := BuildCreativeModelBindingsDryRun(config)
	require.NoError(t, err)
	require.True(t, result.NoProviderCall)
	require.Len(t, result.Bindings, 1)

	preview := result.Bindings[0].RequestPreview
	require.Equal(t, "fixture", preview["transport"])
	require.Equal(t, "grsai", preview["adapterFamily"])
	require.Equal(t, true, preview["offline"])
	require.Equal(t, "gpt-image-2", preview["model"])
	body := preview["requestBody"].(map[string]any)
	require.Equal(t, "gpt-image-2", body["model"])
	require.Equal(t, "1024x1024", body["aspectRatio"])
	require.Equal(t, "json", body["replyType"])
	require.NotContains(t, body, "images")
	require.NotContains(t, fmtAnyForTest(body), "<managed-input-image-ref>")
	require.NotContains(t, fmtAnyForTest(preview), "authorization")
	require.NotContains(t, fmtAnyForTest(preview), "bearer")
	require.NotContains(t, fmtAnyForTest(preview), "http://")
	require.NotContains(t, fmtAnyForTest(preview), "https://")
	require.NotContains(t, fmtAnyForTest(preview), "baseurl")
}

func TestBuildCreativeModelBindingsDryRunMirrorsGrsAIGPTImageLiveMapping(t *testing.T) {
	setupCreativeCapabilityServiceTestDB(t)
	baseURL := "https://grsai.example"
	channelID := 7108
	require.NoError(t, model.DB.Create(&model.Channel{
		Id:      channelID,
		Key:     "test-key",
		Status:  common.ChannelStatusEnabled,
		Name:    "creative-vip-dryrun",
		Models:  "gpt-image-2-vip",
		Group:   "default",
		BaseURL: &baseURL,
	}).Error)
	config := CreativeModelBindingsConfig{
		Version: 1,
		Bindings: []CreativeModelBindingConfig{{
			Id:                "grsai:gpt-image-2-vip:live",
			ProviderModelId:   "gpt-image-2-vip",
			PriceModelId:      "grsai-gpt-image-2-vip-price",
			DisplayName:       "GrsAI GPT Image 2 VIP",
			Modality:          "image",
			Enabled:           false,
			ChannelId:         &channelID,
			AdapterPreset:     CreativeImageAdapterPresetGrsAILive,
			ParameterTemplate: "grsai_gpt_image_vip",
			ParameterSchema:   creativeGrsAIGPTImageVIPSchemaForTest("16:9", "4K", "high"),
		}},
	}

	result, err := BuildCreativeModelBindingsDryRun(config)
	require.NoError(t, err)
	require.True(t, result.NoProviderCall)
	require.Len(t, result.Bindings, 1)

	body := result.Bindings[0].RequestPreview["requestBody"].(map[string]any)
	require.Equal(t, "gpt-image-2-vip", body["model"])
	require.Equal(t, "3840x2160", body["aspectRatio"])
	require.Equal(t, "async", body["replyType"])
	require.Equal(t, "high", body["quality"])
	require.NotContains(t, body, "imageSize")
}

func TestBuildCreativeModelBindingsDryRunOmitsGrsAIGPTImageAutoAspectRatioLikeLiveAdapter(t *testing.T) {
	setupCreativeCapabilityServiceTestDB(t)
	baseURL := "https://grsai.example"
	channelID := 7111
	require.NoError(t, model.DB.Create(&model.Channel{
		Id:      channelID,
		Key:     "test-key",
		Status:  common.ChannelStatusEnabled,
		Name:    "creative-grs-default",
		Models:  "gpt-image-2-vip",
		Group:   "default",
		BaseURL: &baseURL,
	}).Error)
	config := CreativeModelBindingsConfig{
		Version: 1,
		Bindings: []CreativeModelBindingConfig{{
			Id:                "grsai:gpt-image-2-vip:live",
			ProviderModelId:   "gpt-image-2-vip",
			PriceModelId:      "gpt-image-2-vip",
			DisplayName:       "GrsAI GPT Image 2 VIP",
			Modality:          "image",
			Enabled:           false,
			ChannelId:         &channelID,
			AdapterPreset:     CreativeImageAdapterPresetGrsAILive,
			ParameterTemplate: "grsai_gpt_image_vip",
			ParameterSchema:   creativeGrsAIGPTImageVIPSchemaForTest("auto", "1K", "auto"),
		}},
	}

	result, err := BuildCreativeModelBindingsDryRun(config)
	require.NoError(t, err)
	require.Len(t, result.Bindings, 1)

	body := result.Bindings[0].RequestPreview["requestBody"].(map[string]any)
	require.Equal(t, "gpt-image-2-vip", body["model"])
	require.Equal(t, "async", body["replyType"])
	require.NotContains(t, body, "aspectRatio")
	require.NotContains(t, body, "imageSize")
	require.NotContains(t, body, "quality")
}

func TestBuildCreativeModelBindingsDryRunMirrorsDuomiLiveSizeMapping(t *testing.T) {
	setupCreativeCapabilityServiceTestDB(t)
	baseURL := "https://duomi.example"
	channelID := 7112
	require.NoError(t, model.DB.Create(&model.Channel{
		Id:      channelID,
		Key:     "test-key",
		Status:  common.ChannelStatusEnabled,
		Name:    "creative-duomi-21x9-dryrun",
		Models:  "gpt-image-2",
		Group:   "default",
		BaseURL: &baseURL,
	}).Error)
	config := CreativeModelBindingsConfig{
		Version: 1,
		Bindings: []CreativeModelBindingConfig{{
			Id:                "duomi:gpt-image-2:live",
			ProviderModelId:   "gpt-image-2",
			PriceModelId:      "gpt-image-2",
			DisplayName:       "Duomi GPT Image 2",
			Modality:          "image",
			Enabled:           false,
			ChannelId:         &channelID,
			AdapterPreset:     CreativeImageAdapterPresetDuomiLive,
			ParameterTemplate: "duomi_gpt_image",
			ParameterSchema:   creativeDuomiGPTImageSchemaForTest("21:9", "high"),
		}},
	}

	result, err := BuildCreativeModelBindingsDryRun(config)
	require.NoError(t, err)
	require.Len(t, result.Bindings, 1)

	body := result.Bindings[0].RequestPreview["requestBody"].(map[string]any)
	require.Equal(t, "1792x768", body["size"])
	require.Equal(t, "high", body["quality"])
	require.NotEqual(t, "21:9", body["size"])
	require.NotContains(t, body, "aspectRatio")
	require.NotContains(t, body, "imageSize")
}

func TestBuildCreativeModelBindingsDryRunOmitsDuomiAutoQualityLikeLiveAdapter(t *testing.T) {
	setupCreativeCapabilityServiceTestDB(t)
	baseURL := "https://duomi.example"
	channelID := 7109
	require.NoError(t, model.DB.Create(&model.Channel{
		Id:      channelID,
		Key:     "test-key",
		Status:  common.ChannelStatusEnabled,
		Name:    "creative-duomi-dryrun",
		Models:  "gpt-image-2",
		Group:   "default",
		BaseURL: &baseURL,
	}).Error)
	config := CreativeModelBindingsConfig{
		Version: 1,
		Bindings: []CreativeModelBindingConfig{{
			Id:                "duomi:gpt-image-2:live",
			ProviderModelId:   "gpt-image-2",
			PriceModelId:      "gpt-image-2",
			DisplayName:       "Duomi GPT Image 2",
			Modality:          "image",
			Enabled:           false,
			ChannelId:         &channelID,
			AdapterPreset:     CreativeImageAdapterPresetDuomiLive,
			ParameterTemplate: "duomi_gpt_image",
			ParameterSchema:   creativeDuomiGPTImageSchemaForTest("auto", "auto"),
		}},
	}

	result, err := BuildCreativeModelBindingsDryRun(config)
	require.NoError(t, err)
	require.Len(t, result.Bindings, 1)

	body := result.Bindings[0].RequestPreview["requestBody"].(map[string]any)
	require.NotContains(t, body, "size")
	require.NotContains(t, body, "quality")
	require.NotContains(t, body, "aspectRatio")
	require.NotContains(t, body, "imageSize")
}

func TestParseCreativeGrsAIImageFixtureResponseRedactsProviderResults(t *testing.T) {
	summary, err := ParseCreativeGrsAIImageFixtureResponse([]byte(`{
		"id": "14-fixture-task",
		"status": "succeeded",
		"results": [{"url": "https://provider.example/private/result.png?token=secret"}],
		"progress": 100
	}`))
	require.NoError(t, err)
	require.Equal(t, "14-fixture-task", summary.Id)
	require.Equal(t, "succeeded", summary.Status)
	require.Equal(t, 1, summary.ResultCount)
	require.Equal(t, 100, summary.Progress)
	require.NotContains(t, fmtAnyForTest(summary), "https://")
	require.NotContains(t, fmtAnyForTest(summary), "token=secret")

	wrapperSummary, err := ParseCreativeGrsAIImageFixtureResponse([]byte(`{
		"code": 0,
		"data": {
			"id": "wrapped-fixture-task",
			"status": "succeeded",
			"results": ["https://provider.example/private/wrapped.png?token=secret"],
			"progress": 100,
			"error": "Bearer token https://provider.example/private/error"
		}
	}`))
	require.NoError(t, err)
	require.Equal(t, "wrapped-fixture-task", wrapperSummary.Id)
	require.Equal(t, "succeeded", wrapperSummary.Status)
	require.Equal(t, 1, wrapperSummary.ResultCount)
	require.Equal(t, 100, wrapperSummary.Progress)
	require.Empty(t, wrapperSummary.Error)
	require.NotContains(t, fmtAnyForTest(wrapperSummary), "https://")
	require.NotContains(t, fmtAnyForTest(wrapperSummary), "token")
	require.NotContains(t, fmtAnyForTest(wrapperSummary), "bearer")

	_, err = ParseCreativeGrsAIImageFixtureResponse([]byte(`{"id":"14-fixture-task","status":"queued"}`))
	require.Error(t, err)
	require.NotContains(t, err.Error(), "https://")

	_, err = ParseCreativeGrsAIImageFixtureResponse([]byte(`{"id":"https://provider.example/private/task?token=secret","status":"running"}`))
	require.Error(t, err)
	require.NotContains(t, err.Error(), "provider.example")
	require.NotContains(t, err.Error(), "token=secret")

	_, err = ParseCreativeGrsAIImageFixtureResponse([]byte(`{"id":"14-fixture-task","status":"https://provider.example/private/status?token=secret"}`))
	require.Error(t, err)
	require.NotContains(t, err.Error(), "provider.example")
	require.NotContains(t, err.Error(), "token=secret")

	_, err = ParseCreativeGrsAIImageFixtureResponse([]byte(`{"id":"14-fixture-task","status":"succeeded","results":[]}`))
	require.Error(t, err)

	_, err = ParseCreativeGrsAIImageFixtureResponse([]byte(`{"code":0,"data":{"id":"wrapped-fixture-task","status":"succeeded"}}`))
	require.Error(t, err)
}

func TestBuildCreativeModelBindingsDryRunHasNoProviderTransportReferences(t *testing.T) {
	_, testFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	sourceFile := filepath.Join(filepath.Dir(testFile), "creative_model_capability.go")
	fileSet := token.NewFileSet()
	parsed, err := parser.ParseFile(fileSet, sourceFile, nil, 0)
	require.NoError(t, err)

	dryRunFunctions := map[string]*ast.FuncDecl{}
	for _, declaration := range parsed.Decls {
		fn, ok := declaration.(*ast.FuncDecl)
		if !ok {
			continue
		}
		switch fn.Name.Name {
		case "BuildCreativeModelBindingsDryRun", "creativeModelBindingDryRunRequestPreview", "creativeDryRunSchemaDefault":
			dryRunFunctions[fn.Name.Name] = fn
		}
	}
	require.Len(t, dryRunFunctions, 3)

	forbiddenIdents := map[string]struct{}{
		"http":          {},
		"http2":         {},
		"DefaultClient": {},
		"GetHttpClient": {},
		"RoundTrip":     {},
		"Do":            {},
		"Channel":       {},
		"BaseURL":       {},
		"ApiKey":        {},
		"APIKey":        {},
		"Key":           {},
	}
	forbiddenStringFragments := []string{
		"http://",
		"https://",
		"authorization",
		"api_key",
		"base_url",
		"baseurl",
		"bearer ",
	}

	for functionName, dryRunFunc := range dryRunFunctions {
		ast.Inspect(dryRunFunc.Body, func(node ast.Node) bool {
			switch typed := node.(type) {
			case *ast.Ident:
				if _, forbidden := forbiddenIdents[typed.Name]; forbidden {
					t.Fatalf("%s must remain provider-transport-free, found identifier %q", functionName, typed.Name)
				}
			case *ast.BasicLit:
				for _, fragment := range forbiddenStringFragments {
					if strings.Contains(strings.ToLower(typed.Value), fragment) {
						t.Fatalf("%s must remain provider-transport-free, found literal %s", functionName, typed.Value)
					}
				}
			}
			return true
		})
	}
}

func fmtAnyForTest(value any) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(fmt.Sprintf("%#v", value))), "\\\\", "")
}

func creativeGrsAIGPTImageVIPSchemaForTest(defaultAspectRatio string, defaultImageSize string, defaultQuality string) []dto.CreativeParameterSchemaItem {
	return []dto.CreativeParameterSchemaItem{
		{
			Id:           "aspectRatio",
			Label:        "图片尺寸",
			Type:         "enum",
			DefaultValue: defaultAspectRatio,
			Options: []dto.CreativeParamOption{
				{Value: "auto", Label: "自动"},
				{Value: "1:1", Label: "1:1"},
				{Value: "2:3", Label: "2:3"},
				{Value: "3:2", Label: "3:2"},
				{Value: "3:4", Label: "3:4"},
				{Value: "4:3", Label: "4:3"},
				{Value: "4:5", Label: "4:5"},
				{Value: "5:4", Label: "5:4"},
				{Value: "9:16", Label: "9:16"},
				{Value: "16:9", Label: "16:9"},
				{Value: "21:9", Label: "21:9"},
			},
		},
		{
			Id:           "imageSize",
			Label:        "图片分辨率",
			Type:         "enum",
			DefaultValue: defaultImageSize,
			Options: []dto.CreativeParamOption{
				{Value: "1K", Label: "1K"},
				{Value: "2K", Label: "2K"},
				{Value: "4K", Label: "4K"},
			},
		},
		{
			Id:           "quality",
			Label:        "质量",
			Type:         "enum",
			DefaultValue: defaultQuality,
			Options: []dto.CreativeParamOption{
				{Value: "auto", Label: "自动"},
				{Value: "low", Label: "快速"},
				{Value: "medium", Label: "标准"},
				{Value: "high", Label: "高清"},
			},
		},
	}
}

func creativeDuomiGPTImageSchemaForTest(defaultAspectRatio string, defaultQuality string) []dto.CreativeParameterSchemaItem {
	return []dto.CreativeParameterSchemaItem{
		{
			Id:           "aspectRatio",
			Label:        "图片尺寸",
			Type:         "enum",
			DefaultValue: defaultAspectRatio,
			Options: []dto.CreativeParamOption{
				{Value: "auto", Label: "自动"},
				{Value: "1:1", Label: "1:1"},
				{Value: "2:3", Label: "2:3"},
				{Value: "3:2", Label: "3:2"},
				{Value: "3:4", Label: "3:4"},
				{Value: "4:3", Label: "4:3"},
				{Value: "4:5", Label: "4:5"},
				{Value: "5:4", Label: "5:4"},
				{Value: "9:16", Label: "9:16"},
				{Value: "16:9", Label: "16:9"},
				{Value: "21:9", Label: "21:9"},
			},
		},
		{
			Id:           "imageSize",
			Label:        "图片分辨率",
			Type:         "enum",
			DefaultValue: "1K",
			Options:      []dto.CreativeParamOption{{Value: "1K", Label: "1K"}},
		},
		{
			Id:           "quality",
			Label:        "质量",
			Type:         "enum",
			DefaultValue: defaultQuality,
			Options: []dto.CreativeParamOption{
				{Value: "auto", Label: "自动"},
				{Value: "low", Label: "快速"},
				{Value: "medium", Label: "标准"},
				{Value: "high", Label: "高清"},
			},
		},
	}
}
