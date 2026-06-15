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
