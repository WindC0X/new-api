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
	validateData := creativeResponseData(t, decodeCreativeResponse(t, validate))
	require.Equal(t, true, validateData["valid"])
	require.NotContains(t, validate.Body.String(), "apiKey")
	require.NotContains(t, validate.Body.String(), "baseUrl")

	dryRun := performJSONRequest(t, router, http.MethodPost, "/api/creative/model-bindings/dry-run", validCreativeModelBindingsPayload())
	require.Equal(t, http.StatusOK, dryRun.Code)
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
	putData := creativeResponseData(t, decodeCreativeResponse(t, put))
	require.NotEmpty(t, putData["configJSON"])
	require.Contains(t, logBuffer.String(), "creative model bindings updated")
	require.Contains(t, logBuffer.String(), "user_id=1")
	require.Contains(t, logBuffer.String(), "bindings=1")
	require.NotContains(t, logBuffer.String(), "gpt-image-2")
	require.NotContains(t, logBuffer.String(), "apiKey")

	get := performJSONRequest(t, router, http.MethodGet, "/api/creative/model-bindings", nil)
	require.Equal(t, http.StatusOK, get.Code)
	getData := creativeResponseData(t, decodeCreativeResponse(t, get))
	config := creativeResponseObject(t, getData, "config")
	require.Equal(t, float64(1), config["version"])
	require.Contains(t, get.Body.String(), "mock:gpt-image-2:preview")
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
	require.Contains(t, decodeCreativeResponse(t, accessToken)["message"], "dashboard session")
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

			badNonce := performCreativeModelBindingsRouteRequest(t, router, route.method, route.path, encoded, map[string]string{
				"X-Creative-CSRF":  "csrf-test",
				"X-Creative-Nonce": "wrong",
			})
			require.Equal(t, http.StatusForbidden, badNonce.Code)
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

func bytesReader(data []byte) *strings.Reader {
	return strings.NewReader(string(data))
}
