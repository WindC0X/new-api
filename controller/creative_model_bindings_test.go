package controller

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func validCreativeModelBindingsPayload() map[string]any {
	return map[string]any{
		"config": map[string]any{
			"version": float64(1),
			"bindings": []any{
				map[string]any{
					"id":                "mock:gpt-image-2:preview",
					"providerModelId":   "gpt-image-2",
					"priceModelId":      "mock-gpt-image-2-price",
					"displayName":       "Mock GPT Image 2",
					"modality":          "image",
					"enabled":           false,
					"canaryGroups":      []any{"test"},
					"adapterPreset":     "mock_image_task",
					"parameterTemplate": "mock_gpt_image",
					"parameterSchema": []any{
						map[string]any{
							"id":           "size",
							"label":        "Size",
							"type":         "enum",
							"defaultValue": "1024x1024",
							"options": []any{
								map[string]any{"value": "1024x1024", "label": "1024×1024"},
							},
						},
					},
				},
			},
		},
	}
}

func TestCreativeModelBindingsAdminValidateDryRunAndPut(t *testing.T) {
	setupCreativeControllerTestDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.Option{}))
	setCreativeModelBindingsOptionForTest(t, "")
	router := newCreativeModelBindingsAdminTestRouter(false)

	validate := performJSONRequest(t, router, http.MethodPost, "/api/creative/model-bindings/validate", validCreativeModelBindingsPayload())
	require.Equal(t, http.StatusOK, validate.Code)
	requireCreativeModelBindingsNoStore(t, validate)
	validateData := creativeResponseData(t, decodeCreativeResponse(t, validate))
	require.Equal(t, true, validateData["valid"])
	require.NotContains(t, validate.Body.String(), "apiKey")
	require.NotContains(t, validate.Body.String(), "baseUrl")

	dryRun := performJSONRequest(t, router, http.MethodPost, "/api/creative/model-bindings/dry-run", validCreativeModelBindingsPayload())
	require.Equal(t, http.StatusOK, dryRun.Code)
	requireCreativeModelBindingsNoStore(t, dryRun)
	dryRunData := creativeResponseData(t, decodeCreativeResponse(t, dryRun))
	require.Equal(t, true, dryRunData["noProviderCall"])
	bindings := creativeResponseArray(t, dryRunData, "bindings")
	require.Len(t, bindings, 1)
	binding := bindings[0].(map[string]any)
	require.Equal(t, "mock:gpt-image-2:preview", binding["id"])
	require.Equal(t, "gpt-image-2", binding["providerModelId"])
	require.NotContains(t, dryRun.Body.String(), "Authorization")
	require.NotContains(t, dryRun.Body.String(), "sk-test")

	var logBuffer bytes.Buffer
	common.LogWriterMu.Lock()
	originalWriter := gin.DefaultWriter
	gin.DefaultWriter = &logBuffer
	common.LogWriterMu.Unlock()
	t.Cleanup(func() {
		common.LogWriterMu.Lock()
		gin.DefaultWriter = originalWriter
		common.LogWriterMu.Unlock()
	})

	put := performJSONRequest(t, router, http.MethodPut, "/api/creative/model-bindings", validCreativeModelBindingsPayload())
	require.Equal(t, http.StatusOK, put.Code)
	requireCreativeModelBindingsNoStore(t, put)
	putData := creativeResponseData(t, decodeCreativeResponse(t, put))
	require.NotEmpty(t, putData["configJSON"])
	require.Contains(t, logBuffer.String(), "creative model bindings updated")
	require.Contains(t, logBuffer.String(), "user_id=1")
	require.Contains(t, logBuffer.String(), "bindings=1")
	require.NotContains(t, logBuffer.String(), "gpt-image-2")
	require.NotContains(t, logBuffer.String(), "apiKey")

	get := performJSONRequest(t, router, http.MethodGet, "/api/creative/model-bindings", nil)
	require.Equal(t, http.StatusOK, get.Code)
	requireCreativeModelBindingsNoStore(t, get)
	getData := creativeResponseData(t, decodeCreativeResponse(t, get))
	config := creativeResponseObject(t, getData, "config")
	require.Equal(t, float64(1), config["version"])
	require.Contains(t, get.Body.String(), "mock:gpt-image-2:preview")
}

func TestCreativeChannelSummariesOmitSensitiveChannelFields(t *testing.T) {
	setupCreativeControllerTestDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.Channel{}))
	router := newCreativeModelBindingsAdminTestRouter(false)

	baseURL := "https://provider-secret.example/v1"
	headerOverride := `{"Authorization":"Bearer sk-test-secret"}`
	paramOverride := `{"apiKey":"sk-param-secret"}`
	setting := `{"proxy":"http://proxy-secret.example"}`
	remark := "private remark"
	require.NoError(t, model.DB.Create(&model.Channel{
		Id:                 42,
		Type:               1,
		Key:                "sk-live-secret",
		Status:             common.ChannelStatusEnabled,
		Name:               "Fixture Channel",
		Group:              "test",
		Models:             "gpt-image-2,grs-image",
		BaseURL:            &baseURL,
		HeaderOverride:     &headerOverride,
		ParamOverride:      &paramOverride,
		Setting:            &setting,
		Other:              `{"credential":"hidden"}`,
		OtherSettings:      `{"token":"hidden"}`,
		OtherInfo:          "private other info",
		ModelMapping:       common.GetPointer(`{"gpt-image-2":"upstream-secret-model"}`),
		Remark:             &remark,
		OpenAIOrganization: common.GetPointer("org-secret"),
	}).Error)

	recorder := performJSONRequest(t, router, http.MethodGet, "/api/creative/channel-summaries?channel_id=42", nil)
	require.Equal(t, http.StatusOK, recorder.Code)
	requireCreativeModelBindingsNoStore(t, recorder)
	raw := recorder.Body.String()
	for _, forbidden := range []string{
		"sk-live-secret",
		"provider-secret",
		"header_override",
		"param_override",
		"base_url",
		"settings",
		"other_info",
		"model_mapping",
		"remark",
		"openai_organization",
		"Authorization",
		"credential",
		"proxy-secret",
		"org-secret",
	} {
		require.NotContains(t, raw, forbidden)
	}
	data := creativeResponseData(t, decodeCreativeResponse(t, recorder))
	items := creativeResponseArray(t, data, "items")
	require.Len(t, items, 1)
	item := items[0].(map[string]any)
	require.Equal(t, float64(42), item["id"])
	require.Equal(t, "Fixture Channel", item["name"])
	require.Equal(t, "test", item["group"])
	require.Equal(t, float64(common.ChannelStatusEnabled), item["status"])
	require.Equal(t, []any{"gpt-image-2", "grs-image"}, item["models"])
}

func TestCreativeModelBindingsAdminRejectsUnsafeAndAccessToken(t *testing.T) {
	router := newCreativeModelBindingsAdminTestRouter(false)
	unsafePayload := validCreativeModelBindingsPayload()
	config := unsafePayload["config"].(map[string]any)
	bindings := config["bindings"].([]any)
	binding := bindings[0].(map[string]any)
	binding["parameterSchema"] = []any{map[string]any{"id": "callback", "label": "Hook", "type": "string"}}

	unsafe := performJSONRequest(t, router, http.MethodPost, "/api/creative/model-bindings/validate", unsafePayload)
	require.Equal(t, http.StatusBadRequest, unsafe.Code)
	require.Contains(t, decodeCreativeResponse(t, unsafe)["message"], "forbidden")

	accessTokenRouter := newCreativeModelBindingsAdminTestRouter(true)
	accessToken := performJSONRequest(t, accessTokenRouter, http.MethodPost, "/api/creative/model-bindings/validate", validCreativeModelBindingsPayload())
	require.Equal(t, http.StatusForbidden, accessToken.Code)
	requireCreativeModelBindingsNoStore(t, accessToken)
	require.Contains(t, decodeCreativeResponse(t, accessToken)["message"], "dashboard session")
}

func TestCreativeModelBindingsAdminRejectsFakeSecretCorpusWithoutLogging(t *testing.T) {
	router := newCreativeModelBindingsAdminTestRouter(false)
	secrets := []string{
		"Bearer sk-test-secret",
		"sk-test-secret",
		"https://provider.example/v1/images?X-Amz-Signature=abc&X-Amz-Credential=credential",
		"data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAAB",
		"cookie=session-secret",
		"csrf=csrf-secret",
		"nonce=nonce-secret",
		"object_key=private/object.png",
		"token=secret",
	}
	var logBuffer bytes.Buffer
	common.LogWriterMu.Lock()
	originalWriter := gin.DefaultWriter
	gin.DefaultWriter = &logBuffer
	common.LogWriterMu.Unlock()
	t.Cleanup(func() {
		common.LogWriterMu.Lock()
		gin.DefaultWriter = originalWriter
		common.LogWriterMu.Unlock()
	})

	for _, secret := range secrets {
		t.Run(secret, func(t *testing.T) {
			payload := validCreativeModelBindingsPayload()
			config := payload["config"].(map[string]any)
			bindings := config["bindings"].([]any)
			binding := bindings[0].(map[string]any)
			binding["providerModelId"] = secret

			validate := performJSONRequest(t, router, http.MethodPost, "/api/creative/model-bindings/validate", payload)
			require.Equal(t, http.StatusBadRequest, validate.Code)
			dryRun := performJSONRequest(t, router, http.MethodPost, "/api/creative/model-bindings/dry-run", payload)
			require.Equal(t, http.StatusBadRequest, dryRun.Code)
			for _, surface := range []string{validate.Body.String(), dryRun.Body.String(), logBuffer.String()} {
				require.NotContains(t, surface, secret)
			}
		})
	}
}

func TestCreativeModelBindingsAdminRouteRequiresNonce(t *testing.T) {
	router := newCreativeModelBindingsRouteTestRouter(t, common.RoleRootUser)
	encoded, err := common.Marshal(validCreativeModelBindingsPayload())
	require.NoError(t, err)

	for _, route := range []struct {
		method string
		path   string
	}{
		{method: http.MethodPost, path: "/api/creative/model-bindings/validate"},
		{method: http.MethodPost, path: "/api/creative/model-bindings/dry-run"},
		{method: http.MethodPut, path: "/api/creative/model-bindings"},
	} {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			missingNonce := performCreativeModelBindingsRouteRequest(t, router, route.method, route.path, encoded, nil)
			require.Equal(t, http.StatusForbidden, missingNonce.Code)
			requireCreativeModelBindingsNoStore(t, missingNonce)

			badNonce := performCreativeModelBindingsRouteRequest(t, router, route.method, route.path, encoded, map[string]string{
				"X-Creative-CSRF":  "csrf-test",
				"X-Creative-Nonce": "wrong",
			})
			require.Equal(t, http.StatusForbidden, badNonce.Code)
			requireCreativeModelBindingsNoStore(t, badNonce)
		})
	}

	ok := performCreativeModelBindingsRouteRequest(t, router, http.MethodPost, "/api/creative/model-bindings/validate", encoded, map[string]string{
		"X-Creative-CSRF":  "csrf-test",
		"X-Creative-Nonce": "nonce-test",
	})
	require.Equal(t, http.StatusOK, ok.Code)
}

func TestCreativeModelBindingsAdminRouteRejectsNonRoot(t *testing.T) {
	router := newCreativeModelBindingsRouteTestRouter(t, common.RoleAdminUser)
	encoded, err := common.Marshal(validCreativeModelBindingsPayload())
	require.NoError(t, err)

	recorder := performCreativeModelBindingsRouteRequest(t, router, http.MethodPost, "/api/creative/model-bindings/validate", encoded, map[string]string{
		"X-Creative-CSRF":  "csrf-test",
		"X-Creative-Nonce": "nonce-test",
	})
	require.Equal(t, false, decodeCreativeResponse(t, recorder)["success"])
	requireCreativeModelBindingsNoStore(t, recorder)
}

func newCreativeModelBindingsRouteTestRouter(t *testing.T, role int) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(sessions.Sessions("session", cookie.NewStore([]byte("creative-bindings-test"))))
	router.Use(func(c *gin.Context) {
		session := sessions.Default(c)
		session.Set("username", "root")
		session.Set("role", role)
		session.Set("id", 1)
		session.Set("status", common.UserStatusEnabled)
		session.Set("group", "default")
		session.Set("creative_csrf_token", "csrf-test")
		session.Set("creative_nonce", "nonce-test")
		require.NoError(t, session.Save())
		c.Next()
	})
	group := router.Group("/api/creative")
	group.Use(middleware.DisableCache())
	group.Use(middleware.RootAuth())
	group.PUT("/model-bindings", middleware.CreativeRequireNonce(), UpdateCreativeModelBindings)
	group.POST("/model-bindings/validate", middleware.CreativeRequireNonce(), ValidateCreativeModelBindings)
	group.POST("/model-bindings/dry-run", middleware.CreativeRequireNonce(), DryRunCreativeModelBindings)
	return router
}

func performCreativeModelBindingsRouteRequest(t *testing.T, router *gin.Engine, method string, path string, encoded []byte, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, bytesReader(encoded))
	request.Host = "example.test"
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("New-Api-User", "1")
	request.Header.Set("Origin", "http://example.test")
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

func newCreativeModelBindingsAdminTestRouter(useAccessToken bool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		if useAccessToken {
			c.Set("use_access_token", true)
		}
		c.Set("id", 1)
		c.Next()
	})
	router.GET("/api/creative/model-bindings", GetCreativeModelBindings)
	router.PUT("/api/creative/model-bindings", UpdateCreativeModelBindings)
	router.POST("/api/creative/model-bindings/validate", ValidateCreativeModelBindings)
	router.POST("/api/creative/model-bindings/dry-run", DryRunCreativeModelBindings)
	router.GET("/api/creative/channel-summaries", GetCreativeChannelSummaries)
	return router
}

func setCreativeModelBindingsOptionForTest(t *testing.T, value string) {
	t.Helper()
	common.OptionMapRWMutex.Lock()
	if common.OptionMap == nil {
		common.OptionMap = map[string]string{}
	}
	previous, hadPrevious := common.OptionMap[service.CreativeModelBindingsOptionKey]
	common.OptionMap[service.CreativeModelBindingsOptionKey] = value
	common.OptionMapRWMutex.Unlock()

	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		defer common.OptionMapRWMutex.Unlock()
		if hadPrevious {
			common.OptionMap[service.CreativeModelBindingsOptionKey] = previous
		} else {
			delete(common.OptionMap, service.CreativeModelBindingsOptionKey)
		}
	})
}

func requireCreativeModelBindingsNoStore(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()
	cacheControl := recorder.Header().Get("Cache-Control")
	require.Contains(t, cacheControl, "private")
	require.Contains(t, cacheControl, "no-store")
	require.Equal(t, "no-cache", recorder.Header().Get("Pragma"))
}

func bytesReader(data []byte) *strings.Reader {
	return strings.NewReader(string(data))
}
