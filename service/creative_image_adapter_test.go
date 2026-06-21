package service

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/stretchr/testify/require"
)

func TestCreativeImageProviderParsersNormalizeStatusesAndResults(t *testing.T) {
	duomiRunning, err := parseDuomiCreativeImageResult([]byte(`{"id":"dm-task-1","state":"running","progress":12}`), true)
	require.NoError(t, err)
	require.Equal(t, "dm-task-1", duomiRunning.UpstreamTaskID)
	require.Equal(t, string(model.TaskStatusInProgress), string(duomiRunning.Status))
	require.Equal(t, "12%", duomiRunning.Progress)

	duomiSuccess, err := parseDuomiCreativeImageResult([]byte(`{"id":"dm-task-2","state":"succeeded","data":{"images":[{"url":"https://cdn.example/result.png?signature=secret"}]}}`), false)
	require.NoError(t, err)
	require.Equal(t, string(model.TaskStatusSuccess), string(duomiSuccess.Status))
	require.Equal(t, "100%", duomiSuccess.Progress)
	require.Equal(t, "https://cdn.example/result.png?signature=secret", duomiSuccess.ResultURL)

	_, err = parseDuomiCreativeImageResult([]byte(`{"id":"dm-task-3","state":"succeeded","data":{"images":[]}}`), false)
	require.Error(t, err)
	require.True(t, CreativeImageProviderTerminalError(err))
	require.Contains(t, err.Error(), "without result")

	grsViolation, err := parseGrsAICreativeImageResult([]byte(`{"id":"grs-task-1","status":"violation","error":"Bearer sk-test-secret"}`), true)
	require.NoError(t, err)
	require.Equal(t, string(model.TaskStatusFailure), string(grsViolation.Status))
	require.Equal(t, "creative image provider task failed", grsViolation.FailReason)

	_, err = parseGrsAICreativeImageResult([]byte(`{"status":"running"}`), true)
	require.Error(t, err)
	require.Contains(t, err.Error(), "task id")
}

func TestCreativeImageProviderParsersCoverTerminalFailuresAndMalformedResults(t *testing.T) {
	tests := []struct {
		name       string
		parse      func([]byte, bool) (CreativeImageProviderResult, error)
		raw        string
		forSubmit  bool
		wantStatus model.TaskStatus
		wantURL    string
		wantErr    string
	}{
		{
			name:       "duomi failed sanitizes sensitive error",
			parse:      parseDuomiCreativeImageResult,
			raw:        `{"id":"dm-failed","state":"failed","error":"Bearer sk-sensitive"}`,
			wantStatus: model.TaskStatusFailure,
		},
		{
			name:    "duomi malformed json",
			parse:   parseDuomiCreativeImageResult,
			raw:     `{`,
			wantErr: "invalid creative image provider response",
		},
		{
			name:      "duomi missing id",
			parse:     parseDuomiCreativeImageResult,
			raw:       `{"state":"running"}`,
			forSubmit: true,
			wantErr:   "task id",
		},
		{
			name:       "grsai succeeded",
			parse:      parseGrsAICreativeImageResult,
			raw:        `{"id":"grs-ok","status":"succeeded","results":[{"url":"https://cdn.example/grs.png?token=secret"}]}`,
			wantStatus: model.TaskStatusSuccess,
			wantURL:    "https://cdn.example/grs.png?token=secret",
		},
		{
			name:    "grsai succeeded missing result",
			parse:   parseGrsAICreativeImageResult,
			raw:     `{"id":"grs-empty","status":"succeeded","results":[]}`,
			wantErr: "without result",
		},
		{
			name:       "grsai failed sanitizes sensitive error",
			parse:      parseGrsAICreativeImageResult,
			raw:        `{"id":"grs-failed","status":"failed","error":"https://provider.example/callback?token=secret"}`,
			wantStatus: model.TaskStatusFailure,
		},
		{
			name:    "grsai malformed json",
			parse:   parseGrsAICreativeImageResult,
			raw:     `{`,
			wantErr: "invalid creative image provider response",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := tt.parse([]byte(tt.raw), tt.forSubmit)
			if tt.wantErr != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, string(tt.wantStatus), string(result.Status))
			if tt.wantURL != "" {
				require.Equal(t, tt.wantURL, result.ResultURL)
			}
			require.NotContains(t, result.FailReason, "sk-sensitive")
			require.NotContains(t, result.FailReason, "token=secret")
			require.NotContains(t, result.FailReason, "provider.example")
		})
	}
}

func TestCreativeImageProviderAdaptersMapHTTPContracts(t *testing.T) {
	previousClient := httpClient
	defer func() { httpClient = previousClient }()

	var seen []string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.RequestURI()+" "+r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.RequestURI() {
		case "/v1/images/generations?async=true":
			require.Equal(t, "duomi-key", r.Header.Get("Authorization"))
			body := readRequestBodyForTest(t, r)
			require.Contains(t, body, `"quality":"high"`)
			require.Contains(t, body, `"size":"1792x768"`)
			require.NotContains(t, body, `"size":"21:9"`)
			_, _ = w.Write([]byte(`{"id":"dm-http-1","state":"running","progress":9}`))
		case "/v1/tasks/dm-http-1":
			require.Equal(t, "duomi-key", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"id":"dm-http-1","state":"succeeded","data":{"images":[{"url":"https://cdn.example/dm.png"}]}}`))
		case "/v1/api/generate":
			require.Equal(t, "Bearer grs-key", r.Header.Get("Authorization"))
			body := readRequestBodyForTest(t, r)
			require.Contains(t, body, `"replyType":"async"`)
			require.Contains(t, body, `"aspectRatio":"3:4"`)
			require.Contains(t, body, `"imageSize":"1K"`)
			_, _ = w.Write([]byte(`{"id":"grs-http-1","status":"running","progress":11}`))
		case "/v1/api/result?id=grs-http-1":
			require.Equal(t, "Bearer grs-key", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"id":"grs-http-1","status":"succeeded","results":[{"url":"https://cdn.example/grs.png"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer provider.Close()
	httpClient = provider.Client()

	duomiSubmit, err := SubmitCreativeImageProviderTask(t.Context(), CreativeImageProviderRequest{
		AdapterPreset:   CreativeImageAdapterPresetDuomiLive,
		Endpoint:        provider.URL,
		Credential:      "duomi-key",
		ProviderModelID: "gpt-image-2",
		Prompt:          "safe prompt",
		UserParams:      map[string]any{"aspectRatio": "21:9", "imageSize": "1K", "quality": "high"},
	})
	require.NoError(t, err)
	require.Equal(t, "dm-http-1", duomiSubmit.UpstreamTaskID)
	duomiPoll, err := PollCreativeImageProviderTask(t.Context(), CreativeImageProviderRequest{
		AdapterPreset:   CreativeImageAdapterPresetDuomiLive,
		Endpoint:        provider.URL,
		Credential:      "duomi-key",
		ProviderModelID: "gpt-image-2",
	}, duomiSubmit.UpstreamTaskID)
	require.NoError(t, err)
	require.Equal(t, string(model.TaskStatusSuccess), string(duomiPoll.Status))
	require.Equal(t, "https://cdn.example/dm.png", duomiPoll.ResultURL)

	grsSubmit, err := SubmitCreativeImageProviderTask(t.Context(), CreativeImageProviderRequest{
		AdapterPreset:   CreativeImageAdapterPresetGrsAILive,
		Endpoint:        provider.URL,
		Credential:      "grs-key",
		ProviderModelID: "nano-banana-pro",
		Prompt:          "safe prompt",
		UserParams:      map[string]any{"aspectRatio": "3:4", "imageSize": "1K"},
	})
	require.NoError(t, err)
	require.Equal(t, "grs-http-1", grsSubmit.UpstreamTaskID)
	grsPoll, err := PollCreativeImageProviderTask(t.Context(), CreativeImageProviderRequest{
		AdapterPreset:   CreativeImageAdapterPresetGrsAILive,
		Endpoint:        provider.URL,
		Credential:      "grs-key",
		ProviderModelID: "nano-banana-pro",
	}, grsSubmit.UpstreamTaskID)
	require.NoError(t, err)
	require.Equal(t, string(model.TaskStatusSuccess), string(grsPoll.Status))
	require.Equal(t, "https://cdn.example/grs.png", grsPoll.ResultURL)
	require.Len(t, seen, 4)
}

func TestCreativeGrsAIGPTImageMapsUiAspectAndResolutionToPixelAspectRatio(t *testing.T) {
	previousClient := httpClient
	defer func() { httpClient = previousClient }()

	var bodies []string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/api/generate", r.URL.RequestURI())
		bodies = append(bodies, readRequestBodyForTest(t, r))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"grs-map","status":"running","progress":1}`))
	}))
	defer provider.Close()
	httpClient = provider.Client()

	_, err := SubmitCreativeImageProviderTask(t.Context(), CreativeImageProviderRequest{
		AdapterPreset:   CreativeImageAdapterPresetGrsAILive,
		Endpoint:        provider.URL,
		Credential:      "grs-key",
		ProviderModelID: "gpt-image-2",
		Prompt:          "safe prompt",
		UserParams:      map[string]any{"aspectRatio": "3:4", "imageSize": "1K"},
	})
	require.NoError(t, err)
	require.Contains(t, bodies[0], `"aspectRatio":"1090x1443"`)
	require.NotContains(t, bodies[0], `"imageSize"`)

	_, err = SubmitCreativeImageProviderTask(t.Context(), CreativeImageProviderRequest{
		AdapterPreset:   CreativeImageAdapterPresetGrsAILive,
		Endpoint:        provider.URL,
		Credential:      "grs-key",
		ProviderModelID: "gpt-image-2-vip",
		Prompt:          "safe prompt",
		UserParams:      map[string]any{"aspectRatio": "16:9", "imageSize": "4K", "quality": "high"},
	})
	require.NoError(t, err)
	require.Contains(t, bodies[1], `"aspectRatio":"3840x2160"`)
	require.Contains(t, bodies[1], `"quality":"high"`)
	require.NotContains(t, bodies[1], `"imageSize"`)
}

func TestCreativeImageProviderAdaptersOmitMissingOptionalParams(t *testing.T) {
	previousClient := httpClient
	defer func() { httpClient = previousClient }()

	var bodies []string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodies = append(bodies, readRequestBodyForTest(t, r))
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/images/generations":
			_, _ = w.Write([]byte(`{"id":"dm-default","state":"running","progress":1}`))
		case "/v1/api/generate":
			_, _ = w.Write([]byte(`{"id":"grs-default","status":"running","progress":1}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer provider.Close()
	httpClient = provider.Client()

	_, err := SubmitCreativeImageProviderTask(t.Context(), CreativeImageProviderRequest{
		AdapterPreset:   CreativeImageAdapterPresetDuomiLive,
		Endpoint:        provider.URL,
		Credential:      "duomi-key",
		ProviderModelID: "gpt-image-2",
		Prompt:          "safe prompt",
		UserParams:      map[string]any{},
	})
	require.NoError(t, err)
	require.NotContains(t, bodies[0], "<nil>")
	require.NotContains(t, bodies[0], `"size"`)
	require.NotContains(t, bodies[0], `"quality"`)

	_, err = SubmitCreativeImageProviderTask(t.Context(), CreativeImageProviderRequest{
		AdapterPreset:   CreativeImageAdapterPresetGrsAILive,
		Endpoint:        provider.URL,
		Credential:      "grs-key",
		ProviderModelID: "gpt-image-2-vip",
		Prompt:          "safe prompt",
		UserParams:      map[string]any{"aspectRatio": "16:9"},
	})
	require.NoError(t, err)
	require.NotContains(t, bodies[1], "<nil>")
	require.Contains(t, bodies[1], `"aspectRatio":"1280x720"`)
	require.NotContains(t, bodies[1], `"imageSize"`)
}

func TestFetchCreativeImageProviderContentRejectsSVG(t *testing.T) {
	fetchSetting := system_setting.GetFetchSetting()
	previousSSRFProtection := fetchSetting.EnableSSRFProtection
	fetchSetting.EnableSSRFProtection = false
	t.Cleanup(func() { fetchSetting.EnableSSRFProtection = previousSSRFProtection })

	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml")
		_, _ = w.Write([]byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`))
	}))
	defer provider.Close()

	_, err := FetchCreativeImageProviderContent(t.Context(), provider.URL+"/result.svg")
	require.Error(t, err)
	require.Contains(t, err.Error(), "content type")
}

func readRequestBodyForTest(t *testing.T, r *http.Request) string {
	t.Helper()
	raw, err := io.ReadAll(r.Body)
	require.NoError(t, err)
	return string(raw)
}

func creativeDuomiGPTImageSchemaForAdapterTest(defaultAspectRatio string, defaultQuality string) []dto.CreativeParameterSchemaItem {
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

func TestCreativeLiveBindingValidationCatalogAndResolver(t *testing.T) {
	setupCreativeCapabilityServiceTestDB(t)
	baseURL := "https://duomi.example"
	require.NoError(t, model.DB.Create(&model.Channel{
		Id:      7101,
		Key:     "test-key",
		Status:  common.ChannelStatusEnabled,
		Name:    "creative-live-test",
		Models:  "gpt-image-2",
		Group:   "default",
		BaseURL: &baseURL,
	}).Error)

	channelID := 7101
	config := CreativeModelBindingsConfig{
		Version: 1,
		Bindings: []CreativeModelBindingConfig{{
			Id:                "duomi:gpt-image-2:live",
			ProviderModelId:   "gpt-image-2",
			PriceModelId:      "duomi-gpt-image-2-price",
			DisplayName:       "Duomi GPT Image 2",
			Modality:          "image",
			Enabled:           true,
			CanaryGroups:      []string{"*"},
			ChannelId:         &channelID,
			AdapterPreset:     CreativeImageAdapterPresetDuomiLive,
			ParameterTemplate: "duomi_gpt_image",
			ParameterSchema:   creativeDuomiGPTImageSchemaForAdapterTest("1:1", "medium"),
		}},
	}
	require.NoError(t, ValidateCreativeModelBindingsConfig(config))
	configJSON, err := NormalizeCreativeModelBindingsConfigJSON(config)
	require.NoError(t, err)
	withCreativeCapabilityOptions(t, map[string]string{
		CreativeAdapterEnabledOptionKey:        "true",
		CreativeMockImageTasksEnabledOptionKey: "false",
		CreativeModelBindingsOptionKey:         configJSON,
	})

	catalog := GetStoredCreativeModelBindingsCatalogForGroup("default")
	require.Len(t, catalog, 1)
	require.Equal(t, "duomi:gpt-image-2:live", catalog[0].Id)
	require.Equal(t, "gpt-image-2", catalog[0].ProviderModelId)
	require.NotContains(t, catalog[0].Tags, "channel")
	require.NotContains(t, catalog[0].Tags, "mock")
	require.Contains(t, catalog[0].Tags, "live")
	require.Contains(t, catalog[0].Tags, "duomi")
	require.NotEmpty(t, catalog[0].ParameterSchema)

	resolved, err := ResolveCreativeImageModelBindingForGroup("duomi:gpt-image-2:live", "default", map[string]any{
		"aspectRatio": "1:1",
		"imageSize":   "1K",
		"quality":     "high",
	})
	require.NoError(t, err)
	require.Equal(t, CreativeImageAdapterPresetDuomiLive, resolved.AdapterPreset)
	require.Equal(t, channelID, resolved.ChannelId)
	require.Equal(t, map[string]any{"aspectRatio": "1:1", "imageSize": "1K", "quality": "high"}, resolved.UserParams)

	dryRun, err := BuildCreativeModelBindingsDryRun(config)
	require.NoError(t, err)
	require.True(t, dryRun.NoProviderCall)
	require.Equal(t, "live", dryRun.Bindings[0].RequestPreview["transport"])
	require.Equal(t, true, dryRun.Bindings[0].RequestPreview["offline"])
}

func TestCreativeLiveBindingRejectsSchemaFieldsNotMappedByAdapter(t *testing.T) {
	setupCreativeCapabilityServiceTestDB(t)
	baseURL := "https://grsai.example"
	require.NoError(t, model.DB.Create(&model.Channel{
		Id:      7102,
		Key:     "test-key",
		Status:  common.ChannelStatusEnabled,
		Name:    "creative-live-test",
		Models:  "gpt-image-2",
		Group:   "default",
		BaseURL: &baseURL,
	}).Error)
	channelID := 7102
	config := CreativeModelBindingsConfig{
		Version: 1,
		Bindings: []CreativeModelBindingConfig{{
			Id:                "grsai:gpt-image-2:live",
			ProviderModelId:   "gpt-image-2",
			PriceModelId:      "grsai-gpt-image-2-price",
			DisplayName:       "GrsAI GPT Image 2",
			Modality:          "image",
			Enabled:           true,
			CanaryGroups:      []string{"*"},
			ChannelId:         &channelID,
			AdapterPreset:     CreativeImageAdapterPresetGrsAILive,
			ParameterTemplate: "grsai_gpt_image",
			ParameterSchema: []dto.CreativeParameterSchemaItem{
				{Id: "aspectRatio", Label: "比例", Type: "enum", DefaultValue: "1:1", Options: []dto.CreativeParamOption{{Value: "1:1", Label: "1:1"}}},
				{Id: "replyType", Label: "Reply Type", Type: "enum", DefaultValue: "sync", Options: []dto.CreativeParamOption{{Value: "sync", Label: "sync"}}},
			},
		}},
	}
	err := ValidateCreativeModelBindingsConfig(config)
	require.Error(t, err)
	require.Contains(t, err.Error(), "replyType")
	require.Contains(t, err.Error(), "not supported")
}

func TestCreativeGrsAIGPTImageVIPUsesAspectRatioAndResolutionTemplate(t *testing.T) {
	setupCreativeCapabilityServiceTestDB(t)
	baseURL := "https://grsai.example"
	require.NoError(t, model.DB.Create(&model.Channel{
		Id:      7106,
		Key:     "test-key",
		Status:  common.ChannelStatusEnabled,
		Name:    "creative-vip-schema",
		Models:  "gpt-image-2-vip",
		Group:   "default",
		BaseURL: &baseURL,
	}).Error)
	channelID := 7106
	config := CreativeModelBindingsConfig{
		Version: 1,
		Bindings: []CreativeModelBindingConfig{{
			Id:                "grsai:gpt-image-2-vip:live",
			ProviderModelId:   "gpt-image-2-vip",
			PriceModelId:      "grsai-gpt-image-2-vip-price",
			DisplayName:       "GrsAI GPT Image 2 VIP",
			Modality:          "image",
			Enabled:           true,
			CanaryGroups:      []string{"*"},
			ChannelId:         &channelID,
			AdapterPreset:     CreativeImageAdapterPresetGrsAILive,
			ParameterTemplate: "grsai_gpt_image",
			ParameterSchema: []dto.CreativeParameterSchemaItem{
				creativeGrsAIGPTImageVIPSchemaForTest("auto", "1K", "auto")[0],
			},
		}},
	}

	err := ValidateCreativeModelBindingsConfig(config)
	require.Error(t, err)
	require.Contains(t, err.Error(), "providerModelId")

	config.Bindings[0].ParameterTemplate = "grsai_gpt_image_vip"
	err = ValidateCreativeModelBindingsConfig(config)
	require.Error(t, err)
	require.Contains(t, err.Error(), "requires parameterSchema field")

	config.Bindings[0].ParameterSchema = []dto.CreativeParameterSchemaItem{
		{Id: "aspectRatio", Label: "图片尺寸", Type: "enum", DefaultValue: "auto", Options: []dto.CreativeParamOption{{Value: "auto", Label: "自动"}, {Value: "1:1", Label: "1:1"}, {Value: "2:3", Label: "2:3"}, {Value: "3:2", Label: "3:2"}, {Value: "3:4", Label: "3:4"}, {Value: "4:3", Label: "4:3"}, {Value: "4:5", Label: "4:5"}, {Value: "5:4", Label: "5:4"}, {Value: "9:16", Label: "9:16"}, {Value: "16:9", Label: "16:9"}, {Value: "21:9", Label: "21:9"}}},
		{Id: "imageSize", Label: "图片分辨率", Type: "enum", DefaultValue: "8K", Options: []dto.CreativeParamOption{{Value: "8K", Label: "8K"}}},
		{Id: "quality", Label: "质量", Type: "enum", DefaultValue: "auto", Options: []dto.CreativeParamOption{{Value: "auto", Label: "自动"}, {Value: "low", Label: "快速"}, {Value: "medium", Label: "标准"}, {Value: "high", Label: "高清"}}},
	}
	err = ValidateCreativeModelBindingsConfig(config)
	require.Error(t, err)
	require.Contains(t, err.Error(), "imageSize")

	config.Bindings[0].ParameterSchema[1] = dto.CreativeParameterSchemaItem{
		Id:           "imageSize",
		Label:        "图片分辨率",
		Type:         "enum",
		DefaultValue: "1K",
		Options:      []dto.CreativeParamOption{{Value: "1K", Label: "1K"}, {Value: "2K", Label: "2K"}, {Value: "4K", Label: "4K"}},
	}
	require.NoError(t, ValidateCreativeModelBindingsConfig(config))
}

func TestCreativeLiveBindingRequiresUsableChannelKeyAndExplicitBaseURL(t *testing.T) {
	setupCreativeCapabilityServiceTestDB(t)
	baseURL := "https://duomi.example"
	require.NoError(t, model.DB.Create(&model.Channel{
		Id:      7103,
		Key:     "",
		Status:  common.ChannelStatusEnabled,
		Name:    "creative-live-missing-key",
		Models:  "gpt-image-2",
		Group:   "default",
		BaseURL: &baseURL,
	}).Error)

	channelID := 7103
	config := CreativeModelBindingsConfig{
		Version: 1,
		Bindings: []CreativeModelBindingConfig{{
			Id:                "duomi:gpt-image-2:live",
			ProviderModelId:   "gpt-image-2",
			PriceModelId:      "duomi-gpt-image-2-price",
			DisplayName:       "Duomi GPT Image 2",
			Modality:          "image",
			Enabled:           true,
			CanaryGroups:      []string{"*"},
			ChannelId:         &channelID,
			AdapterPreset:     CreativeImageAdapterPresetDuomiLive,
			ParameterTemplate: "duomi_gpt_image",
			ParameterSchema:   creativeDuomiGPTImageSchemaForAdapterTest("1:1", "auto"),
		}},
	}

	err := ValidateCreativeModelBindingsConfig(config)
	require.Error(t, err)
	require.Contains(t, err.Error(), "available channel key")
	configJSON, jsonErr := NormalizeCreativeModelBindingsConfigJSON(config)
	require.Error(t, jsonErr)
	require.Empty(t, configJSON)
	rawConfigJSON, marshalErr := common.Marshal(config)
	require.NoError(t, marshalErr)
	withCreativeCapabilityOptions(t, map[string]string{
		CreativeAdapterEnabledOptionKey: "true",
		CreativeModelBindingsOptionKey:  string(rawConfigJSON),
	})
	require.Empty(t, GetStoredCreativeModelBindingsCatalogForGroup("default"))

	require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", 7103).Update("key", "test-key").Error)
	emptyBaseURL := ""
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", 7103).Update("base_url", &emptyBaseURL).Error)

	err = ValidateCreativeModelBindingsConfig(config)
	require.Error(t, err)
	require.Contains(t, err.Error(), "explicit baseURL")
	configJSON, jsonErr = NormalizeCreativeModelBindingsConfigJSON(config)
	require.Error(t, jsonErr)
	require.Empty(t, configJSON)
	withCreativeCapabilityOptions(t, map[string]string{
		CreativeAdapterEnabledOptionKey: "true",
		CreativeModelBindingsOptionKey:  string(rawConfigJSON),
	})
	require.Empty(t, GetStoredCreativeModelBindingsCatalogForGroup("default"))

	require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", 7103).Update("base_url", &baseURL).Error)
	require.NoError(t, ValidateCreativeModelBindingsConfig(config))
	configJSON, jsonErr = NormalizeCreativeModelBindingsConfigJSON(config)
	require.NoError(t, jsonErr)
	withCreativeCapabilityOptions(t, map[string]string{
		CreativeAdapterEnabledOptionKey: "true",
		CreativeModelBindingsOptionKey:  configJSON,
	})
	require.Len(t, GetStoredCreativeModelBindingsCatalogForGroup("default"), 1)
}

func TestStoredCreativeModelBindingsCatalogSkipsDriftedLiveBindingWithoutHidingValidBindings(t *testing.T) {
	setupCreativeCapabilityServiceTestDB(t)
	config := CreativeModelBindingsConfig{
		Version: 1,
		Bindings: []CreativeModelBindingConfig{
			{
				Id:                "duomi:gpt-image-2:live",
				ProviderModelId:   "gpt-image-2",
				PriceModelId:      "duomi-gpt-image-2-price",
				DisplayName:       "Duomi GPT Image 2",
				Modality:          "image",
				Enabled:           true,
				CanaryGroups:      []string{"*"},
				ChannelId:         common.GetPointer(404),
				AdapterPreset:     CreativeImageAdapterPresetDuomiLive,
				ParameterTemplate: "duomi_gpt_image",
				ParameterSchema:   creativeDuomiGPTImageSchemaForAdapterTest("1:1", "auto"),
			},
			{
				Id:                "mock:gpt-image-2:preview",
				ProviderModelId:   "gpt-image-2",
				PriceModelId:      "mock-gpt-image-2-price",
				DisplayName:       "Mock GPT Image 2",
				Modality:          "image",
				Enabled:           true,
				CanaryGroups:      []string{"*"},
				AdapterPreset:     CreativeImageAdapterPresetMock,
				ParameterTemplate: "mock_gpt_image",
				ParameterSchema: []dto.CreativeParameterSchemaItem{
					{Id: "size", Label: "尺寸", Type: "enum", DefaultValue: "1024x1024", Options: []dto.CreativeParamOption{{Value: "1024x1024", Label: "1024×1024"}}},
				},
			},
		},
	}
	rawConfigJSON, err := common.Marshal(config)
	require.NoError(t, err)
	withCreativeCapabilityOptions(t, map[string]string{
		CreativeAdapterEnabledOptionKey:        "true",
		CreativeMockImageTasksEnabledOptionKey: "true",
		CreativeModelBindingsOptionKey:         string(rawConfigJSON),
	})

	catalog := GetStoredCreativeModelBindingsCatalogForGroup("default")
	require.Len(t, catalog, 1)
	require.Equal(t, "mock:gpt-image-2:preview", catalog[0].Id)
	state, err := GetCreativeModelBindingsAdminState()
	require.NoError(t, err)
	require.Len(t, state.Config.Bindings, 2)
}

func TestCreativeLiveBindingReadinessDoesNotAdvanceMultiKeyPollingIndex(t *testing.T) {
	setupCreativeCapabilityServiceTestDB(t)
	baseURL := "https://duomi.example"
	require.NoError(t, model.DB.Create(&model.Channel{
		Id:      7105,
		Key:     "key-a\nkey-b",
		Status:  common.ChannelStatusEnabled,
		Name:    "creative-live-multikey",
		Models:  "gpt-image-2",
		Group:   "default",
		BaseURL: &baseURL,
		ChannelInfo: model.ChannelInfo{
			IsMultiKey:           true,
			MultiKeyMode:         constant.MultiKeyModePolling,
			MultiKeyPollingIndex: 0,
			MultiKeyStatusList: map[int]int{
				0: common.ChannelStatusEnabled,
				1: common.ChannelStatusEnabled,
			},
		},
	}).Error)
	channelID := 7105
	config := CreativeModelBindingsConfig{
		Version: 1,
		Bindings: []CreativeModelBindingConfig{{
			Id:                "duomi:gpt-image-2:live",
			ProviderModelId:   "gpt-image-2",
			PriceModelId:      "duomi-gpt-image-2-price",
			DisplayName:       "Duomi GPT Image 2",
			Modality:          "image",
			Enabled:           true,
			CanaryGroups:      []string{"*"},
			ChannelId:         &channelID,
			AdapterPreset:     CreativeImageAdapterPresetDuomiLive,
			ParameterTemplate: "duomi_gpt_image",
			ParameterSchema:   creativeDuomiGPTImageSchemaForAdapterTest("1:1", "auto"),
		}},
	}
	configJSON, err := NormalizeCreativeModelBindingsConfigJSON(config)
	require.NoError(t, err)
	withCreativeCapabilityOptions(t, map[string]string{
		CreativeAdapterEnabledOptionKey: "true",
		CreativeModelBindingsOptionKey:  configJSON,
	})

	require.Len(t, GetStoredCreativeModelBindingsCatalogForGroup("default"), 1)
	var stored model.Channel
	require.NoError(t, model.DB.Where("id = ?", 7105).First(&stored).Error)
	require.Equal(t, 0, stored.ChannelInfo.MultiKeyPollingIndex)
}

func TestCreativeLiveBindingRejectsEmptyOrAllHiddenSchema(t *testing.T) {
	setupCreativeCapabilityServiceTestDB(t)
	baseURL := "https://grsai.example"
	require.NoError(t, model.DB.Create(&model.Channel{
		Id:      7104,
		Key:     "test-key",
		Status:  common.ChannelStatusEnabled,
		Name:    "creative-live-empty-schema",
		Models:  "gpt-image-2",
		Group:   "default",
		BaseURL: &baseURL,
	}).Error)
	channelID := 7104
	config := CreativeModelBindingsConfig{
		Version: 1,
		Bindings: []CreativeModelBindingConfig{{
			Id:                "grsai:gpt-image-2:live",
			ProviderModelId:   "gpt-image-2",
			PriceModelId:      "grsai-gpt-image-2-price",
			DisplayName:       "GrsAI GPT Image 2",
			Modality:          "image",
			Enabled:           true,
			CanaryGroups:      []string{"*"},
			ChannelId:         &channelID,
			AdapterPreset:     CreativeImageAdapterPresetGrsAILive,
			ParameterTemplate: "grsai_gpt_image",
			ParameterSchema:   []dto.CreativeParameterSchemaItem{},
		}},
	}

	err := ValidateCreativeModelBindingsConfig(config)
	require.Error(t, err)
	require.Contains(t, err.Error(), "parameterSchema")
	require.Contains(t, err.Error(), "required")

	config.Bindings[0].ParameterSchema = []dto.CreativeParameterSchemaItem{{
		Id:           "aspectRatio",
		Label:        "比例",
		Type:         "enum",
		DefaultValue: "1:1",
		Options:      []dto.CreativeParamOption{{Value: "1:1", Label: "1:1"}},
		Hidden:       true,
	}}
	err = ValidateCreativeModelBindingsConfig(config)
	require.Error(t, err)
	require.Contains(t, err.Error(), "visible")
}
