package service

import (
	"math"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/stretchr/testify/require"
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

func withCreativeAdapterPreviewOptions(t *testing.T, enabled string, canaryGroups string) {
	t.Helper()

	common.OptionMapRWMutex.Lock()
	originalMap := common.OptionMap
	copyMap := make(map[string]string, len(originalMap)+2)
	for key, value := range originalMap {
		copyMap[key] = value
	}
	if enabled == "" {
		delete(copyMap, CreativeAdapterEnabledOptionKey)
	} else {
		copyMap[CreativeAdapterEnabledOptionKey] = enabled
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

func TestCreativeForbiddenKeyNormalizerCoversControlVariants(t *testing.T) {
	for _, key := range []string{
		"notifyHook",
		"notify_hook",
		"notify-hook",
		"headers.Authorization",
		"callback_url",
		"owner-id",
		"sourceProfileId",
		"idempotency:key",
		"base URL",
	} {
		require.True(t, CreativeForbiddenKey(key), key)
	}
	require.Equal(t, "notifyhook", NormalizeCreativeForbiddenKey("notify_hook"))
	require.False(t, CreativeForbiddenKey("size"))
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
			raw:  `{"version":1,"bindings":[{"id":"mock:image:a","providerModelId":"p","priceModelId":"price","modality":"image"},{"id":"MOCK:IMAGE:A","providerModelId":"p","priceModelId":"price","modality":"image"}]}`,
		},
		{
			name: "forbidden binding id",
			raw:  `{"version":1,"bindings":[{"id":"callback:image","providerModelId":"p","priceModelId":"price","modality":"image"}]}`,
		},
		{
			name: "missing provider model",
			raw:  `{"version":1,"bindings":[{"id":"mock:image:a","priceModelId":"price","modality":"image"}]}`,
		},
		{
			name: "forbidden schema id",
			raw:  `{"version":1,"bindings":[{"id":"mock:image:a","providerModelId":"p","priceModelId":"price","modality":"image","parameterSchema":[{"id":"notifyHook","label":"Hook","type":"string"}]}]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseCreativeModelBindingsConfig(tt.raw)
			require.Error(t, err)
		})
	}
}
