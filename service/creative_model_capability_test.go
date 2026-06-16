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

func TestResolveCreativeImageModelBindingForGroupIsMockOnlyAndGroupScoped(t *testing.T) {
	config := validCreativeModelBindingsConfigForTest()
	config.Bindings[0].Enabled = true
	config.Bindings[0].CanaryGroups = []string{"test"}
	configJSON, err := NormalizeCreativeModelBindingsConfigJSON(config)
	require.NoError(t, err)
	withCreativeCapabilityOptions(t, map[string]string{
		CreativeAdapterEnabledOptionKey: "true",
		CreativeModelBindingsOptionKey:  configJSON,
	})

	resolved, err := ResolveCreativeImageModelBindingForGroup("mock:gpt-image-2:preview", "test", map[string]any{"size": "1024x1024"})
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
	_, err = ResolveCreativeImageModelBindingForGroup("mock:gpt-image-2:preview", "test", map[string]any{"size": "1024x1024"})
	require.Error(t, err)
}

func TestStoredCreativeModelBindingsCatalogHonorsKillSwitchesAndHidesHiddenSchema(t *testing.T) {
	config := validCreativeModelBindingsConfigForTest()
	config.Bindings[0].Enabled = true
	config.Bindings[0].CanaryGroups = []string{"test"}
	config.Bindings[0].ParameterSchema = append(config.Bindings[0].ParameterSchema, dto.CreativeParameterSchemaItem{
		Id:     "serverOnly",
		Label:  "Server Only",
		Type:   "string",
		Hidden: true,
	})
	configJSON, err := NormalizeCreativeModelBindingsConfigJSON(config)
	require.NoError(t, err)

	withCreativeCapabilityOptions(t, map[string]string{
		CreativeAdapterEnabledOptionKey: "true",
		CreativeModelBindingsOptionKey:  configJSON,
	})
	items := GetStoredCreativeModelBindingsCatalogForGroup("test")
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
		CreativeAdapterEnabledOptionKey: "true",
		CreativeModelBindingsOptionKey:  disabledJSON,
	})
	require.Empty(t, GetStoredCreativeModelBindingsCatalogForGroup("test"))

	withCreativeCapabilityOptions(t, map[string]string{
		CreativeAdapterEnabledOptionKey: "",
		CreativeModelBindingsOptionKey:  configJSON,
	})
	require.Empty(t, GetStoredCreativeModelBindingsCatalogForGroup("test"))
}

func TestStoredCreativeModelBindingsCatalogHidesFixtureProviderBindings(t *testing.T) {
	config := grsAIGPTImageDryRunConfigForTest()
	config.Bindings[0].Enabled = true
	config.Bindings[0].CanaryGroups = []string{"test"}
	configJSON, err := NormalizeCreativeModelBindingsConfigJSON(config)
	require.NoError(t, err)

	withCreativeCapabilityOptions(t, map[string]string{
		CreativeAdapterEnabledOptionKey: "true",
		CreativeModelBindingsOptionKey:  configJSON,
	})

	require.Empty(t, GetStoredCreativeModelBindingsCatalogForGroup("test"))
	_, err = ResolveCreativeImageModelBindingForGroup(config.Bindings[0].Id, "test", map[string]any{"aspectRatio": "1024x1024"})
	require.Error(t, err)
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
	require.NoError(t, db.AutoMigrate(&model.Channel{}))

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
	require.NotContains(t, fmtAnyForTest(preview), "authorization")
	require.NotContains(t, fmtAnyForTest(preview), "bearer")
	require.NotContains(t, fmtAnyForTest(preview), "http://")
	require.NotContains(t, fmtAnyForTest(preview), "https://")
	require.NotContains(t, fmtAnyForTest(preview), "baseurl")
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

	_, err = ParseCreativeGrsAIImageFixtureResponse([]byte(`{"id":"14-fixture-task","status":"queued"}`))
	require.Error(t, err)
	require.NotContains(t, err.Error(), "https://")

	_, err = ParseCreativeGrsAIImageFixtureResponse([]byte(`{"id":"14-fixture-task","status":"succeeded","results":[]}`))
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
